package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/privfs"
)

// A watcher is a routine that runs a command and no model (decision 0029
// rule 6). Its time runs the command. A run starts only when the command's
// output changed, so a check that finds nothing new costs no model call.
//
// 🔑 An outside event is the one way into a team for anything that no member
// and no person sent. A watcher's check is the first source. A webhook, a
// connector's event frame and an MCP events client arrive later, and they
// put their events in the same way (talkootEventPut).
//
// ⚠️ The code says "event" where the decision says "inbox". The person's
// cards already have the name inbox here (workspace_talkoot_inbox.go).

// watcherSource is the source that a watcher's own check gives its events.
const watcherSource = "watcher"

const (
	// watcherTimeout bounds one check. A check is a look at a state, and a
	// command that takes longer is at work and not at a look.
	watcherTimeout = 2 * time.Minute
	// watcherMaxOutput bounds what one check may print. A run reads the
	// output from a file, and a larger one costs more than a look is worth.
	watcherMaxOutput = 256 * 1024
	// watcherKeepChanges is the count of changes whose two files stay on
	// disk. The files of a change that started no run are removed at once,
	// so these are the last runs that started (watcherPrune).
	watcherKeepChanges = 4
)

// watchCommand is one check of a watcher.
type watchCommand struct {
	// Command is the command as the card showed it.
	Command string
	// Dir is the directory that the command starts in.
	Dir string
	// StateDir is the one directory that the command may write to.
	StateDir string
	Timeout  time.Duration
	// MaxOutput is the count of bytes that the caller reads. The function
	// may return one byte more, which marks an output above the limit.
	MaxOutput int
}

// watchExec runs one check, and returns what the command printed to its
// standard output. An error is a command that failed, timed out, or could
// not start.
//
// 🚨 It is nil in a build that runs no watcher command, and a watcher then
// does nothing at its time. The function must run the command read-only,
// with StateDir as the one place to write (decision 0029 rule 6). Nothing in
// this file can do that. TKT-01M7N389B holds the question of what does.
var watchExec func(ctx context.Context, c watchCommand) ([]byte, error)

// watcherRuns reports whether this build runs a watcher's command.
func watcherRuns() bool { return watchExec != nil }

// outsideEvent is one event from outside a team. It asks for a run of the
// watcher that it names.
type outsideEvent struct {
	// Source names the kind of source, such as "watcher".
	Source string
	// Routine is the id of the watcher that the event belongs to. The
	// approval of that watcher's card is the event's authority.
	Routine string
	// Old and New are the paths of the files that hold what the source saw
	// before, and what it sees now. Both are inside the team's directory
	// (eventFile).
	Old, New string
	// done, when it is set, is called one time on the team's goroutine.
	// started says that a run of the event began, and that its session
	// took the text with the event's two paths.
	//
	// 🔑 A source removes its own duplicates (decision 0002). An event that
	// started no run is not taken, and its source decides whether to put it
	// again.
	done func(started bool)
}

// eventsDir is the directory, inside a team's directory, where a source
// that is no watcher puts the files of its events.
const eventsDir = "events"

// eventFile reports whether path is a file that an event may point a run
// at: a regular file inside the team's watch or events directory.
//
// 🚨 The run's text tells a model to read the path. A source may get its
// content from outside, and it must never get to choose the path too. So a
// source copies what it saw into the team's directory, and nothing else
// passes. The path must be in its plain form, and no link.
func eventFile(run *talkootRun, path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	// 🚨 The place is tested on the path with every link followed. A link in
	// a directory of the path would pass a test of the path's text, and the
	// read tool follows it. The team's own directory may be reached through
	// a link, so it is resolved too. The watch and events directories are
	// then named under it and not resolved: one of them that is itself a
	// link out of the team gives a path outside it.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	team, err := filepath.EvalSymlinks(run.dir)
	if err != nil {
		return false
	}
	inside := false
	for _, sub := range []string{watcherRootDir, eventsDir} {
		if rel, err := filepath.Rel(filepath.Join(team, sub), real); err == nil && filepath.IsLocal(rel) && rel != "." {
			inside = true
		}
	}
	if !inside {
		return false
	}
	// The last part is no link itself, and it is a plain file.
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// talkootEventPut hands an event from outside to the team id. The team's own
// goroutine takes the events in the order of their arrival. It reports an
// error only for a team that this daemon does not run.
func (w *Workspace) talkootEventPut(id string, ev outsideEvent) error {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return err
	}
	// 🔑 The time of an event is the time at which the team takes it. A
	// source gives none, so a stale time or a time ahead cannot pass the
	// end of a routine.
	run.later(func() { w.eventArrived(run, ev, time.Now()) })
	return nil
}

