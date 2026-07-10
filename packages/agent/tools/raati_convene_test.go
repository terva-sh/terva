package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/provider"
)

// fakeRaatiUnit is a minimal raati.UnitHandle: turns completed before
// a watcher is installed deliver at install (the real runner's
// buffered-watcher semantics), so tests never sleep.
type fakeRaatiUnit struct {
	id string

	mu       sync.Mutex
	cb       func(int, string)
	pending  bool
	findings string
	done     chan struct{}
}

func (u *fakeRaatiUnit) AgentID() string { return u.id }
func (u *fakeRaatiUnit) SetOnTurnEnd(fn func(int, string)) {
	u.mu.Lock()
	u.cb = fn
	deliver := fn != nil && u.pending
	if deliver {
		u.pending = false
	}
	u.mu.Unlock()
	if deliver {
		fn(1, "")
	}
}
func (u *fakeRaatiUnit) Wait()      { <-u.done }
func (u *fakeRaatiUnit) Err() error { return nil }
func (u *fakeRaatiUnit) Findings() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.findings
}
func (u *fakeRaatiUnit) vote(verdict string) {
	u.mu.Lock()
	u.findings = "Deliberation.\n\n```ballot\n{\"verdict\": \"" + verdict + "\", \"confidence\": 0.8, \"rationale\": \"because " + verdict + "\"}\n```\n"
	cb := u.cb
	if cb == nil {
		u.pending = true
	}
	u.mu.Unlock()
	if cb != nil {
		cb(1, "")
	}
}

// fakeRaatiEngine votes each persona per the votes map, instantly.
type fakeRaatiEngine struct {
	mu    sync.Mutex
	votes map[string]string // persona -> verdict
	units map[string]*fakeRaatiUnit
}

func (e *fakeRaatiEngine) SpawnUnit(_ context.Context, req swarm.SpawnRequest) (raati.UnitHandle, error) {
	e.mu.Lock()
	u := &fakeRaatiUnit{id: "agent-" + req.Persona, done: make(chan struct{})}
	e.units[u.id] = u
	v := e.votes[req.Persona]
	e.mu.Unlock()
	if v != "" {
		u.vote(v)
	}
	return u, nil
}
func (e *fakeRaatiEngine) SendUserTurn(id, _ string) error {
	e.mu.Lock()
	u := e.units[id]
	v := ""
	if u != nil {
		v = e.votes[strings.TrimPrefix(id, "agent-")]
	}
	e.mu.Unlock()
	if u != nil && v != "" {
		u.vote(v)
	}
	return nil
}
func (e *fakeRaatiEngine) Stop(string) error { return nil }

// fakeBoard records the hook lifecycle.
type fakeBoard struct {
	mu     sync.Mutex
	begun  bool
	busy   bool
	events int
	ended  bool
}

func (b *fakeBoard) Begin(_, _, _, _ string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy {
		return false
	}
	b.begun = true
	return true
}
func (b *fakeBoard) Event(raati.Event) {
	b.mu.Lock()
	b.events++
	b.mu.Unlock()
}
func (b *fakeBoard) End(error) {
	b.mu.Lock()
	b.ended = true
	b.mu.Unlock()
}

func TestRaatiConveneDisabled(t *testing.T) {
	tool := &RaatiConveneTool{Engine: &fakeRaatiEngine{units: map[string]*fakeRaatiUnit{}}}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"q"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(firstText(res.Content), "raati.convene_tool") {
		t.Errorf("disabled tool result = %+v, want an opt-in refusal", res)
	}
}

