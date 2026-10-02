package workspace

import (
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

func nativeWorktreeCrew(home, jev string) []byte {
	return []byte("---\nname: crew\nhome: " + home + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n" + jev + "---\n")
}

const jevInWorktree = "    workspace: worktree\n    posture: auto-edit\n"

// nativeWorktreeSetup starts the crew talkoot with jev native in a worktree,
// on fake leases that hand out real directories.
func nativeWorktreeSetup(t *testing.T) (*Workspace, *fakeLeases, string) {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	fl := &fakeLeases{root: testsupport.TempDir(t)}
	w.talkootLeases = fl
	if _, err := w.talkootCreate(t.Context(), "crew", nativeWorktreeCrew(cwd, jevInWorktree)); err != nil {
		t.Fatalf("a native worktree member was refused: %v", err)
	}
	return w, fl, cwd
}

// memberLiveSession waits for member's session to exist, and returns it live.
func memberLiveSession(t *testing.T, w *Workspace, member string) *wsSession {
	t.Helper()
	waitTalkoot(t, member+"'s session", func() bool { return memberView(t, w, "crew", member).Session != "" })
	s, err := w.resolve(memberView(t, w, "crew", member).Session)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// awaitTurnEnd waits for member's turn line. A session closed before its turn
// wrote anything is empty and deletes itself, so a restart test waits first.
func awaitTurnEnd(t *testing.T, member string) {
	t.Helper()
	waitTalkoot(t, member+"'s turn to end", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == member })) > 0
	})
}

func leaseDir(fl *fakeLeases, member string) string {
	return filepath.Join(fl.root, "crew", member)
}

// A native member in a worktree runs its session in its own lease, so its
// file tools and bash work in that checkout and not the shared one.
func TestANativeWorktreeMemberRunsInItsLease(t *testing.T) {
	w, fl, cwd := nativeWorktreeSetup(t)
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	s := memberLiveSession(t, w, "jev")
	if want := leaseDir(fl, "jev"); s.cwd != want {
		t.Fatalf("jev runs in %q, want its lease %q (home is %q)", s.cwd, want, cwd)
	}
	if acquired, _ := fl.snapshot(); !slices.Contains(acquired, "jev") || slices.Contains(acquired, "helm") {
		t.Errorf("acquired %v, want jev's lease only", acquired)
	}
}

// Two native members can each write in their own worktree, where the shared
// checkout allows one writer, and each works in its own lease.
func TestTwoNativeWritersEachTakeAWorktree(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	fl := &fakeLeases{root: testsupport.TempDir(t)}
	w.talkootLeases = fl
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: auto-edit\n  - id: jev\n    role: specialist\n    posture: auto-edit\n    workspace: worktree\n  - id: kai\n    role: specialist\n    posture: auto-edit\n    workspace: worktree\n---\n"
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(text)); err != nil {
		t.Fatalf("a roster with one shared writer and two worktree writers was refused: %v", err)
	}
	for _, member := range []string{"jev", "kai"} {
		if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{member}, "Start.", nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	jev, kai := memberLiveSession(t, w, "jev"), memberLiveSession(t, w, "kai")
	if jev.cwd != leaseDir(fl, "jev") || kai.cwd != leaseDir(fl, "kai") || jev.cwd == kai.cwd {
		t.Fatalf("jev runs in %q and kai in %q, want each in its own lease", jev.cwd, kai.cwd)
	}
}

// A native member whose lease fails takes no turn, and never falls back to the
// home checkout, where the one-writer rule assumed it would not write.
func TestAFailedLeaseFailsANativeDelivery(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	w.talkootLeases = &fakeLeases{err: errors.New("no worktree for you")}
	if _, err := w.talkootCreate(t.Context(), "crew", nativeWorktreeCrew(cwd, jevInWorktree)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the refused delivery", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool {
			return l.Type == talkoot.LineGuard && l.Guard == talkoot.GuardDelivery && l.Member == "jev"
		})) > 0
	})
	if sid := memberView(t, w, "crew", "jev").Session; sid != "" {
		if s := w.existing(sid); s != nil && s.cwd == cwd {
			t.Fatalf("jev fell back to the home checkout %q", cwd)
		}
	}
}

