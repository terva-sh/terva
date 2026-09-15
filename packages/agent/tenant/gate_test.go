package tenant

import (
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
)

func principal(roles ...authz.Role) authz.Principal {
	return authz.Principal{Subject: "sub", Source: authz.SourceOIDC, Roles: roles}
}

// The coupling D4 calls load-bearing: tenants share ONE provider credential, so
// a tenant holding auth.* could replace or delete the credential every other
// tenant is using. Not even an owner-roled tenant gets it across the proxy —
// the operator manages credentials, a tenant spends them.
func TestCredentialAndSecretGroupsNeverCrossTheProxy(t *testing.T) {
	for _, g := range []ctrlproto.Group{ctrlproto.GroupAuth, ctrlproto.GroupSecrets} {
		if Forwardable(g) {
			t.Errorf("%s is forwardable to a tenant", g)
		}
		offered := []ctrlproto.Group{g, ctrlproto.GroupConversation}
		got := ForwardableGroups(offered, principal(authz.RoleOwner))
		if slices.Contains(got, g) {
			t.Errorf("%s survived narrowing for an owner-roled tenant: %v", g, got)
		}
		// The control: the narrowing is selective, not a blanket refusal.
		if !slices.Contains(got, ctrlproto.GroupConversation) {
			t.Fatalf("CONTROL: conversation must survive alongside %s, got %v", g, got)
		}
	}
}

// A verb in a non-forwardable group must read as absent, not as refused.
// Refusal is a check; a check is one bug away from passing, and it also
// CONFIRMS the surface is there.
func TestANonForwardableVerbReadsAsAbsentNotRefused(t *testing.T) {
	f := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 7, Method: ctrlproto.MethodAuthLoginStart}
	got := CheckCommand(f, principal(authz.RoleOwner))
	if got == nil {
		t.Fatal("an auth verb was forwarded")
	}
	if got.Error.Code != ctrlproto.CodeUnsupported {
		t.Errorf("code %q, want %q — a tenant must not learn the group exists", got.Error.Code, ctrlproto.CodeUnsupported)
	}
	if got.ID != 7 {
		t.Errorf("the refusal did not answer the caller's frame: id=%d", got.ID)
	}
	if strings.Contains(strings.ToLower(got.Error.Message), "supervisor") ||
		strings.Contains(strings.ToLower(got.Error.Message), "permitted") {
		t.Errorf("the message confirms the surface exists: %q", got.Error.Message)
	}
}

// The capability axis, exercised through the proxy: a viewer holds the same
// GROUPS as a member and differs only in the mask, which is the whole argument
// for the second axis existing.
func TestAViewerMayReadButNotSpend(t *testing.T) {
	viewer := principal(authz.RoleViewer)

	read := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.MethodSessionsList}
	if got := CheckCommand(read, viewer); got != nil {
		t.Fatalf("CONTROL: a viewer must be able to list sessions: %v", got.Error)
	}

	spend := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 2, Method: ctrlproto.MethodPrompt}
	got := CheckCommand(spend, viewer)
	if got == nil {
		t.Fatal("a viewer prompted the model — that spends the shared subscription")
	}
	if got.Error.Code != ctrlproto.CodeForbidden {
		t.Errorf("code %q, want %q", got.Error.Code, ctrlproto.CodeForbidden)
	}

	// And the same verb for a member, so the refusal is about the ROLE and not
	// about the verb being broken.
	if got := CheckCommand(spend, principal(authz.RoleMember)); got != nil {
		t.Fatalf("CONTROL: a member must be able to prompt: %v", got.Error)
	}
}

// A principal with no roles at all is the shape a missing middleware produces.
func TestAnUnroledPrincipalGetsNothing(t *testing.T) {
	var nobody authz.Principal
	if got := ForwardableGroups([]ctrlproto.Group{ctrlproto.GroupConversation}, nobody); len(got) != 0 {
		t.Errorf("a zero principal was granted %v", got)
	}
	f := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.MethodSessionsList}
	if got := CheckCommand(f, nobody); got == nil {
		t.Error("a zero principal was allowed to list sessions")
	}
}

// An unknown verb has no group, so it falls off the allowlist rather than
// through it.
func TestAnUnknownVerbIsNotForwarded(t *testing.T) {
	f := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.Method("tenants.list")}
	got := CheckCommand(f, principal(authz.RoleOwner))
	if got == nil {
		t.Fatal("an unknown verb was forwarded to the child")
	}
	if got.Error.Code != ctrlproto.CodeUnsupported {
		t.Errorf("code %q, want %q", got.Error.Code, ctrlproto.CodeUnsupported)
	}
}

// Frames that are not commands carry no method to gate, and must pass through
// untouched — an event or a response filtered here would break the stream.
func TestNonCommandFramesArePassedThrough(t *testing.T) {
	for _, k := range []ctrlproto.Kind{ctrlproto.KindEvent, ctrlproto.KindResp, ctrlproto.KindHello} {
		if got := CheckCommand(ctrlproto.Frame{Kind: k}, principal(authz.RoleViewer)); got != nil {
			t.Errorf("a %s frame was refused: %v", k, got.Error)
		}
	}
}

// The hello the browser sees must be what this principal may actually use: a
// panel that renders affordances the gate will refuse is worse than one that
// renders fewer.
func TestTheHelloTheBrowserSeesMatchesWhatTheGateWillAllow(t *testing.T) {
	child := ctrlproto.Hello{
		Role:   ctrlproto.RoleServer,
		Groups: []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupSession, ctrlproto.GroupControl, ctrlproto.GroupAuth, ctrlproto.GroupSecrets},
	}
	for _, role := range []authz.Role{authz.RoleOwner, authz.RoleOperator, authz.RoleMember, authz.RoleViewer} {
		p := principal(role)
		advertised := NarrowHello(child, p, Carrier{}).Groups
		for _, g := range advertised {
			if !Forwardable(g) {
				t.Errorf("%s: hello advertises %s, which the proxy will not carry", role, g)
			}
			if !slices.Contains(authz.Grant(p), g) {
				t.Errorf("%s: hello advertises %s, which this role was not granted", role, g)
			}
		}
	}
	// NarrowHello must not mutate the caller's hello — the same child hello is
	// narrowed once per connection, and a mutation would let the first tenant's
	// role decide what every later one sees.
	if len(child.Groups) != 5 {
		t.Errorf("NarrowHello mutated the child's hello: %v", child.Groups)
	}
}

// The group census lives in gate_features_test.go now, next to its sibling for
// features, and reads the constants out of ctrlproto's source instead of a list
// typed here. The hand-typed list this replaces had already gone stale: it
// enumerated six groups after ctrlproto grew a seventh, so the census that
// exists to catch an undecided group did not catch GroupTenants.
