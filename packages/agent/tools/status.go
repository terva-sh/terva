package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// StatusTool lets the agent query its own runtime status: the model and
// provider it's running as, the working directory, the reasoning effort,
// and how much of the context window the conversation has consumed.
//
// None of this is otherwise visible to the model — the system prompt
// carries only the date and cwd, and live context usage is computed by
// the harness after each turn, never surfaced to the model. Exposing it
// lets the model check "how full is my context?" and decide whether to
// wrap up or summarize before it gets force-compacted.
//
// CWD is captured when the tool is built. The provider-identity facts
// (Provider, AuthMethod, BaseURL) start captured too but are also
// re-bound by SetProvider: a cross-provider model swap that keeps the
// same agent + registry rather than rebuilding them (the web/ctrlproto
// path — Workspace.switchModel — and any other SetClientAndModel host)
// must push the new provider in, or the report would keep naming the
// launch-time provider and the stale name would break the
// context-window lookup below. The TUI/RPC /model path instead rebuilds
// the whole registry, minting a fresh tool with the new facts.
//
// Live facts (current model, reasoning, token usage, session identity)
// are read from the CALLING *core.Agent at call time — resolved from
// the dispatch context first (core.AgentFromContext), so the report is
// correct even when several live agents share this tool through one
// registry (bot mode mints an agent per chat) — and they stay correct
// across same-provider /model swaps (which mutate the agent in place).
type StatusTool struct {
	// mu guards the provider-identity fields against a concurrent
	// SetProvider (a swap can land mid-turn, while Execute is reading
	// them on the turn goroutine). CWD/Agent are set at construction and
	// not re-bound at runtime, so they sit outside the lock.
	mu         sync.RWMutex
	Provider   string
	CWD        string
	AuthMethod string // "apikey" | "oauth" | ""
	BaseURL    string // non-empty only for custom / self-hosted endpoints

	// Agent is the fallback conversation this tool reports on when the
	// dispatch context carries no agent (direct Execute calls, tests).
	// Bound after the agent is constructed (see Resolved.NewAgent) and
	// REBOUND by every subsequent construction from the same registry,
	// so under multiple live agents it points at the most recently
	// minted one — which is why the context lookup wins. When both are
	// nil, the tool reports the static facts and marks live usage
	// unavailable.
	Agent *core.Agent

	// Build is the running binary's build identity (version, commit,
	// build timestamp). Immutable for the process lifetime, so it's set
	// once at construction (from buildinfo.Get) and never re-bound — no
	// lock, unlike the provider-identity fields. A zero Build (an SDK
	// embedder that never recorded it, a direct-construction test) is
	// reported as unavailable rather than as empty fields.
	Build buildinfo.Info
}

// SetProvider re-binds the provider-identity facts after a live
// cross-provider model swap that keeps this tool's agent and registry
// (SetClientAndModel) rather than rebuilding them. Without it,
// terva_status keeps reporting the launch-time provider, auth method,
// and endpoint after such a swap — and the stale provider name silently
// breaks the context-window lookup, because FindModel(oldProvider,
// newModel) misses and the window reads as unknown.
func (t *StatusTool) SetProvider(provider, authMethod, baseURL string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Provider = provider
	t.AuthMethod = authMethod
	t.BaseURL = baseURL
}

func (t *StatusTool) Name() string { return "terva_status" }

func (t *StatusTool) Description() string {
	return "Report your own runtime status: model, provider, the running terva binary's version/commit/build-date, process uptime, working directory, session id and transcript file, reasoning effort, and how full your context window is. Takes no arguments. Useful for deciding whether to summarize or wrap up before the context fills, and for reporting which build is serving this session."
}

