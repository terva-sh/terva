package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

var ciWatch = talkoot.RoutineAsk{Kind: talkoot.RoutineWatcher, Schedule: "every 15m", Command: "just forge-runs 5 | grep -v 'in progress'"}

// watchScript stands for a watcher's command. Check n prints outs[n], and
// the last output again after that. A check whose number is in fails holds
// an error in place of an output.
type watchScript struct {
	mu    sync.Mutex
	outs  []string
	fails map[int]error
	calls []watchCommand
	// hold, when it is set, keeps each check open until it closes or the
	// check's context ends.
	hold chan struct{}
	// onCancel, when it is set, is called by a check whose context ended,
	// before the check returns.
	onCancel func(c watchCommand)
}

func (ws *watchScript) exec(ctx context.Context, c watchCommand) ([]byte, error) {
	ws.mu.Lock()
	n := len(ws.calls)
	ws.calls = append(ws.calls, c)
	hold, onCancel := ws.hold, ws.onCancel
	out := ws.outs[min(n, len(ws.outs)-1)]
	err := ws.fails[n]
	ws.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			if onCancel != nil {
				onCancel(c)
			}
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (ws *watchScript) checks() []watchCommand {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]watchCommand(nil), ws.calls...)
}

// watching puts ws in place of the function that runs a watcher's command.
// Call it before the daemon starts, so the daemon is closed before the
// function is put back.
func watching(t *testing.T, outs ...string) *watchScript {
	t.Helper()
	ws := &watchScript{outs: outs, fails: map[int]error{}}
	old := watchExec
	watchExec = ws.exec
	t.Cleanup(func() { watchExec = old })
	return ws
}

// check moves the clock to the watcher's next time, and waits until the
// team has taken the result of check number n, counted from 1.
func check(t *testing.T, w *Workspace, clock *stepClock, ws *watchScript, n int) {
	t.Helper()
	fire(t, w, clock, 16*time.Minute)
	checked(t, w, ws, n)
}

func checked(t *testing.T, w *Workspace, ws *watchScript, n int) {
	t.Helper()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the result of a check", func() bool {
		run.watchMu.Lock()
		defer run.watchMu.Unlock()
		return len(ws.checks()) >= n && len(run.watching) == 0
	})
	// The first call waits for the function that took the result. That
	// function may have put an event, and the second call waits for it.
	settleRuns(t, w)
	settleRuns(t, w)
}

