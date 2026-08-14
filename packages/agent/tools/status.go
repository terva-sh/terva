package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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

	// Swarm, when set, lets the status line report spend by sub-agents that are
	// still running — which the session's delegated total cannot include, since
	// a child is booked against the parent only once its recap flushes. Nil
	// wherever swarm is not wired (bot mode, a sub-agent's own status), and the
	// line is simply omitted.
	Swarm *swarm.Swarm

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

	// SetExtensions binds this; see the setter. Guarded by mu because the
	// merge that supplies it can re-run mid-session (a live approval-mode
	// switch rebuilds the registry and re-merges) while Execute reads it.
	extensions func() []ExtensionIdentity
}

// ExtensionIdentity is one loaded extension as terva_status reports it.
//
// Declared here rather than reusing extdriver's richer diagnostic type so this
// package keeps no dependency on the extension machinery: the host passes a
// closure at merge time. It also keeps the reported surface deliberately
// narrow — name and version are what a model can act on; readiness, log paths
// and diagnostics belong to `terva ext doctor`.
type ExtensionIdentity struct {
	Name    string
	Version string
}

// SetExtensions binds the source of loaded-extension identities. Called from
// the extension merge, which is the one point every surface (cli, acp, rpc,
// web) already funnels through — so no surface can wire tools and forget this.
// Re-callable: the merge is idempotent and may re-run.
func (t *StatusTool) SetExtensions(fn func() []ExtensionIdentity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.extensions = fn
}

func (t *StatusTool) extensionList() []ExtensionIdentity {
	t.mu.RLock()
	fn := t.extensions
	t.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
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

// The description used to invite the model to poll this tool for context: "Use
// this tool to decide if you must summarize or stop before the context becomes
// full." That sentence sat in the always-on tools array, so it rode 100% of
// requests — and the session review that rebuilt the context-pressure note
// recorded the behaviour it produced, a model "spending turns polling
// terva_status". The note itself was fixed then; this half of the same symptom
// was left standing.
//
// The replacement names the POSITIVE trigger before the prohibition, and that
// order is load-bearing. A flat "do not call this to check your context" also
// suppresses the call when the user asks outright, which is a measured scenario
// (status-context-usage in scripts/eval/scenarios.json). Naming "the user asks"
// first keeps that route open and closes only the unprompted one.
func (t *StatusTool) Description() string {
	return i18n.D("tool.terva_status.description", "Report your own status. Call this tool when the user asks about your runtime state. Do not call it to watch your own context, because terva warns you when the window fills. The report gives the model, the provider, and the version, commit, and build date of the terva binary. It also gives the loaded extensions and their versions, the run time of the process, the working directory, the session id, the transcript file, the reasoning effort, and the quantity of your context window that is in use. This tool takes no arguments.\n\nUse it also to report which build and which extension versions serve this session. Give these values in each record that you write, so that a later reader can compare them with the release.")
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
	// The extensions loaded into THIS session, with their versions. For an
	// agent whose tool surface beyond files and shell is supplied by
	// extensions, "what am I running" is only half answered by the binary — and
	// the half that changes most often was the missing one. Naming them also
	// makes anything the agent writes checkable later: a review headed with the
	// extension version it ran against can be re-read against what shipped.
	exts := t.extensionList()
	if len(exts) > 0 {
		fmt.Fprintf(&sb, "extensions: %s\n", fmtExtensions(exts))
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
		// Delegated spend, named separately. It is already inside the totals
		// above — a subset, not an addition — but a single merged figure hides
		// which is which, and the delegated part is the one that can dwarf the
		// rest: one measured run spent $24.49 through sub-agents against $5.36
		// of its own turns.
		if del := agent.DelegatedCost(); del.CostUSD > 0 {
			fmt.Fprintf(&sb, "  of which delegated to sub-agents: $%.4f", del.CostUSD)
			if del.InputTokens > 0 || del.OutputTokens > 0 {
				fmt.Fprintf(&sb, " (%s in / %s out)", fmtTokens(del.InputTokens+del.CacheReadTokens+del.CacheWriteTokens), fmtTokens(del.OutputTokens))
			}
			sb.WriteByte('\n')
		}
	}
	// Spend by sub-agents STILL RUNNING, which the delegated figure above cannot
	// include: a child's spend is booked against this session only when its recap
	// flushes. Without this line, "of which delegated" silently understates for
	// as long as a swarm is in flight — which is exactly the window in which a
	// coordinator is deciding whether to spawn more.
	if line := liveDelegatedLine(t.Swarm); line != "" {
		sb.WriteString("  ")
		sb.WriteString(line)
		sb.WriteByte('\n')
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
			// A subset of cumulative, not an addition — see the text line.
			"delegated": func() provider.Usage {
				if haveAgent {
					return agent.DelegatedCost()
				}
				return provider.Usage{}
			}(),
			// NOT a subset of cumulative: sub-agents still running have not been
			// booked against this session yet, so this is spend that exists and
			// is nowhere in the figures above.
			"delegated_in_flight": func() provider.Usage {
				u, _ := t.Swarm.InFlightSpend()
				return u
			}(),
			// Unclipped, unlike the text line: a session record is read by
			// tools, and the reason to persist this at all is so a claim made
			// during the session can be checked against what was loaded.
			"extensions": exts,
		},
	}, nil
}

// extListMax bounds the text line. A fleet can load a dozen extensions, and
// this is one fact among fifteen — the names past this point are a `terva ext`
// away, and the full set still rides in Details for anything reading the
// session record.
const extListMax = 8

// fmtExtensions renders the loaded set as one line: "jmap-mail v0.14.0,
// terva-git-worktree v0.3.1". A version the manifest never declared reads as
// "(no version)" rather than an empty gap, because "I could not tell you" and
// "it has none" are the same fact to a caller and both are worth seeing.
func fmtExtensions(exts []ExtensionIdentity) string {
	parts := make([]string, 0, len(exts))
	for i, e := range exts {
		if i == extListMax {
			parts = append(parts, fmt.Sprintf("+%d more", len(exts)-extListMax))
			break
		}
		v := strings.TrimSpace(e.Version)
		if v == "" {
			v = "(no version)"
		} else if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		parts = append(parts, e.Name+" "+v)
	}
	return strings.Join(parts, ", ")
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
