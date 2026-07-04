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
)

// Feature strings name additive capabilities negotiated on top of the groups.
// New capabilities land as features rather than protocol bumps, so an old
// client simply never uses one a newer host advertises. This list will grow.
const (
	// FeatureImages advertises inbound image attachments on prompt.
	FeatureImages = "images"
	// FeatureResolveEvents advertises the permission_resolved / ask_resolved
	// multi-client dismissal events.
	FeatureResolveEvents = "resolve-events"
	// FeatureRestart advertises that the daemon will serve control.restart
	// (Tier-1 self-restart), so a client can show a restart control. Set by the
	// composition root only when --web-allow-restart is passed.
	FeatureRestart = "restart"
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
// (all three groups + every feature). Carriers may trim it.
func ServerHello(agent, version string) Hello {
	return Hello{
		Role:     RoleServer,
		Protocol: Protocol,
		Agent:    agent,
		Version:  version,
		Groups:   []Group{GroupConversation, GroupSession, GroupControl},
		Features: []string{FeatureImages, FeatureResolveEvents},
	}
}
