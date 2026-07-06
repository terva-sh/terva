package modes

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// Carrier is the in-process ctrlproto surface the interactive TUI drives by
// default (the legacy direct driver hides behind --tui-legacy): the full
// [ctrlproto.WorkspaceService], plus a reliable (no-drop) Subscribe and a
// transitional [core.Agent] accessor.
//
// It is the seam of the TUI-on-ctrlproto migration
// (docs/proposals/tui-on-ctrlproto.md). The concrete implementation is
// *agent.Workspace, which already satisfies this interface — modes cannot import
// package agent (that package imports modes), so the TUI holds this interface and
// the composition root (cli.go) injects the concrete *Workspace.
//
// SubscribeReliable and AgentFor are in-process-only affordances, deliberately
// off ctrlproto.WorkspaceService: a networked carrier can neither promise
// unbounded delivery nor hand out a live *core.Agent. AgentFor is the transitional
// crutch — the TUI renders the finalized transcript and drives not-yet-migrated
// management dialogs off the agent directly while its hot path (prompt dispatch +
// the event stream + approvals/asks) goes through the wire. It disappears as those
// readers move onto snapshots and surfaces.
type Carrier interface {
	ctrlproto.WorkspaceService

	// SubscribeReliable is Subscribe with no-drop delivery — a dropped text
	// delta would corrupt the rendered transcript.
	SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error)

	// AgentFor returns the in-process agent backing sess (materializing it) and
	// the resolved session id — the transitional rendering/dialog crutch.
	AgentFor(sess string) (*core.Agent, string, error)
}
