package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/testsupport"
)

func boundStore(t *testing.T) *tasks.Store {
	t.Helper()
	s := tasks.NewStore(tasks.NewDirFS(testsupport.TempDir(t)), "test-agent")
	if err := s.Rebind("sess-1"); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	return s
}

func TestCreateValid(t *testing.T) {
	s := boundStore(t)
	text, isErr := Create(s, json.RawMessage(`{"tasks":[
		{"title":"Patch the parser bug"},
		{"title":"Add regression test","active_form":"Adding regression test"}
	]}`))
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "Created 2 task(s):") {
		t.Errorf("missing count header:\n%s", text)
	}
	if !strings.Contains(text, "task-1") || !strings.Contains(text, "task-2") {
		t.Errorf("missing ids:\n%s", text)
	}
	// The full current list is appended so the model sees inline state.
	if !strings.Contains(text, "task-1  pending  Patch the parser bug") {
		t.Errorf("missing inline list:\n%s", text)
	}
}

func TestCreateErrors(t *testing.T) {
	s := boundStore(t)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"bad json", `{`, "invalid args"},
		{"empty tasks", `{"tasks":[]}`, "at least one task"},
		{"blank title", `{"tasks":[{"title":"  "}]}`, "title is required"},
		{"bad status", `{"tasks":[{"title":"x","status":"in_progress"}]}`, "invalid status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := Create(s, json.RawMessage(c.raw))
			if !isErr {
				t.Fatalf("expected error, got: %s", text)
			}
			if !strings.Contains(text, c.want) {
				t.Errorf("want %q in %q", c.want, text)
			}
		})
	}
	if n := len(s.List()); n != 0 {
		t.Errorf("failed creates must not mutate; have %d", n)
	}
}

func TestUpdateActivationReportsDeactivation(t *testing.T) {
	s := boundStore(t)
	if _, isErr := Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`)); isErr {
		t.Fatal("seed create failed")
	}
	if text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`)); isErr {
		t.Fatalf("activate A: %s", text)
	}
	text, isErr := Update(s, json.RawMessage(`{"id":"task-2","status":"active"}`))
	if isErr {
		t.Fatalf("activate B: %s", text)
	}
	if !strings.Contains(text, "Updated task-2 → active") {
		t.Errorf("missing update line:\n%s", text)
	}
	if !strings.Contains(text, "Deactivated task-1 (was active)") {
		t.Errorf("missing deactivation line:\n%s", text)
	}
}

func TestUpdateUnknownID(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))
	text, isErr := Update(s, json.RawMessage(`{"id":"task-9","status":"done"}`))
	if !isErr {
		t.Fatalf("expected error, got: %s", text)
	}
	if !strings.Contains(text, `no task with id "task-9"`) {
		t.Errorf("unexpected error text: %s", text)
	}
}

func TestUpdateEvidenceNudge(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))

	// done without evidence -> sharpened nudge naming the task and the status.
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done"}`))
	if isErr {
		t.Fatalf("nudge must stay soft (no error): %s", text)
	}
	if !strings.Contains(text, "task-1 marked done without evidence") {
		t.Errorf("expected sharpened evidence nudge naming the task:\n%s", text)
	}
	// done with evidence -> no nudge, evidence shown inline.
	text, _ = Update(s, json.RawMessage(`{"id":"task-2","status":"done","evidence":"go test ./... passed"}`))
	if strings.Contains(text, "without evidence") {
		t.Errorf("unexpected nudge when evidence given:\n%s", text)
	}
	if !strings.Contains(text, "go test ./... passed") {
		t.Errorf("evidence not shown inline:\n%s", text)
	}
}

// Closing-the-list warning (Invariant A2): marking a task done/cancelled while
// real work remains and nothing is active is surfaced as a soft note, never an
// error, and never on a genuinely complete list or mid-work completion.
func TestUpdateClosingListWarning(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))

	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done"}`))
	if isErr {
		t.Fatalf("warning must stay soft (no error): %s", text)
	}
	if !strings.Contains(text, "still open") || !strings.Contains(text, "task-2 pending") {
		t.Errorf("expected closing-list warning naming task-2:\n%s", text)
	}
	if !strings.Contains(text, "none active") {
		t.Errorf("warning should note nothing is active:\n%s", text)
	}
}

