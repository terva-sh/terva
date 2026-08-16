package modes

// The wiring between the /shared panel and the verbs it triggers.
//
// The dialog decides WHICH action a key means and returns it as a SharedAction;
// the host owns every action. Both halves were tested — SharedDialog.HandleKey
// in the dialogs package, copySharedPath/openSharedFile/saveSharedFile here —
// and the switch that joins them was not. It is four `case` arms reading four
// fields off one struct, which is exactly the shape where a copy-paste puts the
// wrong verb behind a key: routing CopyID to openSharedFile would launch a
// system viewer when the user pressed `c`, and every test on either side would
// still pass.
//
// So this drives the REAL registry. i.buildOverlays() is the production
// wiring, i.handleKey is the production entry point, and the assertions watch
// the four places a verb reaches the outside world. Nothing here reimplements
// the switch it is checking.

import (
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// sharedVerbLog records the two actions that reach outside terva. The other
// two verbs are observed on the carrier: save is the only thing that fetches,
// refresh the only thing that lists.
type sharedVerbLog struct {
	mu        sync.Mutex
	clipboard []string
	viewer    []string
}

func (l *sharedVerbLog) counts() (clip, view int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clipboard), len(l.viewer)
}

// verbCounts is every observable the four verbs leave behind, sampled together
// so a press can be asked "what moved?" rather than "did X happen".
type verbCounts struct{ clip, view, fetch, list int }

func sampleVerbs(l *sharedVerbLog, c *shareCarrier) verbCounts {
	clip, view := l.counts()
	return verbCounts{clip: clip, view: view, fetch: len(c.askedFor()), list: c.listCount()}
}

// fired names the verbs whose observable moved between two samples. The whole
// point is the plural: a key that reaches TWO verbs is as wrong as one that
// reaches the wrong one, and only a diff of everything can say so.
func (a verbCounts) fired(b verbCounts) []string {
	var out []string
	if b.clip > a.clip {
		out = append(out, "copy")
	}
	if b.view > a.view {
		out = append(out, "open")
	}
	if b.fetch > a.fetch {
		out = append(out, "save")
	}
	if b.list > a.list {
		out = append(out, "refresh")
	}
	return out
}

// sharedOverlayFixture is a TUI with the /shared panel open over a listing, the
// real overlay registry built, and both outside-world seams recorded.
func sharedOverlayFixture(t *testing.T) (*Interactive, *shareCarrier, *sharedVerbLog) {
	t.Helper()
	cwd := testsupport.TempDir(t)
	files := []ctrlproto.SharedFileEntry{
		entry("shr_a", "report.pdf", "/daemon/side/shr_a-report.pdf"),
	}
	c := newShareCarrier(map[string][]byte{"shr_a": []byte("BODY")})
	// The same rows come back from a refresh, so pressing r cannot empty the
	// panel out from under the next assertion.
	c.list = files

	log := &sharedVerbLog{}
	t.Cleanup(swapClipboard(func(s string) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.clipboard = append(log.clipboard, s)
		return nil
	}))
	t.Cleanup(swapViewer(func(s string) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.viewer = append(log.viewer, s)
		return nil
	}))

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"
	i.cfg.CarrierLocal = true // copy and open are local-only verbs
	i.cfg.CWD = cwd
	i.sharedDialog = dialogs.NewSharedDialog()
	i.sharedFiles = files
	i.sharedFilesSession = "s1"
	i.sharedDialog.Open(i.sharedFileRows)

	// The production wiring, not a stand-in for it.
	i.keymap = i.buildGlobalKeymap()
	i.overlays = i.buildOverlays()
	return i, c, log
}

