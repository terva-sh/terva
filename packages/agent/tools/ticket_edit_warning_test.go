package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// warnStore lays out a tree with a ticket store in it and returns the root.
func warnStore(t *testing.T, rels ...string) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	for _, rel := range rels {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("- [ ] one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// warnText joins the text a model would read off a tool result.
func warnText(t *testing.T, res core.ToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

func editTicket(t *testing.T, ed *EditTool, rel string) core.ToolResult {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"path":  rel,
		"edits": []map[string]any{{"oldText": "- [ ] one", "newText": "- [x] one"}},
	})
	res, err := ed.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("edit %s: %v", rel, err)
	}
	return res
}

// The warning lands on the first direct write to the store, names the tool that
// does the job, and does NOT stop the write. It then stays quiet for the rest of
// the session, across BOTH tools, because write and edit share one warner.
func TestTicketEditWarningFiresOnceAcrossBothTools(t *testing.T) {
	dir := warnStore(t, ".tickets/tickets/TKT-A.md", ".tickets/tickets/TKT-B.md")
	w := &TicketEditWarner{}
	w.Enable()
	ed := &EditTool{CWD: dir, Files: NewFileState(), Tickets: w}
	wr := &WriteTool{CWD: dir, Files: NewFileState(), Tickets: w}

	first := warnText(t, editTicket(t, ed, ".tickets/tickets/TKT-A.md"))
	if !strings.Contains(first, "ticket store") {
		t.Errorf("first ticket edit carried no warning: %q", first)
	}
	// AC 1: it must name the tool for the job, or it is a scolding with no
	// remedy and the model has nowhere to go.
	if !strings.Contains(first, "ticket_update") {
		t.Errorf("the warning should name the tool for the job: %q", first)
	}
	// The write still lands. A bump in the road is not a wall.
	body, err := os.ReadFile(filepath.Join(dir, ".tickets", "tickets", "TKT-A.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "- [x] one") {
		t.Fatalf("the warning blocked the edit; file holds %q", body)
	}

	// AC 2, and stronger than it asks: once per SESSION, so also at most once
	// per turn however many ticket files the turn touches.
	second := warnText(t, editTicket(t, ed, ".tickets/tickets/TKT-B.md"))
	if strings.Contains(second, "ticket store") {
		t.Errorf("the warning repeated on a second ticket file: %q", second)
	}

	// The other tool shares the warner, so it stays quiet too.
	args, _ := json.Marshal(map[string]any{
		"path": ".tickets/tickets/TKT-C.md", "content": "- [ ] fresh\n",
	})
	res, err := wr.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnText(t, res); strings.Contains(got, "ticket store") {
		t.Errorf("write warned after edit already had: %q", got)
	}
}

// AC 3. A warner nobody enabled is the --no-ticket session and the repository
// with no store: there the ticket tools do not exist, a direct edit is the only
// way to touch a store at all, and a warning naming ticket_update would name a
// tool the model cannot call.
func TestTicketEditWarningStaysSilentUntilEnabled(t *testing.T) {
	dir := warnStore(t, ".tickets/tickets/TKT-A.md")

	quiet := &EditTool{CWD: dir, Files: NewFileState(), Tickets: &TicketEditWarner{}}
	if got := warnText(t, editTicket(t, quiet, ".tickets/tickets/TKT-A.md")); strings.Contains(got, "ticket store") {
		t.Errorf("a warner nobody enabled still warned: %q", got)
	}

	// A nil warner is the same silence and must not panic: a host that builds
	// the tools itself passes no warner at all.
	os.WriteFile(filepath.Join(dir, ".tickets", "tickets", "TKT-A.md"), []byte("- [ ] one\n"), 0o644)
	none := &EditTool{CWD: dir, Files: NewFileState()}
	if got := warnText(t, editTicket(t, none, ".tickets/tickets/TKT-A.md")); strings.Contains(got, "ticket store") {
		t.Errorf("a nil warner warned: %q", got)
	}
}

// The store holds files no ticket tool can write, and warning on those would be
// advice with no remedy. epics.md is deliberately NOT in this list: ticket_fix
// rewrites it, so a hand edit there does have a tool.
func TestTicketEditWarningIgnoresWhatNoTicketToolOwns(t *testing.T) {
	for _, rel := range []string{
		".tickets/README.md",
		".tickets/CONVENTIONS.md",
		"docs/notes.md",
	} {
		dir := warnStore(t, rel)
		w := &TicketEditWarner{}
		w.Enable()
		ed := &EditTool{CWD: dir, Files: NewFileState(), Tickets: w}
		if got := warnText(t, editTicket(t, ed, rel)); strings.Contains(got, "ticket store") {
			t.Errorf("%s drew a warning: %q", rel, got)
		}
	}

	// The positive control for the three above: the same helper on a real
	// ticket DOES warn, so a silent result there cannot be the test misfiring.
	dir := warnStore(t, ".tickets/done/TKT-Z.md")
	w := &TicketEditWarner{}
	w.Enable()
	ed := &EditTool{CWD: dir, Files: NewFileState(), Tickets: w}
	if got := warnText(t, editTicket(t, ed, ".tickets/done/TKT-Z.md")); !strings.Contains(got, "ticket store") {
		t.Fatalf("the control case did not warn, so this test proves nothing: %q", got)
	}
}
