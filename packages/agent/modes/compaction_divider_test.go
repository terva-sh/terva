package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// compactedSnapshot is what the daemon broadcasts after a compaction: the
// synthetic summary standing in for the folded-away turns, then the tail it kept
// verbatim.
//
// The summary is a provider.Message shaped exactly as core.Compact shapes it, and
// it reaches the wire through core.MessageToWireFull — the real serialization the
// daemon uses. That conversion is precisely where the compaction marker used to be
// dropped, so a test that hand-set the wire fields instead would sail straight past
// the bug.
func compactedSnapshot() ctrlproto.Snapshot {
	summary := core.MessageToWireFull(provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Content{
			provider.TextBlock{Text: "## Context Summary (compacted)\n\nwe renamed the widget"},
		},
		Meta: map[string]string{
			core.MetaCompaction:   "true",
			core.MetaTokensBefore: "112000",
		},
	})
	tail := core.WireMessage{Role: "user", Content: []core.WireBlock{
		{Type: "text", Text: "carry on then"},
	}}
	return ctrlproto.Snapshot{
		Session:  ctrlproto.SessionInfo{ID: "s1"},
		Messages: []core.WireMessage{summary, tail},
	}
}

// TestCompactionRendersAsDividerNotUserBubble is the end-to-end pin for the bug
// this whole change exists for.
//
// The TUI is carrier-backed: every message it draws is rebuilt by
// core.MessageFromWire. WireMessage had no compaction marker and no Meta field,
// so the summary arrived as a plain RoleUser message and the TUI drew it as a
// user bubble containing raw "## Context Summary" markdown — while the collapsed/
// expandable compaction block that view.go has carried all along could never fire.
//
// The old view tests missed it by constructing provider.Message and setting Meta
// directly: they tested a renderer the wire cannot actually produce. This one
// drives the real snapshot path, so it fails if the marker is ever dropped again
// anywhere between compact.go and the screen.
func TestCompactionRendersAsDividerNotUserBubble(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 8)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	fc.stream <- ctrlproto.SnapshotEvent(compactedSnapshot())

	// The tail proves the snapshot landed, so a missing divider below is a real
	// failure rather than a race with the pump.
	h.waitText("carry on then")
	h.waitText("compacted")

	screen := h.term.Screen().Text()
	if !strings.Contains(screen, "112k") {
		t.Errorf("divider should carry the pre-compaction token count; screen:\n%s", screen)
	}
	// The tell for the regression: the summary's raw heading on screen means it
	// rendered as an ordinary message rather than as a divider.
	if strings.Contains(screen, "Context Summary") {
		t.Errorf("compaction summary leaked its raw markdown heading — it rendered as a user bubble, not a divider; screen:\n%s", screen)
	}
	// Collapsed, the summary body stays behind the rule.
	if strings.Contains(screen, "we renamed the widget") {
		t.Errorf("collapsed divider should hide the summary body; screen:\n%s", screen)
	}

	// ctrl+o expands it — the path that was unreachable while Meta could not cross.
	h.term.Type("\x0f")
	h.waitText("we renamed the widget")
}
