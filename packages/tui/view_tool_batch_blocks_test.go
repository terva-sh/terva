package tui

// How a tool run is divided into blocks under the reduced displays.
//
// #515 made a whole run flush, which fixed the six-paragraphs-of-edits problem
// but threw away something real along with the accident: a message carrying
// several results is a batch the model asked for in one breath — it judged
// those calls independent — while a sequence of one-call messages means each
// call waited for the last. Those are different facts about the work.
//
// So the split is the model's own grouping and nothing else. Consecutive
// singles still coalesce (that part was pure accident); a batch boundary earns
// a blank row.

import (
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// blockShape reduces a rendered transcript to the sizes of its tool blocks —
// "4,3,5,2" for four blocks of those row counts. Blank rows are the divider,
// so this is the whole of what these tests are about.
func blockShape(rows []string) string {
	var sizes []string
	n := 0
	for _, r := range rows {
		if strings.Contains(r, "· ") || strings.Contains(r, "× ") {
			n++
			continue
		}
		if strings.TrimSpace(r) == "" && n > 0 {
			sizes = append(sizes, strconv.Itoa(n))
			n = 0
		}
	}
	if n > 0 {
		sizes = append(sizes, strconv.Itoa(n))
	}
	return strings.Join(sizes, ",")
}

// The reported screen, reconstructed: two batches, a stretch the model worked
// through one call at a time, then another batch.
func TestBatchesAndSinglesFormSeparateBlocks(t *testing.T) {
	var msgs []provider.Message
	msgs = append(msgs, batchedCalls(4)...) // the model asked for four at once
	msgs = append(msgs, batchedCalls(3)...) // and then three at once
	for i := 0; i < 5; i++ {                // then worked through five, one by one
		msgs = append(msgs, toolPair("toolu_s"+strconv.Itoa(i), "edit", "a.go", 3)...)
	}
	msgs = append(msgs, batchedCalls(2)...)

	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	if got, want := blockShape(rows), "4,3,5,2"; got != want {
		t.Errorf("block shape = %q; want %q\n%s", got, want, strings.Join(rows, "\n"))
	}
}

// Two batches back to back are two decisions, not one. #515 ran them together;
// this is the part of it being taken back.
func TestAdjacentBatchesAreSeparated(t *testing.T) {
	msgs := append(batchedCalls(3), batchedCalls(2)...)
	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	if got, want := blockShape(rows), "3,2"; got != want {
		t.Errorf("block shape = %q; want %q\n%s", got, want, strings.Join(rows, "\n"))
	}
}

// The half of #515 that stands: six one-call messages are six paragraphs only
// because each was its own message, and nothing about the work changes if the
// model had batched them.
func TestConsecutiveSinglesStillCoalesce(t *testing.T) {
	var msgs []provider.Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, toolPair("toolu_"+strconv.Itoa(i), "read", "a.go", 2)...)
	}
	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	if got, want := blockShape(rows), "6"; got != want {
		t.Errorf("block shape = %q; want %q\n%s", got, want, strings.Join(rows, "\n"))
	}
}

// A batch of ONE is a single. Nothing was decided in parallel, so nothing
// should look like it was — otherwise every lone call becomes its own block
// and #515 is undone entirely.
func TestABatchOfOneIsASingle(t *testing.T) {
	msgs := append(toolPair("toolu_a", "read", "a.go", 2), batchedCalls(1)...)
	msgs = append(msgs, toolPair("toolu_b", "read", "b.go", 2)...)
	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	if got, want := blockShape(rows), "3"; got != want {
		t.Errorf("block shape = %q; want %q — a one-call message is a single\n%s",
			got, want, strings.Join(rows, "\n"))
	}
}

// packToolBlocks directly: the assistant's tool-calls-only message renders
// nothing, and must not split the block it sits in. It is the member most
// likely to be mishandled, because it is invisible in the output — a bug here
// shows up as a stray blank with no row to explain it.
func TestPackToolBlocksKeepsTheInvisibleAssistantHalfInPlace(t *testing.T) {
	// single, then a batch of 2, then single.
	msgs := toolPair("toolu_a", "read", "a.go", 2)
	msgs = append(msgs, batchedCalls(2)...)
	msgs = append(msgs, toolPair("toolu_b", "read", "b.go", 2)...)

	blocks := packToolBlocks(msgs, 0)
	// [assistantA toolA assistantBatch] [toolBatch] [assistantB toolB]
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3: %v", len(blocks), blocks)
	}
	if len(blocks[1]) != 1 {
		t.Errorf("the batch block holds %d messages, want 1: %v", len(blocks[1]), blocks[1])
	}
	// Indices must stay contiguous and ascending across the whole run, or the
	// anchors they drive point at the wrong rows.
	next := 0
	for _, b := range blocks {
		for _, j := range b {
			if j != next {
				t.Fatalf("blocks are not a contiguous partition: saw %d, want %d (%v)", j, next, blocks)
			}
			next++
		}
	}
	if next != len(msgs) {
		t.Errorf("partition covers %d of %d messages", next, len(msgs))
	}
}

// base offsets the indices into the full transcript. Off-by-one here anchors
// /jump to the wrong message, silently.
func TestPackToolBlocksAddressesTheFullTranscript(t *testing.T) {
	run := batchedCalls(2)
	blocks := packToolBlocks(run, 7)
	var seen []int
	for _, b := range blocks {
		seen = append(seen, b...)
	}
	if len(seen) != 2 || seen[0] != 7 || seen[1] != 8 {
		t.Errorf("indices = %v; want [7 8]", seen)
	}
}

// Prose still bounds the whole run — the separator's original job, unchanged by
// any of the splitting inside it.
func TestProseStillBoundsARunWithBlocks(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "start."}}},
	}
	msgs = append(msgs, batchedCalls(2)...)
	msgs = append(msgs, toolPair("toolu_a", "read", "a.go", 2)...)
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "end."}},
	})

	v := View{Theme: Dark, ToolDisplay: ToolDisplayMinimal, Messages: msgs}
	rows := toolRows(t, v)
	joined := strings.Join(rows, "\n")
	start, end := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "start.") {
			start = i
		}
		if strings.Contains(r, "end.") {
			end = i
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("prose missing:\n%s", joined)
	}
	if strings.TrimSpace(rows[start+1]) != "" {
		t.Errorf("no blank between prose and the run:\n%s", joined)
	}
	if strings.TrimSpace(rows[end-1]) != "" {
		t.Errorf("no blank between the run and the prose after it:\n%s", joined)
	}
}
