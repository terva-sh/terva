package modes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// staticSnapshots returns a deterministic snapshot slice for the
// dialog to render.
func staticSnapshots(rows ...swarm.AgentSnapshot) func() []swarm.AgentSnapshot {
	return func() []swarm.AgentSnapshot {
		out := make([]swarm.AgentSnapshot, len(rows))
		copy(out, rows)
		return out
	}
}

func TestSwarmDialogEmptyState(t *testing.T) {
	d := newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil, nil, nil, nil, "")
	lines := d.Render(tui.Theme{}, 80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "no agents") {
		t.Fatalf("empty state missing hint:\n%s", joined)
	}
	if !strings.Contains(joined, "press n") {
		t.Fatalf("empty state should advertise n shortcut:\n%s", joined)
	}
}

func TestSwarmDialogRendersRows(t *testing.T) {
	now := time.Now()
	rows := []swarm.AgentSnapshot{
		{ID: "alpha-1", Task: "fix login", Status: swarm.StatusRunning, Activity: "editing file", Started: now},
		{ID: "beta-2", Task: "write tests", Status: swarm.StatusDone, Activity: "done", Started: now},
	}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	out := strings.Join(d.Render(tui.Theme{}, 100), "\n")
	for _, want := range []string{"alpha-1", "beta-2", "editing file", "done", "STATUS", "ACTIVITY"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestSwarmDialogStopAndRemoveCallbacks(t *testing.T) {
	now := time.Now()
	rows := []swarm.AgentSnapshot{
		{ID: "alpha-1", Status: swarm.StatusRunning, Started: now},
	}
	stops := 0
	removes := 0
	d := newSwarmDialog()
	d.Open(
		staticSnapshots(rows...),
		func(id string) error { stops++; return nil },
		func(id string) error { removes++; return nil },
		nil,
		nil,
		nil,
		"",
	)
	// Render once so cursor + rows are populated.
	_ = d.Render(tui.Theme{}, 80)

	_, msg, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'k'})
	if stops != 1 {
		t.Fatalf("stop calls = %d", stops)
	}
	if !strings.Contains(msg, "alpha-1") || errMsg != "" {
		t.Fatalf("stop status (%q, %q)", msg, errMsg)
	}

	_, msg, errMsg = d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'})
	if removes != 1 {
		t.Fatalf("remove calls = %d", removes)
	}
	if !strings.Contains(msg, "alpha-1") || errMsg != "" {
		t.Fatalf("remove status (%q, %q)", msg, errMsg)
	}
}

func TestSwarmDialogEnterShowsTranscript(t *testing.T) {
	now := time.Now()
	rows := []swarm.AgentSnapshot{
		{ID: "alpha-1", Task: "do stuff", Status: swarm.StatusDone, Tail: "line a\nline b", Started: now},
	}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	for _, want := range []string{"task:", "dir:", "line a", "line b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript view missing %q:\n%s", want, out)
		}
	}
	// Esc should bring us back to the list view.
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	out = strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "STATUS") {
		t.Fatalf("did not return to list view:\n%s", out)
	}
}

func TestSwarmDialogEscClosesList(t *testing.T) {
	d := newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil, nil, nil, nil, "")
	closed, _, _ := d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if !closed {
		t.Fatal("esc did not close")
	}
	if d.Active() {
		t.Fatal("still active after esc")
	}
}

// TestSwarmDialogSpawnFlow drives the inline n-prompt end-to-end:
// press n, type a task, press enter, observe the spawn callback
// fires with the right text and the prompt closes.
func TestSwarmDialogSpawnFlow(t *testing.T) {
	var spawned string
	d := newSwarmDialog()
	d.Open(
		staticSnapshots(),
		nil, nil,
		func(task, _, _ string) error { spawned = task; return nil },
		nil,
		nil,
		"",
	)
	_ = d.Render(tui.Theme{}, 80)

	// Press n to open the spawn editor directly. The model is
	// taken from whatever the host set via SetCurrentModel; this
	// test doesn't pin one.
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if !d.spawning {
		t.Fatal("n did not enter spawning mode")
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "enter spawn") || !strings.Contains(out, "@ file/dir picker") {
		t.Fatalf("spawn editor hint not rendered:\n%s", out)
	}

	// Type "fix x".
	for _, r := range "fix x" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	// Submit.
	_, msg, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if msg != "spawned" {
		t.Fatalf("status msg = %q", msg)
	}
	if spawned != "fix x" {
		t.Fatalf("spawn callback got %q", spawned)
	}
	if d.spawning {
		t.Fatal("prompt did not close after enter")
	}
}

func TestSwarmDialogSpawnFilePickerFlow(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	var spawned string
	d := newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil, func(task, _, _ string) error { spawned = task; return nil }, nil, nil, cwd)
	_ = d.Render(tui.Theme{}, 80)

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	for _, r := range "summarize @READ" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "README.md") {
		t.Fatalf("@ file picker did not render match:\n%s", out)
	}

	// Enter while the file picker is active selects the highlighted file,
	// inserting the compact [file:] chip instead of submitting yet.
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if d.newTaskEd == nil || !strings.Contains(d.newTaskEd.Value(), "[file:README.md]") {
		t.Fatalf("file picker did not insert chip; editor=%v", d.newTaskEd)
	}

	// A second Enter submits. The chip should expand to the absolute
	// path before the task reaches Swarm.Spawn.
	_, msg, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if msg != "spawned" {
		t.Fatalf("status msg = %q", msg)
	}
	wantPath := filepath.Join(cwd, "README.md")
	if !strings.Contains(spawned, wantPath) {
		t.Fatalf("spawned task %q missing expanded path %q", spawned, wantPath)
	}
}

