package ctrlproto

// Group is a capability-negotiated method group. Each side declares which
// groups it speaks in its [Hello]; the intersection is the contract. Keeping
// groups separable is what lets one protocol serve a focused mobile client, a
// full desktop control panel, and a fleet controller without any of them
// implementing surface they don't use.
type Group string

const (
	// GroupConversation carries the AgentEvent stream (out) and the turn
	// commands (prompt / queue / cancel / approve / answer / subscribe).
	GroupConversation Group = "conversation"
	// GroupSession carries session lifecycle + usage.
	GroupSession Group = "session"
	// GroupControl carries host-reconfiguring management: models, lore,
	// extensions, prompt overrides, templates, jail. Categorically higher
	// authority than the other two (see the protocol proposal's security note).
	GroupControl Group = "control"
	// GroupReplay carries a recorded session's transport (replay.control /
	// replay.state) and its replay_state broadcast. Optional and off the base
	// ServerHello — only a carrier backing a ReplayController advertises it, so a
	// client that negotiates it is guaranteed the group is served. See
	// docs/proposals/session-player.md.
	GroupReplay Group = "replay"
	// GroupAuth carries MODEL-PROVIDER credential mutation: establish, repair,
	// and revoke the credential terva uses to reach Anthropic, OpenAI, Kimi.
	// (Reading that state is auth.providers, which needs no authority and lives
	// in the session group.)
	//
	// Its own group rather than a corner of control, because groups are the unit
	// of authority gating (see the protocol proposal's security note). An
	// extension granted the control group can switch models and edit lore; it
	// must not thereby be able to REPLACE YOUR ANTHROPIC TOKEN. Separating them
	// now is nearly free; retrofitting the separation after something has been
	// granted control is not.
	//
	// Optional and off the base ServerHello, like GroupReplay: a carrier
	// advertises it only when it will actually serve a login, so a client that
	// negotiates it is guaranteed the group works. `terva web` advertises it only
	// under --web-allow-login, and refuses to on an unauthenticated listener.
	GroupAuth Group = "auth"
)

// Feature strings name additive capabilities negotiated on top of the groups.
// New capabilities land as features rather than protocol bumps, so an old
// client simply never uses one a newer host advertises. This list will grow.
//
// Two kinds exist, and the split below is load-bearing. SERVE-BEHAVIOR
// features change what serve.go does on the wire; every one of them MUST
// join [ServerHello]'s list, because Negotiate is a strict intersection — a
// serve-behavior feature missing there is dead no matter how many clients
// request it (how history-window shipped dark). ROOT-APPENDED features (the
// second block) are gated on host state the protocol layer can't see; the
// composition root appends them, and they must NOT appear in the base list.
// TestServerHelloAdvertisesServeGatedFeatures pins the distinction.
const (
	// FeatureImages advertises inbound image attachments on prompt.
	FeatureImages = "images"
	// FeatureResolveEvents advertises the permission_resolved / ask_resolved
	// multi-client dismissal events.
	FeatureResolveEvents = "resolve-events"
	// FeatureContextTree advertises that context.get carries the hierarchical
	// ContextBreakdown.Tree (section/turn/message outline). A client that sees it
	// renders the collapsible tree; one that doesn't falls back to the flat
	// Messages list. See docs/proposals/context-inspector.md.
	FeatureContextTree = "context-tree"
	// FeatureImageData advertises OUTBOUND image payloads: when negotiated,
	// image blocks in snapshots and message/tool-result events keep their
	// raw Data alongside MimeType+Bytes, so the client can render real
	// pixels. Without it a serialized carrier strips Data at the connection
	// boundary (size-only blocks; the lean wire shape) — inlined payloads
	// are opt-in bloat, not a default. In-process carriers bypass
	// serialization entirely and always see the full form. The inbound
	// direction (attachments on prompt) is FeatureImages, unchanged.
	FeatureImageData = "image-data"
	// FeatureFilesList advertises files.list — daemon-side workspace file
	// listing for a client's @-file picker. A client that doesn't see it
	// keeps its local-disk fallback (correct on the daemon's host, absent
	// cross-host) instead of firing a method the daemon can't serve.
	FeatureFilesList = "files-list"
	// FeatureGenerateTitle advertises sessions.generate_title — on-demand
	// session titling. A client that doesn't see it withholds the surface
	// (the TUI's `g` binding, the web button) instead of firing a method an
	// older daemon can't serve.
	FeatureGenerateTitle = "generate-title"
	// FeatureWorkspaceEvents advertises the reserved address [AddrWorkspace]:
	// a client may subscribe to the WORKSPACE and receive the facts that are
	// true of the daemon rather than of any session (a login landing, the
	// session set changing, the locale changing, a workspace-scoped surface
	// updating).
	//
	// A client that does not negotiate it still receives those events, because
	// the server falls back to what it did before this existed: stamping one
	// copy with each session id the client is subscribed to (serveState's compat
	// pump). That fallback is the reason the feature can be added without a
	// flag day — and the reason the duplication is now a property of an old
	// client's contract rather than of the workspace, which publishes once.
	FeatureWorkspaceEvents = "workspace-events"
	// FeatureHistoryWindow advertises WINDOWED snapshots: Snapshot.Messages carries
	// a tail of the transcript rather than all of it, plus the Epoch/Base/Total it
	// was cut at, and the client pages backward with conversation.history.
	//
	// It exists because a snapshot rides EVERY subscribe and — more expensively —
	// the end of EVERY turn (workspace_session.go: "one full-transcript frame per
	// turn end to every subscriber"). In-process that is free, image bytes and all,
	// because the slices are shared. Over a WebSocket to a phone it is the whole
	// conversation, again, after every message.
	//
	// A client that does not negotiate it receives the entire transcript exactly as
	// before. That is the point: the window is cut at the serialization boundary,
	// per contract, next to where image data is already stripped — so nothing that
	// exists has to change, and no flag day is needed.
	FeatureHistoryWindow = "history-window"
)

