package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/card"
)

func greetingCard() card.Card {
	return card.Card{
		Name:               "Kobeni",
		FirstMes:           "H-hello...",
		AlternateGreetings: []string{"You again.", "Oh! It's you."},
	}
}

// Existing greetings are editable, addressed one at a time, and each carries
// its own current text as the proposal's Before.
func TestDoctorFieldsExposesEachGreeting(t *testing.T) {
	f := doctorFields(greetingCard())
	if got := f["alternate_greetings[0]"]; got != "You again." {
		t.Errorf("greeting 0 = %q, want the first greeting", got)
	}
	if got := f["alternate_greetings[1]"]; got != "Oh! It's you." {
		t.Errorf("greeting 1 = %q, want the second greeting", got)
	}
}

// The append slot is what makes CREATE possible: one index past the end, empty,
// but PRESENT in the map — presence is what the allow-list check tests.
func TestDoctorFieldsOffersExactlyOneAppendSlot(t *testing.T) {
	f := doctorFields(greetingCard())
	v, ok := f["alternate_greetings[2]"]
	if !ok {
		t.Fatal("no append slot; the doctor could only rewrite greetings, never add one")
	}
	if v != "" {
		t.Errorf("append slot = %q, want empty", v)
	}
	// Exactly one. Two slots let an author accept the far one and decline the
	// near one, leaving a hole in the array.
	if _, ok := f["alternate_greetings[3]"]; ok {
		t.Error("a second append slot exists; accepting out of order would leave a gap")
	}
}

// A card with no greetings at all is the one most in need of the feature, so
// the slot has to be there before there is anything to index.
func TestDoctorFieldsOffersASlotOnACardWithNoGreetings(t *testing.T) {
	f := doctorFields(card.Card{Name: "Bare"})
	if _, ok := f["alternate_greetings[0]"]; !ok {
		t.Fatal("a card with no greetings got no append slot")
	}
}

// The allow-list is enforced by map presence, so an out-of-range index must be
// absent — otherwise a model hallucinating [7] would write past the end.
func TestOutOfRangeGreetingIsNotEditable(t *testing.T) {
	f := doctorFields(greetingCard())
	for _, field := range []string{"alternate_greetings[3]", "alternate_greetings[99]", "alternate_greetings[-1]", "alternate_greetings[]", "alternate_greetings"} {
		if _, ok := f[field]; ok {
			t.Errorf("%q is editable; it should be out of range", field)
		}
	}
}

func TestGreetingIndexRoundTrips(t *testing.T) {
	for _, i := range []int{0, 1, 12} {
		got, ok := greetingIndex(greetingField(i))
		if !ok || got != i {
			t.Errorf("greetingIndex(greetingField(%d)) = %d,%v", i, got, ok)
		}
	}
	for _, bad := range []string{"first_mes", "alternate_greetings", "alternate_greetings[]", "alternate_greetings[x]", "alternate_greetings[-1]", "alternate_greetings[0"} {
		if _, ok := greetingIndex(bad); ok {
			t.Errorf("greetingIndex(%q) accepted a non-greeting field", bad)
		}
	}
}

// The model can only propose what the prompt shows it. A greeting absent from
// the card dump is one the doctor cannot edit, whatever the allow-list says.
func TestPromptShowsGreetingsUnderTheirProposalNames(t *testing.T) {
	c := greetingCard()
	out := renderDoctorPrompt(doctorFields(c), card.Lint(c), nil, "")
	for _, want := range []string{"alternate_greetings[0]", "You again.", "alternate_greetings[1]", "Oh! It's you."} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}
	// The empty slot must be visible AND explained — an index shown as blank
	// with no gloss reads as a greeting that exists and is empty.
	if !strings.Contains(out, "alternate_greetings[2]") {
		t.Error("prompt hides the append slot; the doctor cannot know where the array ends")
	}
	if !strings.Contains(out, "ADD") {
		t.Error("the append slot is shown but never explained as an add")
	}
}

// Both contracts have to name the indexed form, or the model emits
// "alternate_greetings" bare and every proposal is dropped by the allow-list.
func TestBothTaskContractsTeachTheIndexedForm(t *testing.T) {
	for name, task := range map[string]string{"doctor": doctorTask, "editor": editorTask} {
		if !strings.Contains(task, "alternate_greetings[N]") {
			t.Errorf("%s contract does not offer alternate_greetings[N] in its field enum", name)
		}
		if !strings.Contains(task, "alternate_greetings[0]") {
			t.Errorf("%s contract never shows a concrete indexed example", name)
		}
	}
}

// The parser's allow-list check is map presence, so this is the end-to-end
// proof that a greeting proposal survives it and an out-of-range one does not.
func TestParseAcceptsGreetingProposals(t *testing.T) {
	fields := doctorFields(greetingCard())
	raw := `{"note":"","proposals":[
	  {"id":"p1","field":"alternate_greetings[1]","severity":"suggestion","rationale":"warmer","after":"Oh — you came back."},
	  {"id":"p2","field":"alternate_greetings[2]","severity":"suggestion","rationale":"a colder way in","after":"...do I know you?"},
	  {"id":"p3","field":"alternate_greetings[9]","severity":"suggestion","rationale":"out of range","after":"nope"}
	]}`
	res, err := parseDoctorResult(raw, fields)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Proposals) != 2 {
		t.Fatalf("got %d proposals, want 2 (the out-of-range one dropped)", len(res.Proposals))
	}
	if res.Proposals[0].Before != "Oh! It's you." {
		t.Errorf("edit proposal Before = %q, want the greeting's current text", res.Proposals[0].Before)
	}
	if res.Proposals[1].Before != "" {
		t.Errorf("append proposal Before = %q, want empty", res.Proposals[1].Before)
	}
}
