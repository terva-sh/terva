package tui

// Tests for the path row of a shared-file card.
//
// The row exists for the terminal that draws no picture: there the card was a
// name and a size, and the path is the one thing on it a user can act on. So
// the properties worth pinning are the ones that decide whether the path can
// be used. It must be the whole path, it must appear when it arrives late, and
// it must stay off a card whose client is not on the store's host.

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// sharePath is long enough to wrap at the widths these tests use, and carries
// no whitespace, so "the whole path is on screen" can be asserted by removing
// the wrap's own whitespace and looking for it.
const sharePath = "/home/someone/.terva/shared/ses_0123456789abcdef/shr_00112233445566778899-quarterly-latency.png"

// pathCardView is a transcript holding one image share, with no preview bytes:
// the state of every terminal that cannot draw an image.
func pathCardView(t *testing.T, f SharedFile) View {
	t.Helper()
	f.ID, f.CallID = "shr_a", "t1"
	return View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"out/latency.png"}`),
		{
			Role:    provider.RoleTool,
			Content: []provider.Content{toolResult("t1", false, "shared")},
			Meta:    sharedMeta(t, f),
		},
	}}
}

// unwrapped removes every break and indent the renderer added, so a path that
// spans rows reads as one string again.
func unwrapped(rows []string) string {
	plain := stripANSI(strings.Join(rows, "\n"))
	return strings.Join(strings.Fields(plain), "")
}

// The feature: a card names where the file is.
func TestSharedFileCardShowsThePath(t *testing.T) {
	v := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	v.SharedPaths = map[string]string{"shr_a": sharePath}

	plain := stripANSI(strings.Join(v.Build(200), "\n"))
	if !strings.Contains(plain, sharePath) {
		t.Fatalf("the card does not show the file's path:\n%s", plain)
	}
}

// A card with no path known renders as it always did. The path row is an
// addition, and its absence must not leave a blank line or a placeholder.
func TestSharedFileCardWithoutAPathAddsNoRow(t *testing.T) {
	bare := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	other := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	other.SharedPaths = map[string]string{"shr_other": sharePath}

	if a, b := len(bare.Build(80)), len(other.Build(80)); a != b {
		t.Errorf("a path for another share changed this card's height: %d rows, then %d", a, b)
	}
}

// 🔑 The reason the row wraps instead of taking appendTruncated like the rest of
// the card. A truncated name still names the file. A truncated path opens
// nothing, and it fails without saying so: it looks like a path.
func TestSharedFileCardWrapsALongPathInsteadOfCuttingIt(t *testing.T) {
	const width = 40
	v := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	v.SharedPaths = map[string]string{"shr_a": sharePath}

	rows := v.Build(width)
	if got := unwrapped(rows); !strings.Contains(got, sharePath) {
		t.Fatalf("the path did not survive a %d-column terminal whole:\n%s", width, stripANSI(strings.Join(rows, "\n")))
	}
	for _, row := range rows {
		if w := visibleWidth(row); w > width {
			t.Errorf("a row is %d cells wide on a %d-column terminal: %q", w, width, stripANSI(row))
		}
	}
}

// The pieces must rejoin into the exact path at every width, including one
// narrower than the indent, where the row gives up alignment before it gives
// up a character.
func TestSharedPathRowsRejoinIntoThePath(t *testing.T) {
	for _, width := range []int{1, 3, 7, 20, 41, 80, 400} {
		rows := sharedPathRows(Dark, "      ", sharePath, width)
		var got strings.Builder
		for _, row := range rows {
			got.WriteString(strings.TrimLeft(stripANSI(row), " "))
		}
		if got.String() != sharePath {
			t.Errorf("width %d: rows rejoin to %q, want the path", width, got.String())
		}
		if width >= 7 {
			for _, row := range rows {
				if w := visibleWidth(row); w > width {
					t.Errorf("width %d: a row is %d cells wide", width, w)
				}
			}
		}
	}
}

// The path comes from a second request that lands after the card is on screen,
// and it changes nothing about the message. Only the cache key's share
// component tells the two renders apart, which is the trap the preview bytes
// fell into first (see TestSharedFileCardShowsAPreviewThatLandsAfterTheCard).
func TestSharedFileCardShowsAPathThatLandsAfterTheCard(t *testing.T) {
	v := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})

	if plain := stripANSI(strings.Join(v.Build(200), "\n")); strings.Contains(plain, sharePath) {
		t.Fatalf("the path cannot be there before the listing lands:\n%s", plain)
	}

	v.SharedPaths = map[string]string{"shr_a": sharePath}

	if plain := stripANSI(strings.Join(v.Build(200), "\n")); !strings.Contains(plain, sharePath) {
		t.Errorf("the cache served back the row painted before the path arrived:\n%s", plain)
	}
}

// On `terva attach` the store is on another machine. The card must not print
// that machine's path, and it must not leave the user with a bare name either.
func TestSharedFileCardOnARemoteStorePointsAtSave(t *testing.T) {
	v := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	v.SharedRemote = true

	plain := stripANSI(strings.Join(v.Build(200), "\n"))
	if !strings.Contains(plain, "/shared") || !strings.Contains(plain, "saves a copy") {
		t.Errorf("a remote card does not say how to get the file:\n%s", plain)
	}
}

// The hint lands on a cached row for the same reason the path does, so it
// needs the same proof that the cache key sees it.
func TestSharedFileCardRemoteHintDefeatsTheRenderCache(t *testing.T) {
	v := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	if plain := stripANSI(strings.Join(v.Build(200), "\n")); strings.Contains(plain, "saves a copy") {
		t.Fatalf("a local card carries the remote hint:\n%s", plain)
	}

	v.SharedRemote = true

	if plain := stripANSI(strings.Join(v.Build(200), "\n")); !strings.Contains(plain, "saves a copy") {
		t.Errorf("the cache served back the row painted before the carrier was known remote:\n%s", plain)
	}
}

// An expired share is one the sweeper may already have taken. A path printed
// under "expired" invites the user to open a file that is gone.
func TestSharedFileCardHidesThePathOfAnExpiredShare(t *testing.T) {
	expired := pinnedNow.Add(-time.Hour).Format(time.RFC3339)

	local := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image", ExpiresAt: expired})
	local.SharedPaths = map[string]string{"shr_a": sharePath}
	if plain := stripANSI(strings.Join(local.Build(200), "\n")); strings.Contains(plain, sharePath) {
		t.Errorf("an expired card still offers its path:\n%s", plain)
	}

	remote := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image", ExpiresAt: expired})
	remote.SharedRemote = true
	if plain := stripANSI(strings.Join(remote.Build(200), "\n")); strings.Contains(plain, "saves a copy") {
		t.Errorf("an expired card still offers a save that will fail:\n%s", plain)
	}
}

// A path is the one field on the card that must be exact. One that carries an
// escape cannot be shown as it is, and a cleaned copy would name a file that
// does not exist, so the row is left out.
func TestSharedFileCardDropsAPathItCannotShowExactly(t *testing.T) {
	const hostile = "/home/someone/.terva/shared/s1/shr_a-\x1b[31mred.png"
	v := pathCardView(t, SharedFile{Name: "latency.png", Kind: "image"})
	v.SharedPaths = map[string]string{"shr_a": hostile}

	raw := strings.Join(v.Build(200), "\n")
	if strings.Contains(raw, "\x1b[31mred.png") {
		t.Errorf("an escape in a path reached the terminal:\n%q", raw)
	}
	if strings.Contains(stripANSI(raw), "red.png") {
		t.Errorf("the card printed a cleaned-up path, which names no file:\n%s", stripANSI(raw))
	}
}
