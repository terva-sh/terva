// Package modelreply salvages structured data out of a model's prose reply.
//
// Models wrap structured output in prose and code fences no matter how firmly
// the prompt forbids it, so every caller that asks a model for JSON needs the
// same salvage step before it can unmarshal anything. This package is that
// step, written once.
//
// It is deliberately a top-level leaf with no terva dependencies. The package
// rule keeps packages/core clear of packages/agent, so a parser living under
// agent could never serve core; here it can serve anyone. Keep it that way —
// nothing in this package should know what a verdict, a ballot, or a tool
// call is. Callers own their own schema, and the domain checks that go with
// it.
package modelreply

// LastJSONObject returns the last brace-balanced {...} span in s, tracking
// string state so a brace inside a reason string does not end the object.
//
// Take the LAST balanced object, because when a model narrates and then
// answers, the answer is last.
//
// It scans FORWARD even though it wants the last match. Scanning backwards
// looks cheaper and is a trap: a closing quote is only distinguishable from an
// escaped one by counting the backslashes before it, so the state machine has
// to look forward at every quote anyway. A backward scan that skips the string
// tracking is the bug this package exists to stop repeating — it reads the
// braces inside a rationale as structure and returns nothing at all.
//
// It does NOT validate that the span is JSON. It narrows a reply down to the
// one substring worth handing to encoding/json, and unmarshalling is what
// reports whether the model actually answered in the requested shape.
func LastJSONObject(s string) (string, bool) {
	depth, start := 0, -1
	last, found := "", false
	inStr, esc := false, false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				// A stray closer with no opener: ignore rather than go
				// negative, so later well-formed objects still parse.
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				last, found = s[start:i+1], true
			}
		}
	}
	return last, found
}
