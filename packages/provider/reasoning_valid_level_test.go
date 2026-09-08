package provider

import "testing"

// ValidReasoningLevel is the door check every spawn surface uses, so it must
// accept exactly what NormalizeReasoning understands and nothing else. The
// interesting half is the aliases: a word one door takes must not be refused
// at another, and the function derives them rather than listing them, so this
// test is what proves the derivation still covers the mapper's switch.
func TestValidReasoningLevel(t *testing.T) {
	for _, lv := range ReasoningLevels {
		if !ValidReasoningLevel(lv) {
			t.Errorf("ValidReasoningLevel(%q) = false, but the ladder advertises it", lv)
		}
	}

	// Every alias the mapper accepts, plus the empty string, which means the
	// caller chose nothing.
	for _, lv := range []string{
		"", "  ", "HIGH", " Low ",
		"off", "none", "no", "false", "disabled",
		"min", "minimal", "med", "hi",
	} {
		if !ValidReasoningLevel(lv) {
			t.Errorf("ValidReasoningLevel(%q) = false, but NormalizeReasoning accepts it", lv)
		}
	}

	for _, lv := range []string{"banana", "extreme", "very high", "-1", "off!"} {
		if ValidReasoningLevel(lv) {
			t.Errorf("ValidReasoningLevel(%q) = true, but it is not an effort", lv)
		}
	}
}
