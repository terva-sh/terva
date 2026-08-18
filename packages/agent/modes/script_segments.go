package modes

// User-defined status-line script segments. Each configured script
// runs through the platform shell with a JSON session snapshot on
// stdin; its first stdout line becomes a status-bar atom. The runner
// follows the git prober's shape — its own goroutine, coalescing
// pokes, never blocking the render loop — with a deliberate anti-
// runaway posture: no free-running poll (triggers are turn end, /cd,
// and the minute boundary), a hard per-run timeout, and sequential
// execution so there is never more than one child at a time.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// StatusScript is one user-defined status-line script — the modes-side
// mirror of the agent config's StatusLineScript (modes cannot import
// the agent package).
type StatusScript struct {
	Command string
	Timeout time.Duration // 0 = default; clamped to [100ms, 10s]
}

const (
	scriptSegmentDebounce       = 300 * time.Millisecond
	scriptSegmentDefaultTimeout = 2 * time.Second
	scriptSegmentMinTimeout     = 100 * time.Millisecond
	scriptSegmentMaxTimeout     = 10 * time.Second
)

// errScriptTimeout marks a run that hit its deadline: transient, keep
// the previous output rather than flapping the segment off.
var errScriptTimeout = errors.New("status script timed out")

// scriptExec runs one script command with the payload on stdin; a
// field seam so tests can drive the runner without real processes.
type scriptExec func(ctx context.Context, script StatusScript, stdin []byte) (string, error)

func execStatusScript(ctx context.Context, script StatusScript, stdin []byte) (string, error) {
	timeout := script.Timeout
	if timeout <= 0 {
		timeout = scriptSegmentDefaultTimeout
	}
	timeout = min(max(timeout, scriptSegmentMinTimeout), scriptSegmentMaxTimeout)
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	cmd := exec.CommandContext(tctx, shell, flag, script.Command)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil && tctx.Err() != nil {
		return "", errScriptTimeout
	}
	return string(out), err
}

// statusScriptPayload is the JSON contract scripts receive on stdin.
// Additive and versioned via Schema: fields may be added, never
// renamed or removed. Documented in docs/tui.md.
type statusScriptPayload struct {
	Schema       int                  `json:"schema"`
	Version      string               `json:"version,omitempty"`
	CWD          string               `json:"cwd"`
	Provider     string               `json:"provider"`
	Model        string               `json:"model"`
	Reasoning    string               `json:"reasoning,omitempty"`
	Experience   string               `json:"experience,omitempty"`
	SessionPath  string               `json:"session_path,omitempty"`
	SessionName  string               `json:"session_name,omitempty"`
	PersonaName  string               `json:"persona_name,omitempty"`
	Subscription bool                 `json:"subscription"`
	CostUSD      float64              `json:"cost_usd"`
	RunCostUSD   float64              `json:"run_cost_usd"` // spend since this run's epoch
	Tokens       payloadTokens        `json:"tokens"`
	ContextUsed  int                  `json:"context_used"`
	ContextMax   int                  `json:"context_max"`
	Usage        []payloadUsageWindow `json:"usage_windows,omitempty"`
	Git          *payloadGit          `json:"git,omitempty"`
	SwarmAgents  int                  `json:"swarm_agents"`
	EditsAdded   int                  `json:"edits_added"`
	EditsRemoved int                  `json:"edits_removed"`
	Cols         int                  `json:"cols"`
}

type payloadTokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

type payloadUsageWindow struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