func watcherFile(t *testing.T, rid, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(talkoot.Dir(), "crew", "watch", rid, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 🚨 Criterion 1. A watcher runs its command at each of its times, and an
// output that did not change starts no model call. Criterion 4: the command
// that runs is the command of the card, byte for byte.
func TestAWatcherThatSeesNoChangeStartsNoModelCall(t *testing.T) {
	ws := watching(t, "run 41: success\n")
	p := &runScript{reply: "Checked."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	if got := routineOf(t, w, rid); got.Problem != "" {
		t.Fatalf("a build that runs a watcher's command names no problem: %q", got.Problem)
	}
	for n := 1; n <= 3; n++ {
		check(t, w, clock, ws, n)
	}
	calls := ws.checks()
	if len(calls) != 3 {
		t.Fatalf("the command must run at each of 3 times, got %d", len(calls))
	}
	state := filepath.Join(talkoot.Dir(), "crew", "watch", rid, "state")
	for _, c := range calls {
		if c.Command != ciWatch.Command || c.Dir != cwd || c.StateDir != state || c.Timeout != watcherTimeout || c.MaxOutput != watcherMaxOutput {
			t.Fatalf("the check must run the card's command in the daemon's directory, with the watcher's state directory: %+v", c)
		}
	}
	if fi, err := os.Stat(state); err != nil || !fi.IsDir() {
		t.Fatalf("the state directory must be there before the command runs: %v", err)
	}
	if n := len(p.runs()); n != 0 {
		t.Fatalf("an output that did not change made %d model requests", n)
	}
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("an output that did not change started %d runs", n)
	}
	if got := watcherFile(t, rid, watcherSeenFile); got != "run 41: success" {
		t.Fatalf("the first check keeps its output, with no white space at its end: %q", got)
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineActive || r.Runs != 0 {
		t.Fatalf("a watcher that saw no change is active, and it counts no run: %+v", r)
	}
}

// 🚨 Criterion 2. A changed output starts a run, and the run's first text
// points at the output before the change and at the output now. Criterion 5:
// that text enters the session as a team's delivery, and never as a
// person's message.
func TestAChangedOutputStartsARunWithAPointerToBothOutputs(t *testing.T) {
	ws := watching(t, "run 41: success\n", "run 41: success\nrun 42: failure\n")
	p := &runScript{reply: "Run 42 failed in lint."}
	w, seat, clock, _, release := runCrew(t, p, true)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("the first check sets what later checks compare with, and it started %d runs", n)
	}
	check(t, w, clock, ws, 2)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the change started no run")
	}
	roots := runRoots(t)
	if len(roots) != 1 || roots[0].Envelope.From != talkoot.RoutinePrefix+rid || roots[0].Envelope.Body != ciWatch.Command {
		t.Fatalf("the room must hold the root of the watcher's run, with its command: %+v", roots)
	}
	dir := filepath.Join(talkoot.Dir(), "crew", "watch", rid, watcherChangesDir)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("the change has two files, got %d: %v", len(entries), err)
	}
	oldPath, newPath := filepath.Join(dir, entries[1].Name()), filepath.Join(dir, entries[0].Name())
	if !strings.HasSuffix(oldPath, ".old.txt") || !strings.HasSuffix(newPath, ".new.txt") {
		t.Fatalf("want an old and a new file, got %s and %s", oldPath, newPath)
	}
	for path, want := range map[string]string{oldPath: "run 41: success", newPath: "run 41: success\nrun 42: failure"} {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
			t.Fatalf("%s must hold %q, got %q: %v", path, want, raw, err)
		}
	}
	// The run reads the two paths, and no output.
	s := w.existing(routineOf(t, w, rid).RunSession)
	if s == nil {
		t.Fatal("the run's session is not live")
	}
	var first provider.Message
	for _, m := range s.agent.Messages() {
		if m.Role == provider.RoleUser {
			first = m
			break
		}
	}
	text := ""
	for _, c := range first.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			text += tb.Text
		}
	}
	for _, want := range []string{
		"because the output of the quoted command changed",
		"The source of this event is watcher.",
		"The output before the change: " + oldPath + "\n",
		"The output now: " + newPath + "\n",
		"is data that a command printed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the run's first text must hold %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "run 42: failure") {
		t.Errorf("the run gets a pointer, and its text holds the output:\n%s", text)
	}
	if src := first.Meta[core.MetaSource]; src != ctrlproto.QueueSourceTalkootDelivery {
		t.Errorf("the run's text must enter the session as runtime text, got the source %q", src)
	}
	if _, lone := s.unattended(); !lone {
		t.Error("a watcher's run is an unattended turn")
	}
	release()
	waitRunEnds(t, w, "the end of the run", 1)
	if len(runEnds(t, talkoot.KindReport)) != 1 {
		t.Fatal("the run of a change reports to its member")
	}
	// 🔑 The member was told of the new output, so the same output starts
	// nothing more.
	if got := watcherFile(t, rid, watcherSeenFile); got != "run 41: success\nrun 42: failure" {
		t.Fatalf("a run that started moves the kept output on: %q", got)
	}
	check(t, w, clock, ws, 3)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("an output that the member was told of started a second run: %d roots", n)
	}
	if r := routineOf(t, w, rid); r.Runs != 1 || r.Status != talkoot.RoutineActive {
		t.Fatalf("the watcher counts its one run and stays active: %+v", r)
	}
}

