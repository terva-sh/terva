package contextpressure

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// host is the smallest host that connects a tracker: a system prompt, the
// tracker's segment, and delivery forwarded to it.
type host struct{ t *Tracker }

func (h *host) Assemble(core.AssembleMode) core.Frame {
	return core.Frame{Segments: []core.Segment{
		{Stability: core.Stable, Tag: "system", Content: "sys"},
		h.t.Segment(),
	}}
}

func (h *host) TailDelivered(ids []string) { h.t.TailDelivered(ids) }

// reqCaptureClient answers every request and keeps its tail and tool list.
type reqCaptureClient struct {
	mu        sync.Mutex
	ephemeral []string
	tools     [][]provider.Tool
}

func (c *reqCaptureClient) Name() string { return "reqcap" }

func (c *reqCaptureClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.ephemeral = append(c.ephemeral, req.EphemeralContext)
	c.tools = append(c.tools, req.Tools)
	c.mu.Unlock()
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

func (c *reqCaptureClient) tails() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ephemeral...)
}

// newTestAgent builds an agent whose host carries a tracker, connected as a
// component.
func newTestAgent(c provider.Client, model string, tools core.Registry, opts ...core.Option) *core.Agent {
	t := &Tracker{}
	a, err := core.New(c, model, append([]core.Option{core.WithAssembler(&host{t: t}), core.WithTools(tools),
		core.WithGate(core.AllowAll), core.WithComponent(t)}, opts...)...)
	if err != nil {
		panic(err)
	}
	return a
}

// pressureRun drives n turns at a fixed context usage and reports how many of
// them carried the note. Driven through Prompt rather than by calling Segment
// directly: the cadence is only correct if the frame consults it AND the
// engine reports delivery afterwards, and testing the helper alone would prove
// neither.
func pressureRun(t *testing.T, a *core.Agent, client *reqCaptureClient, used, turns int) int {
	t.Helper()
	before := len(client.tails())
	for i := 0; i < turns; i++ {
		a.SeedLastTurnUsage(provider.Usage{InputTokens: used})
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}
	carried := 0
	for _, e := range client.tails()[before:] {
		if strings.Contains(e, "[context pressure]") {
			carried++
		}
	}
	return carried
}

// The regression, in the numbers it was measured in. A real session put the
// note on 74 of 407 requests — 18% — because past the threshold it rode every
// single one. Sitting at one level must now cost a handful of reminders, not a
// note per request.
func TestSittingAtOneLevelDoesNotWarnEveryRequest(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	// 195k of a 272k window = 71%: past the warn line, inside the first band.
	carried := pressureRun(t, a, client, 195_000, 30)

	if carried == 30 {
		t.Fatal("the note rode every request — this is the level-trigger behaviour the change removes")
	}
	// One on entry, then the repeat interval — band 1's, which is the slowest on
	// the ladder because 71% is the case with the most room to react. Anything
	// much denser is the old behaviour wearing a hat.
	if want := 1 + 30/repeatEvery(1); carried > want {
		t.Errorf("note carried %d times over 30 requests, want at most %d", carried, want)
	}
	if carried == 0 {
		t.Error("the note never rode at all — the model is now never warned")
	}
}

// The other half of the cadence: sitting in one band still gets reminded, once
// the band's interval has passed. A model many turns past the only warning
// behaves like one that was never told.
func TestSittingInABandIsRemindedAfterTheInterval(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	// 71%: band 1. The entry warning, then silence for the interval, then one
	// reminder.
	turns := 2 + repeatEvery(1)
	if carried := pressureRun(t, a, client, 195_000, turns); carried != 2 {
		t.Errorf("over %d requests in one band the note rode %d times, want 2: the entry and one reminder", turns, carried)
	}
}

// Entering the band must say so immediately. A warning that waits for an
// interval is a warning that arrives after the expensive read it existed to
// prevent.
func TestCrossingIntoTheBandWarnsAtOnce(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	if carried := pressureRun(t, a, client, 195_000, 1); carried != 1 {
		t.Errorf("the first request past the threshold carried the note %d times, want 1", carried)
	}
}

