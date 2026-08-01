package tui

// Spacing of consecutive tool calls under the reduced displays.
//
// The reported symptom: in one screen of scrollback, eight reads sit flush
// against each other while six edits each float in their own paragraph. The
// rhythm looks arbitrary, and it is — but not randomly so. It encodes how the
// MODEL batched its calls, which is the one thing nobody is reading the
// transcript for.
//
// Mechanism: Build's separator is per MESSAGE. Eight results in one tool
// message render eight adjacent lines and one trailing blank; eight results in
// eight messages render eight lines each with its own blank. In full-box mode
// that is right — boxes need air. In minimal mode a row is a LIST ITEM, and
// list items are not separated by blank lines.

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// toolPair returns the assistant/tool message pair for one completed call.
func toolPair(id, name, arg string, lines int) []provider.Message {
	body := strings.TrimSuffix(strings.Repeat("x\n", lines), "\n")
	return []provider.Message{
		{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID: id, Name: name,
				Arguments: json.RawMessage(`{"path":"` + arg + `"}`),
			}},
		},
		{
			Role: provider.RoleTool,
			Content: []provider.Content{provider.ToolResultBlock{
				CallID:  id,
				Content: []provider.Content{provider.TextBlock{Text: body}},
			}},
		},
	}
}

// batchedCalls packs n calls into ONE assistant message and ONE tool message —
// the shape a parallel tool batch produces.
func batchedCalls(n int) []provider.Message {
	var calls, results []provider.Content
	for i := 0; i < n; i++ {
		id := "toolu_b" + string(rune('a'+i))
		calls = append(calls, provider.ToolCallBlock{
			ID: id, Name: "read",
			Arguments: json.RawMessage(`{"path":"f.go"}`),
		})
		results = append(results, provider.ToolResultBlock{
			CallID:  id,
			Content: []provider.Content{provider.TextBlock{Text: "a\nb"}},
		})
	}
	return []provider.Message{
		{Role: provider.RoleAssistant, Content: calls},
		{Role: provider.RoleTool, Content: results},
	}
}

// toolRows returns the indices of the non-blank rows, and the rows themselves.
func toolRows(t *testing.T, v View) (rows []string) {
	t.Helper()
	for _, l := range strings.Split(stripANSI(strings.Join(v.Build(80), "\n")), "\n") {
		rows = append(rows, l)
	}
	return rows
}

// blanksInside counts blank rows strictly between the first and last row that
// mentions a tool. That span is the run; a blank inside it is the defect.
func blanksInside(rows []string, marker string) int {
	first, last := -1, -1
	for i, r := range rows {
		if strings.Contains(r, marker) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || last <= first {
		return 0
	}
	n := 0
	for _, r := range rows[first+1 : last] {
		if strings.TrimSpace(r) == "" {
			n++
		}
	}
	return n
}

// The defect, stated as the property that was violated: a run of tool calls
// must look the same whether the model issued them in one message or in six.
// Nothing in the transcript's meaning changes between those two, so nothing in
// its shape should either.
func TestMinimalToolRunSpacingIsIndependentOfBatching(t *testing.T) {
	var sequential []provider.Message
	for i := 0; i < 6; i++ {
		sequential = append(sequential, toolPair("toolu_s"+string(rune('a'+i)), "read", "f.go", 2)...)
	}

	for _, tc := range []struct {
		name string
		msgs []provider.Message
	}{
		{"batched into one message", batchedCalls(6)},
		{"one call per message", sequential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: tc.msgs}
			rows := toolRows(t, v)
			if got := blanksInside(rows, "read"); got != 0 {
				t.Errorf("%d blank row(s) inside a run of 6 calls; want 0\n%s",
					got, strings.Join(rows, "\n"))
			}
		})
	}
}

// A failed call is still a member of the run. It was the loneliest row on the
// reported screen — its own paragraph, three rows tall for one line of content.
func TestAFailedCallDoesNotBreakTheRun(t *testing.T) {
	msgs := toolPair("toolu_1", "read", "a.go", 2)
	msgs = append(msgs, provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.Content{provider.ToolCallBlock{
			ID: "toolu_2", Name: "edit", Arguments: json.RawMessage(`{"path":"a.go"}`),
		}},
	})
	msgs = append(msgs, provider.Message{
		Role: provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{
			CallID: "toolu_2", IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: "no match"}},
		}},
	})
	msgs = append(msgs, toolPair("toolu_3", "read", "a.go", 2)...)

	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	if got := blanksInside(rows, "· "); got != 0 {
		t.Errorf("%d blank row(s) inside a run containing a failure; want 0\n%s",
			got, strings.Join(rows, "\n"))
	}
}

