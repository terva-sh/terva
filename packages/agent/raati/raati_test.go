package raati

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/vote"
)

// fakeUnit implements UnitHandle deterministically: a turn completed
// before the watcher is installed is delivered AT install (mirroring
// the buffered-channel + spawn-window semantics of the real runner)
// so tests never sleep to sequence the barrier.
type fakeUnit struct {
	id string

	mu        sync.Mutex
	onTurnEnd func(int, string)
	pending   *string
	findings  string
	err       error
	exited    bool
	done      chan struct{}
}

func newFakeUnit(id string) *fakeUnit { return &fakeUnit{id: id, done: make(chan struct{})} }

func (u *fakeUnit) AgentID() string { return u.id }

func (u *fakeUnit) SetOnTurnEnd(fn func(int, string)) {
	u.mu.Lock()
	u.onTurnEnd = fn
	var deliver *string
	if fn != nil && u.pending != nil {
		deliver = u.pending
		u.pending = nil
	}
	u.mu.Unlock()
	if deliver != nil {
		fn(1, *deliver)
	}
}

func (u *fakeUnit) Wait() { <-u.done }

func (u *fakeUnit) Err() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.err
}

func (u *fakeUnit) Findings() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.findings
}

// completeTurn finishes the unit's current turn with the given reply
// text (and optional turn error), whether or not a watcher is looking.
func (u *fakeUnit) completeTurn(findings, errMsg string) {
	u.mu.Lock()
	u.findings = findings
	cb := u.onTurnEnd
	if cb == nil {
		e := errMsg
		u.pending = &e
	}
	u.mu.Unlock()
	if cb != nil {
		cb(1, errMsg)
	}
}

// crash simulates the unit's process dying without a turn end.
func (u *fakeUnit) crash(err error) {
	u.mu.Lock()
	u.err = err
	closed := u.exited
	u.exited = true
	u.mu.Unlock()
	if !closed {
		close(u.done)
	}
}

// fakeEngine implements Engine over fakeUnits, scripted per persona.
type fakeEngine struct {
	mu       sync.Mutex
	units    map[string]*fakeUnit
	spawned  []swarm.SpawnRequest
	turns    map[string][]string
	stopped  []string
	spawnErr map[string]error
	// onSpawn scripts round one; onTurn scripts round two. Both may
	// complete the unit's turn synchronously — the coordinator's
	// install-before-trigger ordering must tolerate that.
	onSpawn func(u *fakeUnit, req swarm.SpawnRequest)
	onTurn  func(u *fakeUnit, text string)
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{units: map[string]*fakeUnit{}, turns: map[string][]string{}, spawnErr: map[string]error{}}
}

func (e *fakeEngine) SpawnUnit(_ context.Context, req swarm.SpawnRequest) (UnitHandle, error) {
	e.mu.Lock()
	if err := e.spawnErr[req.Persona]; err != nil {
		e.mu.Unlock()
		return nil, err
	}
	u := newFakeUnit("agent-" + req.Persona)
	e.units[u.id] = u
	e.spawned = append(e.spawned, req)
	onSpawn := e.onSpawn
	e.mu.Unlock()
	if onSpawn != nil {
		onSpawn(u, req)
	}
	return u, nil
}

func (e *fakeEngine) SendUserTurn(id, text string) error {
	e.mu.Lock()
	u := e.units[id]
	e.turns[id] = append(e.turns[id], text)
	onTurn := e.onTurn
	e.mu.Unlock()
	if u == nil {
		return errors.New("no such unit")
	}
	if onTurn != nil {
		onTurn(u, text)
	}
	return nil
}

func (e *fakeEngine) Stop(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = append(e.stopped, id)
	return nil
}

func (e *fakeEngine) turnsFor(id string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.turns[id]...)
}

func ballotText(verdict string, conf float64, rationale string) string {
	return fmt.Sprintf("Deliberation prose.\n\n```ballot\n{\"verdict\": %q, \"confidence\": %v, \"rationale\": %q}\n```\n", verdict, conf, rationale)
}

// scriptRounds wires an engine where each persona votes per the given
// maps in the blind and cross-examination rounds.
func scriptRounds(e *fakeEngine, blind, final map[string]string) {
	e.onSpawn = func(u *fakeUnit, req swarm.SpawnRequest) {
		if v, ok := blind[req.Persona]; ok {
			u.completeTurn(ballotText(v, 0.8, "blind "+v), "")
		}
	}
	e.onTurn = func(u *fakeUnit, _ string) {
		persona := strings.TrimPrefix(u.AgentID(), "agent-")
		if v, ok := final[persona]; ok {
			u.completeTurn(ballotText(v, 0.9, "final "+v), "")
		}
	}
}

