// Package authz answers "what may this caller do", which terva has never asked
// before. Until now the daemon's auth was a gate: authorized() returned a bool,
// and everything past it was the owner — the full ctrlproto surface, including
// the credential-mutating auth group and the at-rest posture in secrets.
//
// The unit of authority is the ctrlproto method GROUP, and that is not a new idea
// here: hello.go already calls groups "the unit of authority gating" and already
// withholds GroupAuth and GroupSecrets from a carrier that should not serve them
// ("terva web advertises it only under --web-allow-login, and refuses to on an
// unauthenticated listener"). This package generalizes that existing move from
// PER-LISTENER to PER-PRINCIPAL, so the same daemon can serve two callers at two
// authorities.
//
// See docs/proposals/daemon-access-auth.md (D1, D2).
package authz

import (
	"slices"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// Role is an authority level a principal holds. Roles are coarse on purpose:
// they map onto method groups, which is the granularity the protocol already
// enforces, rather than inventing a second permission vocabulary that would have
// to be kept in sync with the first.
type Role string

const (
	// RoleOwner is every group the carrier offers. This is what every
	// pre-role auth mode resolves to, which is what makes introducing this
	// package a no-op on existing deployments.
	RoleOwner Role = "owner"
	// RoleOperator may run and reconfigure the host but may NOT touch
	// credentials or the at-rest posture — the separation hello.go argues for
	// when it splits GroupAuth out of GroupControl: "an extension granted the
	// control group can switch models and edit lore; it must not thereby be
	// able to REPLACE YOUR ANTHROPIC TOKEN."
	RoleOperator Role = "operator"
	// RoleMember may converse and manage its own sessions, but not
	// reconfigure the host (no models, lore, extensions, prompt overrides or
	// jail) and not touch credentials.
	RoleMember Role = "member"
)

// RoleViewer may look and may not touch: no mutation, and — the part groups
// alone could never express — no model call, so it cannot spend the operator's
// subscription.
//
// 🔑 This role could not exist until ctrlproto gained per-verb capabilities.
// Groups are the wrong shape for it in BOTH directions: GroupConversation
// carries `subscribe` (harmless) beside `prompt` (spends), and GroupSession
// reads as a listing surface while carrying `sidechat.ask`, `suggest.reply`,
// `sessions.generate_title` and three doctors that all reach a provider. A
// viewer built out of groups would have quietly held a credit card.
const RoleViewer Role = "viewer"

// Source names which authn mode produced a principal. It is diagnostic — no
// authorization decision reads it — but it is what makes a log line or an admin
// panel able to say HOW someone got in, not merely that they did.
type Source string

const (
	SourceNone        Source = "none"         // no auth configured; a loopback bind
	SourceToken       Source = "token"        // bearer token matched
	SourceForwardAuth Source = "forward-auth" // a trusted proxy asserted it
	SourceOIDC        Source = "oidc"         // an identity provider vouched for it
)

// KnownRole reports whether r has an authority table entry.
//
// 🔑 It exists so a configured role name can be REJECTED at the boundary rather
// than silently granting nothing. Grant already ignores an unknown role, so a
// typo in an operator's group→role map is harmless — and invisible, which is
// worse: it looks exactly like a working grant until someone checks what it
// actually allows.
func KnownRole(r Role) bool {
	_, ok := groupsForRole[r]
	return ok
}

// RoleNames lists every role this build understands, most authority first.
//
// It reads the same table KnownRole does rather than repeating the constants,
// so a role that exists is a role this list names. The caller is an error
// message — a supervisor refusing someone whose identity-provider groups map to
// no role has to say what WOULD work, or the refusal is a support conversation.
func RoleNames() []string {
	ordered := []Role{RoleOwner, RoleOperator, RoleMember, RoleViewer}
	out := make([]string, 0, len(groupsForRole))
	for _, r := range ordered {
		if KnownRole(r) {
			out = append(out, string(r))
		}
	}
	// Anything in the table the ordering above forgot, so a new role cannot be
	// silently absent from the one place people are told what to configure.
	for r := range groupsForRole {
		if !slices.Contains(out, string(r)) {
			out = append(out, string(r))
		}
	}
	return out
}

// Principal is who is on the other end of a connection.
//
// Every authn mode produces one, including the modes that predate roles — that
// is the point. It means the authorization seam can land, and be exercised by
// every existing connection, before any identity provider is wired up.
type Principal struct {
	// Subject is the stable identifier for this caller. For a forward-auth
	// principal it is the header value the proxy asserted, which terva
	// previously discarded into a log line and now keys authority on.
	Subject string
	// Display is a human-facing label for logs and admin surfaces. It may be
	// empty, in which case Subject is the thing to show.
	Display string
	Roles   []Role
	Source  Source
}

// Owner builds the all-authority principal that every auth mode resolved to
// before roles existed. Callers that have no role information yet use this, so
// the behavior they had is the behavior they keep.
func Owner(src Source, subject string) Principal {
	return Principal{Subject: subject, Roles: []Role{RoleOwner}, Source: src}
}

// Has reports whether p holds role.
func (p Principal) Has(role Role) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// groupsForRole is the role → authority table. Kept as one map rather than a
// switch so a new group forces a visible decision in every row instead of
// silently defaulting to "not granted" in rows nobody remembered to update.
var groupsForRole = map[Role][]ctrlproto.Group{
	RoleOwner: {
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupControl,
		ctrlproto.GroupReplay,
		ctrlproto.GroupAuth,
		ctrlproto.GroupSecrets,
		// 🚨 The owner alone, and not RoleOperator, whose whole definition is
		// "may run and reconfigure THE HOST but may not touch credentials".
		// Managing tenants is neither: it reaches into other people's
		// environments, and suspending one takes someone's workspace away.
		//
		// Safe to sit in this table even on a single-tenant `terva web`,
		// because Restrict INTERSECTS with what the carrier offered and no
		// workspace carrier offers this group. That is the property Restrict's
		// comment argues for, load-bearing here: without it, adding a row to
		// this table would have added a surface to every daemon.
		ctrlproto.GroupTenants,
	},
	RoleOperator: {
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupControl,
		ctrlproto.GroupReplay,
	},
	RoleMember: {
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupReplay,
	},
	RoleViewer: {
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupReplay,
	},
}

// capabilitiesForRole is the SECOND half of a role, and the half groups cannot
// express. A role's reach is the intersection of the two: which surfaces it may
// negotiate (above) and what it may cause on them (here).
//
// RoleViewer holds the same groups as RoleMember and differs only here — which
// is precisely why the capability axis had to exist. On groups alone the two
// roles are identical.
var capabilitiesForRole = map[Role]ctrlproto.Capability{
	RoleOwner:    ctrlproto.CapRead | ctrlproto.CapWrite | ctrlproto.CapSpend,
	RoleOperator: ctrlproto.CapRead | ctrlproto.CapWrite | ctrlproto.CapSpend,
	RoleMember:   ctrlproto.CapRead | ctrlproto.CapWrite | ctrlproto.CapSpend,
	RoleViewer:   ctrlproto.CapRead,
}

// Authority returns the capability ceiling for p — the union across its roles,
// since holding two roles means being allowed what either permits.
//
// A principal with no role gets NOTHING, the same fail-closed direction Grant
// takes: an unmapped claim produces a caller who can do nothing, never one who
// inherits the daemon.
func Authority(p Principal) ctrlproto.Capability {
	var mask ctrlproto.Capability
	for _, r := range p.Roles {
		mask |= capabilitiesForRole[r]
	}
	return mask
}

// Grant returns the union of the groups p's roles allow — the CEILING on this
// principal's authority, not the set it will actually get.
//
// A principal with no role grants nothing. That is the safe direction: a
// mis-mapped claim produces a caller who can do nothing and complains loudly,
// rather than one who silently inherits the daemon.
func Grant(p Principal) []ctrlproto.Group {
	seen := map[ctrlproto.Group]bool{}
	var out []ctrlproto.Group
	for _, r := range p.Roles {
		for _, g := range groupsForRole[r] {
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// Restrict returns the groups a carrier should advertise to p: the ones it
// OFFERS, intersected with the ones p's roles allow.
//
// 🚨 An INTERSECTION, never a replacement or a union, and the distinction is
// load-bearing. The carrier's offer already encodes decisions authority knows
// nothing about — `terva web` withholds GroupAuth unless --web-allow-login, and
// refuses to advertise it at all on an unauthenticated listener. Handing an
// owner the raw Grant would re-add exactly those groups the operator turned off,
// turning a role system into a privilege-escalation path on its first commit.
//
// Order follows the carrier's offer so the advertised hello stays stable rather
// than reordering with whoever connected.
func Restrict(offered []ctrlproto.Group, p Principal) []ctrlproto.Group {
	allowed := map[ctrlproto.Group]bool{}
	for _, g := range Grant(p) {
		allowed[g] = true
	}
	out := make([]ctrlproto.Group, 0, len(offered))
	for _, g := range offered {
		if allowed[g] {
			out = append(out, g)
		}
	}
	return out
}