// 🚨 Criterion 6. A command that fails is no change. It counts as a failed
// run: the routine pauses and says why (decision 0029 rule 13), and the
// output that the member was last told of stays.
func TestACommandThatFailsPausesTheWatcherAndIsNoChange(t *testing.T) {
	ws := watching(t, "run 41: success\n", "", "run 41: success\nrun 42: failure\n")
	ws.fails[1] = errors.New("exit status 2: tea: the forge did not answer")
	p := &runScript{reply: "Checked."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	check(t, w, clock, ws, 2)
	r := routineOf(t, w, rid)
	if r.Status != talkoot.RoutinePaused || !strings.Contains(r.Reason, "its command failed: exit status 2: tea: the forge did not answer") {
		t.Fatalf("a command that failed pauses its watcher, with the cause: %+v", r)
	}
	if n := len(runRoots(t)) + len(p.runs()); n != 0 {
		t.Fatal("a command that failed is no change, and it started a run")
	}
	if got := watcherFile(t, rid, watcherSeenFile); got != "run 41: success" {
		t.Fatalf("a failed check must keep the last output: %q", got)
	}
	// A person resumes it, and the next check compares with the same output.
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err != nil {
		t.Fatal(err)
	}
	check(t, w, clock, ws, 3)
	waitTalkoot(t, "the run of the change after the resume", func() bool { return len(runRoots(t)) == 1 })
}

// An output above the limit is a failure too, and no change.
func TestAnOutputAboveTheLimitPausesTheWatcher(t *testing.T) {
	ws := watching(t, "ok\n", strings.Repeat("x", watcherMaxOutput+1))
	w, seat, clock, _, _ := runCrew(t, &runScript{reply: "Checked."}, false)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	check(t, w, clock, ws, 2)
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutinePaused || !strings.Contains(r.Reason, "printed more than") {
		t.Fatalf("an output above the limit pauses its watcher: %+v", r)
	}
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("an output above the limit started %d runs", n)
	}
}

// 🔑 A change whose run could not start is not lost. The kept output stays,
// so the next check finds the change again.
func TestAChangeWhoseRunCouldNotStartComesBackAtTheNextCheck(t *testing.T) {
	ws := watching(t, "a\n", "b\n")
	p := &runScript{reply: "Checked."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	if err := w.PauseTalkoot(t.Context(), ctrlproto.TalkootPauseParams{ID: "crew", By: "sothr", Member: "helm", Reason: "lunch"}); err != nil {
		t.Fatal(err)
	}
	check(t, w, clock, ws, 2)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("a pause on the member must hold the run, got %d roots", n)
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "a pause holds it") {
		t.Fatalf("the room must say why the change started no run: %+v", skipped)
	}
	if got := watcherFile(t, rid, watcherSeenFile); got != "a" {
		t.Fatalf("a change that started no run must keep the old output: %q", got)
	}
	if err := w.ResumeTalkoot(t.Context(), ctrlproto.TalkootResumeParams{ID: "crew", By: "sothr", Member: "helm"}); err != nil {
		t.Fatal(err)
	}
	check(t, w, clock, ws, 3)
	waitRunEnds(t, w, "the end of the run", 1)
	if got := watcherFile(t, rid, watcherSeenFile); got != "b" {
		t.Fatalf("the run of the change moves the kept output on: %q", got)
	}
}

// One check of a watcher runs at a time, and the close of the daemon ends a
// check that is at work. A check that the close ended is no failure.
func TestOneCheckRunsAtATimeAndTheCloseEndsIt(t *testing.T) {
	ws := watching(t, "a\n")
	ws.hold = make(chan struct{})
	w, seat, clock, cwd, _ := runCrew(t, &runScript{reply: "Checked."}, false)
	rid := approved(t, w, seat, ciWatch)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the first check", func() bool { return len(ws.checks()) == 1 })
	fire(t, w, clock, 16*time.Minute)
	fire(t, w, clock, 16*time.Minute)
	if n := len(ws.checks()); n != 1 {
		t.Fatalf("a time that finds the last check at work starts none, got %d checks", n)
	}
	_ = w.Close()
	// The next daemon finds the watcher active: the close was no failure.
	ws.mu.Lock()
	ws.hold = nil
	ws.mu.Unlock()
	w2 := wakeDaemon(t, cwd, clock, (&runScript{reply: "Checked."}).serve)
	if r := routineOf(t, w2, rid); r.Status != talkoot.RoutineActive {
		t.Fatalf("a check that the close ended must not pause its watcher: %+v", r)
	}
}