func convene(t *testing.T, e *fakeEngine, mut func(*Config)) (*Result, error) {
	t.Helper()
	cfg := Config{Engine: e, RoundTimeout: 200 * time.Millisecond}
	if mut != nil {
		mut(&cfg)
	}
	return Convene(context.Background(), cfg, "should we ship it?", "")
}

func TestConveneAdvisoryMajorityWithDissent(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
	)
	res, err := convene(t, e, nil)
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Outcome.Decision != vote.Approved {
		t.Fatalf("Decision = %q, want approved", res.Outcome.Decision)
	}
	if len(res.Outcome.Minority) != 1 || res.Outcome.Minority[0].Unit != "MAGATAMA-3" {
		t.Errorf("Minority = %+v, want the MAGATAMA-3 dissent", res.Outcome.Minority)
	}
	if res.Outcome.Degraded {
		t.Errorf("Degraded = true for a whole panel")
	}
	if len(res.Blind) != 3 || len(res.Final) != 3 || len(res.Units) != 3 {
		t.Fatalf("record sizes: blind %d final %d units %d", len(res.Blind), len(res.Final), len(res.Units))
	}
	// Every unit was spawned tool-less on the chat experience and was
	// dismissed (units are daemons; Convene must stop what it convened).
	for _, req := range e.spawned {
		if req.Experience != "chat" {
			t.Errorf("spawn %s: Experience = %q, want chat", req.Persona, req.Experience)
		}
	}
	if len(e.stopped) < 3 {
		t.Errorf("stopped %d units, want all 3 dismissed", len(e.stopped))
	}
	// Cross-examination reveals the OTHER seats' ballots, not one's own.
	turns := e.turnsFor("agent-raati-crew:yata")
	if len(turns) != 1 {
		t.Fatalf("YATA-1 got %d cross-exam turns, want 1", len(turns))
	}
	if strings.Contains(turns[0], "YATA-1:") {
		t.Errorf("cross-exam reveal includes the unit's own ballot")
	}
	for _, other := range []string{"KUSANAGI-2", "MAGATAMA-3"} {
		if !strings.Contains(turns[0], other) {
			t.Errorf("cross-exam reveal is missing %s", other)
		}
	}
}

func TestConveneRevisionInCrossExam(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
	)
	res, err := convene(t, e, nil)
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Outcome.Decision != vote.Approved || len(res.Outcome.Minority) != 0 {
		t.Errorf("outcome = %+v, want unanimous approval after revision", res.Outcome)
	}
	if res.Blind[2].Verdict != vote.Reject {
		t.Errorf("blind record lost the provisional dissent: %+v", res.Blind[2])
	}
	if res.Final[2].Verdict != vote.Approve {
		t.Errorf("final record missed the revision: %+v", res.Final[2])
	}
}

func TestConveneSingleRound(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "reject", "raati-crew:magatama": "approve"},
		nil,
	)
	res, err := convene(t, e, func(c *Config) { c.SingleRound = true })
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Outcome.Decision != vote.Approved {
		t.Errorf("Decision = %q, want approved", res.Outcome.Decision)
	}
	for id, turns := range e.turns {
		if len(turns) != 0 {
			t.Errorf("%s received %d cross-exam turns in single-round mode", id, len(turns))
		}
	}
	for i := range res.Blind {
		if res.Blind[i] != res.Final[i] {
			t.Errorf("final[%d] = %+v differs from blind %+v in single-round mode", i, res.Final[i], res.Blind[i])
		}
	}
}

func TestConveneTimeoutMarksAbsent(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{ /* yata never answers */ "raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
		map[string]string{"raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
	)
	res, err := convene(t, e, func(c *Config) { c.RoundTimeout = 100 * time.Millisecond })
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Outcome.Decision != vote.Approved || !res.Outcome.Degraded {
		t.Fatalf("outcome = %+v, want degraded approval on 2/3 quorum", res.Outcome)
	}
	if !res.Final[0].Absent || !strings.Contains(res.Final[0].Rationale, "timed out") {
		t.Errorf("final[0] = %+v, want absent-by-timeout", res.Final[0])
	}
	if got := e.turnsFor("agent-raati-crew:yata"); len(got) != 0 {
		t.Errorf("absent unit received %d cross-exam turns, want 0", len(got))
	}
}

func TestConveneCrashMarksAbsent(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:magatama": "approve"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:magatama": "approve"},
	)
	prev := e.onSpawn
	e.onSpawn = func(u *fakeUnit, req swarm.SpawnRequest) {
		if req.Persona == "raati-crew:kusanagi" {
			u.crash(errors.New("boom"))
			return
		}
		prev(u, req)
	}
	res, err := convene(t, e, nil)
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if !res.Final[1].Absent || !strings.Contains(res.Final[1].Rationale, "exited") {
		t.Errorf("final[1] = %+v, want absent-by-crash", res.Final[1])
	}
	if res.Outcome.Decision != vote.Approved || !res.Outcome.Degraded {
		t.Errorf("outcome = %+v, want degraded approval", res.Outcome)
	}
}

