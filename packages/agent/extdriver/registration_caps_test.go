package extdriver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/extproto"
)

func capTestExt(t *testing.T) *Extension {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "ext.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return &Extension{
		Manifest:  Manifest{Name: "ext"},
		logFile:   f,
		eventSubs: map[string]struct{}{},
	}
}

func countUnknownSubs(ext *Extension) int {
	n := 0
	for ev := range ext.eventSubs {
		if !extproto.IsKnownEvent(ev) {
			n++
		}
	}
	return n
}

// A runaway extension can't register more than maxExtTools tools.
func TestRegisterToolCap(t *testing.T) {
	d := &Driver{toolIndex: map[string]*Extension{}, commandIndex: map[string]*Extension{}}
	ext := capTestExt(t)
	for i := 0; i < maxExtTools+20; i++ {
		d.registerTool(ext, extproto.RegisterToolFromExt{Name: fmt.Sprintf("tool%d", i)})
	}
	if len(ext.tools) != maxExtTools {
		t.Errorf("tools capped at %d, got %d", maxExtTools, len(ext.tools))
	}
}

func TestRegisterCommandCap(t *testing.T) {
	d := &Driver{toolIndex: map[string]*Extension{}, commandIndex: map[string]*Extension{}}
	ext := capTestExt(t)
	for i := 0; i < maxExtCommands+20; i++ {
		d.registerCommand(ext, extproto.RegisterCommandFromExt{Name: fmt.Sprintf("cmd%d", i)})
	}
	if len(ext.commands) != maxExtCommands {
		t.Errorf("commands capped at %d, got %d", maxExtCommands, len(ext.commands))
	}
}

// Event subscriptions cap UNKNOWN names, dropping them first, while known
// events are always accepted — preserving optimistic opt-in and keeping a
// runaway extension's garbage from crowding out real subscriptions.
func TestSubscribeEventsCapDropsUnknownFirst(t *testing.T) {
	ext := capTestExt(t)

	flood := make([]string, 0, maxExtEventSubs+50)
	for i := 0; i < maxExtEventSubs+50; i++ {
		flood = append(flood, fmt.Sprintf("garbage_%d", i))
	}
	ext.subscribeEvents(flood)
	if got := countUnknownSubs(ext); got != maxExtEventSubs {
		t.Errorf("unknown subscriptions capped at %d, got %d", maxExtEventSubs, got)
	}

	// Known events are accepted even though the unknown cap is full.
	ext.subscribeEvents([]string{"session_start", "transcript_compacted"})
	for _, ev := range []string{"session_start", "transcript_compacted"} {
		if _, ok := ext.eventSubs[ev]; !ok {
			t.Errorf("known event %q must be accepted despite the unknown cap", ev)
		}
	}
	// Knowns don't count against the unknown cap.
	if got := countUnknownSubs(ext); got != maxExtEventSubs {
		t.Errorf("unknown count should stay at the cap, got %d", got)
	}

	// A future/unknown event is still recorded when under the cap (opt-in
	// degradation preserved) — proven on a fresh extension.
	fresh := capTestExt(t)
	fresh.subscribeEvents([]string{"some_future_event"})
	if _, ok := fresh.eventSubs["some_future_event"]; !ok {
		t.Error("an unknown name under the cap must still be recorded (graceful opt-in)")
	}
}
