package persona

import (
	"strings"
	"testing"
)

// Hautoja's charter came out of a seed pass (docs/proposals/hautoja-team-seed-pass/).
// Each marker below is a sentence that pass found it needed. Losing one
// brings back a failure the pass recorded: a draft with invented fields that
// talkoot_draft refuses, a question with a safe default, or yolo proposed
// without a request.
func TestHautojaCharterKeepsTheSeedPassRules(t *testing.T) {
	raw, err := BuiltinFS.ReadFile(BuiltinRoot + "/hautoja.md")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(string(raw), "embedded:hautoja.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"a personal assistant, a coding team, a research team, and a decision council",
		"Ask nothing else.",
		"`talkoot_templates`",
		"Add no field the template does not use.",
		"write no persona file into a draft",
		"`talkoot_draft`",
		"no team exists until they press Create",
		"Read the team with `talkoot_roster` before you propose a change.",
		"Propose `yolo` only when the person asks for it, and only for a member in a worktree.",
		"only the person can make it",
		"You never write a roster.",
	} {
		if !strings.Contains(p.Charter, marker) {
			t.Errorf("the Hautoja charter lost %q", marker)
		}
	}
	// 🔑 The charter is the whole prompt the manager and the recruiter read,
	// and the pass ran it under the 3500-character static-block budget that
	// terva persona validate checks (personaCharterBudget in
	// packages/agent/personacmd.go).
	if n := len(p.ComposedCharter()); n > 3500 {
		t.Errorf("the Hautoja charter is %d characters, over the 3500 budget", n)
	}
	if len(p.GoodFor) != 0 {
		t.Errorf("Hautoja has good_for %v; a swarm child has none of its tools", p.GoodFor)
	}
}
