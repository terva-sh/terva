package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// cdTranscript is a two-turn session shaped like real work: the first
// turn answers with prose and code, the second is broken by tool work so
// the assistant speaks twice with a tool result between.
//
//	0 user   "first question"
//	1 asst   thinking + prose + fence
//	2 user   "second question about the wrap bug"
//	3 asst   "Looking now."
//	4 tool   result, which must never be offered
//	5 asst   "Second answer."
func cdTranscript() []provider.Message {
	return []provider.Message{
		jdUser("first question"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ReasoningBlock{Summary: "weighing the options carefully"},
			provider.TextBlock{Text: "First answer.\n\n```go\nx := 1\n```"},
		}},
		jdUser("second question about the wrap bug"),
		jdAsst("Looking now."),
		{Role: provider.RoleTool, Content: []provider.Content{
			provider.TextBlock{Text: "tool output nobody copies"},
		}},
		jdAsst("Second answer."),
	}
}

func openCopy(t *testing.T) *CopyDialog {
	t.Helper()
	d := NewCopyDialog()
	d.Open(cdTranscript(), "")
	if !d.Active() {
		t.Fatal("dialog is not active after Open")
	}
	if d.stage != copyStageTurns {
		t.Fatalf("stage = %v after Open, want the turn list", d.stage)
	}
	return d
}

func key(k tui.KeyKind) tui.Key { return tui.Key{Kind: k} }

func TestCopyDialogOpensOnTheTurnList(t *testing.T) {
	d := openCopy(t)
	if len(d.visibleTurn) != 2 {
		t.Fatalf("got %d turns, want 2", len(d.visibleTurn))
	}
	if d.visibleTurn[0].Preview != "first question" {
		t.Errorf("first row = %q", d.visibleTurn[0].Preview)
	}
}

func TestCopyDialogEnterDescendsToParts(t *testing.T) {
	d := openCopy(t)
	if act := d.HandleKey(key(tui.KeyEnter)); act.Copy || act.Close {
		t.Fatalf("descending should neither copy nor close, got %+v", act)
	}
	if d.stage != copyStageParts {
		t.Fatal("enter on a turn did not descend")
	}
	if d.turnNo != 1 {
		t.Errorf("turnNo = %d, want 1", d.turnNo)
	}
	roles := map[PartRole]int{}
	for _, p := range d.visiblePart {
		roles[p.Role]++
	}
	if roles[RoleUser] == 0 || roles[RoleThinking] == 0 || roles[RoleReply] == 0 {
		t.Errorf("turn 1 should offer prompt, thinking and reply; got %v", roles)
	}
}

// The bulk of a transcript is tool traffic and none of it is ever copied
// out of a picker. Offering it would bury the three things that are.
func TestCopyDialogNeverOffersToolResults(t *testing.T) {
	d := openCopy(t)
	d.HandleKey(key(tui.KeyDown))  // turn 2, the one with tool work
	d.HandleKey(key(tui.KeyEnter)) // descend
	for _, p := range d.visiblePart {
		if strings.Contains(p.Text, "tool output nobody copies") {
			t.Fatalf("a tool result was offered as a part: %q", p.Preview)
		}
	}
	if len(d.visiblePart) == 0 {
		t.Fatal("turn 2 offered nothing at all")
	}
}

// Escape backs out one level. Closing instead would make a wrong descent
// cost a reopen, which is the whole reason the dialog has two stages.
func TestCopyDialogEscapeGoesBackBeforeItCloses(t *testing.T) {
	d := openCopy(t)
	d.HandleKey(key(tui.KeyEnter))
	if act := d.HandleKey(key(tui.KeyEsc)); act.Close {
		t.Fatal("esc in the part list closed the dialog instead of going back")
	}
	if d.stage != copyStageTurns {
		t.Fatal("esc did not return to the turn list")
	}
	if !d.Active() {
		t.Fatal("dialog closed while going back a stage")
	}
	if act := d.HandleKey(key(tui.KeyEsc)); !act.Close {
		t.Fatal("esc on the turn list did not close")
	}
	if d.Active() {
		t.Fatal("dialog still active after closing")
	}
}