// 🚨 A person who pauses or deletes a watcher while its command runs gets no
// run from that check, and the command does not go on: the check is
// cancelled. A delete removes the watcher's state, and also what the command
// wrote there after the delete.
func TestAWatcherThatAPersonStoppedDuringACheckStartsNothing(t *testing.T) {
	for _, action := range []string{ctrlproto.TalkootRoutinePause, ctrlproto.TalkootRoutineDelete} {
		t.Run(action, func(t *testing.T) {
			ws := watching(t, "a\n", "b\n")
			p := &runScript{reply: "Checked."}
			w, seat, clock, _, _ := runCrew(t, p, false)
			rid := approved(t, w, seat, ciWatch)
			check(t, w, clock, ws, 1)
			ws.mu.Lock()
			// Nothing closes hold, so the check ends only when it is
			// cancelled. A command that is killed can still have written.
			ws.hold = make(chan struct{})
			ws.onCancel = func(c watchCommand) {
				_ = os.MkdirAll(c.StateDir, 0o700)
				_ = os.WriteFile(filepath.Join(c.StateDir, "late"), []byte("x"), 0o600)
			}
			ws.mu.Unlock()
			fire(t, w, clock, 16*time.Minute)
			waitTalkoot(t, "the second check", func() bool { return len(ws.checks()) == 2 })
			if _, err := setRoutine(t, w, rid, action, "Too noisy."); err != nil {
				t.Fatal(err)
			}
			checked(t, w, ws, 2)
			if n := len(runRoots(t)) + len(p.runs()); n != 0 {
				t.Fatal("a watcher that a person stopped started a run")
			}
			_, err := os.Stat(filepath.Join(talkoot.Dir(), "crew", "watch", rid))
			if gone := errors.Is(err, os.ErrNotExist); gone != (action == ctrlproto.TalkootRoutineDelete) {
				t.Fatalf("a delete removes the watcher's state, and a pause keeps it: %v", err)
			}
			if action == ctrlproto.TalkootRoutinePause {
				if r := routineOf(t, w, rid); r.Status != talkoot.RoutinePaused || !strings.Contains(r.Reason, "Too noisy.") {
					t.Fatalf("a check that the pause cancelled is no failure, and the person's reason stays: %+v", r)
				}
			}
		})
	}
}

// eventFiles writes the two files of an event of another source into the
// team's events directory, where such a source must put them.
func eventFiles(t *testing.T) (oldPath, newPath string) {
	t.Helper()
	dir := filepath.Join(talkoot.Dir(), "crew", eventsDir, "forge-hook")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath, newPath = filepath.Join(dir, "before.json"), filepath.Join(dir, "after.json")
	for _, path := range []string{oldPath, newPath} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return oldPath, newPath
}

// put hands ev to the team, and returns what its source heard.
func put(t *testing.T, w *Workspace, ev outsideEvent) (started bool) {
	t.Helper()
	heard := make(chan bool, 1)
	ev.done = func(started bool) { heard <- started }
	if err := w.talkootEventPut("crew", ev); err != nil {
		t.Fatal(err)
	}
	select {
	case started = <-heard:
	case <-time.After(10 * time.Second):
		t.Fatal("the source heard nothing of its event")
	}
	return started
}

// 🚨 Criterion 7. An event of a second source starts a run through the same
// way in, and the router needed no change for it.
func TestAnEventOfASecondSourceStartsARun(t *testing.T) {
	ws := watching(t, "a\n")
	p := &runScript{reply: "Pull request 12 got a review."}
	w, seat, _, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	oldPath, newPath := eventFiles(t)
	if !put(t, w, outsideEvent{Source: "forge-hook", Routine: rid, Old: oldPath, New: newPath}) {
		t.Fatal("a source must hear that the room holds its event's run")
	}
	waitRunEnds(t, w, "the end of the run", 1)
	if !runRead(p, "The source of this event is forge-hook.", "The output before the change: "+oldPath, "The output now: "+newPath) {
		t.Fatal("the run must read the source and the two paths of the event")
	}
	if n := len(ws.checks()); n != 0 {
		t.Fatalf("an event of another source runs no command, got %d checks", n)
	}
	if err := w.talkootEventPut("no-such-team", outsideEvent{Source: "forge-hook", Routine: rid, Old: oldPath, New: newPath}); err == nil {
		t.Fatal("an event for a team that this daemon does not run must be refused")
	}
}