// Escalation is news even mid-interval: 71% and 86% are different situations,
// and the second must not be silenced by having recently mentioned the first.
func TestClimbingIntoANewBandWarnsAgainImmediately(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	pressureRun(t, a, client, 195_000, 1) // enter band 1
	if carried := pressureRun(t, a, client, 220_000, 1); carried != 1 {
		t.Errorf("crossing into the 80%% band did not warn (carried %d)", carried)
	}
	if carried := pressureRun(t, a, client, 235_000, 1); carried != 1 {
		t.Errorf("crossing into the 85%% band did not warn (carried %d)", carried)
	}
}

// The flapping this replaces: the gauge is the last request's input count and
// does not move monotonically, so a transcript hovering at the line crossed it
// repeatedly and the note appeared, vanished and returned. Hysteresis means a
// dip that never really relieved anything does not re-announce the same band.
func TestAGaugeHoveringOnTheLineDoesNotReAnnounce(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	pressureRun(t, a, client, 191_000, 1) // 70.2% — enters the band, warns
	// Jitter around the boundary: just under, then just over, repeatedly.
	carried := 0
	for i := 0; i < 4; i++ {
		carried += pressureRun(t, a, client, 189_000, 1) // 69.5%
		carried += pressureRun(t, a, client, 191_000, 1) // 70.2%
	}
	if carried > 0 {
		t.Errorf("jittering across the threshold re-announced the same band %d times", carried)
	}
}

// ...but a real recovery must reset, or a session that compacts and then climbs
// all the way back would never be warned a second time.
func TestACompactionThatRelievesPressureRearmsTheWarning(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	pressureRun(t, a, client, 195_000, 1) // warned
	pressureRun(t, a, client, 40_000, 1)  // a compaction lands: 15%
	if carried := pressureRun(t, a, client, 195_000, 1); carried != 1 {
		t.Errorf("after real relief the climb back past the line did not warn (carried %d)", carried)
	}
}

// Below the line, nothing — the note must not leak into ordinary turns.
func TestBelowTheThresholdNothingIsSaid(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})

	if carried := pressureRun(t, a, client, 50_000, 5); carried != 0 {
		t.Errorf("a comfortable session was warned %d times", carried)
	}
}

// The note is a last-in-turn ephemeral block, the same shape as the
// inactive-groups note that ended 80 of 80 transcripts with the model answering
// the NOTE instead of the user. The review that produced the band ladder
// recorded this note's own version of that failure — a model "narrating its
// context budget back at the user" — and prohibition-first is what measured
// 20-of-20 answers back on the sibling note. A prohibition buried after the
// detail it governs loses completely on a weak model, so ORDER is the assertion.
// This holds the neutral default to it; terva's own note is held to it in
// packages/agent/build.
func TestTheNoteLeadsWithTheDoNotReplyGuard(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})
	pressureRun(t, a, client, 195_000, 1)

	eph := client.tails()[0]
	guard := strings.Index(eph, "Do not reply to this note")
	if guard < 0 {
		t.Fatalf("the note carries no do-not-reply guard: %q", eph)
	}
	gauge := strings.Index(eph, "% full")
	if gauge < 0 {
		t.Fatalf("the note carries no gauge: %q", eph)
	}
	if guard > gauge {
		t.Error("the gauge precedes the prohibition; prohibition-first is the measured ordering")
	}
}

// bandNoter records the state the tracker hands the policy's note.
type bandNoter struct {
	core.DefaultCompactionPolicy
	got []State
}

func (n *bandNoter) PressureNote(s State) string {
	n.got = append(n.got, s)
	return "[context pressure] band note"
}

// The tracker decides when the note rides and tells the policy where on the
// ladder the window sits, so a host can graduate its words by band. The band it
// is told is the band the fraction falls in, and the words it returns are what
// the request carries.
func TestThePolicyIsToldTheBandItWords(t *testing.T) {
	client := &reqCaptureClient{}
	n := &bandNoter{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{}, core.WithCompactionPolicy(n))
	pressureRun(t, a, client, 235_000, 1) // 86% — band 3
	if len(n.got) == 0 {
		t.Fatal("the policy was never asked for the note")
	}
	s := n.got[len(n.got)-1]
	if s.Band != 3 || s.Used != 235_000 || s.Window == 0 || !s.Compacts {
		t.Errorf("the policy was told %+v; want band 3 of a real window, with compaction on", s)
	}
	if !strings.Contains(client.tails()[0], "band note") {
		t.Errorf("the request did not carry the policy's words: %q", client.tails()[0])
	}
}

