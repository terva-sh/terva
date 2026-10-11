package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"terva.sh/terva/packages/core/i18n"
	"terva.sh/terva/packages/provider"
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

// cutOffCallMessage is the result for a tool call in a reply that stopped at
// the output limit. The loop ends the turn at that stop, so the call never
// reaches runOneTool and unparseableArgsMessage. Before this, the next request
// filled the gap with the generic interrupt stub, which said "aborted", and a
// model told "aborted" sends the same oversized call again, into the same
// limit. A model told the limit cut the call off can split it.
//
// cut says whether this is the call the limit cut (see cutOffCallIndex). A call
// whose arguments arrived whole did not run either, but telling the model its
// arguments are incomplete would be false, and the model would split a call
// that needed no change.
func cutOffCallMessage(tr i18n.Translator, tool string, cut bool) string {
	if cut {
		return i18n.In(tr).P("tools.cut_off",
			"%s: your reply reached the output limit before this call was complete. Its arguments arrived incomplete, so the call did not run. Do not send the same call again. Split large content into smaller parts, and send them in successive calls.",
			tool)
	}
	return i18n.In(tr).P("tools.cut_off.whole",
		"%s: your reply reached the output limit after this call, so no call in the reply ran. This call arrived complete. Send it again.",
		tool)
}

// cutOffCallIndex returns the index in msg.Content of the tool call that the
// output limit cut, or -1 when the limit cut no call. A provider streams the
// blocks of a reply in order, so only the last one can stop partway. Blocks
// other than text and tool calls are skipped, because a provider can place a
// reasoning block after the content.
//
// Position alone does not prove a cut. Gemini sends each call whole, and a
// streamed call can end exactly at the close of its object. A whole call can
// also be malformed, with an invalid escape or a control byte between tokens,
// and that call needs the encoding explanation, not an order to split it. So
// the last call counts as cut only when its arguments end early (see
// argumentsEndEarly). provider.FinalizeToolArguments repairs control bytes and
// nothing else, so a buffer the limit cut always keeps RawArguments, and the
// test reads that text.
//
// ⚠️ Two cuts read as something else. A call the limit stopped before its
// first argument delta has an empty buffer, which is a valid call with no
// arguments, so the model is told to send it again. A call cut inside a
// string that already holds an invalid escape reports the escape first, so
// the model gets the encoding explanation, which is still true. Each costs at
// most one more request, so the rule keeps both.
func cutOffCallIndex(msg provider.Message) int {
	for i := len(msg.Content) - 1; i >= 0; i-- {
		switch b := msg.Content[i].(type) {
		case provider.ToolCallBlock:
			if b.RawArguments == "" || !argumentsEndEarly(b.RawArguments) {
				return -1
			}
			return i
		case provider.TextBlock:
			return -1
		}
	}
	return -1
}

// argumentsEndEarly reports whether raw stops before its first JSON value is
// complete, rather than holding a whole value that is malformed.
//
// json.Unmarshal cannot tell these apart: it reports a short buffer as a
// *json.SyntaxError, the same type as a bad escape. json.Decoder.Decode
// returns io.ErrUnexpectedEOF for a short buffer, and io.EOF for one with
// nothing but white space, which also stopped before the object began.
//
// Each control byte becomes a space before the decode. A raw tab inside a
// string is the common encoding defect, because a coding model types
// tab-indented source, and the decoder would report that tab before it reached
// the cut. A control byte is never a structural token, so the swap cannot move
// the point where the value ends. A byte loop does the swap, because
// strings.Map rewrites invalid UTF-8.
func argumentsEndEarly(raw string) bool {
	buf := []byte(raw)
	for i, c := range buf {
		if c < 0x20 {
			buf[i] = ' '
		}
	}
	err := json.NewDecoder(bytes.NewReader(buf)).Decode(new(json.RawMessage))
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
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
