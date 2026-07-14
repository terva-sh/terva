package core

import (
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The property the assignable On* fields could not provide: a second host that
// observes messages does not unwire the first. Before this change, the second
// assignment silently replaced durable transcript persistence, with no compile
// error and no test signal unless a test read the JSONL back off disk.
func TestMessageObserversCompose(t *testing.T) {
	a := &Agent{}

	var order []string
	a.AddMessageObserver(func(provider.Message) { order = append(order, "persist") })
	a.AddMessageObserver(func(provider.Message) { order = append(order, "mirror") })

	a.fireMessageAppended(provider.Message{Role: provider.RoleAssistant})

	if len(order) != 2 {
		t.Fatalf("observers fired %v; want both, so a mirror cannot unwire persistence", order)
	}
	if order[0] != "persist" || order[1] != "mirror" {
		t.Fatalf("delivery order = %v; want registration order", order)
	}
}

// Registration order is delivery order for every observer kind. The workspace
// relies on it (broadcast to clients before the slower extension fan-out), and
// ACP relies on it (translate to session/update before the extension fan-out,
// so a fan-out can never preempt the editor's narration).
func TestEventObserversDeliverInRegistrationOrder(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})

	var order []string
	a.AddEventObserver(func(AgentEvent) { order = append(order, "first") })
	a.AddEventObserver(func(AgentEvent) { order = append(order, "second") })

	a.EmitLifecycle(EvCompactStart{Reason: "test"})

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("delivery order = %v; want [first second]", order)
	}
}

// wrapSink runs observers before the per-Prompt sink, so a sink that mutates
// shared state cannot beat an observer to it. (The old wrapSink did the same;
// this pins it against the snapshot rewrite.)
func TestEventObserversRunBeforeSink(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})

	var order []string
	a.AddEventObserver(func(AgentEvent) { order = append(order, "observer") })
	wrapped := a.wrapSink(func(AgentEvent) { order = append(order, "sink") })
	wrapped(EvCompactEnd{})

	if len(order) != 2 || order[0] != "observer" || order[1] != "sink" {
		t.Fatalf("order = %v; want observer before sink", order)
	}
}

// A nil observer is dropped at registration, so callers wiring an optional hook
// (ACP's sa.ObserveEvent) need no branch. This is what let the hand-rolled
// compose-or-overwrite in acp/agent.go collapse to two unconditional calls.
func TestNilObserverIsDropped(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})

	a.AddEventObserver(nil)
	a.AddMessageObserver(nil)
	a.AddUsageObserver(nil)
	a.AddTranscriptCompactedObserver(nil)
	a.AddImageExcludedObserver(nil)

	// Each emit path must be a no-op rather than a nil-call panic.
	a.EmitLifecycle(EvCompactEnd{})
	a.fireMessageAppended(provider.Message{})
	a.fireUsage(provider.Usage{}, provider.Usage{})
	a.fireTranscriptCompacted(nil, CompactResult{})
	a.fireImageExcluded("deadbeef")

	if got := a.eventObservers(); len(got) != 0 {
		t.Fatalf("nil observer was registered: %d present", len(got))
	}
}

// Observers fire outside the registry lock, so an observer may take its own
// locks, and registration may race an emit. Meaningful only under -race.
func TestObserverRegistrationIsRaceFree(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})

	var mu sync.Mutex
	var seen int
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.AddMessageObserver(func(provider.Message) {
				mu.Lock()
				seen++
				mu.Unlock()
			})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.fireMessageAppended(provider.Message{})
		}()
	}
	wg.Wait()

	// Every registered observer must fire on a subsequent emit.
	mu.Lock()
	seen = 0
	mu.Unlock()
	a.fireMessageAppended(provider.Message{})
	mu.Lock()
	defer mu.Unlock()
	if seen != 8 {
		t.Fatalf("after 8 registrations an emit reached %d observers; want 8", seen)
	}
}
