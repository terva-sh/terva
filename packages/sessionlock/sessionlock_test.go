package sessionlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/testsupport"
)

// testManager is a Manager on a frozen clock with a scriptable pid probe, so
// staleness is driven rather than waited for.
func testManager(t *testing.T, now *time.Time, alive map[int]bool) *Manager {
	t.Helper()
	m := New()
	m.now = func() time.Time { return *now }
	m.pidAlive = func(pid int) bool { return alive[pid] }
	m.host = "testhost"
	m.selfPID = 4242
	return m
}

func transcript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(testsupport.TempDir(t), "20260101-120000-abcd1234.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArtifactNamesNeverLookLikeATranscript(t *testing.T) {
	// The six bucket scanners in packages/core all gate on a .jsonl suffix, so
	// this naming is what keeps a lock file from being listed as a session.
	for _, p := range ArtifactPaths("/x/sessions/ab/20260101-120000-abcd1234.jsonl") {
		if strings.HasSuffix(p, ".jsonl") {
			t.Errorf("%s ends in .jsonl, so every session scanner would list it", p)
		}
	}
	if ClaimPath("a/s.jsonl") != "a/s.lock.json" {
		t.Errorf("claim path: %s", ClaimPath("a/s.jsonl"))
	}
	// One file, and only while held. The flock lives on the transcript, so an
	// ordinary session leaves nothing behind.
	if got := ArtifactPaths("a/s.jsonl"); len(got) != 1 {
		t.Errorf("artifacts = %v, want just the claim record", got)
	}
}

func TestAcquireWritesAReasonAnExpiryAndAHeartbeat(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Reason: "it serves the control panel", Holder: "terva web"})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer h.Release()

	c, ok := m.ReadClaim(path)
	if !ok {
		t.Fatal("no claim record was written")
	}
	if c.Reason != "it serves the control panel" || c.Holder != "terva web" {
		t.Errorf("claim did not carry why or who: %+v", c)
	}
	if c.Kind != KindAuto || c.PID != m.selfPID {
		t.Errorf("an automatic lock must record its pid: %+v", c)
	}
	if c.ExpiresAt == "" || c.Heartbeat == "" {
		t.Errorf("claim needs an expiry and a heartbeat: %+v", c)
	}
	if c.BeatSeconds != int(BeatInterval/time.Second) {
		t.Errorf("claim must say what interval it beats on, got %d", c.BeatSeconds)
	}
	// The record is the neighbour, never the transcript itself.
	if b, _ := os.ReadFile(path); string(b) != "{}\n" {
		t.Error("the transcript was modified by taking a lock")
	}
}

func TestASecondAcquireIsRefusedAndNamesTheHolder(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	first, err := m.Acquire(path, Request{Reason: "a turn is in flight", Holder: "terva web"})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer first.Release()

	_, err = m.Acquire(path, Request{Reason: "second", Holder: "terva --print"})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("a second acquire must be refused, got %v", err)
	}
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("refusal must be a *BusyError, got %T", err)
	}
	if busy.Cause != CauseLiveHandle {
		t.Errorf("cause = %q, want a live write handle", busy.Cause)
	}
	if !strings.Contains(err.Error(), "a turn is in flight") {
		t.Errorf("the refusal must say why the session is held: %s", err)
	}
	if !strings.Contains(err.Error(), "terva web") {
		t.Errorf("the refusal must name the holder: %s", err)
	}
	if !strings.Contains(err.Error(), "lapses at") {
		t.Errorf("the refusal must give the expiry: %s", err)
	}
}

func TestReleaseFreesTheSessionAndRemovesTheRecord(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Reason: "first", Holder: "terva"})
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
	h.Release() // idempotent: a caller may defer it and also call it

	if _, ok := m.ReadClaim(path); ok {
		t.Error("the record outlived its holder")
	}
	second, err := m.Acquire(path, Request{Reason: "second", Holder: "terva"})
	if err != nil {
		t.Fatalf("a released session must be takeable: %v", err)
	}
	second.Release()
}