func TestRaatiConveneHappyPathWithBoardAndDissent(t *testing.T) {
	eng := &fakeRaatiEngine{
		units: map[string]*fakeRaatiUnit{},
		votes: map[string]string{
			"raati-crew:yata":     "approve",
			"raati-crew:kusanagi": "approve",
			"raati-crew:magatama": "reject",
		},
	}
	board := &fakeBoard{}
	tool := &RaatiConveneTool{
		Engine:       eng,
		Enabled:      func() bool { return true },
		HostProvider: "ollama",
		HostModel:    "qwen3:8b",
		Board:        board,
		Persist: func(res *raati.Result) (string, error) {
			return "/records/raati-test.json", nil
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"ship it?","class":"advisory"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %s", firstText(res.Content))
	}
	text := firstText(res.Content)
	for _, want := range []string{
		"verdict: APPROVED",
		"2 approve / 1 reject",
		"minority report (weigh this before acting):",
		"MAGATAMA-3: because reject",
		"seats: YATA-1=ollama/qwen3:8b",
		"record: /records/raati-test.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
	details, _ := res.Details.(map[string]any)
	if details["decision"] != "approved" || details["minority"] != 1 {
		t.Errorf("details = %+v", res.Details)
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if !board.begun || !board.ended || board.events == 0 {
		t.Errorf("board hook lifecycle: begun=%v events=%d ended=%v", board.begun, board.events, board.ended)
	}
}

func TestRaatiConveneBusyBoardStillDeliberates(t *testing.T) {
	eng := &fakeRaatiEngine{
		units: map[string]*fakeRaatiUnit{},
		votes: map[string]string{
			"raati-crew:yata":     "approve",
			"raati-crew:kusanagi": "approve",
			"raati-crew:magatama": "approve",
		},
	}
	board := &fakeBoard{busy: true}
	tool := &RaatiConveneTool{
		Engine: eng, Enabled: func() bool { return true }, Board: board,
		HostProvider: "ollama", HostModel: "qwen3:8b",
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"q","single_round":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("busy board must not block the deliberation: err=%v res=%s", err, firstText(res.Content))
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if board.events != 0 || board.ended {
		t.Errorf("unwatched run must not feed the board: %+v", board)
	}
}

func TestRaatiConveneLevelErrorsSurfaceCleanly(t *testing.T) {
	tool := &RaatiConveneTool{
		Engine:  &fakeRaatiEngine{units: map[string]*fakeRaatiUnit{}},
		Enabled: func() bool { return true },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"q","level":2}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(firstText(res.Content), "raati.level2") {
		t.Errorf("level-2-unconfigured result = %s, want actionable config guidance", firstText(res.Content))
	}
}

func firstText(content []provider.Content) string {
	for _, c := range content {
		if tb, ok := c.(provider.TextBlock); ok {
			return tb.Text
		}
	}
	return ""
}

// TestRaatiToolReportInquiries: the docket reaches the convening agent
// in the tool result itself. In the first live test the summary omitted
// it entirely and the agent only found the panel's questions by reading
// the record file off the returned path — the open gaps are the most
// actionable part of the output ("reconvene with better evidence"), so
// they must not require a second hop.
func TestRaatiToolReportInquiries(t *testing.T) {
	res := &raati.Result{
		Question: "sign it?",
		Inquiries: []raati.Inquiry{
			{Unit: "YATA-1", Question: "Is the exit fee specified?", Answer: "No. The record says it is unspecified.", Source: raati.SourceRecord, Round: 1},
			{Unit: "KUSANAGI-2", Question: "Is the funding contingent on exclusivity?", Answer: "Yes, per the convener.", Source: raati.SourceConvener, Round: 1},
			{Unit: "MAGATAMA-3", Question: "Does the draft prohibit re-identification?", Source: raati.SourceUnanswered, Round: 1},
		},
	}
	text := raatiToolReport(res, "")
	for _, want := range []string{
		"the panel asked:",
		"YATA-1: Is the exit fee specified?",
		"answer (record): No. The record says it is unspecified.",
		"answer (convener): Yes, per the convener.",
		"MAGATAMA-3: Does the draft prohibit re-identification?",
		"open — the record does not answer this; the panel decided with this gap",
		"open questions are unmet evidence — reconvening with answers beats re-rolling the same packet",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}

	// No inquiries -> no section, and no stray open-question coaching.
	if text := raatiToolReport(&raati.Result{}, ""); strings.Contains(text, "the panel asked") || strings.Contains(text, "unmet evidence") {
		t.Errorf("empty docket rendered an inquiry section:\n%s", text)
	}

	// All answered -> section without the open-question coaching line.
	answered := &raati.Result{Inquiries: []raati.Inquiry{
		{Unit: "YATA-1", Question: "Q?", Answer: "A.", Source: raati.SourceRecord, Round: 1},
	}}
	if text := raatiToolReport(answered, ""); strings.Contains(text, "unmet evidence") {
		t.Errorf("fully answered docket still coached about open questions:\n%s", text)
	}
}

// TestRaatiConveneProfile: a named profile fills what the call left
// unset (seats, class, order, shape), explicit args override it, and an
// unknown name errors with the configured list — the agent picks WHICH
// profile, only the config says WHAT it means.
func TestRaatiConveneProfile(t *testing.T) {
	boolp := func(v bool) *bool { return &v }
	eng := &fakeRaatiEngine{
		units: map[string]*fakeRaatiUnit{},
		votes: map[string]string{
			"raati-crew:yata":     "approve",
			"raati-crew:kusanagi": "approve",
			"raati-crew:magatama": "approve",
		},
	}
	tool := &RaatiConveneTool{
		Engine:       eng,
		Enabled:      func() bool { return true },
		HostProvider: "ollama",
		HostModel:    "qwen3:8b",
		Profiles: map[string]raati.Profile{
			"code-review": {
				Description: "gate-grade code panel",
				Seats: []raati.Binding{
					{Provider: "p1", Model: "m1"}, {Provider: "p2", Model: "m2"}, {Provider: "p3", Model: "m3"},
				},
				SeatOrder:   "fixed", // deterministic seats for the assertion
				Class:       "gate",
				SingleRound: boolp(true),
			},
		},
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"ship it?","profile":"code-review"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("profile convene: err=%v res=%s", err, firstText(res.Content))
	}
	text := firstText(res.Content)
	for _, want := range []string{
		"verdict: APPROVED (rule unanimity",                       // profile class=gate applied
		"seats: YATA-1=p1/m1  KUSANAGI-2=p2/m2  MAGATAMA-3=p3/m3", // pinned seats, fixed order
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}

	// Explicit args beat the profile: level 0 reseats on the host
	// binding (the caller may downgrade rigor, never choose models),
	// advisory swaps the rule back.
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"question":"ship it?","profile":"code-review","level":0,"class":"advisory"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("override convene: err=%v res=%s", err, firstText(res.Content))
	}
	text = firstText(res.Content)
	for _, want := range []string{
		"verdict: APPROVED (rule majority",
		"seats: YATA-1=ollama/qwen3:8b",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("override report missing %q:\n%s", want, text)
		}
	}

	// Unknown profile: refuse with the configured list.
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"question":"q","profile":"nope"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("unknown profile: err=%v res=%+v", err, res)
	}
	if msg := firstText(res.Content); !strings.Contains(msg, `"nope"`) || !strings.Contains(msg, "code-review") {
		t.Errorf("unknown-profile message = %q", msg)
	}

	// A short-seated profile errors as a profile problem, not a
	// misleading raati.level2 complaint.
	tool.Profiles["short"] = raati.Profile{Seats: []raati.Binding{{Provider: "p1", Model: "m1"}}}
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"question":"q","profile":"short"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("short profile: err=%v res=%+v", err, res)
	}
	if msg := firstText(res.Content); !strings.Contains(msg, `profile "short"`) || !strings.Contains(msg, "pins 1 seat(s)") {
		t.Errorf("short-profile message = %q", msg)
	}
}

