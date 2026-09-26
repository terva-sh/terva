package core

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/provider"
)

// A dispatch observer is told the request as it went on the wire, then hears
// its stream, event by event and in order, before the engine acts on each.
func TestADispatchObserverHearsTheRequestAndItsStream(t *testing.T) {
	transport := provider.TransportInfo{ConnReused: true, Ray: "aa-SJC"}
	client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		return append([]provider.Event{provider.EventTransport{Info: transport}}, saidText("ok", 100)...), nil
	}}
	a := newTestAgent(client, "m", "sys", Registry{})
	var sent []provider.Request
	var kinds []string
	a.addDispatchObserver(DispatchObserver{
		Sent: func(req provider.Request) { sent = append(sent, req) },
		Event: func(ev provider.Event) {
			switch e := ev.(type) {
			case provider.EventTransport:
				if e.Info != transport {
					t.Errorf("transport = %+v, want %+v", e.Info, transport)
				}
				kinds = append(kinds, "transport")
			case provider.EventUsage:
				kinds = append(kinds, "usage")
			case provider.EventDone:
				kinds = append(kinds, "done")
			}
		},
	})
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].System != client.calls()[0].System || len(sent[0].Messages) != len(client.calls()[0].Messages) {
		t.Fatalf("Sent saw %d requests, want the one the provider got", len(sent))
	}
	if got := len(kinds); got != 3 || kinds[0] != "transport" || kinds[1] != "usage" || kinds[2] != "done" {
		t.Errorf("Event saw %v, want [transport usage done]", kinds)
	}
}

// A request the provider refused never reached it, so no observer hears of it.
func TestARefusedRequestIsNotReportedAsSent(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		return nil, errors.New("connection refused")
	}}
	a := newTestAgent(client, "m", "sys", Registry{})
	a.maxRetries = 0
	sent := 0
	a.addDispatchObserver(DispatchObserver{Sent: func(provider.Request) { sent++ }})
	_ = a.Prompt(context.Background(), "go", nil, nil)
	if sent != 0 {
		t.Errorf("Sent was called %d times for a request that never reached the provider", sent)
	}
}

// An observer with no hooks is not registered.
func TestAnEmptyDispatchObserverIsIgnored(t *testing.T) {
	a := newTestAgent(nil, "m", "sys", Registry{})
	a.addDispatchObserver(DispatchObserver{})
	if n := len(a.dispatchObservers()); n != 0 {
		t.Errorf("%d observers registered, want 0", n)
	}
}

// A component's diagnostic row reaches every attached store that keeps
// diagnostics, and a failed write latches the persistence error as a
// transcript row's would.
func TestAppendDiagnosticReachesTheAttachedStore(t *testing.T) {
	a := newTestAgent(nil, "m", "sys", Registry{})
	// No store: nothing to write to, and nothing fails.
	a.AppendDiagnostic(func(TranscriptDiagnostics) error { return errors.New("must not run") })
	if err := a.PersistenceError(); err != nil {
		t.Fatalf("a write with no store latched %v", err)
	}

	// A store without diagnostics gets nothing either.
	a.AttachTranscriptStore(NewMemoryTranscriptStore())
	a.AppendDiagnostic(func(TranscriptDiagnostics) error { return errors.New("must not run") })
	if err := a.PersistenceError(); err != nil {
		t.Fatalf("a store without diagnostics was written: %v", err)
	}

	st := &diagStore{}
	a.AttachTranscriptStore(st)
	rec := StallRecord{Tool: "read", Rung: 1}
	a.AppendDiagnostic(func(s TranscriptDiagnostics) error { return s.AppendStall(rec) })
	if got := st.stalls(); len(got) != 1 || got[0] != rec {
		t.Fatalf("the diagnostics store got %+v, want [%+v]", got, rec)
	}

	boom := errors.New("disk full")
	a.AppendDiagnostic(func(TranscriptDiagnostics) error { return boom })
	if err := a.PersistenceError(); !errors.Is(err, boom) {
		t.Errorf("a failed diagnostic write latched %v, want it to wrap %v", err, boom)
	}
}
