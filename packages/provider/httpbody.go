package provider

import (
	"io"
	"strings"
)

// Response bodies from provider endpoints are read for two reasons: to include
// a snippet in a diagnostic error, or to parse a discovery/model listing. Both
// were previously read with an unbounded io.ReadAll, so a hostile, compromised,
// or merely misconfigured endpoint could force an arbitrarily large allocation
// (and an oversized intermediate error string). These caps bound that.
const (
	// maxErrorBodyBytes caps a body read purely for an error message. Providers
	// occasionally return a large HTML page from an edge proxy; 8 KiB is plenty
	// of context to diagnose the failure.
	maxErrorBodyBytes = 8 << 10 // 8 KiB
	// maxDiscoveryBodyBytes caps model-discovery responses we parse as JSON.
	// Legitimate listings are far smaller; the cap keeps a bad endpoint from
	// forcing an unbounded read.
	maxDiscoveryBodyBytes = 1 << 20 // 1 MiB
)

// readBodyCapped reads up to max bytes from r, returning the bytes and whether
// the source exceeded the cap (was truncated). It reads max+1 so truncation is
// detectable, then trims to max.
func readBodyCapped(r io.Reader, max int64) (body []byte, truncated bool) {
	b, _ := io.ReadAll(io.LimitReader(r, max+1))
	if int64(len(b)) > max {
		return b[:max], true
	}
	return b, false
}

// errorBodySnippet reads a response body for inclusion in an error message:
// capped at maxErrorBodyBytes, space-trimmed, with a truncation marker when the
// body was clipped. It does not close r.
func errorBodySnippet(r io.Reader) string {
	b, truncated := readBodyCapped(r, maxErrorBodyBytes)
	s := strings.TrimSpace(string(b))
	if truncated {
		s += " …[truncated]"
	}
	return s
}
