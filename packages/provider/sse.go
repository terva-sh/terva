package provider

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"time"

	"terva.sh/terva/packages/lineframe"
)

// maxSSELineBytes bounds one line of a provider's text/event-stream. It is
// generous because a line is not always a small delta: Gemini streams a
// complete GenerateContentResponse per line, base64 inline image bytes
// included. Matches the ceiling the previous bufio.Scanner enforced.
const maxSSELineBytes = 10 << 20 // 10 MiB

// sseEvent is one parsed event from a text/event-stream.
type sseEvent struct {
	Event string // value of "event:" field (may be empty)
	Data  string // concatenated "data:" lines
}

// sseStream reads a text/event-stream in the background, delivering parsed
// events on Events() and the reason the read stopped on Err().
//
// The Err() half is the point. A bare `for sc.Scan()` loop cannot say *why* it
// ended, and this reader used to throw the answer away: a clean EOF, a
// mid-stream TCP reset, and a line past the size cap all reached the caller as
// nothing but a closed channel. Clients then inferred "truncated" from the
// missing terminal frame and reported all three as a transient network death —
// so an over-limit line, which is perfectly deterministic, was retried until
// the budget ran out, re-paying the input tokens each attempt, and then blamed
// on the network.
//
// The lines of an SSE response are one logical payload, so this reader takes
// lineframe's REJECT policy rather than Reader's skip-and-continue: dropping a
// data: line would punch a silent hole in the assistant's message. An
// over-limit line aborts the stream with a permanent error instead.
//
// The stream OWNS the response body: the reader goroutine closes it when its
// reads end, and callers defer Close instead of resp.Body.Close(). Close has
// to unpark the reader, which sits in one of two places, and each needs a
// different key:
//
//   - parked in Read, waiting for bytes that never come -> cancelling the
//     request (a cancelBody's Cancel) makes the pending Read fail. The body is
//     NOT closed from here: net/http does not support Close racing a Read, and
//     when the reader's Read reached EOF a beat after the terminal frame, the
//     HTTP/1 transport's end-of-body handshake left it parked until the
//     server dropped the idle connection — ~90 s per turn behind a keep-alive
//     proxy. A body without Cancel falls back to Close, the best available.
//   - parked on the buffered send, because the client returned (on ctx.Done,
//     or right after the terminal frame) and nobody drains -> only a receiver
//     can free it. A goroutine blocked on a channel send does not care that
//     its request is now cancelled.
//
// The second case is the one that leaked: every cancelled generation long
// enough to fill the 16-slot buffer stranded a goroutine and its connection
// for the life of the process. Draining in Close (rather than selecting on ctx
// in the send) also keeps cancellation semantics untouched: the channel is
// closed only after the client has left its select, so a closed Events channel
// can never race ctx.Done() into the client's !ok branch.
type sseStream struct {
	ch   chan sseEvent
	body io.ReadCloser
	done chan struct{} // closed when the reader goroutine has returned
	err  error         // written before ch closes; read only after it closes
}

// Events returns the event channel. It closes when the stream ends for any
// reason; check Err() afterwards to learn which.
func (s *sseStream) Events() <-chan sseEvent { return s.ch }

// Err returns the failure that ended the stream, or nil if it ended at a clean
// EOF. Safe to call once the Events channel has closed: the close
// happens-after the write, so the read needs no further synchronization.
func (s *sseStream) Err() error { return s.err }

// Close releases the reader goroutine and the underlying connection, and does
// not return until the goroutine has actually exited. It is idempotent and safe
// to call after the stream has already ended, so callers just defer it.
//
// All three steps are load-bearing, and each covers a different park:
//
//	cancel        frees a reader parked in Read (see the type comment)
//	drain         frees a reader parked on the buffered send
//	<-s.done      makes "released" a guarantee rather than a hope
//
// Without the wait, Close returning would say nothing about the goroutine —
// which is exactly how a leak like this hides from its own test.
func (s *sseStream) Close() {
	// Drain in the background so a reader parked on a send is freed while the
	// grace period runs; the goroutine ends when run closes the channel.
	go func() {
		for range s.ch { //nolint:revive // drain: unpark a blocked send
		}
	}()
	// Almost every stream ends by itself a beat after its terminal frame: the
	// chunk terminator rides in the same flush, the reader sees EOF, and the
	// connection goes back to the pool. Give it that beat before intervening —
	// cancelling here would abort a request that is finishing on its own and
	// churn the pooled connection on every turn.
	select {
	case <-s.done:
		return
	case <-time.After(sseCloseGrace):
	}
	if c, ok := s.body.(interface{ Cancel() }); ok {
		c.Cancel()
	} else {
		_ = s.body.Close()
	}
	<-s.done
}

// sseCloseGrace is how long Close lets the reader finish on its own before it
// cancels the request. Generous against a normal server (the terminator
// arrives within a round trip of the terminal frame) and short against a proxy
// that idles the connection instead of ending the body.
const sseCloseGrace = 250 * time.Millisecond

// newSSEStream starts reading body as a text/event-stream, taking ownership of
// it. provider names the client, for error attribution.
func newSSEStream(body io.ReadCloser, provider string) *sseStream {
	s := &sseStream{ch: make(chan sseEvent, 16), body: body, done: make(chan struct{})}
	go s.run(body, provider)
	return s
}

func (s *sseStream) run(r io.Reader, provider string) {
	defer close(s.done) // declared first, so it runs after close(s.ch)
	defer close(s.ch)
	defer s.body.Close() // the reader owns the body; no Read is in flight here
	br := bufio.NewReaderSize(r, 64*1024)

	var ev sseEvent
	flush := func() {
		if ev.Data == "" && ev.Event == "" {
			return
		}
		s.ch <- ev
		ev = sseEvent{}
	}

	for {
		line, tooLong, err := lineframe.ReadFrame(br, maxSSELineBytes)
		switch {
		case tooLong:
			// Reject, don't skip: this event's payload is gone, and every
			// event carries part of one response. Fail loudly, and
			// permanently — a retry would re-read the same oversized line.
			s.err = NewStreamLimitError(provider, maxSSELineBytes)
			return
		case err == nil:
			// A complete line, terminating newline consumed. Feed it even when
			// empty: the blank line is SSE's event separator, not filler.
			s.feed(string(lineframe.TrimCR(line)), &ev, flush)
		case errors.Is(err, io.EOF):
			// A final unterminated line arrives together with the EOF; feed it
			// before honoring the EOF, as bufio.Scanner did.
			if len(line) > 0 {
				s.feed(string(lineframe.TrimCR(line)), &ev, flush)
			}
			flush() // deliver a trailing event that lacked its blank line
			return
		default:
			// Transport died mid-stream. Drop the half-read event rather than
			// flushing a truncated payload, and report the real cause.
			s.err = NewStreamReadError(provider, err)
			return
		}
	}
}

// feed applies one SSE line to the event under construction.
func (s *sseStream) feed(line string, ev *sseEvent, flush func()) {
	if line == "" {
		flush()
		return
	}
	if strings.HasPrefix(line, ":") {
		return // comment / keep-alive
	}
	field, value, ok := strings.Cut(line, ":")
	if !ok {
		field = line
		value = ""
	}
	// optional single leading space after ':'
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		ev.Event = value
	case "data":
		if ev.Data != "" {
			ev.Data += "\n"
		}
		ev.Data += value
	}
}
