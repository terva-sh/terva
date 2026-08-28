package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/testsupport"
)

// writePromptsOverlay drops an operator overlay at $TERVA_HOME/locales/prompts/
// en.json and activates it, restoring the default catalog when the test ends.
// Same mechanism the eval harness uses (scripts/eval/ab.sh install_overlays),
// so what these tests prove is what an arm actually serves.
func writePromptsOverlay(t *testing.T, body string) {
	t.Helper()
	home := testsupport.TempDir(t)
	dir := filepath.Join(home, "locales", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "en.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := i18n.Configure("en", home); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = i18n.Configure("en", testsupport.TempDir(t)) })
}

// TestGuardOffArmIsExactlyThePreGuardText pins the eval's control arm.
//
// scripts/eval/overlays/tail-background-guard-off.json exists to answer "does
// the guard sentence change what the model answers", and that question is only
// answerable if the two arms differ in the SENTENCE and in nothing else. If the
// control arm also carried a stray blank line or a leading space, the arms
// would differ in whitespace too, and the harness has no way to notice: the
// ephemeral tail is invisible to --dump-prompt=sizes, so arm-diff reports the
// arms identical either way and the behavioural row is the only readout.
//
// The invariant is exact by construction: guarded text == guard + blank line +
// control text, byte for byte.
func TestGuardOffArmIsExactlyThePreGuardText(t *testing.T) {
	guard := tailBackgroundGuard()
	guarded := loreReferenceFrame("SOME LORE BLOCK")

	writePromptsOverlay(t, `{"tail.background.guard":" "}`)
	control := loreReferenceFrame("SOME LORE BLOCK")

	if strings.Contains(control, "[background]") {
		t.Errorf("the control arm still carries the guard it exists to remove:\n%.120q", control)
	}
	// Starts at the block, not at whitespace. The header used to be here and no
	// longer ships; what the assertion protects is unchanged, which is that the
	// control arm carries no leading whitespace to act as a second variable.
	if !strings.HasPrefix(control, "SOME LORE BLOCK") {
		t.Errorf("the control arm does not start at the block — leading whitespace is a second variable:\n%.60q", control)
	}
	if guarded != guard+"\n\n"+control {
		t.Errorf("the arms differ somewhere other than the guard sentence.\n guarded: %.120q\n control: %.120q", guarded, control)
	}
}

// TestGuardLastArmMovesTheGuardAndNothingElse pins the rung that measures
// POSITION, which is the dimension every earlier arm was structurally unable to
// vary. The first three rungs turn the guard's presence up and down; this one
// holds presence fixed and moves it to the end.
//
// Two invariants, and the second is the one that makes the run worth paying
// for. First, shipped must carry NO trailing guard -- the new key's compiled
// default is a single space, so the composition has to be exactly what it was
// before the key existed, or every historical number silently refers to a
// different prompt. Second, the two arms must differ in the guard's position
// and in nothing else: same sentence, same body, same separator. An arm that
// also changed the wording would measure two variables and attribute the result
// to whichever one the author had in mind.
func TestGuardLastArmMovesTheGuardAndNothingElse(t *testing.T) {
	const block = "SOME LORE BLOCK"
	guard := strings.TrimSpace(tailBackgroundGuard())
	shipped := loreReferenceFrame(block)

	if !strings.HasPrefix(shipped, guard) {
		t.Fatalf("shipped composition no longer leads with the guard:\n%.160q", shipped)
	}
	if strings.HasSuffix(shipped, guard) {
		t.Fatalf("shipped composition ends in the guard too — the trailing key is not absent by default, so this key changed what ships:\n%.160q", shipped)
	}

	// Built with encoding/json rather than a string literal: the guard contains
	// punctuation an escaped literal gets wrong quietly, and a malformed overlay
	// falls back to the compiled catalog — which would serve the SHIPPED text in
	// both arms while reporting a comparison.
	body, err := json.Marshal(map[string]string{
		"tail.background.guard":          " ",
		"tail.background.guard.trailing": guard,
	})
	if err != nil {
		t.Fatal(err)
	}
	writePromptsOverlay(t, string(body))
	last := loreReferenceFrame(block)

	if strings.HasPrefix(last, guard) {
		t.Errorf("the guard-last arm still leads with the guard:\n%.160q", last)
	}
	if !strings.HasSuffix(last, guard) {
		t.Fatalf("the guard-last arm does not end with the guard — the trailing key did not take:\n%.160q", last)
	}
	if body := strings.TrimSuffix(last, "\n\n"+guard); shipped != guard+"\n\n"+body {
		t.Errorf("the arms differ somewhere other than the guard's position.\n shipped: %.160q\n last:    %.160q", shipped, last)
	}
}

// TestTheRungsStayStrictlyNested pins the ladder the eval stands on.
//
// The rungs must be strictly NESTED. If they are not, each arm differs from the
// next in more than the one disclaimer it removes, and any number the harness
// prints is unattributable. That rule is what caught the first guard-off arm,
// which left the REFERENCE KNOWLEDGE header in place and so compared one
// disclaimer against another.
//
// The DEFAULT ladder is now two rungs, because the header was measured out and
// ships empty (see loreReferenceFrame). The header rung still exists as an eval
// configuration, restored by overlay, and nesting has to hold there too or the
// arms that removed the header cannot be reproduced.
func TestTheRungsStayStrictlyNested(t *testing.T) {
	const block = "<lore>SOME LORE BLOCK</lore>"
	const header = "REFERENCE KNOWLEDGE (restored by overlay for this test):"

	shipped := loreReferenceFrame(block)

	writePromptsOverlay(t, `{"tail.background.guard":" "}`)
	bare := loreReferenceFrame(block)

	if bare != block {
		t.Errorf("the bare arm is not bare — it still carries framing:\n%q", bare)
	}
	if !strings.Contains(shipped, "[background]") {
		t.Errorf("the shipped text must carry the guard:\n%q", shipped)
	}
	if strings.Contains(shipped, "REFERENCE KNOWLEDGE") {
		t.Errorf("the shipped text must carry NO header — it was measured out:\n%q", shipped)
	}
	if !strings.HasSuffix(shipped, bare) {
		t.Errorf("shipped is not exactly the bare arm plus a guard:\n shipped: %q\n bare:    %q", shipped, bare)
	}

	// The restored-header rung, which is how the header's cost was measured.
	body, err := json.Marshal(map[string]string{"lore.reference.frame": header})
	if err != nil {
		t.Fatal(err)
	}
	writePromptsOverlay(t, string(body))
	withHeader := loreReferenceFrame(block)

	if !strings.Contains(withHeader, header) {
		t.Fatalf("an overlay can no longer restore the header, so the arms that\n"+
			"measured it out are unreproducible:\n%q", withHeader)
	}
	if !strings.HasSuffix(withHeader, header+"\n"+block) {
		t.Errorf("the restored header must introduce the block and nothing else:\n%q", withHeader)
	}
}

// TestEmptyOverlayCannotRemoveTheGuard is a tripwire on the i18n layer, not on
// this package.
//
// keyedText treats an empty catalog value as a MISS (`ok && tr != ""`) and
// falls back to the compiled English. So the obvious spelling of the control
// arm — "tail.background.guard": "" — silently serves the guard it was written
// to remove, and the run reports a comparison it never made. That is why the
// overlay is a space.
//
// If i18n ever starts honouring an empty override, this test fails, and the
// right response is to change the overlay to "" and simplify the TrimSpace in
// loreReferenceFrame. The failure is the notification.
func TestEmptyOverlayCannotRemoveTheGuard(t *testing.T) {
	writePromptsOverlay(t, `{"tail.background.guard":""}`)

	if !strings.Contains(loreReferenceFrame("SOME LORE BLOCK"), "[background]") {
		t.Fatal("i18n now honours an empty override: switch " +
			"scripts/eval/overlays/tail-background-guard-off.json from \" \" to \"\", " +
			"and drop the whitespace handling this test documents")
	}
}