// eventArrived decides what one outside event does, and does it. An event
// asks every test that a firing of the routine asks (routineReady). now is
// the time at which the team takes the event.
func (w *Workspace) eventArrived(run *talkootRun, ev outsideEvent, now time.Time) {
	started := false
	if ev.done != nil {
		defer func() { ev.done(started) }()
	}
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	// 🚨 The kind comes first. routineReady ends a routine that is past its
	// end, and it removes a timer that should not be there. An event has
	// authority over a watcher alone, so one that names a brief or a task
	// must change nothing of it.
	if r, err := talkoot.LoadRoutine(run.dir, ev.Routine); err != nil || r.Kind != talkoot.RoutineWatcher {
		w.diagf("talkoot %s: an event of the source %s names %q, which is no watcher of this team. An event starts a run of a watcher alone, so it starts nothing", run.id, ev.Source, ev.Routine)
		return
	}
	if !eventFile(run, ev.Old) || !eventFile(run, ev.New) {
		w.diagf("talkoot %s: an event of the source %s for watcher %s names a file that is not a plain file in the team's %s or %s directory, so it starts nothing", run.id, ev.Source, ev.Routine, watcherRootDir, eventsDir)
		return
	}
	r, ok := w.routineReady(run, ev.Routine, now)
	if !ok {
		return
	}
	start := talkoot.RunStart{Routine: r, Event: &talkoot.RunEvent{Source: ev.Source, Old: ev.Old, New: ev.New}}
	started = w.startRoutineRun(run, r, start, now)
}

// watcherDir is the directory of one watcher's state, inside its team's
// directory. The read tool may read it, and a run reads its change files
// there.
func watcherDir(run *talkootRun, rid string) string {
	return filepath.Join(run.dir, watcherRootDir, rid)
}

// watcherRootDir holds the state of every watcher of a team, one directory
// each.
const watcherRootDir = "watch"

// The files of a watcher, inside watcherDir.
const (
	// watcherSeenFile holds the output that the member was last told of.
	// The first check writes it, and after that only a run that started.
	watcherSeenFile = "seen.txt"
	// watcherStateDir is the one directory that the command may write to.
	watcherStateDir = "state"
	// watcherChangesDir holds the two files of each change.
	watcherChangesDir = "changes"
)