// No arguments. Providers that require an object schema accept an
// empty-properties object.
func (t *StatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *StatusTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	// The dispatching agent (from ctx) is authoritative; the bound
	// field is the fallback for direct calls — see the struct comment.
	agent := core.AgentFromContext(ctx)
	if agent == nil {
		agent = t.Agent
	}
	var (
		model, reasoning string
		sessID, sessPath string
		cum, last        provider.Usage
		haveAgent        = agent != nil
	)
	if haveAgent {
		model = agent.Model
		reasoning = agent.Reasoning
		sessID, sessPath = agent.SessionIdentity()
		cum = agent.Cost()
		last = agent.LastTurnUsage()
	}

	// Snapshot the provider-identity facts under the lock SetProvider
	// writes with: a web/ctrlproto cross-provider swap can re-bind them
	// mid-turn, concurrent with this Execute.
	t.mu.RLock()
	provName, authMethod, baseURL := t.Provider, t.AuthMethod, t.BaseURL
	t.mu.RUnlock()

	var sb strings.Builder

	prov := provName
	if prov == "" {
		prov = "(unknown)"
	}
	fmt.Fprintf(&sb, "provider: %s\n", prov)
	if model != "" {
		fmt.Fprintf(&sb, "model: %s\n", model)
	}
	switch authMethod {
	case "oauth":
		sb.WriteString("auth: subscription (oauth)\n")
	case "apikey":
		sb.WriteString("auth: api key\n")
	}
	if baseURL != "" {
		fmt.Fprintf(&sb, "base url: %s\n", baseURL)
	}
	// The running process's build identity — what actually serves this
	// session, which may differ from whatever `terva --version` in a
	// shell reports. Immutable, so it's whatever was recorded at startup.
	if line := fmtBuild(t.Build); line != "" {
		fmt.Fprintf(&sb, "version: %s\n", line)
	}
	// Uptime is read at call time (not construction): with the version
	// line it confirms a self-restart really replaced the process —
	// same build or not, the uptime resets.
	uptime := time.Since(buildinfo.Started()).Round(time.Second)
	fmt.Fprintf(&sb, "uptime: %s\n", uptime)
	if reasoning != "" {
		fmt.Fprintf(&sb, "reasoning effort: %s\n", reasoning)
	}
	cwd := t.CWD
	if cwd == "" {
		cwd = "."
	}
	fmt.Fprintf(&sb, "cwd: %s\n", cwd)
	// The project key: what project-scoped extension state and swarm
	// coordination key on for this cwd.
	projectID := core.ProjectKey(cwd)
	fmt.Fprintf(&sb, "project: %s\n", projectID)

	// Session identity: which transcript file this conversation
	// persists to (the id is what --resume accepts). Key for debugging
	// headless runs, where nothing else surfaces it.
	switch {
	case sessID != "":
		fmt.Fprintf(&sb, "session: %s\n", sessID)
		fmt.Fprintf(&sb, "session file: %s\n", sessPath)
	case haveAgent:
		sb.WriteString("session: none (live-only conversation; not persisted)\n")
	}

	// Context-window usage. The window is the model's effective working
	// window (EffectiveContextWindow — the desired working window when set
	// below the model max, else the max), so this percentage matches the
	// status-bar gauge and the auto-compaction threshold. models.json
	// overrides are reflected; usage is the most recent completed turn's
	// input.
	ctxWindow := 0
	modelMax := 0
	if model != "" {
		if m, err := provider.FindModel(provName, model); err == nil {
			ctxWindow = m.EffectiveContextWindow()
			modelMax = m.ContextWindow
		}
	}
	used := last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
	switch {
	case !haveAgent:
		sb.WriteString("context: live usage unavailable (no live agent)\n")
	case ctxWindow > 0 && used > 0:
		fmt.Fprintf(&sb, "context: %s / %s tokens (%.1f%% of window), as of the last turn",
			fmtTokens(used), fmtTokens(ctxWindow), float64(used)/float64(ctxWindow)*100)
		if modelMax > ctxWindow {
			fmt.Fprintf(&sb, " (working window; model max %s)", fmtTokens(modelMax))
		}
		sb.WriteString("\n")
	case ctxWindow > 0:
		fmt.Fprintf(&sb, "context: window %s tokens; no turn has completed yet\n", fmtTokens(ctxWindow))
	case used > 0:
		fmt.Fprintf(&sb, "context: %s tokens used; window size unknown for this model\n", fmtTokens(used))
	default:
		sb.WriteString("context: usage not yet known\n")
	}

	// Cumulative session usage, when a turn has run.
	if haveAgent {
		totalIn := cum.InputTokens + cum.CacheReadTokens + cum.CacheWriteTokens
		if totalIn > 0 || cum.OutputTokens > 0 {
			fmt.Fprintf(&sb, "session totals: %s in / %s out", fmtTokens(totalIn), fmtTokens(cum.OutputTokens))
			if cum.CostUSD > 0 {
				fmt.Fprintf(&sb, ", $%.4f", cum.CostUSD)
			}
			sb.WriteByte('\n')
		}
	}

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		Details: map[string]any{
			"provider":       provName,
			"model":          model,
			"version":        t.Build.Version,
			"commit":         t.Build.Commit,
			"build_date":     t.Build.Date,
			"uptime_seconds": int(uptime.Seconds()),
			"cwd":            cwd,
			"project_id":     projectID,
			"session_id":     sessID,
			"session_path":   sessPath,
			"reasoning":      reasoning,
			"context_window": ctxWindow,
			"context_used":   used,
			"cumulative":     cum,
		},
	}, nil
}

// fmtBuild renders the build identity as one line, e.g.
// "0.120.1 (commit 8f5bd80, built 2026-07-12T06:30:32Z)". The commit is
// abbreviated to 7 chars for the text line (the full hash rides in
// Details); commit and date are each omitted when absent. Returns "" for
// a zero Info (no version recorded) so the caller drops the line
// entirely.
func fmtBuild(b buildinfo.Info) string {
	if b.Version == "" {
		return ""
	}
	s := b.Version
	if b.Commit != "" {
		short := b.Commit
		if len(short) > 7 {
			short = short[:7]
		}
		s += " (commit " + short
		if b.Date != "" {
			s += ", built " + b.Date
		}
		s += ")"
	} else if b.Date != "" {
		s += " (built " + b.Date + ")"
	}
	return s
}

// fmtTokens renders a token count compactly: 850, 12.3k, 1.2M.
func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