// The payload is markdown source sliced from the message, fence markers
// and all. Anything else pastes as garbage.
func TestCopyDialogEnterCopiesThePartVerbatim(t *testing.T) {
	d := openCopy(t)
	d.HandleKey(key(tui.KeyEnter))
	var fence int = -1
	for i, p := range d.visiblePart {
		if p.Kind == tui.BlockFence {
			fence = i
			break
		}
	}
	if fence < 0 {
		t.Fatal("no fence part in turn 1")
	}
	for range fence {
		d.HandleKey(key(tui.KeyDown))
	}
	act := d.HandleKey(key(tui.KeyEnter))
	if !act.Copy {
		t.Fatal("enter on a part did not copy")
	}
	if want := "```go\nx := 1\n```"; act.Text != want {
		t.Errorf("copied %q, want %q", act.Text, want)
	}
	if act.Kind != tui.BlockFence {
		t.Errorf("kind = %v, want BlockFence", act.Kind)
	}
	if d.Active() {
		t.Error("dialog stayed open after copying")
	}
}

// ctrl+y is the "I did want the whole thing" escape hatch, and it works
// from the turn list without descending.
func TestCopyDialogCtrlYCopiesTheWholeReplyFromTheTurnList(t *testing.T) {
	d := openCopy(t)
	act := d.HandleKey(key(tui.KeyCtrlY))
	if !act.Copy || !act.Whole {
		t.Fatalf("ctrl+y did not copy the whole reply: %+v", act)
	}
	if !strings.Contains(act.Text, "First answer.") || !strings.Contains(act.Text, "x := 1") {
		t.Errorf("whole reply is missing content: %q", act.Text)
	}
	if strings.Contains(act.Text, "first question") {
		t.Error("the whole reply swept in the user's prompt")
	}
	if strings.Contains(act.Text, "weighing the options") {
		t.Error("the whole reply swept in the thinking")
	}
}

// A turn split by tool work has two assistant messages. Both belong to
// the reply, so ctrl+y must not stop at the first.
func TestCopyDialogWholeReplyJoinsBothHalvesOfASplitTurn(t *testing.T) {
	d := openCopy(t)
	d.HandleKey(key(tui.KeyDown))
	act := d.HandleKey(key(tui.KeyCtrlY))
	if !act.Copy {
		t.Fatal("ctrl+y did not copy")
	}
	if !strings.Contains(act.Text, "Looking now.") || !strings.Contains(act.Text, "Second answer.") {
		t.Errorf("split reply lost a half: %q", act.Text)
	}
}

// Typing filters. Every printable key belongs to the filter, which is
// exactly why copy-whole had to be a chord.
func TestCopyDialogRunesFilterTheCurrentStage(t *testing.T) {
	d := openCopy(t)
	for _, r := range "wrap" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	if d.turnFilter != "wrap" {
		t.Fatalf("turnFilter = %q", d.turnFilter)
	}
	if len(d.visibleTurn) != 1 {
		t.Fatalf("got %d turns for \"wrap\", want 1", len(d.visibleTurn))
	}
	d.HandleKey(key(tui.KeyBackspace))
	if d.turnFilter != "wra" {
		t.Errorf("backspace left %q", d.turnFilter)
	}
}

// Descending resets the filter. Carrying the turn filter into the parts
// would hide most of the turn the user just chose.
func TestCopyDialogDescendingClearsTheFilter(t *testing.T) {
	d := openCopy(t)
	for _, r := range "wrap" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(key(tui.KeyEnter))
	if d.partFilter != "" {
		t.Errorf("partFilter = %q after descending, want empty", d.partFilter)
	}
	if len(d.visiblePart) == 0 {
		t.Error("no parts visible after descending with a turn filter set")
	}
}

// The kind name is part of the haystack, so "fence" finds the code
// without the user knowing a word that is in it.
func TestCopyDialogFilterMatchesTheKindName(t *testing.T) {
	d := openCopy(t)
	d.HandleKey(key(tui.KeyEnter))
	for _, r := range "fence" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	if len(d.visiblePart) != 1 {
		t.Fatalf("got %d parts for \"fence\", want 1", len(d.visiblePart))
	}
	if d.visiblePart[0].Kind != tui.BlockFence {
		t.Errorf("kind = %v", d.visiblePart[0].Kind)
	}
}