func TestSwarmDialogSpawnEscCancels(t *testing.T) {
	called := false
	d := newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil, func(string, string, string) error { called = true; return nil }, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	for _, r := range "abc" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if called {
		t.Fatal("spawn callback ran despite esc")
	}
	if d.spawning {
		t.Fatal("prompt still open after esc")
	}
	if d.Active() == false {
		t.Fatal("esc on spawn prompt should not close the whole dialog")
	}
}

func TestSwarmDialogNoNWithoutSpawnCallback(t *testing.T) {
	d := newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil, nil, nil, nil, "") // spawn callback nil
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if d.spawning {
		t.Fatal("n opened spawn prompt without a spawn callback wired")
	}
}

// TestSwarmDialogFullTranscriptRendersEverything pins the /btw-style
// rendering: the dialog NEVER clips the transcript into a fixed
// window with internal scroll. Every line goes into the rendered
// output and the host's outer scrollback owns overflow. This is
// what makes the transcript grow with the window and with the
// content, instead of cutting off at row 16 with a "↑ N more above"
// indicator.
func TestSwarmDialogFullTranscriptRendersEverything(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	rows := []swarm.AgentSnapshot{{
		ID: "alpha-1", Task: "x", Status: swarm.StatusDone,
		Lines: lines, Started: time.Now(),
	}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // open viewer

	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	// First line must be present (no internal clipping from the top).
	if !strings.Contains(out, "line 0") {
		t.Errorf("first transcript line missing — the view appears to be clipping:\n%s", out)
	}
	// Last line must be present (no internal clipping from the bottom).
	if !strings.Contains(out, fmt.Sprintf("line %d", len(lines)-1)) {
		t.Errorf("last transcript line missing — the view appears to be clipping:\n%s", out)
	}
	// The old "↑ N more above" / "↓ N more below" cliff markers
	// must NOT appear; those belonged to the old viewport model.
	if strings.Contains(out, "more above") || strings.Contains(out, "more below") {
		t.Errorf("transcript still uses internal scroll markers:\n%s", out)
	}

	// Esc returns to the list view.
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if d.viewing {
		t.Fatal("esc did not exit viewer")
	}
}

// TestSwarmDialogOpenViewingJumpsToAgent regression-tests /swarm logs
// <id>: OpenViewing should select the row whose id matches the
// (possibly truncated) argument and enter the transcript view
// directly without showing the list first.
func TestSwarmDialogOpenViewingJumpsToAgent(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "alpha-1", Lines: []string{"hi from alpha"}, Started: time.Now()},
		{ID: "beta-2", Lines: []string{"hi from beta"}, Started: time.Now()},
	}
	d := newSwarmDialog()
	ok := d.OpenViewing("beta", staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	if !ok {
		t.Fatal("prefix match did not resolve")
	}
	if !d.viewing || d.cursor != 1 {
		t.Fatalf("viewing=%v cursor=%d", d.viewing, d.cursor)
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "hi from beta") {
		t.Fatalf("wrong agent shown:\n%s", out)
	}
}

func TestSwarmDialogOpenViewingUnknownIDReturnsFalse(t *testing.T) {
	d := newSwarmDialog()
	ok := d.OpenViewing("nope", staticSnapshots(), nil, nil, nil, nil, nil, "")
	if ok {
		t.Fatal("expected miss for unknown id")
	}
}

// TestSwarmDialogPromptFlowFromList drives the inline p-prompt:
// press p on the selected agent, type a message, press enter,
// and assert the send callback fired with the right id+text.
func TestSwarmDialogPromptFlowFromList(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "alpha-1", Status: swarm.StatusRunning, Started: time.Now()},
	}
	var gotID, gotText string
	sends := 0
	d := newSwarmDialog()
	d.Open(
		staticSnapshots(rows...),
		nil, nil, nil,
		func(id, text string) error { sends++; gotID = id; gotText = text; return nil },
		nil,
		"",
	)
	_ = d.Render(tui.Theme{}, 80)

	// Press 'p' to open the inline prompt for alpha-1.
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if !d.prompting {
		t.Fatal("p did not enter prompting mode")
	}
	if d.promptTargetID != "alpha-1" {
		t.Fatalf("prompt target = %q; want alpha-1", d.promptTargetID)
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "send to alpha-1:") || !strings.Contains(out, "enter send") {
		t.Fatalf("prompt editor hint not rendered:\n%s", out)
	}

	// Type "do the thing" and submit.
	for _, r := range "do the thing" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	_, msg, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if msg != "sent to alpha-1" {
		t.Fatalf("status msg = %q", msg)
	}
	if sends != 1 || gotID != "alpha-1" || gotText != "do the thing" {
		t.Fatalf("send callback got id=%q text=%q (calls=%d)", gotID, gotText, sends)
	}
	if d.prompting {
		t.Fatal("prompt did not close after enter")
	}
}

