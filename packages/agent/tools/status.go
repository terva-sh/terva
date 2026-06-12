package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
// Static facts (Provider, CWD, AuthMethod, BaseURL) are captured when
// the tool is built. Live facts (current model, reasoning, token usage)
// are read from the bound *core.Agent at call time, so they stay correct
// across both same-provider /model swaps (which mutate the agent in
// place) and cross-provider swaps (which rebuild the agent and this
// tool). Provider can only change via a rebuild, so capturing it is safe.
type StatusTool struct {
	Provider   string
	CWD        string
	AuthMethod string // "apikey" | "oauth" | ""
	BaseURL    string // non-empty only for custom / self-hosted endpoints

	// Agent is the live conversation this tool reports on, bound after
	// the agent is constructed (see Resolved.NewAgent). When nil, the
	// tool reports the static facts and marks live usage unavailable.
	Agent *core.Agent
}

func (t *StatusTool) Name() string { return "terva_status" }

func (t *StatusTool) Description() string {
	return "Report your own runtime status: model, provider, working directory, reasoning effort, and how full your context window is. Takes no arguments. Useful for deciding whether to summarize or wrap up before the context fills."
}

// No arguments. Providers that require an object schema accept an
// empty-properties object.
func (t *StatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *StatusTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var (
		model, reasoning string
		cum, last        provider.Usage
		haveAgent        = t.Agent != nil
	)
	if haveAgent {
		model = t.Agent.Model
		reasoning = t.Agent.Reasoning
		cum = t.Agent.Cost()
		last = t.Agent.LastTurnUsage()
	}

	var sb strings.Builder

	prov := t.Provider
	if prov == "" {
		prov = "(unknown)"
	}
	fmt.Fprintf(&sb, "provider: %s\n", prov)
	if model != "" {
		fmt.Fprintf(&sb, "model: %s\n", model)
	}
	switch t.AuthMethod {
	case "oauth":
		sb.WriteString("auth: subscription (oauth)\n")
	case "apikey":
		sb.WriteString("auth: api key\n")
	}
	if t.BaseURL != "" {
		fmt.Fprintf(&sb, "base url: %s\n", t.BaseURL)
	}
	if reasoning != "" {
		fmt.Fprintf(&sb, "reasoning effort: %s\n", reasoning)
	}
	cwd := t.CWD
	if cwd == "" {
		cwd = "."
	}
	fmt.Fprintf(&sb, "cwd: %s\n", cwd)

	// Context-window usage. The window comes from the live catalog
	// (so models.json overrides are reflected); usage is the most
	// recent completed turn's input, matching the status-bar gauge.
	ctxWindow := 0
	if model != "" {
		if m, err := provider.FindModel(t.Provider, model); err == nil {
			ctxWindow = m.ContextWindow
		}
	}
	used := last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
	switch {
	case !haveAgent:
		sb.WriteString("context: live usage unavailable (no active session)\n")
	case ctxWindow > 0 && used > 0:
		fmt.Fprintf(&sb, "context: %s / %s tokens (%.1f%% of window), as of the last turn\n",
			fmtTokens(used), fmtTokens(ctxWindow), float64(used)/float64(ctxWindow)*100)
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
			"provider":       t.Provider,
			"model":          model,
			"cwd":            cwd,
			"reasoning":      reasoning,
			"context_window": ctxWindow,
			"context_used":   used,
			"cumulative":     cum,
		},
	}, nil
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
