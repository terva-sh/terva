// Package lineframe is the one bounded line reader for terva's
// newline-delimited wire protocols. Every framed transport that reads from a
// separate, potentially buggy or hostile peer — extension stdio (extproto),
// connector carriers (connproto), MCP servers, the ACP client, provider
// text/event-stream responses — delimits its wire on '\n'. bufio.Scanner is
// the obvious tool for that and the wrong one: a single token past its buffer
// returns ErrTooLong, the Scanner is *permanently* done, and it reports that
// only through Err() — which callers forget to check, turning an oversized
// frame into a silent end-of-stream.
//
// ReadFrame is the primitive: an over-limit line is fully consumed through its
// newline and reported (tooLong) rather than poisoning the stream, so the
// caller decides what an oversized frame means. That decision is not the same
// everywhere, and the package deliberately supports both answers:
//
//   - RECOVER (Reader): drop the frame, warn, keep reading. Right for a
//     multiplexed peer wire, where one malformed message must not tear down a
//     session carrying many others. extproto, connproto, MCP, ACP, terva rpc.
//
//   - REJECT (ReadFrame directly): abort the stream with a typed error. Right
//     when the frames are *one* logical payload, so dropping one silently
//     corrupts the whole — a provider SSE response, where a skipped data: line
//     is a hole in the assistant's message. See packages/provider/sse.go.
//
// The failure to distinguish these is what a raw Scanner forces: it always
// rejects, always silently, and never recovers.
//
// Scope: WIRE peers only. Local trusted files (session transcripts, JSONL
// exports) deliberately read with no size cap and must NOT use this — see the
// ReadBytes comments in packages/core/session_portable.go.
//
// The full inventory of terva's payload boundaries — wire, file, and network —
// is docs/resource-limits.md.
package lineframe

import (
	"bufio"
	"fmt"
	"io"
)

// DefaultMaxBytes is the frame ceiling most terva wire protocols use: the
// historical 4 MiB bufio.Scanner cap, now enforced recoverably. Protocols
// that legitimately carry larger single frames (ACP prompts/results) pass
// their own limit to ReadFrame/NewReader.
const DefaultMaxBytes = 4 << 20 // 4 MiB

// ReadFrame reads one '\n'-terminated frame from r, bounding its length at
// max. It is the recoverable replacement for bufio.Scanner on a wire: where
// Scanner is permanently done after a token exceeds its buffer (ErrTooLong),
// ReadFrame fully consumes an over-limit line through its newline and returns
// tooLong=true with a nil line, so the caller can log it, skip it, and keep
// reading subsequent frames. The returned line excludes the trailing newline
// and is a fresh copy (safe to retain). err is io.EOF (or another read error)
// only at the actual end of the stream; a normal frame returns err == nil.
func ReadFrame(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		// The terminating newline doesn't count toward the limit.
		add := len(chunk)
		if e == nil && add > 0 && chunk[add-1] == '\n' {
			add--
		}
		if tooLong {
			// Already over the limit: keep draining to the newline,
			// discarding, so the next frame starts clean.
		} else if len(line)+add > max {
			tooLong = true
			line = nil
		} else {
			// ReadSlice's buffer is reused on the next read, so copy now.
			line = append(line, chunk...)
		}
		switch e {
		case nil:
			if !tooLong && len(line) > 0 && line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			return line, tooLong, nil
		case bufio.ErrBufferFull:
			continue // line longer than the bufio buffer; keep reading
		default:
			return line, tooLong, e // io.EOF or a real read error
		}
	}
}

// Reader is the carrier-side read policy over ReadFrame: it skips over-limit
// frames (reporting each through warn) instead of dying, and preserves the two
// bufio.Scanner behaviors carriers historically relied on — a final
// unterminated line is still delivered before EOF, and one trailing '\r' is
// stripped (CRLF-writing peers keep working). Read through this one type so
// the skip-and-continue policy cannot drift per site.
type Reader struct {
	r    *bufio.Reader
	max  int
	warn func(string)
	err  error // pending stream error after a delivered final line
}

// NewReader wraps r for frame-at-a-time reads bounded at max (or DefaultMaxBytes
// when max <= 0). warn, when non-nil, receives one message per skipped
// over-limit frame.
func NewReader(r io.Reader, max int, warn func(string)) *Reader {
	if max <= 0 {
		max = DefaultMaxBytes
	}
	return &Reader{r: bufio.NewReader(r), max: max, warn: warn}
}

// Read returns the next frame, skipping (and warning about) over-limit lines.
// It returns io.EOF at end of stream; a final unterminated line is returned
// first, with the EOF held for the following call.
func (f *Reader) Read() ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	for {
		line, tooLong, err := ReadFrame(f.r, f.max)
		if tooLong {
			if f.warn != nil {
				f.warn(fmt.Sprintf("dropped a frame over the %d MiB limit; the stream continues", f.max>>20))
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			if len(line) > 0 {
				// Unterminated final line: deliver it, surface the stream
				// error on the next call (Scanner parity).
				f.err = err
				return TrimCR(line), nil
			}
			return nil, err
		}
		return TrimCR(line), nil
	}
}

// TrimCR drops one trailing carriage return, matching bufio.ScanLines' CRLF
// tolerance. ReadFrame leaves it on (it strips only the '\n'), so callers that
// replace a Scanner must apply this to keep CRLF-writing peers working —
// exported so that the reject-policy readers get it from here rather than
// growing their own copy.
func TrimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}