// TestSwarmDialogPromptFromTranscriptView pins the always-on editor
// behaviour for the transcript view: entering an agent immediately
// auto-opens the inline composer (no `p` needed), typing flows
// straight into it, Enter sends, and the metadata stays visible.
// The transcript view is /btw-style now — you never leave the
// view to send a follow-up.
func TestSwarmDialogPromptFromTranscriptView(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "beta-2", Task: "do thing", Status: swarm.StatusRunning, Started: time.Now(), Tail: "hi from beta"},
	}
	var gotID, gotText string
	d := newSwarmDialog()
	d.Open(
		staticSnapshots(rows...),
		nil, nil, nil,
		func(id, text string) error { gotID = id; gotText = text; return nil },
		nil,
		"",
	)
	// Enter the transcript view; for a running agent the editor
	// must auto-open right away.
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.viewing {
		t.Fatal("enter did not open transcript view")
	}
	if !d.prompting {
		t.Fatal("transcript view did not auto-open inline editor for running agent")
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "task:   do thing") {
		t.Fatalf("transcript metadata not visible:\n%s", out)
	}
	if !strings.Contains(out, "swarm: beta-2") {
		t.Fatalf("frame header missing agent id:\n%s", out)
	}

	// Typing flows straight into the editor — no `p` modal step.
	for _, r := range "keep going" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	_, msg, _ := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if msg != "sent to beta-2" || gotID != "beta-2" || gotText != "keep going" {
		t.Fatalf("send didn't fire correctly: msg=%q id=%q text=%q", msg, gotID, gotText)
	}

	// Editor must stay mounted after submit (/btw-style), with an
	// empty buffer ready for the next message.
	if !d.prompting {
		t.Error("editor closed after submit; /btw-style transcript view should keep it mounted")
	}
	if !d.viewing {
		t.Error("transcript view collapsed after submit; should stay open")
	}
	if d.promptEd != nil && d.promptEd.Value() != "" {
		t.Errorf("editor buffer not cleared after submit: %q", d.promptEd.Value())
	}
}

// TestSwarmDialogPromptEscCancels makes sure Esc inside the prompt
// editor aborts without invoking the send callback.
func TestSwarmDialogPromptEscCancels(t *testing.T) {
	rows := []swarm.AgentSnapshot{{ID: "gamma-3", Status: swarm.StatusRunning, Started: time.Now()}}
	sends := 0
	d := newSwarmDialog()
	d.Open(
		staticSnapshots(rows...),
		nil, nil, nil,
		func(id, text string) error { sends++; return nil },
		nil,
		"",
	)
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	for _, r := range "oops" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if d.prompting {
		t.Fatal("esc did not close prompt editor")
	}
	if sends != 0 {
		t.Fatalf("send fired despite esc: calls=%d", sends)
	}
}

// TestSwarmDialogPWithoutSendCallback verifies that pressing 'p'
// when no send callback is wired is a silent no-op (mirrors the
// existing 'n' / no-spawn-callback contract).
func TestSwarmDialogPWithoutSendCallback(t *testing.T) {
	rows := []swarm.AgentSnapshot{{ID: "delta-4", Status: swarm.StatusRunning, Started: time.Now()}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if d.prompting {
		t.Fatal("p opened prompt editor without a send callback")
	}
}

// TestOpenForResumeParksCursorOnFirstResumable verifies the picker
// entry point: with a mix of running and detached agents, the
// dialog opens with the cursor on the first detached row (skipping
// any leading running ones) so the user can press R immediately.
func TestOpenForResumeParksCursorOnFirstResumable(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "live-1", Status: swarm.StatusRunning, Started: time.Now()},
		{ID: "live-2", Status: swarm.StatusRunning, Started: time.Now()},
		{ID: "detached-1", Status: swarm.StatusDetached, Started: time.Now()},
		{ID: "detached-2", Status: swarm.StatusKilled, Started: time.Now()},
	}
	d := newSwarmDialog()
	count := d.OpenForResume(
		staticSnapshots(rows...),
		nil, nil, nil, nil,
		func(id string) error { return nil },
		"",
	)
	if count != 2 {
		t.Fatalf("resumable count = %d; want 2", count)
	}
	if d.cursor != 2 {
		t.Fatalf("cursor = %d; want 2 (first resumable row)", d.cursor)
	}
	if sel := d.selected(); sel == nil || sel.ID != "detached-1" {
		t.Fatalf("selected = %+v; want detached-1", sel)
	}
}

// TestOpenForResumeWithNoResumableLeavesDialogActive ensures the
// picker still opens the dashboard when nothing is resumable, so the
// user sees what's there instead of getting silently nothing. The
// slash handler is responsible for surfacing the "no resumable
// agents" status string — the dialog itself just opens.
func TestOpenForResumeWithNoResumableLeavesDialogActive(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "live-1", Status: swarm.StatusRunning, Started: time.Now()},
	}
	d := newSwarmDialog()
	count := d.OpenForResume(
		staticSnapshots(rows...),
		nil, nil, nil, nil,
		func(id string) error { return nil },
		"",
	)
	if count != 0 {
		t.Fatalf("resumable count = %d; want 0", count)
	}
	if !d.Active() {
		t.Fatal("dialog should remain active so the user sees the empty state")
	}
	if d.cursor != 0 {
		t.Fatalf("cursor = %d; want 0 (no parking)", d.cursor)
	}
}

