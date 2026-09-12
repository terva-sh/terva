package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func runList(t *testing.T, home, cwd, args string) core.ToolResult {
	t.Helper()
	tool := &SessionListTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
	if err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
	return res
}

// seedSession writes a transcript into the bucket for project, and stamps its
// modification time so the ordering this tool promises is testable. Two files
// written in the same millisecond otherwise sort on the path tiebreak, which
// would make a paging assertion depend on filesystem timestamp resolution.
func seedSession(t *testing.T, home, project, id, marker string, mod time.Time) string {
	t.Helper()
	dir := core.SessionsDir(home, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	writeSessionFixture(t, path, project, "prompt", marker)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

// The default must not reach beyond this project. A listing that silently
// included every project would be a scope change nobody asked for, and the
// short answer a caller expected would quietly become a long one.
func TestSessionListDefaultScopeStaysInThisProject(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	other := testsupport.TempDir(t)
	base := time.Now().Add(-time.Hour)

	seedSession(t, home, cwd, "20260101-000000-aaaaaaaa", "mine", base)
	seedSession(t, home, other, "20260101-000000-bbbbbbbb", "theirs", base.Add(time.Minute))

	res := runList(t, home, cwd, `{}`)
	if res.IsError {
		t.Fatalf("listing this project should succeed: %q", inspectText(t, res))
	}
	got := inspectText(t, res)
	if !strings.Contains(got, "20260101-000000-aaaaaaaa") {
		t.Errorf("this project's session is missing: %q", got)
	}
	if strings.Contains(got, "20260101-000000-bbbbbbbb") {
		t.Errorf("the default scope leaked another project's session: %q", got)
	}
	// And it says the wider scope exists, so a caller does not read one
	// project's list as the whole store.
	if !strings.Contains(got, `scope "all"`) {
		t.Errorf("a project listing should name the wider scope, got: %q", got)
	}
}

// The gap this ticket exists to close: with only an id-resolving reader, a
// caller holding a question rather than an id had nowhere to start. This is the
// route that lets TKT-01M29HFZEX assemble a corpus.
func TestSessionListAllScopeReachesAnotherProject(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	other := testsupport.TempDir(t)
	base := time.Now().Add(-time.Hour)

	seedSession(t, home, cwd, "20260101-000000-aaaaaaaa", "mine", base)
	seedSession(t, home, other, "20260101-000000-bbbbbbbb", "theirs", base.Add(time.Minute))

	res := runList(t, home, cwd, `{"scope":"all"}`)
	if res.IsError {
		t.Fatalf("the wider scope should succeed: %q", inspectText(t, res))
	}
	got := inspectText(t, res)
	for _, want := range []string{"20260101-000000-aaaaaaaa", "20260101-000000-bbbbbbbb"} {
		if !strings.Contains(got, want) {
			t.Errorf("scope all should list %s, got: %q", want, got)
		}
	}
	// The working directory of each row, so a reader can tell the projects
	// apart rather than seeing two opaque ids.
	if !strings.Contains(got, other) {
		t.Errorf("a cross-project row should name its working directory %q, got: %q", other, got)
	}
	if !strings.Contains(got, "(this project)") {
		t.Errorf("the caller's own project should be marked, got: %q", got)
	}
	// Newest first, so the most recently touched session leads.
	if i, j := strings.Index(got, "bbbbbbbb"), strings.Index(got, "aaaaaaaa"); i > j {
		t.Errorf("rows should be newest first, got: %q", got)
	}
	// The ids ride in Details so a sweep can consume them without parsing prose.
	details, _ := res.Details.(map[string]any)
	ids, _ := details["session_ids"].([]string)
	if len(ids) != 2 {
		t.Errorf("Details.session_ids = %v, want both ids for a programmatic caller", details["session_ids"])
	}
}

// An unknown scope is refused rather than narrowed to the default. A caller
// that asked for "global" and silently received one project would read the
// short list as "there is nothing else", which is the one answer an enumeration
// must never fake.
func TestSessionListRefusesAnUnknownScope(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	seedSession(t, home, cwd, "20260101-000000-aaaaaaaa", "mine", time.Now())

	res := runList(t, home, cwd, `{"scope":"global"}`)
	if !res.IsError {
		t.Fatalf("an unknown scope must be refused, got: %q", inspectText(t, res))
	}
	if got := inspectText(t, res); !strings.Contains(got, "project") || !strings.Contains(got, "all") {
		t.Errorf("the refusal should name both valid scopes, got: %q", got)
	}
}

// Paging is what makes this usable on a real store, where one project can hold
// hundreds of sessions.
func TestSessionListPages(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	base := time.Now().Add(-time.Hour)
	ids := []string{
		"20260101-000000-aaaaaaaa",
		"20260101-000000-bbbbbbbb",
		"20260101-000000-cccccccc",
	}
	// Ascending mtimes, so the listing returns them reversed.
	for i, id := range ids {
		seedSession(t, home, cwd, id, "m", base.Add(time.Duration(i)*time.Minute))
	}

	res := runList(t, home, cwd, `{"limit":2}`)
	got := inspectText(t, res)
	if !strings.Contains(got, "3 total") || !strings.Contains(got, "showing 1–2") {
		t.Errorf("first page should report the total and its range, got: %q", got)
	}
	if !strings.Contains(got, "offset 2") {
		t.Errorf("a cut listing should name the next offset, got: %q", got)
	}
	if strings.Contains(got, "aaaaaaaa") {
		t.Errorf("the oldest session should fall onto page two, got: %q", got)
	}

	res = runList(t, home, cwd, `{"limit":2,"offset":2}`)
	got = inspectText(t, res)
	if !strings.Contains(got, "aaaaaaaa") {
		t.Errorf("page two should hold the oldest session, got: %q", got)
	}
	if strings.Contains(got, "more: pass offset") {
		t.Errorf("the last page should not advertise another, got: %q", got)
	}

	// Past the end is a mistake worth naming, not an empty list that reads as
	// "this project has nothing".
	res = runList(t, home, cwd, `{"offset":99}`)
	if !res.IsError {
		t.Errorf("an offset past the end should be refused, got: %q", inspectText(t, res))
	}
}

// The other half of this ticket's definition of done, and a guard rather than a
// feature: session_search must NOT have widened. Enumeration and search are
// separate contracts, and the decision recorded on TKT-01M29MJZ was to add a
// listing route and leave search alone. A later change that quietly makes
// search cross-project would be a scope change made by accident.
func TestSessionSearchStaysProjectScoped(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	other := testsupport.TempDir(t)

	seedSession(t, home, cwd, "20260101-000000-aaaaaaaa", "mine-marker", time.Now())
	seedSession(t, home, other, "20260101-000000-bbbbbbbb", "theirs-marker", time.Now())

	got := runSearch(t, home, cwd, `{"query":"theirs-marker"}`)
	// Assert on the other session's ID and on the count of sessions considered.
	// NOT on the marker text: a miss echoes the query back, so "theirs-marker"
	// appears in the refusal of a correctly scoped search and that assertion
	// fails on working code.
	if strings.Contains(got, "bbbbbbbb") {
		t.Errorf("session_search reached another project's session: %q", got)
	}
	if !strings.Contains(got, "1 session") {
		t.Errorf("session_search should have considered this project's 1 session only, got: %q", got)
	}
	// The control: the same search DOES find this project's own marker, so the
	// miss above is scoping rather than a broken fixture or a dead query path.
	if got := runSearch(t, home, cwd, `{"query":"mine-marker"}`); !strings.Contains(got, "mine-marker") {
		t.Fatalf("the search harness itself is broken: it cannot find this project's own marker: %q", got)
	}
}