func TestUpdateClosingWarningOnCancelled(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))
	text, _ := Update(s, json.RawMessage(`{"id":"task-1","status":"cancelled"}`))
	if !strings.Contains(text, "still open") {
		t.Errorf("cancelling the active task with open work should warn:\n%s", text)
	}
}

func TestUpdateNoWarningWhenListComplete(t *testing.T) {
	// Marking the last open task done => no warning (criterion 3: don't nag on a
	// genuinely complete list).
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done"}`))
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if strings.Contains(text, "still open") {
		t.Errorf("complete list must not warn:\n%s", text)
	}
}

func TestUpdateNoWarningWhenAnotherActive(t *testing.T) {
	// Closing a sibling while a different task is still active is normal mid-work
	// completion (focus is elsewhere) => no warning.
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))
	text, _ := Update(s, json.RawMessage(`{"id":"task-2","status":"done"}`))
	if strings.Contains(text, "still open") {
		t.Errorf("a task still active means work is mid-flight; no warning:\n%s", text)
	}
}

func TestUpdateNoWarningOnReopen(t *testing.T) {
	// A reopen (done -> pending) keys off a non-terminal target status and must
	// not trigger the closing warning even though open work then exists.
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"done"}`))
	text, _ := Update(s, json.RawMessage(`{"id":"task-1","status":"pending"}`))
	if strings.Contains(text, "still open") {
		t.Errorf("reopen must not warn:\n%s", text)
	}
}

// TestUpdateAbsentVsEmpty pins the pointer semantics: a field omitted from the
// JSON is left unchanged, while a field present as "" is applied.
func TestUpdateAbsentVsEmpty(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A","note":"keep me"}]}`))

	// Status-only update omits note -> note preserved.
	if _, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`)); isErr {
		t.Fatal("update failed")
	}
	if got := s.List()[0].Note; got != "keep me" {
		t.Errorf("absent note should be preserved, got %q", got)
	}
	// Explicit empty note -> cleared.
	if _, isErr := Update(s, json.RawMessage(`{"id":"task-1","note":""}`)); isErr {
		t.Fatal("update failed")
	}
	if got := s.List()[0].Note; got != "" {
		t.Errorf("explicit empty note should clear, got %q", got)
	}
}

// A patch with nothing to patch must be refused, not applied and reported as a
// success. The success line is what makes an id-only call loopable: it echoes
// the value the model was trying to change, which reads as "the write didn't
// land", so the model sends the same call again.
func TestUpdateRejectsAPatchThatChangesNothing(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))
	before := s.List()[0]

	for _, args := range []string{
		`{"id":"task-1"}`,
		`{"id":"task-1","title":"   "}`, // the store silently drops a blank title
	} {
		text, isErr := Update(s, json.RawMessage(args))
		if !isErr {
			t.Fatalf("%s: want an error, got success:\n%s", args, text)
		}
		if !strings.Contains(text, "No state changed.") {
			t.Errorf("%s: should say nothing was written:\n%s", args, text)
		}
		// The message has to name what to send instead — a model that had the
		// shape wrong learns nothing from "bad args".
		for _, field := range []string{"status", "title", "active_form", "note", "evidence"} {
			if !strings.Contains(text, field) {
				t.Errorf("%s: message omits the %q field:\n%s", args, field, text)
			}
		}
		if got := s.List()[0]; got != before {
			t.Errorf("%s: the task was mutated: %+v -> %+v", args, before, got)
		}
	}
}

// The refusal is narrow: anything that genuinely changes state still applies,
// including clearing a field and re-asserting the status a task already holds.
func TestUpdateAllowsEveryPatchThatChangesSomething(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A","note":"n"}]}`))
	for _, args := range []string{
		`{"id":"task-1","note":""}`,
		`{"id":"task-1","evidence":""}`,
		`{"id":"task-1","active_form":""}`,
		`{"id":"task-1","title":"B"}`,
		`{"id":"task-1","status":"pending"}`, // already pending: coherent, not malformed
		`{"id":"task-1","status":"active"}`,
	} {
		if text, isErr := Update(s, json.RawMessage(args)); isErr {
			t.Errorf("%s: should have applied, got error:\n%s", args, text)
		}
	}
}