// TestOpenForResumeREnterTriggersResume drives the full picker UX:
// /swarm resume → dialog opens parked on a resumable row → user
// presses R → the resume callback fires for that exact id.
func TestOpenForResumeREnterTriggersResume(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "live-1", Status: swarm.StatusRunning, Started: time.Now()},
		{ID: "detached-1", Status: swarm.StatusDetached, Started: time.Now()},
	}
	var resumed string
	d := newSwarmDialog()
	d.OpenForResume(
		staticSnapshots(rows...),
		nil, nil, nil, nil,
		func(id string) error { resumed = id; return nil },
		"",
	)
	_ = d.Render(tui.Theme{}, 80)
	_, msg, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'R'})
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if resumed != "detached-1" {
		t.Fatalf("resume callback got %q; want detached-1", resumed)
	}
	if msg != "resumed detached-1" {
		t.Fatalf("status msg = %q", msg)
	}
}

// TestNeedsTickRefresh enumerates the four dialog states so the host's
// animation tick wakes the renderer for live updates but stays out of
// the way of the inline editors (which need the cursor blink).
func TestNeedsTickRefresh(t *testing.T) {
	// Closed.
	d := newSwarmDialog()
	if d.NeedsTickRefresh() {
		t.Fatal("closed dialog should not request tick refresh")
	}

	// List view (open, no editors): tick should refresh so rows update.
	d.Open(staticSnapshots(), nil, nil, nil, nil, nil, "")
	if !d.NeedsTickRefresh() {
		t.Fatal("open list view should request tick refresh")
	}

	// Transcript view: still background-driven; tick should refresh.
	rows := []swarm.AgentSnapshot{{ID: "a", Status: swarm.StatusRunning, Started: time.Now()}}
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.viewing {
		t.Fatal("setup: enter did not open transcript view")
	}
	if !d.NeedsTickRefresh() {
		t.Fatal("transcript view should request tick refresh")
	}

	// Spawn editor open: tick should NOT refresh (cursor blink).
	d = newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil, func(string, string, string) error { return nil }, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if !d.spawning {
		t.Fatal("setup: n did not enter spawning mode")
	}
	if d.NeedsTickRefresh() {
		t.Fatal("spawn editor should suppress tick refresh")
	}

	// Prompt editor open: same suppression.
	d = newSwarmDialog()
	rows = []swarm.AgentSnapshot{{ID: "a", Status: swarm.StatusRunning, Started: time.Now()}}
	d.Open(staticSnapshots(rows...), nil, nil, nil, func(string, string) error { return nil }, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if !d.prompting {
		t.Fatal("setup: p did not enter prompting mode")
	}
	if d.NeedsTickRefresh() {
		t.Fatal("prompt editor should suppress tick refresh")
	}
}

// TestCursorPosPromptEditor verifies the dialog reports a real
// caret row for the prompt editor in both list-view and
// transcript-view contexts. Without this, the terminal cursor
// stayed at the (now-hidden) main editor row when the user
// pressed 'p', so the visible caret looked wrong.
func TestCursorPosPromptEditor(t *testing.T) {
	rows := []swarm.AgentSnapshot{
		{ID: "a", Task: "t", Status: swarm.StatusRunning, Started: time.Now()},
	}

	// From the list view.
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil }, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if !d.prompting {
		t.Fatal("setup: p did not enter prompting from list view")
	}
	row, _ := d.CursorPos(80)
	if row < 1 {
		t.Fatalf("list-view prompt CursorPos row = %d; want >= 1", row)
	}

	// From the transcript view (deeper offset due to metadata block).
	d = newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil }, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // open transcript
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if !d.prompting || !d.viewing {
		t.Fatalf("setup: prompting=%v viewing=%v", d.prompting, d.viewing)
	}
	rowT, _ := d.CursorPos(80)
	if rowT <= row {
		t.Fatalf("transcript-view prompt row = %d; should be deeper than list row %d", rowT, row)
	}
}

