package transport

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"

	"terva.sh/terva/packages/provider"
)

// diagStore keeps the transport rows a store would write.
type diagStore struct {
	core.MemoryTranscriptStore
	mu  sync.Mutex
	net []provider.TransportInfo
}

var _ core.TranscriptDiagnostics = (*diagStore)(nil)

func (s *diagStore) AppendTransport(ti provider.TransportInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.net = append(s.net, ti)
	return nil
}

func (s *diagStore) rows() []provider.TransportInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.TransportInfo(nil), s.net...)
}

func (s *diagStore) AppendEscalation(core.EscalationRecord) error       { return nil }
func (s *diagStore) AppendStall(core.StallRecord) error                 { return nil }
func (s *diagStore) AppendRetry(core.RetryRecord) error                 { return nil }
func (s *diagStore) AppendTail(core.TailRecord) error                   { return nil }
func (s *diagStore) AppendPrefixDivergence(core.PrefixDivergence) error { return nil }
func (s *diagStore) AppendCacheCliff(core.CacheCliff) error             { return nil }

// frame is the smallest assembler: one Stable segment for the system prompt.
type frame struct{}

func (frame) Assemble(core.AssembleMode) core.Frame {
	return core.Frame{Segments: []core.Segment{{Stability: core.Stable, Tag: "system", Content: "sys"}}}
}

// streamClient answers every request with a transport event and a short reply.
type streamClient struct{ info provider.TransportInfo }

func (streamClient) Name() string { return "stream" }

func (c streamClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 5)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "stream"}
		out <- provider.EventTransport{Info: c.info}
		out <- provider.EventTextDelta{Delta: "ok"}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 10, OutputTokens: 2}}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// A provider's transport picture becomes a row in the attached store, driven
// through a real Prompt so the event travels the stream loop production uses.
// Off, it is dropped.
func TestTheRecorderWritesEachRequestsTransport(t *testing.T) {
	want := provider.TransportInfo{ConnReused: true, RemoteAddr: "1.2.3.4:443", Ray: "aa-SJC"}
	st := &diagStore{}
	r := &Recorder{}
	a, err := core.New(streamClient{info: want}, "m", core.WithAssembler(frame{}), core.WithTools(core.Registry{}),
		core.WithGate(core.AllowAll), core.WithComponent(r), core.WithTranscriptStore(st))
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if got := st.rows(); len(got) != 0 {
		t.Fatalf("a recorder that is off wrote %+v", got)
	}

	r.SetEnabled(true)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if got := st.rows(); len(got) != 1 || got[0] != want {
		t.Fatalf("the store got %+v, want exactly [%+v]", got, want)
	}
}
