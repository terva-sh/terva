package modes

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// Carrier is the in-process ctrlproto surface the interactive TUI drives — the
// only TUI backend since the legacy direct driver's removal (--tui-legacy
// survives as a deprecated no-op): the full [ctrlproto.WorkspaceService] plus a
// reliable (no-drop) Subscribe.
//
// It is the seam of the TUI-on-ctrlproto migration
// (docs/proposals/tui-on-ctrlproto.md). The concrete implementation is
// *agent.Workspace, which already satisfies this interface — modes cannot import
// package agent (that package imports modes), so the TUI holds this interface and
// the composition root (cli.go) injects the concrete *Workspace.
//
// SubscribeReliable is an in-process-only affordance, deliberately off
// ctrlproto.WorkspaceService: a networked carrier cannot promise unbounded
// delivery. It is now the ONLY thing here beyond the wire service.
//
// STATUS (plan 4.1, complete): the TUI holds no *core.Agent. The transitional
// AgentFor accessor — once ~22 readers deep (usage gauges, the prompt queue,
// tool rebinds, /btw, the engine's idle checks) — is gone. Every reader moved
// to a snapshot, a surface, or a wire verb; /btw, the last, drives the sidechat
// surface. The default TUI's whole control path is ctrlproto: prompt dispatch,
// the event stream, approvals/asks, transcript rendering, /context,
// /permissions, /settings, /clear, /model, login, and side chat. What remains
// non-wire is file-based session fork/tree/export (out of wire scope v1).
//
// Policy (review theme B): every NEW TUI/control feature must answer, at design
// time, "is this a ctrlproto method/surface/event, or intentionally
// local-only?" Reaching back for a live agent is no longer possible — the
// accessor is gone and nothing may reintroduce it. A feature that needs
// daemon-side state adds a verb or a surface, the way the sidechat surface
// replaced the /btw agent read.
type Carrier interface {
	ctrlproto.WorkspaceService

	// SubscribeReliable is Subscribe with no-drop delivery — a dropped text
	// delta would corrupt the rendered transcript.
	SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error)
}
