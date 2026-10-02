package workspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// talkootHome turns talkoot on in a throwaway TERVA_HOME and returns a
// directory for a workspace to run in.
func talkootHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"talkoot_enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return testsupport.TempDir(t)
}

// openTalkootWorkspace builds a workspace in cwd. The provider is a local
// stub that refuses every request with a 400, so a member's turn fails at
// once and no request leaves the machine: a failed turn still has to report
// and free its member. A closed port would not do, because the agent loop
// retries a refused connection with a backoff longer than a test waits.
func openTalkootWorkspace(t *testing.T, cwd string) *Workspace {
	t.Helper()
	return openTalkootWorkspaceWith(t, cwd, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"the test provider refuses every request"}}`))
	})
}

func openTalkootWorkspaceWith(t *testing.T, cwd string, provider http.HandlerFunc) *Workspace {
	t.Helper()
	srv := httptest.NewServer(provider)
	// Registered first, so it closes after the workspace.
	t.Cleanup(srv.Close)
	w, err := NewWorkspace(build.Args{
		Provider: "openai-compatible", BaseURL: srv.URL, APIKey: "k", Model: "fake-model",
		CWD: cwd, NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = w.Close() }) })
	return w
}

func crewText(home string) []byte {
	return []byte("---\nname: crew\nhome: " + home + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n---\n")
}

func waitTalkoot(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// resumeFailed waits for member's failed turn to pause it, and resumes the
// member as a person does after a look. The test provider refuses every
// request, so each native turn fails, and a failed turn holds its member.
func resumeFailed(t *testing.T, w *Workspace, id, member string) {
	t.Helper()
	waitTalkoot(t, member+"'s failed turn to pause it", func() bool {
		return slices.Contains(memberView(t, w, id, member).Status.Pauses, "failed")
	})
	if err := w.talkootResume(context.Background(), id, "sothr", member, ""); err != nil {
		t.Fatal(err)
	}
}

func memberView(t *testing.T, w *Workspace, id, member string) talkootMemberView {
	t.Helper()
	v, err := w.talkootGet(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range v.Members {
		if m.Member.ID == member {
			return m
		}
	}
	t.Fatalf("no member %s", member)
	return talkootMemberView{}
}

func roomLines(t *testing.T, id string, keep func(talkoot.Line) bool) []talkoot.Line {
	t.Helper()
	lines, err := talkoot.OpenRoom(filepath.Join(talkoot.Dir(), id)).Read()
	if err != nil {
		t.Fatal(err)
	}
	var out []talkoot.Line
	for _, l := range lines {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out
}

func TestCreateMakesTheRoomAndRefusesAnExistingID(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), "crew", talkoot.KeyFile)); err != nil {
		t.Fatalf("create must make the room key: %v", err)
	}
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want a second create refused, got %v", err)
	}
	list, err := w.talkootList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Running || list[0].Problem != "" {
		t.Errorf("want crew running, got %+v", list)
	}
	// The room starts unpaused: CreateRoom made its key, so no guard line.
	if why := memberView(t, w, "crew", "helm").Status.Paused; why != "" {
		t.Errorf("a new talkoot must start unpaused, got %q", why)
	}
}

func TestCreateRefusesAForeignHome(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(testsupport.TempDir(t))); err == nil || !strings.Contains(err.Error(), "not this workspace's directory") {
		t.Errorf("want a foreign home refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), "crew")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused create must leave nothing behind: %v", err)
	}
}

// 🚨 The id becomes a path under the talkoot directory, so an id that is
// empty or climbs out of it is refused before anything is made.
func TestCreateRefusesAnIDThatIsNotAName(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	for _, id := range []string{"", "../escape", "a/b", "Crew"} {
		if _, err := w.talkootCreate(ctx, id, crewText(cwd)); err == nil {
			t.Errorf("want id %q refused", id)
		}
		if _, err := w.talkootGet(ctx, id); !errors.Is(err, ErrTalkootNotFound) {
			t.Errorf("want id %q not found, got %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(talkoot.Dir()), "escape")); err == nil {
		t.Error("an id climbed out of the talkoot directory")
	}
}

// 🚨 A person's post makes the coordinator's session on its first delivery,
// in the roster's posture, with the team tools, and never as the person's
// own default session. The turn fails here, and it must still report, or the
// member would stay working and hold a slot for good.
func TestAPostMakesTheCoordinatorsSession(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan the lake schema.", nil, ""); err != nil {
		t.Fatal(err)
	}
	helm := memberView(t, w, "crew", "helm")
	if helm.Session == "" {
		t.Fatal("the coordinator has no session after its first delivery")
	}
	s := w.existing(helm.Session)
	if s == nil {
		t.Fatal("the member session is not live")
	}
	if got := s.argsSnapshot().Approval; got != "plan" {
		t.Errorf("want the roster's plan posture, got %q", got)
	}
	if _, ok := s.agent.LookupTool("talkoot_send"); !ok {
		t.Error("the member session has no talkoot_send")
	}
	if p := w.latestPersonSession(); p != "" && build.SessionIDFromPath(p) == helm.Session {
		t.Error("a member's session became the person's default")
	}
	if seats := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineSeat }); len(seats) != 1 || seats[0].Ref != helm.Session {
		t.Errorf("want one seat line for the session, got %+v", seats)
	}
	waitTalkoot(t, "the failed turn to free helm", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	if turns := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" }); len(turns) != 1 {
		t.Errorf("want the turn recorded once, got %+v", turns)
	}
	// The test provider refuses the request, so the turn failed, and the
	// member waits for a person.
	if st := memberView(t, w, "crew", "helm").Status; !slices.Equal(st.Pauses, []string{"failed"}) || !strings.Contains(st.Paused, "refuses every request") {
		t.Errorf("paused %q with kinds %v, want failed with the provider's error", st.Paused, st.Pauses)
	}
}

// The roster owns a member's posture, so the settings surface cannot change it.
func TestASeatedSessionRefusesAPostureChange(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	s := w.existing(memberView(t, w, "crew", "helm").Session)
	if err := s.settingsAction("set", map[string]string{"key": "approval", "value": "yolo"}); err == nil || !strings.Contains(err.Error(), "roster sets") {
		t.Errorf("want the posture change refused, got %v", err)
	}
	// The refusal changed nothing: the args, the gate, and the tool view.
	if got := s.argsSnapshot().Approval; got != "plan" {
		t.Errorf("want the session still in plan, got %q", got)
	}
	if s.gate == nil || s.gate.Mode() != permission.ApprovalPlan {
		t.Error("the refused change moved the gate off plan")
	}
	if _, ok := s.agent.LookupTool("write"); ok {
		t.Error("the refused change gave the member the write tool")
	}
}

// 🚨 Only one process runs a talkoot. A second router over the room would
// seal to its own last line and break the other's chain.
func TestASecondWorkspaceSkipsALockedTalkoot(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	list, err := w2.talkootList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Running || !strings.Contains(list[0].Problem, "another terva process") {
		t.Errorf("want crew skipped with a reason, got %+v", list)
	}
	if _, err := w2.talkootPost(ctx, "crew", "sothr", nil, "x", nil, ""); !errors.Is(err, ErrTalkootNotHere) {
		t.Errorf("want a post refused where the talkoot does not run, got %v", err)
	}
}

// A talkoot runs only in the workspace of its home directory. Elsewhere it is
// listed, not run, and without a problem: that is where it belongs.
func TestStartLoadsOnlyTheTalkootsHomedHere(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	other := openTalkootWorkspace(t, testsupport.TempDir(t))
	other.LoadTalkoots()
	list, err := other.talkootList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Running || list[0].Problem != "" {
		t.Errorf("want crew listed, not running, with no problem, got %+v", list)
	}
	if _, err := other.talkootGet(ctx, "crew"); !errors.Is(err, ErrTalkootNotHere) {
		t.Errorf("want ErrTalkootNotHere, got %v", err)
	}
	_ = other.Close()

	home := openTalkootWorkspace(t, cwd)
	home.LoadTalkoots()
	if _, err := home.talkootGet(ctx, "crew"); err != nil {
		t.Errorf("the home workspace did not run crew: %v", err)
	}
}

// A deleted member session loses its seat, and the member's next delivery
// makes it a new one.
func TestAMemberSessionRecreatesWhenItsFileIsGone(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	resumeFailed(t, w, "crew", "helm")
	first := memberView(t, w, "crew", "helm").Session
	if err := w.DeleteSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.talkootSeatOf(first); ok {
		t.Error("a deleted session kept its seat")
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's second turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	second := memberView(t, w, "crew", "helm").Session
	if second == "" || second == first {
		t.Fatalf("want a new session for helm, got %q after %q", second, first)
	}
	if _, ok := w.talkootSeatOf(second); !ok {
		t.Error("the new session has no seat")
	}
	if turns := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" }); len(turns) != 2 {
		t.Errorf("want both turns recorded, got %d", len(turns))
	}
}

// 🚨 A delete or an update can revoke a seat while its turn runs. The turn
// still spent as the member, so its end must reach the caps and free the slot.
func TestARevokedSeatStillReportsItsTurn(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	run, _ := w.talkootRunOf("crew")
	w.seatTalkoot("s1", run, "jev")
	seat, _ := w.talkootSeatOf("s1")
	w.unseatTalkoot("s1")
	if err := seat.TurnEnded(0.5); err != nil {
		t.Fatalf("a revoked seat could not report its turn: %v", err)
	}
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got != 0.5 {
		t.Errorf("want the turn's spend counted, got %v", got)
	}
}

// 🚨 Close stops the routers before it closes the sessions. A delivery in
// that window would land in a closing session and be lost, yet the room would
// record it as delivered. Held instead, it is owed to the next start.
func TestCloseOwesUndeliveredEnvelopesToTheNextStart(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	w.stopTalkoots()
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := memberView(t, w, "crew", "helm").Session; got != "" {
		t.Errorf("a stopped talkoot delivered, and made session %q", got)
	}
	_ = w.Close()

	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	waitTalkoot(t, "the owed delivery to reach helm", func() bool {
		v := memberView(t, w2, "crew", "helm")
		return v.Session != "" && !v.Status.Working
	})
	if got := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineDelivery }); len(got) != 1 {
		t.Errorf("want one delivery, after the restart, got %d", len(got))
	}
}

// 🚨 An update the caller is told failed must not apply at the next start,
// so the roster file changes only once nothing else can fail. An unreadable
// room fails the new router.
func TestAFailedUpdateLeavesTheRosterAsItWas(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a file mode stops a read only for an ordinary user on unix")
	}
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(talkoot.Dir(), "crew")
	before, err := os.ReadFile(filepath.Join(dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	// The room file appears with its first line.
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	room := filepath.Join(dir, talkoot.RoomFile)
	if err := os.Chmod(room, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(room, 0o600) })
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); err == nil {
		t.Fatal("want the update refused when its router cannot read the room")
	}
	_ = os.Chmod(room, 0o600)
	after, err := os.ReadFile(filepath.Join(dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a failed update changed the roster file")
	}
	v, err := w.talkootGet(ctx, "crew")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Members) != 2 {
		t.Errorf("a failed update changed the running roster: %+v", v.Members)
	}
}

// 🚨 An update retires a seat under the lock that swaps the router, before
// the revoke can run. A tool that got past the revoke check must still be
// refused inside the router call, or it would send through the new router
// as a member that has left.
func TestARetiredSeatRefusesACallBeforeItsRevoke(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	run, _ := w.talkootRunOf("crew")
	w.seatTalkoot("s1", run, "jev")
	seat, _ := w.talkootSeatOf("s1")
	seat.b.retired.Store(true)
	if _, err := seat.Send(talkoot.Outgoing{To: []string{"helm"}, Kind: talkoot.KindMessage, Body: "Still here."}); !errors.Is(err, errSeatRevoked) {
		t.Errorf("a retired seat sent: %v", err)
	}
	if _, err := seat.Roster(); !errors.Is(err, errSeatRevoked) {
		t.Errorf("a retired seat read the roster: %v", err)
	}
}

// 🚨 An update the room cannot record stops before the new roster reaches
// disk. A lost seat line would let a restart put the new roster over the old
// seats, and a lost roster line would hide who made the change.
func TestAnUpdateStopsWhenTheRoomCannotRecordIt(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a file mode stops a write only for an ordinary user on unix")
	}
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Look.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "jev's turn to end", func() bool { return !memberView(t, w, "crew", "jev").Status.Working })
	jev := memberView(t, w, "crew", "jev").Session
	dir := filepath.Join(talkoot.Dir(), "crew")
	before, err := os.ReadFile(filepath.Join(dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	room := filepath.Join(dir, talkoot.RoomFile)
	if err := os.Chmod(room, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(room, 0o600) })
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); err == nil {
		t.Fatal("want the update refused when the room cannot record jev leaving")
	}
	after, err := os.ReadFile(filepath.Join(dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("the refused update changed the roster file")
	}
	if _, ok := w.talkootSeatOf(jev); !ok {
		t.Error("the refused update took jev's seat")
	}
}

// With no member leaving, the roster line alone stops an update the room
// cannot record.
func TestAnUpdateStopsWhenTheRoomCannotRecordItsRosterLine(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a file mode stops a write only for an ordinary user on unix")
	}
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	// The room file appears with its first line.
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	dir := filepath.Join(talkoot.Dir(), "crew")
	before, err := os.ReadFile(filepath.Join(dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	room := filepath.Join(dir, talkoot.RoomFile)
	if err := os.Chmod(room, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(room, 0o600) })
	posture := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: ask\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(posture)); err == nil {
		t.Fatal("want a posture-only update refused when the room cannot record it")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, talkoot.FileName)); string(after) != string(before) {
		t.Error("the refused posture update changed the roster file")
	}
}

// 🚨 A member session exists only once the room records its seat. A session
// the room does not know would lose its member at the next start, and it
// could become the person's default with the team's envelopes in it.
func TestAMemberSessionNeedsItsSeatLine(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a file mode stops a write only for an ordinary user on unix")
	}
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	// The first post makes the room file, and helm's session with it.
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	sessionsBefore := len(session.ListSessions(w.root, w.cwd))
	room := filepath.Join(talkoot.Dir(), "crew", talkoot.RoomFile)
	if err := os.Chmod(room, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(room, 0o600) })
	run, _ := w.talkootRunOf("crew")
	jev, _ := memberOf(*run.roster.Load(), "jev")
	if id, err := w.memberSession(run, jev); err == nil {
		t.Fatalf("want the session refused when the room cannot record its seat, got %q", id)
	}
	if got := memberView(t, w, "crew", "jev").Session; got != "" {
		t.Errorf("jev holds session %q with no seat line", got)
	}
	if got := len(session.ListSessions(w.root, w.cwd)); got != sessionsBefore {
		t.Errorf("a refused member session left a file: %d sessions, want %d", got, sessionsBefore)
	}
}

// 🚨 An update that fails after it sealed a leaver's empty seat line seals
// the old seat again. The caller was told nothing changed, so a restart must
// not take the member's session away, and no posture may have moved.
func TestAFailedUpdateRestoresTheSeatsItSealed(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"helm", "jev"}, "Look.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "both turns to end", func() bool {
		return !memberView(t, w, "crew", "helm").Status.Working && !memberView(t, w, "crew", "jev").Status.Working
	})
	helm := w.existing(memberView(t, w, "crew", "helm").Session)
	jev := memberView(t, w, "crew", "jev").Session
	// A directory in the roster file's place fails the rename that writes
	// it, and leaves the room writable.
	file := filepath.Join(talkoot.Dir(), "crew", talkoot.FileName)
	if err := os.Rename(file, file+".aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(file, "block"), 0o700); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: ask\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); err == nil {
		t.Fatal("want the update refused when its roster file cannot be written")
	}
	if _, ok := w.talkootSeatOf(jev); !ok {
		t.Error("the refused update took jev's seat")
	}
	if helm.gate.Mode() != permission.ApprovalPlan || helm.argsSnapshot().Approval != "plan" {
		t.Errorf("the refused update changed helm's posture: gate %s, args %q", helm.gate.Mode(), helm.argsSnapshot().Approval)
	}
	if lines := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoster }); len(lines) != 2 || lines[1].Reason == "" {
		t.Errorf("want the roster line and the line that says it failed, got %+v", lines)
	}
	if err := os.RemoveAll(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".aside", file); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	if got := memberView(t, w2, "crew", "jev").Session; got != jev {
		t.Errorf("want jev back in %s after the restart, got %q", jev, got)
	}
}

// 🚨 A member turn that has not reported by the shutdown deadline never
// will, because a closed run refuses its report. The turn is recorded as
// free and the member pauses, so a restart does not forget it.
func TestShutdownPausesAMemberWhoseTurnNeverReported(t *testing.T) {
	cwd := talkootHome(t)
	// The provider hangs until the request goes away, so helm's turn is still
	// running when the talkoots close. The release frees a request nothing
	// cancels, before the server's Close waits on it.
	release := make(chan struct{})
	w := openTalkootWorkspaceWith(t, cwd, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to start", func() bool { return w.talkoot.turns.Load() > 0 })
	defer func(d time.Duration) { talkootCloseWait = d }(talkootCloseWait)
	talkootCloseWait = 20 * time.Millisecond
	// Not Close: Close would cancel the turn first, and it would report.
	w.closeTalkoots()

	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	helm := memberView(t, w2, "crew", "helm")
	if !strings.Contains(helm.Status.Paused, "had not reported its cost") {
		t.Errorf("want helm paused for the unreported turn, got %q", helm.Status.Paused)
	}
	if helm.Status.Turns != 1 {
		t.Errorf("want the unreported turn counted, got %d turns", helm.Status.Turns)
	}
}

// A member an update removed can still be mid-turn at shutdown. The router
// no longer lists it, so the run's own count of open turns finds the turn.
func TestShutdownRecordsTheOpenTurnOfAFormerMember(t *testing.T) {
	cwd := talkootHome(t)
	release := make(chan struct{})
	w := openTalkootWorkspaceWith(t, cwd, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Look.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "jev's turn to start", func() bool { return w.talkoot.turns.Load() > 0 })
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { talkootCloseWait = d }(talkootCloseWait)
	talkootCloseWait = 20 * time.Millisecond
	w.closeTalkoots()

	turns := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "jev" })
	if len(turns) != 1 || !strings.Contains(turns[0].Reason, "had not reported its cost") {
		t.Errorf("want jev's open turn recorded as unreported, got %+v", turns)
	}
}

// 🚨 Once shutdown stops the routers, an update must not swap in a router that
// was never stopped, and no talkoot may start. Either would deliver into
// sessions that are closing, and the room would record the delivery.
func TestShutdownRefusesAnUpdateAndANewTalkoot(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	w.stopTalkoots()
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: ask\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); !errors.Is(err, ErrTalkootClosed) {
		t.Errorf("want an update refused during shutdown, got %v", err)
	}
	second := strings.Replace(string(crewText(cwd)), "name: crew", "name: pair", 1)
	if _, err := w.talkootCreate(ctx, "pair", []byte(second)); !errors.Is(err, ErrTalkootClosed) {
		t.Errorf("want a new talkoot refused during shutdown, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), "pair")); err == nil {
		t.Error("the refused create left its directory")
	}
}

// 🚨 A member turn that starts after the talkoots close would run with no run
// to report to, and its cost would be lost. It does not run.
func TestAMemberTurnAfterCloseDoesNotRun(t *testing.T) {
	cwd := talkootHome(t)
	var requests atomic.Int64
	w := openTalkootWorkspaceWith(t, cwd, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"the test provider refuses every request"}}`))
	})
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return w.talkoot.turns.Load() == 0 && requests.Load() > 0 })
	before := requests.Load()
	s := w.existing(memberView(t, w, "crew", "helm").Session)
	w.closeTalkoots()
	s.queue("One more thing.")
	time.Sleep(300 * time.Millisecond)
	if got := requests.Load(); got != before {
		t.Errorf("a member turn ran after the talkoots closed: %d requests, want %d", got, before)
	}
	if n := w.talkoot.turns.Load(); n != 0 {
		t.Errorf("a turn opened after the talkoots closed: %d open", n)
	}
}