func TestCursorPosSpawnEditorWithInheritedModelMatchesPaddedRender(t *testing.T) {
	d := newSwarmDialog()
	d.SetCurrentModel("claude-sonnet-4-5", "anthropic")
	d.Open(staticSnapshots(), nil, nil, func(task, model, provider string) error { return nil }, nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if !d.spawning {
		t.Fatal("setup: n did not enter spawning mode")
	}

	row, _ := d.CursorPos(80)
	lines := padDialogFrame(d.Render(tui.Theme{}, 80))
	if row < 0 || row >= len(lines) {
		t.Fatalf("spawn CursorPos row = %d outside rendered lines %d", row, len(lines))
	}
	if got := stripANSIBytes(lines[row]); !strings.Contains(got, "▌") {
		t.Fatalf("spawn CursorPos row %d = %q; want editor prompt row", row, got)
	}
}

// TestSwarmDialogPOnNonRunningAgentShowsHint covers the regression
// caught by the user's screenshot: pressing 'p' on a detached agent
// dialled a dead unix socket and printed the raw path as an error.
// We now block the prompt editor for any non-running status and
// surface a clear status-bar hint instead.
func TestSwarmDialogPOnNonRunningAgentShowsHint(t *testing.T) {
	for _, st := range []swarm.Status{
		swarm.StatusDetached, swarm.StatusDone, swarm.StatusFailed, swarm.StatusKilled,
	} {
		t.Run(string(st), func(t *testing.T) {
			rows := []swarm.AgentSnapshot{{ID: "a", Status: st, Started: time.Now()}}
			sends := 0
			d := newSwarmDialog()
			d.Open(staticSnapshots(rows...), nil, nil, nil,
				func(string, string) error { sends++; return nil }, nil, "")
			_ = d.Render(tui.Theme{}, 80)

			_, msg, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
			if d.prompting {
				t.Fatal("prompt editor opened on non-running agent")
			}
			if sends != 0 {
				t.Fatal("send callback fired without an open editor")
			}
			if msg != "" {
				t.Errorf("unexpected ok msg: %q", msg)
			}
			if !strings.Contains(errMsg, string(st)) {
				t.Errorf("hint = %q; should mention status %q", errMsg, st)
			}
			if !strings.Contains(errMsg, "resume") && st != swarm.StatusPending && st != swarm.StatusRunning {
				t.Errorf("hint = %q; should mention resume for resumable state", errMsg)
			}
		})
	}
}

// TestSwarmDialogPOnRunningAgentOpensEditor is the positive
// complement of the previous test: running agents still open the
// editor normally.
func TestSwarmDialogPOnRunningAgentOpensEditor(t *testing.T) {
	rows := []swarm.AgentSnapshot{{ID: "a", Status: swarm.StatusRunning, Started: time.Now()}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil }, nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if !d.prompting {
		t.Fatal("prompt editor did not open on running agent")
	}
}

// TestFriendlySendErrRewritesNotReady asserts ErrNotReady becomes the
// user-friendly "isn't accepting input" hint, and unknown errors
// fall through verbatim.
func TestFriendlySendErrRewritesNotReady(t *testing.T) {
	hint := friendlySendErr("alpha-1", swarm.ErrNotReady)
	if !strings.Contains(hint, "alpha-1") || !strings.Contains(hint, "resume") {
		t.Errorf("friendlySendErr(ErrNotReady) = %q", hint)
	}
	if strings.Contains(hint, "swarm:") {
		t.Errorf("friendlySendErr leaked raw error text: %q", hint)
	}

	other := errors.New("some other failure")
	got := friendlySendErr("alpha-1", other)
	if got != "send: some other failure" {
		t.Errorf("friendlySendErr passthrough = %q", got)
	}
}

// TestRenderSwarmTranscriptBlocks_GroupsByRole asserts the flat
// transcript log is regrouped into user / assistant / stderr /
// error blocks before rendering. Each output row should be styled
// for its role rather than printed verbatim.
func TestRenderSwarmTranscriptBlocks_GroupsByRole(t *testing.T) {
	lines := []string{
		"user: hello",
		"hi there",
		"second assistant line",
		"stderr: warning thing",
		"error: boom",
	}
	out := renderSwarmTranscriptBlocks(lines, tui.Theme{}, 80)
	joined := strings.Join(out, "\n")

	// The user echo should appear as a bubble row, which contains
	// the "▌ " left-bar glyph.
	if !strings.Contains(joined, "▌ ") {
		t.Errorf("user echo did not render as a bubble row:\n%s", joined)
	}
	// The user prefix should be stripped from the rendered output.
	if strings.Contains(joined, "user: hello") {
		t.Errorf("user prefix leaked into rendered output")
	}
	// Assistant content survives.
	if !strings.Contains(joined, "hi there") {
		t.Errorf("assistant text missing from rendered output")
	}
	// Stderr line is prefixed with stderr marker (we keep "stderr"
	// word in the rendered output for clarity).
	if !strings.Contains(joined, "stderr") || !strings.Contains(joined, "warning thing") {
		t.Errorf("stderr block missing:\n%s", joined)
	}
	// Error line gets the ✖ marker.
	if !strings.Contains(joined, "✖ boom") {
		t.Errorf("error block missing ✖ marker:\n%s", joined)
	}
}

// TestRenderSwarmTranscriptBlocks_MarkdownStyling proves the
// assistant text goes through tui.RenderMarkdown: a literal **bold**
// pair should become an ANSI bold sequence and bullet "-" should
// become "•".
func TestRenderSwarmTranscriptBlocks_MarkdownStyling(t *testing.T) {
	lines := []string{
		"Here is a list:",
		"- **bold** item",
		"- plain item",
	}
	out := renderSwarmTranscriptBlocks(lines, tui.Theme{}, 80)
	joined := strings.Join(out, "\n")

	// ANSI bold on/off. Markdown rendering uses CSI 1 / 22.
	if !strings.Contains(joined, "\x1b[1m") || !strings.Contains(joined, "\x1b[22m") {
		t.Errorf("expected ANSI bold sequences:\n%q", joined)
	}
	// Bullet glyph upgrade.
	if !strings.Contains(joined, "• ") {
		t.Errorf("expected • bullet:\n%s", joined)
	}
	// Literal "**bold**" markdown source must NOT leak through.
	if strings.Contains(joined, "**bold**") {
		t.Errorf("unrendered markdown leaked through:\n%s", joined)
	}
}

// TestRenderSwarmTranscriptBlocks_GroupsAdjacentAssistantLines
// guards the contract that consecutive assistant lines form one
// markdown block. If we render each line in isolation, fenced code
// (and other multi-line constructs) would never close.
func TestRenderSwarmTranscriptBlocks_GroupsAdjacentAssistantLines(t *testing.T) {
	lines := []string{
		"Some prose.",
		"```go",
		"package main",
		"",
		"func main() {}",
		"```",
		"Done.",
	}
	out := renderSwarmTranscriptBlocks(lines, tui.Theme{}, 80)
	joined := strings.Join(out, "\n")

	// The literal fence markers should be consumed by the markdown
	// renderer; if we rendered each line on its own the ``` would
	// leak as text.
	if strings.Contains(joined, "```") {
		t.Errorf("fence markers leaked — lines weren't grouped:\n%s", joined)
	}
	// The code content should still be in the output — strip ANSI
	// before checking because syntax highlighting wraps each token.
	plain := stripANSIBytes(joined)
	if !strings.Contains(plain, "package main") || !strings.Contains(plain, "func main") {
		t.Errorf("code block content missing:\nansi=%q\nplain=%q", joined, plain)
	}
}

// TestSwarmDialogKOnNonRunningAgentShowsHint mirrors the prompt-
// disabled test: pressing 'k' on an already-terminal or detached
// agent now surfaces a hint instead of routing to Swarm.Stop, which
// is a no-op for those states (and used to segfault for detached
// agents before the cancel-nil guard).
func TestSwarmDialogKOnNonRunningAgentShowsHint(t *testing.T) {
	for _, st := range []swarm.Status{
		swarm.StatusDetached, swarm.StatusDone, swarm.StatusFailed, swarm.StatusKilled,
	} {
		t.Run(string(st), func(t *testing.T) {
			rows := []swarm.AgentSnapshot{{ID: "a", Status: st, Started: time.Now()}}
			stops := 0
			d := newSwarmDialog()
			d.Open(staticSnapshots(rows...),
				func(string) error { stops++; return nil },
				nil, nil, nil, nil, "")
			_ = d.Render(tui.Theme{}, 80)
			_, _, errMsg := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'k'})
			if stops != 0 {
				t.Fatalf("stop callback fired on %s agent", st)
			}
			if !strings.Contains(errMsg, string(st)) || !strings.Contains(errMsg, "remove") {
				t.Errorf("hint = %q; should mention %q and suggest remove", errMsg, st)
			}
		})
	}
}

