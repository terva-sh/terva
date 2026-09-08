package extdriver

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

func capTestExt(t *testing.T) *Extension {
	t.Helper()
	f, err := os.Create(filepath.Join(testsupport.TempDir(t), "ext.log"))
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

// An extension may mark at most maxEssentialTools tools essential; excess
// essential registrations still register but are downgraded to ordinary
// (deferred) tools so they can't pin the whole surface always-visible.
func TestRegisterEssentialToolCap(t *testing.T) {
	d := &Driver{toolIndex: map[string]*Extension{}, commandIndex: map[string]*Extension{}}
	ext := capTestExt(t)
	total := maxEssentialTools + 5
	for i := 0; i < total; i++ {
		d.registerTool(ext, extproto.RegisterToolFromExt{Name: fmt.Sprintf("tool%d", i), Essential: true})
	}
	if len(ext.tools) != total {
		t.Fatalf("all tools should still register, got %d want %d", len(ext.tools), total)
	}
	essential := 0
	for _, tl := range ext.tools {
		if tl.Essential {
			essential++
		}
	}
	if essential != maxEssentialTools {
		t.Errorf("essential tools capped at %d, got %d", maxEssentialTools, essential)
	}
}

// A display hint is presentation, so a malformed one never costs the
// registration: the tool still registers and the model can still call it. What
// changes is how much of the hint survives.
func TestRegisterToolDisplayNormalized(t *testing.T) {
	long := strings.Repeat("x", maxDisplaySubject+1)
	many := make([]string, maxDisplayRedact+5)
	for i := range many {
		many[i] = fmt.Sprintf("key%d", i)
	}

	cases := []struct {
		name string
		in   *extproto.ToolDisplay
		want *extproto.ToolDisplay
	}{
		{"no hint at all", nil, nil},
		{
			"a good hint arrives intact",
			&extproto.ToolDisplay{Subject: "{city}", Body: "table", Redact: []string{"api_key"}},
			&extproto.ToolDisplay{Subject: "{city}", Body: "table", Redact: []string{"api_key"}},
		},
		{
			// The client falls back to text on an unknown body anyway. Clearing
			// it here means the wire carries only names the client knows.
			"an unknown body is cleared and the subject kept",
			&extproto.ToolDisplay{Subject: "{q}", Body: "hologram"},
			&extproto.ToolDisplay{Subject: "{q}"},
		},
		{
			"an oversized subject is dropped, not truncated",
			&extproto.ToolDisplay{Subject: long, Body: "json"},
			&extproto.ToolDisplay{Body: "json"},
		},
		{
			"empty redact keys name no argument and go",
			&extproto.ToolDisplay{Subject: "{q}", Redact: []string{"", "token", ""}},
			&extproto.ToolDisplay{Subject: "{q}", Redact: []string{"token"}},
		},
		{
			// Nothing usable left, so the tool carries no hint rather than an
			// empty one a client would have to special-case.
			"a hint that loses everything becomes no hint",
			&extproto.ToolDisplay{Subject: long, Body: "hologram", Redact: []string{""}},
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Driver{toolIndex: map[string]*Extension{}, commandIndex: map[string]*Extension{}}
			ext := capTestExt(t)
			d.registerTool(ext, extproto.RegisterToolFromExt{Name: "weather", Display: tc.in})
			if len(ext.tools) != 1 {
				t.Fatalf("the tool must register whatever the hint says; got %d tools", len(ext.tools))
			}
			got := ext.tools[0].Display
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("display = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("the redact cap keeps the first keys", func(t *testing.T) {
		d := &Driver{toolIndex: map[string]*Extension{}, commandIndex: map[string]*Extension{}}
		ext := capTestExt(t)
		d.registerTool(ext, extproto.RegisterToolFromExt{
			Name:    "weather",
			Display: &extproto.ToolDisplay{Redact: many},
		})
		got := ext.tools[0].Display
		if got == nil || len(got.Redact) != maxDisplayRedact {
			t.Fatalf("redact capped at %d, got %+v", maxDisplayRedact, got)
		}
		if got.Redact[0] != "key0" {
			t.Errorf("the cap should keep the first keys, got %q first", got.Redact[0])
		}
	})
}

// Every drop is written to the extension's own log. Without this an author
// sees a card that looks generic and has nothing to read that says why.
func TestRegisterToolDisplayLogsWhatItDropped(t *testing.T) {
	d := &Driver{toolIndex: map[string]*Extension{}, commandIndex: map[string]*Extension{}}
	ext := capTestExt(t)
	d.registerTool(ext, extproto.RegisterToolFromExt{
		Name:    "weather",
		Display: &extproto.ToolDisplay{Subject: strings.Repeat("x", maxDisplaySubject+1), Body: "hologram"},
	})
	log, err := os.ReadFile(ext.logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"weather", "display.body", "hologram", "display.subject"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("the extension log does not mention %q; it says:\n%s", want, log)
		}
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
