package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// flagTool is a minimal tool that records whether Execute ran, so a test can
// prove a hidden tool still dispatched.
type flagTool struct {
	name     string
	executed bool
}

func (f *flagTool) Name() string            { return f.name }
func (f *flagTool) Description() string     { return "d-" + f.name }
func (f *flagTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *flagTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	f.executed = true
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

func specNames(specs []provider.Tool) map[string]bool {
	m := make(map[string]bool, len(specs))
	for _, s := range specs {
		m[s.Name] = true
	}
	return m
}

// TestSpecsVisibleFiltersAdvertisedNotCallable pins the registry-level split:
// SpecsVisible narrows the ADVERTISED specs, but the registry — the callable
// surface — is untouched. This is the "daylight" between what the model sees
// and what it can call that lazy tool visibility (H2·b) introduces.
func TestSpecsVisibleFiltersAdvertisedNotCallable(t *testing.T) {
	reg := Registry{
		"alpha": &flagTool{name: "alpha"},
		"bravo": &flagTool{name: "bravo"},
		"charl": &flagTool{name: "charl"},
	}

	// nil predicate advertises everything (Specs's behavior).
	if got := len(reg.Specs()); got != 3 {
		t.Fatalf("Specs() advertised %d tools, want 3", got)
	}

	// Hiding "bravo" removes it from the advertised specs only.
	visible := func(name string) bool { return name != "bravo" }
	adv := specNames(reg.SpecsVisible(visible))
	if adv["bravo"] {
		t.Error("hidden tool must not be advertised")
	}
	if !adv["alpha"] || !adv["charl"] {
		t.Errorf("visible tools must still be advertised, got %v", adv)
	}

	// The registry still resolves the hidden tool: it remains callable.
	if _, err := reg.Get("bravo"); err != nil {
		t.Errorf("hidden tool must remain in the registry (callable), got %v", err)
	}

	// The advertised order stays name-sorted (load-bearing for prompt caching).
	specs := reg.SpecsVisible(visible)
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "charl" {
		t.Errorf("SpecsVisible must stay name-sorted, got %v", specNames(specs))
	}
}

// visibilityCaptureClient records req.Tools per call and drives one tool call:
// call 1 asks for "secret", call 2 ends the turn.
type visibilityCaptureClient struct {
	mu    sync.Mutex
	calls int
	tools [][]provider.Tool
}

func (c *visibilityCaptureClient) Name() string { return "viscap" }

func (c *visibilityCaptureClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.tools = append(c.tools, req.Tools)
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "viscap", Model: req.Model}
		if n == 1 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "t1", Name: "secret", Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// TestAHiddenToolStillDispatchesAndGates is the H2·b load-bearing
// invariant end-to-end: a tool hidden from the model by a ToolVisibility is (a) NOT
// advertised in the request, yet (b) still dispatches when the model calls it
// anyway, and (c) still passes through the permission gate. Visibility is not
// authority — the advertised list never decides callability or authorization.
func TestAHiddenToolStillDispatchesAndGates(t *testing.T) {
	secret := &flagTool{name: "secret"}
	client := &visibilityCaptureClient{}
	a := newTestAgent(client, "fake-model", "system", Registry{"secret": secret})
	a.setToolVisibility(&testVisibility{shown: map[string]bool{}, hidden: map[string]bool{"secret": true}})

	gateFired := false
	a.gate = GateFunc(func(_ context.Context, call provider.ToolCallBlock, _ Tool) (bool, string, json.RawMessage) {
		if call.Name == "secret" {
			gateFired = true
		}
		return true, "", nil
	})

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.tools) == 0 {
		t.Fatal("no request captured")
	}
	// (a) hidden from the advertised list.
	if specNames(client.tools[0])["secret"] {
		t.Error("a hidden tool must not be advertised to the model")
	}
	// (b) still callable — dispatch resolved the full registry.
	if !secret.executed {
		t.Error("a hidden tool must still dispatch when called (visibility != callability)")
	}
	// (c) still gated — the permission hook fired regardless of visibility.
	if !gateFired {
		t.Error("the permission gate must fire for a hidden-but-called tool (visibility != authority)")
	}
}

