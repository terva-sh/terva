package core

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Stable segments join into the system prompt with the blank line terva's own
// renderer uses, empty ones included, so a host rendering with that separator
// gets its bytes back unchanged. Volatile segments are not part of it.
func TestSystemTextJoinsTheStableSegmentsOnly(t *testing.T) {
	f := Frame{Segments: []Segment{
		{Stability: Stable, Tag: "identity", Content: "You are terva."},
		{Stability: Volatile, Tag: TailHost, Content: "the lore"},
		{Stability: Stable, Tag: "empty", Content: ""},
		{Stability: Stable, Tag: "footer", Content: "cwd: /x"},
	}}
	if got, want := f.SystemText(), "You are terva.\n\n\n\ncwd: /x"; got != want {
		t.Errorf("SystemText = %q, want %q", got, want)
	}
}

// An empty Volatile segment adds nothing to the tail, as an empty context
// provider did not.
func TestAnEmptyVolatileSegmentIsNotSent(t *testing.T) {
	f := Frame{Segments: []Segment{
		{Stability: Volatile, Tag: "a", Content: ""},
		{Stability: Volatile, Tag: "b", Content: "kept"},
	}}
	if got := f.Volatile(); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("Volatile = %+v, want only b", got)
	}
	if got := f.VolatileText(); got != "kept" {
		t.Errorf("VolatileText = %q", got)
	}
}

func TestStaticSystem(t *testing.T) {
	if got := StaticSystem("").Assemble(AssembleRequest); len(got.Segments) != 0 {
		t.Errorf("an empty system prompt gave segments: %+v", got.Segments)
	}
	got := StaticSystem("be brief").Assemble(AssemblePeek)
	if len(got.Segments) != 1 || got.Segments[0].Stability != Stable || got.SystemText() != "be brief" {
		t.Errorf("StaticSystem frame = %+v", got.Segments)
	}
}

// countingAssembler renders a different Stable text for each mode, which no
// real host should do, so a test can see which call the prefix came from, and
// counts the calls of each mode.
type countingAssembler struct {
	mu       sync.Mutex
	requests int
	peeks    int
}

func (c *countingAssembler) Assemble(mode AssembleMode) Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	system := "from a peek"
	if mode == AssembleRequest {
		c.requests++
		system = "from a request"
	} else {
		c.peeks++
	}
	return Frame{Segments: []Segment{
		{Stability: Stable, Tag: "system", Content: system},
		{Stability: Volatile, Tag: "lore", Content: "LORE"},
		{Stability: Volatile, Tag: "", Content: ""},
		{Stability: Volatile, Tag: "cards", Content: "CARDS"},
	}}
}

type frameCaptureClient struct {
	mu   sync.Mutex
	reqs []provider.Request
}

func (c *frameCaptureClient) Name() string { return "framecap" }

func (c *frameCaptureClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	out := make(chan provider.Event, 1)
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

// The two cadences the frame replaced, kept. The prefix is pinned from a peek
// at the start of the turn, as System was, so the per-request side effects run
// once per request and never for the pin. The Volatile segments come from the
// request's own assembly, as ContextProvider's output did, each a tail block
// named by its tag and in the host's order, so the recorded tail names them.
func TestTheFramesTwoCadences(t *testing.T) {
	client := &frameCaptureClient{}
	asm := &countingAssembler{}
	a := newAgentOver(client, "m", asm)
	got := collectTail(a)

	for _, p := range []string{"one", "two"} {
		if err := a.Prompt(context.Background(), p, nil, nil); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
	}

	if len(client.reqs) != 2 {
		t.Fatalf("sent %d requests, want 2", len(client.reqs))
	}
	if asm.requests != len(client.reqs) {
		t.Errorf("assembled %d request frames for %d requests; the side effects must run once per request", asm.requests, len(client.reqs))
	}
	for i, req := range client.reqs {
		if req.System != "from a peek" {
			t.Errorf("request %d System = %q; the prefix must be the one pinned from a peek", i, req.System)
		}
		if req.EphemeralContext != "LORE\n\nCARDS" {
			t.Errorf("request %d tail = %q, want the Volatile segments in order", i, req.EphemeralContext)
		}
	}
	if len(*got) == 0 {
		t.Fatal("no tail was recorded")
	}
	if ids := tailIDs((*got)[0]); len(ids) != 2 || ids[0] != "lore" || ids[1] != "cards" {
		t.Errorf("recorded tail IDs = %v, want the segment tags [lore cards]", ids)
	}
}

// A nil assembler is an empty frame, not a crash: a host with nothing to say
// sends no system prompt and no tail.
func TestANilAssemblerIsAnEmptyFrame(t *testing.T) {
	client := &frameCaptureClient{}
	a := newAgentOver(client, "m", nil)
	if err := a.Prompt(context.Background(), "hi", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if req := client.reqs[0]; req.System != "" || req.EphemeralContext != "" {
		t.Errorf("System %q, tail %q; want both empty", req.System, req.EphemeralContext)
	}
	if len(a.FramePreview().Segments) != 0 {
		t.Error("FramePreview of a nil assembler is not empty")
	}
}
