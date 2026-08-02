package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// argsExcerptRadius bounds how much of the model's own text is quoted back at
// it. The model already knows what it sent, so the excerpt exists to locate the
// defect, not to reproduce the call — and a broken buffer can be a whole file.
const argsExcerptRadius = 40

// unparseableArgsMessage explains why a tool call could not be run, in terms of
// what the model should do differently.
//
// The failure this replaces was `encoding/json`'s own text, surfaced verbatim:
//
//	invalid args: invalid character '\t' in string literal
//
// That names a character class and nothing else — no position, no remedy, and
// no hint that the problem is the ENCODING rather than the intent. A model
// reading it has nothing to change, so it re-sends identical bytes; the session
// that prompted this fix did exactly that three times before the stall detector
// broke the loop. Naming the offending byte, where it sits, and the specific
// correction is what makes the difference between a retry and a repair.
func unparseableArgsMessage(tool, raw string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: the arguments for this call did not arrive as valid JSON, so it was not run.", tool)

	offset, incomplete, ok := jsonSyntaxOffset(raw)
	switch {
	case ok && !incomplete && offset < len(raw) && raw[offset] < 0x20:
		// The common case by far: a coding model typing tab-indented source
		// into a string field. Say the fix, precisely.
		fmt.Fprintf(&b, " A literal %s appears inside a JSON string at byte %d. Control characters must be escaped inside JSON strings — write \\t, \\n and \\r rather than the raw bytes.",
			controlByteName(raw[offset]), offset)
	case strings.TrimSpace(raw) == "":
		b.WriteString(" The argument text was empty.")
	case !strings.HasPrefix(strings.TrimSpace(raw), "{"):
		b.WriteString(" The text does not begin with '{', so the start of the object is missing — the call may have been cut off in transit.")
	case incomplete:
		b.WriteString(" The text ends mid-value, so the call arrived incomplete rather than mis-encoded — nothing here needs escaping.")
	default:
		if ok {
			fmt.Fprintf(&b, " The first syntax error is at byte %d.", offset)
		}
	}

	if ex := argsExcerpt(raw, offset, ok); ex != "" {
		b.WriteString(" Near: " + ex)
	}
	b.WriteString(" Send the call again with the arguments correctly encoded.")
	return b.String()
}

// jsonSyntaxOffset reports the byte offset encoding/json rejected — the only
// positional information it exposes — and whether the input simply ran out.
//
// Truncation has to be told apart from mis-encoding because the two have
// opposite remedies: one needs the bytes re-sent, the other needs them escaped.
// The offset alone cannot distinguish them, since a buffer that ends mid-value
// still reports a position inside itself; the "unexpected end of JSON input"
// wording is the actual signal.
func jsonSyntaxOffset(raw string) (offset int, incomplete, ok bool) {
	err := json.Unmarshal([]byte(raw), new(json.RawMessage))
	if err == nil {
		return 0, false, false
	}
	incomplete = strings.Contains(err.Error(), "unexpected end of JSON input")
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		off := int(syn.Offset) - 1
		if off < 0 {
			off = 0
		}
		return off, incomplete, true
	}
	return 0, incomplete, false
}

// argsExcerpt quotes a bounded window around the defect so the model can see
// which part of its own text is meant, without the whole buffer coming back.
func argsExcerpt(raw string, offset int, haveOffset bool) string {
	if raw == "" {
		return ""
	}
	if !haveOffset {
		offset = 0
	}
	start := offset - argsExcerptRadius
	if start < 0 {
		start = 0
	}
	end := offset + argsExcerptRadius
	if end > len(raw) {
		end = len(raw)
	}
	// strconv.Quote renders the raw control bytes visibly as \t and \n, which
	// is the whole point: the defect is invisible in an unquoted excerpt.
	out := strconv.Quote(raw[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(raw) {
		out += "…"
	}
	return out
}

func controlByteName(c byte) string {
	switch c {
	case '\t':
		return "tab"
	case '\n':
		return "newline"
	case '\r':
		return "carriage return"
	}
	return fmt.Sprintf("control character 0x%02x", c)
}
