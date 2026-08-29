package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

func pinnedClock() func() time.Time {
	return func() time.Time { return time.Unix(0, 0).UTC() }
}

// Thinking must respect the terminal width, and the assistant's PROSE column is
// the oracle: both render markdown at the same inner width, so any gap between
// them is a bug in the thinking path rather than a property of the content.
//
// 🪤 The regression this pins: RenderMarkdown does NOT hard-wrap. It lays out
// block structure and leaves a long line long, and every column that renders
// markdown has to follow it with wrapANSILineKeepStyle. renderReasoningRows did
// not, so one ordinary paragraph of thinking rendered 197 cells wide at width
// 60 and ran off the screen.
func TestReasoningBlockWrapsToWidth(t *testing.T) {
	const width = 60

	cases := []struct {
		name string
		text string
	}{
		{"plain prose", "I need to check the config file and then decide whether the handler should retry, because the endpoint sometimes returns a transient error that the caller cannot distinguish from a permanent one."},
		// 🪤 Keep this path host-free. TestInternalHostOnlyInIdentityDefaults
		// bans the internal Forgejo hostname anywhere outside the identity
		// channel-defaults block, and a realistic checkout path is the easiest
		// way to smuggle one into a fixture.
		{"long unbroken token", "Looking at /var/lib/terva/workspaces/deeply/nested/packages/provider/reasoning_declared_efforts_test.go now."},
		{"fenced code", "Consider:\n\n```go\nif effort := openAICompatEffort(m, eff); effort != \"\" && m.Reasoning && len(m.ReasoningEfforts) > 0 {\n```\n\nthat is the line."},
		{"markdown table", "| rung | wire |\n|---|---|\n| maximum | xhigh, which is the server default when the field is omitted entirely |"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := View{Theme: Dark, ExpandAll: true}
			for _, l := range v.renderReasoningRows([]string{tc.text}, width, true) {
				if w := visibleWidth(l); w > width {
					t.Errorf("thinking row overflows width %d by %d cells: %q", width, w-width, stripANSI(l))
				}
			}
		})
	}
}

// Every body row carries the gutter. That bar is the whole boundary marker:
// without it the thinking runs into the reply with nothing between them, which
// is the complaint that motivated this block.
func TestReasoningBlockDrawsAGutterOnEveryBodyRow(t *testing.T) {
	v := View{Theme: Dark}
	rows := v.renderReasoningRows([]string{"first thought\n\nsecond thought"}, 80, true)
	if len(rows) < 3 {
		t.Fatalf("expected a header and body rows, got %d", len(rows))
	}
	body := 0
	for _, r := range rows[1:] {
		plain := strings.TrimRight(stripANSI(r), " ")
		if plain == "" {
			continue // the trailing separator row
		}
		// TrimRight above strips the trailing space of an EMPTY gutter row (the
		// paragraph gap), so match the bar itself rather than the padded form.
		if !strings.HasPrefix(plain, "  "+strings.TrimRight(thinkingGutter, " ")) {
			t.Errorf("body row lacks the thinking gutter: %q", plain)
		}
		body++
	}
	if body == 0 {
		t.Fatal("no body rows rendered")
	}
}

// Exactly one thinking block is open: the newest. An older one collapses back
// to its marker so a long session does not accumulate walls of stale thinking.
func TestNewestThinkingIsOpenAndOlderOnesCollapse(t *testing.T) {
	v := View{
		Theme: Dark,
		Now:   pinnedClock(),
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "one"}}},
			{Role: provider.RoleAssistant, Content: []provider.Content{
				provider.ReasoningBlock{Summary: "OLDERTHOUGHT about the config"},
				provider.TextBlock{Text: "first answer"},
			}},
			{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "two"}}},
			{Role: provider.RoleAssistant, Content: []provider.Content{
				provider.ReasoningBlock{Summary: "NEWERTHOUGHT about the handler"},
				provider.TextBlock{Text: "second answer"},
			}},
		},
	}
	out := stripANSI(strings.Join(v.Build(100), "\n"))
	if !strings.Contains(out, "NEWERTHOUGHT") {
		t.Errorf("the newest thinking block must render expanded:\n%s", out)
	}
	if strings.Contains(out, "OLDERTHOUGHT") {
		t.Errorf("an older thinking block must collapse to its marker:\n%s", out)
	}
	if !strings.Contains(out, "ctrl+r to expand") {
		t.Errorf("the collapsed block must still offer the expand affordance:\n%s", out)
	}
}

// A turn in flight owns the open slot. Otherwise the previous turn's thinking
// would sit expanded ABOVE the live block, putting two of them on screen with
// the stale one on top.
func TestLiveThinkingClosesTheRecordedBlock(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "one"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ReasoningBlock{Summary: "PREVIOUSTHOUGHT about the config"},
			provider.TextBlock{Text: "first answer"},
		}},
	}
	idle := View{Theme: Dark, Now: pinnedClock(), Messages: msgs}
	if out := stripANSI(strings.Join(idle.Build(100), "\n")); !strings.Contains(out, "PREVIOUSTHOUGHT") {
		t.Fatalf("precondition: the newest block is open when idle:\n%s", out)
	}

	busy := View{Theme: Dark, Now: pinnedClock(), Messages: msgs, StreamingReasoning: "now considering the handler"}
	out := stripANSI(strings.Join(busy.Build(100), "\n"))
	if strings.Contains(out, "PREVIOUSTHOUGHT") {
		t.Errorf("the recorded block must close while a turn streams its own thinking:\n%s", out)
	}
	if !strings.Contains(out, "now considering the handler") {
		t.Errorf("live thinking must render while the turn streams:\n%s", out)
	}
}

// 🪤 The live block is CAPPED, and this is the invariant the old one-line row
// enforced by squashing: the same field carries a short headline from one
// provider and multi-paragraph prose from another (a ~1.5k-char median on
// deepseek, a 48k outlier on codex). Uncapped, the block that exists to
// introduce the answer pushes the answer off the screen.
func TestLiveThinkingIsCappedToItsTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "step number %d of the plan\n\n", i)
	}
	v := View{Theme: Dark, StreamingReasoning: b.String()}
	rows := v.BuildLive(80)

	gutters := 0
	for _, r := range rows {
		if strings.Contains(stripANSI(r), strings.TrimSpace(thinkingGutter)) {
			gutters++
		}
	}
	// The tail rows, plus at most one elision marker.
	if gutters > LiveThinkingTailLines+1 {
		t.Errorf("live thinking drew %d gutter rows; cap is %d (+1 marker)", gutters, LiveThinkingTailLines)
	}

	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "step number 40 of the plan") {
		t.Errorf("the cap must keep the TAIL — the model's latest thought:\n%s", plain)
	}
	if strings.Contains(plain, "step number 1 of the plan") {
		t.Errorf("the head must be dropped, not the tail:\n%s", plain)
	}
}

// Nothing to say, nothing drawn. No header and no reserved row for the many
// providers that send no thinking at all.
func TestLiveThinkingIsAbsentWithoutText(t *testing.T) {
	v := View{Theme: Dark}
	if rows := v.renderLiveThinking(80); rows != nil {
		t.Errorf("rendered %d rows with no thinking; want none: %q", len(rows), rows)
	}
	blank := View{Theme: Dark, StreamingReasoning: "   \n\n  "}
	if rows := blank.renderLiveThinking(80); rows != nil {
		t.Errorf("whitespace-only thinking must draw nothing, got %d rows", len(rows))
	}
}
