package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// .terva/cast.json is honored only in a trusted project, and --cast overrides it
// per name.
func TestMergedCastRefs(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".terva", "cast.json"),
		[]byte(`{"innkeeper":"cards/innkeeper.png","guard":"guard"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Untrusted: the project cast is ignored; only --cast applies.
	untrusted := MergedCastRefs(Args{Cast: map[string]string{"aava": "aava.png"}}, dir, false)
	if len(untrusted) != 1 || untrusted["aava"] != "aava.png" {
		t.Errorf("untrusted project cast must be dropped: %v", untrusted)
	}

	// Trusted: project cast + --cast, with --cast winning on a name collision.
	trusted := MergedCastRefs(Args{Cast: map[string]string{"guard": "override.md", "aava": "aava.png"}}, dir, true)
	if trusted["innkeeper"] != "cards/innkeeper.png" {
		t.Errorf("project cast member missing: %v", trusted)
	}
	if trusted["guard"] != "override.md" {
		t.Errorf("--cast should override the project file: %v", trusted)
	}
	if trusted["aava"] != "aava.png" {
		t.Errorf("--cast-only member missing: %v", trusted)
	}

	// No file, no --cast → empty.
	if got := MergedCastRefs(Args{}, testsupport.TempDir(t), true); len(got) != 0 {
		t.Errorf("no declarations should yield an empty cast: %v", got)
	}
}

// A malformed cast.json entry with an empty/whitespace ref must not become a
// dispatchable, identity-less actor (CastMember's invariant is exactly one of
// Persona/Card set). The --cast parser already rejects these; the project file
// must be held to the same rule.
func TestMergedCastRefs_DropsEmptyRefs(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".terva", "cast.json"),
		[]byte(`{"ghost":"","blank":"   ","guard":"guard"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := MergedCastRefs(Args{}, dir, true)
	if len(got) != 1 || got["guard"] != "guard" {
		t.Errorf("empty-ref entries should be dropped, got %v", got)
	}
}

func TestParseArgs_Cast(t *testing.T) {
	a, err := ParseArgs([]string{"--cast", "innkeeper=cards/innkeeper.png", "--cast", "guard=guard"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Cast["innkeeper"] != "cards/innkeeper.png" || a.Cast["guard"] != "guard" {
		t.Errorf("cast = %v", a.Cast)
	}
	if _, err := ParseArgs([]string{"--cast", "noeq"}); err == nil {
		t.Error("--cast without = should error")
	}
	if _, err := ParseArgs([]string{"--cast", "=onlyref"}); err == nil {
		t.Error("--cast with empty name should error")
	}
}

// --cast is never a silent no-op: with no Experience chosen it implies --play
// (mirroring --card implying --chat); under --chat — where nothing would ever
// consume the cast — it errors loudly.
func TestParseArgs_CastImpliesPlay(t *testing.T) {
	a, err := ParseArgs([]string{"--cast", "guard=kertoja"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Experience != ExperiencePlay {
		t.Errorf("--cast alone should imply --play, got %q", a.Experience)
	}

	// --card --cast lands in --play (cast implication runs first), not --chat.
	a, err = ParseArgs([]string{"--card", "x.png", "--cast", "guard=kertoja"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Experience != ExperiencePlay {
		t.Errorf("--card --cast should imply --play, got %q", a.Experience)
	}

	if _, err := ParseArgs([]string{"--chat", "--cast", "guard=kertoja"}); err == nil {
		t.Error("--chat --cast should error (the cast would be silently ignored)")
	}

	// Explicit --play stays --play.
	a, err = ParseArgs([]string{"--play", "--cast", "guard=kertoja"})
	if err != nil || a.Experience != ExperiencePlay {
		t.Errorf("--play --cast: exp=%q err=%v", a.Experience, err)
	}
}

func TestBuildActorCast(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "cards"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Existence is what ref validation checks; content is parsed at dispatch.
	for _, p := range []string{filepath.Join(dir, "cards", "innkeeper.png"), filepath.Join(dir, "door.json")} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cast, err := BuildActorCast(map[string]string{
		"innkeeper": "cards/innkeeper.png",           // relative card → absolutized
		"guard":     "kertoja",                       // Persona name (embedded)
		"door":      filepath.Join(dir, "door.json"), // absolute card, preserved
	}, dir)
	if err != nil {
		t.Fatal(err)
	}

	if got := cast["innkeeper"]; got.Card != filepath.Join(dir, "cards", "innkeeper.png") || got.Persona != "" {
		t.Errorf("innkeeper should be an absolutized card: %+v", got)
	}
	if got := cast["guard"]; got.Persona != "kertoja" || got.Card != "" {
		t.Errorf("guard should be a persona: %+v", got)
	}
	if got := cast["door"]; got.Card != filepath.Join(dir, "door.json") {
		t.Errorf("absolute card path should be preserved: %q", got.Card)
	}
	if c, err := BuildActorCast(nil, dir); err != nil || c != nil {
		t.Error("empty declaration → nil cast, no error")
	}
}

// Cast refs are validated at launch: a typo'd Persona or a missing card file
// fails NOW naming the offending NAME=REF, not opaquely mid-scene ("the actor
// exited before responding") on the actor's first dispatch.
func TestBuildActorCast_ValidatesRefsAtLaunch(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	if _, err := BuildActorCast(map[string]string{"ghost": "no-such-persona"}, dir); err == nil || !strings.Contains(err.Error(), "ghost=no-such-persona") {
		t.Errorf("unknown persona should fail at launch naming NAME=REF, got: %v", err)
	}
	if _, err := BuildActorCast(map[string]string{"aava": "cards/missing.png"}, dir); err == nil || !strings.Contains(err.Error(), "aava=cards/missing.png") {
		t.Errorf("missing card should fail at launch naming NAME=REF, got: %v", err)
	}
}