// 🚨 The run's text tells a model to read the two paths of an event. A
// source may get its content from outside, and it never gets to choose the
// path too: a path outside the team's watch and events directories starts
// nothing, and so does a link out of them.
func TestAnEventMayPointOnlyAtAFileOfTheTeam(t *testing.T) {
	watching(t, "a\n")
	p := &runScript{reply: "Checked."}
	w, seat, _, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	good, other := eventFiles(t)
	outside := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(good), "link.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("this platform makes no link: %v", err)
	}
	team := filepath.Join(talkoot.Dir(), "crew")
	// A directory of the path that is a link out of the team.
	linkDir := filepath.Join(team, eventsDir, "linked")
	if err := os.Symlink(cwd, linkDir); err != nil {
		t.Fatal(err)
	}
	// The watch directory itself as a link out of the team. No check ran,
	// so the daemon has not made it.
	linkRoot := filepath.Join(team, watcherRootDir)
	if err := os.Symlink(cwd, linkRoot); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"a file of the workspace":                       outside,
		"a link out of the directory":                   link,
		"a file under a linked directory":               filepath.Join(linkDir, "notes.txt"),
		"a file under a watch directory that is a link": filepath.Join(linkRoot, "notes.txt"),
		"a file that is not there":                      filepath.Join(filepath.Dir(good), "gone.json"),
		"a directory":                                   filepath.Dir(good),
		"the team's roster":                             filepath.Join(team, "talkoot.md"),
		"a path that climbs out":                        filepath.Join(team, eventsDir) + "/../" + eventsDir + "/forge-hook/before.json",
		"a path that is not absolute":                   "events/forge-hook/before.json",
		"a routine's record":                            filepath.Join(team, talkoot.RoutinesDir, rid+".json"),
		"the events directory itself":                   filepath.Join(team, eventsDir),
		"no path":                                       "",
		"a path in the team's own root":                 filepath.Join(team, "before.json"),
	} {
		for _, ev := range []outsideEvent{
			{Source: "forge-hook", Routine: rid, Old: path, New: other},
			{Source: "forge-hook", Routine: rid, Old: other, New: path},
		} {
			if put(t, w, ev) {
				t.Fatalf("%s: an event that points there must start no run", name)
			}
		}
	}
	if n := len(runRoots(t)) + len(p.runs()); n != 0 {
		t.Fatalf("a refused event started %d runs or requests", n)
	}
	if r := routineOf(t, w, rid); r.Runs != 0 {
		t.Fatalf("a refused event must count no run: %+v", r)
	}
	// The control: the same event with two files of the team starts a run.
	if !put(t, w, outsideEvent{Source: "forge-hook", Routine: rid, Old: good, New: other}) {
		t.Fatal("an event that points at two files of the team must start a run")
	}
}

// 🚨 The approval of a watcher's card is what gives an event its authority.
// An event that names a task starts nothing and counts no run. It also
// changes nothing of the task: here the task is past its last day, and the
// event must not be what ends it.
func TestAnEventStartsNoRunOfATask(t *testing.T) {
	watching(t, "a\n")
	p := &runScript{reply: "Done."}
	w, seat, _, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	dir := filepath.Join(talkoot.Dir(), "crew")
	r, err := talkoot.LoadRoutine(dir, rid)
	if err != nil {
		t.Fatal(err)
	}
	r.EndsAt = time.Now().Add(-time.Hour)
	if err := talkoot.SaveRoutine(dir, r); err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := eventFiles(t)
	if put(t, w, outsideEvent{Source: "forge-hook", Routine: rid, Old: oldPath, New: newPath}) {
		t.Fatal("an event must start no run of a task")
	}
	if n := len(runRoots(t)) + len(p.runs()); n != 0 {
		t.Fatal("an event must start no run of a task")
	}
	got := routineOf(t, w, rid)
	if got.Runs != 0 || got.Status != talkoot.RoutineActive || len(routineTimers(w)) != 1 {
		t.Fatalf("an event must change nothing of a task: %+v, %d timers", got, len(routineTimers(w)))
	}
	if put(t, w, outsideEvent{Source: "forge-hook", Routine: "01NOSUCHROUTINE", Old: oldPath, New: newPath}) {
		t.Fatal("an event that names no routine must start nothing")
	}
}

