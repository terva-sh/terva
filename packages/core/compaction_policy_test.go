package core

import (
	"context"
	"errors"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The default policy's whole table: which points each mode allows, and the
// threshold. `off` refuses every automatic point including the oversize retry;
// `turns` refuses only the mid-turn one. A requested compaction is always yes,
// and a prefix-change offer is never made: that is a host's to ask for.
func TestTheDefaultPolicyDecisions(t *testing.T) {
	mode := func(m AutoCompactMode) func() AutoCompactMode { return func() AutoCompactMode { return m } }
	cases := []struct {
		mode  AutoCompactMode
		point CompactPoint
		frac  float64
		want  bool
	}{
		{AutoCompactSteps, CompactBeforeTurn, 0.85, true},
		{AutoCompactSteps, CompactBeforeTurn, 0.84, false},
		{AutoCompactSteps, CompactMidTurn, 0.90, true},
		{AutoCompactSteps, CompactAfterTurn, 0.90, true},
		{AutoCompactSteps, CompactOversize, 0, true},
		{AutoCompactTurns, CompactMidTurn, 0.99, false},
		{AutoCompactTurns, CompactAfterTurn, 0.90, true},
		{AutoCompactTurns, CompactOversize, 0, true},
		{AutoCompactOff, CompactBeforeTurn, 0.99, false},
		{AutoCompactOff, CompactMidTurn, 0.99, false},
		{AutoCompactOff, CompactAfterTurn, 0.99, false},
		{AutoCompactOff, CompactOversize, 0, false},
		{AutoCompactOff, CompactRequested, 0, true},
		{AutoCompactSteps, CompactPrefixChanged, 0.99, false},
		{AutoCompactTurns, CompactPrefixChanged, 0.99, false},
	}
	for _, c := range cases {
		d := DefaultCompactionPolicy{Mode: mode(c.mode)}.Decide(CompactionState{Point: c.point, Fraction: c.frac, Messages: 10})
		if d.Compact != c.want {
			t.Errorf("%s at %s, %.2f: Compact = %v, want %v", c.mode, c.point, c.frac, d.Compact, c.want)
		}
		if d.KeepTail != AutoCompactKeepTail {
			t.Errorf("%s at %s: KeepTail = %d, want %d", c.mode, c.point, d.KeepTail, AutoCompactKeepTail)
		}
	}
	custom := DefaultCompactionPolicy{Threshold: 0.5, KeepTail: 2}.Decide(CompactionState{Point: CompactAfterTurn, Fraction: 0.6})
	if !custom.Compact || custom.KeepTail != 2 {
		t.Errorf("a custom threshold and keep-tail were not used: %+v", custom)
	}
}

// recordingPolicy answers every point with no, except the ones listed, and
// records the points and states it was asked at.
type recordingPolicy struct {
	yes map[CompactPoint]bool

	mu     sync.Mutex
	points []CompactPoint
	states []CompactionState
}

func (p *recordingPolicy) Decide(s CompactionState) CompactionDecision {
	p.mu.Lock()
	p.points = append(p.points, s.Point)
	p.states = append(p.states, s)
	p.mu.Unlock()
	return CompactionDecision{Compact: p.yes[s.Point], KeepTail: 2}
}

func (p *recordingPolicy) asked(point CompactPoint) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.points {
		if x == point {
			return true
		}
	}
	return false
}

// The engine asks the policy at every point it used to decide by itself:
// before the turn, between tool steps, and after an oversized request. No
// threshold or mode is read on the way.
func TestTheEngineAsksThePolicyAtEachPoint(t *testing.T) {
	client := &midTurnFakeClient{steps: []midTurnStep{
		{usageInput: 190_000, toolCall: true},
		{usageInput: 195_000, toolCall: false},
	}}
	a := newTestAgent(client, "claude-sonnet-4-5", "system", Registry{"noop": noopTool{}})
	pol := &recordingPolicy{}
	a.compactionPolicy = pol
	seedSmallTranscript(a, 8)
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000})

	if err := a.PromptWithPolicy(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy: %v", err)
	}
	for _, p := range []CompactPoint{CompactBeforeTurn, CompactMidTurn} {
		if !pol.asked(p) {
			t.Errorf("the policy was never asked at %s", p)
		}
	}
	if client.compactCalls != 0 {
		t.Errorf("compacted %d time(s) although the policy said no everywhere", client.compactCalls)
	}
	pol.mu.Lock()
	s := pol.states[0]
	pol.mu.Unlock()
	if s.Used != 190_000 || s.Window == 0 || s.Messages != 8 || s.Fraction <= 0 {
		t.Errorf("the policy was not told the state: %+v", s)
	}

	failing := &policyFakeClient{
		firstErr: &provider.ProviderError{Provider: "policy-fake", Status: 400, Msg: "maximum context length exceeded"},
	}
	b := newTestAgent(failing, "fake-model", "system", Registry{})
	bp := &recordingPolicy{}
	b.compactionPolicy = bp
	seedSmallTranscript(b, 8)
	if err := b.PromptWithPolicy(context.Background(), "go", nil, nil); err == nil || !isContextLengthError(err) {
		t.Fatalf("with the policy refusing the retry the error must surface, got %v", err)
	}
	if !bp.asked(CompactOversize) {
		t.Error("the policy was never asked whether to retry an oversized request")
	}
}

