package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// fakeEpoch is a settable transcript epoch for read-dedup tests: bumping
// n simulates a compaction or /clear that may have dropped earlier reads
// from the context window.
type fakeEpoch struct{ n uint64 }

func (f *fakeEpoch) TranscriptEpoch() uint64 { return f.n }

func dedupRead(t *testing.T, tool *ReadTool, args map[string]any) core.ToolResult {
	t.Helper()
	return dedupReadCtx(t, context.Background(), tool, args)
}

// dedupReadCtx is dedupRead with the dispatch context spelled out, so a
// test can read as a script binding does (withScriptCall) rather than as
// the model does.
func dedupReadCtx(t *testing.T, ctx context.Context, tool *ReadTool, args map[string]any) core.ToolResult {
	t.Helper()
	res, err := tool.Execute(ctx, mustJSON(t, args), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func isDedup(res core.ToolResult) bool {
	d, ok := res.Details.(map[string]any)
	if !ok {
		return false
	}
	v, _ := d["deduplicated"].(bool)
	return v
}

func bodyText(t *testing.T, res core.ToolResult) string {
	t.Helper()
	tb, ok := res.Content[0].(provider.TextBlock)
	if !ok {
		t.Fatalf("expected a TextBlock, got %T", res.Content[0])
	}
	return tb.Text
}

// TestReadDedupRepeatedReadIsStubbed: re-reading an unchanged file in the
// same transcript epoch returns a stub instead of the full body.
func TestReadDedupRepeatedReadIsStubbed(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}

	first := dedupRead(t, tool, map[string]any{"path": "big.txt"})
	if isDedup(first) {
		t.Fatal("first read should return full content, not a dedup stub")
	}
	if !strings.Contains(bodyText(t, first), "gamma") {
		t.Fatal("first read missing file content")
	}

	second := dedupRead(t, tool, map[string]any{"path": "big.txt"})
	if !isDedup(second) {
		t.Fatal("second identical read should be a dedup stub")
	}
	body := bodyText(t, second)
	if strings.Contains(body, "gamma") {
		t.Fatalf("dedup stub must NOT re-send the file content, got %q", body)
	}
	if !strings.Contains(body, "unchanged") {
		t.Fatalf("dedup stub should explain itself, got %q", body)
	}
}

// TestReadDedupEpochChangeReturnsFull: a compaction / transcript reset
// (epoch bump) invalidates the dedup cache so the next read is full again.
func TestReadDedupEpochChangeReturnsFull(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("content here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ep := &fakeEpoch{}
	tool := &ReadTool{CWD: dir, Epoch: ep}

	_ = dedupRead(t, tool, map[string]any{"path": "f.txt"}) // prime
	if !isDedup(dedupRead(t, tool, map[string]any{"path": "f.txt"})) {
		t.Fatal("precondition: second read should dedup within the same epoch")
	}

	ep.n++ // simulate compaction / clear: the earlier read may be gone
	res := dedupRead(t, tool, map[string]any{"path": "f.txt"})
	if isDedup(res) {
		t.Fatal("after an epoch change the read must return full content, not a stub")
	}
	if !strings.Contains(bodyText(t, res), "content here") {
		t.Fatal("post-epoch read missing content")
	}
}

// TestReadDedupChangedFileReturnsFull: the dedup is content-addressed, so
// an edited file always returns fresh content even within the same epoch.
func TestReadDedupChangedFileReturnsFull(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}

	_ = dedupRead(t, tool, map[string]any{"path": "f.txt"})
	if err := os.WriteFile(p, []byte("v2 changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := dedupRead(t, tool, map[string]any{"path": "f.txt"})
	if isDedup(res) {
		t.Fatal("a changed file must return fresh content, not a dedup stub")
	}
	if !strings.Contains(bodyText(t, res), "v2 changed") {
		t.Fatal("changed read missing new content")
	}
	// A re-read of the now-current content dedups again.
	if !isDedup(dedupRead(t, tool, map[string]any{"path": "f.txt"})) {
		t.Fatal("re-read of unchanged new content should dedup")
	}
}

// TestReadDedupDisabledWhenEpochNil: with no epoch bound, dedup is off and
// every read returns full content (the pre-feature behavior).
func TestReadDedupDisabledWhenEpochNil(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir} // no Epoch -> dedup disabled

	for range 3 {
		res := dedupRead(t, tool, map[string]any{"path": "f.txt"})
		if isDedup(res) {
			t.Fatal("dedup must be disabled when Epoch is nil")
		}
		if !strings.Contains(bodyText(t, res), "body") {
			t.Fatal("read missing content")
		}
	}
}

// TestReadDedupDifferentWindowReturnsFull: a different offset/limit is a
// distinct window and must never be falsely stubbed.
func TestReadDedupDifferentWindowReturnsFull(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("1\n2\n3\n4\n5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}

	_ = dedupRead(t, tool, map[string]any{"path": "f.txt"})                             // full
	res := dedupRead(t, tool, map[string]any{"path": "f.txt", "offset": 2, "limit": 2}) // different window
	if isDedup(res) {
		t.Fatal("a different offset/limit window must not be dedup-stubbed")
	}
	if got := bodyText(t, res); got != "2\n3\n" {
		t.Fatalf("windowed read wrong content: %q", got)
	}
}

// TestReadDedupSkippedForScriptCalls: a read issued by a script binding is
// never answered with a stub, however many times the script repeats it.
// The stub is prose returned where the program expects file bytes, and the
// context it claims to have saved was never spent: a script's results do
// not enter the model's transcript at all.
func TestReadDedupSkippedForScriptCalls(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte("<html>\nalpha\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}
	ctx := withScriptCall(context.Background())

	for i := range 3 {
		res := dedupReadCtx(t, ctx, tool, map[string]any{"path": "page.html"})
		if isDedup(res) {
			t.Fatalf("script read %d was dedup-stubbed; a script gets the stub as its read() return value", i+1)
		}
		if !strings.Contains(bodyText(t, res), "gamma") {
			t.Fatalf("script read %d missing file content", i+1)
		}
	}
}

// TestReadDedupScriptCallAfterModelReadReturnsFull is the recorded failure
// (docs/reviews/2026-08-29-local-model-harness-friction-review.md, F1): the
// model reads a file into its context, then a code_execution script reads
// the same file. Keyed only on (path, offset, limit), the dedup fired and
// handed the script a sentence beginning "./page.html — unchanged since you
// read it earlier this session", which the program then parsed as HTML.
func TestReadDedupScriptCallAfterModelReadReturnsFull(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte("<html>\n<script>\nlet x=1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}

	// The model reads it conversationally: this is the copy the stub would
	// later point at, and it is genuinely in the transcript.
	if isDedup(dedupRead(t, tool, map[string]any{"path": "page.html"})) {
		t.Fatal("precondition: the model's first read should be full content")
	}

	res := dedupReadCtx(t, withScriptCall(context.Background()), tool,
		map[string]any{"path": "page.html"})
	if isDedup(res) {
		t.Fatal("the script's read was stubbed against the MODEL's transcript; the script never saw those bytes")
	}
	if body := bodyText(t, res); !strings.Contains(body, "<script>") {
		t.Fatalf("script read must return the file, got %q", body)
	}
}

// TestReadDedupScriptCallDoesNotPrimeTheStub is the same bug pointed the
// other way, and it is why the script exemption skips the RECORD and not
// only the lookup. A script read puts nothing in the transcript, so it must
// not be able to make a later model-issued read stub against content the
// model has never seen.
func TestReadDedupScriptCallDoesNotPrimeTheStub(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("only copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}

	// A script reads it first. Nothing about this reaches the model.
	_ = dedupReadCtx(t, withScriptCall(context.Background()), tool,
		map[string]any{"path": "f.txt"})

	res := dedupRead(t, tool, map[string]any{"path": "f.txt"})
	if isDedup(res) {
		t.Fatal("the model's FIRST read was stubbed against a script's read; the model has no copy to be pointed at")
	}
	if !strings.Contains(bodyText(t, res), "only copy") {
		t.Fatal("the model's first read missing content")
	}
	// The model's own re-read still dedups: the exemption is scoped to the
	// script dispatch, it does not disable the feature.
	if !isDedup(dedupRead(t, tool, map[string]any{"path": "f.txt"})) {
		t.Fatal("the model's re-read should still dedup")
	}
}

// TestReadDedupNeverCrossesAgents: two agents share one ReadTool (the
// bot-mode shape: one registry, an agent per chat). A file agent A has
// read must NOT be stubbed for agent B — B's context has never seen the
// content. Guarded by two mechanisms under test: the dispatch context
// identifies the calling agent (over the stale bound Epoch), and
// transcript epochs are salted per agent so they can never compare
// equal across agents.
func TestReadDedupNeverCrossesAgents(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a1 := core.NewAgent(nil, "m", "", nil)
	a2 := core.NewAgent(nil, "m", "", nil)
	// Bound epoch points at a1 — the "most recently built" stale bind.
	tool := &ReadTool{CWD: dir, Epoch: a1}

	read := func(ag *core.Agent) core.ToolResult {
		t.Helper()
		res, err := tool.Execute(core.ContextWithAgent(context.Background(), ag),
			mustJSON(t, map[string]any{"path": "shared.txt"}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	if isDedup(read(a1)) {
		t.Fatal("a1's first read should be full content")
	}
	if isDedup(read(a1)) == false {
		t.Fatal("a1's re-read should be a dedup stub")
	}
	// The regression: a2 has never read this file — a stub here would
	// point its model at content missing from its context.
	if isDedup(read(a2)) {
		t.Fatal("a2's FIRST read was stubbed against a1's transcript state")
	}
}