func TestAnExplicitClaimOutlivesItsProcessAndLapsesOnTime(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{
		Kind:   KindExplicit,
		Reason: "the migration lands tomorrow",
		Holder: "human:sothr",
		TTL:    48 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The whole point: the claimer exits, and the claim stays.
	h.Release()

	c, ok := m.ReadClaim(path)
	if !ok {
		t.Fatal("an explicit claim must survive its holder's exit")
	}
	if c.PID != 0 {
		t.Errorf("an explicit claim has no process, so pid must be 0, got %d", c.PID)
	}

	_, err = m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Cause != CauseExplicitClaim {
		t.Fatalf("an unexpired explicit claim must refuse, got %v", err)
	}

	// Past the expiry it stops binding, which is the only way a claim with no
	// process behind it can ever end.
	now = now.Add(49 * time.Hour)
	got, err := m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	if err != nil {
		t.Fatalf("a lapsed claim must not hold the session: %v", err)
	}
	got.Release()
}

func TestAnExplicitClaimIsCappedSoItCannotOutliveRecovery(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Kind: KindExplicit, Reason: "forever", TTL: 365 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	h.Release()

	c, _ := m.ReadClaim(path)
	exp, _ := time.Parse(time.RFC3339, c.ExpiresAt)
	if exp.After(now.Add(MaxExplicitTTL)) {
		t.Errorf("a claim was allowed past the cap: expires %s", c.ExpiresAt)
	}
}

func TestUnlockRefusesWhileALiveProcessHoldsTheSession(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Reason: "a turn is in flight", Holder: "terva web"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()

	// Removing the record here would tell the next opener the session is free
	// while an append is still in flight.
	if err := m.Unlock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("unlock must refuse a live holder, got %v", err)
	}
}

func TestUnlockClearsAnExplicitClaim(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, _ := m.Acquire(path, Request{Kind: KindExplicit, Reason: "held", Holder: "human:sothr"})
	h.Release()

	if err := m.Unlock(path); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, ok := m.ReadClaim(path); ok {
		t.Error("the claim survived an unlock")
	}
	for _, p := range ArtifactPaths(path) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived an unlock", filepath.Base(p))
		}
	}
}

// TestACrashedHolderIsRecovered covers the case the flock cannot: a record left
// behind on a filesystem where the guard was freed, with no live process.
func TestACrashedHolderIsRecovered(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	// A record from a terva that died: its pid is gone and its beat is old.
	stale := Claim{
		Format: ClaimFormat, Kind: KindAuto, Reason: "a turn was in flight",
		Holder: "terva web", Host: "testhost", PID: 999,
		ClaimedAt: now.Add(-time.Hour).UTC().Format(time.RFC3339),
		ExpiresAt: now.Add(11 * time.Hour).UTC().Format(time.RFC3339),
		Heartbeat: now.Add(-time.Hour).UTC().Format(time.RFC3339),
	}
	if err := writeClaim(ClaimPath(path), stale); err != nil {
		t.Fatal(err)
	}

	st, ok := m.Describe(path)
	if !ok || !st.Stale {
		t.Fatalf("a crashed holder must read as stale: %+v", st)
	}
	if !strings.Contains(st.StaleReason, "process is gone") || !strings.Contains(st.StaleReason, "heartbeat") {
		t.Errorf("the reason must name which witness fired: %q", st.StaleReason)
	}
	// Describe reports and never reclaims, so the record is still there.
	if _, ok := m.ReadClaim(path); !ok {
		t.Error("Describe removed a stale record instead of reporting it")
	}

	h, err := m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	if err != nil {
		t.Fatalf("a crashed holder must not block recovery: %v", err)
	}
	defer h.Release()
}