// TestRaatiConveneDescriptionEnumeratesProfiles: the description is the
// agent's whole selection surface, so configured profiles must appear
// in it, sorted, with their what-it-is-for line.
func TestRaatiConveneDescriptionEnumeratesProfiles(t *testing.T) {
	bare := &RaatiConveneTool{}
	if strings.Contains(bare.Description(), "profiles") {
		t.Errorf("no-profile description mentions profiles:\n%s", bare.Description())
	}
	tool := &RaatiConveneTool{Profiles: map[string]raati.Profile{
		"triage": {Description: "cheap advisory eight-ball"},
		"ethics": {Description: "slow frontier panel"},
	}}
	d := tool.Description()
	for _, want := range []string{"[ethics — slow frontier panel]", "[triage — cheap advisory eight-ball]"} {
		if !strings.Contains(d, want) {
			t.Errorf("description missing %q:\n%s", want, d)
		}
	}
	if strings.Index(d, "[ethics") > strings.Index(d, "[triage") {
		t.Errorf("profiles not sorted:\n%s", d)
	}
}

// TestRaatiConveneBuiltinAutoLevel: the shipped profiles end-to-end —
// code-review (gate, auto) refuses on a bare config (auto lands on the
// correlated level and a correlated gate is a lie), then seats level 2
// the moment the config supports it; counsel (advisory, auto) proceeds
// correlated with the usual disclosure.
func TestRaatiConveneBuiltinAutoLevel(t *testing.T) {
	newEng := func() *fakeRaatiEngine {
		return &fakeRaatiEngine{
			units: map[string]*fakeRaatiUnit{},
			votes: map[string]string{
				"raati-crew:yata":     "approve",
				"raati-crew:kusanagi": "approve",
				"raati-crew:magatama": "approve",
			},
		}
	}
	tool := &RaatiConveneTool{
		Engine:       newEng(),
		Enabled:      func() bool { return true },
		HostProvider: "ollama",
		HostModel:    "qwen3:8b",
		Profiles:     raati.BuiltinProfiles(),
	}

	// Bare config: the gate refuses with guidance.
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"merge it?","profile":"code-review"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("bare-config gate: err=%v res=%+v", err, res)
	}
	if msg := firstText(res.Content); !strings.Contains(msg, "correlated panel cannot hold a gate") {
		t.Errorf("refusal message = %q", msg)
	}

	// counsel proceeds at the correlated level on the same bare config.
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"question":"do it?","profile":"counsel"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("bare-config counsel: err=%v res=%s", err, firstText(res.Content))
	}
	if text := firstText(res.Content); !strings.Contains(text, "seats: YATA-1=ollama/qwen3:8b") {
		t.Errorf("counsel did not seat the host binding:\n%s", text)
	}

	// With level2 configured, the same gate call seats cross-provider.
	tool.Engine = newEng()
	tool.Level2 = []raati.Binding{
		{Provider: "p1", Model: "m1"}, {Provider: "p2", Model: "m2"}, {Provider: "p3", Model: "m3"},
	}
	tool.SeatOrder = "fixed" // deterministic for the seats assertion
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"question":"merge it?","profile":"code-review"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("level2 gate: err=%v res=%s", err, firstText(res.Content))
	}
	text := firstText(res.Content)
	for _, want := range []string{"verdict: APPROVED (rule unanimity", "seats: YATA-1=p1/m1"} {
		if !strings.Contains(text, want) {
			t.Errorf("level2 gate report missing %q:\n%s", want, text)
		}
	}
}
