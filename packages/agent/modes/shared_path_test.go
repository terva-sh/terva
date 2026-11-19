package modes

// The path half of the shared-file cards: the transcript record is a handle,
// so the TUI asks the store where each share on screen lives and hands the
// renderer a path the user can open.
//
// The properties worth pinning are who may be shown a path and what the ask
// costs. The listing names the daemon's disk, so a remote client must never
// print it. A card is on screen every frame, so an ask that is not claimed
// becomes a directory listing per repaint.

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// pathInteractive is a TUI bound to sess whose store lists the given entries.
// local is cfg.CarrierLocal: true for the in-process carrier, false for attach.
func pathInteractive(c *shareCarrier, sess string, local bool) *Interactive {
	i := previewInteractive(c, sess)
	i.cfg.CarrierLocal = local
	return i
}

// waitForPath waits for a listing to land in the path cache.
func waitForPath(t *testing.T, i *Interactive, id string) string {
	t.Helper()
	for range 200 {
		if path := i.sharedPathMap()[id]; path != "" {
			return path
		}
		<-time.After(time.Millisecond)
	}
	t.Fatalf("the path for %s never arrived", id)
	return ""
}

// waitForListings blocks until the carrier has served n listings.
func waitForListings(t *testing.T, c *shareCarrier, n int) {
	t.Helper()
	for range 200 {
		if c.listCount() >= n {
			return
		}
		<-time.After(time.Millisecond)
	}
	t.Fatalf("only %d of %d expected listings were served", c.listCount(), n)
}

// settle gives a goroutine that should NOT exist every chance to run, so a
// count read afterwards is a result and not an early arrival.
func settle() { <-time.After(20 * time.Millisecond) }

// The path the card depends on: a share on screen gets its path from the
// store's listing.
func TestSharedPathIsFetchedOnALocalCarrier(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "chart.png", "/store/s1/shr_a-chart.png")}
	i := pathInteractive(c, "s1", true)

	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image"}})

	if got := waitForPath(t, i, "shr_a"); got != "/store/s1/shr_a-chart.png" {
		t.Errorf("path = %q, want the listing's", got)
	}
}

// 🚨 The rule the whole feature rests on. On `terva attach` the listing's path
// names another machine's disk. A card that printed it would send the user to
// a file that is not there, or to a different file of the same name, and
// nothing on screen would say which.
func TestSharedPathIsNeverFetchedOnARemoteCarrier(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png")}
	i := pathInteractive(c, "s1", false)

	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image"}})
	settle()

	if n := c.listCount(); n != 0 {
		t.Errorf("a remote client listed the store %d times for a path it must not show", n)
	}
	if paths := i.sharedPathMap(); len(paths) != 0 {
		t.Errorf("a remote client holds daemon paths: %v", paths)
	}
	if !i.sharedStoreIsRemote() {
		t.Error("a remote carrier that serves the verbs is not reported remote, so the card has no save hint")
	}
}

// The /shared panel's refresh reads the same listing, and it must not become a
// side door for the daemon's paths on a remote client.
func TestARemoteRefreshDoesNotFillThePathCache(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "chart.png", "/daemon/side/shr_a-chart.png")}
	i := pathInteractive(c, "s1", false)

	if err := i.refreshSharedFiles(); err != nil {
		t.Fatalf("refreshSharedFiles: %v", err)
	}
	if paths := i.sharedPathMap(); len(paths) != 0 {
		t.Errorf("the panel's refresh handed a remote card daemon paths: %v", paths)
	}
}

// A local carrier shows the path, so it must not also tell the user to save a
// copy, and a carrier with no verbs has no /shared to point at.
func TestSharedStoreIsRemoteOnlyWhenThereIsAStoreElsewhere(t *testing.T) {
	local := pathInteractive(newShareCarrier(nil), "s1", true)
	if local.sharedStoreIsRemote() {
		t.Error("a local carrier is reported remote")
	}

	bare := newCtrlprotoTestInteractive()
	bare.cfg.CarrierSession = "s1"
	if _, ok := bare.sharedCarrier(); ok {
		t.Fatal("the bare fixture serves the share verbs, so it proves nothing here")
	}
	if bare.sharedStoreIsRemote() {
		t.Error("a carrier without the share verbs is reported as a remote store")
	}
}

// A card is on screen every frame. One listing answers for the whole session,
// so ten cards and a hundred repaints cost one request.
func TestSharedPathsCostOneListingHoweverManyFrames(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{
		entry("shr_a", "a.png", "/store/s1/shr_a-a.png"),
		entry("shr_b", "b.png", "/store/s1/shr_b-b.png"),
	}
	i := pathInteractive(c, "s1", true)
	shares := []core.SharedFile{{ID: "shr_a", Kind: "image"}, {ID: "shr_b", Kind: "image"}}

	for range 100 {
		i.fetchSharedPaths(shares)
	}
	waitForPath(t, i, "shr_b")
	settle()

	if n := c.listCount(); n != 1 {
		t.Errorf("the store was listed %d times for two cards, want once", n)
	}
}

