//go:build terva_acp

package acp

import (
	"encoding/json"
	"os"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// translateEvent maps one core.AgentEvent onto zero or more session/update
// notifications (the §6 table). It runs on the prompt goroutine's sink; the
// single-writer mutex in conn.write keeps emits ordered.
//
// EvDone is NOT handled here — the prompt handler treats it (informed by the
// last EvTurnEnd) as the turn terminator and resolves session/prompt with a
// stopReason (§11). EvToolUse{Start,Args,End} are intentionally ignored this
// pass (composing-call deltas are polish, not MVP).
func (s *session) translateEvent(ev core.AgentEvent) {
	switch e := ev.(type) {
	case core.EvTextDelta:
		s.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock(e.Delta),
		})

	case core.EvToolCall:
		s.emitToolCall(e)

	case core.EvToolProgress:
		s.emit(map[string]any{
			"sessionUpdate": UpdateToolCallUpdate,
			"toolCallId":    e.ID,
			"status":        ToolStatusInProgress,
			"content": []ToolCallContent{{
				Type:    ToolCallContentContent,
				Content: textContentBlock(e.Text),
			}},
		})

	case core.EvToolResult:
		s.emitToolResult(e)

	case core.EvUserMessageRejected:
		// A BeforeUserMessage guard (extension intercept) refused the prompt:
		// it never reached the model, so without a chunk the editor shows an
		// empty turn with no explanation. titleLine caps the quoted stub the
		// same way tool headers are capped.
		s.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock("(message blocked: " + e.Reason + " — “" + titleLine(e.Text) + "”)"),
		})

	case core.EvCompactStart:
		s.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock("(compacting context: " + e.Reason + ")"),
		})

	case core.EvCompactEnd:
		if e.Err != "" {
			s.emit(map[string]any{
				"sessionUpdate": UpdateAgentMessageChunk,
				"content":       textContentBlock("(compaction failed: " + e.Err + ")"),
			})
		}
	}
}

// ToolCallContent type discriminators (kept here next to their use).
const (
	ToolCallContentContent  = "content"
	ToolCallContentDiff     = "diff"
	ToolCallContentTerminal = "terminal"
)

// emitToolCall sends the initial `tool_call` update (status pending). It
// derives the ACP kind, resolves a path argument into a clickable location,
// and — for edit/write tools — snapshots the file so the eventual diff can
// be a whole-file before/after (§7).
func (s *session) emitToolCall(e core.EvToolCall) {
	kind := toolKind(e.Name)
	path := pathArg(e.Args)

	if path != "" && (kind == ToolKindEdit) {
		s.snapshotEdit(e.ID, path)
	}

	update := map[string]any{
		"sessionUpdate": UpdateToolCall,
		"toolCallId":    e.ID,
		"title":         toolTitle(e.Name, e.Args),
		"kind":          kind,
		"status":        ToolStatusPending,
	}
	if len(e.Args) > 0 {
		update["rawInput"] = json.RawMessage(e.Args)
	}
	if path != "" {
		update["locations"] = []ToolCallLocation{{Path: s.resolvePath(path)}}
	}
	s.emit(update)
}

// emitToolResult sends the terminal `tool_call_update` for a tool call:
// status completed/failed, the textual content, and — for an edit — a diff
// content block reconstructed from the pre/post file snapshot.
func (s *session) emitToolResult(e core.EvToolResult) {
	status := ToolStatusCompleted
	if e.Result.IsError {
		status = ToolStatusFailed
	}

	var content []ToolCallContent
	if txt := toolResultText(e.Result); txt != "" {
		content = append(content, ToolCallContent{
			Type:    ToolCallContentContent,
			Content: textContentBlock(txt),
		})
	}

	// Edit diff via pre/post snapshot: re-read the file now and emit a
	// whole-file {oldText,newText} diff block. Robust to terva's multi-edit
	// tool and its custom Details["diff"] format (§7).
	if snap, ok := s.takeEditSnapshot(e.ID); ok && !e.Result.IsError {
		newText := ""
		if data, err := os.ReadFile(snap.path); err == nil {
			newText = string(data)
		}
		diff := ToolCallContent{
			Type:    ToolCallContentDiff,
			Path:    snap.path,
			NewText: newText,
		}
		if snap.existed {
			diff.OldText = snap.oldText
		}
		content = append(content, diff)
	}

	update := map[string]any{
		"sessionUpdate": UpdateToolCallUpdate,
		"toolCallId":    e.ID,
		"status":        status,
	}
	if len(content) > 0 {
		update["content"] = content
	}
	s.emit(update)
}