// Prose is what a run is separated FROM. A blank on each side of the run is the
// whole point of the separator; removing it inside must not remove it here.
func TestProseStaysSeparatedFromTheRun(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "looking now."}}},
	}
	msgs = append(msgs, toolPair("toolu_1", "read", "a.go", 2)...)
	msgs = append(msgs, toolPair("toolu_2", "read", "b.go", 2)...)
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "found it."}},
	})

	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	joined := strings.Join(rows, "\n")

	before, after := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "looking now.") {
			before = i
		}
		if strings.Contains(r, "found it.") {
			after = i
		}
	}
	if before < 0 || after < 0 {
		t.Fatalf("prose missing:\n%s", joined)
	}
	if strings.TrimSpace(rows[before+1]) != "" {
		t.Errorf("no blank between prose and the run that follows it:\n%s", joined)
	}
	if strings.TrimSpace(rows[after-1]) != "" {
		t.Errorf("no blank between the run and the prose that follows it:\n%s", joined)
	}
}

// The packed path emits its own anchors, and view_anchors_test.go never runs
// under a reduced display — so this is the only thing standing between /jump
// and a row that does not exist. Every message must anchor in bounds, and in
// transcript order.
func TestPackedRunAnchorsStayInBoundsAndInOrder(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}},
	}
	msgs = append(msgs, batchedCalls(3)...)
	msgs = append(msgs, toolPair("toolu_1", "edit", "a.go", 4)...)
	msgs = append(msgs, toolPair("toolu_2", "edit", "b.go", 4)...)
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "done."}},
	})

	for _, display := range []ToolDisplayMode{ToolDisplayMinimal, ToolDisplayHidden} {
		v := View{Theme: Dark, ToolDisplay: display, Messages: msgs}
		lines, anchors := v.BuildWithAnchors(80)
		if len(anchors) != len(msgs) {
			t.Errorf("display %v: %d anchors for %d messages", display, len(anchors), len(msgs))
		}
		prevRow := -1
		for _, a := range anchors {
			if a.Row < 0 || a.Row > len(lines) {
				t.Errorf("display %v: message %d anchors to row %d, out of %d lines",
					display, a.MessageIdx, a.Row, len(lines))
			}
			if a.Row < prevRow {
				t.Errorf("display %v: anchors go backwards at message %d (%d after %d)",
					display, a.MessageIdx, a.Row, prevRow)
			}
			prevRow = a.Row
		}
	}
}

// Hidden mode swallows a run of successful calls completely. A blank row would
// be the only trace left of it — a paragraph break with nothing on either side.
func TestHiddenModeLeavesNoBlankBehindASwallowedRun(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "one."}}},
	}
	msgs = append(msgs, batchedCalls(3)...)
	msgs = append(msgs, toolPair("toolu_1", "read", "a.go", 2)...)
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "two."}},
	})

	v := View{Theme: Dark, ToolDisplay: ToolDisplayHidden, Messages: msgs}
	rows := toolRows(t, v)
	one, two := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "one.") {
			one = i
		}
		if strings.Contains(r, "two.") {
			two = i
		}
	}
	if one < 0 || two < 0 {
		t.Fatalf("prose missing:\n%s", strings.Join(rows, "\n"))
	}
	// One blank between the two prose rows, not two stacked where the
	// invisible run used to be.
	if got := two - one; got != 2 {
		t.Errorf("%d rows between the prose either side of a hidden run; want 2 (one blank)\n%s",
			got, strings.Join(rows, "\n"))
	}
}

// Full boxes keep their per-message blank: two adjacent boxes flush against
// each other read as one malformed box.
func TestFullBoxesKeepTheirSeparator(t *testing.T) {
	msgs := toolPair("toolu_1", "read", "a.go", 2)
	msgs = append(msgs, toolPair("toolu_2", "read", "b.go", 2)...)

	v := View{Theme: Dark, ToolDisplay: ToolDisplayFull, Messages: msgs}
	rows := toolRows(t, v)
	blanks := 0
	for _, r := range rows {
		if strings.TrimSpace(r) == "" {
			blanks++
		}
	}
	if blanks == 0 {
		t.Errorf("full boxes lost their separator:\n%s", strings.Join(rows, "\n"))
	}
}