// testVisibility hides the names in hidden until show reveals one. It is the
// smallest ToolVisibility with state, and it counts what the engine asks of it.
type testVisibility struct {
	mu     sync.Mutex
	shown  map[string]bool
	hidden map[string]bool
	// pinned is the hidden set at the last pin.
	pinned map[string]bool
	repin  bool
	pins   int
	peeks  int
	begins int
}

func (v *testVisibility) Advertise(_ Registry, pin bool) func(string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	hidden := map[string]bool{}
	for n := range v.hidden {
		if !v.shown[n] {
			hidden[n] = true
		}
	}
	if pin {
		v.pins++
		v.pinned = hidden
	} else {
		v.peeks++
	}
	return func(name string) bool { return !hidden[name] }
}

func (v *testVisibility) Grew(reg Registry) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for n := range reg {
		if v.pinned[n] && v.shown[n] {
			return true
		}
	}
	return false
}

func (v *testVisibility) BeginPrompt() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.begins++
	return v.repin
}

func (v *testVisibility) show(name string) {
	v.mu.Lock()
	v.shown[name] = true
	v.mu.Unlock()
}

func (v *testVisibility) counts() (pins, peeks, begins int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.pins, v.peeks, v.begins
}

// revealTool shows a hidden name when called, as activate_tools does.
type revealTool struct {
	v    *testVisibility
	name string
}

func (r *revealTool) Name() string            { return "reveal" }
func (r *revealTool) Description() string     { return "reveals a tool" }
func (r *revealTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *revealTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	r.v.show(r.name)
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "revealed"}}}, nil
}

// revealThenAnswer calls reveal on the first request, and answers after.
func revealThenAnswer(n int, _ provider.Request) ([]provider.Event, error) {
	if n == 0 {
		return []provider.Event{
			provider.EventStart{Provider: "scripted"},
			provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "r1", Name: "reveal", Arguments: json.RawMessage(`{}`)}},
			}},
		}, nil
	}
	return saidText("done", 10), nil
}

func advertised(req provider.Request, name string) bool { return specNames(req.Tools)[name] }

// The engine pins the advertisement once per segment. A tool batch that grew
// it re-pins for the very next step when BeginPrompt asked for that, and
// otherwise leaves the growth to the next Prompt.
func TestAGrownAdvertisementRepinsWhenThePromptAsks(t *testing.T) {
	for _, repin := range []bool{true, false} {
		t.Run(fmt.Sprintf("repin=%v", repin), func(t *testing.T) {
			v := &testVisibility{shown: map[string]bool{}, hidden: map[string]bool{"secret": true}, repin: repin}
			client := &scriptedClient{name: "scripted", script: revealThenAnswer}
			reg := Registry{"secret": &flagTool{name: "secret"}}
			a := newTestAgent(client, "m", "sys", reg)
			reg["reveal"] = &revealTool{v: v, name: "secret"}
			a.setToolVisibility(v)
			if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
				t.Fatal(err)
			}
			calls := client.calls()
			if len(calls) != 2 || advertised(calls[0], "secret") {
				t.Fatalf("got %d requests, the first advertising secret: %v; want 2, hidden at first", len(calls), len(calls) > 0 && advertised(calls[0], "secret"))
			}
			if got := advertised(calls[1], "secret"); got != repin {
				t.Errorf("the step after the reveal advertised secret: %v, want %v", got, repin)
			}
			if _, _, begins := v.counts(); begins != 1 {
				t.Errorf("BeginPrompt ran %d times in one Prompt, want 1", begins)
			}
			if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
				t.Fatal(err)
			}
			if last := client.calls()[2]; !advertised(last, "secret") {
				t.Error("the next Prompt's pin did not advertise the revealed tool")
			}
		})
	}
}

