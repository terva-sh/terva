package prefixwatch

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// diagStore keeps the prefix and cliff rows a store would write.
type diagStore struct {
	core.MemoryTranscriptStore
	mu    sync.Mutex
	divs  []core.PrefixDivergence
	cliff []core.CacheCliff
	// order is the kind of each usage and cliff row, in the order written.
	order []string
}

func (s *diagStore) AppendUsage(r core.UsageRecord) error {
	s.mu.Lock()
	s.order = append(s.order, "usage")
	s.mu.Unlock()
	return s.MemoryTranscriptStore.AppendUsage(r)
}

var _ core.TranscriptDiagnostics = (*diagStore)(nil)

func (s *diagStore) AppendPrefixDivergence(d core.PrefixDivergence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.divs = append(s.divs, d)
	return nil
}

func (s *diagStore) AppendCacheCliff(cc core.CacheCliff) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cliff = append(s.cliff, cc)
	s.order = append(s.order, "cliff")
	return nil
}

func (s *diagStore) AppendEscalation(core.EscalationRecord) error { return nil }
func (s *diagStore) AppendStall(core.StallRecord) error           { return nil }
func (s *diagStore) AppendRetry(core.RetryRecord) error           { return nil }
func (s *diagStore) AppendTail(core.TailRecord) error             { return nil }
func (s *diagStore) AppendTransport(provider.TransportInfo) error { return nil }

// The rows reach the store the host attached, with no wiring of the Watch's
// own: a rewritten transcript writes one prefix row, and a cliff run writes
// its events, the same ones the observers hear.
func TestTheWatchsRowsReachTheAttachedStore(t *testing.T) {
	a, w := newAgent(textClient{})
	st := &diagStore{}
	a.AttachTranscriptStore(st)
	r := record(w)

	for i := 0; i < 2; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	msgs := a.Messages()
	msgs[0] = msgText(provider.RoleUser, "REBUILT")
	a.SetMessages(msgs)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.divs) != 1 || len(r.divs()) != 1 || st.divs[0] != r.divs()[0] {
		t.Errorf("the store got %+v and the observer %+v, want the same one divergence", st.divs, r.divs())
	}
}

// The cliff's rows reach the store too, each event the observers hear.
func TestTheCliffsRowsReachTheAttachedStore(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728},
		{input: 60_000, cacheRead: 9_728},
	}
	a, events, run := cliffAgent(t, steps)
	st := &diagStore{}
	a.AttachTranscriptStore(st)
	run(0, len(steps))
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(*events) == 0 || len(st.cliff) != len(*events) {
		t.Errorf("the store got %d cliff rows and the observer %d events, want the same, and some", len(st.cliff), len(*events))
	}
	// The run opens on the fourth dispatch's usage, so its row follows that
	// dispatch's usage row: a reader pairs a cliff row with the usage above it.
	first := -1
	for i, k := range st.order {
		if k == "cliff" {
			first = i
			break
		}
	}
	if first != len(steps) {
		t.Errorf("rows were written in the order %v, want the first cliff row after all %d usage rows", st.order, len(steps))
	}
}