// TestABrokenFilesystemStillRefusesALiveHolder drives the one case the flock
// cannot cover: a mount that reports every lock as free.
func TestABrokenFilesystemStillRefusesALiveHolder(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	alive := map[int]bool{999: true}
	m := testManager(t, &now, alive)
	// Simulate NFS without lockd: every lock looks free.
	m.tryLock = func(path string) (*filelock.Lock, bool, error) { return nil, true, nil }
	path := transcript(t)

	live := Claim{
		Format: ClaimFormat, Kind: KindAuto, Reason: "a turn is in flight",
		Holder: "terva web", Host: "testhost", PID: 999,
		ClaimedAt: now.Add(-time.Minute).UTC().Format(time.RFC3339),
		ExpiresAt: now.Add(11 * time.Hour).UTC().Format(time.RFC3339),
		Heartbeat: now.Add(-time.Second).UTC().Format(time.RFC3339),
	}
	if err := writeClaim(ClaimPath(path), live); err != nil {
		t.Fatal(err)
	}

	_, err := m.Acquire(path, Request{Reason: "second", Holder: "terva"})
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Cause != CauseLiveHandleUnflocked {
		t.Fatalf("a live holder must still refuse where flock does nothing, got %v", err)
	}

	// 🔑 And the fallback must not outlive its evidence. Both witnesses have to
	// fail before recovery, or a crashed holder on the same mount would wedge
	// the session forever.
	alive[999] = false
	now = now.Add(time.Hour)
	h, err := m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	if err != nil {
		t.Fatalf("a dead holder must not wedge the session: %v", err)
	}
	h.Release()
}

func TestAPidAloneDoesNotRefuse(t *testing.T) {
	// A pid outlives the process that owned it, and the number gets reused. If
	// a live pid alone were enough, a recycled number would lock a session that
	// nothing is writing. config/listen.go made this argument first.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{999: true})
	m.tryLock = func(path string) (*filelock.Lock, bool, error) { return nil, true, nil }
	path := transcript(t)

	recycled := Claim{
		Format: ClaimFormat, Kind: KindAuto, Reason: "old", Holder: "terva",
		Host: "testhost", PID: 999,
		ClaimedAt: now.Add(-9 * time.Hour).UTC().Format(time.RFC3339),
		ExpiresAt: now.Add(3 * time.Hour).UTC().Format(time.RFC3339),
		Heartbeat: now.Add(-9 * time.Hour).UTC().Format(time.RFC3339), // long stopped
	}
	if err := writeClaim(ClaimPath(path), recycled); err != nil {
		t.Fatal(err)
	}
	h, err := m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	if err != nil {
		t.Fatalf("a live pid with a dead heartbeat must not refuse: %v", err)
	}
	h.Release()
}

func TestAPidFromAnotherHostIsNotProbed(t *testing.T) {
	// A pid number means nothing off the machine that produced it, so probing
	// one against this host's process table invents an answer.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{999: true})
	m.tryLock = func(path string) (*filelock.Lock, bool, error) { return nil, true, nil }
	path := transcript(t)

	foreign := Claim{
		Format: ClaimFormat, Kind: KindAuto, Reason: "elsewhere", Holder: "terva",
		Host: "some-other-box", PID: 999,
		ClaimedAt: now.Add(-time.Minute).UTC().Format(time.RFC3339),
		ExpiresAt: now.Add(11 * time.Hour).UTC().Format(time.RFC3339),
		Heartbeat: now.Add(-time.Second).UTC().Format(time.RFC3339),
	}
	if err := writeClaim(ClaimPath(path), foreign); err != nil {
		t.Fatal(err)
	}
	h, err := m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	if err != nil {
		t.Fatalf("a pid from another host must not be probed here: %v", err)
	}
	h.Release()
}

func TestAHeartbeatFromTheFutureIsFreshNotCrashed(t *testing.T) {
	// A clock that disagrees is not evidence that a process died.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := Claim{Heartbeat: now.Add(time.Hour).UTC().Format(time.RFC3339)}
	if !c.beatFreshAt(now, BeatInterval) {
		t.Error("a heartbeat from the future must read as fresh")
	}
}