// Thinking that was never recorded contributes nothing, rather than an
// empty row. Sessions saved before thinking was retained are the common
// case here.
func TestCopyDialogSkipsUnrecordedThinking(t *testing.T) {
	msgs := []provider.Message{
		jdUser("q"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ReasoningBlock{Summary: ""},
			provider.TextBlock{Text: "an answer"},
		}},
	}
	for _, p := range partsForTurn(msgs, 0) {
		if p.Role == RoleThinking {
			t.Fatalf("an empty reasoning block produced a thinking part: %q", p.Preview)
		}
	}
}

// Ordinals disambiguate a split turn's two replies, and stay absent when
// there is only one -- a bare "reply" reads better than "reply 1".
func TestReplyOrdinalsOnlyNumberWhenThereAreSeveral(t *testing.T) {
	single := partsForTurn(cdTranscript(), 0)
	if got := replyOrdinals(single); got != nil {
		t.Errorf("single-reply turn got ordinals %v, want none", got)
	}
	split := partsForTurn(cdTranscript(), 2)
	ord := replyOrdinals(split)
	if len(ord) != 2 {
		t.Fatalf("split turn got %d ordinals, want 2", len(ord))
	}
	if ord[3] != 1 || ord[5] != 2 {
		t.Errorf("ordinals = %v, want message 3 first and message 5 second", ord)
	}
}

// Stage two pays for the preview pane, so its chrome is larger. The host
// re-sizes every frame, which is what makes two answers safe.
func TestCopyDialogChromeGrowsForThePreviewPane(t *testing.T) {
	d := openCopy(t)
	turns := d.ChromeRows()
	d.HandleKey(key(tui.KeyEnter))
	parts := d.ChromeRows()
	if parts <= turns {
		t.Errorf("chrome at the part stage = %d, not more than %d at the turn stage", parts, turns)
	}
	if parts != 5+1+copyPreviewRows {
		t.Errorf("chrome = %d, want %d", parts, 5+1+copyPreviewRows)
	}
}

func TestCopyDialogRendersThePreviewOfTheHighlightedPart(t *testing.T) {
	d := openCopy(t)
	d.HandleKey(key(tui.KeyEnter))
	d.MaxRows = 6
	out := strings.Join(d.Render(budgetTheme(), 80), "\n")
	if !strings.Contains(out, d.visiblePart[d.cursor].Preview) {
		t.Errorf("the highlighted part is missing from the render:\n%s", out)
	}
	if !strings.Contains(out, "turn 1") {
		t.Errorf("header does not name the turn:\n%s", out)
	}
}

// An empty transcript must say so rather than draw an empty frame.
func TestCopyDialogHandlesAnEmptyTranscript(t *testing.T) {
	d := NewCopyDialog()
	d.Open(nil, "")
	out := strings.Join(d.Render(budgetTheme(), 80), "\n")
	if !strings.Contains(strings.ToLower(out), "nothing to copy") {
		t.Errorf("empty session render:\n%s", out)
	}
	if act := d.HandleKey(key(tui.KeyEnter)); act.Copy {
		t.Error("enter copied something from an empty transcript")
	}
	if act := d.HandleKey(key(tui.KeyCtrlY)); act.Copy {
		t.Error("ctrl+y copied something from an empty transcript")
	}
}

func TestPreviewPaneRowsWrapsAndCaps(t *testing.T) {
	rows := previewPaneRows(strings.Repeat("abcdefghij", 10), 20, 3)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want the cap of 3", len(rows))
	}
	for i, r := range rows {
		if n := len([]rune(r)); n > 20 {
			t.Errorf("row %d is %d runes wide, want <= 20", i, n)
		}
	}
	if got := previewPaneRows("x", 0, 3); got != nil {
		t.Errorf("a zero width should render nothing, got %v", got)
	}
}

func TestTruncateRunesKeepsItWithinWidth(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("short strings must pass through, got %q", got)
	}
	got := truncateRunes("hello world", 8)
	if n := len([]rune(got)); n != 8 {
		t.Errorf("truncated to %d runes, want 8: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation should be marked, got %q", got)
	}
	// Multibyte must be counted as runes, not bytes, or the row overflows.
	if n := len([]rune(truncateRunes("ääääääää", 4))); n != 4 {
		t.Error("multibyte truncation counted bytes")
	}
}
