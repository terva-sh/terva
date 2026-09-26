package core

import (
	"context"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// deliveryFrame is a testFrame that also implements TailDeliveryObserver and
// keeps each report.
type deliveryFrame struct {
	testFrame
	mu      sync.Mutex
	reports [][]string
}

func (f *deliveryFrame) TailDelivered(ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, append([]string(nil), ids...))
}

func (f *deliveryFrame) seen() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.reports...)
}

// Each request reports the blocks it carried, the host's own and the engine's,
// in the order the model reads them.
func TestTheAssemblerHearsWhatEachRequestCarried(t *testing.T) {
	f := &deliveryFrame{testFrame: testFrame{system: "system", host: func() string { return "the task card" }}}
	a := newAgentOver(&okClient{}, "fake-model", f)

	if err := a.Prompt(context.Background(), "hello", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	got := f.seen()
	if len(got) != 1 {
		t.Fatalf("got %d reports for one request, want 1: %v", len(got), got)
	}
	if strings.Join(got[0], ",") != TailHost {
		t.Errorf("reported %v, want [%s]", got[0], TailHost)
	}
}

// A continue turn suppresses the whole tail, so the host segment it assembled
// never reached the model. The report says so rather than skipping the request:
// an assembler that counts reports is told the truth either way.
func TestAContinueTurnReportsNothingCarried(t *testing.T) {
	client := &prefillFakeClient{cont: " and vanished into the trees."}
	f := &deliveryFrame{testFrame: testFrame{system: "system", host: func() string { return "the task card" }}}
	a := newAgentOver(client, "fake-model", f)
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Tell me a story."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,"}}},
	})

	if err := a.ContinueAssistant(context.Background(), nil); err != nil {
		t.Fatalf("ContinueAssistant: %v", err)
	}
	if client.lastReq.EphemeralContext != "" {
		t.Fatalf("the continue turn carried a tail, so this proves nothing: %q", client.lastReq.EphemeralContext)
	}
	got := f.seen()
	if len(got) != 1 {
		t.Fatalf("got %d reports for one request, want 1: %v", len(got), got)
	}
	if len(got[0]) != 0 {
		t.Errorf("a continue turn reported %v as carried; the model saw none of it", got[0])
	}
}