func TestUpdateActivateNext(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"},{"title":"C"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))

	// Close task-1 and focus task-2 in one step.
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done","evidence":"shipped","activate_next":"task-2"}`))
	if isErr {
		t.Fatalf("activate_next should not error: %s", text)
	}
	if !strings.Contains(text, "Updated task-1 → done") || !strings.Contains(text, "Activated task-2 (next)") {
		t.Errorf("should report both the completion and the next activation:\n%s", text)
	}
	// Because task-2 is now active, the closing-list warning must NOT fire.
	if strings.Contains(text, "still open") {
		t.Errorf("activate_next fills the focus gap; no closing-list warning expected:\n%s", text)
	}
	got := s.List()
	if got[0].Status != tasks.StatusDone || got[1].Status != tasks.StatusActive {
		t.Errorf("task-1 should be done and task-2 active: %+v", got)
	}
}

func TestActivateNextDemotesOtherActive(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"},{"title":"C"}]}`))
	Update(s, json.RawMessage(`{"id":"task-3","status":"active"}`)) // C is active

	// Complete A (not the active one) and focus B: the one-active invariant must
	// demote C, and the result should report it.
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done","activate_next":"task-2"}`))
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "Activated task-2 (next)") || !strings.Contains(text, "Deactivated task-3 (was active)") {
		t.Errorf("activating the next task should demote the previously active one:\n%s", text)
	}
	got := s.List()
	if got[1].Status != tasks.StatusActive || got[2].Status != tasks.StatusPending {
		t.Errorf("task-2 should be active, task-3 demoted to pending: %+v", got)
	}
}

func TestActivateNextOnBlockedAndCancelled(t *testing.T) {
	// blocked: park this task (stuck) and pivot to the next.
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"blocked","evidence":"waiting on API key","activate_next":"task-2"}`))
	if isErr {
		t.Fatalf("activate_next on blocked should be allowed: %s", text)
	}
	if !strings.Contains(text, "Updated task-1 → blocked") || !strings.Contains(text, "Activated task-2 (next)") {
		t.Errorf("blocked + activate_next should park and pivot:\n%s", text)
	}
	if got := s.List(); got[0].Status != tasks.StatusBlocked || got[1].Status != tasks.StatusActive {
		t.Errorf("task-1 blocked, task-2 active expected: %+v", got)
	}

	// cancelled: abandon this task and pick up the next.
	s2 := boundStore(t)
	Create(s2, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	text, isErr = Update(s2, json.RawMessage(`{"id":"task-1","status":"cancelled","activate_next":"task-2"}`))
	if isErr {
		t.Fatalf("activate_next on cancelled should be allowed: %s", text)
	}
	if got := s2.List(); got[0].Status != tasks.StatusCancelled || got[1].Status != tasks.StatusActive {
		t.Errorf("task-1 cancelled, task-2 active expected: %+v", got)
	}
}

func TestActivateNextRejectsNonSteppingStatus(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	// Pending, active, and a status-less patch can't carry activate_next, and
	// nothing mutates.
	for _, raw := range []string{
		`{"id":"task-1","status":"pending","activate_next":"task-2"}`,
		`{"id":"task-1","status":"active","activate_next":"task-2"}`,
		`{"id":"task-1","activate_next":"task-2"}`,
	} {
		text, isErr := Update(s, json.RawMessage(raw))
		if !isErr || !strings.Contains(text, "step away from this task") {
			t.Errorf("activate_next should be rejected for %s: %s", raw, text)
		}
	}
	if got := s.List(); got[0].Status != tasks.StatusPending || got[1].Status != tasks.StatusPending {
		t.Errorf("rejected activate_next must not mutate: %+v", got)
	}
}

func TestActivateNextUnknownTargetDoesNotMutate(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done","activate_next":"task-9"}`))
	if !isErr || !strings.Contains(text, `no task with id "task-9"`) {
		t.Fatalf("unknown activate_next target should error: %s", text)
	}
	// Pre-validated before mutation: task-1 must NOT have been marked done.
	if got := s.List(); got[0].Status != tasks.StatusPending {
		t.Errorf("bad activate_next must not half-apply the done; task-1 is %q", got[0].Status)
	}
}

