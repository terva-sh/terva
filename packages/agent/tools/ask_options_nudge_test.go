package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// The failure this guards: across every ask recorded on one machine (296
// questions, 85 calls), the model supplied options 84% of the time — and of
// the 46 that arrived without them, 34 had written the choices into the
// question prose instead. The user then gets a text box under a question that
// just listed its own answers, and types back one of the choices the model
// already had.
//
// The strings below are the real shapes, shortened. The full corpus was run
// through this detector before it shipped: 34 of 46 caught, 0 of the genuinely
// open ones touched.
func TestQuestionThatEnumeratesItsOwnOptionsIsHandedBack(t *testing.T) {
	caught := []struct{ name, q string }{
		{"lettered", "What IS the bloom? (a) LARVAL SWARM — a mass hatching; (b) MICRO-MEDUSA SWARM — millions of tiny adults; (c) SPAWNING EVENT — a coordinated release."},
		{"options lead-in", "How long do the cysts persist? Options: (a) SHORT — one season; (b) LONG — until salt water."},
		{"numbered", "Which rollout? (1) all at once, or (2) one region at a time."},
		{"capitalised", "Pick a shape. (A) flat file (B) sqlite"},
		{"mid-sentence", "Decide the adult stage — (a) BENTHIC POLYP settles on the bottom; (b) PELAGIC MEDUSA drifts."},
	}
	for _, c := range caught {
		t.Run(c.name, func(t *testing.T) {
			if !enumeratesItsOwnOptions(c.q) {
				t.Fatalf("not detected: %s", c.q)
			}
			_, err := askArgs{Question: c.q}.questions()
			if err == nil {
				t.Fatal("accepted a question that listed its own options")
			}
			for _, want := range []string{"options is empty", "put each choice in options"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the model cannot act on this: %v", err)
				}
			}
		})
	}
}

// The guard must never block a question that genuinely has no candidate
// answers — those are the ones free text is FOR. Every string here is a real
// question from the corpus that arrived without options and should have.
func TestGenuinelyOpenQuestionsAreLeftAlone(t *testing.T) {
	open := []string{
		"What do the queen's eggs become — what uses the host's body?",
		"Order name under Insectoid (sets the taxonomy code segment).",
		"How big is a colony, and how often do humans encounter it? This drives encounter_rate.",
		"The roadmap floated this as a candidate for `pervasive` (see the comparison above). Where should encounter_rate land?",
		// A confirmation has nothing to detect — no enumeration — so the
		// schema wording is what has to reach it. A rule strict enough to
		// catch this would also catch every open question ending in '?'.
		"Alkaloid mechanism: the pollen acts on the host's drive centers once a dose crosses threshold. Confirm as canon?",
	}
	for _, q := range open {
		if enumeratesItsOwnOptions(q) {
			t.Errorf("false positive — this question has no options to move: %s", q)
		}
		if _, err := (askArgs{Question: q}).questions(); err != nil {
			t.Errorf("blocked an open question: %v", err)
		}
	}
}

// A parenthetical is not an enumeration. "(a note on naming)" and "(see
// above)" both start with a letter in brackets and neither offers a choice.
func TestParentheticalsDoNotTripTheGuard(t *testing.T) {
	for _, q := range []string{
		"Should we rename it (a decision we can revisit)?",
		"Which approach (see the comparison above) do you prefer?",
		"Is (b) still the plan?", // bare marker, no following text
		"What are the options:",  // a lead-in with nothing after it
	} {
		if enumeratesItsOwnOptions(q) {
			t.Errorf("false positive on a parenthetical: %s", q)
		}
	}
}

// The guard is per question and names which one, since a set is answered as a
// unit and "one of these is wrong" is not actionable.
func TestGuardNamesTheOffendingQuestionInASet(t *testing.T) {
	_, err := askArgs{Questions: []askQuestion{
		{Question: "Which database?", Options: []string{"postgres", "sqlite"}},
		{Question: "Which cache? (a) redis (b) in-process"},
	}}.questions()
	if err == nil {
		t.Fatal("a set with one bad question was accepted")
	}
	if !strings.Contains(err.Error(), "question 2") {
		t.Errorf("does not say which question: %v", err)
	}
}

// A question WITH options may say whatever it likes — 11 of the corpus's
// optioned questions also enumerate in the prose, and there is nothing wrong
// with restating them.
func TestOptionsPresentSuppressesTheGuard(t *testing.T) {
	q := "What IS the bloom? (a) LARVAL SWARM (b) MICRO-MEDUSA SWARM"
	if _, err := (askArgs{Question: q, Options: []string{"larval swarm", "micro-medusa swarm"}}).questions(); err != nil {
		t.Fatalf("blocked a question that DID supply options: %v", err)
	}
}

// End to end: the model gets the message back, and the user is never shown
// the badly-shaped question.
func TestExecuteHandsBackWithoutAsking(t *testing.T) {
	asker := &fakeAsker{ans: core.UserAnswer{Answer: "this"}}
	tool := &AskUserTool{Asker: asker}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"Which? (a) this (b) that"}`), nil)
	if err == nil {
		t.Fatal("Execute accepted it")
	}
	if asker.gotSet != nil {
		t.Error("the user was shown a question that listed its own options")
	}
}
