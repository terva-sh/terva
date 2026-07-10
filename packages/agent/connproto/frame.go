package connproto

import (
	"bufio"
	"io"

	"terva.sh/terva/packages/lineframe"
)

// MaxFrameBytes is the largest single wire frame the read side accepts: the
// historical 4 MiB cap the connproto carriers used, enforced recoverably by
// lineframe (an over-limit frame is skipped, not fatal).
const MaxFrameBytes = lineframe.DefaultMaxBytes

// ReadFrame reads one '\n'-terminated frame from r, bounded at MaxFrameBytes.
// Thin wrapper over lineframe.ReadFrame — see that package for the recoverable
// framing contract shared across every terva wire protocol.
func ReadFrame(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	return lineframe.ReadFrame(r, MaxFrameBytes)
}

// FrameReader is the connproto carrier read policy: lineframe.Reader bounded at
// MaxFrameBytes. Every connproto carrier (connlocal's pipe pair, the external
// proxy's child stdio, the SDK's stdin loop) reads through this one type so the
// skip-and-continue policy cannot drift per site.
type FrameReader = lineframe.Reader

// NewFrameReader wraps r for frame-at-a-time reads. warn, when non-nil,
// receives one message per skipped over-limit frame.
func NewFrameReader(r io.Reader, warn func(string)) *FrameReader {
	return lineframe.NewReader(r, MaxFrameBytes, warn)
}
