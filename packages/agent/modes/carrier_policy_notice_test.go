package modes

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// A project config that cannot be parsed loses its deny and ask rules, and the
// daemon says so with an error notice when the session's first turn starts
// (workspace_policy_notice_test.go). The TUI must show it in the status line and
// keep it there through the turn it arrived with, because the notice is sent
// once and never replayed.
func TestAPolicyNoticeStaysOnTheStatusLineThroughItsTurn(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.resetTurnUI() // the prompt was dispatched; the notice follows it
	const warning = "config: project config unreadable, so none of its settings apply until it is fixed. These restrictions are NOT applied: permissions (its deny and ask rules), disable_extensions, disable_mcp, disable_context_extensions, tickets, project_scoped. Extensions and MCP servers it disables can start: parse /x/.terva/config.json: bad"
	i.handleCarrierEvent(ctrlproto.NoticeEvent("error", "", warning))
	if i.statusErr != warning {
		t.Fatalf("statusErr = %q, want the policy warning", i.statusErr)
	}

	i.handleCarrierEvent(conv(core.WireEvent{Type: "turn_end", Stop: string(provider.StopEnd)}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "done"}))
	if i.statusErr != warning {
		t.Fatalf("the warning was cleared by the end of its turn: statusErr = %q", i.statusErr)
	}
}