// A native member that moves to the home checkout gives its worktree back and
// gets a fresh session there.
func TestANativeMemberGivesBackTheWorktreeItLeaves(t *testing.T) {
	w, fl, cwd := nativeWorktreeSetup(t)
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	old := memberLiveSession(t, w, "jev").id
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", nativeWorktreeCrew(cwd, "    posture: plan\n")); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return slices.Contains(r, "jev") })
	if sid := memberView(t, w, "crew", "jev").Session; sid == old {
		t.Errorf("jev kept session %s, which still runs in its old worktree", sid)
	}
}

// 🚨 Review finding on PR #1556: a native member that left its worktree gave
// it back at once, while its old session's turn still worked there. The
// worktree now waits for that turn to end.
func TestANativeMembersWorktreeWaitsForItsTurn(t *testing.T) {
	cwd := talkootHome(t)
	ch, in := make(chan struct{}), make(chan struct{}, 1)
	w := openTalkootWorkspaceWith(t, cwd, heldProvider(ch, in, false))
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	fl := &fakeLeases{root: testsupport.TempDir(t)}
	w.talkootLeases = fl
	if _, err := w.talkootCreate(t.Context(), "crew", nativeWorktreeCrew(cwd, jevInWorktree)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	<-in
	if !memberLiveSession(t, w, "jev").busy() {
		t.Fatal("fixture: jev's turn is not running")
	}
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", nativeWorktreeCrew(cwd, "    posture: plan\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); slices.Contains(released, "jev") {
		t.Fatal("jev's worktree went back while its turn still ran there")
	}
	release()
	waitTalkoot(t, "the release after the turn", func() bool { _, r := fl.snapshot(); return slices.Contains(r, "jev") })
}

// heldBy reports whether m stays locked for about 100ms. A lock held by the
// caller's own goroutine stays locked; a brief hold by another does not.
func heldBy(m *sync.Mutex) bool {
	for range 20 {
		if m.TryLock() {
			m.Unlock()
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// 🚨 Review finding on PR #1556: the session build acquired the lease under
// w.mu, and the worktree engine runs git, so every other session call waited
// on git. The lease is now acquired before w.mu, on both the delivery and
// the resolve path.
func TestANativeMemberLeaseRunsOutsideTheWorkspaceLock(t *testing.T) {
	cwd := talkootHome(t)
	var cur atomic.Pointer[Workspace]
	var underLock, calls atomic.Int64
	fl := &fakeLeases{root: testsupport.TempDir(t), onAcquire: func() {
		calls.Add(1)
		if heldBy(&cur.Load().mu) {
			underLock.Add(1)
		}
	}}
	w := openTalkootWorkspace(t, cwd)
	cur.Store(w)
	w.talkootLeases = fl
	if _, err := w.talkootCreate(t.Context(), "crew", nativeWorktreeCrew(cwd, jevInWorktree)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	memberLiveSession(t, w, "jev")
	awaitTurnEnd(t, "jev")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	cur.Store(w2)
	w2.talkootLeases = fl
	w2.LoadTalkoots()
	if s := memberLiveSession(t, w2, "jev"); s.cwd != leaseDir(fl, "jev") {
		t.Fatalf("after a restart jev runs in %q, want its lease", s.cwd)
	}
	if calls.Load() < 2 {
		t.Fatalf("fixture: %d acquires, want the delivery's and the restart's", calls.Load())
	}
	if n := underLock.Load(); n != 0 {
		t.Fatalf("%d of %d lease acquires ran under w.mu", n, calls.Load())
	}
}

// A run that starts again keeps a native member's worktree, and the member's
// session runs in it again. The release at start is for members the roster
// no longer puts in a worktree.
//
// ⚠️ This proves a negative. The release at start is a goroutine that calls
// held and then release at once, so the bounded wait is ample for it to run;
// the rebuilt session's cwd is the positive half.
func TestARestartKeepsANativeMembersWorktree(t *testing.T) {
	w, fl, cwd := nativeWorktreeSetup(t)
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	memberLiveSession(t, w, "jev")
	awaitTurnEnd(t, "jev")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootLeases = fl
	w2.LoadTalkoots()
	if s := memberLiveSession(t, w2, "jev"); s.cwd != leaseDir(fl, "jev") {
		t.Fatalf("after a restart jev runs in %q, want its lease", s.cwd)
	}
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Fatalf("a restart gave back %v, a native member that is still in its worktree", released)
	}
}