type payloadGit struct {
	Branch  string `json:"branch"`
	Dirty   bool   `json:"dirty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

func (i *Interactive) buildScriptPayload() []byte {
	i.mu.Lock()
	p := statusScriptPayload{
		Schema:       1,
		Version:      i.cfg.Version,
		CWD:          i.cfg.CWD,
		Provider:     i.cfg.Provider,
		Model:        i.cfg.Model,
		Reasoning:    i.cfg.Reasoning,
		Experience:   i.cfg.Experience,
		PersonaName:  i.cfg.PersonaName,
		Subscription: i.cfg.AuthMethod == "oauth",
		CostUSD:      i.cumUsage.CostUSD,
		RunCostUSD:   i.cumUsage.CostUSD - i.costBase,
		Tokens: payloadTokens{
			Input:      i.cumUsage.InputTokens,
			Output:     i.cumUsage.OutputTokens,
			CacheRead:  i.cumUsage.CacheReadTokens,
			CacheWrite: i.cumUsage.CacheWriteTokens,
		},
		ContextUsed:  i.lastCtxInput,
		EditsAdded:   i.editsAdded,
		EditsRemoved: i.editsRemoved,
	}
	if i.gitInfo.Present {
		p.Git = &payloadGit{
			Branch:  i.gitInfo.Branch,
			Dirty:   i.gitInfo.Dirty,
			Added:   i.gitInfo.Added,
			Removed: i.gitInfo.Removed,
		}
	}
	i.mu.Unlock()

	p.ContextMax = provider.ContextGauge(p.Provider, p.Model)
	for _, w := range i.statusUsageWindows() {
		pw := payloadUsageWindow{Label: w.Label, UsedPercent: w.UsedPercent}
		if !w.ResetsAt.IsZero() {
			pw.ResetsAt = w.ResetsAt.Format(time.RFC3339)
		}
		p.Usage = append(p.Usage, pw)
	}
	p.SwarmAgents = i.swarmAgentCount()
	p.SessionName = i.sessionShortName()
	if i.cfg.CurrentSessionPath != nil {
		p.SessionPath = i.cfg.CurrentSessionPath()
	}
	if i.cfg.Terminal != nil {
		p.Cols = i.lastCols()
	}

	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// runStatusScripts is the runner goroutine: run every configured
// script once at startup, then again after each coalesced poke (turn
// end, /cd, minute boundary) with a short debounce so a burst of
// triggers costs one run.
func (i *Interactive) runStatusScripts(ctx context.Context) {
	if len(i.cfg.StatusScripts) == 0 {
		return
	}
	for {
		i.probeScriptsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-i.scriptPoke:
		}
		timer := time.NewTimer(scriptSegmentDebounce)
	drain:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-i.scriptPoke:
				// coalesce further pokes into this pending run
			case <-timer.C:
				break drain
			}
		}
	}
}

// probeScriptsOnce runs every configured script sequentially and
// stores their outputs, invalidating the frame only on change. A
// timeout keeps the previous output (transient); a real failure
// clears the segment and notes it once per failure streak.
func (i *Interactive) probeScriptsOnce(ctx context.Context) {
	names := make([]string, 0, len(i.cfg.StatusScripts))
	for name := range i.cfg.StatusScripts {
		names = append(names, name)
	}
	sort.Strings(names)

	payload := i.buildScriptPayload()
	run := i.scriptExec
	if run == nil {
		run = execStatusScript
	}

	changed := false
	for _, name := range names {
		out, err := run(ctx, i.cfg.StatusScripts[name], payload)
		if errors.Is(err, errScriptTimeout) {
			continue // transient: keep the previous output
		}
		text := ""
		if err == nil {
			text = firstOutputLine(out)
		}
		i.mu.Lock()
		if err != nil && !i.scriptFailing[name] {
			i.scriptFailing[name] = true
			i.statusOK = i18n.T("status script %s failed", name)
			changed = true
		} else if err == nil && i.scriptFailing[name] {
			i.scriptFailing[name] = false
		}
		if i.scriptSegs[name] != text {
			i.scriptSegs[name] = text
			changed = true
		}
		i.mu.Unlock()
	}
	if changed {
		i.invalidate()
	}
}

// firstOutputLine trims script output to its first non-empty line.
func firstOutputLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimRight(line, "\r"); strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

// pokeStatusScripts requests an out-of-band script run; non-blocking,
// a pending poke already covers it.
func (i *Interactive) pokeStatusScripts() {
	select {
	case i.scriptPoke <- struct{}{}:
	default:
	}
}
