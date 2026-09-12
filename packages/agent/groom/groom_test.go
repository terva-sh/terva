package groom

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"terva.sh/terva/packages/provider"
)

// fakeClient scripts one reply and captures the request that produced it.
type fakeClient struct {
	reply     string
	streamErr error
	doneErr   error
	got       provider.Request
	calls     int
}

func (c *fakeClient) Name() string { return "fake" }

func (c *fakeClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.got = req // written before returning, so the test reads it race-free
	c.calls++
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	ch := make(chan provider.Event, 4)
	go func() {
		defer close(ch)
		if c.reply != "" {
			ch <- provider.EventTextDelta{Delta: c.reply}
		}
		ch <- provider.EventDone{Err: c.doneErr}
	}()
	return ch, nil
}

func pool(ids ...string) []Draft {
	out := make([]Draft, 0, len(ids))
	for _, id := range ids {
		out = append(out, Draft{ID: id, Title: "title of " + id, Priority: "normal"})
	}
	return out
}

func run(t *testing.T, c *fakeClient, drafts []Draft) Report {
	t.Helper()
	p := New(Options{Client: c, Model: "cheap-model"})
	if p == nil {
		t.Fatal("New returned nil for a valid client and model")
	}
	rep, err := p.Run(context.Background(), drafts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

// --- the bound, which criterion 4 is about --------------------------------

func TestExcerptHoldsTheBound(t *testing.T) {
	long := strings.Repeat("a", ExcerptBytes*3)

	got := Excerpt(long)

	// The ellipsis is allowed past the cut, so the bound is on the text.
	if trimmed := strings.TrimSuffix(got, "..."); len(trimmed) > ExcerptBytes {
		t.Errorf("excerpt is %d bytes, over the %d bound", len(trimmed), ExcerptBytes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("a truncated excerpt must say it was truncated")
	}
}

func TestExcerptLeavesAShortDescriptionAlone(t *testing.T) {
	if got := Excerpt("  short  "); got != "short" {
		t.Errorf("a description under the bound must survive whole, got %q", got)
	}
	if strings.HasSuffix(Excerpt("short"), "...") {
		t.Error("an untruncated excerpt must not claim it was cut")
	}
}

// A cut in the middle of a multi-byte rune would hand the model mojibake, and
// the store holds plenty of non-ASCII prose.
func TestExcerptNeverSplitsARune(t *testing.T) {
	// Three-byte runes, so a naive cut at 600 lands mid-rune.
	got := Excerpt(strings.Repeat("あ", ExcerptBytes))

	body := strings.TrimSuffix(got, "...")
	if !utf8.ValidString(body) {
		t.Errorf("excerpt split a rune: %q", body)
	}
	if len(body) > ExcerptBytes {
		t.Errorf("excerpt is %d bytes, over the %d bound", len(body), ExcerptBytes)
	}
}

// The measurement behind the constant. This is the claim ExcerptBytes makes in
// its doc comment, held by a test rather than by the comment alone: a pool of
// oversized drafts must project to roughly bytes-per-draft times drafts, not
// to the size of the corpus.
func TestProjectionStaysInBudget(t *testing.T) {
	const n = 79 // the pool size measured on 2026-09-11
	drafts := make([]Draft, 0, n)
	for i := 0; i < n; i++ {
		d := Draft{
			ID:       string(rune('A'+i%26)) + strings.Repeat("0", 3),
			Title:    "a draft with a perfectly ordinary title",
			Priority: "normal",
			// Far past the bound, including the 35 KB outlier shape.
			Excerpt: Excerpt(strings.Repeat("x", 36000)),
		}
		drafts = append(drafts, d)
	}

	sent := len(renderPool(drafts))

	// Generous ceiling: the excerpt bound plus per-draft scaffolding.
	if ceiling := n * (ExcerptBytes + 300); sent > ceiling {
		t.Errorf("projection is %d bytes, over the %d ceiling the bound implies", sent, ceiling)
	}
	// The control. Without it a projection that dropped every excerpt would
	// pass the ceiling above while sending the model nothing to judge.
	if floor := n * ExcerptBytes; sent < floor {
		t.Errorf("projection is %d bytes, under the %d floor: the excerpts are missing", sent, floor)
	}
}

func TestReportCarriesWhatItActuallySent(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[],"not_ready":[]}`}

	rep := run(t, c, pool("T1", "T2"))

	if rep.SentBytes != len(renderPool(pool("T1", "T2"))) {
		t.Errorf("SentBytes %d does not match the projection", rep.SentBytes)
	}
	if rep.SentBytes == 0 {
		t.Error("SentBytes is the measurement that checks the estimate, so it must be recorded")
	}
}

// --- determinism ----------------------------------------------------------

// Two runs over an unchanged store must send byte-identical input, or the zero
// temperature buys nothing and a reader comparing reports cannot tell a change
// in the store from a change in map iteration order.
func TestRenderPoolIsStableAcrossOrdering(t *testing.T) {
	a := renderPool(pool("T3", "T1", "T2"))
	b := renderPool(pool("T2", "T3", "T1"))

	if a != b {
		t.Errorf("projection depends on input order:\n%q\nvs\n%q", a, b)
	}
	if !strings.Contains(a, "T1") || !strings.Contains(a, "T3") {
		t.Error("the projection dropped a draft, so the comparison above proves nothing")
	}
}

func TestTheCallIsPinnedToZeroTemperatureAndNoReasoning(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[],"not_ready":[]}`}

	run(t, c, pool("T1"))

	if c.got.Temperature == nil || *c.got.Temperature != 0 {
		t.Errorf("temperature must be pinned to zero, got %v", c.got.Temperature)
	}
	if !c.got.ReasoningSet {
		t.Error("reasoning must be set explicitly, or a model default turns every groom into a thinking turn")
	}
}

// --- the model does not get to invent things ------------------------------

func TestAnIdOutsideThePoolIsDropped(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[
		{"id":"T1","reason":"has criteria and a plan"},
		{"id":"TKT-INVENTED","reason":"looks great"}
	],"not_ready":[]}`}

	rep := run(t, c, pool("T1", "T2"))

	if len(rep.Candidates) != 1 || rep.Candidates[0].ID != "T1" {
		t.Fatalf("only the real id may survive, got %+v", rep.Candidates)
	}
	if rep.Unknown != 1 {
		t.Errorf("an invented id must be counted and reported, got Unknown=%d", rep.Unknown)
	}
}

// A report that attached a real id to a title the model wrote would be a
// convincing lie, so the title is taken from the pool.
func TestTitleComesFromThePoolNotTheModel(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[{"id":"T1","reason":"ready"}],"not_ready":[]}`}

	rep := run(t, c, pool("T1"))

	if got := rep.Candidates[0].Title; got != "title of T1" {
		t.Errorf("title must come from the pool, got %q", got)
	}
}

