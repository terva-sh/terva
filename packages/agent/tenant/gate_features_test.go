package tenant

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// --- the narrowing ---

// A hello feature is a claim about the CARRIER, and under `terva serve` the
// carrier is the supervisor. A child that mounts /stage/ on its own mux is
// describing a server the browser is not talking to.
func TestAFeatureNamingARouteThisHostDoesNotServeIsStripped(t *testing.T) {
	child := ctrlproto.Hello{
		Role:     ctrlproto.RoleServer,
		Features: []string{ctrlproto.FeatureImages, ctrlproto.FeatureStage, ctrlproto.FeatureSharedFiles},
	}

	got := NarrowHello(child, principal(authz.RoleOwner), Carrier{Stage: false}).Features
	if slices.Contains(got, ctrlproto.FeatureStage) {
		t.Errorf("a supervisor that does not mount /stage/ still told the browser it serves Stage: %v", got)
	}

	// The CONTROL, and it is the half that proves this is a decision rather than
	// a blanket strip: on a host that DOES mount /stage/, the child's own answer
	// stands. Without this the test would pass just as well against
	// `Features: nil`.
	got = NarrowHello(child, principal(authz.RoleOwner), Carrier{Stage: true}).Features
	if !slices.Contains(got, ctrlproto.FeatureStage) {
		t.Errorf("a supervisor that mounts /stage/ withheld Stage from the browser: %v", got)
	}
}

// Narrowing features must not become a way to lose the ones that have nothing
// to do with routes — those are gated by the group allowlist, and dropping them
// here would silently remove working affordances.
func TestFeaturesWithNoRouteBehindThemCrossUntouched(t *testing.T) {
	child := ctrlproto.Hello{
		Role: ctrlproto.RoleServer,
		Features: []string{
			ctrlproto.FeatureImages, ctrlproto.FeatureContextTree, ctrlproto.FeatureRestart,
			// Route-backed, but on paths the proxy carries for EVERY tenant:
			// POST /upload and GET /shared/ are both on the allowlist.
			ctrlproto.FeatureAttachments, ctrlproto.FeatureSharedFiles,
		},
	}
	got := NarrowHello(child, principal(authz.RoleOwner), Carrier{}).Features
	for _, want := range child.Features {
		if !slices.Contains(got, want) {
			t.Errorf("%s was dropped by carrier narrowing, and nothing about it depends on the carrier: %v", want, got)
		}
	}
}

// The same rule NarrowHello already obeys for groups: the child's hello is
// narrowed once per connection, so a mutation would let one tenant's host
// decide what every later one sees.
func TestNarrowHelloDoesNotMutateTheChildsFeatures(t *testing.T) {
	child := ctrlproto.Hello{
		Role:     ctrlproto.RoleServer,
		Features: []string{ctrlproto.FeatureStage, ctrlproto.FeatureImages},
	}
	_ = NarrowHello(child, principal(authz.RoleOwner), Carrier{})
	if len(child.Features) != 2 || child.Features[0] != ctrlproto.FeatureStage {
		t.Errorf("NarrowHello mutated the child's features: %v", child.Features)
	}
}

// The operator panel is offered to exactly the people /supervisor lets in:
// those whose grant includes the tenants group, which today is the owner.
// The group itself never crosses (see the group census below); one feature
// does.
func TestOnlyAPersonWhoMayOpenTheOperatorPanelIsOfferedIt(t *testing.T) {
	child := ctrlproto.Hello{Role: ctrlproto.RoleServer, Groups: []ctrlproto.Group{ctrlproto.GroupConversation}}
	for _, role := range []authz.Role{authz.RoleOwner, authz.RoleOperator, authz.RoleMember, authz.RoleViewer} {
		got := NarrowHello(child, principal(role), Carrier{})
		offered := slices.Contains(got.Features, ctrlproto.FeatureOperatorPanel)
		may := slices.Contains(authz.Grant(principal(role)), ctrlproto.GroupTenants)
		if offered != may {
			t.Errorf("%s: operator panel offered = %v, but the page would let them in = %v", role, offered, may)
		}
		if slices.Contains(got.Groups, ctrlproto.GroupTenants) {
			t.Errorf("%s: the tenants group crossed to a tenant connection: %v", role, got.Groups)
		}
	}
	// The positive control: the owner must actually be offered it, or the loop
	// above passes against a NarrowHello that never adds the feature at all.
	if !slices.Contains(NarrowHello(child, principal(authz.RoleOwner), Carrier{}).Features, ctrlproto.FeatureOperatorPanel) {
		t.Fatal("the owner was not offered the operator panel")
	}
}

// A child is a tenant's own daemon, and a tenant controls its config. Its claim
// to the feature must not reach a person the supervisor would refuse.
func TestAChildCannotClaimTheOperatorPanel(t *testing.T) {
	child := ctrlproto.Hello{Role: ctrlproto.RoleServer, Features: []string{ctrlproto.FeatureOperatorPanel}}
	got := NarrowHello(child, principal(authz.RoleMember), Carrier{Stage: true}).Features
	if slices.Contains(got, ctrlproto.FeatureOperatorPanel) {
		t.Errorf("a member was offered the operator panel because the child claimed it: %v", got)
	}
	got = NarrowHello(child, principal(authz.RoleOwner), Carrier{}).Features
	if n := countOf(got, ctrlproto.FeatureOperatorPanel); n != 1 {
		t.Errorf("the owner's hello names the operator panel %d times, want once: %v", n, got)
	}
}