func TestAnUnreadableExpiryDoesNotHandTheSessionAway(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := Claim{ExpiresAt: "not a timestamp"}
	if c.expiredAt(now) {
		t.Error("an unparseable expiry must not read as expired")
	}
	if (Claim{}).expiredAt(now) {
		t.Error("an absent expiry must not read as expired")
	}
}

func TestAnUnreadableRecordBehindAHeldGuardStillRefuses(t *testing.T) {
	// Fail closed where the strong witness says somebody is there, and fail
	// open where it says they are gone.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Reason: "held", Holder: "terva"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if err := os.WriteFile(ClaimPath(path), []byte("{{{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(path, Request{Reason: "second"}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a held guard must refuse whatever the record says, got %v", err)
	}
}

func TestAnUnreadableRecordBehindAFreeGuardIsTaken(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	if err := os.WriteFile(ClaimPath(path), []byte("{{{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := m.Acquire(path, Request{Reason: "resume", Holder: "terva"})
	if err != nil {
		t.Fatalf("a free guard means nobody is here, whatever the record says: %v", err)
	}
	h.Release()
}

func TestAFutureFormatIsHonouredRatherThanIgnored(t *testing.T) {
	// A record this binary is too old to have written still binds while its
	// expiry parses. Discarding it would let an older terva walk through a
	// newer one's claim.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	future := Claim{
		Format: 99, Kind: KindExplicit, Reason: "from a newer terva",
		Holder:    "human:sothr",
		ClaimedAt: now.UTC().Format(time.RFC3339),
		ExpiresAt: now.Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if err := writeClaim(ClaimPath(path), future); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(path, Request{Reason: "resume"}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a future-format claim must still bind, got %v", err)
	}
}

func TestTouchRestampsOnlyOnceAnIntervalHasPassed(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Reason: "writing", Holder: "terva"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	first := h.Claim().Heartbeat

	now = now.Add(BeatInterval / 2)
	h.Touch()
	if h.Claim().Heartbeat != first {
		t.Error("Touch rewrote the record before an interval had passed")
	}

	now = now.Add(2 * BeatInterval)
	h.Touch()
	if h.Claim().Heartbeat == first {
		t.Error("Touch did not restamp after an interval")
	}
	c, _ := m.ReadClaim(path)
	if c.Heartbeat != h.Claim().Heartbeat {
		t.Error("the restamped heartbeat did not reach the file")
	}
}

func TestReleaseLeavesAClaimThatReplacedOurs(t *testing.T) {
	// A `terva session lock` may land while a session is open. That claim is
	// meant to outlive us, so our Release must not take it with us.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, err := m.Acquire(path, Request{Reason: "writing", Holder: "terva"})
	if err != nil {
		t.Fatal(err)
	}
	theirs := Claim{
		Format: ClaimFormat, Kind: KindExplicit, Reason: "hold for review",
		Holder: "human:sothr", ClaimedAt: now.UTC().Format(time.RFC3339),
		ExpiresAt: now.Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if err := writeClaim(ClaimPath(path), theirs); err != nil {
		t.Fatal(err)
	}
	h.Release()

	c, ok := m.ReadClaim(path)
	if !ok || c.Kind != KindExplicit {
		t.Fatalf("Release removed a claim that was not ours: %+v", c)
	}
}

func TestIsHeldTreatsAnUnknownAnswerAsHeld(t *testing.T) {
	// IsHeld gates destructive sweeps, so "I could not tell" must mean "leave
	// it alone".
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	m.tryLock = func(path string) (*filelock.Lock, bool, error) {
		return nil, false, errors.New("permission denied")
	}
	if !m.IsHeld(transcript(t)) {
		t.Error("a failed probe must read as held")
	}
}

func TestRemoveArtifactsTakesBothFiles(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := testManager(t, &now, map[int]bool{})
	path := transcript(t)

	h, _ := m.Acquire(path, Request{Reason: "writing", Holder: "terva"})
	h.Release()
	RemoveArtifacts(path)
	for _, p := range ArtifactPaths(path) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived RemoveArtifacts", filepath.Base(p))
		}
	}
}
