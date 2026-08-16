package modes

// The preview half of the shared-file cards: the TUI has no HTTP route, so it
// pulls an image share's bytes over shared.fetch and hands them to the renderer.
//
// The properties worth pinning are the ones that cost something when wrong: a
// card sits on screen every frame, so an unclaimed fetch becomes a request per
// repaint; a share id means nothing outside its own session; and a carrier that
// cannot serve the verb must degrade to a card without a picture rather than to
// an error.

import (
	"context"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// shareCarrier serves the two shared-file verbs and counts what was asked for.
type shareCarrier struct {
	*fakeCarrier
	mu      sync.Mutex
	asked   []string
	listed  int // shared.list calls, which is how the refresh verb is observed
	data    map[string][]byte
	list    []ctrlproto.SharedFileEntry
	err     error
	release chan struct{} // when non-nil, a fetch parks until this closes
	// name overrides the filename a fetch reports. The daemon sanitized this
	// name on the way in, so a hostile one can only arrive across a machine
	// boundary — which is exactly the case the save path re-checks.
	name string
}

func newShareCarrier(data map[string][]byte) *shareCarrier {
	return &shareCarrier{fakeCarrier: newFakeCarrier(), data: data}
}

func (c *shareCarrier) SharedFiles(context.Context, string) ([]ctrlproto.SharedFileEntry, error) {
	c.mu.Lock()
	c.listed++
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	return c.list, nil
}

// listCount is how many times the listing verb was called — the only
// observable the refresh action leaves behind.
func (c *shareCarrier) listCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listed
}

func (c *shareCarrier) SharedFileFetch(_ context.Context, sess string, p ctrlproto.SharedFileRef) (ctrlproto.SharedFileContent, error) {
	c.mu.Lock()
	c.asked = append(c.asked, sess+"/"+p.ID)
	release := c.release
	c.mu.Unlock()
	if release != nil {
		<-release
	}
	if c.err != nil {
		return ctrlproto.SharedFileContent{}, c.err
	}
	name := c.name
	if name == "" {
		name = "chart.png"
	}
	return ctrlproto.SharedFileContent{ID: p.ID, Name: name, Mime: "image/png", Data: c.data[p.ID]}, nil
}

func (c *shareCarrier) askedFor() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

// waitForAsks blocks until the carrier has recorded n requests.
//
// The CLAIM that suppresses duplicate fetches is taken synchronously, but the
// request itself goes out on another goroutine — so a test that reads the
// counter straight after the call is racing the fetch it wants to count, and
// would pass by arriving early rather than by the code being right.
func (c *shareCarrier) waitForAsks(t *testing.T, n int) {
	t.Helper()
	for range 200 {
		if len(c.askedFor()) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d of %d expected requests were made", len(c.askedFor()), n)
}

// previewInteractive is an Interactive bound to sess, exactly as the three
// construction paths (attach, ctrlproto, replay) leave one: cfg.CarrierSession
// set, and the preview cache UNARMED.
//
// It deliberately does not stamp sharedPreviewsSession. An earlier version did,
// with a comment claiming that was "the state the session-bind path leaves
// behind" — it was not, and arming it by hand here is what hid the fact that
// previews never loaded until the user switched sessions. The fetch adopts the
// binding on first use, so the honest fixture is the one that arms nothing.
func previewInteractive(c *shareCarrier, sess string) *Interactive {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = sess
	return i
}

// waitForPreview waits for a fetch to land in the cache. The fetch is
// deliberately off the render goroutine, so the test synchronizes on the result
// rather than on a sleep.
func waitForPreview(t *testing.T, i *Interactive, id string) []byte {
	t.Helper()
	for range 200 {
		i.mu.Lock()
		data := i.sharedPreviews[id]
		i.mu.Unlock()
		if len(data) > 0 {
			return data
		}
		<-time.After(time.Millisecond)
	}
	t.Fatalf("preview for %s never arrived", id)
	return nil
}

// The path the card depends on: an image share's bytes reach the cache the
// renderer reads.
func TestImageSharePreviewIsFetched(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("PNGDATA")})
	i := previewInteractive(c, "s1")

	i.fetchSharedPreviews([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}})

	if got := string(waitForPreview(t, i, "shr_a")); got != "PNGDATA" {
		t.Errorf("cached preview = %q, want the fetched bytes", got)
	}
	if got := c.askedFor(); len(got) != 1 || got[0] != "s1/shr_a" {
		t.Errorf("asked for %v, want exactly s1/shr_a", got)
	}
}