func TestBandBoundaries(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		want int
	}{
		{0.00, 0}, {0.69, 0}, {0.70, 1}, {0.77, 1},
		{0.78, 2}, {0.84, 2}, {0.85, 3}, {0.91, 3},
		{0.92, 4}, {1.00, 4},
	} {
		if got := bandOf(tc.f); got != tc.want {
			t.Errorf("bandOf(%.2f) = %d, want %d", tc.f, got, tc.want)
		}
	}
}

// Two rungs of the ladder are not free parameters. The first is what
// TailDelivered clears against, so a ladder that starts anywhere else
// would arm and disarm at different fractions and re-announce forever; and the
// band the note calls "terva compacts when this turn ends" has to be the band
// where terva actually does that. Both couplings live in prose in two files,
// which is exactly the kind that rots.
func TestLadderMatchesPolicy(t *testing.T) {
	if got := bands[0].at; got != WarnFraction {
		t.Errorf("first band is %.2f, WarnFraction is %.2f — the clear margin is measured off the second", got, WarnFraction)
	}
	// Band 3 is the one terva's note (build.CompactionPolicy) switches its
	// closing sentence on.
	if got := bands[2].at; got != core.AutoCompactThreshold {
		t.Errorf("band 3 is %.2f, AutoCompactThreshold is %.2f — the note would promise a valve at the wrong fraction", got, core.AutoCompactThreshold)
	}
}

// The reminder interval must tighten as the window fills. A flat interval was
// the old behaviour and it is wrong at one end or the other: the same cadence
// cannot serve 71%, where there is a phase of work left, and 93%, where there
// are a few requests.
func TestReminderIntervalTightensWithPressure(t *testing.T) {
	for i := 1; i < len(bands); i++ {
		prev, cur := repeatEvery(i), repeatEvery(i+1)
		if cur >= prev {
			t.Errorf("band %d repeats every %d requests, band %d every %d — the ladder must get more urgent, not less", i, prev, i+1, cur)
		}
	}
	// Off the ladder there is no interval to give, and returning a real one
	// would let a below-threshold request satisfy the cadence check.
	if got := repeatEvery(0); got != 0 {
		t.Errorf("repeatEvery(0) = %d, want 0", got)
	}
	if got := repeatEvery(len(bands) + 1); got != 0 {
		t.Errorf("contextPressureRepeatEvery past the ladder = %d, want 0", got)
	}
}

// A continue turn suppresses the whole tail, so the note it would have carried
// never reached the model. That must count as a request that did not carry it,
// not as the announcement: the next real request still has to say it.
func TestAContinueTurnDoesNotSpendTheAnnouncement(t *testing.T) {
	client := &prefillClient{}
	a := newTestAgent(client, "gpt-5.6-sol", core.Registry{})
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Tell me a story."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,"}}},
	})
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 195_000})
	if err := a.ContinueAssistant(context.Background(), nil); err != nil {
		t.Fatalf("ContinueAssistant: %v", err)
	}
	if got := client.last(); got.EphemeralContext != "" {
		t.Fatalf("the continue turn carried a tail, so this proves nothing: %q", got.EphemeralContext)
	}

	a.SeedLastTurnUsage(provider.Usage{InputTokens: 195_000})
	if err := a.Prompt(context.Background(), "go on", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := client.last().EphemeralContext; !strings.Contains(got, "[context pressure]") {
		t.Errorf("the announcement was spent on a continue turn the model never saw it in:\n%s", got)
	}
}

// prefillClient continues an assistant message and answers a prompt.
type prefillClient struct {
	mu   sync.Mutex
	reqs []provider.Request
}