// The in-process terminal runs no talkoots, but it shares a directory with the
// daemon that does. A member session must not become its default either.
func TestAWorkspaceThatRunsNoTalkootsSkipsTheirMemberSessions(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	helm := memberView(t, w, "crew", "helm").Session

	// No LoadTalkoots: the terminal beside the daemon.
	term := openTalkootWorkspace(t, cwd)
	if p := term.latestPersonSession(); p != "" && build.SessionIDFromPath(p) == helm {
		t.Error("the terminal's default is helm's member session")
	}
}

// An update takes the seat from a member that left, and gives a member whose
// tier changed a fresh session on its next delivery.
func TestUpdateUnseatsALeaverAndRenewsATierChange(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"helm", "jev"}, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "both turns to end", func() bool {
		return !memberView(t, w, "crew", "helm").Status.Working && !memberView(t, w, "crew", "jev").Status.Working
	})
	helm := memberView(t, w, "crew", "helm").Session
	jev := memberView(t, w, "crew", "jev").Session
	if helm == "" || jev == "" {
		t.Fatalf("want both members seated, got helm %q jev %q", helm, jev)
	}
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    tier: weak\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.talkootSeatOf(jev); ok {
		t.Error("jev left the roster and kept its seat")
	}
	if seats := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineSeat && l.Member == "jev" }); len(seats) == 0 || seats[len(seats)-1].Ref != "" {
		t.Errorf("want jev's leaving sealed as an empty seat line, got %+v", seats)
	}
	if _, ok := w.talkootSeatOf(helm); ok {
		t.Error("helm's tier changed and its old session kept the seat")
	}
	if got := memberView(t, w, "crew", "helm").Session; got != "" {
		t.Errorf("want helm unbound until its next delivery, got %q", got)
	}
	resumeFailed(t, w, "crew", "helm")
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's next turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	fresh := memberView(t, w, "crew", "helm").Session
	if fresh == "" || fresh == helm {
		t.Errorf("want a fresh session for helm, got %q", fresh)
	}
	// 🚨 An unseated session keeps the team's envelopes, and its member
	// posture until a rebuild. It never becomes the person's default, here
	// or after a restart.
	notDefault := func(w *Workspace, when string) {
		t.Helper()
		if p := w.latestPersonSession(); p != "" {
			if id := build.SessionIDFromPath(p); id == helm || id == jev || id == fresh {
				t.Errorf("%s: the former member session %s became the person's default", when, id)
			}
		}
	}
	notDefault(w, "after the update")
	_ = w.Close()
	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	notDefault(w2, "after a restart")
}