// toolKind derives the ACP ToolKind from a terva tool name (§7). MCP and
// any unrecognised tool fall back to `other`.
func toolKind(name string) string {
	switch name {
	case "read":
		return ToolKindRead
	case "write", "edit":
		return ToolKindEdit
	case "bash":
		return ToolKindExecute
	default:
		return ToolKindOther
	}
}

// pathArg extracts a `path` string field from a tool's raw JSON args, if
// present. terva's read/write/edit tools all use {"path": ...}.
func pathArg(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v.Path
}

// toolTitle builds a human-readable header for a tool_call. The bare tool
// name ("bash") is uninformative in the editor — Zed renders `title` and
// tucks `rawInput` away — so surface the salient argument: the command for
// bash, the path for read/write/edit. The full structured args still ride in
// rawInput. Unknown / MCP tools keep their name.
func toolTitle(name string, args json.RawMessage) string {
	switch name {
	case "bash":
		if cmd := stringArg(args, "command"); cmd != "" {
			return titleLine(cmd)
		}
	case "read", "write", "edit":
		if p := pathArg(args); p != "" {
			return name + " " + p
		}
	}
	return name
}

// stringArg extracts a top-level string field from raw JSON args, or "".
func stringArg(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// titleLine collapses a possibly multi-line value to a single line and caps
// its length so a long command doesn't blow up the editor's tool header.
func titleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}

// toolResultText flattens a ToolResult's content blocks to their joined
// text. Image blocks are skipped — the editor gets the textual outcome.
func toolResultText(r core.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// messageText flattens a transcript message's text/reasoning blocks to their
// joined text, for replaying a loaded session as agent/user message chunks
// (§10). Only conversational text is repainted: tool-call/result and image
// blocks carry no chunk text and are skipped.
//
// A reasoning summary is a FALLBACK, not an addition: it is repainted only
// when the turn produced no visible text, which is the case this exists for —
// providers whose reasoning_content IS the whole assistant output would
// otherwise replay as a blank turn. Where a turn has both (the codex path with
// reasoning summaries enabled), repainting both would splice the model's
// internal reasoning into its visible answer.
func messageText(m provider.Message) string {
	var text, summary strings.Builder
	add := func(sb *strings.Builder, s string) {
		if s == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(s)
	}
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.TextBlock:
			add(&text, b.Text)
		case provider.ReasoningBlock:
			add(&summary, b.Summary)
		}
	}
	if text.Len() > 0 {
		return text.String()
	}
	return summary.String()
}

// stopReasonFor maps the terminal turn state onto an ACP StopReason (§11).
// `cancelled` wins when a cancel was requested; otherwise the last
// provider.StopReason drives the mapping. tool_use is never terminal (the
// loop continues), so it falls through to end_turn here — the loop only
// reaches EvDone after a non-tool stop.
func stopReasonFor(cancelled bool, last provider.StopReason) string {
	if cancelled {
		return StopCancelled
	}
	switch last {
	case provider.StopLength:
		return StopMaxTokens
	case provider.StopAborted:
		return StopCancelled
	case provider.StopEnd, provider.StopError, provider.StopToolUse:
		return StopEndTurn
	default:
		return StopEndTurn
	}
}
