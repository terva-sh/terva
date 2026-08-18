package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// fakeEngine answers spawns from a task→outcome table and counts them.
type fakeEngine struct {
	mu       sync.Mutex
	spawns   int
	requests []swarm.SpawnRequest
	// respond maps a task-substring to its outcome; unmatched tasks echo
	// the task text as a JSON string result.
	fail map[string]string
	// cost bills each spawn, for the budget backstop. Zero keeps the
	// long-standing 0.01 so every other test reads as it always did.
	cost float64
}

type fakeHandle struct {
	id  string
	out Outcome
}

func (h fakeHandle) ID() string                        { return h.id }
func (h fakeHandle) AwaitTask(context.Context) Outcome { return h.out }

func (e *fakeEngine) AllowBackend(name string) error {
	if name != "" {
		return fmt.Errorf("backend %q not allowed in tests", name)
	}
	return nil
}

func (e *fakeEngine) Spawn(_ context.Context, req swarm.SpawnRequest) (Handle, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spawns++
	e.requests = append(e.requests, req)
	id := fmt.Sprintf("fake-%d", e.spawns)
	for sub, msg := range e.fail {
		if strings.Contains(req.Task, sub) {
			return fakeHandle{id: id, out: Outcome{AgentID: id, Err: msg}}, nil
		}
	}
	var raw json.RawMessage
	if len(req.Schema) > 0 {
		raw = json.RawMessage(fmt.Sprintf(`{"echo":%q}`, req.Task))
	} else {
		raw, _ = json.Marshal("did: " + req.Task)
	}
	cost := e.cost
	if cost == 0 {
		cost = 0.01
	}
	return fakeHandle{id: id, out: Outcome{AgentID: id, Result: raw, CostUSD: cost}}, nil
}

const testScript = `export const meta = {
  name: 'test-flow',
  description: 'exercise the runner',
  phases: [{ title: 'Fan', detail: 'two agents' }],
}
phase('Fan')
const pair = await parallel([
  () => agent('alpha task', { label: 'a' }),
  () => agent('beta task', { label: 'b' }),
])
log('pair done')
const solo = await agent('gamma task', { schema: { type: 'object', required: ['echo'], properties: { echo: { type: 'string' } } } })
return { pair, solo: solo.echo, spent: budget.spent(), argsSeen: args.tag }
`

func runOpts(t *testing.T) Options {
	t.Helper()
	return Options{Root: testsupport.TempDir(t), Args: map[string]any{"tag": "T1"}}
}

func TestRunHappyPath(t *testing.T) {
	eng := &fakeEngine{}
	res, err := Run(context.Background(), eng, []byte(testScript), runOpts(t))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Meta.Name != "test-flow" || len(res.Meta.Phases) != 1 {
		t.Fatalf("meta = %+v", res.Meta)
	}
	v, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", res.Value)
	}
	pair, _ := v["pair"].([]any)
	if len(pair) != 2 || pair[0] != "did: alpha task" || pair[1] != "did: beta task" {
		t.Fatalf("pair = %#v", pair)
	}
	if v["solo"] != "gamma task" {
		t.Fatalf("solo = %#v (schema deliverable should decode as object)", v["solo"])
	}
	if v["argsSeen"] != "T1" {
		t.Fatalf("args global lost: %#v", v["argsSeen"])
	}
	if res.Agents != 3 || res.CachedAgents != 0 || eng.spawns != 3 {
		t.Fatalf("agents=%d cached=%d spawns=%d", res.Agents, res.CachedAgents, eng.spawns)
	}
	if res.Elapsed <= 0 {
		t.Fatalf("Elapsed = %v — the defer must stamp the returned Result (named returns), not a dead local", res.Elapsed)
	}
	// The schema opt reached the SpawnRequest.
	var withSchema *swarm.SpawnRequest
	for i := range eng.requests {
		if len(eng.requests[i].Schema) > 0 {
			withSchema = &eng.requests[i]
		}
	}
	if withSchema == nil || !strings.Contains(string(withSchema.Schema), `"echo"`) {
		t.Fatalf("schema did not reach the spawn: %+v", eng.requests)
	}
}