func TestAnIdInBothListsLandsOnce(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[{"id":"T1","reason":"ready"}],
		"not_ready":[{"id":"T1","reason":"actually not"}]}`}

	rep := run(t, c, pool("T1"))

	if len(rep.Candidates)+len(rep.NotReady) != 1 {
		t.Errorf("a contradicted id must be placed once, got %d candidates and %d not-ready",
			len(rep.Candidates), len(rep.NotReady))
	}
	if rep.Unknown != 1 {
		t.Errorf("the discarded half is a contradiction worth reporting, got Unknown=%d", rep.Unknown)
	}
}

// --- the empty cases, which criterion 5 is about --------------------------

// An empty pool is a healthy store, not a failure, and it must not cost a
// model call.
func TestAnEmptyPoolIsNotAnErrorAndCallsNoModel(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[],"not_ready":[]}`}
	p := New(Options{Client: c, Model: "cheap-model"})

	rep, err := p.Run(context.Background(), nil)

	if err != nil {
		t.Fatalf("an empty pool must not be an error: %v", err)
	}
	if rep.Scanned != 0 {
		t.Errorf("Scanned must be 0 for an empty pool, got %d", rep.Scanned)
	}
	if c.calls != 0 {
		t.Errorf("an empty pool must not cost a model call, got %d", c.calls)
	}
}

// A pool the model had nothing to say about is a real answer. Scanned is what
// lets the caller tell it apart from an empty store.
func TestNoCandidatesStillReportsTheScan(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[],"not_ready":[]}`}

	rep := run(t, c, pool("T1", "T2", "T3"))

	if rep.Scanned != 3 {
		t.Errorf("Scanned must count the pool even with no findings, got %d", rep.Scanned)
	}
	if len(rep.Candidates) != 0 || len(rep.NotReady) != 0 {
		t.Error("no findings were scripted, so none may appear")
	}
}

// --- failure ---------------------------------------------------------------

func TestATransportFailureReportsRatherThanInventingAReport(t *testing.T) {
	c := &fakeClient{reply: `{"candidates":[{"id":"T1","reason":"x"}],"not_ready":[]}`, doneErr: context.DeadlineExceeded}

	p := New(Options{Client: c, Model: "cheap-model"})
	rep, err := p.Run(context.Background(), pool("T1"))

	if err == nil {
		t.Fatal("a failed stream must be an error, not a silently empty report")
	}
	if len(rep.Candidates) != 0 {
		t.Error("a failed run must report no candidates")
	}
	if rep.Scanned != 1 {
		t.Errorf("the scan count survives a failure, got %d", rep.Scanned)
	}
}

func TestAnUnparseableReplyIsAnError(t *testing.T) {
	c := &fakeClient{reply: "I had a think about your tickets and honestly they all look fine"}

	p := New(Options{Client: c, Model: "cheap-model"})
	if _, err := p.Run(context.Background(), pool("T1")); err == nil {
		t.Fatal("a reply with no JSON object must be an error")
	}
}

func TestNewRefusesWhatItCannotRun(t *testing.T) {
	if New(Options{Model: "m"}) != nil {
		t.Error("no client must refuse")
	}
	if New(Options{Client: &fakeClient{}}) != nil {
		t.Error("no model must refuse")
	}
}
