package build

import (
	"strings"
	"testing"
	"time"
)

// The whole point of the split, asserted directly: the identity says who the
// agent is and nothing about what runs it. If "terva" ever reappears here, a
// Claude Code worker gets told it lives in a harness it has never heard of —
// and the leak is silent, because the worker still runs.
func TestIdentityNamesNoHarness(t *testing.T) {
	for _, tc := range []struct{ name, persona, experience string }{
		{"default coding persona", "", ""},
		{"named coding persona", "Aava", ""},
		{"chat", "Aava", ExperienceChat},
		{"play", "Aava", ExperiencePlay},
		{"default persona in chat", "", ExperienceChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intro := identityIntro(tc.persona, tc.experience)
			if strings.Contains(strings.ToLower(intro), "terva") {
				t.Errorf("identity names the harness — it must be portable:\n%s", intro)
			}
			if intro == "" {
				t.Error("identity is empty; the agent must at least know who it is")
			}
		})
	}
}

// The vessel is the other half: it exists to carry terva's framing, so it had
// better contain it. A vessel that lost its content would silently strip the
// harness from terva's *own* agents.
func TestVesselCarriesTheHarness(t *testing.T) {
	for _, tc := range []struct{ name, persona, experience string }{
		{"default coding persona", "", ""},
		{"named coding persona", "Aava", ""},
		{"chat", "Aava", ExperienceChat},
		{"play", "Aava", ExperiencePlay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if v := vesselFraming(tc.persona, tc.experience); !strings.Contains(strings.ToLower(v), "terva") {
				t.Errorf("vessel does not name terva; terva's own agents lose the framing:\n%s", v)
			}
		})
	}
}

// Splitting must not change what terva's own agents are told — only how it is
// labeled. Both halves land in the system prompt, in order, for a normal run.
func TestIdentityAndVesselBothReachTervasOwnAgents(t *testing.T) {
	segs := SystemSegments(SystemPromptOpts{
		Now:         time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		PersonaName: "Mieli",
	})
	var order []string
	for _, s := range segs {
		order = append(order, s.Source)
	}
	idx := func(src string) int {
		for i, s := range order {
			if s == src {
				return i
			}
		}
		return -1
	}
	i, v := idx(SourceIdentityIntro), idx(SourceVessel)
	if i < 0 {
		t.Fatalf("no identity segment; got %v", order)
	}
	if v < 0 {
		t.Fatalf("no vessel segment — terva's own agent lost its framing; got %v", order)
	}
	if v != i+1 {
		t.Errorf("vessel should sit immediately after the identity (they were one paragraph); got %v", order)
	}
}

// A card or persona that supplies its own intro owns the agent's identity
// outright, and terva's branding has never appeared beside it. The split must
// not sneak a vessel in behind one — that would leak terva onto a character.
func TestIntroOverrideGetsNoVessel(t *testing.T) {
	segs := SystemSegments(SystemPromptOpts{
		Now:           time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		Experience:    ExperiencePlay,
		IntroOverride: "You are Aava, a knife-sharp smuggler.",
		IntroSource:   SourceCardSystem,
	})
	for _, s := range segs {
		if s.Source == SourceVessel {
			t.Fatalf("a card's identity got terva's vessel bolted on:\n%s", s.Text)
		}
		if strings.Contains(strings.ToLower(s.Text), "terva") {
			t.Errorf("terva leaked into a card's prompt via %q:\n%s", s.Source, s.Text)
		}
	}
}

// The card path and the immersive identity used to carry hand-maintained
// near-copies of the same framing, and they had drifted ("a world that you
// perceive" vs "a world you perceive"). They share one string now; this pins
// that they cannot drift apart again.
func TestImmersiveIdentityReusesTheCardFraming(t *testing.T) {
	for _, exp := range []string{ExperienceChat, ExperiencePlay} {
		framing := experienceFraming(exp)
		intro := identityIntro("Aava", exp)
		if !strings.Contains(intro, framing) {
			t.Errorf("%s identity does not reuse the shared framing — the copies have drifted again\nintro:   %s\nframing: %s", exp, intro, framing)
		}
	}
}