func TestRunResumeReplaysJournal(t *testing.T) {
	opts := runOpts(t)
	eng := &fakeEngine{}
	first, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	eng2 := &fakeEngine{}
	opts.ResumeID = first.RunID
	second, err := Run(context.Background(), eng2, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if eng2.spawns != 0 {
		t.Fatalf("resume spawned %d agents, want pure replay", eng2.spawns)
	}
	if second.CachedAgents != 3 || second.Agents != 0 {
		t.Fatalf("cached=%d agents=%d", second.CachedAgents, second.Agents)
	}
	if fmt.Sprint(second.Value) != fmt.Sprint(first.Value) {
		// budget.spent differs (0 on pure replay) — compare the stable part.
		f := first.Value.(map[string]any)
		s := second.Value.(map[string]any)
		if fmt.Sprint(s["pair"]) != fmt.Sprint(f["pair"]) || s["solo"] != f["solo"] {
			t.Fatalf("replayed value diverged: %#v vs %#v", second.Value, first.Value)
		}
	}
}

func TestRunResumeReRunsOnlyEditedCalls(t *testing.T) {
	opts := runOpts(t)
	eng := &fakeEngine{}
	first, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	edited := strings.Replace(testScript, "'beta task'", "'beta task v2'", 1)
	eng2 := &fakeEngine{}
	opts.ResumeID = first.RunID
	second, err := Run(context.Background(), eng2, []byte(edited), opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if eng2.spawns != 1 || second.CachedAgents != 2 {
		t.Fatalf("spawns=%d cached=%d, want exactly the edited call live", eng2.spawns, second.CachedAgents)
	}
	if !strings.Contains(eng2.requests[0].Task, "beta task v2") {
		t.Fatalf("wrong call re-ran: %+v", eng2.requests)
	}
}

func TestRunFailuresAreNullAndRetryOnResume(t *testing.T) {
	opts := runOpts(t)
	eng := &fakeEngine{fail: map[string]string{"beta": "child crashed"}}
	first, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	pair := first.Value.(map[string]any)["pair"].([]any)
	if pair[1] != nil {
		t.Fatalf("failed agent must resolve to null, got %#v", pair[1])
	}
	// The failure was journaled NEVER: resume retries it (and only it,
	// plus nothing else since the rest are cached).
	eng2 := &fakeEngine{}
	opts.ResumeID = first.RunID
	second, err := Run(context.Background(), eng2, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if eng2.spawns != 1 {
		t.Fatalf("resume spawns = %d, want just the failed call", eng2.spawns)
	}
	if pair := second.Value.(map[string]any)["pair"].([]any); pair[1] != "did: beta task" {
		t.Fatalf("retry did not heal: %#v", pair)
	}
}

func TestRunRejectsClaudeCodeOnlyOptions(t *testing.T) {
	script := `export const meta = { name: 'x', description: 'd' }
return await agent('t', { agentType: 'reviewer' })`
	_, err := Run(context.Background(), &fakeEngine{}, []byte(script), runOpts(t))
	if err == nil || !strings.Contains(err.Error(), "agentType→persona") {
		t.Fatalf("err = %v, want the conversion-mapping error", err)
	}
}

func TestRunDeterminismCheckedBeforeSpawns(t *testing.T) {
	script := `export const meta = { name: 'x', description: 'd' }
const t = Date.now()
return await agent('t')`
	eng := &fakeEngine{}
	_, err := Run(context.Background(), eng, []byte(script), runOpts(t))
	if err == nil || !strings.Contains(err.Error(), "Date.now") {
		t.Fatalf("err = %v", err)
	}
	if eng.spawns != 0 {
		t.Fatal("determinism must be checked before any agent spawns")
	}
}

func TestRunMetaRequired(t *testing.T) {
	_, err := Run(context.Background(), &fakeEngine{}, []byte(`return 1`), runOpts(t))
	if err == nil || !strings.Contains(err.Error(), "meta") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunConcurrencyCapQueuesNotFails(t *testing.T) {
	// 6 parallel agents through a cap of 2: all complete, never >2 at once.
	var inflight, peak atomic.Int64
	eng := &gateEngine{inflight: &inflight, peak: &peak}
	script := `export const meta = { name: 'x', description: 'd' }
const out = await parallel([1,2,3,4,5,6].map(n => () => agent('task ' + n)))
return out.filter(Boolean).length`
	opts := runOpts(t)
	opts.Concurrency = 2
	res, err := Run(context.Background(), eng, []byte(script), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(res.Value) != "6" {
		t.Fatalf("value = %#v, want all 6 to complete", res.Value)
	}
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeded the cap", peak.Load())
	}
}

type gateEngine struct {
	inflight, peak *atomic.Int64
	n              atomic.Int64
}

func (e *gateEngine) AllowBackend(string) error { return nil }
func (e *gateEngine) Spawn(_ context.Context, req swarm.SpawnRequest) (Handle, error) {
	return gateHandle{e: e, id: fmt.Sprintf("g-%d", e.n.Add(1)), task: req.Task}, nil
}

type gateHandle struct {
	e    *gateEngine
	id   string
	task string
}

func (h gateHandle) ID() string { return h.id }
func (h gateHandle) AwaitTask(context.Context) Outcome {
	cur := h.e.inflight.Add(1)
	defer h.e.inflight.Add(-1)
	for {
		old := h.e.peak.Load()
		if cur <= old || h.e.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	raw, _ := json.Marshal("ok: " + h.task)
	return Outcome{AgentID: h.id, Result: raw}
}

// TW-041. `label` was narration only: it reached progress() and nothing else,
// so the durable artifacts — the agent's state dir and the journal row — were
// named after the shared prompt preamble. Reading a row back to a slice meant
// matching agent_id through the narration line that printed both, and narration
// is a stderr stream that an interrupted run leaves nowhere.
func TestLabelReachesTheSpawnAndTheJournal(t *testing.T) {
	eng := &fakeEngine{}
	opts := runOpts(t)
	res, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatal(err)
	}

	var labels []string
	for _, r := range eng.requests {
		labels = append(labels, r.Label)
	}
	for _, want := range []string{"a", "b"} {
		if !slices.Contains(labels, want) {
			t.Errorf("label %q never reached a SpawnRequest; got %v", want, labels)
		}
	}
	// The unlabelled third agent must not invent one — its id stays slugged
	// from the task, exactly as before.
	for _, r := range eng.requests {
		if strings.Contains(r.Task, "gamma") && r.Label != "" {
			t.Errorf("an unlabelled agent() gained label %q", r.Label)
		}
	}

	rows := readJournalRows(t, opts.Root, res.RunID)
	got := map[string]bool{}
	for _, r := range rows {
		if r.Type == "result" && r.Label != "" {
			got[r.Label] = true
		}
	}
	for _, want := range []string{"a", "b"} {
		if !got[want] {
			t.Errorf("journal has no result row labelled %q — a row still cannot be read back to a slice", want)
		}
	}
}

// The label must NOT change what a call is keyed by. Resume matches on key
// alone, and key hashes the full (prompt, opts) pair — label included, as it
// always did. Rewriting how an id is NAMED must not invalidate a journal.
func TestResumeStillReplaysAfterLabelsReachTheSpawn(t *testing.T) {
	opts := runOpts(t)
	eng := &fakeEngine{}
	res, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatal(err)
	}
	if eng.spawns != 3 {
		t.Fatalf("first run spawned %d, want 3", eng.spawns)
	}

	eng2 := &fakeEngine{}
	opts2 := opts
	opts2.ResumeID = res.RunID
	if _, err := Run(context.Background(), eng2, []byte(testScript), opts2); err != nil {
		t.Fatal(err)
	}
	if eng2.spawns != 0 {
		t.Errorf("resume spawned %d agents; every call should have replayed", eng2.spawns)
	}
}

// A journal written BEFORE this change has no `label` field at all. It must
// still resume — the field is additive and resume never reads it.
func TestJournalWrittenWithoutLabelsStillResumes(t *testing.T) {
	opts := runOpts(t)
	eng := &fakeEngine{}
	res, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the journal as the old code would have written it: same rows,
	// no label key. A fixture that still contained the field would prove
	// nothing about the format it is meant to stand in for.
	path := filepath.Join(opts.Root, res.RunID, "journal.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if _, had := m["label"]; !had {
			t.Fatal("fixture is not exercising anything: the row had no label to strip")
		}
		delete(m, "label")
		b, _ := json.Marshal(m)
		out.Write(b)
		out.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	eng2 := &fakeEngine{}
	opts2 := opts
	opts2.ResumeID = res.RunID
	if _, err := Run(context.Background(), eng2, []byte(testScript), opts2); err != nil {
		t.Fatalf("a pre-label journal failed to resume: %v", err)
	}
	if eng2.spawns != 0 {
		t.Errorf("resume from a pre-label journal spawned %d agents; want 0", eng2.spawns)
	}
}

// journalLine mirrors the on-disk row deliberately rather than importing the
// writer's struct: what this asserts is the FORMAT — the field names a resume
// (and every reader in runs/) matches on — and a mirror is what makes a rename
// on either side show up here instead of passing silently.
type journalLine struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

func readJournalRows(t *testing.T, root, runID string) []journalLine {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, runID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []journalLine
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var r journalLine
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

// TW-039 acceptance: a resumed run must not double-count the agents it replayed
// from the journal. It does not, because a journal hit returns before the spawn
// and therefore before the spend is folded in — but that is a property of where
// an early return sits, which is exactly the kind of thing a later refactor
// moves without noticing. Pinned.
func TestResumeDoesNotRebillReplayedAgents(t *testing.T) {
	opts := runOpts(t)
	eng := &fakeEngine{}
	first, err := Run(context.Background(), eng, []byte(testScript), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.CostUSD <= 0 {
		t.Fatalf("first run reported $%.4f — the fixture bills nothing, so this proves nothing", first.CostUSD)
	}

	eng2 := &fakeEngine{}
	opts2 := opts
	opts2.ResumeID = first.RunID
	second, err := Run(context.Background(), eng2, []byte(testScript), opts2)
	if err != nil {
		t.Fatal(err)
	}
	if eng2.spawns != 0 {
		t.Fatalf("resume spawned %d agents; the run is not fully replayed so the cost check below is meaningless", eng2.spawns)
	}
	if second.CostUSD != 0 {
		t.Errorf("a fully replayed run billed $%.4f — replayed agents are being charged twice", second.CostUSD)
	}
}