func TestActivateNextSameIDRejected(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"Verify config"}]}`))
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done","activate_next":"task-1"}`))
	if !isErr {
		t.Fatalf("activate_next naming the same task should error: %s", text)
	}
	// Corrective error (TW-013 F3): it states no mutation occurred and names a
	// valid pending task to activate instead — not just what was wrong.
	if !strings.Contains(text, "No state changed") {
		t.Errorf("same-id error must state no mutation occurred:\n%s", text)
	}
	if !strings.Contains(text, "task-2") {
		t.Errorf("same-id error should suggest a pending task to activate instead:\n%s", text)
	}
	if got := s.List(); got[0].Status != tasks.StatusPending {
		t.Errorf("rejected same-id activate_next must not mutate: task-1 is %q", got[0].Status)
	}
}

func TestActivateNextEmptyIsIgnored(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))
	// activate_next:"" is padding — treated as absent, so this is a plain done
	// that leaves task-2 open with nothing active: the warning (with the tip) fires.
	text, isErr := Update(s, json.RawMessage(`{"id":"task-1","status":"done","activate_next":""}`))
	if isErr {
		t.Fatalf("empty activate_next must not error: %s", text)
	}
	if !strings.Contains(text, "still open") || !strings.Contains(text, "activate_next") {
		t.Errorf("plain done leaving open work should warn and recommend activate_next:\n%s", text)
	}
}

func TestClosingWarningRecommendsActivateNext(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"active"}`))
	// Cancel leaving open work: warns AND recommends activate_next (now valid for
	// cancelled, not just done).
	text, _ := Update(s, json.RawMessage(`{"id":"task-1","status":"cancelled"}`))
	if !strings.Contains(text, "still open") || !strings.Contains(text, "activate_next") {
		t.Errorf("cancel with open work should warn and recommend activate_next:\n%s", text)
	}
}

func TestListEmptyAndPopulated(t *testing.T) {
	s := boundStore(t)
	if got, _ := List(s, nil); got != "No tasks." {
		t.Errorf("empty list: %q", got)
	}
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))
	if got, _ := List(s, nil); !strings.Contains(got, "task-1  pending  A") {
		t.Errorf("populated list: %q", got)
	}
}

func TestArchiveDefaultEmptiesAndWarns(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"done","evidence":"shipped"}`))
	// task-2 stays pending: archiving the whole board parks it.

	text, isErr := Archive(s, json.RawMessage(`{"label":"phase one"}`))
	if isErr {
		t.Fatalf("archive should not error: %s", text)
	}
	if !strings.Contains(text, "Archived generation 1 (phase one)") {
		t.Errorf("missing archive header:\n%s", text)
	}
	if !strings.Contains(text, "current list is now empty") {
		t.Errorf("default archive should empty the list:\n%s", text)
	}
	if !strings.Contains(text, "parked 1 unfinished task(s)") || !strings.Contains(text, "task-2 pending") {
		t.Errorf("should warn about the parked open task naming it:\n%s", text)
	}
	if got, _ := List(s, nil); got != "No tasks." {
		t.Errorf("current list should be empty after archive: %q", got)
	}
}

func TestArchiveKeepOpenRetainsOpen(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"B"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"done"}`))

	text, isErr := Archive(s, json.RawMessage(`{"keep_open":true}`))
	if isErr {
		t.Fatalf("archive keep_open should not error: %s", text)
	}
	if !strings.Contains(text, "Open tasks were kept") {
		t.Errorf("keep_open should keep open tasks:\n%s", text)
	}
	if strings.Contains(text, "parked") {
		t.Errorf("keep_open must not warn about parked work:\n%s", text)
	}
	got, _ := List(s, nil)
	if !strings.Contains(got, "task-2  pending  B") || strings.Contains(got, "task-1") {
		t.Errorf("keep_open should keep open task-2, drop done task-1: %q", got)
	}
}

func TestArchiveWarnsWhenParkingBlocked(t *testing.T) {
	// The gap from the dogfood session: archive-all files a blocked task off the
	// board, and that genuinely-unfinished work must be named in the warning (the
	// closing-list warning excludes blocked, but archive does not — it's leaving
	// the board with no resume).
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"Audit"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"done"}`))
	Update(s, json.RawMessage(`{"id":"task-2","status":"blocked","evidence":"no scanner available"}`))

	text, isErr := Archive(s, json.RawMessage(`{"label":"phase-2"}`))
	if isErr {
		t.Fatalf("archive should not error: %s", text)
	}
	if !strings.Contains(text, "parked 1 unfinished task(s)") || !strings.Contains(text, "task-2 blocked") {
		t.Errorf("archiving a blocked task should warn and name it:\n%s", text)
	}
	// And the summary count agrees (1 done, 1 open) — the inconsistency is gone.
	if !strings.Contains(text, "1 done, 0 cancelled, 1 open") {
		t.Errorf("summary should count the blocked task as open:\n%s", text)
	}
	// keep_open is the suggested alternative: it retains the blocked task on the board.
	s2 := boundStore(t)
	Create(s2, json.RawMessage(`{"tasks":[{"title":"A"},{"title":"Audit"}]}`))
	Update(s2, json.RawMessage(`{"id":"task-1","status":"done"}`))
	Update(s2, json.RawMessage(`{"id":"task-2","status":"blocked"}`))
	Archive(s2, json.RawMessage(`{"keep_open":true}`))
	if got, _ := List(s2, nil); !strings.Contains(got, "task-2  blocked") {
		t.Errorf("keep_open should retain the blocked task on the board: %q", got)
	}
}

