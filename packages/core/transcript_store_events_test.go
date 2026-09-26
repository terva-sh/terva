package core

import (
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// diagStore is an in-memory store that also keeps the stuck-loop hatch's
// diagnostic rows, the part a test of seam A asks about.
type diagStore struct {
	MemoryTranscriptStore
	mu    sync.Mutex
	stall []StallRecord
	esc   []EscalationRecord
}

var _ TranscriptDiagnostics = (*diagStore)(nil)

func (s *diagStore) AppendStall(r StallRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stall = append(s.stall, r)
	return nil
}

func (s *diagStore) AppendEscalation(r EscalationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.esc = append(s.esc, r)
	return nil
}

func (s *diagStore) AppendRetry(RetryRecord) error                 { return nil }
func (s *diagStore) AppendTail(TailRecord) error                   { return nil }
func (s *diagStore) AppendPrefixDivergence(PrefixDivergence) error { return nil }
func (s *diagStore) AppendTransport(provider.TransportInfo) error  { return nil }
func (s *diagStore) AppendCacheCliff(CacheCliff) error             { return nil }
func (s *diagStore) stalls() []StallRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StallRecord(nil), s.stall...)
}
func (s *diagStore) escalations() []EscalationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]EscalationRecord(nil), s.esc...)
}

// The rows are written from the events, so an event a component emits is
// persisted with no hook of its own, and the row is written before a client
// hears of it: the observers run ahead of the sink. That order is what the
// private observers gave, and a reader of the session file relies on a live
// event never describing a row that does not exist yet.
func TestStallAndEscalationRowsAreWrittenFromTheirEvents(t *testing.T) {
	a := newTestAgent(nil, "m", "", Registry{})
	st := &diagStore{}
	a.AttachTranscriptStore(st)

	stall := StallRecord{Axis: "spin", Tool: "read", Detail: "same result", Rung: 1}
	esc := EscalationRecord{Reason: "spinning", Tool: "read", ToModel: "bigger", Disposition: EscalationSwitched}
	var rowsWhenSunk []int
	sink := a.wrapSink(func(ev AgentEvent) {
		rowsWhenSunk = append(rowsWhenSunk, len(st.stalls())+len(st.escalations()))
	})
	sink(EvStall{StallRecord: stall})
	sink(EvEscalation{EscalationRecord: esc})

	if got := st.stalls(); len(got) != 1 || got[0] != stall {
		t.Errorf("stall rows = %+v, want [%+v]", got, stall)
	}
	if got := st.escalations(); len(got) != 1 || got[0] != esc {
		t.Errorf("escalation rows = %+v, want [%+v]", got, esc)
	}
	if len(rowsWhenSunk) != 2 || rowsWhenSunk[0] != 1 || rowsWhenSunk[1] != 2 {
		t.Errorf("rows present when the sink heard each event = %v, want [1 2]: a client heard of a row before it was written", rowsWhenSunk)
	}
}

// A store without diagnostics gets none, and attaching one costs nothing: the
// event observer is only registered for a store that keeps the rows.
func TestAStoreWithoutDiagnosticsIsNotHandedStallRows(t *testing.T) {
	a := newTestAgent(nil, "m", "", Registry{})
	a.AttachTranscriptStore(NewMemoryTranscriptStore())
	a.wrapSink(func(AgentEvent) {})(EvStall{StallRecord: StallRecord{Tool: "read", Rung: 1}})
	if n := len(a.eventObservers()); n != 0 {
		t.Errorf("%d event observers registered for a store that keeps no diagnostics", n)
	}
}
