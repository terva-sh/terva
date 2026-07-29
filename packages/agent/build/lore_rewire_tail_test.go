package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// RewireLoreContext installs a WHOLE context provider, so anything it does not
// carry is a thing it takes away.
//
// It used to install the run's freshly-resolved tail bare. The extension cards
// and the task card that the session build had stacked on top were gone from
// that moment on — for the rest of the session, in every host that re-derives,
// and with nothing to show for it: what remained was a perfectly valid tail,
// just two layers short. The model stopped being told what work it had open and
// stopped seeing every extension's context.
//
// It is reached by more than a trust flip. In the daemon reloadLore runs it on a
// lore edit and on a user-persona change too, so the common way to hit it was
// editing a lorebook entry.
func TestRewireLoreContextKeepsTheLiveCards(t *testing.T) {
	ag, args := loreAgent(t)

	ctrl := tasktool.New(tasks.NewStore(nil, "agent"))
	if _, err := ctrl.Store().Create([]tasks.CreateSpec{{Title: "ship the thing", ActiveForm: "shipping the thing"}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	tail := EphemeralTail{Ext: func() string { return "EXT-CARD" }, Tasks: ctrl}

	WireEphemeralTail(ag, tail)
	before := ag.ContextPreview()
	for _, want := range []string{"EXT-CARD", "ship the thing", "PROJECT-LORE-MARKER"} {
		if !strings.Contains(before, want) {
			t.Fatalf("precondition: %q missing from the built tail:\n%s", want, before)
		}
	}

	if rr := RewireLoreContext(ag, args, tail); rr == nil {
		t.Fatal("RewireLoreContext reported no resolve")
	}

	after := ag.ContextPreview()
	for _, want := range []string{"EXT-CARD", "ship the thing"} {
		if !strings.Contains(after, want) {
			t.Errorf("%q is gone from the tail after a re-derivation — the model loses it "+
				"for the rest of the session:\n%s", want, after)
		}
	}
	if !strings.Contains(after, "PROJECT-LORE-MARKER") {
		t.Errorf("the re-derived lore itself is missing, which is what this call is FOR:\n%s", after)
	}
	// The whole point of the re-derivation: it must still be a re-derivation.
	if before != after {
		t.Logf("tail changed across the rewire (expected — fresh records):\n%s", after)
	}
}

// The order the model reads is: task card, extension cards, then the run's own
// tail — with a card's post_history_instructions last of everything. A rewire
// that restored the layers in the wrong order would pass a "nothing is missing"
// check while moving PHI out of its after-history position.
func TestRewireLoreContextKeepsTheCardOrder(t *testing.T) {
	ag, args := loreAgent(t)

	ctrl := tasktool.New(tasks.NewStore(nil, "agent"))
	if _, err := ctrl.Store().Create([]tasks.CreateSpec{{Title: "ship the thing", ActiveForm: "shipping the thing"}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	tail := EphemeralTail{Ext: func() string { return "EXT-CARD" }, Tasks: ctrl}
	WireEphemeralTail(ag, tail)

	wantOrder := []string{"ship the thing", "EXT-CARD", "PROJECT-LORE-MARKER"}
	assertOrder := func(when, got string) {
		t.Helper()
		at := -1
		for _, marker := range wantOrder {
			i := strings.Index(got, marker)
			if i < 0 {
				t.Fatalf("%s: %q missing:\n%s", when, marker, got)
			}
			if i < at {
				t.Errorf("%s: %q is out of order (want %v):\n%s", when, marker, wantOrder, got)
			}
			at = i
		}
	}
	assertOrder("at build", ag.ContextPreview())
	RewireLoreContext(ag, args, tail)
	assertOrder("after the rewire", ag.ContextPreview())
}

// reloadLore runs on every lore edit, so the rewire is not a once-per-session
// event and has to be idempotent in BOTH directions: the cards must still be
// there after the fourth edit, and there must still be only one of each.
//
// Dropping them is the bug this file is about. Accumulating them is the failure
// mode of the other way to write the fix — composing onto whatever provider is
// currently installed rather than onto the freshly-resolved run tail — which
// would grow a duplicate task card and a duplicate copy of every extension's
// context per edit, silently eating the context window.
func TestRepeatedRewiresDoNotStackTheCards(t *testing.T) {
	ag, args := loreAgent(t)

	ctrl := tasktool.New(tasks.NewStore(nil, "agent"))
	if _, err := ctrl.Store().Create([]tasks.CreateSpec{{Title: "ship the thing", ActiveForm: "shipping the thing"}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	tail := EphemeralTail{Ext: func() string { return "EXT-CARD" }, Tasks: ctrl}
	WireEphemeralTail(ag, tail)

	for i := 0; i < 3; i++ {
		RewireLoreContext(ag, args, tail)
	}
	got := ag.ContextPreview()
	if n := strings.Count(got, "EXT-CARD"); n != 1 {
		t.Errorf("extension card appears %d times after three rewires, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, "ship the thing"); n != 1 {
		t.Errorf("task card appears %d times after three rewires, want 1:\n%s", n, got)
	}
}

// loreAgent builds a trusted project with one keyword-triggered entry and an
// agent whose messages fire it, so the run has a tail of its own for the cards
// to sit on top of.
func loreAgent(t *testing.T) (*core.Agent, Args) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	cwd := testsupport.TempDir(t)
	dir := filepath.Join(cwd, ".terva", "lore")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := "---\nname: dragons\nkeys: [dragon]\n---\nDragons hoard PROJECT-LORE-MARKER.\n"
	if err := os.WriteFile(filepath.Join(dir, "dragons.md"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.TrustPath(cwd, false); err != nil {
		t.Fatal(err)
	}

	args := Args{CWD: cwd, Provider: "openai", Model: "gpt-5"}
	r, err := Resolve(args, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent()
	ag.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "tell me about the dragon"}},
	}})
	return ag, args
}
