package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// The /help keys section is generated from the global keymap, and these
// tests are what keep it that way.
//
// The bug they exist for: /help used to carry its own hand-written list of
// chords beside buildGlobalKeymap, and the two drifted apart silently.
// ctrl+y, ctrl+r, ctrl+d, shift+tab, alt+up and ctrl+enter were all
// registered and none of them documented — and ctrl+c's row still promised
// a turn-cancelling behaviour keyCtrlC had already stopped doing. Nothing
// failed; the help was simply wrong for releases at a time.
//
// A binding added without a description is now a red build instead.

func TestEveryBindingIsDocumented(t *testing.T) {
	for _, b := range (&Interactive{}).buildGlobalKeymap() {
		if b.name == "" {
			t.Errorf("a binding (key kind %v) has no stable action name", b.kind)
			continue
		}
		// hideFromHelp is the deliberate opt-out: a partner of a merged
		// row, or an alias. Requiring it to be silent keeps it meaning
		// one thing, so "folded into another row" never blurs into
		// "somebody wrote a desc and it is being dropped".
		if b.hideFromHelp {
			if b.desc != "" {
				t.Errorf("binding %q sets hideFromHelp but also carries a desc — pick one", b.name)
			}
			continue
		}
		if strings.TrimSpace(b.desc) == "" {
			t.Errorf("binding %q has no /help description; add desc, or set hideFromHelp if another row already covers this chord", b.name)
		}
		if strings.TrimSpace(b.label) == "" {
			t.Errorf("binding %q has a description but no label to print it against", b.name)
		}
	}
}

// A chord must not be documented twice. helpEditorRows is only for chords
// the keymap does NOT define, so an entry there matching a binding's label
// means a registered chord was added to the hand-maintained list by
// mistake — which would print it once from each source.
func TestEditorRowsDoNotShadowBindings(t *testing.T) {
	byLabel := map[string]string{}
	for _, b := range (&Interactive{}).buildGlobalKeymap() {
		if !b.hideFromHelp {
			byLabel[b.label] = b.name
		}
	}
	for _, r := range helpEditorRows {
		if name, dup := byLabel[r[0]]; dup {
			t.Errorf("helpEditorRows has %q, which the %q binding already documents; registered chords belong in the keymap only", r[0], name)
		}
	}
}

// Two visible rows must not claim the same label, or one of them is
// unreachable text.
func TestNoDuplicateHelpLabels(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range helpKeyRows((&Interactive{}).buildGlobalKeymap()) {
		if seen[r[0]] {
			t.Errorf("/help prints the label %q twice", r[0])
		}
		seen[r[0]] = true
	}
}

// The reported symptom, pinned end to end: ctrl+y opened the copy picker
// but /help never mentioned it. Renders the real block and looks for the
// chords that were missing, so a regression shows up as the user sees it
// rather than only in the table.
func TestHelpRendersRegisteredChords(t *testing.T) {
	km := (&Interactive{}).buildGlobalKeymap()
	out := strings.Join(renderHelpBlock(tui.Theme{}, 100, km), "\n")

	for _, want := range []string{
		"ctrl+y",    // the copy picker — the chord that prompted this
		"ctrl+r",    // recorded-thinking expand
		"ctrl+d",    // quit when idle
		"shift+tab", // approval-mode wheel
		"alt+↑",     // pop a queued message
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/help does not mention the registered chord %q", want)
		}
	}

	// The editor rows still survive the switch to a generated section.
	for _, want := range []string{"enter", "ctrl+j", "tab", "ctrl+w", "alt+←"} {
		if !strings.Contains(out, want) {
			t.Errorf("/help lost the editor chord %q", want)
		}
	}

	// And the stale promise is gone: ctrl+c no longer cancels a turn.
	if strings.Contains(out, "exit (while idle) - cancel the current turn (while busy)") {
		t.Error("/help still carries the old ctrl+c text, which keyCtrlC no longer implements")
	}
}