// watcherFired starts one check of the watcher r. The caller holds
// run.routineMu, on the team's goroutine.
//
// 🔑 The command runs on a goroutine of its own, because a check may take
// its time and the team's goroutine carries every firing of the team. One
// check of a watcher runs at a time. A firing that finds the last check
// still at work starts none.
func (w *Workspace) watcherFired(run *talkootRun, r talkoot.Routine, now time.Time) {
	if !watcherRuns() {
		return
	}
	dir := watcherDir(run, r.ID)
	state := filepath.Join(dir, watcherStateDir)
	if err := privfs.MkdirAll(state); err != nil {
		w.pauseRoutine(run, r.ID, "the directory of its state could not be made: "+failureReason(err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), watcherTimeout)
	run.watchMu.Lock()
	if _, busy := run.watching[r.ID]; busy || run.watchStopped {
		run.watchMu.Unlock()
		cancel()
		return
	}
	if run.watching == nil {
		run.watching = map[string]context.CancelFunc{}
	}
	run.watching[r.ID] = cancel
	run.watchMu.Unlock()
	c := watchCommand{Command: r.Command, Dir: w.cwd, StateDir: state, Timeout: watcherTimeout, MaxOutput: watcherMaxOutput}
	exec := watchExec
	began := time.Now()
	go func() {
		out, err := exec(ctx, c)
		// The team's close cancels the check, and so does a person who
		// pauses or deletes the watcher. A time limit that passed is a
		// failure of the command, and a cancel is none.
		stopped := errors.Is(ctx.Err(), context.Canceled)
		cancel()
		// 🔑 now is the timer service's reading of the clock when the check
		// began. The time that the command took is added, so the tests of
		// the routine's end read the time of the result.
		run.later(func() { w.watcherChecked(run, r.ID, out, err, stopped, now.Add(time.Since(began))) })
	}()
}

// stopWatcher cancels the check of the watcher rid, when one is at work. A
// person's pause or delete calls it, so the command does not go on after
// the person stopped its watcher.
func (run *talkootRun) stopWatcher(rid string) {
	run.watchMu.Lock()
	defer run.watchMu.Unlock()
	if cancel := run.watching[rid]; cancel != nil {
		cancel()
	}
}

// stopWatchers cancels each check that is at work, and lets no new one
// start. The close of a team calls it, so no command outlives its team.
func (run *talkootRun) stopWatchers() {
	run.watchMu.Lock()
	defer run.watchMu.Unlock()
	run.watchStopped = true
	for _, cancel := range run.watching {
		cancel()
	}
}

// watcherChecked takes the result of one check, on the team's goroutine.
//
// 🚨 A command that failed is no change. It counts as a failed run, and a
// failed run pauses its routine (decision 0029 rule 13), so a person sees
// why the watcher went quiet.
//
// 🔑 The output that the member was last told of moves on only when the room
// holds the root of the run for the change. A daemon that stops before that,
// or a run that could not start, loses nothing: the next check finds the
// same change again. So the watcher needs no file for the events that it
// has put.
func (w *Workspace) watcherChecked(run *talkootRun, rid string, out []byte, checkErr error, stopped bool, now time.Time) {
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	run.watchMu.Lock()
	delete(run.watching, rid)
	run.watchMu.Unlock()
	// A person may have paused or deleted the watcher while its command
	// ran, and the team may have closed.
	r, err := talkoot.LoadRoutine(run.dir, rid)
	if errors.Is(err, talkoot.ErrNoRoutine) {
		// 🚨 The delete removed the watcher's state while the command could
		// still write to it. The command has ended now, so what it left
		// goes too.
		if err := os.RemoveAll(watcherDir(run, rid)); err != nil {
			w.diagf("talkoot %s: watcher %s is deleted, and the directory of its state stays: %v", run.id, rid, err)
		}
		return
	}
	if stopped || err != nil || r.Status != talkoot.RoutineActive || r.Kind != talkoot.RoutineWatcher {
		// A cancel is no failure of the command: the team closed, or a
		// person stopped the watcher. The next check compares again.
		return
	}
	switch {
	case checkErr != nil:
		w.pauseRoutine(run, rid, "its command failed: "+failureReason(checkErr.Error()))
		return
	case len(out) > watcherMaxOutput:
		w.pauseRoutine(run, rid, fmt.Sprintf("its command printed more than %d bytes, and a check reads no more", watcherMaxOutput))
		return
	}
	out = watcherOutput(out)
	dir := watcherDir(run, rid)
	seenPath := filepath.Join(dir, watcherSeenFile)
	seen, err := os.ReadFile(seenPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// The first check. It sets what the later checks compare with, and
		// it starts nothing.
		if err := privfs.WriteFile(seenPath, out); err != nil {
			w.pauseRoutine(run, rid, "its first output could not be kept: "+failureReason(err.Error()))
		}
		return
	case err != nil:
		w.pauseRoutine(run, rid, "its last output does not read: "+failureReason(err.Error()))
		return
	case bytes.Equal(seen, out):
		return
	}
	oldPath, newPath, err := watcherChange(dir, seen, out)
	if err != nil {
		w.pauseRoutine(run, rid, "the files of its change could not be written: "+failureReason(err.Error()))
		return
	}
	// 🔑 The event goes the way of every outside event, and not straight to
	// a run. The team's goroutine takes it after this call returns.
	run.later(func() {
		w.eventArrived(run, outsideEvent{
			Source: watcherSource, Routine: rid, Old: oldPath, New: newPath,
			done: func(started bool) {
				if !started {
					// 🚨 A change that started no run leaves no files, and it
					// removes no others. The files that stay are then those
					// of runs that started, and the newest of them is the one
					// that a run at work reads.
					_ = os.Remove(oldPath)
					_ = os.Remove(newPath)
					return
				}
				if err := privfs.WriteFile(seenPath, out); err != nil {
					w.diagf("talkoot %s: watcher %s started a run for a change, and could not keep the new output. Its next check finds the same change again: %v", run.id, rid, err)
				}
				// The run of this change is the one at work now, and the
				// runs of the older changes have ended.
				watcherPrune(dir)
			},
		}, now)
	})
}

// watcherOutput is the form of an output that a check compares: the bytes as
// printed, with no white space at the end.
//
// ⚠️ A command that prints a time, or a count that moves, changes at every
// check. Nothing here can tell such a change from one that matters. The
// routine's daily spend limit bounds what it costs.
func watcherOutput(out []byte) []byte {
	return bytes.TrimRight(out, " \t\r\n")
}

// watcherChange writes the two files of one change, and returns their paths.
//
// 🔑 The name is a count, one above the highest in the directory. A name
// from the clock would sort wrong after the clock went back, and the files
// that are removed are chosen by that order (watcherPrune).
func watcherChange(dir string, old, new []byte) (oldPath, newPath string, err error) {
	changes := filepath.Join(dir, watcherChangesDir)
	if err := privfs.MkdirAll(changes); err != nil {
		return "", "", err
	}
	seqs, err := watcherChanges(changes)
	if err != nil {
		return "", "", err
	}
	next := 1
	if len(seqs) > 0 {
		next = seqs[len(seqs)-1] + 1
	}
	oldPath, newPath = watcherChangePaths(changes, next)
	if err := privfs.WriteFile(oldPath, old); err != nil {
		return "", "", err
	}
	if err := privfs.WriteFile(newPath, new); err != nil {
		_ = os.Remove(oldPath)
		return "", "", err
	}
	return oldPath, newPath, nil
}

func watcherChangePaths(changes string, seq int) (oldPath, newPath string) {
	name := fmt.Sprintf("%08d", seq)
	return filepath.Join(changes, name+".old.txt"), filepath.Join(changes, name+".new.txt")
}

// watcherChanges returns the count of each change that has a file in the
// directory, lowest first.
func watcherChanges(changes string) ([]int, error) {
	entries, err := os.ReadDir(changes)
	if err != nil {
		return nil, err
	}
	var seqs []int
	for _, e := range entries {
		name, _, _ := strings.Cut(e.Name(), ".")
		if n, err := strconv.Atoi(name); err == nil && n > 0 && !slices.Contains(seqs, n) {
			seqs = append(seqs, n)
		}
	}
	slices.Sort(seqs)
	return seqs, nil
}

// watcherPrune removes the files of every change before the last
// watcherKeepChanges.
//
// 🚨 The caller runs it when a run started for the newest change, and at no
// other time. One run of a watcher works at a time, so no run reads an older
// pair then. A change that started no run removes its own pair and nothing
// else. So nothing ever removes the pair of a run at work, whatever the
// clock does.
func watcherPrune(dir string) {
	changes := filepath.Join(dir, watcherChangesDir)
	seqs, err := watcherChanges(changes)
	if err != nil {
		return
	}
	for i := 0; i < len(seqs)-watcherKeepChanges; i++ {
		oldPath, newPath := watcherChangePaths(changes, seqs[i])
		_ = os.Remove(oldPath)
		_ = os.Remove(newPath)
	}
}