// A card is on screen every frame. Without a claim taken BEFORE the request
// goes out, every repaint would launch another fetch for the same file — the
// bug this test exists to prevent, so it drives the call the way the render
// path does: repeatedly, while the first is still in flight.
func TestSharePreviewIsFetchedOncePerShare(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("PNGDATA")})
	c.release = make(chan struct{})
	i := previewInteractive(c, "s1")

	files := []core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}}
	for range 5 {
		i.fetchSharedPreviews(files)
	}
	c.waitForAsks(t, 1)
	close(c.release)
	waitForPreview(t, i, "shr_a")

	// And once more after the bytes have landed: a cached share must not be
	// re-requested either.
	i.fetchSharedPreviews(files)

	if got := c.askedFor(); len(got) != 1 {
		t.Errorf("asked %d times for one share (%v) — the render path is re-fetching every frame", len(got), got)
	}
}

// Only images become previews. A PDF's bytes do nothing for a terminal that the
// path does not do better, and a video would be an expensive way to render
// nothing.
//
// The eligible image is the POSITIVE CONTROL, and it is what makes the negative
// assertion mean anything. The claim is taken synchronously but the request
// leaves on another goroutine, so "nothing was asked for" is also what you see
// by looking before anything could have been — this test and its neighbour both
// passed with the kind and size filters deleted outright. Waiting for a share
// that MUST be fetched proves the fetch pass ran to completion, and only then
// does the absence of the others carry information.
func TestOnlyImageSharesArePreviewed(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_ok": []byte("PNGDATA")})
	i := previewInteractive(c, "s1")

	i.fetchSharedPreviews([]core.SharedFile{
		{ID: "shr_doc", Name: "report.pdf", Kind: "document", Size: 10},
		{ID: "shr_vid", Name: "clip.mp4", Kind: "video", Size: 10},
		{ID: "shr_aud", Name: "take.mp3", Kind: "audio", Size: 10},
		{ID: "shr_ok", Name: "chart.png", Kind: "image", Size: 7},
	})

	c.waitForAsks(t, 1)
	waitForPreview(t, i, "shr_ok")

	if got := c.askedFor(); len(got) != 1 || got[0] != "s1/shr_ok" {
		t.Errorf("asked for %v, want the image alone", got)
	}
}

// An image too large to be worth a thumbnail is left alone. It is still listed,
// still has a path, and still opens in a real viewer.
//
// Same shape as its neighbour above, for the same reason: the small image is
// the positive control that proves the pass completed before the oversized
// one's absence is read as a decision rather than as a race won.
func TestOversizedImageShareIsNotPreviewed(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_small": []byte("PNGDATA")})
	i := previewInteractive(c, "s1")

	i.fetchSharedPreviews([]core.SharedFile{
		{ID: "shr_big", Name: "huge.png", Kind: "image", Size: maxPreviewBytes + 1},
		{ID: "shr_small", Name: "chart.png", Kind: "image", Size: 7},
	})

	c.waitForAsks(t, 1)
	waitForPreview(t, i, "shr_small")

	if got := c.askedFor(); len(got) != 1 || got[0] != "s1/shr_small" {
		t.Errorf("asked for %v, want the oversized image left alone", got)
	}
}

// A fetch that fails must not retry forever. `fetched` is marked before the
// request, so a failure costs one request rather than one per frame.
func TestAFailedPreviewFetchIsNotRetriedEveryFrame(t *testing.T) {
	c := newShareCarrier(nil)
	c.err = ctrlproto.Errorf(ctrlproto.CodeNotFound, "swept")
	i := previewInteractive(c, "s1")

	files := []core.SharedFile{{ID: "shr_gone", Name: "gone.png", Kind: "image", Size: 7}}
	i.fetchSharedPreviews(files)
	c.waitForAsks(t, 1)

	// Now that the failure has been observed, keep repainting. The claim must
	// survive it, or a card for a swept file re-requests forever.
	for range 4 {
		i.fetchSharedPreviews(files)
	}
	time.Sleep(10 * time.Millisecond) // let any stray request land and be counted

	if got := c.askedFor(); len(got) != 1 {
		t.Errorf("asked %d times after a failure (%v), want one", len(got), got)
	}
	i.mu.Lock()
	cached := len(i.sharedPreviews)
	i.mu.Unlock()
	if cached != 0 {
		t.Errorf("a failed fetch cached %d entries, want none", cached)
	}
}

