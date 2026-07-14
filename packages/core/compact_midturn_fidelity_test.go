package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// What a mid-turn compaction actually risks, and why these tests exist.
//
// A tool step is exactly TWO messages: one assistant message carrying the whole
// batch of tool_use blocks, and one tool message carrying the whole batch of
// tool_result blocks. AutoCompactKeepTail is 4. So a compaction keeps the last
// TWO STEPS verbatim — a fixed number, while an agentic loop grows unboundedly.
//
// In a 50-step loop that means 97 of 101 messages are summarized away, and every
// state-changing action but the last two steps' reaches the resuming agent ONLY
// through the summary. The "## Actions Already Executed" ledger is not a
// nice-to-have; it is the sole defense against re-running a side effect.
//
// Which makes what the summarizer can SEE load-bearing.

// midTurnLoop builds a realistic agentic tool loop: N steps of
// assistant(tool_use) -> tool(tool_result), with periodic state-changing
// commands and one early failure.
func midTurnLoop(steps int) []provider.Message {
	msgs := []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "refactor the auth package"}},
	}}
	for i := 0; i < steps; i++ {
		id := fmt.Sprintf("call_%d", i)
		name, args := "read", fmt.Sprintf(`{"path":"pkg/file_%d.go"}`, i)
		if i%5 == 0 {
			name, args = "bash", fmt.Sprintf(`{"command":"npm install && rm -rf build/%d"}`, i)
		}
		msgs = append(msgs, provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.ToolCallBlock{ID: id, Name: name, Arguments: json.RawMessage(args)},
			},
		})
		isErr := i == 10
		text := fmt.Sprintf("ok (step %d)", i)
		if isErr {
			text = "ENOENT: build/10 not found"
		}
		msgs = append(msgs, provider.Message{
			Role: provider.RoleTool,
			Content: []provider.Content{provider.ToolResultBlock{
				CallID: id, IsError: isErr,
				Content: []provider.Content{provider.TextBlock{Text: text}},
			}},
		})
	}
	return msgs
}

// The keep-tail arithmetic, pinned. If AutoCompactKeepTail ever changes, or the
// batching of tool results changes, the blast radius of a mid-turn compaction
// changes with it — and this is the number that says how big it is.
func TestMidTurnCompactionKeepsExactlyTwoToolSteps(t *testing.T) {
	msgs := midTurnLoop(50)
	if len(msgs) != 101 {
		t.Fatalf("fixture built %d messages; want 101 (1 user + 50 steps x 2)", len(msgs))
	}

	tail := repairOrphanedToolResults(msgs[len(msgs)-AutoCompactKeepTail:])
	summarized := msgs[:len(msgs)-AutoCompactKeepTail]

	if len(tail) != 4 {
		t.Errorf("kept %d messages verbatim; want 4", len(tail))
	}
	// Two steps survive. Everything else is prose.
	if got, want := len(summarized), 97; got != want {
		t.Errorf("summarized %d messages; want %d", got, want)
	}

	// And this is the part that matters: none of the state-changing commands
	// survive verbatim. All ten of them exist, for the resuming agent, only as
	// whatever the summary chose to say about them.
	stateChanging := 0
	for _, m := range tail {
		for _, c := range m.Content {
			if tc, ok := c.(provider.ToolCallBlock); ok && tc.Name == "bash" {
				stateChanging++
			}
		}
	}
	if stateChanging != 0 {
		t.Errorf("%d state-changing calls survived in the tail; the fixture expects them all to be summarized "+
			"— if this changed, the risk model in the docs changed with it", stateChanging)
	}
}

// The cold summarizer must be able to tell a FAILED tool call from a successful
// one. It could not: IsError was dropped, so a command that aborted serialized
// byte-identically to one that completed, and a terse failure carries no word
// the summarizer could key on.
//
// This is how a resumed agent concludes a side effect already happened when it
// did not — or re-runs one that did. The warm path never had the bug (it sends
// the native blocks and the provider serializes is_error), which makes the
// BESPOKE summarizer the lossier of the two for exactly the case its dedicated
// prompt was supposed to protect.
func TestSerializedTranscriptMarksFailedToolResults(t *testing.T) {
	msgs := midTurnLoop(15)
	ser := serializeTranscript(msgs)

	if !strings.Contains(ser, "[tool_result ERROR] ENOENT: build/10 not found") {
		t.Errorf("the failed step is not marked as an error; the summarizer cannot distinguish it "+
			"from a success:\n%s", ser)
	}
	// The successes stay unmarked, or the marker means nothing.
	if !strings.Contains(ser, "[tool_result] ok (step 11)") {
		t.Error("a successful tool result should carry the plain marker")
	}
	if strings.Count(ser, "[tool_result ERROR]") != 1 {
		t.Errorf("exactly one step failed; got %d error markers", strings.Count(ser, "[tool_result ERROR]"))
	}
}

// An image result cannot be summarized, but its EXISTENCE is evidence the call
// ran. Dropping the block silently made a screenshot-producing step look like it
// never happened at all.
func TestSerializedTranscriptKeepsImageToolResultsVisible(t *testing.T) {
	msgs := []provider.Message{{
		Role: provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{
			CallID: "c1",
			Content: []provider.Content{
				provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3, 4}},
			},
		}},
	}}
	ser := serializeTranscript(msgs)
	if !strings.Contains(ser, "[image: image/png, 4 bytes]") {
		t.Errorf("an image-only tool result vanished from the summarizer's view entirely:\n%q", ser)
	}
}