// A share the sweeper took is absent from the listing for good. The claim must
// outlive that answer, or its card lists the directory on every repaint to
// learn the same thing.
func TestASweptShareIsNotListedForAgainEveryFrame(t *testing.T) {
	c := newShareCarrier(nil) // the store lists nothing
	i := pathInteractive(c, "s1", true)
	shares := []core.SharedFile{{ID: "shr_gone", Kind: "image"}}

	i.fetchSharedPaths(shares)
	waitForListings(t, c, 1)
	for range 50 {
		i.fetchSharedPaths(shares)
	}
	settle()

	if n := c.listCount(); n != 1 {
		t.Errorf("a swept share cost %d listings, want one", n)
	}
}

// A share that lands later in the session is a new id, and it must get its own
// ask: the first listing predates it.
func TestALaterShareTriggersAFreshListing(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "a.png", "/store/s1/shr_a-a.png")}
	i := pathInteractive(c, "s1", true)

	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Kind: "image"}})
	waitForPath(t, i, "shr_a")

	c.mu.Lock()
	c.list = append(c.list, entry("shr_b", "b.png", "/store/s1/shr_b-b.png"))
	c.mu.Unlock()
	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Kind: "image"}, {ID: "shr_b", Kind: "image"}})

	if got := waitForPath(t, i, "shr_b"); got != "/store/s1/shr_b-b.png" {
		t.Errorf("the later share's path = %q", got)
	}
}

// A card that failed to get its path must not stay without one for the rest of
// the session. Opening /shared reads the same listing, so it repairs the card.
func TestOpeningThePanelFillsAPathTheCardMissed(t *testing.T) {
	c := newShareCarrier(nil)
	i := pathInteractive(c, "s1", true)

	// The card's own ask finds nothing, and the claim stops it asking again.
	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Kind: "image"}})
	waitForListings(t, c, 1)
	settle()
	if paths := i.sharedPathMap(); len(paths) != 0 {
		t.Fatalf("a path arrived from an empty listing: %v", paths)
	}

	c.mu.Lock()
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "a.png", "/store/s1/shr_a-a.png")}
	c.mu.Unlock()
	if err := i.refreshSharedFiles(); err != nil {
		t.Fatalf("refreshSharedFiles: %v", err)
	}

	if got := i.sharedPathMap()["shr_a"]; got != "/store/s1/shr_a-a.png" {
		t.Errorf("the panel's listing did not reach the card: %q", got)
	}
}

// A share id resolves only inside the session that published it, and so does
// its path. A caller still holding the old session id must not list for it.
func TestAPathFetchForASupersededSessionIsRefused(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "a.png", "/store/s1/shr_a-a.png")}
	i := pathInteractive(c, "s2", true) // the binding moved on

	i.fetchSharedPathsFor("s1", []core.SharedFile{{ID: "shr_a", Kind: "image"}})
	settle()

	if n := c.listCount(); n != 0 {
		t.Errorf("listed %d times for a session the TUI has left", n)
	}
}

// A listing that lands after a switch belongs to the conversation the user
// left. Its paths must not reach the new binding's cards.
func TestAPathLandingAfterASessionSwitchIsDropped(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "a.png", "/store/s1/shr_a-a.png")}
	c.listRelease = make(chan struct{})
	i := pathInteractive(c, "s1", true)

	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Kind: "image"}})
	waitForListings(t, c, 1)

	// The user switches sessions while the listing is in flight.
	i.mu.Lock()
	i.cfg.CarrierSession = "s2"
	i.sharedPaths = nil
	i.sharedPathsAsked = nil
	i.sharedPathsSession = "s2"
	i.mu.Unlock()
	close(c.listRelease)
	settle()

	if paths := i.sharedPathMap(); len(paths) != 0 {
		t.Errorf("a previous session's path is in the new binding's cache: %v", paths)
	}
}

// The renderer reads what it is given outside the lock while the fetch
// goroutine may still be writing, so it must never be handed the live map.
func TestSharedPathMapHandsOutACopy(t *testing.T) {
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "a.png", "/store/s1/shr_a-a.png")}
	i := pathInteractive(c, "s1", true)
	i.fetchSharedPaths([]core.SharedFile{{ID: "shr_a", Kind: "image"}})
	waitForPath(t, i, "shr_a")

	got := i.sharedPathMap()
	got["shr_a"] = "/somewhere/else"

	if again := i.sharedPathMap()["shr_a"]; again != "/store/s1/shr_a-a.png" {
		t.Errorf("a caller's write reached the cache: %q", again)
	}
}

// The join the two halves meet at: the path the host fetched is the path the
// card prints. Each half passes alone with a field name that does not match.
func TestAFetchedPathReachesTheCard(t *testing.T) {
	const path = "/store/s1/shr_a-deliverable.pdf"
	c := newShareCarrier(nil)
	c.list = []ctrlproto.SharedFileEntry{entry("shr_a", "deliverable.pdf", path)}
	i := pathInteractive(c, "s1", true)
	i.appendCarrierToolResultLocked(toolResultWithShares("call_1", core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "deliverable.pdf", Kind: "document", Size: 2048,
	}))

	msgs := i.carrierTranscript()
	i.fetchSharedPaths(transcriptShares(msgs))
	waitForPath(t, i, "shr_a")

	v := tui.View{Theme: tui.Dark, Messages: msgs, SharedPaths: i.sharedPathMap(), SharedRemote: i.sharedStoreIsRemote()}
	if plain := stripANSI(v.Build(120)); !strings.Contains(plain, path) {
		t.Errorf("the fetched path did not reach the card:\n%s", plain)
	}
}