// Previews must load on the ORDINARY startup path, with no session switch
// anywhere in it.
//
// The three construction paths (attach, ctrlproto, replay) build an Interactive
// that is ALREADY bound to its session and never call SwitchCarrierSession —
// which used to be the only place the preview cache was stamped. So the fetch
// returned early on every frame and a card never got its picture until the user
// switched sessions, which is the one thing most sessions never do. The whole
// feature was dead on the path nearly everyone takes.
func TestPreviewsLoadOnTheStartupPathWithNoSessionSwitch(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("PNGDATA")})
	i := newCtrlprotoTestInteractive()
	// Exactly what the constructors leave behind: bound, and nothing else.
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"

	i.fetchSharedPreviews([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}})

	if got := string(waitForPreview(t, i, "shr_a")); got != "PNGDATA" {
		t.Errorf("preview = %q, want the fetched bytes", got)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sharedPreviewsSession != "s1" {
		t.Errorf("the cache adopted %q, want the bound session", i.sharedPreviewsSession)
	}
}

// Adopting a binding on first use must not adopt a STALE one. The id read
// before the lock is checked against the session the TUI is actually bound to,
// so a fetch that raced a switch is dropped rather than stamping the cache with
// a session that has already gone.
func TestAFetchForASupersededSessionDoesNotArmTheCache(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("PNGDATA")})
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s2" // the binding moved on

	// A caller still holding the old id asks for its preview.
	i.fetchSharedPreviewsFor("s1", []core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}})

	if asked := c.askedFor(); len(asked) != 0 {
		t.Errorf("fetched %v for a session the TUI has left", asked)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sharedPreviewsSession == "s1" {
		t.Error("the cache armed itself for a superseded session")
	}
}

// A share id resolves only inside the session that published it. A fetch that
// lands after a switch must not deposit bytes into the new binding's cache,
// where a card would draw another conversation's picture.
func TestAPreviewLandingAfterASessionSwitchIsDropped(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("PNGDATA")})
	c.release = make(chan struct{})
	i := previewInteractive(c, "s1")

	i.fetchSharedPreviews([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}})

	// The user switches sessions while the fetch is in flight.
	i.mu.Lock()
	i.cfg.CarrierSession = "s2"
	i.sharedPreviews = nil
	i.sharedPreviewsFetched = nil
	i.sharedPreviewsSession = "s2"
	i.mu.Unlock()
	close(c.release)

	// Give the in-flight fetch every chance to deposit its bytes.
	for range 50 {
		<-time.After(time.Millisecond)
		i.mu.Lock()
		n := len(i.sharedPreviews)
		i.mu.Unlock()
		if n > 0 {
			t.Fatal("a previous session's preview landed in the new binding's cache")
		}
	}
}

// The renderer must never be handed the live map: it reads what it is given
// outside the lock while the fetch goroutine keeps inserting, and a map read
// concurrent with a map write is a race whoever owns the bytes.
func TestSharedPreviewBytesHandsOutACopy(t *testing.T) {
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("PNGDATA")})
	i := previewInteractive(c, "s1")
	i.fetchSharedPreviews([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}})
	waitForPreview(t, i, "shr_a")

	snapshot := i.sharedPreviewBytes()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot = %v, want the one preview", snapshot)
	}
	// Mutating the live cache must not reach through into what a frame holds.
	i.mu.Lock()
	i.sharedPreviews["shr_b"] = []byte("OTHER")
	i.mu.Unlock()
	if len(snapshot) != 1 {
		t.Errorf("the frame's map grew to %d entries — it is the live map, not a copy", len(snapshot))
	}
}

// A carrier that does not serve the verb at all (a replay carrier: the
// controller is optional) must leave the card intact and simply not preview it.
func TestAPreviewIsSkippedWhenTheCarrierCannotServeIt(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier() // no SharedFilesController
	i.cfg.CarrierSession = "s1"

	// The bare fakeCarrier would panic on any verb it does not implement, so
	// reaching the wire here at all is itself the failure.
	i.fetchSharedPreviews([]core.SharedFile{{ID: "shr_a", Name: "chart.png", Kind: "image", Size: 7}})

	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.sharedPreviews) != 0 {
		t.Errorf("cached %v from a carrier that cannot serve previews", i.sharedPreviews)
	}
}

// The fetch set is read off the rendered transcript, so a share recorded on a
// tool-role message is found and a malformed record is skipped rather than
// failing the pass.
func TestTranscriptSharesReadsWhatTheCardsRender(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.appendCarrierToolResultLocked(toolResultWithShares("call_1", core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "chart.png", Kind: "image",
	}))
	msgs := i.carrierTranscript()

	got := transcriptShares(msgs)
	if len(got) != 1 || got[0].ID != "shr_a" {
		t.Fatalf("transcriptShares = %+v, want the recorded share", got)
	}

	// A record the renderer would drop is one the fetch drops too, so the two
	// cannot disagree about what exists.
	msgs[0].Meta[core.MetaShared] = `{"not":"an array"`
	if got := transcriptShares(msgs); len(got) != 0 {
		t.Errorf("transcriptShares = %+v on a malformed record, want nothing", got)
	}
}