// Root-appended features: gated on host state (a flag, an enabled app), so
// the composition root appends them to the hello it serves — never this
// package, and never the base [ServerHello] list.
const (
	// FeatureRestart advertises that the daemon will serve control.restart
	// (Tier-1 self-restart), so a client can show a restart control. Set by the
	// composition root only when --web-allow-restart is passed.
	FeatureRestart = "restart"
	// FeatureStage advertises that this daemon serves the Stage app at /stage/
	// (web_stage enabled). A panel reads it to show an "open in Stage" link only
	// where the surface actually exists; it gates presentation, not a method group
	// (the library verbs the Stage app uses live in the always-on control group).
	FeatureStage = "stage"
)

// Hello is the handshake frame each side sends once at connect time. The
// client sends first (role "client"); the server replies with role "server".
type Hello struct {
	// Role is "client" or "server".
	Role string `json:"role"`
	// Protocol is the sender's ctrlproto wire version ([Protocol]).
	Protocol int `json:"protocol"`
	// Agent/Version identify the peer for logs and diagnostics.
	Agent   string `json:"agent,omitempty"`
	Version string `json:"version,omitempty"`
	// Groups are the method groups this side speaks.
	Groups []Group `json:"groups"`
	// Features are the additive capabilities this side supports.
	Features []string `json:"features,omitempty"`
	// Locale is the server's active UI language as a BCP-47 tag ("en" when
	// unset). The server advertises it so a client (e.g. the web PWA) can
	// select the matching catalog for its own strings; server-originated
	// display text on the wire is already localized to this language. The
	// protocol layer never sets this — the composition root does, to avoid a
	// dependency on the i18n package.
	Locale string `json:"locale,omitempty"`
	// CWD is the server's workspace working directory (absolute, host-local).
	// A client renders it so the status chrome names the tree it is actually
	// controlling, and — when it shares the daemon's host, the v1 attach
	// stance — points its local affordances (the @-file picker, the git
	// probe) at that tree instead of its own process cwd. Like Locale, the
	// protocol layer never sets this; the composition root does.
	CWD string `json:"cwd,omitempty"`
	// Jailed reports the workspace sandbox confining tools to the working
	// directory. A headless daemon has no runtime jail toggle, and the hello
	// re-announces on every (re)connect, so connect-time state stays truthful
	// across a restart that changes the flag. Server-side only, like Locale.
	Jailed bool `json:"jailed,omitempty"`
	// MaxUploadBytes is the largest single file the carrier will accept in one
	// frame, or 0 when the carrier does not bound it. A client checks a file
	// against this BEFORE sending: an oversized frame is not rejected with an
	// error, it kills the connection, so the request it belonged to fails with a
	// generic dead-socket message that names nothing useful. Advertising the
	// bound is what lets the panel say "this file is 19.6 MB, the limit is 24 MB"
	// instead. Carrier-owned (web/conn.go), so the composition root sets it.
	MaxUploadBytes int64 `json:"max_upload_bytes,omitempty"`
}

const (
	// RoleClient / RoleServer are the two [Hello.Role] values.
	RoleClient = "client"
	RoleServer = "server"
)

// Contract is the negotiated intersection of two [Hello]s: the groups and
// features both sides speak, at the lower of the two protocol versions. It is
// the effective capability set for the connection.
type Contract struct {
	Protocol int
	Groups   []Group
	Features []string
}

// Has reports whether the contract includes group g.
func (c Contract) Has(g Group) bool {
	for _, x := range c.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// HasFeature reports whether the contract includes feature f.
func (c Contract) HasFeature(f string) bool {
	for _, x := range c.Features {
		if x == f {
			return true
		}
	}
	return false
}

// Negotiate computes the [Contract] from the local and remote hellos: the
// intersection of groups and features, at min(protocol). Order follows the
// local side's declaration so the result is deterministic (golden-testable).
func Negotiate(local, remote Hello) Contract {
	proto := local.Protocol
	if remote.Protocol < proto {
		proto = remote.Protocol
	}
	c := Contract{Protocol: proto}
	remoteGroups := make(map[Group]bool, len(remote.Groups))
	for _, g := range remote.Groups {
		remoteGroups[g] = true
	}
	for _, g := range local.Groups {
		if remoteGroups[g] {
			c.Groups = append(c.Groups, g)
		}
	}
	remoteFeat := make(map[string]bool, len(remote.Features))
	for _, f := range remote.Features {
		remoteFeat[f] = true
	}
	for _, f := range local.Features {
		if remoteFeat[f] {
			c.Features = append(c.Features, f)
		}
	}
	return c
}

// ServerHello is the default server-side [Hello] for a full workspace
// (all three groups + every serve-behavior feature). Carriers may trim it.
// Presentation features gated on host state (FeatureStage) are appended by
// the composition root, not here. A feature that serve.go changes behavior
// on MUST appear in this list: Negotiate is a strict intersection, so a
// server-behavior feature missing here is dead on the wire no matter how
// many clients request it — TestServerHelloAdvertisesServeGatedFeatures
// pins that.
func ServerHello(agent, version string) Hello {
	return Hello{
		Role:     RoleServer,
		Protocol: Protocol,
		Agent:    agent,
		Version:  version,
		Groups:   []Group{GroupConversation, GroupSession, GroupControl},
		Features: []string{FeatureImages, FeatureResolveEvents, FeatureContextTree, FeatureImageData, FeatureFilesList, FeatureGenerateTitle, FeatureWorkspaceEvents, FeatureHistoryWindow},
	}
}