// 🚨 The files of a change stay while a run reads them. A check that finds
// the change again while that run works starts no run, and it leaves no
// files. So the count of kept changes never removes the pair of the run at
// work.
func TestTheFilesOfARunAtWorkStayThroughLaterChecks(t *testing.T) {
	ws := watching(t, "1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n")
	p := &runScript{reply: "Checked."}
	w, seat, clock, _, release := runCrew(t, p, true)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	check(t, w, clock, ws, 2)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the change started no run")
	}
	dir := filepath.Join(talkoot.Dir(), "crew", "watch", rid, watcherChangesDir)
	names := func() []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	first := names()
	if len(first) != 2 {
		t.Fatalf("the run has two files, got %v", first)
	}
	// More checks than the count of kept changes, each with a new output.
	for n := 3; n <= 3+watcherKeepChanges+1; n++ {
		check(t, w, clock, ws, n)
	}
	if got := names(); !slices.Equal(got, first) {
		t.Fatalf("the files of the run at work must stay, and a change that started no run leaves none:\nwant %v\ngot  %v", first, got)
	}
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("one run of a watcher works at a time, got %d roots", n)
	}
	release()
	waitRunEnds(t, w, "the end of the run", 1)
}

// 🚨 The tests of a routine's end read the time of the check's result, and
// not the time at which the check began. A watcher whose last day ends while
// its command runs starts no run.
func TestAWatcherThatEndsDuringItsCheckStartsNoRun(t *testing.T) {
	ws := watching(t, "a\n", "b\n")
	p := &runScript{reply: "Checked."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	ask := ciWatch
	ask.EndAfterDays = new(1)
	rid := approved(t, w, seat, ask)
	check(t, w, clock, ws, 1)
	// The record's seal covers its last day, so the test reads the day that
	// the approval gave, and moves the clock to a moment before it.
	r, err := talkoot.LoadRoutine(filepath.Join(talkoot.Dir(), "crew"), rid)
	if err != nil || r.EndsAt.IsZero() {
		t.Fatalf("the watcher must have a last day: %+v %v", r, err)
	}
	ws.mu.Lock()
	ws.hold = make(chan struct{})
	hold := ws.hold
	ws.mu.Unlock()
	fire(t, w, clock, r.EndsAt.Round(0).Sub(clock.Now().Round(0))-50*time.Millisecond)
	waitTalkoot(t, "the second check", func() bool { return len(ws.checks()) == 2 })
	time.Sleep(200 * time.Millisecond)
	close(hold)
	checked(t, w, ws, 2)
	if n := len(runRoots(t)) + len(p.runs()); n != 0 {
		t.Fatal("a watcher whose end passed during its check started a run")
	}
	if got := routineOf(t, w, rid); got.Status != talkoot.RoutineEnded {
		t.Fatalf("the watcher is past its last day, so it ends: %+v", got)
	}
}

// A build that runs no watcher command says so in the list, and a watcher's
// time does nothing there.
func TestABuildWithNoCommandRunnerRunsNoWatcher(t *testing.T) {
	if watchExec != nil {
		t.Skip("this build runs a watcher's command")
	}
	w, seat, clock, _, _ := runCrew(t, &runScript{reply: "Checked."}, false)
	rid := approved(t, w, seat, ciWatch)
	fire(t, w, clock, 16*time.Minute)
	if r := routineOf(t, w, rid); !strings.Contains(r.Problem, "A watcher does not run yet") || r.Status != talkoot.RoutineActive {
		t.Fatalf("the list must say that a watcher does not run yet: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), "crew", "watch", rid)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a watcher that does not run must write no state: %v", err)
	}
}

// 🚨 A run that has a root and no session did not read the change. The
// watcher keeps the change, so it comes back after a person resumes the
// routine that the failed run paused.
func TestAChangeWhoseRunHadNoSessionComesBack(t *testing.T) {
	defer func(fn func() error) { runBuilding = fn }(runBuilding)
	ws := watching(t, "a\n", "b\n")
	p := &runScript{reply: "Checked."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	runBuilding = func() error { return errors.New("the model's file does not read") }
	check(t, w, clock, ws, 2)
	if tl := runTurns(t); len(tl) != 1 || tl[0].Guard != talkoot.GuardFailed || !strings.Contains(tl[0].Reason, "the run's session could not be built") {
		t.Fatalf("the run has a root, and it ends as failed: %+v", tl)
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutinePaused {
		t.Fatalf("a failed run pauses its watcher: %+v", r)
	}
	if got := watcherFile(t, rid, watcherSeenFile); got != "a" {
		t.Fatalf("a change that no session read must keep the old output: %q", got)
	}
	if entries, err := os.ReadDir(filepath.Join(talkoot.Dir(), "crew", "watch", rid, watcherChangesDir)); err != nil || len(entries) != 0 {
		t.Fatalf("a change that no session read leaves no files, got %d: %v", len(entries), err)
	}
	runBuilding = func() error { return nil }
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err != nil {
		t.Fatal(err)
	}
	check(t, w, clock, ws, 3)
	waitRunEnds(t, w, "the end of the run of the change", 2)
	if got := watcherFile(t, rid, watcherSeenFile); got != "b" {
		t.Fatalf("the run that read the change moves the kept output on: %q", got)
	}
}

// 🚨 The files of a change have a count for a name, and no time. A clock
// that went back can then neither give a new change an old name nor make the
// count remove a pair that it should keep. A change removes no files of
// another change: that happens when a run starts, and at no other time.
func TestTheFilesOfAChangeAreNamedByACount(t *testing.T) {
	dir := filepath.Join(talkootHome(t), "watch")
	var pairs [][2]string
	for range watcherKeepChanges + 3 {
		oldPath, newPath, err := watcherChange(dir, []byte("old"), []byte("new"))
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, [2]string{oldPath, newPath})
	}
	if got := filepath.Base(pairs[0][0]); got != "00000001.old.txt" {
		t.Fatalf("the first change is number 1, got %s", got)
	}
	if got := filepath.Base(pairs[len(pairs)-1][1]); got != "00000007.new.txt" {
		t.Fatalf("each change has the next number, got %s", got)
	}
	entries, err := os.ReadDir(filepath.Join(dir, watcherChangesDir))
	if err != nil || len(entries) != 2*len(pairs) {
		t.Fatalf("a change removes no files of another, got %d files: %v", len(entries), err)
	}
	// A run started for the newest change, so the older ones may go.
	watcherPrune(dir)
	for i, pair := range pairs {
		for _, path := range pair {
			_, err := os.Stat(path)
			if kept := err == nil; kept != (i >= len(pairs)-watcherKeepChanges) {
				t.Fatalf("the last %d changes stay and the ones before go, and change %d is wrong: %v", watcherKeepChanges, i+1, err)
			}
		}
	}
	// A number that was removed is not given again.
	oldPath, _, err := watcherChange(dir, []byte("old"), []byte("new"))
	if err != nil || filepath.Base(oldPath) != "00000008.old.txt" {
		t.Fatalf("the next change is number 8, got %s: %v", oldPath, err)
	}
}

// The files of older runs go when a run starts for a newer change, and the
// last few stay for a member that reads a report later.
func TestTheFilesOfOlderRunsGoWhenARunStarts(t *testing.T) {
	ws := watching(t, "1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n")
	p := &runScript{reply: "Checked."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciWatch)
	check(t, w, clock, ws, 1)
	runs := watcherKeepChanges + 2
	for n := 1; n <= runs; n++ {
		check(t, w, clock, ws, n+1)
		waitRunEnds(t, w, "the end of a run", n)
	}
	entries, err := os.ReadDir(filepath.Join(talkoot.Dir(), "crew", "watch", rid, watcherChangesDir))
	if err != nil || len(entries) != 2*watcherKeepChanges {
		t.Fatalf("want the files of the last %d runs, got %d: %v", watcherKeepChanges, len(entries), err)
	}
	if got := entries[len(entries)-1].Name(); got != "00000006.old.txt" {
		t.Fatalf("the newest run's files must stay, and the last name is %s", got)
	}
}