func TestConveneUnparseableBallotIsAbsentNotAbstain(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve"},
	)
	prev := e.onSpawn
	e.onSpawn = func(u *fakeUnit, req swarm.SpawnRequest) {
		if req.Persona == "raati-crew:magatama" {
			u.completeTurn("I decline to use the ballot format.", "")
			return
		}
		prev(u, req)
	}
	res, err := convene(t, e, nil)
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if !res.Final[2].Absent || !strings.Contains(res.Final[2].Rationale, "unparseable") {
		t.Errorf("final[2] = %+v, want absent-by-unparseable-ballot", res.Final[2])
	}
	if res.Final[2].Verdict != vote.Abstain {
		t.Errorf("absent ballot verdict = %q, want abstain", res.Final[2].Verdict)
	}
}

func TestConveneGateFailsClosedOnAbsence(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve"},
	)
	res, err := convene(t, e, func(c *Config) {
		c.Class = ClassGate
		c.RoundTimeout = 100 * time.Millisecond
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Outcome.Decision != vote.Escalated {
		t.Errorf("Decision = %q, want escalated (gate fails closed on a missing unit)", res.Outcome.Decision)
	}
}

func TestConveneVetoDefaultHolderBlocks(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
	)
	res, err := convene(t, e, func(c *Config) { c.Class = ClassVeto })
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Outcome.Decision != vote.Rejected {
		t.Fatalf("Decision = %q, want rejected by veto", res.Outcome.Decision)
	}
	if len(res.Outcome.Minority) != 2 {
		t.Errorf("Minority = %+v, want the two outvoted approvals on the record", res.Outcome.Minority)
	}
}

func TestConveneBlindBallotSurvivesFinalTimeout(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve" /* magatama never files a final */},
	)
	res, err := convene(t, e, func(c *Config) { c.RoundTimeout = 100 * time.Millisecond })
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	fb := res.Final[2]
	if !fb.Absent {
		t.Fatalf("final[2] = %+v, want absent after missing the final round", fb)
	}
	if !strings.Contains(fb.Rationale, "blind ballot was reject") {
		t.Errorf("final[2] rationale %q must preserve the provisional position", fb.Rationale)
	}
	if res.Blind[2].Verdict != vote.Reject || res.Blind[2].Absent {
		t.Errorf("blind[2] = %+v, want the cast provisional reject", res.Blind[2])
	}
}

func TestConveneSpawnFailureStopsTheConvened(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e, map[string]string{"raati-crew:yata": "approve"}, nil)
	e.spawnErr["raati-crew:kusanagi"] = errors.New("no such persona")
	_, err := convene(t, e, nil)
	if err == nil || !strings.Contains(err.Error(), "KUSANAGI-2") {
		t.Fatalf("err = %v, want a spawn failure naming the seat", err)
	}
	if len(e.stopped) != 1 || e.stopped[0] != "agent-raati-crew:yata" {
		t.Errorf("stopped = %v, want the already-seated YATA-1 dismissed", e.stopped)
	}
}

