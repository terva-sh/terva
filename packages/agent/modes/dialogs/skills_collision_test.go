package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/tui"
)

// collidingCatalog mirrors what VisibleSkills hands the picker on a machine
// where a ~/.claude/skills/handoff meets the built-in of the same name.
func collidingCatalog() []*skills.Skill {
	winner := &skills.Skill{
		Name:      "handoff",
		Namespace: skills.NamespaceBuiltin,
		Source:    "built-in",
		Path:      "builtin:handoff",
		Builtin:   true,
	}
	loser := &skills.Skill{
		Name:         "handoff",
		Namespace:    skills.NamespaceClaude,
		Description:  "Compact the current conversation into a handoff document.",
		Source:       "global (claude)",
		Path:         "/home/u/.claude/skills/handoff/SKILL.md",
		ShadowedBy:   winner,
		ArgumentHint: "What will the next session be used for?",
		Body:         "# Session handoff\n\nWrite a handoff document.\n",
	}
	winner.Shadowed = []*skills.Skill{loser}
	return []*skills.Skill{
		winner,
		{Name: "release", Namespace: skills.NamespaceClaude, Description: "Cut a release.", Source: "project (claude)", Path: "/p/.claude/skills/release/SKILL.md"},
	}
}

// The row for a shadowed skill has to carry three things or it reads as a
// duplicate: the qualified name (which is what the user must type), the fact
// that it lost, and to whom.
func TestSkillsDialogRendersCollisionRow(t *testing.T) {
	d := NewSkillsDialog()
	d.MaxRows = 8
	d.Open(skills.VisibleSkills(collidingCatalog()))

	out := d.Render(tui.Theme{}, 100)
	joined := strings.Join(out, "\n")
	t.Logf("rendered picker:\n%s", joined)

	// Scope the assertions to the collision ROW, not the whole pane: a
	// Contains over everything can match chrome and cannot see a line that
	// wrapped or truncated.
	var row string
	for _, l := range out {
		if strings.Contains(l, "claude:handoff") {
			row = l
			break
		}
	}
	if row == "" {
		t.Fatalf("no row shows the qualified name — the user cannot tell what to type:\n%s", joined)
	}
	if !strings.Contains(row, "shadowed by builtin") {
		t.Errorf("collision row does not say what beat it:\n%q", row)
	}
	// The built-in itself must stay out of the picker.
	if strings.Contains(joined, "built-in") {
		t.Errorf("a built-in leaked into the picker:\n%s", joined)
	}
	// Width is measured on the PRINTABLE text: the escape sequences a theme
	// emits are bytes the terminal never spends a column on, so len() would
	// flag every styled row and guard nothing.
	if w := printableWidth(row); w > 100 {
		t.Errorf("collision row is %d printable cols, past the 100-col pane: %q", w, row)
	}
}

// printableWidth is the visible column count of a rendered row: ANSI escapes
// stripped, then runes counted.
func printableWidth(s string) int {
	return len([]rune(widgets.StripANSIBytes(s)))
}

// The body view is where a user lands to ask "why is this named oddly?", so
// the answer has to be there — and ChromeRows has to have counted the extra
// lines, or the dialog overruns its budget and squeezes the transcript.
func TestSkillsDialogBodyViewExplainsCollision(t *testing.T) {
	d := NewSkillsDialog()
	d.MaxRows = 10
	d.Open(skills.VisibleSkills(collidingCatalog()))

	// Enter on the first row (claude:handoff sorts before release).
	if closed := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); closed {
		t.Fatal("enter should open the body view, not close the dialog")
	}
	out := d.Render(tui.Theme{}, 100)
	joined := strings.Join(out, "\n")
	t.Logf("rendered body view:\n%s", joined)

	if !strings.Contains(joined, "shadowed:") {
		t.Errorf("body view does not explain the collision:\n%s", joined)
	}
	if !strings.Contains(joined, "claude:handoff") {
		t.Errorf("body view does not give the name to load it by:\n%s", joined)
	}
	if !strings.Contains(joined, "argument:") {
		t.Errorf("body view drops the argument hint:\n%s", joined)
	}
	// ChromeRows must match what Render actually emitted around the body.
	if got, want := len(out), d.MaxRows+d.ChromeRows(); got > want {
		t.Errorf("rendered %d rows, budget was %d (MaxRows %d + chrome %d) — an oversized dialog squeezes the transcript",
			got, want, d.MaxRows, d.ChromeRows())
	}
}
