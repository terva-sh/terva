package workspace

import (
	"strings"
	"testing"
)

// Suggest and the routed voice call are the two side-channel prompts that write
// ABOUT or AS the player — a character addressing you, a line drafted in your
// voice. They were also the two that knew least about you: each hand-rolled its
// own "Name: description" line and dropped gender and pronouns on the floor.
//
// That is the exact input shape that produced the misgendering the main turn's
// identity frame was built to fix, so these paths could reintroduce it in a
// session where the main turn had it right.
func TestSuggestPromptCarriesPlayerIdentity(t *testing.T) {
	player := userPersona{Name: "Kira", Description: "a wary courier", Gender: "Woman", Pronouns: "she/her"}

	for _, tc := range []struct {
		name   string
		target suggestTarget
	}{
		{"player draft", suggestTarget{}},
		{"actor draft", suggestTarget{kind: "actor", name: "Elira"}},
		{"narrator draft", suggestTarget{kind: "narrator"}},
	} {
		sys := renderSuggestSystem(tc.target, player, nil, "", nil)
		if !strings.Contains(sys, "she/her") {
			t.Errorf("%s: the player's pronouns never reach the drafter:\n%s", tc.name, sys)
		}
		if !strings.Contains(sys, "a wary courier") {
			t.Errorf("%s: lost the description while adding identity", tc.name)
		}
	}
}

func TestVoicePromptCarriesPlayerIdentity(t *testing.T) {
	player := userPersona{Name: "Kira", Description: "a wary courier", Gender: "Woman", Pronouns: "she/her"}
	sys := renderVoiceSystem("Elira", nil, player, "Kertoja", nil, "", nil, "Kira")
	if !strings.Contains(sys, "she/her") {
		t.Errorf("a routed character is told who the player is but not how to refer to them:\n%s", sys)
	}
	if !strings.Contains(sys, "a wary courier") {
		t.Error("lost the description while adding identity")
	}
}

// An unbound persona must still render the caller's own "not specified" wording
// rather than a bare steer with nobody to steer about.
func TestUnboundPlayerKeepsTheNotSpecifiedFallback(t *testing.T) {
	if sys := renderSuggestSystem(suggestTarget{}, userPersona{}, nil, "", nil); !strings.Contains(sys, "not specified") {
		t.Errorf("suggest lost its unbound fallback:\n%s", sys)
	}
	if sys := renderVoiceSystem("Elira", nil, userPersona{}, "Kertoja", nil, "", nil, "Me"); !strings.Contains(sys, "not specified") {
		t.Errorf("voice lost its unbound fallback:\n%s", sys)
	}
}

// Every prose-writing side-channel prompt must name the tense. Three of the five
// never mentioned it, and the routed narrator drifted to present tense in a
// past-tense scene even though it did — so all five now say to read the tense off
// the transcript rather than defaulting.
func TestSideChannelPromptsPinTheTense(t *testing.T) {
	prompts := map[string]string{
		"suggest player":   renderSuggestSystem(suggestTarget{}, userPersona{Name: "Kira"}, nil, "", nil),
		"suggest actor":    renderSuggestSystem(suggestTarget{kind: "actor", name: "Elira"}, userPersona{Name: "Kira"}, nil, "", nil),
		"suggest narrator": renderSuggestSystem(suggestTarget{kind: "narrator"}, userPersona{Name: "Kira"}, nil, "", nil),
		"voice actor":      renderVoiceSystem("Elira", nil, userPersona{Name: "Kira"}, "Kertoja", nil, "", nil, "Kira"),
		"voice narrator":   renderVoiceSystem("", nil, userPersona{Name: "Kira"}, "Kertoja", nil, "", nil, "Kira"),
	}
	for name, sys := range prompts {
		if !strings.Contains(sys, "tense") {
			t.Errorf("%s: prompt never mentions tense:\n%s", name, sys)
		}
		if !strings.Contains(sys, "defaulting to present tense") {
			t.Errorf("%s: does not name the actual failure mode (defaulting to present)", name)
		}
	}
}