func TestConveneCancelledContext(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e, map[string]string{ /* nobody answers */ }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := Convene(ctx, Config{Engine: e, RoundTimeout: 10 * time.Second}, "q", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestConveneValidation(t *testing.T) {
	e := newFakeEngine()
	if _, err := Convene(context.Background(), Config{Engine: e, Units: []Unit{{Name: "solo", Persona: "p"}}}, "q", ""); err == nil {
		t.Errorf("a one-unit panel must be rejected")
	}
	if _, err := Convene(context.Background(), Config{Engine: e}, "   ", ""); err == nil {
		t.Errorf("an empty question must be rejected")
	}
	custom := []Unit{{Name: "A", Persona: "a"}, {Name: "B", Persona: "b"}}
	if _, err := Convene(context.Background(), Config{Engine: e, Units: custom, Class: ClassVeto}, "q", ""); err == nil {
		t.Errorf("veto on a custom panel without a holder must be rejected")
	}
	if _, err := Convene(context.Background(), Config{}, "q", ""); err == nil {
		t.Errorf("a nil engine must be rejected")
	}
}

func TestConvenePerUnitBindings(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"p:a": "approve", "p:b": "approve", "p:c": "approve"},
		nil,
	)
	units := []Unit{
		{Name: "A", Persona: "p:a", Provider: "anthropic", Model: "claude-opus"},
		{Name: "B", Persona: "p:b", Provider: "openai-codex", Model: "gpt-5.5"},
		{Name: "C", Persona: "p:c"}, // inherits the panel binding
	}
	var seatedBindings []string
	res, err := convene(t, e, func(c *Config) {
		c.Units = units
		c.Provider, c.Model = "ollama", "qwen3:8b"
		c.SingleRound = true
		c.OnEvent = func(ev Event) {
			if ev.Kind == EventSeated {
				seatedBindings = append(seatedBindings, ev.Binding)
			}
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	want := map[string][2]string{
		"p:a": {"anthropic", "claude-opus"},
		"p:b": {"openai-codex", "gpt-5.5"},
		"p:c": {"ollama", "qwen3:8b"},
	}
	for _, req := range e.spawned {
		w := want[req.Persona]
		if req.Provider != w[0] || req.Model != w[1] {
			t.Errorf("spawn %s: binding %s/%s, want %s/%s", req.Persona, req.Provider, req.Model, w[0], w[1])
		}
	}
	if len(seatedBindings) != 3 || seatedBindings[0] != "anthropic/claude-opus" || seatedBindings[2] != "ollama/qwen3:8b" {
		t.Errorf("seated bindings = %v", seatedBindings)
	}
	if res.Units[1].Provider != "openai-codex" || res.Units[1].Model != "gpt-5.5" {
		t.Errorf("record binding = %+v", res.Units[1])
	}
	// The caller's panel must be untouched by the binding merge.
	if units[2].Provider != "" || units[2].Model != "" {
		t.Errorf("caller's unit slice was mutated: %+v", units[2])
	}
}

func TestConveneRejectsHalfBinding(t *testing.T) {
	e := newFakeEngine()
	_, err := Convene(context.Background(), Config{
		Engine: e,
		Units: []Unit{
			{Name: "A", Persona: "p:a", Model: "gpt-5.5"}, // model without provider
			{Name: "B", Persona: "p:b"},
		},
	}, "q", "")
	if err == nil || !strings.Contains(err.Error(), "together") {
		t.Fatalf("err = %v, want half-binding rejection", err)
	}
	if len(e.spawned) != 0 {
		t.Errorf("half-binding validation must run before any spawn; spawned %d", len(e.spawned))
	}
}

var testPool = []Binding{
	{Provider: "openai-codex", Model: "gpt-5.5"},
	{Provider: "opencode-go", Model: "qwen3.7-plus"},
	{Provider: "openrouter", Model: "z.ai/glm-5.2"},
}

func spawnedBinding(e *fakeEngine, persona string, nth int) (string, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := 0
	for _, req := range e.spawned {
		if req.Persona == persona {
			if seen == nth {
				return req.Provider, req.Model
			}
			seen++
		}
	}
	return "", ""
}

func TestSeatOrderFixedIdentityAndMap(t *testing.T) {
	for name, tc := range map[string]struct {
		seatMap []int
		want    [3]int // pool index per seat
	}{
		"identity": {nil, [3]int{0, 1, 2}},
		"remapped": {[]int{2, 0, 1}, [3]int{2, 0, 1}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newFakeEngine()
			scriptRounds(e, map[string]string{
				"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve",
			}, nil)
			res, err := convene(t, e, func(c *Config) {
				c.Bindings = testPool
				c.SeatOrder = SeatOrderFixed
				c.SeatMap = tc.seatMap
				c.SingleRound = true
			})
			if err != nil {
				t.Fatalf("Convene: %v", err)
			}
			for i, persona := range []string{"raati-crew:yata", "raati-crew:kusanagi", "raati-crew:magatama"} {
				want := testPool[tc.want[i]]
				prov, model := spawnedBinding(e, persona, 0)
				if prov != want.Provider || model != want.Model {
					t.Errorf("seat %d spawned on %s/%s, want %s/%s", i, prov, model, want.Provider, want.Model)
				}
			}
			if res.Units[0].Model == "" {
				t.Errorf("record missing seat binding: %+v", res.Units[0])
			}
		})
	}
}

func TestSeatOrderConveneShufflesOnceAndHoldsSeats(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
	)
	permCalls := 0
	_, err := convene(t, e, func(c *Config) {
		c.Bindings = testPool
		c.SeatOrder = SeatOrderConvene
		c.Perm = func(n int) []int { permCalls++; return []int{1, 2, 0} }
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if permCalls != 1 {
		t.Errorf("perm drawn %d times, want once per convening", permCalls)
	}
	e.mu.Lock()
	spawns := len(e.spawned)
	e.mu.Unlock()
	if spawns != 3 {
		t.Fatalf("%d spawns, want 3 (seats hold their children across rounds)", spawns)
	}
	if prov, model := spawnedBinding(e, "raati-crew:yata", 0); prov != "opencode-go" || model != "qwen3.7-plus" {
		t.Errorf("YATA-1 seat = %s/%s, want pool[1] via the injected perm", prov, model)
	}
}

func TestSeatOrderTurnReseatsForTheFinalRound(t *testing.T) {
	e := newFakeEngine()
	// First spawn per persona votes the blind round; the reseated spawn
	// votes the final round (fresh child, no inbox turn).
	spawnsPerPersona := map[string]int{}
	e.onSpawn = func(u *fakeUnit, req swarm.SpawnRequest) {
		e.mu.Lock()
		n := spawnsPerPersona[req.Persona]
		spawnsPerPersona[req.Persona] = n + 1
		e.mu.Unlock()
		if n == 0 {
			u.completeTurn(ballotText("reject", 0.7, "blind doubts"), "")
		} else {
			u.completeTurn(ballotText("approve", 0.9, "persuaded on new weights"), "")
		}
	}
	perms := [][]int{{0, 1, 2}, {2, 0, 1}}
	permCalls := 0
	var mu sync.Mutex
	var seated []Event
	res, err := convene(t, e, func(c *Config) {
		c.Bindings = testPool
		c.SeatOrder = SeatOrderTurn
		c.Perm = func(n int) []int { p := perms[permCalls]; permCalls++; return p }
		c.OnEvent = func(ev Event) {
			if ev.Kind == EventSeated {
				mu.Lock()
				seated = append(seated, ev)
				mu.Unlock()
			}
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if permCalls != 2 {
		t.Errorf("perm drawn %d times, want once per round", permCalls)
	}
	if len(seated) != 6 {
		t.Fatalf("seated events = %d, want 3 seats + 3 reseats", len(seated))
	}
	// YATA-1's final-round seat draws pool[2] via the second perm.
	if prov, model := spawnedBinding(e, "raati-crew:yata", 1); prov != "openrouter" || model != "z.ai/glm-5.2" {
		t.Errorf("YATA-1 reseated on %s/%s, want pool[2]", prov, model)
	}
	// The cold prompt carries the seat's own provisional ballot and the
	// full question again (the fresh child has no history).
	e.mu.Lock()
	var cold string
	for _, req := range e.spawned[3:] {
		if req.Persona == "raati-crew:yata" {
			cold = req.Task
		}
	}
	e.mu.Unlock()
	for _, want := range []string{"provisional ballot", "reject", "blind doubts", "should we ship it?"} {
		if !strings.Contains(cold, want) {
			t.Errorf("cold reseat prompt missing %q", want)
		}
	}
	// The record points at both seatings.
	u0 := res.Units[0]
	if u0.Provider != "openrouter" || u0.BlindProvider != "openai-codex" || u0.BlindAgentID == "" {
		t.Errorf("record seatings = %+v, want final openrouter with blind openai-codex pointer", u0)
	}
	// And the revision itself happened: blind reject, final approve.
	if res.Blind[0].Verdict != vote.Reject || res.Final[0].Verdict != vote.Approve {
		t.Errorf("ballots: blind %s final %s", res.Blind[0].Verdict, res.Final[0].Verdict)
	}
	if res.Outcome.Decision != vote.Approved {
		t.Errorf("Decision = %q, want approved", res.Outcome.Decision)
	}
}

func TestSeatPoolValidation(t *testing.T) {
	e := newFakeEngine()
	if _, err := convene(t, e, func(c *Config) { c.Bindings = testPool[:2] }); err == nil {
		t.Errorf("pool size mismatch must be rejected")
	}
	if _, err := convene(t, e, func(c *Config) {
		c.Bindings = []Binding{{Provider: "a", Model: "m"}, {Provider: "b"}, {Provider: "c", Model: "m"}}
	}); err == nil {
		t.Errorf("half-empty pool entry must be rejected")
	}
	for _, m := range [][]int{{0, 1}, {0, 1, 1}, {0, 1, 3}} {
		if _, err := convene(t, e, func(c *Config) { c.Bindings = testPool; c.SeatOrder = SeatOrderFixed; c.SeatMap = m }); err == nil {
			t.Errorf("seat_map %v must be rejected", m)
		}
	}
	if _, err := ParseSeatOrder("chaotic"); err == nil {
		t.Errorf("unknown seat order must be rejected")
	}
}

func TestConveneEventFeed(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
		map[string]string{"raati-crew:yata": "approve", "raati-crew:kusanagi": "approve", "raati-crew:magatama": "reject"},
	)
	var mu sync.Mutex
	var feed []Event
	_, err := convene(t, e, func(c *Config) {
		c.OnEvent = func(ev Event) {
			mu.Lock()
			feed = append(feed, ev)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	var kinds []string
	for _, ev := range feed {
		kinds = append(kinds, string(ev.Kind))
	}
	want := []string{
		"seated", "seated", "seated",
		"round", "voted", "voted", "voted",
		"round", "voted", "voted", "voted",
		"decided",
	}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("feed kinds = %v, want %v", kinds, want)
	}
	if feed[3].Round != 1 || feed[7].Round != 2 {
		t.Errorf("round events carry rounds %d,%d; want 1,2", feed[3].Round, feed[7].Round)
	}
	if feed[4].Ballot == nil || feed[4].Ballot.Verdict != vote.Approve || feed[4].Round != 1 {
		t.Errorf("first voted event = %+v, want round-1 approve with ballot", feed[4])
	}
	last := feed[len(feed)-1]
	if last.Outcome == nil || last.Outcome.Decision != vote.Approved {
		t.Errorf("decided event = %+v, want the approved outcome", last)
	}
	for _, ev := range feed[:3] {
		if ev.Kind != EventSeated || ev.Unit == "" || ev.AgentID == "" {
			t.Errorf("seated event %+v missing unit/agent id", ev)
		}
	}
}

func TestConveneAbsentEventFiresOnce(t *testing.T) {
	e := newFakeEngine()
	scriptRounds(e,
		map[string]string{ /* yata never answers */ "raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
		map[string]string{"raati-crew:kusanagi": "approve", "raati-crew:magatama": "approve"},
	)
	var mu sync.Mutex
	absents := 0
	_, err := convene(t, e, func(c *Config) {
		c.RoundTimeout = 100 * time.Millisecond
		c.OnEvent = func(ev Event) {
			if ev.Kind == EventAbsent {
				mu.Lock()
				absents++
				mu.Unlock()
				if ev.Unit != "YATA-1" || ev.Round != 1 || ev.Why == "" {
					t.Errorf("absent event = %+v, want YATA-1 in round 1 with a cause", ev)
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if absents != 1 {
		t.Errorf("absent events = %d, want exactly one for the sticky absence", absents)
	}
}

// TestRevealRedactsConfidence pins the anti-anchoring rule: the
// cross-examination reveal carries verdicts and rationales, never the
// confidence numbers (numeric anchors pull the panel's certainty
// toward its mean — observed live before this rule).
func TestRevealRedactsConfidence(t *testing.T) {
	revealed := []vote.Ballot{
		{Unit: "KUSANAGI-2", Verdict: vote.Approve, Confidence: 0.83, Rationale: "acting is survivable"},
		{Unit: "MAGATAMA-3", Verdict: vote.Reject, Confidence: 0.67, Rationale: "users carry the cost"},
	}
	out := round2Prompt(Unit{Name: "YATA-1"}, revealed, "", false)
	for _, leak := range []string{"0.83", "0.67", "confidence 0"} {
		if strings.Contains(out, leak) {
			t.Errorf("reveal leaks a confidence anchor %q:\n%s", leak, out)
		}
	}
	for _, must := range []string{"KUSANAGI-2", "acting is survivable", "users carry the cost"} {
		if !strings.Contains(out, must) {
			t.Errorf("reveal is missing %q", must)
		}
	}
}

func TestParseClass(t *testing.T) {
	for in, want := range map[string]Class{"": ClassAdvisory, "advisory": ClassAdvisory, "GATE": ClassGate, " veto ": ClassVeto} {
		got, err := ParseClass(in)
		if err != nil || got != want {
			t.Errorf("ParseClass(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseClass("tribunal"); err == nil {
		t.Errorf("ParseClass must reject unknown classes")
	}
}

func TestParseBallot(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		want    vote.Verdict
		conf    float64
		wantErr bool
	}{
		{"fenced", ballotText("approve", 0.8, "ok"), vote.Approve, 0.8, false},
		{"uppercase verdict", ballotText("REJECT", 0.5, "no"), vote.Reject, 0.5, false},
		{"last fence wins", ballotText("reject", 0.4, "old") + ballotText("approve", 0.9, "new"), vote.Approve, 0.9, false},
		{"unterminated fence", "prose\n```ballot\n{\"verdict\":\"abstain\",\"confidence\":0.3,\"rationale\":\"unsure\"}", vote.Abstain, 0.3, false},
		{"bare json fallback", `I vote thus: {"verdict":"approve","confidence":0.7,"rationale":"fine"}`, vote.Approve, 0.7, false},
		{"confidence clamped high", ballotText("approve", 1.7, "sure"), vote.Approve, 1, false},
		{"confidence clamped low", ballotText("approve", -0.4, "eh"), vote.Approve, 0, false},
		{"bare json without verdict", `{"mood":"grim"}`, "", 0, true},
		{"no ballot at all", "I decline.", "", 0, true},
		{"invalid verdict word", ballotText("yes", 0.9, "sure"), "", 0, true},
		{"broken json in fence", "```ballot\n{verdict: approve}\n```", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _, err := parseBallot("YATA-1", tc.text)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if b.Verdict != tc.want || b.Confidence != tc.conf || b.Unit != "YATA-1" {
				t.Errorf("ballot = %+v, want %s @ %v", b, tc.want, tc.conf)
			}
		})
	}
}

// scriptQueues wires an engine where the blind round votes per the
// blind map and each later SendUserTurn pops the persona's next queued
// reply — round-aware scripting for convergence tests.
func scriptQueues(e *fakeEngine, blind map[string]string, queues map[string][]string) {
	e.onSpawn = func(u *fakeUnit, req swarm.SpawnRequest) {
		if v, ok := blind[req.Persona]; ok {
			u.completeTurn(v, "")
		}
	}
	var mu sync.Mutex
	e.onTurn = func(u *fakeUnit, _ string) {
		persona := strings.TrimPrefix(u.AgentID(), "agent-")
		mu.Lock()
		q := queues[persona]
		var v string
		if len(q) > 0 {
			v, queues[persona] = q[0], q[1:]
		}
		mu.Unlock()
		if v != "" {
			u.completeTurn(v, "")
		}
	}
}

func ballotWithInquiries(verdict string, inqs ...string) string {
	quoted := make([]string, len(inqs))
	for i, q := range inqs {
		quoted[i] = fmt.Sprintf("%q", q)
	}
	return fmt.Sprintf("prose\n```ballot\n{\"verdict\": %q, \"confidence\": 0.8, \"rationale\": \"r\", \"inquiries\": [%s]}\n```\n",
		verdict, strings.Join(quoted, ","))
}

// TestConveneInquiryDigest: blind-round questions are resolved in the
// inquiry gap and the pooled digest (answers AND recorded gaps) rides
// every round-2 prompt; the record and event feed carry each entry.
func TestConveneInquiryDigest(t *testing.T) {
	e := newFakeEngine()
	scriptQueues(e,
		map[string]string{
			"raati-crew:yata":     ballotWithInquiries("approve", "what does migration cost?"),
			"raati-crew:kusanagi": ballotText("approve", 0.8, "fine"),
			"raati-crew:magatama": ballotWithInquiries("reject", "who maintains it?"),
		},
		map[string][]string{
			"raati-crew:yata":     {ballotText("approve", 0.9, "f")},
			"raati-crew:kusanagi": {ballotText("approve", 0.9, "f")},
			"raati-crew:magatama": {ballotText("reject", 0.9, "f")},
		})
	var events []Event
	res, err := convene(t, e, func(c *Config) {
		c.AnswerInquiries = func(_ context.Context, qs []Inquiry) []Inquiry {
			for i := range qs {
				if strings.Contains(qs[i].Question, "migration") {
					qs[i].Answer, qs[i].Source = "about two days", SourceRecord
				}
			}
			return qs
		}
		prev := c.OnEvent
		c.OnEvent = func(ev Event) {
			events = append(events, ev)
			if prev != nil {
				prev(ev)
			}
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if len(res.Inquiries) != 2 {
		t.Fatalf("Inquiries = %+v, want 2 entries", res.Inquiries)
	}
	byQ := map[string]Inquiry{}
	for _, q := range res.Inquiries {
		byQ[q.Question] = q
	}
	if q := byQ["what does migration cost?"]; q.Source != SourceRecord || q.Answer != "about two days" || q.Unit != "YATA-1" || q.Round != 1 {
		t.Errorf("answered inquiry = %+v", q)
	}
	if q := byQ["who maintains it?"]; q.Source != SourceUnanswered || q.Answer != "" {
		t.Errorf("unanswered inquiry = %+v", q)
	}
	// The round-1 spawn solicits; the round-2 prompt carries the digest
	// (answer + recorded gap) but does NOT solicit at max_rounds 2.
	for _, req := range e.spawned {
		if !strings.Contains(req.Task, "inquiries") {
			t.Errorf("round-1 prompt for %s does not solicit inquiries", req.Persona)
		}
	}
	turn := e.turnsFor("agent-raati-crew:kusanagi")
	if len(turn) != 1 {
		t.Fatalf("kusanagi turns = %d, want 1", len(turn))
	}
	for _, must := range []string{"about two days", "what does migration cost?", "who maintains it?", "YATA-1"} {
		if !strings.Contains(turn[0], must) {
			t.Errorf("round-2 prompt missing %q", must)
		}
	}
	if strings.Contains(turn[0], "you may add an `inquiries` array") {
		t.Errorf("round-2 prompt solicits inquiries at max_rounds 2")
	}
	inqEvents := 0
	for _, ev := range events {
		if ev.Kind == EventInquiry {
			inqEvents++
			if ev.Why == "" || ev.Source == "" {
				t.Errorf("inquiry event missing fields: %+v", ev)
			}
		}
	}
	if inqEvents != 2 {
		t.Errorf("inquiry events = %d, want 2", inqEvents)
	}
}

// TestConveneConvergenceRound: a cross-examination flip triggers ONE
// extra reveal round at max_rounds 3; the record keeps the middle
// ballots and the final tally reflects the converged configuration.
func TestConveneConvergenceRound(t *testing.T) {
	e := newFakeEngine()
	scriptQueues(e,
		map[string]string{
			"raati-crew:yata":     ballotText("approve", 0.8, "b"),
			"raati-crew:kusanagi": ballotText("approve", 0.8, "b"),
			"raati-crew:magatama": ballotText("reject", 0.8, "b"),
		},
		map[string][]string{
			// round 2: magatama flips to approve; round 3: everyone holds
			"raati-crew:yata":     {ballotText("approve", 0.9, "r2"), ballotText("approve", 0.9, "r3")},
			"raati-crew:kusanagi": {ballotText("approve", 0.9, "r2"), ballotText("approve", 0.9, "r3")},
			"raati-crew:magatama": {ballotText("approve", 0.7, "persuaded"), ballotText("approve", 0.8, "held")},
		})
	var rounds []int
	res, err := convene(t, e, func(c *Config) {
		c.MaxRounds = 3
		c.OnEvent = func(ev Event) {
			if ev.Kind == EventRound {
				rounds = append(rounds, ev.Round)
			}
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if want := []int{1, 2, 3}; fmt.Sprint(rounds) != fmt.Sprint(want) {
		t.Fatalf("rounds = %v, want %v", rounds, want)
	}
	if len(res.Middle) != 3 {
		t.Fatalf("Middle = %+v, want the round-2 ballots", res.Middle)
	}
	if res.Middle[2].Rationale != "persuaded" || res.Final[2].Rationale != "held" {
		t.Errorf("revision path wrong: middle %+v final %+v", res.Middle[2], res.Final[2])
	}
	if res.Outcome.Decision != vote.Approved || res.Outcome.Tally.Approve != 3 {
		t.Errorf("outcome = %+v, want unanimous approval", res.Outcome)
	}
	// The convergence turn carries the stabilization framing.
	turns := e.turnsFor("agent-raati-crew:yata")
	if len(turns) != 2 {
		t.Fatalf("yata turns = %d, want 2", len(turns))
	}
	if !strings.Contains(turns[1], "CONVERGENCE") {
		t.Errorf("round-3 prompt missing convergence framing:\n%s", turns[1])
	}
}

// TestConveneNoFlipNoThirdRound: max_rounds 3 spends nothing when
// cross-examination holds every verdict.
func TestConveneNoFlipNoThirdRound(t *testing.T) {
	e := newFakeEngine()
	scriptQueues(e,
		map[string]string{
			"raati-crew:yata":     ballotText("approve", 0.8, "b"),
			"raati-crew:kusanagi": ballotText("approve", 0.8, "b"),
			"raati-crew:magatama": ballotText("reject", 0.8, "b"),
		},
		map[string][]string{
			"raati-crew:yata":     {ballotText("approve", 0.9, "r2")},
			"raati-crew:kusanagi": {ballotText("approve", 0.9, "r2")},
			"raati-crew:magatama": {ballotText("reject", 0.9, "r2")},
		})
	res, err := convene(t, e, func(c *Config) { c.MaxRounds = 3 })
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if res.Middle != nil {
		t.Errorf("Middle = %+v for a flip-less deliberation", res.Middle)
	}
	if got := len(e.turnsFor("agent-raati-crew:yata")); got != 1 {
		t.Errorf("yata turns = %d, want 1 (no convergence round)", got)
	}
}

// TestConveneSingleRoundInquiriesRecordedOpen: with no later round to
// consume answers, posed questions land on the record as unanswered —
// the decision was made with these open.
func TestConveneSingleRoundInquiriesRecordedOpen(t *testing.T) {
	e := newFakeEngine()
	scriptQueues(e,
		map[string]string{
			"raati-crew:yata":     ballotWithInquiries("approve", "is there budget?"),
			"raati-crew:kusanagi": ballotText("approve", 0.8, "b"),
			"raati-crew:magatama": ballotText("approve", 0.8, "b"),
		}, nil)
	res, err := convene(t, e, func(c *Config) {
		c.SingleRound = true
		c.AnswerInquiries = func(_ context.Context, qs []Inquiry) []Inquiry {
			t.Errorf("AnswerInquiries called with no round left to inform")
			return qs
		}
	})
	if err != nil {
		t.Fatalf("Convene: %v", err)
	}
	if len(res.Inquiries) != 1 || res.Inquiries[0].Source != SourceUnanswered {
		t.Errorf("Inquiries = %+v, want one open question", res.Inquiries)
	}
}

// TestParseBallotInquiryCaps: at most two questions per ballot, each
// bounded, blanks dropped.
func TestParseBallotInquiryCaps(t *testing.T) {
	long := strings.Repeat("x", 400)
	_, inqs, err := parseBallot("YATA-1", ballotWithInquiries("approve", "  ", "one?", long, "three?"))
	if err != nil {
		t.Fatalf("parseBallot: %v", err)
	}
	if len(inqs) != 2 {
		t.Fatalf("inquiries = %v, want 2 (cap)", inqs)
	}
	if inqs[0] != "one?" || len(inqs[1]) != 300 {
		t.Errorf("caps wrong: %q, len %d", inqs[0], len(inqs[1]))
	}
}
