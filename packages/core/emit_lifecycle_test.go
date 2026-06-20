package core

import "testing"

// EmitLifecycle is the path that lets compaction events (emitted outside
// the Prompt loop) reach the OnEvent observer / extension fanout. Without
// it, transcript_compacted and compact_start never fire from real
// compaction. Verify it delivers in order.
func TestEmitLifecycleReachesOnEvent(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})
	var got []string
	a.OnEvent = func(ev AgentEvent) { got = append(got, ev.Type()) }

	a.EmitLifecycle(EvCompactStart{Reason: "context near limit"})
	a.EmitLifecycle(EvCompactEnd{})

	if len(got) != 2 || got[0] != "compact_start" || got[1] != "compact_end" {
		t.Fatalf("got %v, want [compact_start compact_end]", got)
	}
}

// EmitLifecycle is nil-safe: an agent with no OnEvent (headless, no
// extensions) must not panic.
func TestEmitLifecycleNilOnEventSafe(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})
	a.EmitLifecycle(EvCompactStart{}) // must not panic
}
