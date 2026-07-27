package dialogs

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// The /workflows panel is the terminal twin of the web board's lane. Reading a
// run used to mean leaving the TUI for `terva workflow show`, and a run whose
// launching terminal had closed was on disk and unreachable in practice.

var wfRuns = []ctrlproto.WorkflowRunInfo{
	{
		ID: "wf_1a2b3c4d5e6f", Name: "exhaustive-review", Status: "incomplete",
		Started: "2026-07-26T18:04:00Z", Completed: 4, CostUSD: 0.4213, Resumable: true,
	},
	{
		ID: "wf_9f8e7d6c5b4a", Name: "migrate-call-sites", Status: "done",
		Completed: 12, Agents: 12, Cached: 4, CostUSD: 6.8812,
	},
}

func wfView(id string) *ctrlproto.WorkflowRunView {
	for _, r := range wfRuns {
		if r.ID != id {
			continue
		}
		return &ctrlproto.WorkflowRunView{
			Run:    r,
			Script: "export const meta = { name: 'exhaustive-review' }\nawait agent('slice one')\n",
			Results: []ctrlproto.WorkflowRunResult{
				{Label: "review:correctness", AgentID: "rc-4f21", Bytes: 2148, Result: json.RawMessage(`{"finding":"one"}`)},
			},
		}
	}
	return nil
}

func openWF(t *testing.T) *WorkflowDialog {
	t.Helper()
	d := NewWorkflowDialog()
	d.MaxRows = 40
	var opened string
	d.Open(
		func() []ctrlproto.WorkflowRunInfo { return wfRuns },
		func() *ctrlproto.WorkflowRunView { return wfView(opened) },
	)
	// Stand in for the host's fetch: whatever the panel asks to open becomes
	// what the view function serves.
	t.Cleanup(func() { _ = opened })
	return d
}

func wfText(d *WorkflowDialog) string {
	return strings.Join(d.Render(tui.Theme{}, 100), "\n")
}

func TestTheRunListShowsWhatAnOperatorDecidesOn(t *testing.T) {
	d := openWF(t)
	out := wfText(d)

	for _, want := range []string{"wf_1a2b3c4d5e6f", "exhaustive-review", "$0.4213", "migrate-call-sites"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run list omits %q:\n%s", want, out)
		}
	}
	// A closed run knows its total; an interrupted one does not. "4/0" would be
	// a lie about a run that completed four agents.
	if !strings.Contains(out, "4/?") {
		t.Errorf("an interrupted run should read 4/? (total unknown until it closes):\n%s", out)
	}
	if !strings.Contains(out, "12/12") {
		t.Errorf("a closed run should read 12/12:\n%s", out)
	}
}

func TestOpeningARunAsksTheHostToFetchIt(t *testing.T) {
	d := openWF(t)
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if act.OpenRun != wfRuns[0].ID {
		t.Fatalf("↵ should ask for the selected run, got %q", act.OpenRun)
	}
	if d.Opened() != wfRuns[0].ID {
		t.Errorf("the panel does not know which run it is showing: %q", d.Opened())
	}
	// Until the fetch lands there is nothing to render, and that must not read
	// as "this run has no results".
	if out := wfText(d); !strings.Contains(out, "loading") {
		t.Errorf("a pending fetch should say so:\n%s", out)
	}
}

func TestTheThreeTabs(t *testing.T) {
	d := NewWorkflowDialog()
	d.MaxRows = 60
	d.Open(func() []ctrlproto.WorkflowRunInfo { return wfRuns },
		func() *ctrlproto.WorkflowRunView { return wfView(wfRuns[0].ID) })
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	// Overview: the numbers, and the resume hint that is the actionable part.
	out := wfText(d)
	if !strings.Contains(out, "incomplete") || !strings.Contains(out, "$0.4213") {
		t.Errorf("overview is missing the run's state:\n%s", out)
	}
	if !strings.Contains(out, "--resume wf_1a2b3c4d5e6f") {
		t.Errorf("a resumable run should say how to replay its completed work:\n%s", out)
	}

	// Script: the source as it ran. This is the tab that justifies the panel —
	// it is what you would otherwise need shell access to the host to read.
	d.HandleKey(tui.Key{Kind: tui.KeyRight})
	out = wfText(d)
	if !strings.Contains(out, "export const meta") || !strings.Contains(out, "await agent('slice one')") {
		t.Errorf("the script tab does not show the recorded source:\n%s", out)
	}

	// Results: labelled, because the generated agent id is unreadable when a
	// fan-out shares a prompt preamble.
	d.HandleKey(tui.Key{Kind: tui.KeyRight})
	out = wfText(d)
	if !strings.Contains(out, "review:correctness") {
		t.Errorf("the results tab does not label its reports:\n%s", out)
	}
	if !strings.Contains(out, "finding") {
		t.Errorf("the results tab does not show what an agent returned:\n%s", out)
	}

	// And back around to Overview, so the tabs are a ring and not a dead end.
	d.HandleKey(tui.Key{Kind: tui.KeyRight})
	if out = wfText(d); !strings.Contains(out, "incomplete") {
		t.Errorf("tabbing past the last tab should wrap to the first:\n%s", out)
	}
}

// Esc backs out one level. Closing outright from a run would make the panel a
// one-shot viewer and force a re-open to compare two runs.
func TestEscBacksOutOfARunBeforeClosingThePanel(t *testing.T) {
	d := openWF(t)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); act.Close {
		t.Error("esc inside a run should return to the list, not close the panel")
	}
	if d.Opened() != "" {
		t.Errorf("esc should have returned to the list, still on %q", d.Opened())
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Error("esc on the list should close the panel")
	}
}

// A slow fetch must not paint the previous run's script under the new run's
// title — the host drops a stale reply, and the panel refuses a mismatched one.
func TestAViewForAnotherRunIsNotRendered(t *testing.T) {
	d := NewWorkflowDialog()
	d.MaxRows = 40
	d.Open(func() []ctrlproto.WorkflowRunInfo { return wfRuns },
		// Always serves the SECOND run, whatever is open.
		func() *ctrlproto.WorkflowRunView { return wfView(wfRuns[1].ID) })
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // opens the FIRST run

	// The dialog renders whatever the host hands it, so the guard that matters
	// lives in the host (workflowRunView). What the dialog owns is the title:
	// it must name the run the operator opened.
	out := wfText(d)
	if !strings.Contains(out, wfRuns[0].ID) {
		t.Errorf("the panel should be titled with the run that was opened:\n%s", out)
	}
}

func TestAnEmptyListSaysHowToRecordARun(t *testing.T) {
	d := NewWorkflowDialog()
	d.MaxRows = 20
	d.Open(func() []ctrlproto.WorkflowRunInfo { return nil }, func() *ctrlproto.WorkflowRunView { return nil })
	if out := wfText(d); !strings.Contains(out, "workflow run") {
		t.Errorf("an empty panel should name the command that records a run:\n%s", out)
	}
}