// A boundary re-pins when the ended segment grew the advertisement: a gate
// that continues the Prompt continues it with the new tool live.
func TestABoundaryRepinsAGrownAdvertisement(t *testing.T) {
	v := &testVisibility{shown: map[string]bool{}, hidden: map[string]bool{"secret": true}, repin: true}
	client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		return saidText("done", 10), nil
	}}
	a := newTestAgent(client, "m", "sys", Registry{"secret": &flagTool{name: "secret"}})
	a.setToolVisibility(v)
	a.AddContinuationGate(ContinuationGate{Cause: "reveal", Fire: func(provider.StopReason) (string, bool) {
		v.show("secret")
		return "go on", true
	}})
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	calls := client.calls()
	if len(calls) != 2 || advertised(calls[0], "secret") || !advertised(calls[1], "secret") {
		t.Fatalf("advertised secret per request: %v, want [false true]", func() []bool {
			var out []bool
			for _, c := range calls {
				out = append(out, advertised(c, "secret"))
			}
			return out
		}())
	}
}

// The prefix readers ask without pinning: a peek must not move what the
// running segment advertises.
func TestThePrefixReadersPeekWithoutPinning(t *testing.T) {
	v := &testVisibility{shown: map[string]bool{}, hidden: map[string]bool{}}
	a := newTestAgent(nil, "m", "sys", Registry{"read": &flagTool{name: "read"}})
	a.setToolVisibility(v)
	_, _ = a.compactionPrefix()
	a.mu.Lock()
	_ = a.livePrefixLocked("sys")
	a.mu.Unlock()
	if pins, peeks, _ := v.counts(); pins != 0 || peeks == 0 {
		t.Errorf("prefix reads made %d pins and %d peeks, want 0 pins and some peeks", pins, peeks)
	}
}

// A Fallback gate runs after every gate without it, whatever the order they
// were registered in.
func TestAFallbackGateRunsAfterTheOthers(t *testing.T) {
	a := newTestAgent(nil, "m", "sys", Registry{})
	yes := func(provider.StopReason) (string, bool) { return "x", true }
	a.AddContinuationGate(ContinuationGate{Cause: "fallback", Fallback: true, Fire: yes})
	a.AddContinuationGate(ContinuationGate{Cause: "first", Fire: yes})
	a.AddContinuationGate(ContinuationGate{Cause: "second", Fire: yes})
	var causes []string
	for _, g := range a.continuationGateSnapshot() {
		causes = append(causes, g.Cause)
	}
	if strings.Join(causes, ",") != "first,second,fallback" {
		t.Errorf("gates run in the order %v, want [first second fallback]", causes)
	}
}

// A component's required record reaches the attached store through its
// writer, and a failed write latches the persistence error.
func TestAppendRecordReachesTheAttachedStore(t *testing.T) {
	a := newTestAgent(nil, "m", "sys", Registry{})
	a.AppendRecord(func(TranscriptStore) error { return errors.New("must not run") })
	if err := a.PersistenceError(); err != nil {
		t.Fatalf("a record with no store latched %v", err)
	}
	st := NewMemoryTranscriptStore()
	a.AttachTranscriptStore(st)
	a.AppendRecord(func(s TranscriptStore) error { return s.AppendToolGroupActivation("mail") })
	if got := st.Transcript().ActiveToolGroups; len(got) != 1 || got[0] != "mail" {
		t.Fatalf("the store holds groups %v, want [mail]", got)
	}
	boom := errors.New("disk full")
	a.AppendRecord(func(TranscriptStore) error { return boom })
	if err := a.PersistenceError(); !errors.Is(err, boom) {
		t.Errorf("a failed record latched %v, want it to wrap %v", err, boom)
	}
}

// restoringVisibility is a GroupRestorer that keeps what it was handed.
type restoringVisibility struct {
	testVisibility
	restored []string
}

func (r *restoringVisibility) RestoreActiveGroups(groups []string) { r.restored = groups }

// Resume hands the transcript's groups to a visibility that restores them.
func TestResumeRestoresGroupsThroughTheVisibility(t *testing.T) {
	a := newTestAgent(nil, "m", "sys", Registry{})
	r := &restoringVisibility{}
	a.setToolVisibility(r)
	a.Resume(Transcript{ActiveToolGroups: []string{"mail", "db"}})
	if strings.Join(r.restored, ",") != "mail,db" {
		t.Errorf("the visibility was handed %v, want [mail db]", r.restored)
	}
	// A visibility that does not restore is left alone.
	a.setToolVisibility(&testVisibility{})
	a.Resume(Transcript{ActiveToolGroups: []string{"mail"}})
}
