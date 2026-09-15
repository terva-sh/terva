package authz

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

func has(gs []ctrlproto.Group, want ctrlproto.Group) bool {
	for _, g := range gs {
		if g == want {
			return true
		}
	}
	return false
}

// The owner principal is what every pre-role auth mode produces, so if this
// stops granting everything the carrier offers, introducing roles silently
// demoted every existing deployment.
func TestOwnerKeepsEverythingTheCarrierOffers(t *testing.T) {
	offered := []ctrlproto.Group{
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupControl,
		ctrlproto.GroupReplay,
		ctrlproto.GroupAuth,
		ctrlproto.GroupSecrets,
	}
	got := Restrict(offered, Owner(SourceToken, "token"))
	if len(got) != len(offered) {
		t.Fatalf("owner got %d groups, want all %d: %v", len(got), len(offered), got)
	}
	for _, g := range offered {
		if !has(got, g) {
			t.Errorf("owner lost group %q", g)
		}
	}
}

// 🚨 The escalation case. The carrier's offer encodes operator decisions —
// `terva web` withholds GroupAuth unless --web-allow-login. If Restrict returned
// the principal's grant instead of intersecting with the offer, an owner would
// get back exactly the groups the operator turned off, and the first commit of
// the role system would be a privilege-escalation path.
func TestRestrictNeverAddsAGroupTheCarrierWithheld(t *testing.T) {
	// A listener with --web-allow-login and --web-allow-secrets both OFF.
	offered := []ctrlproto.Group{
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupControl,
	}
	got := Restrict(offered, Owner(SourceNone, "local"))
	for _, forbidden := range []ctrlproto.Group{ctrlproto.GroupAuth, ctrlproto.GroupSecrets, ctrlproto.GroupReplay} {
		if has(got, forbidden) {
			t.Errorf("Restrict re-added %q, which the carrier never offered: %v", forbidden, got)
		}
	}
	if len(got) != len(offered) {
		t.Errorf("Restrict = %v, want exactly the offered set %v", got, offered)
	}
}

// The narrowing has to actually narrow, or the seam is decoration. Operator is
// the separation hello.go argues for: reconfigure the host, never touch the
// credential.
func TestOperatorCannotReachCredentialsOrSecrets(t *testing.T) {
	offered := []ctrlproto.Group{
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupControl,
		ctrlproto.GroupAuth,
		ctrlproto.GroupSecrets,
	}
	got := Restrict(offered, Principal{Subject: "op", Roles: []Role{RoleOperator}, Source: SourceForwardAuth})
	if !has(got, ctrlproto.GroupControl) {
		t.Errorf("operator lost control, which it must keep: %v", got)
	}
	if has(got, ctrlproto.GroupAuth) {
		t.Error("operator reached the auth group — it must not be able to replace the provider credential")
	}
	if has(got, ctrlproto.GroupSecrets) {
		t.Error("operator reached the secrets group")
	}
}

func TestMemberCannotReconfigureTheHost(t *testing.T) {
	offered := []ctrlproto.Group{
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupControl,
		ctrlproto.GroupAuth,
	}
	got := Restrict(offered, Principal{Subject: "m", Roles: []Role{RoleMember}, Source: SourceForwardAuth})
	if !has(got, ctrlproto.GroupConversation) {
		t.Errorf("member cannot converse: %v", got)
	}
	if has(got, ctrlproto.GroupControl) {
		t.Error("member reached the control group — models, lore, extensions and jail are host reconfiguration")
	}
	if has(got, ctrlproto.GroupAuth) {
		t.Error("member reached the auth group")
	}
}

// The zero Principal is what a handler sees if it was mounted without the
// middleware. It must grant nothing: "nobody authenticated this" has to fail
// closed, never default to the owner.
func TestZeroPrincipalGrantsNothing(t *testing.T) {
	offered := []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupControl}
	if got := Restrict(offered, Principal{}); len(got) != 0 {
		t.Fatalf("the zero principal got %v, want no groups at all", got)
	}
	if got := Grant(Principal{}); len(got) != 0 {
		t.Fatalf("Grant(zero) = %v, want empty", got)
	}
}

