package core

import (
	"strings"
	"testing"
)

// The message exists to be ACTED on. encoding/json's own text named a
// character class and nothing else, so the model had nothing to change and
// re-sent identical bytes three times. These assert the things that make the
// difference: what is wrong, where, and what to do instead.
func TestTheControlCharacterMessageSaysWhatToFix(t *testing.T) {
	raw := "{\"path\":\"a.go\",\"edits\":[{\"oldText\":\"func f() {\n\treturn\n}\"}]}"
	msg := unparseableArgsMessage("edit", raw)

	for _, want := range []string{
		"edit",    // which call failed
		"not run", // that nothing happened, so no partial write is feared
		"newline", // the actual offending byte, named in words
		"\\n",     // the escape to use instead
		"Send the call again",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
	// The remedy must not be buried behind jargon the model has to decode.
	if strings.Contains(msg, "invalid character") {
		t.Errorf("message still leans on encoding/json's wording:\n%s", msg)
	}
}

func TestTheMessageNamesATabAsATab(t *testing.T) {
	msg := unparseableArgsMessage("edit", "{\"a\":\"x\ty\"}")
	if !strings.Contains(msg, "tab") {
		t.Errorf("a raw tab was not named:\n%s", msg)
	}
	if !strings.Contains(msg, `\t`) {
		t.Errorf("the \\t escape was not offered:\n%s", msg)
	}
}

// A truncated call is a different failure with a different remedy: there is
// nothing to escape, the text simply stopped. Telling the model to escape
// control characters there would send it looking for a defect that isn't real.
func TestATruncatedCallIsNotDescribedAsAnEscapingProblem(t *testing.T) {
	msg := unparseableArgsMessage("edit", `{"path":"a.go",`)
	if strings.Contains(msg, "Control characters must be escaped") {
		t.Errorf("truncation was misreported as an escaping problem:\n%s", msg)
	}
	if !strings.Contains(msg, "incomplete") {
		t.Errorf("truncation was not named:\n%s", msg)
	}
}

func TestALostPrefixIsNamedAsSuch(t *testing.T) {
	msg := unparseableArgsMessage("bash", `command":"cd /tmp"}`)
	if !strings.Contains(msg, "'{'") {
		t.Errorf("the missing opening brace was not named:\n%s", msg)
	}
}

// The excerpt locates the defect; it must not paste the whole buffer back.
// A broken edit can carry an entire file, and returning it verbatim would cost
// more context than the failed call did.
func TestTheExcerptIsBounded(t *testing.T) {
	huge := `{"path":"a.go","body":"` + strings.Repeat("x", 20000) + "\t" + strings.Repeat("y", 20000) + `"}`
	msg := unparseableArgsMessage("edit", huge)
	if len(msg) > 1000 {
		t.Errorf("message is %d bytes; it should quote a window, not the buffer", len(msg))
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("a truncated excerpt was not marked as truncated:\n%s", msg)
	}
}

// The excerpt has to RENDER the invisible byte. An unquoted window shows a tab
// as whitespace, which is exactly as unreadable as the original error.
func TestTheExcerptRendersTheInvisibleByte(t *testing.T) {
	msg := unparseableArgsMessage("edit", "{\"a\":\"x\ty\"}")
	if !strings.Contains(msg, `\t`) {
		t.Errorf("the excerpt did not render the tab visibly:\n%s", msg)
	}
}
