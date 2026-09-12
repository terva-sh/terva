package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/groom"
	"terva.sh/terva/packages/testsupport"
)

// fakePass drives the command with no provider and no credential.
type fakePass struct {
	rep   groom.Report
	err   error
	calls int
	got   []groom.Draft
}

func (f *fakePass) Run(_ context.Context, d []groom.Draft) (groom.Report, error) {
	f.calls++
	f.got = d
	rep := f.rep
	rep.Scanned = len(d) // the real pass fills this, so the fake must too
	return rep, f.err
}

// withPass installs a fake for one test and restores the real builder.
func withPass(t *testing.T, f *fakePass) {
	t.Helper()
	prev := newGroomPassFn
	newGroomPassFn = func(string, io.Writer) (groomPass, error) { return f, nil }
	t.Cleanup(func() { newGroomPassFn = prev })
}

// groomStore makes a store holding one draft per title.
func groomStore(t *testing.T, titles ...string) (dir string, ids []string) {
	t.Helper()
	dir = testsupport.TempDir(t)
	s, err := ticket.Init(dir, ticket.InitOptions{Actor: ticket.Actor{ID: "agent:test", Name: "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range titles {
		res, err := s.Create(context.Background(), ticket.CreateOptions{
			Title: title, Type: "task", Priority: "normal",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.Ticket.ID)
	}
	return dir, ids
}

// hashTree fingerprints every file under root. Comparing the whole tree
// catches a status move, a stray note, a timestamp bump and anything nobody
// predicted, where an assertion about one call would catch only what it named.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("fingerprinted no files, so a before-and-after comparison would prove nothing")
	}
	return out
}

func diffTrees(before, after map[string]string) []string {
	var changed []string
	for p, h := range after {
		if before[p] != h {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			changed = append(changed, p+" (removed)")
		}
	}
	sort.Strings(changed)
	return changed
}

func runGroom(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runTicketIn(dir, append([]string{"groom"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// --- interception, which criterion 1 rests on -----------------------------

// groom must be answered by terva and never forwarded, and the forward must
// still work for everything else. The list case is the control: without it a
// broken forward would look like a passing interception test.
func TestGroomIsInterceptedAndOtherVerbsStillForward(t *testing.T) {
	dir, _ := groomStore(t, "A draft")
	withPass(t, &fakePass{})

	code, out, errs := runGroom(t, dir)
	if code != 0 {
		t.Fatalf("groom exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "Scanned") {
		t.Errorf("groom did not produce a report, so it may have been forwarded: %q %q", out, errs)
	}

	var lout, lerr bytes.Buffer
	if code := runTicketIn(dir, []string{"list"}, &lout, &lerr); code != 0 {
		t.Fatalf("the control failed: `terva ticket list` exited %d: %s", code, lerr.String())
	}
	if !strings.Contains(lout.String(), "A draft") {
		t.Errorf("the control failed: list did not reach the embedded CLI: %q", lout.String())
	}
}

// --- criterion 2: it never changes a status -------------------------------

// Against a store, not against a reading of the code.
func TestGroomChangesNothingInTheStore(t *testing.T) {
	dir, ids := groomStore(t, "First draft", "Second draft")
	withPass(t, &fakePass{rep: groom.Report{
		Candidates: []groom.Finding{{ID: ids[0], Title: "First draft", Reason: "has a plan"}},
		NotReady:   []groom.Finding{{ID: ids[1], Title: "Second draft", Reason: "no criteria"}},
	}})

	store := filepath.Join(dir, ".tickets")
	before := hashTree(t, store)

	code, out, errs := runGroom(t, dir)
	if code != 0 {
		t.Fatalf("groom exited %d: %s", code, errs)
	}

	if changed := diffTrees(before, hashTree(t, store)); len(changed) > 0 {
		t.Errorf("a default groom run changed the store: %v", changed)
	}
	// The control. A run that read nothing would also change nothing.
	if !strings.Contains(out, ids[0]) {
		t.Errorf("the report named no ticket, so the comparison above proves nothing: %q", out)
	}
}

// Even asked to write, it writes a NOTE and never moves a status.
func TestGroomWithNoteWritesANoteAndLeavesStatusAlone(t *testing.T) {
	dir, ids := groomStore(t, "First draft")
	withPass(t, &fakePass{rep: groom.Report{
		Candidates: []groom.Finding{{ID: ids[0], Title: "First draft", Reason: "worth a look"}},
	}})

	code, _, errs := runGroom(t, dir, "--note")
	if code != 0 {
		t.Fatalf("groom --note exited %d: %s", code, errs)
	}

	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != ticket.StatusDraft {
		t.Errorf("status moved to %q; groom must never promote", tk.Status)
	}
	if !strings.Contains(tk.Body.Notes, "worth a look") {
		t.Errorf("the note did not land: %q", tk.Body.Notes)
	}
	if !strings.Contains(tk.Body.Notes, "a person still decides") {
		t.Error("the note must say it decided nothing, or a later reader reads it as a verdict")
	}
}

// --- criterion 3: the default writes nothing ------------------------------

// A not-ready finding must not earn a note even with --note. The flag widens
// the write to candidates only, and a timer running this must not spray notes
// across the whole pool.
func TestNoteWritesOnlyToCandidates(t *testing.T) {
	dir, ids := groomStore(t, "First draft", "Second draft")
	withPass(t, &fakePass{rep: groom.Report{
		Candidates: []groom.Finding{{ID: ids[0], Title: "First draft", Reason: "ready"}},
		NotReady:   []groom.Finding{{ID: ids[1], Title: "Second draft", Reason: "not ready"}},
	}})

	if code, _, errs := runGroom(t, dir, "--note"); code != 0 {
		t.Fatalf("groom --note exited %d: %s", code, errs)
	}

	s, _ := ticket.Discover(dir)
	notReady, err := s.Get(context.Background(), ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(notReady.Body.Notes) != "" {
		t.Errorf("a not-ready draft was written to: %q", notReady.Body.Notes)
	}
	// The control: the candidate did get one, so the flag works at all.
	candidate, _ := s.Get(context.Background(), ids[0])
	if !strings.Contains(candidate.Body.Notes, "ready") {
		t.Error("the candidate got no note, so the assertion above proves nothing")
	}
}

// --- criterion 5: the empty cases -----------------------------------------

// An empty store must say so, and must not need a model at all: the command
// returns before a pass is ever built, so it needs no credential.
func TestGroomOnAnEmptyStoreSaysSoAndBuildsNoPass(t *testing.T) {
	dir, _ := groomStore(t)
	f := &fakePass{}
	withPass(t, f)

	code, out, errs := runGroom(t, dir)

	if code != 0 {
		t.Fatalf("groom exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "nothing to groom") {
		t.Errorf("an empty store must say so rather than print nothing, got %q", out)
	}
	if f.calls != 0 {
		t.Errorf("an empty store must cost no model call, got %d", f.calls)
	}
}

// A scan that found nothing is a different answer from an empty store, and
// both must be distinguishable from a crash.
func TestGroomWithNoFindingsSaysSo(t *testing.T) {
	dir, _ := groomStore(t, "First draft", "Second draft")
	withPass(t, &fakePass{})

	code, out, errs := runGroom(t, dir)

	if code != 0 {
		t.Fatalf("groom exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "no candidates") {
		t.Errorf("a scan with no findings must say so, got %q", out)
	}
	if !strings.Contains(out, "Scanned 2") {
		t.Errorf("the scan count is what separates this from an empty store, got %q", out)
	}
	if strings.Contains(out, "nothing to groom") {
		t.Error("a scanned pool must not be reported as an empty store")
	}
}

// --- criterion 1: the report says what and why ----------------------------

func TestGroomReportNamesCandidatesAndReasons(t *testing.T) {
	dir, ids := groomStore(t, "First draft", "Second draft")
	withPass(t, &fakePass{rep: groom.Report{
		Candidates: []groom.Finding{{ID: ids[0], Title: "First draft", Reason: "carries criteria and a plan"}},
		NotReady:   []groom.Finding{{ID: ids[1], Title: "Second draft", Reason: "names no acceptance criteria"}},
	}})

	code, out, errs := runGroom(t, dir)
	if code != 0 {
		t.Fatalf("groom exited %d: %s", code, errs)
	}

	for _, want := range []string{
		ids[0], "First draft", "carries criteria and a plan",
		ids[1], "Second draft", "names no acceptance criteria",
		"Worth a look now", "Not ready, and why",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	// The report must not let a reader mistake it for a decision.
	if !strings.Contains(out, "advisory") || !strings.Contains(out, "nothing here\nchanged a ticket") {
		t.Errorf("the report must say what it is and what it did not do:\n%s", out)
	}
}

func TestGroomReportsAnUnknownIdCount(t *testing.T) {
	dir, ids := groomStore(t, "First draft")
	withPass(t, &fakePass{rep: groom.Report{
		Candidates: []groom.Finding{{ID: ids[0], Title: "First draft", Reason: "ready"}},
		Unknown:    2,
	}})

	_, out, _ := runGroom(t, dir)

	if !strings.Contains(out, "not in the pool") {
		t.Errorf("a dropped id must be reported, or the reader over-trusts the rest:\n%s", out)
	}
}

// --- the projection reaches the pass --------------------------------------

func TestTheProjectionCarriesReadinessSignals(t *testing.T) {
	dir, ids := groomStore(t, "First draft")
	f := &fakePass{}
	withPass(t, f)

	if code, _, errs := runGroom(t, dir); code != 0 {
		t.Fatalf("groom exited %d: %s", code, errs)
	}

	if len(f.got) != 1 {
		t.Fatalf("the pass saw %d drafts, want 1", len(f.got))
	}
	d := f.got[0]
	if d.ID != ids[0] || d.Title != "First draft" {
		t.Errorf("projection lost identity: %+v", d)
	}
	if d.Priority != "normal" {
		t.Errorf("projection lost priority: %+v", d)
	}
}

func TestCountChecklistCountsBothBoxes(t *testing.T) {
	got := countChecklist("- [ ] one\n- [x] two\nsome prose\n  - [ ] three\n")
	if got != 3 {
		t.Errorf("countChecklist = %d, want 3 (ticked and unticked both count)", got)
	}
	if countChecklist("") != 0 {
		t.Error("an empty section has no criteria")
	}
}