func countOf(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}

// --- the censuses, read from source so they cannot go stale ---

// notRouteBacked names every ctrlproto feature that is NOT backed by an HTTP
// route, and therefore crosses the proxy on the group allowlist alone.
//
// A feature belongs here when a browser can use it over /ws and nothing else:
// the supervisor forwards the conversation, session, control and replay groups,
// so anything those verbs carry works wherever the socket does.
var notRouteBacked = map[string]string{
	ctrlproto.FeatureImages:          "inbound images ride the prompt frame",
	ctrlproto.FeatureResolveEvents:   "events on the socket",
	ctrlproto.FeatureContextTree:     "context.get / context.node, session group",
	ctrlproto.FeatureImageData:       "image bytes ride the frame",
	ctrlproto.FeatureFilesList:       "files.list, session group",
	ctrlproto.FeatureGenerateTitle:   "sessions.generate_title, session group",
	ctrlproto.FeatureWorkspaceEvents: "events on the socket",
	ctrlproto.FeatureHistoryWindow:   "conversation.history, session group",
	ctrlproto.FeatureRestart:         "control.restart, control group — restarts the CHILD, which is the tenant's own daemon",
	ctrlproto.FeatureFleetHub:        "names the daemon's role; the member listener is its own socket, not a route the supervisor carries",
}

// A census over the ctrlproto source itself: every Feature constant must be
// either route-backed (and so answerable by a Carrier) or explicitly named as
// not route-backed.
//
// 🔑 Read from the FILE rather than from a hand-typed list, because the
// hand-typed one next door had already gone stale: TestEveryGroupIsADecidedCase
// enumerates six groups and ctrlproto has seven — GroupTenants was added and
// the census that exists to catch an undecided group did not catch it. A list
// that must be updated by the same person who forgot to update the other list
// is not a guard.
func TestEveryFeatureIsADecidedCase(t *testing.T) {
	all := constNamesFrom(t, "Feature")
	if len(all) < 10 {
		t.Fatalf("only %d Feature constants found — the source scan is broken, not the feature list", len(all))
	}
	backed := routeBackedFeatures(Carrier{})
	for name, value := range all {
		_, isRouted := backed[value]
		_, isPlain := notRouteBacked[value]
		switch {
		case isRouted && isPlain:
			t.Errorf("%s (%q) is BOTH route-backed and named as needing no route — gate.go and this census disagree", name, value)
		case !isRouted && !isPlain:
			t.Errorf("%s (%q) is undecided: add it to routeBackedFeatures in gate.go if a supervisor route serves it, or to notRouteBacked here with the verb that does", name, value)
		}
	}
	// The reverse check, so an entry cannot outlive the constant it excuses.
	values := map[string]bool{}
	for _, v := range all {
		values[v] = true
	}
	for v := range notRouteBacked {
		if !values[v] {
			t.Errorf("notRouteBacked excuses %q, which is not a ctrlproto feature any more", v)
		}
	}
	for v := range backed {
		if !values[v] {
			t.Errorf("routeBackedFeatures names %q, which is not a ctrlproto feature any more", v)
		}
	}
}

// The group census, rebuilt on the same source scan — see the comment above for
// why the hand-typed one it replaces could not have caught GroupTenants.
func TestEveryGroupIsADecidedCaseFromSource(t *testing.T) {
	all := constNamesFrom(t, "Group")
	if len(all) < 6 {
		t.Fatalf("only %d Group constants found — the source scan is broken, not the group list", len(all))
	}
	// Named refusals: absent from the allowlist ON PURPOSE, with the reason in
	// gate.go.
	withheld := map[ctrlproto.Group]string{
		ctrlproto.GroupAuth:    "one shared provider credential; a tenant could replace what everyone else spends",
		ctrlproto.GroupSecrets: "reports on the key that opens everything",
		// 🚨 The one the old census missed. Absent is not merely the safe
		// answer here, it is the DESIGN — D8's non-existence boundary. A tenant
		// must not be able to name tenants.*, never mind be refused it.
		ctrlproto.GroupTenants: "served on the supervisor's own connection only (D8)",
	}
	for name, value := range all {
		g := ctrlproto.Group(value)
		_, named := withheld[g]
		switch {
		case forwardable[g] && named:
			t.Errorf("%s is BOTH forwardable and named as withheld", name)
		case !forwardable[g] && !named:
			t.Errorf("%s (%q) is neither forwardable nor deliberately withheld — decide it in gate.go", name, value)
		}
	}
	if len(forwardable)+len(withheld) != len(all) {
		t.Errorf("the group census is stale: %d forwardable + %d withheld != %d defined in ctrlproto",
			len(forwardable), len(withheld), len(all))
	}
}

// constNamesFrom reads ctrlproto's hello.go and returns every constant whose
// name starts with prefix, as name → string value.
//
// Parsing the source rather than reflecting because these are untyped and typed
// string CONSTANTS: they do not exist at runtime as a collection, so there is
// nothing to reflect over. The file is the only enumeration there is.
func constNamesFrom(t *testing.T, prefix string) map[string]string {
	t.Helper()
	const path = "../ctrlproto/hello.go"
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[name.Name] = strings.Trim(lit.Value, "`\"")
			}
		}
	}
	return out
}
