package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RepairToolArguments makes a streamed tool-argument buffer parseable when the
// only thing wrong with it is raw control bytes inside a JSON string.
//
// Every provider streams tool arguments as opaque text fragments the model
// generated token by token — Anthropic's input_json_delta, OpenAI's
// function-call argument deltas, and the Bedrock/Gemini equivalents. None of
// them validate that text on the way out, so whatever the model typed is what
// arrives. A model writing Go into an `edit` call types real tabs to indent it,
// and a real tab inside a JSON string is a syntax error:
//
//	invalid args: invalid character '\t' in string literal
//
// The call is not ambiguous, only mis-encoded: the model's intent survives
// intact in the bytes, and escaping the control characters recovers it exactly.
// Without that the tool rejects the call, and because the error names a
// character class rather than a location the model has nothing to act on and
// re-sends the identical bytes until the stall detector intervenes.
//
// This is deliberately NOT a general JSON fixer. It repairs one defect, and it
// is safe precisely because that defect is impossible in valid JSON: RFC 8259
// forbids unescaped bytes below 0x20 inside a string, so any buffer this
// function changes was already unparseable. Already-valid input is returned
// untouched, and a repair that does not produce valid JSON is discarded in
// favour of the original — so a caller can never be handed something that
// parses differently than what the model sent.
func RepairToolArguments(raw string) string {
	if raw == "" || json.Valid([]byte(raw)) {
		return raw
	}
	repaired := escapeControlBytesInStrings(raw)
	if repaired == raw || !json.Valid([]byte(repaired)) {
		// Broken some other way (truncated mid-stream, a lost leading delta).
		// Hand back the original so the caller's own fallback decides, rather
		// than substituting a half-repair that is still not parseable.
		return raw
	}
	return repaired
}

// FinalizeToolArguments turns a streamed argument buffer into the two things a
// ToolCallBlock needs: Arguments that are ALWAYS valid JSON, and — when the
// buffer could not be made parseable — the original text, kept verbatim.
//
// Arguments being unconditionally valid is the point. An invalid
// json.RawMessage does not fail politely at the tool that reads it: it makes
// every enclosing json.Marshal fail, and a ToolCallBlock is marshalled by at
// least three independent paths (the session JSONL, each provider's request
// builder, and the ctrlproto wire behind the TUI and `terva attach`). Guarding
// each of those separately is whack-a-mole, and the one that was missed lost
// whole assistant turns off the transcript. Normalising here — at the single
// boundary where model-generated text becomes a typed block — makes the
// invariant hold everywhere downstream by construction.
//
// The unparseable text is returned rather than dropped because it is the only
// evidence of what the model actually tried to do. A session that discarded it
// recorded a tool_result with no call in front of it, which is unreadable after
// the fact: the tool name, the arguments, and the fact a call happened at all
// were simply gone.
func FinalizeToolArguments(raw string) (args json.RawMessage, unparsed string) {
	repaired := RepairToolArguments(raw)
	if repaired == "" {
		// No argument buffer at all — a call to a no-argument tool.
		return json.RawMessage("{}"), ""
	}
	if json.Valid([]byte(repaired)) {
		return json.RawMessage(repaired), ""
	}
	// Truncated mid-stream, or a lost leading delta. The call cannot be run,
	// but it can still be reported honestly and recorded in full.
	return json.RawMessage("{}"), raw
}

// escapeControlBytesInStrings rewrites raw bytes below 0x20 that appear INSIDE
// a string literal, leaving the identical bytes alone everywhere else — between
// tokens they are legal JSON whitespace, and a model that pretty-prints its
// arguments across lines is not making a mistake.
//
// Scanning is byte-wise, which is safe for UTF-8: every byte of a multi-byte
// rune is >= 0x80, so no continuation byte can be mistaken for a control
// character. Quote tracking honours backslash escapes so an escaped quote does
// not flip the parser out of the string it is inside.
func escapeControlBytesInStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			// Whatever follows a backslash is consumed verbatim so it cannot
			// close the string. A backslash followed by a RAW control byte is
			// itself malformed, and is deliberately left that way: guessing at
			// what the model meant there would be a rewrite, not a repair, and
			// the validity check in RepairToolArguments discards the attempt.
			b.WriteByte(c)
			escaped = false
		case inString && c == '\\':
			b.WriteByte(c)
			escaped = true
		case c == '"':
			inString = !inString
			b.WriteByte(c)
		case inString && c < 0x20:
			b.WriteString(jsonControlEscape(c))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// jsonControlEscape renders one control byte as its JSON escape, preferring the
// two-character short forms so a repaired argument reads the way the model
// would have written it had it escaped the byte itself.
func jsonControlEscape(c byte) string {
	switch c {
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	}
	return fmt.Sprintf(`\u%04x`, c)
}
