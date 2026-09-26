package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// stepClient answers a request that ends in a user message with one call to
// the recorder tool, and any other request with text. It keeps every request.
type stepClient struct {
	mu   sync.Mutex
	reqs []provider.Request
}

func (c *stepClient) Name() string { return "step" }

func (c *stepClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	msg, stop := provider.Message{Role: provider.RoleAssistant}, provider.StopEnd
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == provider.RoleUser {
		msg.Content = []provider.Content{recorderCall()}
		stop = provider.StopToolUse
	} else {
		msg.Content = []provider.Content{provider.TextBlock{Text: "done"}}
	}
	out := make(chan provider.Event, 3)
	out <- provider.EventStart{Provider: "step", Model: req.Model}
	out <- provider.EventDone{Stop: stop, Message: msg}
	close(out)
	return out, nil
}

func (c *stepClient) requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.reqs...)
}

// probe implements every capability a component can have except the ones
// only one component may hold, and counts what the engine calls.
type probe struct {
	mu    sync.Mutex
	bound *Agent
	calls map[string]int
}

func newProbe() *probe { return &probe{calls: map[string]int{}} }

func (p *probe) hit(name string) {
	p.mu.Lock()
	p.calls[name]++
	p.mu.Unlock()
}

func (p *probe) count(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[name]
}

func (p *probe) Bind(a *Agent) { p.mu.Lock(); p.bound = a; p.mu.Unlock() }
func (p *probe) StepGate() StepGate {
	return StepGate{Cause: "probe", After: func(context.Context, StepState, func(AgentEvent)) bool { p.hit("step"); return false }}
}
func (p *probe) ContinuationGates() []ContinuationGate {
	return []ContinuationGate{{Cause: "probe", Fire: func(provider.StopReason) (string, bool) { p.hit("continuation"); return "", false }}}
}
func (p *probe) WrapGate(inner Gate) Gate {
	return GateFunc(func(ctx context.Context, call provider.ToolCallBlock, tool Tool) (bool, string, json.RawMessage) {
		p.hit("gate")
		return inner.CheckTool(ctx, call, tool)
	})
}
func (p *probe) ObserveEvent(AgentEvent)         { p.hit("event") }
func (p *probe) ObserveMessage(provider.Message) { p.hit("message") }
func (p *probe) QueueDrained([]string)           { p.hit("queue") }
func (p *probe) DispatchObserver() DispatchObserver {
	return DispatchObserver{Sent: func(provider.Request) { p.hit("dispatch") }}
}

// filterProbe is a TurnFilter and a MessageFilter that allows everything.
type filterProbe struct{ probe *probe }

func (f filterProbe) BeforeTurn(int) (bool, string) { f.probe.hit("turn"); return true, "" }
func (f filterProbe) BeforeUserMessage(string) (bool, string, string) {
	f.probe.hit("user")
	return true, "", ""
}
func (f filterProbe) BeforeAssistantMessage(string) (bool, string, string) {
	f.probe.hit("assistant")
	return true, "", ""
}

// Every capability a component implements is connected, and Bind receives
// the agent New returns.
func TestNewConnectsEveryCapabilityAComponentHas(t *testing.T) {
	p := newProbe()
	tool := &cancelProbeTool{}
	a, err := New(&stepClient{}, "m",
		WithGate(AllowAll),
		WithTools(Registry{tool.Name(): tool}),
		WithComponent(p),
		WithComponent(filterProbe{probe: p}),
	)
	if err != nil {
		t.Fatal(err)
	}
	// A queued message is drained at the first boundary, which is what a
	// QueueObserver hears.
	a.QueueMessage("and then this")
	if err := a.Run(context.Background(), PromptInput{Text: "go"}, nil); err != nil {
		t.Fatal(err)
	}
	if !tool.ran.Load() {
		t.Fatal("the tool did not run, so the gate and the step gate had nothing to see")
	}
	for _, hook := range []string{"step", "continuation", "gate", "event", "message", "dispatch", "queue", "turn", "user", "assistant"} {
		if p.count(hook) == 0 {
			t.Errorf("the %s capability was never called", hook)
		}
	}
	if p.bound != a {
		t.Errorf("Bind got %p, want the agent New returned (%p)", p.bound, a)
	}
}

