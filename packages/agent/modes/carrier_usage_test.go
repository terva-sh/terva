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

// Opening /usage asks the daemon to refresh; the daemon falls back to the
// cached snapshot for providers with no usage endpoint, so refresh=true is
// always safe to send.
func TestOpenUsageDialogRequestsRefresh(t *testing.T) {
	i, fc := newUsageTestInteractive()
	i.fetchCarrierUsage(false) // seed Refreshable

	i.openUsageDialog()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		calls := append([]bool(nil), fc.usageCalls...)
		fc.mu.Unlock()
		for _, refresh := range calls {
			if refresh {
				return // saw the refresh=true call
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("/usage never asked the daemon to refresh")
}
