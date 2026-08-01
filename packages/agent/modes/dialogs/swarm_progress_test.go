package dialogs

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/tui"
)

// The motivating screenshot: two agents, both 31m old, one "idle" and one
// "thinking", and no way to tell whether either was alive. The counters and
// the quiet timer are what make those two rows different.
func TestSwarmRowShowsProgress(t *testing.T) {
	now := time.Now()
	rows := []swarm.AgentSnapshot{{
		ID: "alpha-1", Task: "review a single file", Status: swarm.StatusRunning,
		Activity: "idle", Started: now.Add(-31 * time.Minute),
		Turns: 14, ToolCalls: 62, LastEvent: now.Add(-4 * time.Second),
	}}
	d := NewSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	out := strings.Join(d.Render(tui.Theme{}, 120), "\n")

	for _, want := range []string{"TURNS", "TOOLS", "14", "62", "idle · 4s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

// The quiet timer is a claim about a process that is expected to speak. A
// finished agent is silent forever and correctly so; a climbing "quiet 3h"
// beside it would read as a fault.
func TestQuietTimeOnlyForLiveAgents(t *testing.T) {
	now := time.Now()
	live := swarm.AgentSnapshot{Status: swarm.StatusRunning, LastEvent: now.Add(-90 * time.Second)}
	if got := quietFor(live); got != "1m" {
		t.Errorf("quietFor(running) = %q, want 1m", got)
	}
	pending := swarm.AgentSnapshot{Status: swarm.StatusPending, LastEvent: now.Add(-2 * time.Second)}
	if got := quietFor(pending); got != "2s" {
		t.Errorf("quietFor(pending) = %q, want 2s", got)
	}
	for _, s := range []swarm.Status{swarm.StatusDone, swarm.StatusFailed, swarm.StatusKilled, swarm.StatusDetached} {
		done := swarm.AgentSnapshot{Status: s, LastEvent: now.Add(-3 * time.Hour)}
		if got := quietFor(done); got != "" {
			t.Errorf("quietFor(%s) = %q, want empty", s, got)
		}
	}
	// Before the first event there is nothing to report, and "0s quiet"
	// would be a claim the agent just spoke.
	fresh := swarm.AgentSnapshot{Status: swarm.StatusRunning}
	if got := quietFor(fresh); got != "" {
		t.Errorf("quietFor(no events yet) = %q, want empty", got)
	}
}

// On a narrow terminal the counters yield rather than clipping the activity
// string away. Activity is the older, denser signal — "tool: run_tests" says
// more in a glance than any number — so buying columns with it is a downgrade.
func TestNarrowDashboardKeepsActivityAndDropsCounters(t *testing.T) {
	rows := []swarm.AgentSnapshot{{
		ID: "alpha-1", Status: swarm.StatusRunning, Activity: "tool: run_tests",
		Started: time.Now(), Turns: 14, ToolCalls: 62,
	}}
	d := NewSwarmDialog()
	d.Open(staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	out := strings.Join(d.Render(tui.Theme{}, 70), "\n")

	if !strings.Contains(out, "tool: run_tests") {
		t.Fatalf("narrow render lost the activity string:\n%s", out)
	}
	if strings.Contains(out, "TURNS") || strings.Contains(out, "TOOLS") {
		t.Fatalf("narrow render advertised columns it has no room for:\n%s", out)
	}
}

// The header and the rows are two printf strings that must agree. A header
// promising TURNS above rows that dropped it is worse than no header.
func TestHeaderMatchesRowsAtEveryWidth(t *testing.T) {
	row := swarm.AgentSnapshot{
		ID: "alpha-1", Status: swarm.StatusRunning, Activity: "idle",
		Started: time.Now(), Turns: 7, ToolCalls: 9,
	}
	for w := 40; w <= 200; w += 3 {
		header := swarmListHeader(w - 2)
		headerHasCols := strings.Contains(header, "TURNS")
		body := formatSwarmRow(row, w-2)
		// The counters are right-aligned in width-5 cells, so look for the
		// padded forms the row would actually contain.
		bodyHasCols := strings.Contains(body, "    7      9  ")
		if headerHasCols != bodyHasCols {
			t.Fatalf("width %d: header cols=%v but row cols=%v\nheader: %q\nrow:    %q",
				w, headerHasCols, bodyHasCols, header, body)
		}
		// The frame-safety rule: no rendered row may exceed the frame.
		if n := len([]rune(body)); n > w-2 {
			t.Fatalf("width %d: row is %d runes, over the %d budget: %q", w, n, w-2, body)
		}
	}
}

// The transcript view is where a user goes when they suspect an agent is
// stuck, and a transcript that has not moved looks the same either way. The
// numbers have to be there too.
func TestTranscriptStatusLineCarriesProgress(t *testing.T) {
	now := time.Now()
	rows := []swarm.AgentSnapshot{{
		ID: "alpha-1", Task: "review", Status: swarm.StatusRunning, Activity: "thinking",
		Started: now.Add(-time.Hour), Turns: 14, ToolCalls: 62,
		LastEvent: now.Add(-6 * time.Minute), Lines: []string{"working on it"},
	}}
	d := NewSwarmDialog()
	d.OpenViewing("alpha-1", staticSnapshots(rows...), nil, nil, nil, nil, nil, "")
	out := strings.Join(d.Render(tui.Theme{}, 100), "\n")

	for _, want := range []string{"quiet 6m", "14 turns, 62 tools"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript status line missing %q:\n%s", want, out)
		}
	}
}
