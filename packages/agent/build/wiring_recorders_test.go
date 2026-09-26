package build

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// recorderStore keeps the rows the two recorders write.
type recorderStore struct {
	core.MemoryTranscriptStore
	mu   sync.Mutex
	divs int
	net  int
}

var _ core.TranscriptDiagnostics = (*recorderStore)(nil)

func (s *recorderStore) AppendPrefixDivergence(core.PrefixDivergence) error {
	s.mu.Lock()
	s.divs++
	s.mu.Unlock()
	return nil
}

func (s *recorderStore) AppendTransport(provider.TransportInfo) error {
	s.mu.Lock()
	s.net++
	s.mu.Unlock()
	return nil
}

func (s *recorderStore) counts() (divs, net int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.divs, s.net
}

func (s *recorderStore) AppendEscalation(core.EscalationRecord) error { return nil }
func (s *recorderStore) AppendStall(core.StallRecord) error           { return nil }
func (s *recorderStore) AppendRetry(core.RetryRecord) error           { return nil }
func (s *recorderStore) AppendTail(core.TailRecord) error             { return nil }
func (s *recorderStore) AppendCacheCliff(core.CacheCliff) error       { return nil }

// transportClient answers every request with a transport picture and a reply.
type transportClient struct{}

func (transportClient) Name() string { return "transport-fake" }

func (transportClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 5)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "transport-fake", Model: req.Model}
		out <- provider.EventTransport{Info: provider.TransportInfo{ConnReused: true, Ray: "aa-SJC"}}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 10, OutputTokens: 2}}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// terva's NewAgent attaches both recorders, and their engine features switch
// them: with both on, a rewritten transcript writes a prefix row and every
// request writes a transport row; with both off, nothing is written.
func TestNewAgentWiresTheRecorders(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	run := func(on bool) (divs, net int) {
		ag := r.NewAgent(core.AllowAll)
		for _, id := range []string{"prefix_divergence_recording", "transport_recording"} {
			f, _ := EngineFeatureByID(id)
			f.Apply(ag, on)
		}
		ag.SetClientAndModel(transportClient{}, "gpt-5")
		st := &recorderStore{}
		ag.AttachTranscriptStore(st)
		for i := 0; i < 2; i++ {
			if err := ag.Prompt(context.Background(), "go", nil, nil); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
		}
		msgs := ag.Messages()
		msgs[0] = provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "REBUILT"}}}
		ag.SetMessages(msgs)
		if err := ag.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		return st.counts()
	}
	if divs, net := run(true); divs != 1 || net != 3 {
		t.Errorf("with both on: %d prefix rows and %d transport rows, want 1 and 3", divs, net)
	}
	if divs, net := run(false); divs != 0 || net != 0 {
		t.Errorf("with both off: %d prefix rows and %d transport rows, want none", divs, net)
	}
}