func TestArchiveNoOpMessages(t *testing.T) {
	s := boundStore(t)
	// Empty board.
	if text, _ := Archive(s, nil); !strings.Contains(text, "already empty") {
		t.Errorf("archiving empty board: %s", text)
	}
	// Only open tasks + keep_open => nothing terminal to roll off.
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))
	text, _ := Archive(s, json.RawMessage(`{"keep_open":true}`))
	if !strings.Contains(text, "no done/cancelled tasks") {
		t.Errorf("keep_open with no terminal tasks should be a no-op note: %s", text)
	}
	if got, _ := List(s, nil); !strings.Contains(got, "task-1") {
		t.Errorf("no-op archive must not drop the open task: %q", got)
	}
}

func TestListArchivedAndGeneration(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"Alpha"}]}`))
	Archive(s, json.RawMessage(`{"label":"first"}`))

	idx, isErr := List(s, json.RawMessage(`{"archived":true}`))
	if isErr {
		t.Fatalf("archived list errored: %s", idx)
	}
	if !strings.Contains(idx, "gen 1") || !strings.Contains(idx, "first") || !strings.Contains(idx, "1 open") {
		t.Errorf("archive index should show gen, label, open count:\n%s", idx)
	}

	one, isErr := List(s, json.RawMessage(`{"generation":1}`))
	if isErr {
		t.Fatalf("generation read errored: %s", one)
	}
	if !strings.Contains(one, "Archived gen 1") || !strings.Contains(one, "Alpha") {
		t.Errorf("generation read should show the archived task:\n%s", one)
	}

	// Unknown generation is a clean error that names what's available.
	if text, isErr := List(s, json.RawMessage(`{"generation":99}`)); !isErr ||
		!strings.Contains(text, "no archived generation 99") || !strings.Contains(text, "Available generation(s): 1") {
		t.Errorf("unknown generation should error and list available ones: %s", text)
	}
}

// TestListMarkdown exercises the format:"markdown" worklog export (T1): the
// current list, the whole archived worklog, and one generation — plus the md
// alias and an actionable error on an unknown format.
func TestListMarkdown(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"Alpha"},{"title":"Beta"}]}`))
	Update(s, json.RawMessage(`{"id":"task-1","status":"done","evidence":"tests pass"}`))

	// Current (live) list as a checkbox worklog fragment.
	cur, isErr := List(s, json.RawMessage(`{"format":"markdown"}`))
	if isErr {
		t.Fatalf("markdown current list errored: %s", cur)
	}
	if !strings.Contains(cur, "## Tasks") || !strings.Contains(cur, "- [x] task-1 Alpha — tests pass") {
		t.Errorf("current markdown list:\n%s", cur)
	}

	// Archive (default empties the board), then the whole archive as one worklog.
	Archive(s, json.RawMessage(`{"label":"phase one"}`))
	wl, isErr := List(s, json.RawMessage(`{"archived":true,"format":"markdown"}`))
	if isErr {
		t.Fatalf("markdown worklog errored: %s", wl)
	}
	if !strings.HasPrefix(wl, "# Task worklog") || !strings.Contains(wl, "## Generation 1 — ") || !strings.Contains(wl, "phase one") {
		t.Errorf("archived worklog markdown:\n%s", wl)
	}
	if !strings.Contains(wl, "- [x] task-1 Alpha — tests pass") {
		t.Errorf("worklog should carry the archived task + evidence:\n%s", wl)
	}

	// One generation as markdown.
	if one, isErr := List(s, json.RawMessage(`{"generation":1,"format":"markdown"}`)); isErr || !strings.HasPrefix(one, "## Generation 1") {
		t.Errorf("single generation markdown (err=%v):\n%s", isErr, one)
	}

	// "md" is an accepted alias; an unknown format is a clean, actionable error.
	if _, isErr := List(s, json.RawMessage(`{"format":"md"}`)); isErr {
		t.Errorf(`format "md" should be accepted`)
	}
	if text, isErr := List(s, json.RawMessage(`{"format":"yaml"}`)); !isErr || !strings.Contains(text, "unknown format") {
		t.Errorf("unknown format should error with guidance: %s", text)
	}
}