// New refuses what it cannot build, with an error, rather than returning an
// agent that fails on its first turn.
func TestNewRefusesAConfigurationItCannotBuild(t *testing.T) {
	nilWrapper := gateWrapperFunc(func(Gate) Gate { return nil })
	twice := newProbe()
	for name, tc := range map[string]struct {
		opts []Option
		want string
	}{
		"no gate":                 {nil, "AllowAll"},
		"nil GateFunc":            {[]Option{WithGate(GateFunc(nil))}, "AllowAll"},
		"nil component":           {[]Option{WithGate(AllowAll), WithComponent(nil)}, "component is nil"},
		"typed-nil component":     {[]Option{WithGate(AllowAll), WithComponent((*probe)(nil))}, "component is nil"},
		"one component twice":     {[]Option{WithGate(AllowAll), WithComponent(twice), WithComponent(twice)}, "passed again"},
		"a component of no use":   {[]Option{WithGate(AllowAll), WithComponent("stall")}, "implements no capability"},
		"two visibilities":        {[]Option{WithGate(AllowAll), WithComponent(allVisible{}), WithComponent(allVisible{})}, "ToolVisibility"},
		"two turn filters":        {[]Option{WithGate(AllowAll), WithComponent(filterProbe{newProbe()}), WithComponent(filterProbe{newProbe()})}, "TurnFilter"},
		"a wrapper that drops it": {[]Option{WithGate(AllowAll), WithComponent(nilWrapper)}, "nil gate"},
		"the zero Option":         {[]Option{WithGate(AllowAll), {}}, "zero Option"},
		"negative max steps":      {[]Option{WithGate(AllowAll), WithMaxSteps(-1)}, "negative"},
	} {
		t.Run(name, func(t *testing.T) {
			a, err := New(&stepClient{}, "m", tc.opts...)
			if err == nil {
				t.Fatalf("New built %p; want an error naming %q", a, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
	if _, err := New(nil, "m"); !errors.Is(err, ErrNilGate) {
		t.Errorf("a missing gate is %v; want ErrNilGate", err)
	}
}

type gateWrapperFunc func(Gate) Gate

func (f gateWrapperFunc) WrapGate(g Gate) Gate { return f(g) }

type allVisible struct{}

func (allVisible) Advertise(Registry, bool) func(string) bool { return nil }
func (allVisible) Grew(Registry) bool                         { return false }
func (allVisible) BeginPrompt() bool                          { return false }

// Options set what they name; what no option names keeps its default.
func TestNewAppliesOptionsAndKeepsDefaults(t *testing.T) {
	a, err := New(nil, "m", WithGate(AllowAll), WithMaxSteps(7), WithMaxTokens(900),
		WithReasoning(""), WithTemperature(0.25), WithShowReasoning(true))
	if err != nil {
		t.Fatal(err)
	}
	if a.maxSteps != 7 || a.maxTokens != 900 || !a.showReasoning {
		t.Errorf("MaxSteps %d, MaxTokens %d, ShowReasoning %v", a.maxSteps, a.maxTokens, a.showReasoning)
	}
	if a.reasoning != "" || !a.reasoningSet {
		t.Errorf("WithReasoning(\"\") is an explicit off: Reasoning %q, ReasoningSet %v", a.reasoning, a.reasoningSet)
	}
	if a.temperature == nil || *a.temperature != 0.25 {
		t.Errorf("Temperature %v", a.temperature)
	}
	if a.maxRetries != 6 || a.retryBaseDelay != 2*time.Second {
		t.Errorf("the retry defaults changed: %d from %v", a.maxRetries, a.retryBaseDelay)
	}
	b, err := New(nil, "m", WithGate(AllowAll), WithRetries(0, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if b.maxRetries != 0 || b.retryBaseDelay != time.Second {
		t.Errorf("WithRetries(0, 1s) gave %d from %v", b.maxRetries, b.retryBaseDelay)
	}
}

// Run starts a turn from each input kind as the method it stands in for.
func TestRunStartsATurnFromEachInput(t *testing.T) {
	client := &stepClient{}
	tool := &cancelProbeTool{}
	a, err := New(client, "m", WithGate(AllowAll), WithTools(Registry{tool.Name(): tool}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.Run(ctx, PromptInput{Text: "first"}, nil); err != nil {
		t.Fatal(err)
	}
	if first := client.requests()[0].Messages; firstText(first[len(first)-1]) != "first" {
		t.Errorf("the prompt's request ends in %#v, want the user's text", first[len(first)-1])
	}

	before := len(client.requests())
	if err := a.Run(ctx, ContinueInput{Cue: "cue-marker"}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := client.requests()
	if len(reqs) == before {
		t.Fatal("ContinueInput made no request")
	}
	if !requestMentions(reqs[before], "cue-marker") {
		t.Error("ContinueInput{Cue} did not carry its cue into the request")
	}

	before = len(client.requests())
	if err := a.Run(ctx, ContinueInput{}, nil); err != nil {
		t.Fatal(err)
	}
	reqs = client.requests()
	if len(reqs) == before {
		t.Fatal("ContinueInput{} made no request")
	}
	if requestMentions(reqs[before], "cue-marker") {
		t.Error("the cue outlived its turn: ContinueInput{} carried it")
	}

	// Only ContinueAssistant answers with ErrContinueUnsupported, and the step
	// client does not advertise a prefill, so the error shows where Run went.
	if err := a.Run(ctx, ContinueAssistantInput{}, nil); !errors.Is(err, ErrContinueUnsupported) {
		t.Errorf("ContinueAssistantInput gave %v; want ContinueAssistant's ErrContinueUnsupported", err)
	}

	if err := a.Run(ctx, nil, nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("a nil input gave %v; want an error", err)
	}

	// A pointer to an input is an Input as well, since the marker method has
	// a value receiver. Run takes it as the value it points to.
	before = len(client.requests())
	if err := a.Run(ctx, &PromptInput{Text: "by pointer"}, nil); err != nil {
		t.Fatalf("a *PromptInput gave %v", err)
	}
	if reqs := client.requests(); len(reqs) == before || !requestMentions(reqs[before], "by pointer") {
		t.Error("a *PromptInput did not start a turn from its text")
	}
	if err := a.Run(ctx, &ContinueAssistantInput{}, nil); !errors.Is(err, ErrContinueUnsupported) {
		t.Errorf("a *ContinueAssistantInput gave %v; want ContinueAssistant's error", err)
	}
	if err := a.Run(ctx, (*ContinueInput)(nil), nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("a nil *ContinueInput gave %v; want the nil-input error", err)
	}
}

func firstText(m provider.Message) string {
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			return tb.Text
		}
	}
	return ""
}

// requestMentions reports whether s is anywhere the request carries text: the
// system prompt, the ephemeral tail a stage cue rides in, or a message.
func requestMentions(req provider.Request, s string) bool {
	if strings.Contains(req.System, s) || strings.Contains(req.EphemeralContext, s) {
		return true
	}
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.Contains(tb.Text, s) {
				return true
			}
		}
	}
	return false
}

// A catalog re-derives MaxTokens from the model's cap. An explicit
// WithMaxTokens is the host's choice and wins, in whichever order the two
// options are passed; without it the catalog's cap applies.
func TestWithMaxTokensWinsOverTheCatalogsCap(t *testing.T) {
	reg := provider.NewRegistry()
	reg.SetUserModels([]provider.Model{{
		Provider: "anthropic", ID: "m", DisplayName: "m",
		ContextWindow: 100000, MaxOutput: 4000, Source: "user",
	}})
	for name, opts := range map[string][]Option{
		"catalog first":    {WithCatalog(reg), WithMaxTokens(50)},
		"max tokens first": {WithMaxTokens(50), WithCatalog(reg)},
		"catalog only":     {WithCatalog(reg)},
	} {
		a, err := New(nil, "m", append([]Option{WithGate(AllowAll)}, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		want := 50
		if name == "catalog only" {
			want = 4000
		}
		if a.maxTokens != want {
			t.Errorf("%s: MaxTokens %d, want %d", name, a.maxTokens, want)
		}
	}
}
