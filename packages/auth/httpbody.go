package auth

import "io"

// maxTokenBodyBytes caps OAuth token, device-authorization, and token-poll
// responses that we parse as JSON. Legitimate payloads are a few KiB; the cap
// keeps a hostile or misconfigured auth endpoint from forcing an unbounded read
// (the auth package sits below provider, so it carries its own copy of this
// helper rather than importing provider's).
const maxTokenBodyBytes = 1 << 20 // 1 MiB

// readCappedBody reads up to max bytes from r, returning the bytes and whether
// the source exceeded the cap (was truncated). It reads max+1 so truncation is
// detectable, then trims to max.
func readCappedBody(r io.Reader, max int64) ([]byte, bool) {
	b, _ := io.ReadAll(io.LimitReader(r, max+1))
	if int64(len(b)) > max {
		return b[:max], true
	}
	return b, false
}