// TestSwarmDialogSpawnFlow_CurrentModelInherited proves the spawn
// callback receives whatever model the host pinned via
// SetCurrentModel before opening the dialog. This is how /model is
// surfaced to swarm agents: the user picks via /model first, then
// /swarm n, and the new agent inherits.
func TestSwarmDialogSpawnFlow_CurrentModelInherited(t *testing.T) {
	var gotTask, gotModel, gotProv string
	d := newSwarmDialog()
	d.SetCurrentModel("claude-sonnet-4-5", "anthropic")
	d.Open(staticSnapshots(), nil, nil,
		func(task, model, prov string) error {
			gotTask, gotModel, gotProv = task, model, prov
			return nil
		},
		nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)

	// `n` goes straight to the spawn editor; no picker step.
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if !d.spawning {
		t.Fatal("n did not enter spawning mode")
	}

	// The spawn editor advertises the inherited model so the user
	// knows what they're about to spawn against.
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "model: claude-sonnet-4-5") {
		t.Errorf("spawn editor missing model banner:\n%s", out)
	}

	for _, r := range "do thing" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	_, msg, _ := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if gotTask != "do thing" || gotModel != "claude-sonnet-4-5" || gotProv != "anthropic" {
		t.Fatalf("spawn args = (%q,%q,%q); want (\"do thing\",\"claude-sonnet-4-5\",\"anthropic\")",
			gotTask, gotModel, gotProv)
	}
	if !strings.Contains(msg, "claude-sonnet-4-5") {
		t.Errorf("status msg = %q; should mention inherited model", msg)
	}
}

// TestSwarmDialogSpawnEditor_SlashModelOpensPicker proves the in-
// editor /model command pops the same model dialog the global
// /model uses, that picking a model captures it as the pending
// override, and that the spawn editor reopens with any previously-
// typed task text intact so the user doesn't lose work.
func TestSwarmDialogSpawnEditor_SlashModelOpensPicker(t *testing.T) {
	var gotTask, gotModel, gotProv string
	d := newSwarmDialog()
	d.SetCurrentModel("old-model", "old-prov")
	d.Open(staticSnapshots(), nil, nil,
		func(task, model, prov string) error {
			gotTask, gotModel, gotProv = task, model, prov
			return nil
		},
		nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)

	// Open spawn editor and type some task text plus a /model line.
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if !d.spawning {
		t.Fatal("n did not enter spawning mode")
	}
	for _, r := range "refactor X" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	// Newline so the editor has a two-line buffer when we add /model.
	d.newTaskEd.SetValue("refactor X\n/model")
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if !d.pickingModel {
		t.Fatalf("/model submit did not open the picker (spawning=%v picking=%v)", d.spawning, d.pickingModel)
	}
	if d.spawning {
		t.Fatal("spawn editor still marked open while picker is up")
	}
	if d.spawnDraft != "refactor X" {
		t.Fatalf("spawnDraft = %q; want %q (the /model line should be stripped, the task kept)", d.spawnDraft, "refactor X")
	}

	// Picker render must take over the dialog frame.
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "select model for next spawn") {
		t.Errorf("picker render hint missing:\n%s", out)
	}

	// Inject a fake selection into the embedded modelDialog and confirm.
	d.modelPicker.all = []provider.Model{{Provider: "acme", ID: "fake-1"}}
	d.modelPicker.view = d.modelPicker.all
	d.modelPicker.cursor = 0
	_, msg, _ := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if d.pickingModel {
		t.Fatal("picker did not close on select")
	}
	if !d.spawning {
		t.Fatal("spawn editor did not reopen after picker select")
	}
	if d.pendingModel != "fake-1" || d.pendingProvider != "acme" {
		t.Errorf("pending = (%q,%q); want (fake-1, acme)", d.pendingModel, d.pendingProvider)
	}
	if !strings.Contains(msg, "fake-1") {
		t.Errorf("status msg = %q; should mention picked model", msg)
	}
	if got := d.newTaskEd.Value(); got != "refactor X" {
		t.Errorf("reopened editor buffer = %q; want preserved task %q", got, "refactor X")
	}

	// Now submit the task and confirm the spawn callback gets the
	// new model.
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if gotTask != "refactor X" || gotModel != "fake-1" || gotProv != "acme" {
		t.Errorf("spawn args = (%q,%q,%q); want (refactor X, fake-1, acme)", gotTask, gotModel, gotProv)
	}
}