// An unknown role is the mis-mapped-claim case: an IdP hands over a group name
// nothing maps. It must contribute nothing rather than falling through to a
// default.
func TestUnknownRoleGrantsNothing(t *testing.T) {
	p := Principal{Subject: "x", Roles: []Role{Role("wizard")}, Source: SourceForwardAuth}
	if got := Grant(p); len(got) != 0 {
		t.Fatalf("unknown role granted %v, want nothing", got)
	}
}

// Roles are additive: holding two gives the union, not the last one written.
func TestMultipleRolesUnion(t *testing.T) {
	p := Principal{Roles: []Role{RoleMember, RoleOperator}}
	got := Grant(p)
	if !has(got, ctrlproto.GroupControl) {
		t.Errorf("the operator half was lost: %v", got)
	}
	seen := map[ctrlproto.Group]int{}
	for _, g := range got {
		seen[g]++
	}
	for g, n := range seen {
		if n != 1 {
			t.Errorf("group %q appears %d times; the union must dedupe", g, n)
		}
	}
}

// Restrict follows the carrier's ordering so the advertised hello does not
// reshuffle depending on who connected.
func TestRestrictPreservesCarrierOrder(t *testing.T) {
	offered := []ctrlproto.Group{ctrlproto.GroupSession, ctrlproto.GroupConversation, ctrlproto.GroupControl}
	got := Restrict(offered, Owner(SourceNone, "local"))
	for i := range offered {
		if got[i] != offered[i] {
			t.Fatalf("Restrict reordered: got %v, want %v", got, offered)
		}
	}
}

// 🚨 The whole reason step 2 existed. A viewer holds the SAME GROUPS as a
// member — on the group axis they are indistinguishable — and differs only in
// the capability mask. If roles were expressed on groups alone, this role would
// have been able to prompt the model and spend the operator's subscription.
func TestViewerHoldsTheSameGroupsAsMemberButCannotSpend(t *testing.T) {
	offered := []ctrlproto.Group{
		ctrlproto.GroupConversation,
		ctrlproto.GroupSession,
		ctrlproto.GroupReplay,
	}
	viewer := Principal{Subject: "v", Roles: []Role{RoleViewer}}
	member := Principal{Subject: "m", Roles: []Role{RoleMember}}

	vg, mg := Restrict(offered, viewer), Restrict(offered, member)
	if len(vg) != len(mg) {
		t.Fatalf("viewer groups %v differ from member groups %v — the distinction must live in the mask, not the groups", vg, mg)
	}

	if got := Authority(viewer); got != ctrlproto.CapRead {
		t.Errorf("viewer authority = %b, want read only", got)
	}
	if Authority(member)&ctrlproto.CapSpend == 0 {
		t.Error("member lost spend; a member is expected to be able to converse")
	}
}

// The verbs a viewer must NOT reach, named individually because each was found
// by reading a handler rather than by reading its name.
func TestViewerIsRefusedEverySpendingVerb(t *testing.T) {
	mask := Authority(Principal{Roles: []Role{RoleViewer}})
	for _, m := range []ctrlproto.Method{
		ctrlproto.MethodPrompt,
		ctrlproto.MethodSuggestReply,
		ctrlproto.MethodSideChatAsk,
		ctrlproto.MethodSessionGenerateTitle,
		ctrlproto.MethodSessionsDoctor,
		ctrlproto.MethodBackgroundGenerate,
		ctrlproto.MethodApprove,
	} {
		if m.Permits(mask) {
			t.Errorf("a viewer was permitted %q, which spends", m)
		}
	}
	// ...and the ones it must keep, or the role is useless.
	for _, m := range []ctrlproto.Method{
		ctrlproto.MethodSubscribe,
		ctrlproto.MethodSessionsList,
		ctrlproto.MethodConversationHistory,
		ctrlproto.MethodUsageGet,
	} {
		if !m.Permits(mask) {
			t.Errorf("a viewer was refused %q, which only reads", m)
		}
	}
}

// A principal with no roles must reach nothing on EITHER axis.
func TestZeroPrincipalHasNoAuthority(t *testing.T) {
	if got := Authority(Principal{}); got != 0 {
		t.Fatalf("Authority(zero) = %b, want 0", got)
	}
	if ctrlproto.MethodSubscribe.Permits(Authority(Principal{})) {
		t.Error("the zero principal was permitted a read verb")
	}
}
