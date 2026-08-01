package modes

import (
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/provider"
)

func usageFixture() ctrlproto.UsageInfo {
	return ctrlproto.UsageInfo{
		Provider:    "openrouter",
		HasData:     true,
		Refreshable: true,
		CapturedAt:  "2026-07-09T12:00:00Z",
		Windows: []ctrlproto.UsageWindowInfo{
			{Label: "5h", UsedPercent: 40, Kind: "plan", ResetsAt: "2026-07-09T17:00:00Z"},
			{Label: "weekly", UsedPercent: 12, Kind: "credit"},
			{Label: "rpm", UsedPercent: 90, Kind: "rate_limit"},
		},
		Credits: &ctrlproto.CreditsInfo{HasCredits: true, Balance: 4.5, Used: 1.5},
	}
}

func newUsageTestInteractive() (*Interactive, *fakeCarrier) {
	fc := newFakeCarrier()
	fc.usage = usageFixture()
	i := newCtrlprotoTestInteractive()
	i.usageDialog = dialogs.NewUsageDialog()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"
	return i, fc
}

// The mirror is filled from the usage.snapshot verb, and the wire round-trip
// preserves the provider's whole picture: every window kind, the credits, the
// captured-at.
func TestCarrierUsageMirrorRoundTrip(t *testing.T) {
	i, _ := newUsageTestInteractive()

	if _, ok := i.currentUsage(); ok {
		t.Fatal("an unfetched mirror reported data")
	}

	i.fetchCarrierUsage(false)

	snap, ok := i.currentUsage()
	if !ok {
		t.Fatal("mirror reported no data after a fetch")
	}
	if snap.Provider != "openrouter" {
		t.Fatalf("provider = %q", snap.Provider)
	}
	if len(snap.Windows) != 3 {
		t.Fatalf("windows = %d, want 3 (rate-limit included on the wire)", len(snap.Windows))
	}
	if snap.Windows[2].Kind != provider.WindowRateLimit {
		t.Fatalf("third window kind = %v, want rate_limit", snap.Windows[2].Kind)
	}
	if snap.Credits == nil || snap.Credits.Balance != 4.5 || snap.Credits.Used != 1.5 {
		t.Fatalf("credits = %+v", snap.Credits)
	}
	want, _ := time.Parse(time.RFC3339, "2026-07-09T12:00:00Z")
	if !snap.CapturedAt.Equal(want) {
		t.Fatalf("capturedAt = %v, want %v", snap.CapturedAt, want)
	}
	if snap.Windows[0].ResetsAt.IsZero() {
		t.Fatal("plan window lost its ResetsAt")
	}
	if !snap.Windows[1].ResetsAt.IsZero() {
		t.Fatal("a window without a reset time invented one")
	}
}

// The status bar drops rate-limit windows — they would churn an always-visible
// meter — while the modal keeps them. That filter is the client's, which is why
// the wire carries every kind.
func TestStatusUsageWindowsDropsRateLimit(t *testing.T) {
	i, _ := newUsageTestInteractive()
	i.fetchCarrierUsage(false)

	ws := i.statusUsageWindows()
	if len(ws) != 2 {
		t.Fatalf("status windows = %d, want 2 (plan + credit)", len(ws))
	}
	for _, w := range ws {
		if w.Kind == provider.WindowRateLimit {
			t.Fatal("a rate-limit window reached the status bar")
		}
	}
	// The modal still sees all three.
	snap, _ := i.currentUsage()
	if len(snap.Windows) != 3 {
		t.Fatalf("modal windows = %d, want 3", len(snap.Windows))
	}
}

// A failed usage read is decorative: it must keep the last good picture, not
// clear it and not raise a status banner on every turn.
func TestCarrierUsageFetchErrorKeepsMirror(t *testing.T) {
	i, fc := newUsageTestInteractive()
	i.fetchCarrierUsage(false)

	fc.mu.Lock()
	fc.usageErr = errors.New("daemon went away")
	fc.mu.Unlock()
	i.fetchCarrierUsage(false)

	if _, ok := i.currentUsage(); !ok {
		t.Fatal("a failed refresh wiped the mirror")
	}
	i.mu.Lock()
	gotErr := i.statusErr
	i.mu.Unlock()
	if gotErr != "" {
		t.Fatalf("a failed usage read raised a status banner: %q", gotErr)
	}
}

// waitUsageRefresh blocks until the daemon is asked for a REFRESHED snapshot,
// draining the passive (refresh=false) reads a fixture makes along the way.
//
// It waits on the effect rather than polling a wall-clock budget. The previous
// shape — sleep 5ms, re-read a slice, give up at 2s — was a race between a
// background goroutine getting scheduled and a deadline being spent mostly
// asleep, and it lost on a loaded CI runner under -race while passing ~90 local
// runs under deliberate contention. The timeout below is a backstop that fires
// only on a genuine hang, not a budget the work has to beat: a passing run
// blocks for microseconds and never reaches it.
func waitUsageRefresh(t *testing.T, fc *fakeCarrier) {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case refresh := <-fc.usageRefreshes:
			if refresh {
				return
			}
		case <-timeout:
			fc.mu.Lock()
			calls := append([]bool(nil), fc.usageCalls...)
			fc.mu.Unlock()
			t.Fatalf("/usage never asked the daemon to refresh; UsageSnapshot calls: %v", calls)
		}
	}
}

// Opening /usage asks the daemon to refresh; the daemon falls back to the
// cached snapshot for providers with no usage endpoint, so refresh=true is
// always safe to send.
func TestOpenUsageDialogRequestsRefresh(t *testing.T) {
	i, fc := newUsageTestInteractive()
	i.fetchCarrierUsage(false) // seed Refreshable

	i.openUsageDialog()

	waitUsageRefresh(t, fc)
}

// The dialog must be OPEN before the refresh is kicked off, because the
// refresh's completion hop is gated on Active(). Opened second, a fast answer
// — which is what the daemon gives for providers it serves from cache — lands
// while Active() is still false, and the fresh snapshot is silently dropped:
// the modal renders the pre-refresh picture, and can sit on "fetching…" with
// the data already in the mirror.
//
// Asserting on ordering rather than on a rendered string keeps this pinned to
// the cause. The race the same ordering produced is covered by -race on the
// test above; this one covers what the user would have seen.
func TestUsageDialogIsOpenBeforeTheRefreshIsRequested(t *testing.T) {
	i, fc := newUsageTestInteractive()
	i.fetchCarrierUsage(false)
	drainUsageRefreshes(fc)

	activeAtRequest := make(chan bool, 4)
	fc.onUsageSnapshot = func(refresh bool) {
		if refresh {
			activeAtRequest <- i.usageDialog.Active()
		}
	}

	i.openUsageDialog()

	select {
	case active := <-activeAtRequest:
		if !active {
			t.Fatal("the refresh was requested before the dialog opened; its result " +
				"lands on an inactive dialog and is discarded")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("/usage never asked the daemon to refresh")
	}
}

// drainUsageRefreshes empties the signal channel so a test observes only the
// calls it provoked, not the fixture's seeding read.
func drainUsageRefreshes(fc *fakeCarrier) {
	for {
		select {
		case <-fc.usageRefreshes:
		default:
			return
		}
	}
}
