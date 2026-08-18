package chat

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ChunkMessage is the single outbound size chokepoint for both Loop and Bridge,
// and it had no direct test at all.
//
// The hard-split branch sliced raw bytes at limit, so a line over the limit was
// cut mid-rune whenever the boundary fell inside a multi-byte character — which
// is most of the time for CJK or emoji, and happens to any accented Latin text
// that lands unluckily. Both halves reach the user as replacement characters.
func TestAHardSplitNeverCutsARune(t *testing.T) {
	// Chinese: every rune is 3 bytes, so a limit that is not a multiple of 3
	// puts the byte cut inside a rune every single time.
	line := strings.Repeat("測試文字", 40) // 480 bytes, 160 runes
	const limit = 100                  // 100 % 3 != 0

	chunks := ChunkMessage(line, limit)
	if len(chunks) < 2 {
		t.Fatalf("fixture did not split: %d chunk(s) for %d bytes at limit %d", len(chunks), len(line), limit)
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8 — a rune was cut in half and the user sees �: %q", i, c)
		}
		if len(c) > limit {
			t.Errorf("chunk %d is %d bytes, over the %d-byte limit the connector declared", i, len(c), limit)
		}
	}
	if got := strings.Join(chunks, ""); got != line {
		t.Errorf("rejoining the chunks did not reproduce the input (%d bytes vs %d)", len(got), len(line))
	}
}

// The complement: chunking must not become "one rune per chunk" or "never
// split", either of which would satisfy the test above on its own.
func TestChunksFillTheLimit(t *testing.T) {
	line := strings.Repeat("測試文字", 40)
	const limit = 100

	chunks := ChunkMessage(line, limit)
	// 3-byte runes at a 100-byte limit: 33 runes = 99 bytes per chunk.
	for i, c := range chunks[:len(chunks)-1] {
		if len(c) != 99 {
			t.Errorf("chunk %d is %d bytes; a rune-safe cut at limit 100 over 3-byte runes should take 99", i, len(c))
		}
	}
	if want := (len(line) + 98) / 99; len(chunks) != want {
		t.Errorf("got %d chunks, want %d — the split is not filling the limit", len(chunks), want)
	}
}

// A rune wider than the whole limit has no correct answer: nothing valid fits.
// It must still terminate and still emit valid UTF-8, because returning a
// zero-length cut would spin the hard-split loop forever.
func TestARuneWiderThanTheLimitTerminates(t *testing.T) {
	done := make(chan []string, 1)
	go func() { done <- ChunkMessage("😀😀😀", 2) }() // 4-byte runes, 2-byte limit

	select {
	case chunks := <-done:
		if len(chunks) != 3 {
			t.Errorf("got %d chunks, want one per rune: %q", len(chunks), chunks)
		}
		for i, c := range chunks {
			if !utf8.ValidString(c) {
				t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ChunkMessage did not terminate: a cut of zero bytes never advances the hard-split loop")
	}
}

// Line-boundary splitting is the preferred path and must not regress: a body
// that fits on line boundaries should never reach the hard-split branch.
func TestSplitsOnLineBoundariesWhenItCan(t *testing.T) {
	body := "alpha\nbravo\ncharlie\ndelta"
	chunks := ChunkMessage(body, 12)

	for i, c := range chunks {
		if strings.HasPrefix(c, "\n") || strings.HasSuffix(c, "\n") {
			t.Errorf("chunk %d carries a boundary newline: %q", i, c)
		}
		if len(c) > 12 {
			t.Errorf("chunk %d is %d bytes, over the limit", i, len(c))
		}
	}
	if got := strings.Join(chunks, "\n"); got != body {
		t.Errorf("line-boundary chunking lost text: %q", got)
	}
}

func TestNoLimitReturnsOneChunk(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if got := ChunkMessage("anything at all", limit); len(got) != 1 || got[0] != "anything at all" {
			t.Errorf("limit %d: got %q, want the input unsplit", limit, got)
		}
	}
}
