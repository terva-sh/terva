package tui

import "testing"

// TestTruncateToWidth locks in the behaviour of the byte-indexed
// truncation rewrite (no []rune allocation): plain clipping, the
// byte-length fast path, zero-width ANSI preservation, wide runes,
// inline-image passthrough, and invalid-UTF-8 handling.
func TestTruncateToWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want string
	}{
		{"plain truncate", "hello world", 5, "hello"},
		{"fits exactly", "hello", 5, "hello"},
		{"byte fast path", "hi", 5, "hi"},
		{"cols zero passthrough", "hello", 0, "hello"},
		// Visible width 2 but 13 bytes: must render in full at cols 5,
		// i.e. styled lines aren't clipped by their escape bytes.
		{"styled fits by width", "\x1b[31mhi\x1b[0m", 5, "\x1b[31mhi\x1b[0m"},
		// Reset sitting right at the cut point is preserved.
		{"reset at cut survives", "\x1b[31mhi\x1b[0m x", 2, "\x1b[31mhi\x1b[0m"},
		// Wide (2-cell) runes: 4 cells = two CJK chars.
		{"wide runes", "日本語", 4, "日本"},
		// Inline-image escape: returned untouched.
		{"image escape passthrough", "\x1b]1337;File=abc", 3, "\x1b]1337;File=abc"},
		// Invalid byte maps to U+FFFD, matching the old []rune(s) path.
		{"invalid utf8", "\xffabcde", 3, "�ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncateToWidth(c.in, c.cols); got != c.want {
				t.Fatalf("truncateToWidth(%q, %d) = %q, want %q", c.in, c.cols, got, c.want)
			}
		})
	}
}

// TestTruncateCached verifies the Renderer-level memo returns exactly
// what truncateToWidth would, serves repeats from cache, and drops the
// cache when the column count changes.
func TestTruncateCached(t *testing.T) {
	line := "\x1b[31mhello world\x1b[0m and then some more text"
	r := &Renderer{cols: 5}

	want5 := truncateToWidth(line, 5)
	if got := r.truncateCached(line); got != want5 {
		t.Fatalf("first call = %q, want %q", got, want5)
	}
	if got := r.truncateCached(line); got != want5 { // cache hit
		t.Fatalf("cached call = %q, want %q", got, want5)
	}

	// A resize must invalidate, not serve the width-5 result at width 12.
	r.cols = 12
	want12 := truncateToWidth(line, 12)
	if got := r.truncateCached(line); got != want12 {
		t.Fatalf("after resize = %q, want %q", got, want12)
	}

	// Short lines bypass the cache and return unchanged.
	if got := r.truncateCached("hi"); got != "hi" {
		t.Fatalf("short line = %q, want %q", got, "hi")
	}
}
