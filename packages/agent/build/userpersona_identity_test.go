package build

import (
	"strings"
	"testing"
)

// The likeliest way to fill the persona form in: pick your pronouns from the
// dropdown (one click) and skip the bio (work). The whole identity frame was
// gated on the DESCRIPTION being non-empty, so that user's pronouns reached the
// model as nothing at all — not the pronouns, not the anti-inference steer the
// frame exists to carry. Silent, and in exactly the configuration most likely to
// matter to the person who bothered to state them.
func TestIdentityRendersWithoutADescription(t *testing.T) {
	got := userPersonaFrame("Kira", "she/her", "Woman", "")
	if !strings.Contains(got, "she/her") {
		t.Errorf("pronouns missing when no description is set:\n%s", got)
	}
	if !strings.Contains(got, "Kira's gender: Woman.") {
		t.Errorf("gender missing when no description is set:\n%s", got)
	}
	// And it must not leave a dangling empty line where the bio would go.
	if strings.Contains(got, "):\n\n") {
		t.Errorf("empty description left a dangling blank line:\n%q", got)
	}
}

// Gender stated, pronouns not. The old text asserted the gender and then, in the
// same breath, told the model not to assume one — so it either ignored the
// contradiction or ignored the gender.
func TestGenderWithoutPronounsDoesNotContradictItself(t *testing.T) {
	got := strings.Join(UserPersonaIdentity("Kira", "Woman", ""), " ")
	if !strings.Contains(got, "Kira's gender: Woman.") {
		t.Fatalf("stated gender dropped: %q", got)
	}
	if strings.Contains(got, "do not assume a gender") {
		t.Errorf("states the gender and then forbids assuming one:\n%s", got)
	}
	if !strings.Contains(got, "pronouns are not stated") {
		t.Errorf("should say the pronouns specifically are unstated: %q", got)
	}
}

// "Prefer not to say" is an answer, not a gender. Restating it as one tells the
// model nothing and reads as a value; declining to state it is precisely the
// case the steer is for.
func TestPreferNotToSayIsTreatedAsWithheld(t *testing.T) {
	got := strings.Join(UserPersonaIdentity("Kira", "Prefer not to say", ""), " ")
	if strings.Contains(got, "gender: Prefer not to say") {
		t.Errorf("echoed the refusal back as a gender value:\n%s", got)
	}
	if !strings.Contains(got, "do not assume a gender") {
		t.Errorf("a withheld gender must still steer the model off inventing one: %q", got)
	}
}

// Stated identity still wins, and the steer stays away when it would contradict.
func TestStatedIdentityIsUsedNotSteeredAgainst(t *testing.T) {
	got := strings.Join(UserPersonaIdentity("Kira", "Woman", "she/her"), " ")
	if !strings.Contains(got, "Refer to Kira with she/her pronouns.") {
		t.Errorf("stated pronouns not used: %q", got)
	}
	if strings.Contains(got, "do not assume") || strings.Contains(got, "not stated") {
		t.Errorf("steered against an identity the user explicitly stated: %q", got)
	}
}

// Nothing bound at all: still steer, never invent.
func TestNoIdentityStillSteers(t *testing.T) {
	for _, name := range []string{"", "Kira"} {
		got := strings.Join(UserPersonaIdentity(name, "", ""), " ")
		if !strings.Contains(got, "do not assume a gender or pronouns") {
			t.Errorf("name=%q: no anti-inference steer: %q", name, got)
		}
	}
}

// The compact side-channel form carries identity too — that is the whole reason
// it exists. Empty only when genuinely nothing is bound, so a caller's own
// "not specified" fallback still fires.
func TestBriefCarriesIdentityAndIsEmptyOnlyWhenUnbound(t *testing.T) {
	got := UserPersonaBrief("Kira", "a wary courier", "Woman", "she/her")
	for _, want := range []string{"Kira: a wary courier", "she/her", "gender: Woman"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q:\n%s", want, got)
		}
	}
	if UserPersonaBrief("", "", "", "") != "" {
		t.Error("an unbound persona should render empty so the caller can say 'not specified'")
	}
	// Pronouns but no name/description is still worth saying.
	if b := UserPersonaBrief("", "", "", "they/them"); !strings.Contains(b, "they/them") {
		t.Errorf("pronouns-only persona rendered as %q", b)
	}
}

// The gate, not just the frame. PerTurnContext decides whether the identity
// frame is emitted AT ALL, and it used to ask only "is there a description?".
// A direct userPersonaFrame test cannot see that decision — this one drives the
// real per-turn tail the model receives.
func TestPerTurnTailEmitsIdentityWithoutADescription(t *testing.T) {
	// Pronouns only, no bio: the one-click configuration.
	r := &Resolved{userDesc: &NoteRecord{}, userName: "Kira", userPronouns: "she/her"}
	got := r.PerTurnContext(nil)()
	if !strings.Contains(got, "she/her") {
		t.Errorf("pronouns never reached the per-turn tail with no description set:\n%q", got)
	}

	// Gender only, no bio.
	r2 := &Resolved{userDesc: &NoteRecord{}, userName: "Kira", userGender: "Woman"}
	if got := r2.PerTurnContext(nil)(); !strings.Contains(got, "gender: Woman") {
		t.Errorf("gender never reached the per-turn tail with no description set:\n%q", got)
	}

	// Nothing bound at all still renders nothing — the gate must not become
	// "always on", which would inject a steer into every session that has a
	// persona name and nothing else.
	r3 := &Resolved{userDesc: &NoteRecord{}, userName: "Kira"}
	if got := r3.PerTurnContext(nil)(); got != "" {
		t.Errorf("a name with no identity and no bio should render nothing, got %q", got)
	}
}