// TestListGenerationZeroFallsThrough replays the session-log footgun: a model
// padding the call with the JSON zero value (generation:0, archived:false) wants
// the current list, not a "no archived generation 0" error loop.
func TestListGenerationZeroFallsThrough(t *testing.T) {
	s := boundStore(t)
	Create(s, json.RawMessage(`{"tasks":[{"title":"A"}]}`))

	text, isErr := List(s, json.RawMessage(`{"archived":false,"generation":0}`))
	if isErr {
		t.Fatalf("generation:0 must not error: %s", text)
	}
	if !strings.Contains(text, "task-1  pending  A") {
		t.Errorf("generation:0 should return the current list:\n%s", text)
	}
	// Negative is treated the same way.
	if text, isErr := List(s, json.RawMessage(`{"generation":-1}`)); isErr || !strings.Contains(text, "task-1") {
		t.Errorf("generation:-1 should return current list (err=%v):\n%s", isErr, text)
	}
	// generation:0 alongside archived:true still yields the index (0 is ignored).
	Archive(s, json.RawMessage(`{"label":"first"}`))
	if text, isErr := List(s, json.RawMessage(`{"archived":true,"generation":0}`)); isErr || !strings.Contains(text, "gen 1") {
		t.Errorf("archived:true with generation:0 should list the index (err=%v):\n%s", isErr, text)
	}
}

// TestListGenerationNotFoundWithNoArchives points the model at the current list
// when it asks for a generation but none exist yet.
func TestListGenerationNotFoundWithNoArchives(t *testing.T) {
	s := boundStore(t)
	text, isErr := List(s, json.RawMessage(`{"generation":1}`))
	if !isErr {
		t.Fatalf("generation lookup with no archives should error: %s", text)
	}
	if !strings.Contains(text, "no archived lists yet") || !strings.Contains(text, "no arguments") {
		t.Errorf("should steer to the no-arg current list:\n%s", text)
	}
}

// TW-037. A mutating echo restated every task's evidence body, so changing one
// row cost a multiple of the ROSTER rather than of the change. Nothing caught
// it because no test had ever asserted on the trailing render at all — the
// echo's shape was free to be anything.
func TestUpdateEchoDoesNotRestateOtherEvidence(t *testing.T) {
	s := boundStore(t)
	mustCreate(t, s, `{"tasks":[
		{"title":"Alpha"},{"title":"Beta"},{"title":"Gamma"}
	]}`)
	ids := taskIDs(t, s)
	// Give the two bystanders evidence the echo must not repeat.
	mustUpdate(t, s, `{"id":"`+ids[1]+`","status":"done","evidence":"BETA_EVIDENCE_MARKER"}`)
	mustUpdate(t, s, `{"id":"`+ids[2]+`","status":"done","evidence":"GAMMA_EVIDENCE_MARKER"}`)

	text := mustUpdate(t, s, `{"id":"`+ids[0]+`","status":"done","evidence":"ALPHA_EVIDENCE_MARKER"}`)

	if !strings.Contains(text, "ALPHA_EVIDENCE_MARKER") {
		t.Errorf("the changed task lost its own evidence:\n%s", text)
	}
	for _, marker := range []string{"BETA_EVIDENCE_MARKER", "GAMMA_EVIDENCE_MARKER"} {
		if strings.Contains(text, marker) {
			t.Errorf("echo restated an unchanged task's evidence (%s):\n%s", marker, text)
		}
	}
	// Orientation is still there: every task is named, with its status.
	for _, id := range ids {
		if !strings.Contains(text, id) {
			t.Errorf("echo dropped task %s entirely — the model can no longer see it:\n%s", id, text)
		}
	}
}