// The strategies in a decision choose the summarizer, a nil list is cold
// alone, and a list with no strategy that can finish fails by name rather than
// falling back to one the policy did not allow.
func TestTheDecisionsStrategiesChooseTheSummarizer(t *testing.T) {
	summarizes := func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}
	only := func(s ...CompactStrategy) *DefaultCompactionPolicy {
		return &DefaultCompactionPolicy{Strategies: s}
	}
	compacted := func(t *testing.T, a *Agent) CompactStrategy {
		t.Helper()
		if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
			t.Fatal(err)
		}
		res, err := a.Compact(context.Background(), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Strategy
	}

	// The default an embedder gets, and the reason: warm and the backend's
	// compaction are trades a host must choose, not ones the engine makes.
	t.Run("nil is cold alone", func(t *testing.T) {
		a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: summarizes})
		a.compactionPolicy = only()
		if got := compacted(t, a); got != CompactCold {
			t.Errorf("Strategy = %q; a nil list allows cold alone", got)
		}
	})

	t.Run("cold only", func(t *testing.T) {
		a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: summarizes})
		a.compactionPolicy = only(CompactCold)
		if got := compacted(t, a); got != CompactCold {
			t.Errorf("Strategy = %q; the policy allowed only cold", got)
		}
	})

	t.Run("warm, then cold", func(t *testing.T) {
		a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: summarizes})
		a.compactionPolicy = only(CompactWarm, CompactCold)
		if got := compacted(t, a); got != CompactWarm {
			t.Errorf("Strategy = %q; the policy asked for warm", got)
		}
	})

	t.Run("warm only, and warm fails", func(t *testing.T) {
		a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
			if n == 0 {
				return saidText("working on it", 100), nil
			}
			return calledATool(5_000), nil
		}})
		a.compactionPolicy = only(CompactWarm)
		if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Compact(context.Background(), 0, nil); !errors.Is(err, ErrNoCompactionStrategy) {
			t.Errorf("err = %v; want ErrNoCompactionStrategy, not a cold fallback the policy did not allow", err)
		}
	})
}

// CompactIfDue is the call a host makes after a turn. When the policy says no
// it does nothing at all; when it says yes it compacts, with the policy's
// keep-tail, and announces start and end.
func TestCompactIfDue(t *testing.T) {
	client := &midTurnFakeClient{}
	a := newTestAgent(client, "claude-sonnet-4-5", "system", Registry{})
	pol := &recordingPolicy{}
	a.compactionPolicy = pol
	seedSmallTranscript(a, 8)

	var events []AgentEvent
	sink := func(ev AgentEvent) { events = append(events, ev) }
	if _, ran, err := a.CompactIfDue(context.Background(), CompactAfterTurn, sink); ran || err != nil || len(events) != 0 {
		t.Fatalf("not due: ran %v, err %v, %d events; want nothing", ran, err, len(events))
	}

	pol.yes = map[CompactPoint]bool{CompactAfterTurn: true}
	_, ran, err := a.CompactIfDue(context.Background(), CompactAfterTurn, sink)
	if !ran || err != nil {
		t.Fatalf("due: ran %v, err %v", ran, err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want compact start and end", len(events))
	}
	if _, ok := events[0].(EvCompactStart); !ok {
		t.Errorf("first event is %T, want EvCompactStart", events[0])
	}
	if end, ok := events[1].(EvCompactEnd); !ok || end.Err != "" {
		t.Errorf("second event is %+v, want a clean EvCompactEnd", events[1])
	}
	if client.compactCalls != 1 {
		t.Errorf("compact calls = %d, want 1", client.compactCalls)
	}
	// The summary plus the policy's keep-tail of 2.
	if n := len(a.Messages()); n != 3 {
		t.Errorf("transcript has %d messages after compaction, want the summary and the policy's keep-tail of 2", n)
	}

	// A transcript no longer than the keep-tail has nothing to summarize, so
	// the engine's own check says no even though the policy says yes.
	a.SetMessages(a.Messages()[:2])
	if _, ran, _ := a.CompactIfDue(context.Background(), CompactAfterTurn, nil); ran {
		t.Error("compacted a transcript that is all keep-tail")
	}
}

// pointPolicy says yes at the listed points only, and gives each point its own
// strategies, so a test can see which point's decision a compaction used.
type pointPolicy struct {
	yes        map[CompactPoint]bool
	strategies map[CompactPoint][]CompactStrategy
}

func (p pointPolicy) Decide(s CompactionState) CompactionDecision {
	return CompactionDecision{Compact: p.yes[s.Point], KeepTail: 0, Strategies: p.strategies[s.Point]}
}

// A host that asks for a point's decision and runs the compaction its own way
// gets that point's strategies, not the requested-compaction ones. The daemon
// compacts after a turn like this, and a policy that varies strategies by point
// must see them honoured there.
func TestCompactWithUsesTheDecisionsStrategies(t *testing.T) {
	summarizes := func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}
	a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: summarizes})
	a.compactionPolicy = pointPolicy{strategies: map[CompactPoint][]CompactStrategy{
		CompactAfterTurn: {CompactCold},
		CompactRequested: {CompactWarm, CompactCold},
	}}
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.CompactWith(context.Background(), a.Compaction(CompactAfterTurn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != CompactCold {
		t.Errorf("Strategy = %q; the after-turn decision allowed only cold", res.Strategy)
	}
}
