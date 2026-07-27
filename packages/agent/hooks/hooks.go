// Package hooks runs user-configured external programs at tool-call
// boundaries: pre-tool-use hooks that can allow, deny, or rewrite a
// call before the confirm gate sees it, and post-tool-use hooks that
// observe results. Hooks are the zero-protocol extension point — a
// shell script or any executable speaking one JSON object on stdin
// and (for pre) one on stdout — sitting beside the richer extension
// event-intercept protocol.
//
// Trust boundary: hooks always load from the USER config. A project
// (.terva/config.json) MAY also define hooks, but ONLY when the workspace
// is trusted (Workspace Trust Phase 6) — an UNtrusted cloned repo's hooks
// are never loaded, so opening a repo can't hand it arbitrary code
// execution on a tool call. When trusted, the project's hooks are appended
// to the user's (both fire, user first); the gate is enforced by the host
// in buildHookEngine (agent/cli.go), which threads the trust verdict and
// merges trustedProjectHooks. See docs/plans/workspace-trust.md.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/procenv"
)

// Config is the JSON shape under the user config's "hooks" key.
type Config struct {
	PreToolUse  []Spec `json:"pre_tool_use,omitempty"`
	PostToolUse []Spec `json:"post_tool_use,omitempty"`
}

// Spec configures one hook program.
type Spec struct {
	// Command is the executable to run. Resolved via PATH when bare.
	Command string `json:"command"`
	// Args are fixed arguments placed before the JSON-on-stdin event.
	Args []string `json:"args,omitempty"`
	// Tools filters which tool calls fire this hook: exact name or
	// prefix glob ending in '*'. Empty means every tool.
	Tools string `json:"tools,omitempty"`
	// TimeoutMS bounds one invocation. Default 10s (pre) / 30s (post).
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

func (s Spec) matches(tool string) bool {
	switch {
	case s.Tools == "":
		return true
	case strings.HasSuffix(s.Tools, "*"):
		return strings.HasPrefix(tool, strings.TrimSuffix(s.Tools, "*"))
	default:
		return s.Tools == tool
	}
}

func (s Spec) timeout(def time.Duration) time.Duration {
	if s.TimeoutMS > 0 {
		return time.Duration(s.TimeoutMS) * time.Millisecond
	}
	return def
}

// Decision values a pre hook can return.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	DecisionAsk   = "ask"
)

// PreResult is the outcome of the pre-hook chain for one call.
type PreResult struct {
	// Decision is DecisionAllow / DecisionDeny / DecisionAsk, or ""
	// when no hook had an opinion. Allow and deny are final: allow
	// skips the confirm gate, deny refuses the call. Ask and ""
	// fall through to the gate.
	Decision string
	// Reason accompanies a deny (shown to the model).
	Reason string
	// UpdatedArgs, when non-nil, replaces the call's arguments for
	// everything downstream (gate preview, extensions, execution).
	// Rewrites accumulate across the chain.
	UpdatedArgs json.RawMessage
}

// preEvent is the JSON a pre hook reads on stdin.
type preEvent struct {
	Event string          `json:"event"` // "pre_tool_use"
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
	CWD   string          `json:"cwd"`
}