// The newly-activated task keeps its body: it is the other row the caller is
// acting on, and its note is what says what to do next.
func TestUpdateEchoKeepsTheActivatedTaskInFull(t *testing.T) {
	s := boundStore(t)
	mustCreate(t, s, `{"tasks":[
		{"title":"First"},{"title":"Second","note":"NEXT_NOTE_MARKER"},{"title":"Third","note":"THIRD_NOTE_MARKER"}
	]}`)
	ids := taskIDs(t, s)
	mustUpdate(t, s, `{"id":"`+ids[0]+`","status":"active"}`)

	text := mustUpdate(t, s, `{"id":"`+ids[0]+`","status":"done","evidence":"did it","activate_next":"`+ids[1]+`"}`)

	if !strings.Contains(text, "NEXT_NOTE_MARKER") {
		t.Errorf("the activated task's note is missing — the model cannot see what is next:\n%s", text)
	}
	if strings.Contains(text, "THIRD_NOTE_MARKER") {
		t.Errorf("an untouched task's note was restated:\n%s", text)
	}
}

// The acceptance criterion itself: the echo must scale with the CHANGE, not
// with the roster. Ten extra tasks carrying maximum-length evidence may add
// only their status lines.
func TestUpdateEchoScalesWithTheChangeNotTheRoster(t *testing.T) {
	long := strings.Repeat("x", 300)

	echoWith := func(extra int) int {
		s := boundStore(t)
		spec := `{"tasks":[{"title":"Target"}`
		for i := 0; i < extra; i++ {
			spec += `,{"title":"Filler"}`
		}
		spec += `]}`
		mustCreate(t, s, spec)
		ids := taskIDs(t, s)
		for _, id := range ids[1:] {
			mustUpdate(t, s, `{"id":"`+id+`","status":"done","evidence":"`+long+`"}`)
		}
		return len(mustUpdate(t, s, `{"id":"`+ids[0]+`","status":"done","evidence":"done it"}`))
	}

	small, large := echoWith(2), echoWith(12)
	// 10 more rows, each carrying 300 chars of evidence. A status line is well
	// under 60 bytes; restating the bodies would be 3000+.
	if grew := large - small; grew > 10*60 {
		t.Errorf("echo grew %d bytes for 10 extra tasks — it is still scaling with the roster", grew)
	}
}

// task_list is the documented way to get everything, and trimming the echo is
// only defensible while that stays true.
func TestListStillRendersEveryEvidenceBody(t *testing.T) {
	s := boundStore(t)
	mustCreate(t, s, `{"tasks":[{"title":"Alpha"},{"title":"Beta"}]}`)
	ids := taskIDs(t, s)
	mustUpdate(t, s, `{"id":"`+ids[1]+`","status":"done","evidence":"BETA_EVIDENCE_MARKER"}`)

	text, isErr := List(s, json.RawMessage(`{}`))
	if isErr {
		t.Fatalf("task_list failed: %s", text)
	}
	if !strings.Contains(text, "BETA_EVIDENCE_MARKER") {
		t.Errorf("task_list stopped returning evidence — the escape hatch is gone:\n%s", text)
	}
}

func mustCreate(t *testing.T, s *tasks.Store, args string) string {
	t.Helper()
	text, isErr := Create(s, json.RawMessage(args))
	if isErr {
		t.Fatalf("create: %s", text)
	}
	return text
}

func mustUpdate(t *testing.T, s *tasks.Store, args string) string {
	t.Helper()
	text, isErr := Update(s, json.RawMessage(args))
	if isErr {
		t.Fatalf("update: %s", text)
	}
	return text
}

func taskIDs(t *testing.T, s *tasks.Store) []string {
	t.Helper()
	list := s.List()
	ids := make([]string, 0, len(list))
	for _, task := range list {
		ids = append(ids, task.ID)
	}
	return ids
}