// TestSwarmDialogSpawnEditor_SlashModelEscRestoresDraft confirms
// pressing Esc inside the picker (no selection) reopens the spawn
// editor with the previously-typed text intact AND leaves the
// pending model untouched, so the user can bail out cleanly.
func TestSwarmDialogSpawnEditor_SlashModelEscRestoresDraft(t *testing.T) {
	d := newSwarmDialog()
	d.SetCurrentModel("keep-me", "keep-prov")
	d.Open(staticSnapshots(), nil, nil,
		func(string, string, string) error { return nil },
		nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	d.newTaskEd.SetValue("task here\n/model")
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.pickingModel {
		t.Fatal("/model did not open the picker")
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if d.pickingModel {
		t.Fatal("esc did not close the picker")
	}
	if !d.spawning {
		t.Fatal("spawn editor did not reopen after picker cancel")
	}
	if d.pendingModel != "keep-me" || d.pendingProvider != "keep-prov" {
		t.Errorf("pending changed on Esc: (%q,%q); want (keep-me, keep-prov)", d.pendingModel, d.pendingProvider)
	}
	if got := d.newTaskEd.Value(); got != "task here" {
		t.Errorf("reopened editor buffer = %q; want %q", got, "task here")
	}
}

// TestStripSlashModelLine pins the helper's behaviour: drop any
// line whose trimmed content is "/model" or starts with "/model ",
// preserve everything else verbatim.
func TestStripSlashModelLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/model", ""},
		{"  /model  ", ""},
		{"foo\n/model", "foo"},
		{"/model\nfoo", "foo"},
		{"foo\n/model\nbar", "foo\nbar"},
		{"/model gpt-5", ""},
		{"a /model in prose", "a /model in prose"}, // not first non-blank token → keep
		{"", ""},
		{"plain task", "plain task"},
	}
	for _, c := range cases {
		if got := stripSlashModelLine(c.in); got != c.want {
			t.Errorf("stripSlashModelLine(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestSwarmDialogSpawnFlow_NoModelInheritsChildDefault confirms that
// when the host doesn't pin a model (SetCurrentModel never called or
// called with empty strings), the spawn callback receives empty
// model/provider and the child subprocess resolves its own default.
func TestSwarmDialogSpawnFlow_NoModelInheritsChildDefault(t *testing.T) {
	var gotModel, gotProv string
	d := newSwarmDialog()
	d.Open(staticSnapshots(), nil, nil,
		func(_, model, prov string) error {
			gotModel, gotProv = model, prov
			return nil
		},
		nil, nil, "")
	_ = d.Render(tui.Theme{}, 80)

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	for _, r := range "do thing" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if gotModel != "" || gotProv != "" {
		t.Fatalf("spawn args = (model=%q, prov=%q); want both empty", gotModel, gotProv)
	}
}

// TestSwarmTranscriptGrowsWithNewMessages confirms the /btw-style
// "the transcript grows with the content" property: when the agent
// reply lands in the snapshot, the next render includes it in full,
// alongside everything that was already there. Replaces the old
// followTail/scroll tests that pinned a viewport mechanic we no
// longer have.
func TestSwarmTranscriptGrowsWithNewMessages(t *testing.T) {
	cur := []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusRunning,
		Lines: []string{"old line 1", "old line 2"}, Started: time.Now(),
	}}
	snap := func() []swarm.AgentSnapshot { return cur }

	d := newSwarmDialog()
	d.Open(snap, nil, nil, nil,
		func(string, string) error { return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "old line 1") || !strings.Contains(out, "old line 2") {
		t.Fatalf("initial render missing existing lines:\n%s", out)
	}

	// Agent appends a fresh reply: the very next render must show
	// both the new content AND keep the old content (no clipping).
	cur = []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusRunning,
		Lines:   []string{"old line 1", "old line 2", "new reply A", "new reply B"},
		Started: cur[0].Started,
	}}
	out = strings.Join(d.Render(tui.Theme{}, 80), "\n")
	for _, want := range []string{"old line 1", "old line 2", "new reply A", "new reply B"} {
		if !strings.Contains(out, want) {
			t.Errorf("line %q missing after growth:\n%s", want, out)
		}
	}
}

// TestSwarmTranscriptBtwStyleMultipleSends pins the /btw-style flow
// for the transcript view: after one Enter-submit the editor stays
// mounted and clears, and a second message can be composed and sent
// without any modal toggle. Regresses the previous design where
// every send closed the prompt and forced the user to press `p`
// again.
func TestSwarmTranscriptBtwStyleMultipleSends(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusRunning, Started: time.Now(),
		Lines: []string{"warm-up"},
	}}
	var sent []string
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(_, text string) error { sent = append(sent, text); return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // open transcript + auto-editor

	// First message.
	for _, r := range "first" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	// Second message, no `p` toggle in between.
	for _, r := range "second" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if len(sent) != 2 || sent[0] != "first" || sent[1] != "second" {
		t.Fatalf("sent = %v; want [first second]", sent)
	}
	if !d.viewing || !d.prompting {
		t.Errorf("transcript view/editor torn down between sends (viewing=%v prompting=%v)", d.viewing, d.prompting)
	}
}