// A restart binds the member to the session it had, from the seat line, and
// keeps the spend.
func TestARestartKeepsTheSeatAndTheSpend(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	run, _ := w.talkootRunOf("crew")
	if err := run.do(func(rt *talkoot.Router) error { return rt.TurnEnded("jev", 1.25) }); err != nil {
		t.Fatal(err)
	}
	sid := memberView(t, w, "crew", "helm").Session
	_ = w.Close()

	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	// The failed turn left the person's words in the transcript, so the
	// session outlives the close.
	if _, err := os.Stat(w2.sessionPath(sid)); err != nil {
		t.Fatalf("helm's session did not survive the close: %v", err)
	}
	if got := memberView(t, w2, "crew", "helm").Session; got != sid {
		t.Errorf("want helm back in %s, got %q", sid, got)
	}
	if _, ok := w2.talkootSeatOf(sid); !ok {
		t.Error("the restart did not seat helm's session")
	}
	if got := memberView(t, w2, "crew", "jev").Status.SpendUSD; got != 1.25 {
		t.Errorf("want jev's spend kept across the restart, got %v", got)
	}
}

func TestUpdateKeepsSpendAndChangesPostureLive(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	run, _ := w.talkootRunOf("crew")
	if err := run.do(func(rt *talkoot.Router) error { return rt.TurnEnded("jev", 2) }); err != nil {
		t.Fatal(err)
	}
	sid := memberView(t, w, "crew", "helm").Session
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: ask\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(text)); err != nil {
		t.Fatal(err)
	}
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got != 2 {
		t.Errorf("an update must keep the spend, got %v", got)
	}
	if got := memberView(t, w, "crew", "helm").Session; got != sid {
		t.Errorf("a posture change must keep the session, got %q", got)
	}
	if got := w.existing(sid).argsSnapshot().Approval; got != "ask" {
		t.Errorf("want the new posture live, got %q", got)
	}
	if lines := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoster }); len(lines) != 1 || lines[0].By != "human:sothr" {
		t.Errorf("want one roster line by the person, got %+v", lines)
	}
}

