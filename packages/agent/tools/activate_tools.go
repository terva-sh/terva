package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// groupSchemaEchoBudget caps how many bytes of tool schemas activate_tools
// echoes into its result. Echoing the group's schemas lets the model compose
// its next-turn call now — the activated schemas only reach the advertised
// Tools array next turn (the per-turn pin) — but the echo is a one-time cost
// that rides the transcript until compaction, and a large MCP group would
// re-inflate the very schema weight lazy tools exists to defer. Past the budget
// we fall back to a name-only line; the schemas still arrive in the Tools array
// next turn regardless. Sized to admit a focused extension (a handful of tools)
// while excluding a sprawling MCP server; well under the 8 KiB ephemeral cap.
const groupSchemaEchoBudget = 4096

// ActivateToolsTool lets the model bring a hidden capability group into the
// advertised set under lazy tool visibility (retro H2·b). It resolves the
// calling agent from the dispatch context and marks the group active for the
// next turn — visibility only: the tools it reveals were always callable and
// still face their normal permission gate, so activation grants no authority.
// It is registered only when lazy mode is on (build.Resolved.LazyTools); with
// every group already advertised there would be nothing to activate.
type ActivateToolsTool struct{}

func (t *ActivateToolsTool) Name() string { return "activate_tools" }

func (t *ActivateToolsTool) Description() string {
	return i18n.D("tool.activate_tools.description", "Activate an installed tool group. You can then call the tools in that group. The [inactive tool groups] context note lists the inactive groups and their tools. Give the name of one group, for example \"mail\" or \"mcp:github\".\n\nThis tool controls visibility only. It does not give authority. Each tool that becomes visible still needs its usual permission when you call it.\n\nYou cannot change the tool calls that you sent in this same reply. The group becomes available on your next step. Call the tools of the group directly then. Do not call activate_tools again for that group.")
}

func (t *ActivateToolsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"group":{"type":"string","description":"The capability group to activate, for example \"mail\" or \"mcp:github\". The [inactive tool groups] note lists the groups."}},"required":["group"]}`)
}

func (t *ActivateToolsTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a struct {
		Group string `json:"group"`
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
		}
	}
	group := strings.TrimSpace(a.Group)
	if group == "" {
		return toolErr(`activate_tools: provide a non-empty "group" (valid names are in the [inactive tool groups] note)`), nil
	}
	agent := core.AgentFromContext(ctx)
	if agent == nil {
		return toolErr("activate_tools: no agent in context"), nil
	}
	names := agent.ToolsInGroup(group)
	if len(names) == 0 {
		return toolErr(fmt.Sprintf("activate_tools: no installed tool group %q; the valid group names are listed in the [inactive tool groups] note", group)), nil
	}
	joined := strings.Join(names, ", ")
	autoContinue := agent.ActivationContinuationEnabled()
	if !agent.ActivateGroup(group) {
		if autoContinue {
			return activateResult(fmt.Sprintf("Tool group %q is already active: %s. Its tools are available on your next model step — call them directly rather than re-activating; retrying cannot make them appear sooner.", group, joined)), nil
		}
		return activateResult(fmt.Sprintf("Tool group %q is already active: %s. Activation lands on your NEXT turn — if you activated it earlier THIS turn, retrying cannot make its tools appear sooner; continue with your currently advertised tools and use it next turn.", group, joined)), nil
	}
	var msg string
	if autoContinue {
		msg = fmt.Sprintf("Activated tool group %q: %s. These tools are available on your next model step — call them directly; do not call activate_tools again. Tool calls already emitted in this batch are unchanged. Visibility only: each still requires its normal permission when called.", group, joined)
	} else {
		msg = fmt.Sprintf("Activated tool group %q — its tools (%s) join your advertised set on your NEXT turn. They cannot appear during this turn's remaining tool calls, so do not retry; continue with your current tools. Each still requires its normal permission when called.", group, joined)
	}
	// Echo the group's schemas so the model can compose its next-turn call now,
	// bridging the per-turn pin without a second discovery round-trip. Budgeted:
	// a sprawling group falls back to a name-only line rather than re-inflating
	// the schema weight lazy tools defers (renderGroupSchemas).
	if note := renderGroupSchemas(agent.ToolSpecsInGroup(group), groupSchemaEchoBudget); note != "" {
		msg += "\n\n" + note
	}
	return activateResult(msg), nil
}

// renderGroupSchemas echoes a group's tool schemas into the activate_tools
// result so the model can compose its next-turn call now (the schemas reach the
// advertised Tools array only next turn). Over budget it returns a name-only
// fallback line instead — never silent truncation — because the schemas still
// arrive in the Tools array next turn and echoing a large group would re-inflate
// the weight lazy tools defers. Empty specs yield "".
func renderGroupSchemas(specs []provider.Tool, budget int) string {
	if len(specs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Their schemas, so you can compose your next call now (they are not callable during this reply):")
	for _, s := range specs {
		fmt.Fprintf(&b, "\n\n### %s\n%s\nparameters: %s", s.Name, s.Description, compactSchema(s.Schema))
	}
	if b.Len() > budget {
		return fmt.Sprintf("(This group has %d tools; their schemas are large and are not echoed here to keep context lean — they join your advertised tools when they go live, ready to call.)", len(specs))
	}
	return b.String()
}

// compactSchema strips insignificant whitespace from a JSON Schema so an
// extension/MCP tool's pretty-printed schema doesn't inflate the echo. Falls
// back to the raw bytes if the schema isn't valid JSON.
func compactSchema(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func activateResult(msg string) core.ToolResult {
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: msg}}}
}