func (c *prefillClient) Name() string { return "prefill-fake" }

func (c *prefillClient) Capabilities() provider.ClientCapabilities {
	return provider.ClientCapabilities{ContinuesAssistantPrefill: true}
}

func (c *prefillClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventTextDelta{Delta: " and on."}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: " and on."}},
		}}
	}()
	return out, nil
}

func (c *prefillClient) last() provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[len(c.reqs)-1]
}

// Moved from core with the note: past WarnFraction the tail carries the note,
// and below it nothing.
func TestThePressureNoteRidesTheTailPastTheLine(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "claude-sonnet-4-5", core.Registry{})
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 150_000}) // 75% of 200k
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if eph := client.tails()[0]; !strings.Contains(eph, "[context pressure]") || !strings.Contains(eph, "% full") {
		t.Fatalf("the tail is missing the pressure note: %q", eph)
	}

	client2 := &reqCaptureClient{}
	b := newTestAgent(client2, "claude-sonnet-4-5", core.Registry{})
	b.SeedLastTurnUsage(provider.Usage{InputTokens: 50_000}) // 25%
	if err := b.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if eph := client2.tails()[0]; strings.Contains(eph, "[context pressure]") {
		t.Fatalf("the note rode below the warn line: %q", eph)
	}
}

// With auto-compaction off the note must not promise a valve that is not
// there: it says nothing compacts the conversation.
func TestThePressureNoteRespectsAutoCompactOff(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "claude-sonnet-4-5", core.Registry{},
		core.WithCompactionPolicy(core.DefaultCompactionPolicy{Mode: func() core.AutoCompactMode { return core.AutoCompactOff }}))
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 150_000}) // 75% of 200k
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	eph := client.tails()[0]
	if !strings.Contains(eph, "[context pressure]") {
		t.Fatalf("the tail is missing the pressure note: %q", eph)
	}
	// The neutral default's words; terva's own are held to the same rule in
	// packages/agent/build.
	if strings.Contains(eph, "is compacted automatically") || !strings.Contains(eph, "Nothing compacts") {
		t.Errorf("an off-mode note should say nothing compacts the conversation: %q", eph)
	}
}

// The delegation nudge lives in the always-on swarm system addendum, which
// shapes the plan from turn one, NOT in the pressure note: by 70% it is too
// late to restructure the work, and the note must not bloat further.
func TestThePressureNoteCarriesNoDelegationHint(t *testing.T) {
	client := &reqCaptureClient{}
	a := newTestAgent(client, "claude-sonnet-4-5", core.Registry{"swarm_spawn": noopTool{}})
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 150_000}) // 75%
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	eph := client.tails()[0]
	if !strings.Contains(eph, "[context pressure]") {
		t.Fatalf("the pressure note is missing: %q", eph)
	}
	if strings.Contains(eph, "swarm_spawn") {
		t.Fatalf("a delegation hint rode the pressure note: %q", eph)
	}
}

// noopTool is a tool that does nothing.
type noopTool struct{}

func (noopTool) Name() string            { return "swarm_spawn" }
func (noopTool) Description() string     { return "spawn" }
func (noopTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (noopTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

// pointPolicy says yes at the listed points only.
type pointPolicy struct{ yes map[core.CompactPoint]bool }

func (p pointPolicy) Decide(s core.CompactionState) core.CompactionDecision {
	return core.CompactionDecision{Compact: p.yes[s.Point]}
}

// The note tells the model whether anything will relieve the window. A policy
// that compacts at any automatic point does, not only one that compacts after a
// turn.
func TestAPolicyThatCompactsOnlyMidTurnStillCompactsOnItsOwn(t *testing.T) {
	if !compactsOnItsOwn(pointPolicy{yes: map[core.CompactPoint]bool{core.CompactMidTurn: true}}) {
		t.Error("a mid-turn-only policy was reported as never compacting")
	}
	if compactsOnItsOwn(pointPolicy{yes: map[core.CompactPoint]bool{core.CompactRequested: true, core.CompactOversize: true}}) {
		t.Error("a policy that compacts only on request was reported as compacting on its own")
	}
}