// waitForVerb polls until an observable moves. Two of the four arms dispatch on
// a goroutine (`go i.saveSharedFile`, `go i.refreshSharedFiles`) so the panel
// stays painted while the wire call runs — reading the counter straight after
// the press would race the work it means to observe and pass by arriving early.
func waitForVerb(t *testing.T, what string, fn func() bool) {
	t.Helper()
	for range 400 {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Each key reaches its own verb, and only its own.
func TestSharedOverlayRoutesEachKeyToItsOwnVerb(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tui.Key
		want string
		// async marks the arms the overlay dispatches on a goroutine.
		async bool
	}{
		{name: "c copies the path", key: tui.Key{Kind: tui.KeyRune, Rune: 'c'}, want: "copy"},
		{name: "o opens in the viewer", key: tui.Key{Kind: tui.KeyRune, Rune: 'o'}, want: "open"},
		{name: "enter opens too", key: tui.Key{Kind: tui.KeyEnter}, want: "open"},
		{name: "s saves a copy", key: tui.Key{Kind: tui.KeyRune, Rune: 's'}, want: "save", async: true},
		{name: "r refreshes the listing", key: tui.Key{Kind: tui.KeyRune, Rune: 'r'}, want: "refresh", async: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, c, log := sharedOverlayFixture(t)
			before := sampleVerbs(log, c)

			i.handleKey(t.Context(), tc.key)

			if tc.async {
				waitForVerb(t, tc.want, func() bool {
					return len(sampleVerbs(log, c).fired(before)) > 0 ||
						len(before.fired(sampleVerbs(log, c))) > 0
				})
			}
			got := before.fired(sampleVerbs(log, c))
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("the key reached %v, want exactly [%s]", got, tc.want)
			}
			if i.sharedDialog == nil || !i.sharedDialog.Active() {
				t.Error("the panel closed on an action key — only esc closes it")
			}
		})
	}
}

// Esc closes the panel and triggers no verb. It is the one key whose whole job
// is to do nothing else, so a switch that fell through to an action arm on the
// way out would be invisible to every test above.
func TestSharedOverlayEscClosesAndActsOnNothing(t *testing.T) {
	i, c, log := sharedOverlayFixture(t)
	before := sampleVerbs(log, c)

	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEsc})

	if i.sharedDialog.Active() {
		t.Error("esc left the panel open")
	}
	if got := before.fired(sampleVerbs(log, c)); len(got) != 0 {
		t.Errorf("esc reached %v, want no verb at all", got)
	}
}

// The path handed to each verb is the SELECTED row's, not the first row's.
//
// Every arm passes an id from the action, and the dialog resolves that id
// against the row under the cursor. An arm that reached for the listing's head
// instead would act on the wrong file while looking entirely correct on a
// one-row panel, which is what the fixture above is.
func TestSharedOverlayActsOnTheSelectedRow(t *testing.T) {
	cwd := testsupport.TempDir(t)
	files := []ctrlproto.SharedFileEntry{
		entry("shr_a", "first.pdf", "/daemon/side/shr_a-first.pdf"),
		entry("shr_b", "second.pdf", "/daemon/side/shr_b-second.pdf"),
	}
	c := newShareCarrier(nil)
	c.list = files

	log := &sharedVerbLog{}
	t.Cleanup(swapClipboard(func(s string) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.clipboard = append(log.clipboard, s)
		return nil
	}))
	t.Cleanup(swapViewer(func(string) error { return nil }))

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"
	i.cfg.CarrierLocal = true
	i.cfg.CWD = cwd
	i.sharedDialog = dialogs.NewSharedDialog()
	i.sharedFiles = files
	i.sharedFilesSession = "s1"
	i.sharedDialog.Open(i.sharedFileRows)
	i.keymap = i.buildGlobalKeymap()
	i.overlays = i.buildOverlays()

	// Move to the second row, then copy.
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyDown})
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyRune, Rune: 'c'})

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.clipboard) != 1 {
		t.Fatalf("the clipboard was written %d times, want one", len(log.clipboard))
	}
	if log.clipboard[0] != "/daemon/side/shr_b-second.pdf" {
		t.Errorf("copied %q, want the SELECTED row's path", log.clipboard[0])
	}
}