// preResponse is the JSON a pre hook may write on stdout. Empty
// output means no opinion.
type preResponse struct {
	Decision    string          `json:"decision,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	UpdatedArgs json.RawMessage `json:"updated_args,omitempty"`
}

// postEvent is the JSON a post hook reads on stdin. Fire-and-forget:
// stdout is ignored.
type postEvent struct {
	Event   string          `json:"event"` // "post_tool_use"
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	IsError bool            `json:"is_error"`
	CWD     string          `json:"cwd"`
}

// Engine evaluates the configured hooks. A nil *Engine is inert, so
// call sites need no guards.
type Engine struct {
	// specMu guards pre/post, which are REPLACED (never mutated) when the
	// host re-derives them — a Workspace Trust flip changes which project
	// hooks apply, and that must reach a running session rather than waiting
	// for the next launch. Readers snapshot the slice header under RLock and
	// iterate outside it; SetSpecs assigns fresh slices, so an in-flight chain
	// finishes against the set it started with rather than tearing.
	specMu    sync.RWMutex
	pre, post []Spec
	cwd       string
	// logf reports hook misbehavior (timeouts, bad JSON, non-zero
	// exits) somewhere a human can see without breaking the turn.
	logf func(format string, a ...any)

	// closer, if set, owns the logf sink (e.g. the hooks.log file) and
	// is released by Close. Optional — logf may write nowhere closeable.
	closer io.Closer

	// inflight correlates EvToolCall (name+args) with EvToolResult
	// (result) so post hooks see the full call. Tool IDs are unique
	// per turn; entries are popped on result.
	mu       sync.Mutex
	inflight map[string]postEvent
}

// NewEngine builds an engine from config, or nil when no hooks are
// configured. logf may be nil.
func NewEngine(cfg Config, cwd string, logf func(string, ...any)) *Engine {
	// No hooks, no engine: a nil *Engine is a no-op on every method, so a host
	// with nothing configured pays nothing. Right for every caller whose hook
	// set is fixed for the process's lifetime — which is all of them except a
	// host that can flip Workspace Trust while running. See NewStandingEngine.
	if len(cfg.PreToolUse) == 0 && len(cfg.PostToolUse) == 0 {
		return nil
	}
	return NewStandingEngine(cfg, cwd, logf)
}

// NewStandingEngine builds an engine even when cfg is empty.
//
// For a host whose hook set can CHANGE while it runs: a project's hooks are
// trust-gated, so an untrusted repo starts with an empty set that trusting it
// fills in. NewEngine would hand back nil there, and nil is not a thing
// SetSpecs can fill — the emptiness at launch would become permanent, which is
// the bug this exists to avoid. Callers that cannot flip trust should use
// NewEngine and get the cheap nil.
func NewStandingEngine(cfg Config, cwd string, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{
		pre:      cfg.PreToolUse,
		post:     cfg.PostToolUse,
		cwd:      cwd,
		logf:     logf,
		inflight: map[string]postEvent{},
	}
}

// SetCloser attaches a resource (typically the log file backing logf)
// for the engine to release on Close. Optional; pass nil for no-op.
func (e *Engine) SetCloser(c io.Closer) {
	if e != nil {
		e.closer = c
	}
}

// Close releases the engine's attached log resource. Nil-safe (a nil
// *Engine is inert) and idempotent. Long-lived hosts may rely on process
// exit instead; tests must call it so an open log file doesn't block
// temp-dir cleanup on Windows.
func (e *Engine) Close() error {
	if e == nil || e.closer == nil {
		return nil
	}
	c := e.closer
	e.closer = nil
	return c.Close()
}

const (
	defaultPreTimeout  = 10 * time.Second
	defaultPostTimeout = 30 * time.Second
	// hookOutputCap bounds what we read back from a hook; a hook
	// that floods stdout is broken, not authoritative.
	hookOutputCap = 1 << 20
)

// SetSpecs replaces the hook chains with cfg's, atomically from a caller's
// point of view. A nil cfg clears them.
//
// This exists so trust can be LIVE. Project hooks are trust-gated, and the
// engine used to be built once at launch with the verdict of that moment, so
// trusting a repo mid-session left its hooks inert until restart — the one
// reader the trust census could not call live. Replacing the specs rather than
// the Engine keeps the log sink and the in-flight correlator intact, and avoids
// closing a file another goroutine is mid-write on.
func (e *Engine) SetSpecs(cfg *Config) {
	if e == nil {
		return
	}
	e.specMu.Lock()
	defer e.specMu.Unlock()
	if cfg == nil {
		e.pre, e.post = nil, nil
		return
	}
	e.pre = append([]Spec(nil), cfg.PreToolUse...)
	e.post = append([]Spec(nil), cfg.PostToolUse...)
}

func (e *Engine) preSpecs() []Spec {
	e.specMu.RLock()
	defer e.specMu.RUnlock()
	return e.pre
}

func (e *Engine) postSpecs() []Spec {
	e.specMu.RLock()
	defer e.specMu.RUnlock()
	return e.post
}

// RunPre runs the pre-tool-use chain in config order. The first
// decisive answer (allow / deny) stops the chain; ask and no-opinion
// continue. Argument rewrites accumulate. Hook failures (timeout,
// bad JSON, unresolvable command) are logged and treated as no
// opinion — with one deliberate exception: exit status 2 is a deny,
// the shell-ergonomic spelling (`exit 2`) every harness converged on.
func (e *Engine) RunPre(ctx context.Context, tool string, args json.RawMessage) PreResult {
	if e == nil {
		return PreResult{}
	}
	res := PreResult{}
	cur := args
	for i, s := range e.preSpecs() {
		if !s.matches(tool) {
			continue
		}
		payload, _ := json.Marshal(preEvent{Event: "pre_tool_use", Tool: tool, Args: cur, CWD: e.cwd})
		stdout, stderr, exitCode, err := e.runOne(ctx, s, payload, s.timeout(defaultPreTimeout))
		if err != nil {
			e.logf("hook %d (%s): %v — treated as no opinion", i+1, s.Command, err)
			continue
		}
		if exitCode == 2 {
			reason := strings.TrimSpace(stderr)
			if reason == "" {
				reason = fmt.Sprintf("denied by pre-tool-use hook %s", s.Command)
			}
			res.Decision = DecisionDeny
			res.Reason = reason
			return res
		}
		if exitCode != 0 {
			e.logf("hook %d (%s): exit %d — treated as no opinion", i+1, s.Command, exitCode)
			continue
		}
		out := strings.TrimSpace(stdout)
		if out == "" {
			continue
		}
		var pr preResponse
		if jerr := json.Unmarshal([]byte(out), &pr); jerr != nil {
			e.logf("hook %d (%s): bad response JSON (%v) — treated as no opinion", i+1, s.Command, jerr)
			continue
		}
		if len(pr.UpdatedArgs) > 0 && string(pr.UpdatedArgs) != "null" {
			if !json.Valid(pr.UpdatedArgs) {
				e.logf("hook %d (%s): updated_args is not valid JSON — ignored", i+1, s.Command)
			} else {
				cur = pr.UpdatedArgs
				res.UpdatedArgs = cur
			}
		}
		switch pr.Decision {
		case DecisionDeny:
			res.Decision = DecisionDeny
			res.Reason = pr.Reason
			if res.Reason == "" {
				res.Reason = fmt.Sprintf("denied by pre-tool-use hook %s", s.Command)
			}
			return res
		case DecisionAllow:
			res.Decision = DecisionAllow
			return res
		case DecisionAsk:
			res.Decision = DecisionAsk
		case "":
		default:
			e.logf("hook %d (%s): unknown decision %q — treated as no opinion", i+1, s.Command, pr.Decision)
		}
	}
	return res
}

// Observe feeds agent events into the post-hook correlator. Call it
// from the OnEvent fanout with the two tool events; everything else
// is ignored. Safe on a nil engine and for concurrent use.
func (e *Engine) Observe(eventType, id, tool string, args json.RawMessage, isError bool) {
	if e == nil || len(e.postSpecs()) == 0 {
		return
	}
	switch eventType {
	case "tool_call":
		e.mu.Lock()
		e.inflight[id] = postEvent{Event: "post_tool_use", Tool: tool, Args: args, CWD: e.cwd}
		e.mu.Unlock()
	case "tool_result":
		e.mu.Lock()
		ev, ok := e.inflight[id]
		delete(e.inflight, id)
		e.mu.Unlock()
		if !ok {
			return
		}
		ev.IsError = isError
		go e.runPost(ev)
	}
}

func (e *Engine) runPost(ev postEvent) {
	payload, _ := json.Marshal(ev)
	for i, s := range e.postSpecs() {
		if !s.matches(ev.Tool) {
			continue
		}
		if _, _, code, err := e.runOne(context.Background(), s, payload, s.timeout(defaultPostTimeout)); err != nil {
			e.logf("post hook %d (%s): %v", i+1, s.Command, err)
		} else if code != 0 {
			e.logf("post hook %d (%s): exit %d", i+1, s.Command, code)
		}
	}
}

// runOne executes a single hook with the payload on stdin and the
// B7-sanitized environment, returning captured output and exit code.
func (e *Engine) runOne(ctx context.Context, s Spec, payload []byte, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, s.Command, s.Args...)
	cmd.Dir = e.cwd
	cmd.Env = procenv.Inherited()
	cmd.Stdin = bytes.NewReader(payload)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &capWriter{w: &outBuf, n: hookOutputCap}
	cmd.Stderr = &capWriter{w: &errBuf, n: hookOutputCap}
	runErr := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return "", "", 0, fmt.Errorf("timed out after %s", timeout)
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode(), nil
	}
	if runErr != nil {
		return "", "", 0, runErr
	}
	return outBuf.String(), errBuf.String(), 0, nil
}

// capWriter discards bytes past n so a runaway hook cannot balloon
// memory.
type capWriter struct {
	w *bytes.Buffer
	n int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.w.Len() < c.n {
		room := c.n - c.w.Len()
		if len(p) > room {
			c.w.Write(p[:room])
		} else {
			c.w.Write(p)
		}
	}
	return len(p), nil
}
