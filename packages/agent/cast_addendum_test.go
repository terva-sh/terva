package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestCastAddendum(t *testing.T) {
	got := castAddendum(map[string]string{"innkeeper": "x", "aava": "y"})
	if !strings.Contains(got, "actor_spawn") {
		t.Error("addendum should name the actor_spawn tool")
	}
	// Cast members listed, sorted.
	ai := strings.Index(got, "aava")
	ii := strings.Index(got, "innkeeper")
	if ai < 0 || ii < 0 || ai > ii {
		t.Errorf("cast should list both names, sorted (aava before innkeeper): %q", got)
	}
	// Pacing guidance (the soft interaction budget) is present.
	if !strings.Contains(got, "not for every line") {
		t.Error("addendum should include pacing guidance")
	}
}

// The cast addendum is advertised only in --play with a declared cast.
func TestResolve_CastAddendumGatedByPlayAndCast(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := SaveConfig(Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)

	hasCast := func(t *testing.T, exp string, cast map[string]string) bool {
		t.Helper()
		r, err := Resolve(Args{CWD: dir, Experience: exp, Cast: cast}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.systemSegments {
			if s.Source == "cast" {
				return true
			}
		}
		return false
	}

	cast := map[string]string{"aava": "examples/cards/aava-v2.png"}
	if !hasCast(t, ExperiencePlay, cast) {
		t.Error("--play with a cast should carry the cast addendum")
	}
	if hasCast(t, ExperiencePlay, nil) {
		t.Error("--play without a cast should NOT carry the cast addendum")
	}
	if hasCast(t, "", cast) {
		t.Error("coding mode should never carry the cast addendum")
	}
	if hasCast(t, ExperienceChat, cast) {
		t.Error("--chat should not carry the cast addendum (no tools to voice with)")
	}

	// --no-tools suppresses the whole skin: a tool-less session must not be
	// told about a tool it doesn't have.
	r, err := Resolve(Args{CWD: dir, Experience: ExperiencePlay, Cast: cast, NoTools: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.systemSegments {
		if s.Source == "cast" {
			t.Error("--play --no-tools should NOT carry the cast addendum")
		}
	}
}

// castSkinActive is the single gate for both actor_spawn injection sites (the
// tool and its addendum): --play only, never --no-tools. --no-workspace-tools
// does NOT suppress it — actor_spawn is the play skin, not a workspace tool.
func TestCastSkinActive(t *testing.T) {
	cases := []struct {
		name string
		args Args
		want bool
	}{
		{"play", Args{Experience: ExperiencePlay}, true},
		{"play no-tools", Args{Experience: ExperiencePlay, NoTools: true}, false},
		{"play no-workspace-tools", Args{Experience: ExperiencePlay, NoWorkspaceTools: true}, true},
		{"chat", Args{Experience: ExperienceChat}, false},
		{"coding", Args{}, false},
		{"coding no-tools", Args{NoTools: true}, false},
	}
	for _, c := range cases {
		if got := castSkinActive(c.args); got != c.want {
			t.Errorf("%s: castSkinActive = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestKertojaPersonaIsImmersiveGM(t *testing.T) {
	p, err := ResolvePersona("kertoja")
	if err != nil {
		t.Fatalf("resolve kertoja: %v", err)
	}
	if p.Name != "Kertoja" {
		t.Errorf("name = %q, want Kertoja", p.Name)
	}
	if !p.Immersive {
		t.Error("the director persona should be immersive (its charter owns the prompt)")
	}
	if !strings.Contains(strings.ToLower(p.Charter), "narrator") {
		t.Errorf("charter should establish the narrator/GM identity: %q", p.Charter)
	}
}
