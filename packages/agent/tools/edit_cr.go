package tools

import (
	"fmt"
	"strings"
)

// A bare carriage return is the one corruption of oldText that the model
// cannot see and the tool cannot survive.
//
// normalizeNewlines maps \r\n to \n and then any surviving \r to \n. That is
// right for a classic-Mac file, where a bare \r IS the line ending, and it is
// load-bearing for the ordinary case: a model that copied bytes out of a CRLF
// file hands back \r\n, and without the collapse every such edit would miss.
//
// It is destructive for a \r that is not a line ending. In the session behind
// this change the model emitted oldText with a \r before every token —
// "\r \r \r<g\r class\r=\r\"eyes\r-open\r\"" — 270 of them in one edit.
// Normalization turned each into a newline and shredded the block. The tool
// then reported, truthfully and uselessly, that no block in the file resembled
// it and that the file was byte-identical to what the model last read. Both
// statements were true. Neither named the invisible whitespace, so the model
// re-read the file and tried again. Four turns went that way.
//
// The \r did not come from here. The session log stores the parsed arguments,
// and a raw \r inside a JSON string is rejected by encoding/json, so the wire
// carried the two-character escape and the model or its server produced it.
// terva cannot stop it arriving. It can refuse to be silent about it.
//
// The discriminator is exact. A classic-Mac oldText uses \r AS its line
// endings, so it holds \r and no \n. Token noise arrives mixed INTO text that
// already has \n. So a bare \r beside a \n is provably not a line ending.

// bareCarriageReturns counts the \r in s that are not part of a \r\n pair.
func bareCarriageReturns(s string) int {
	return strings.Count(s, "\r") - strings.Count(s, "\r\n")
}

// stripBareCarriageReturns drops every \r that is not a line ending and keeps
// each \r\n pair whole.
func stripBareCarriageReturns(s string) string {
	if bareCarriageReturns(s) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && (i+1 >= len(s) || s[i+1] != '\n') {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// carriageReturnPhrase names the count without an "(s)".
func carriageReturnPhrase(n int) string {
	if n == 1 {
		return "1 carriage return that is not a line ending"
	}
	return fmt.Sprintf("%d carriage returns that are not line endings", n)
}

// carriageReturnNote explains a failed match that a bare \r caused, and PROVES
// the claim before it makes it. Removing the stray \r has to actually produce
// a block that is in the file; only then does the note name the line. An
// unproven guess would send the model to strip whitespace that was never the
// problem, which costs the same turn the note exists to save.
//
// rawOld is the model's oldText BEFORE normalizeNewlines, because the \r are
// gone by the time the matcher sees it. body is the \n-normalized file.
// Returns "" when there is nothing to say.
func carriageReturnNote(body, rawOld string) string {
	n := bareCarriageReturns(rawOld)
	if n == 0 {
		return ""
	}
	cleaned := normalizeNewlines(stripBareCarriageReturns(rawOld))
	if cleaned == "" {
		return ""
	}
	if idx := strings.Index(body, cleaned); idx >= 0 {
		line := 1 + strings.Count(body[:idx], "\n")
		return fmt.Sprintf("\n(oldText holds %s, and they are invisible in your own output; remove them and the block matches at line %d)",
			carriageReturnPhrase(n), line)
	}
	// The \r are real and worth naming even when their removal is not enough
	// on its own. Saying so is still more than the did-you-mean evidence says.
	return fmt.Sprintf("\n(oldText holds %s, and they are invisible in your own output; remove them and retry)",
		carriageReturnPhrase(n))
}
