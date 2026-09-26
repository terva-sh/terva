package transcriptcodec

import (
	"crypto/sha256"
	"encoding/hex"

	"terva.sh/terva/packages/provider"
)

// InterruptStub is the synthetic tool_result injected for a tool_use that was
// restored without a matching result (an interrupted or lost call). Text is the
// model-visible explanation and IsError marks it a failure. A planned restart
// reconciles its interrupted call as expected (IsError:false) rather than a
// generic abort, so the agent does not read its own successful restart as a
// failed tool call.
type InterruptStub struct {
	Text    string
	IsError bool
}

// DefaultInterruptStub reconciles an unmatched tool_use as a generic abort — the
// long-standing behavior for a crash/stop or a lost result.
var DefaultInterruptStub = InterruptStub{Text: "tool call was aborted; no result recorded.", IsError: true}

// RepairToolUseResultPairs walks a restored transcript and
// synthesises stub tool_result blocks for any assistant
// tool_use blocks that aren't paired with a matching result in
// the next message. Anthropic (and OpenAI via the responses API)
// reject any request whose transcript leaves a tool_use without
// its matching tool_result immediately after, with errors like:
//
//	messages.8: `tool_use` ids were found without `tool_result`
//	blocks immediately after
//
// Corruption gets into the transcript two ways we know of:
//
//   - Older terva builds that persisted the assistant tool_use row
//     before the tool_result row, then crashed between the two.
//   - Abort paths in older builds that didn't drop the mid-turn
//     assistant message cleanly.
//
// Rather than change runtime semantics (which would risk hiding a
// real bug), we scrub on load: any unmatched tool_use gets a stub
// tool_result injected as a RoleTool message so the next
// outbound request passes the provider's validity check. The stub
// reads "tool call was aborted; no result recorded." so the
// model can see what happened and decide whether to retry.
//
// Runs once per OpenSession call. No cost on the hot path.
func RepairToolUseResultPairs(msgs []provider.Message) []provider.Message {
	return RepairToolUseResultPairsWith(msgs, DefaultInterruptStub)
}

// RepairToolUseResultPairsWith is RepairToolUseResultPairs with a caller-chosen
// stub for the synthesized results — so a planned restart reconciles its
// interrupted call as expected text (non-error) rather than a generic abort.
func RepairToolUseResultPairsWith(msgs []provider.Message, stub InterruptStub) []provider.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]provider.Message, 0, len(msgs)+2)
	for i, m := range msgs {
		out = append(out, m)
		if m.Role != provider.RoleAssistant {
			continue
		}
		// Collect tool_use ids in this assistant message.
		var ids []string
		for _, c := range m.Content {
			if tc, ok := c.(provider.ToolCallBlock); ok {
				ids = append(ids, tc.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		// Look at the next message (if any) and collect tool_result
		// CallIDs it covers.
		have := map[string]bool{}
		if i+1 < len(msgs) && msgs[i+1].Role == provider.RoleTool {
			for _, c := range msgs[i+1].Content {
				if tr, ok := c.(provider.ToolResultBlock); ok {
					have[tr.CallID] = true
				}
			}
		}
		// Build stubs for any missing id.
		var stubs []provider.Content
		for _, id := range ids {
			if have[id] {
				continue
			}
			stubs = append(stubs, provider.ToolResultBlock{
				CallID:  id,
				Content: []provider.Content{provider.TextBlock{Text: stub.Text}},
				IsError: stub.IsError,
			})
		}
		if len(stubs) == 0 {
			continue
		}
		// Merge into the next tool-role message if present,
		// otherwise insert a synthetic one right after the
		// assistant message. Merging keeps the tool-role row
		// count stable; inserting handles the common case where
		// no tool message was persisted at all.
		if i+1 < len(msgs) && msgs[i+1].Role == provider.RoleTool {
			msgs[i+1].Content = append(msgs[i+1].Content, stubs...)
			// We already appended m to out; the modified next
			// message will be appended on the following iteration.
			continue
		}
		out = append(out, provider.Message{
			Role:    provider.RoleTool,
			Content: stubs,
			Time:    m.Time,
		})
	}
	return out
}

// ApplyImageExclusions replaces every ImageBlock whose content sha256 is in the
// excluded set with the standard rejected-image note — directly in a message
// and nested in a tool result. Content-addressed, so one exclude_image
// directive covers every copy of the image (tool result + codex mirror) and
// survives reordering. Mutates and returns msgs.
func ApplyImageExclusions(msgs []provider.Message, excluded map[string]bool) []provider.Message {
	isExcluded := func(b provider.ImageBlock) bool { return excluded[ImageSHA256(b.Data)] }
	for mi := range msgs {
		content := msgs[mi].Content
		for ci := range content {
			switch v := content[ci].(type) {
			case provider.ImageBlock:
				if isExcluded(v) {
					content[ci] = provider.TextBlock{Text: ImageRejectedNote}
				}
			case provider.ToolResultBlock:
				changed := false
				for ii := range v.Content {
					if ib, ok := v.Content[ii].(provider.ImageBlock); ok && isExcluded(ib) {
						v.Content[ii] = provider.TextBlock{Text: ImageRejectedNote}
						changed = true
					}
				}
				if changed {
					content[ci] = v
				}
			}
		}
	}
	return msgs
}

// ImageSHA256 is the content address of an image's raw bytes — the key an
// exclude_image directive matches on, so one directive drops every copy of the
// image (tool result + codex mirror) regardless of position.
func ImageSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ImageRejectedNote replaces an image the provider refused to accept. It tells
// the model (and, on resume, the reader) why the picture is gone, in place of
// the bytes that broke the turn.
const ImageRejectedNote = "[image omitted: the model's provider rejected it as an invalid or unreadable image]"
