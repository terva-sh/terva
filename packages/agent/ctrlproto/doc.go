// Package ctrlproto is terva's control-plane protocol: the wire-agnostic
// interface a frontend uses to drive and manage a terva workspace, plus one
// JSON serialization of it (the "ctrlproto" wire) and a generic server loop
// that any carrier can bind.
//
// # Interface-first, wire-second
//
// The artifact that matters is [WorkspaceService] — "everything a frontend
// needs to drive and manage terva": run turns, manage sessions, and
// (additively) manage models, lore, extensions, and templates. The wire
// ([Frame] + [Encode]/[Decode]) is one serialization of that interface;
// [ServeConn] adapts any [FrameConn] carrier onto a WorkspaceService.
//
// Different clients pick different carriers of the SAME interface:
//
//   - in-process  → the TUI (direct interface calls, no serialization)
//   - WebSocket   → `terva web`
//   - stdio       → a CLI / fleet controller
//   - ext-tunnel  → extensions, over extproto
//
// This is deliberately the connproto/extproto meta-architecture (a wire spec
// + an SDK-style interface + pluggable carriers + capability negotiation +
// id-correlated commands / uncorrelated streamed events) with a richer,
// non-chat vocabulary. See docs/proposals/control-plane-protocol.md.
//
// # Method groups
//
// The surface is organized into capability-negotiated groups with different
// rates of change and audiences: [GroupConversation] (the AgentEvent stream +
// turn commands), [GroupSession] (session lifecycle + usage), and
// [GroupControl] (models / lore / extensions / templates — mostly v2). A
// minimal client (the mobile PWA) negotiates only the first two.
//
// # Conversation vocabulary
//
// The streaming half reuses core's canonical [core.WireEvent] vocabulary
// verbatim; [Event] embeds it and adds only the two control-plane events a
// browser must render and answer — permission requests and questions — which
// are broadcast to every subscriber and resolved by the first client to
// respond (the multi-client fan-out story). See event.go.
package ctrlproto

// Protocol is the ctrlproto wire version. Capabilities grow additively via
// feature strings (see [Hello]); this bumps only on a breaking envelope
// change, which the negotiation is designed to avoid.
const Protocol = 1