func TestPauseResumeAndWatch(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	kinds := map[string]int{}
	stop := w.talkootWatch("crew", func(ev talkootEvent) {
		mu.Lock()
		kinds[ev.Kind]++
		mu.Unlock()
	})
	defer stop()
	if err := w.talkootPause(ctx, "crew", "sothr", "", "", "hold on"); err != nil {
		t.Fatal(err)
	}
	if why := memberView(t, w, "crew", "helm").Status.Paused; !strings.Contains(why, "hold on") {
		t.Fatalf("want the talkoot paused, got %q", why)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := memberView(t, w, "crew", "helm").Session; got != "" {
		t.Errorf("a paused talkoot must not deliver, but helm has session %q", got)
	}
	if err := w.talkootResume(ctx, "crew", "sothr", "", ""); err != nil {
		t.Fatal(err)
	}
	if memberView(t, w, "crew", "helm").Session == "" {
		t.Error("the resume must deliver what the pause held")
	}
	mu.Lock()
	defer mu.Unlock()
	if kinds["envelope"] != 1 || kinds["status"] == 0 {
		t.Errorf("want one envelope event and status events, got %v", kinds)
	}
}

func TestRoomPagesBackward(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := context.Background()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if err := w.talkootPause(ctx, "crew", "sothr", "", "", "quiet"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "note "+string(rune('a'+i)), nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	page, err := w.talkootRoom(ctx, "crew", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 2 || page.Next != page.Total-2 || page.Lines[1].Envelope == nil || page.Lines[1].Envelope.Body != "note e" {
		t.Fatalf("want the newest two lines, got %+v", page)
	}
	prev, err := w.talkootRoom(ctx, "crew", page.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(prev.Lines) != 2 || prev.Lines[1].Envelope == nil || prev.Lines[1].Envelope.Body != "note c" {
		t.Errorf("want the two lines before, got %+v", prev)
	}
}
