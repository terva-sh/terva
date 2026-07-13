package tasks

import (
	"strings"
	"testing"
)

func TestMarkdownTaskLine(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want []string // all must be substrings
		bad  []string // none may be substrings
	}{
		{
			name: "done carries a checked box and evidence",
			task: Task{ID: "task-1", Title: "Add serializer", Status: StatusDone, Evidence: "go test ./export passes"},
			want: []string{"- [x] task-1 Add serializer — go test ./export passes"},
			bad:  []string{"(done)"}, // the [x] conveys done; no redundant tag
		},
		{
			name: "pending is a bare unchecked box, no tag",
			task: Task{ID: "task-2", Title: "Wire button", Status: StatusPending},
			want: []string{"- [ ] task-2 Wire button"},
			bad:  []string{"(pending)"},
		},
		{
			name: "blocked is tagged and shows its reason",
			task: Task{ID: "task-3", Title: "Migrate tokens", Status: StatusBlocked, Evidence: "waiting on ops"},
			want: []string{"- [ ] task-3 (blocked) Migrate tokens — waiting on ops"},
		},
		{
			name: "cancelled is tagged and struck through",
			task: Task{ID: "task-4", Title: "Rework mw", Status: StatusCancelled, Note: "superseded"},
			want: []string{"- [ ] task-4 (cancelled) ~~Rework mw~~ — superseded"},
		},
		{
			name: "note falls back when no evidence",
			task: Task{ID: "task-5", Title: "Doc it", Status: StatusPending, Note: "see README"},
			want: []string{"- [ ] task-5 Doc it — see README"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := markdownTaskLine(c.task)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("line = %q, want substring %q", got, w)
				}
			}
			for _, bad := range c.bad {
				if strings.Contains(got, bad) {
					t.Errorf("line = %q, should not contain %q", got, bad)
				}
			}
		})
	}
}

// A title with an embedded newline must not become a second Markdown line (no
// heading/list-item injection): CleanOneLine collapses it to one line.
func TestMarkdownTaskLineDefusesInjection(t *testing.T) {
	got := markdownTaskLine(Task{ID: "x", Title: "ok\n## Injected", Status: StatusPending})
	if strings.Contains(got, "\n") {
		t.Errorf("task line must be single-line, got %q", got)
	}
	if !strings.Contains(got, "ok ## Injected") { // newline became a space
		t.Errorf("expected collapsed title, got %q", got)
	}
}

func TestRenderGenerationMarkdown_RoundTripsEvidence(t *testing.T) {
	g := Generation{
		Seq: 3, ArchivedAt: "2026-07-11T09:00:00Z", Label: "auth refactor",
		Tasks: []Task{
			{ID: "task-1", Title: "Add login endpoint", Status: StatusDone, Evidence: "go test ./auth passes"},
			{ID: "task-2", Title: "Wire session store", Status: StatusBlocked, Evidence: "waiting on ops"},
		},
	}
	out := RenderGenerationMarkdown(g)
	// Acceptance: an H2 header and a checkbox/evidence line per task.
	if !strings.HasPrefix(out, "## Generation 3 — 2026-07-11 — auth refactor") {
		t.Errorf("missing/wrong H2 heading:\n%s", out)
	}
	for _, want := range []string{
		"- [x] task-1 Add login endpoint — go test ./auth passes",
		"- [ ] task-2 (blocked) Wire session store — waiting on ops",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderGenerationMarkdown_EmptyTasks(t *testing.T) {
	out := RenderGenerationMarkdown(Generation{Seq: 1, ArchivedAt: "2026-07-11T00:00:00Z"})
	if !strings.HasPrefix(out, "## Generation 1 — 2026-07-11") || !strings.Contains(out, "_No tasks._") {
		t.Errorf("empty generation render: %q", out)
	}
}

func TestRenderWorklogMarkdown(t *testing.T) {
	if out := RenderWorklogMarkdown(nil); !strings.HasPrefix(out, "# Task worklog") || !strings.Contains(out, "_No archived task lists yet._") {
		t.Errorf("empty worklog: %q", out)
	}
	gens := []Generation{
		{Seq: 1, ArchivedAt: "2026-07-10T00:00:00Z", Label: "phase one", Tasks: []Task{{ID: "task-1", Title: "A", Status: StatusDone}}},
		{Seq: 2, ArchivedAt: "2026-07-11T00:00:00Z", Tasks: []Task{{ID: "task-1", Title: "B", Status: StatusCancelled}}},
	}
	out := RenderWorklogMarkdown(gens)
	if !strings.HasPrefix(out, "# Task worklog\n") {
		t.Errorf("worklog missing H1:\n%s", out)
	}
	// Both generations appear as H2 sections, oldest first.
	i1 := strings.Index(out, "## Generation 1")
	i2 := strings.Index(out, "## Generation 2")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("both generations should render oldest-first:\n%s", out)
	}
	if !strings.Contains(out, "- [x] task-1 A") || !strings.Contains(out, "~~B~~") {
		t.Errorf("worklog task lines wrong:\n%s", out)
	}
}

func TestRenderListMarkdown(t *testing.T) {
	if out := RenderListMarkdown(nil); !strings.HasPrefix(out, "## Tasks") || !strings.Contains(out, "_No tasks._") {
		t.Errorf("empty list markdown: %q", out)
	}
	out := RenderListMarkdown([]Task{{ID: "task-1", Title: "Do it", Status: StatusActive}})
	if !strings.Contains(out, "## Tasks") || !strings.Contains(out, "- [ ] task-1 (active) Do it") {
		t.Errorf("list markdown: %q", out)
	}
}