// TestSwarmTranscriptEscClosesView pins the Esc semantics for the
// /btw-style transcript view: a single Esc closes the whole view
// (back to the dashboard list), regardless of whether the editor
// is currently composing. Without this routing the editor's own
// Esc handler would just clear the modal and leave the user stuck
// inside an empty transcript view.
func TestSwarmTranscriptEscClosesView(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusRunning, Started: time.Now(),
		Lines: []string{"hi"},
	}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.viewing || !d.prompting {
		t.Fatalf("setup: viewing=%v prompting=%v", d.viewing, d.prompting)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if d.viewing {
		t.Error("Esc did not close the transcript view")
	}
	if d.prompting {
		t.Error("Esc did not tear down the inline editor")
	}
}

// TestSwarmTranscriptNonRunningAgentNoEditor pins the safety net:
// transcripts for done / failed / killed agents must NOT auto-open
// an editor (the agent's inbox isn't listening, so a send would
// just bounce). The view shows the transcript plus a hint about
// resuming instead.
func TestSwarmTranscriptNonRunningAgentNoEditor(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusDone, Started: time.Now(),
		Lines: []string{"all done"},
	}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if d.prompting {
		t.Fatal("editor auto-opened for a done agent; should only happen for running ones")
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "prompt: agent is done") {
		t.Errorf("hint about non-running status missing:\n%s", out)
	}
}

// TestSwarmTranscriptTickRefreshWhileEditing pins the streaming
// fix: with the always-on editor in transcript view, the host's
// animation tick MUST still call us so streaming agent output
// renders. Without this, sending a message and watching the reply
// land silently failed because the prompting=true branch was
// suppressing redraws (like the modal editors do).
func TestSwarmTranscriptTickRefreshWhileEditing(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusRunning,
		Activity: "thinking", Started: time.Now(),
		Lines: []string{"hello"},
	}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.viewing || !d.prompting {
		t.Fatalf("setup: viewing=%v prompting=%v", d.viewing, d.prompting)
	}

	if !d.NeedsTickRefresh() {
		t.Fatal("transcript view with always-on editor refused tick refresh; streaming output would freeze")
	}
}

// TestSwarmTranscriptTickRefreshModalSuppressed pins the inverse:
// the list-view `p` modal (prompting without viewing) still
// suppresses tick like the other modal editors, so the cursor
// blink works cleanly while composing a one-off prompt.
func TestSwarmTranscriptTickRefreshModalSuppressed(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "agent-1", Status: swarm.StatusRunning, Started: time.Now(),
	}}
	d := newSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil,
		func(string, string) error { return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if d.viewing || !d.prompting {
		t.Fatalf("setup: viewing=%v prompting=%v; want list-view modal", d.viewing, d.prompting)
	}

	if d.NeedsTickRefresh() {
		t.Error("list-view prompt modal did not suppress tick refresh; cursor blink may misbehave")
	}
}

// TestSwarmTranscriptBusySpinnerRenders proves the inline spinner
// appears above the always-on editor while the agent is busy and
// disappears when it goes idle. This is the /btw-style "something
// is happening" feedback the user was missing.
func TestSwarmTranscriptBusySpinnerRenders(t *testing.T) {
	cur := []swarm.AgentSnapshot{{
		ID: "agent-1", Task: "x", Status: swarm.StatusRunning,
		Activity: "thinking", Started: time.Now(),
		Lines: []string{"hello"},
	}}
	snap := func() []swarm.AgentSnapshot { return cur }
	d := newSwarmDialog()
	d.Open(snap, nil, nil, nil,
		func(string, string) error { return nil },
		nil, "")
	_ = d.Render(tui.Theme{}, 80)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "thinking") {
		t.Fatalf("busy spinner did not surface activity \"thinking\" near the editor:\n%s", out)
	}
	if d.transcriptSpin == nil {
		t.Error("spinner not initialised while agent is busy")
	}

	// Flip the activity to idle: spinner should vanish.
	cur[0].Activity = "idle"
	out = strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if d.transcriptSpin != nil {
		t.Error("spinner stayed live after agent reported idle")
	}
	// The bare word "thinking" must not appear anymore.
	if strings.Contains(out, "· thinking, ") {
		t.Errorf("busy line still rendered when agent is idle:\n%s", out)
	}
}

func TestStatusLabelCoversAll(t *testing.T) {
	for _, s := range []swarm.Status{
		swarm.StatusPending, swarm.StatusRunning,
		swarm.StatusDone, swarm.StatusFailed, swarm.StatusKilled,
	} {
		if statusLabel(s) == "" {
			t.Fatalf("no label for %s", s)
		}
	}
}
