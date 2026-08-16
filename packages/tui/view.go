package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// expandTabs replaces tab characters with 4 spaces so code from
// tab-indented languages (Go, Makefiles, etc.) renders at a
// consistent width instead of the terminal's default 8-column tabs.
func expandTabs(s string) string {
	return strings.ReplaceAll(s, "\t", "    ")
}

// sanitizeUserBubbleLine prepares a single user-bubble row for safe
// rendering. Pasted content from another terminal can contain
// embedded ANSI escape sequences, control bytes, and tabs that
// either reset the bubble's background colour or move the cursor in
// ways that break the bubble's painted column.
func sanitizeUserBubbleLine(s string) string {
	if s == "" {
		return s
	}
	s = expandTabs(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC: drop CSI/OSC/DCS and simple escapes.
			i = skipEscapeSequence(s, i)
			continue
		}
		if c == '\r' || c == '\b' || c == 0x07 {
			i++
			continue
		}
		if c < 0x20 || c == 0x7f {
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// pathFromParsed returns the "path"/"file_path" argument from a tool
// call's already-unmarshalled JSON arguments, or "" if they aren't a
// JSON object or don't include one. Used to pick a syntax language for
// rendering the corresponding tool_result. Operating on a pre-decoded
// value lets refreshToolPaths derive path/offset/label from a single
// json.Unmarshal (see parseToolArgInfo) rather than one per field.
func pathFromParsed(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, k := range []string{"path", "file_path"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// offsetFromParsed returns the read tool's 1-indexed `offset` arg (the
// first line of the slice the tool was asked to return) from already-
// unmarshalled args, or 0 when the call didn't specify one. Used by the
// tui to draw the line-number gutter aligned to the right starting row,
// even though the tool's text content itself no longer carries line
// numbers.
func offsetFromParsed(v any) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch n := m["offset"].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// osUserHomeDir is aliased so the test file can swap it.
var osUserHomeDir = os.UserHomeDir

// View turns a transcript + live state into a slice of styled lines,
// already wrapped to width.
type View struct {
	Theme      Theme
	ImageProto ImageProtocol // how to render inline images in this terminal
	Messages   []provider.Message
	// SharedPreviews holds the image bytes this client has fetched for a
	// shared file (share_file), keyed by the share id. A card draws a preview
	// only for an id present here, so an image whose fetch has not landed —
	// or that this carrier cannot serve — renders as a complete card without
	// one. Never populated by the renderer: a share is a handle the DAEMON
	// resolves, and the daemon may be another machine (terva attach), so the
	// bytes arrive through the carrier or not at all.
	SharedPreviews map[string][]byte
	// Now is the clock the shared-file expiry column reads. Nil means
	// time.Now; a test pins it so "6d left" is assertable rather than a race
	// against the store's TTL.
	Now func() time.Time
	// toolPaths maps tool_use_id to the "path" argument of the call, if
	// any, so tool_result rendering can pick the right syntax language.
	// Rebuilt on each Build().
	toolPaths map[string]string
	// toolStartLines maps tool_use_id to the 1-indexed first line
	// number of a `read` result, pulled from the call's offset arg.
	// Used by renderNumberedFile to draw a line-number gutter over
	// raw (unnumbered) file content the model receives. Rebuilt on
	// each Build().
	toolStartLines map[string]int
	// toolCallLabels maps tool_use_id to the box label (tool name +
	// short args) for the call, so a tool_result message can render
	// the box top edge without having to look back at the assistant
	// message that originated the call. Rebuilt on each Build().
	toolCallLabels map[string]string
	// toolCallNames maps tool_use_id to the bare tool name (no args), so
	// grouped display can summarise a run of results ("bash ×3, read")
	// from the result blocks alone (which carry the call id, not the
	// name). Rebuilt on each Build() alongside toolCallLabels.
	toolCallNames map[string]string
	// toolArgCache memoises the parsed path/offset/label for each tool
	// call by tool_use_id, so refreshToolPaths re-parses a call's JSON
	// arguments only when it first appears or while it's still streaming
	// (its args are still growing). See refreshToolPaths.
	toolArgCache    map[string]toolArgInfo
	Streaming       string // current assistant text delta
	StreamingActive bool
	ToolCalls       []ToolCallView // tool calls in flight or completed
	StatusLine      string
	Err             string

	// liveBodyHigh tracks the tallest live-preview body height seen per
	// tool-call id, so a streaming edit/write/bash box never shrinks
	// mid-stream (e.g. between edit 1 and the start of edit 2), which
	// would yank the editor/status band upward and back. Keyed by
	// ToolCallView.ID; entries are only consulted while a call has no
	// Result. BuildWithAnchors prunes the map as calls finish.
	liveBodyHigh map[string]int

	// ExpandAll forces every long tool result to render in full.
	// Toggled from the tui by ctrl+o. When false, results longer than
	// ToolCollapseLines collapse to ToolCollapsePreview lines plus a
	// "... (N more lines, M total, ctrl+o to expand)" footer.
	ExpandAll bool

	// ToolDisplay selects how tool calls render in the transcript and
	// live overlay: full bordered boxes, a one-line muted summary per
	// call, or nothing at all. Cycled from the tui by ctrl+t; immersive
	// experiences (--chat/--play) default to the minimal form so tool
	// mechanics stay out of the conversation. ExpandAll (ctrl+o)
	// overrides any reduced mode with full expanded boxes, which is
	// also the escape hatch that makes minimal/hidden output
	// recoverable without changing modes.
	ToolDisplay ToolDisplayMode

	// renderCache holds the per-message rendered line slices so Build
	// doesn't re-markdown every message on every frame. Keyed by a
	// struct of (content hash, width, expandAll) — any of those
	// changing invalidates the entry. Messages are append-only after
	// they finalise so keeping the cache across turns is safe.
	//
	// Streaming/in-flight work (v.Streaming, v.ToolCalls) is never
	// cached because it changes every delta.
	renderCache map[msgCacheKey][]string

	// TailLimit caps how many messages from the END of Messages are
	// rendered. Messages older than the limit emit a zero-row slice
	// so first paint after a session resume doesn't pay the markdown
	// / chroma cost for the entire transcript at once. 0 means no
	// limit (render everything, the historical behaviour). Interactive
	// raises the cap when the user scrolls past the top of the
	// already-rendered tail.
	TailLimit int
}

// msgCacheKey identifies a cached message render. hash is a 64-bit
// FNV-1a of the message's content, which is cheap to compute and
// unambiguous enough for the cache (collisions produce a stale frame,
// not wrong data, and we recompute on invalidation anyway).
type msgCacheKey struct {
	hash        uint64
	width       int
	expandAll   bool
	toolDisplay ToolDisplayMode
	// turnOpen is true when the previous rendered message belongs to
	// the same agent turn (assistant tool_use, or tool result). The
	// header ("▍ terva") is suppressed in that case so a single turn
	// — even one that spans many assistant/tool message round-trips
	// in the underlying API — renders under one header instead of a
	// new one per assistant message.
	turnOpen bool
}

// InvalidateRenderCache drops all cached message renders. The tui
// calls this when the transcript is replaced wholesale (/compact,
// /clear, session swap) since messages can be replaced in place and
// a content-hash miss alone doesn't reclaim the old entries.
func (v *View) InvalidateRenderCache() {
	v.renderCache = nil
}

// ToolCollapsePreview is the number of lines shown before a long tool
// result is replaced with a "... ctrl+o to expand" footer. Tool
// results shorter than ToolCollapseLines always render in full.
const (
	ToolCollapsePreview = 10
	ToolCollapseLines   = 12
)

// ToolDisplayMode selects the transcript treatment of tool calls.
type ToolDisplayMode int

const (
	// ToolDisplayFull renders each call as a bordered box with its
	// (collapsible) result body — the classic agent-mode view.
	ToolDisplayFull ToolDisplayMode = iota
	// ToolDisplayMinimal renders each call as one muted, borderless
	// summary line ("· bash go test ./... — 42 lines"); ctrl+o
	// expands everything back to full boxes.
	ToolDisplayMinimal
	// ToolDisplayHidden renders nothing for tool calls until the user
	// expands with ctrl+o.
	ToolDisplayHidden
	// ToolDisplayGrouped collapses each consecutive run of tool calls
	// into ONE muted disclosure line ("▸ 5 tool calls  bash ×3, read,
	// edit · 1 failed"); ctrl+o (ExpandAll) expands the whole transcript
	// back to full boxes. This mirrors the web UI's grouped transcript,
	// and shares its name-summary formatter (summarizeToolNames). New
	// values are appended so the existing constants keep their integer
	// tags. The run detection lives in BuildWithAnchors, not renderMessage,
	// because it is a cross-message transform.
	ToolDisplayGrouped
)

// String returns the label shown in status messages and settings.
func (m ToolDisplayMode) String() string {
	switch m {
	case ToolDisplayMinimal:
		return "minimal"
	case ToolDisplayHidden:
		return "hidden"
	case ToolDisplayGrouped:
		return "grouped"
	default:
		return "boxes"
	}
}

// effectiveToolDisplay resolves the display mode for the current
// frame: ctrl+o's expand-all wins over any reduced mode, so minimal
// and hidden tool output is always one keystroke from recoverable.
func (v *View) effectiveToolDisplay() ToolDisplayMode {
	if v.ExpandAll {
		return ToolDisplayFull
	}
	return v.ToolDisplay
}

// minimalToolLine renders the ToolDisplayMinimal form of a tool call:
// one muted line, aligned with the tool boxes' outer margin, carrying
// the call label and an optional summary suffix. Errors keep the "×"
// marker in the error colour so failures stay noticeable even with
// the mechanics tucked away.
//
//	· bash go test ./... — 42 lines
//	× edit packages/tui/view.go — error
func minimalToolLine(th Theme, label string, suffix string, isErr bool, width int) string {
	mark := th.FG256(th.Muted, "·")
	if isErr {
		mark = th.FG256(th.Error, "×")
		if suffix == "" {
			suffix = "error"
		}
	}
	text := oneLineToolLabel(label)
	if suffix != "" {
		text += " — " + suffix
	}
	line := strings.Repeat(" ", toolBoxOuterMargin) + mark + " " + th.FG256(th.Muted, text)
	return truncateToWidth(line, width)
}

// classifyToolRun decides how a message participates in a grouped-mode
// tool run. member is true when the message is part of a collapsible run:
// either a tool-result message (which contributes its results) or an
// assistant message that issued only tool calls and therefore renders
// nothing of its own (a "transparent" member — its box is owned by the
// results, and it must not break the run). Any other message — a user
// prompt, an assistant reply with prose, a compaction summary — is a
// run boundary. res carries the message's tool results (nil for the
// transparent-assistant case) so the caller can tally names and failures
// without re-walking the content.
func classifyToolRun(m provider.Message) (member bool, res []provider.ToolResultBlock) {
	// Both dividers are run boundaries: a tool run must not be grouped across the
	// line where the conversation was condensed or cut.
	if m.Meta["compaction"] == "true" || m.Meta["clear"] != "" {
		return false, nil
	}
	switch m.Role {
	case provider.RoleTool:
		for _, c := range m.Content {
			if tr, ok := c.(provider.ToolResultBlock); ok {
				res = append(res, tr)
			}
		}
		return len(res) > 0, res
	case provider.RoleAssistant:
		hasToolCall, hasText := false, false
		for _, c := range m.Content {
			switch b := c.(type) {
			case provider.ToolCallBlock:
				hasToolCall = true
			case provider.TextBlock:
				if strings.TrimSpace(b.Text) != "" {
					hasText = true
				}
			}
		}
		// Prose makes it a real reply that breaks the run; tool-calls-only
		// renders nothing and stays inside the run.
		return hasToolCall && !hasText, nil
	}
	return false, nil
}

// packToolBlocks splits one tool run into the blocks a reduced display draws
// with a blank row between them. run is the run's messages; base is the index
// of run[0] in the full transcript, so the returned indices address that.
//
// The split is the model's OWN grouping, and it is the one thing about a run's
// shape worth showing. A message carrying several results is a batch the model
// asked for in one breath — it judged those calls independent, which is a fact
// about the work. A sequence of one-call messages is the opposite: each call
// waited for the last, because each informed the next. Those two read
// differently and should look different.
//
// What must NOT survive is the accident: six independent one-call messages are
// six paragraphs only because each was its own message, and nothing about the
// work changes if the model had batched them. So consecutive singles coalesce
// into one block, and only a real batch boundary earns a blank row.
//
// The arity is trustworthy because executeTools returns exactly one tool
// message per assistant message, carrying every result. The count on the tool
// message IS the count of tool_use blocks the model emitted — not an artifact
// of how results were packed on the way back.
func packToolBlocks(run []provider.Message, base int) [][]int {
	var blocks [][]int
	var singles []int
	flush := func() {
		if len(singles) > 0 {
			blocks = append(blocks, singles)
			singles = nil
		}
	}
	for off, m := range run {
		idx := base + off
		_, res := classifyToolRun(m)
		if len(res) > 1 {
			// A batch stands alone. Whatever singles preceded it close first,
			// so the blank lands between them and not inside either.
			flush()
			blocks = append(blocks, []int{idx})
			continue
		}
		// One result, or none at all — the assistant's tool-calls-only half,
		// which renders nothing and must not split the block it sits in.
		singles = append(singles, idx)
	}
	flush()
	return blocks
}

// groupedToolLine renders the ToolDisplayGrouped summary for one run of
// tool calls: a muted disclosure line carrying the call count, the shared
// name summary (summarizeToolNames — identical to the web UI, pinned by
// the golden fixtures), and, when any failed, a failure count in the
// error colour. ctrl+o (ExpandAll) expands the run back to full boxes.
//
//	▸ 5 tool calls  bash ×3, read, edit · 1 failed
func (v *View) groupedToolLine(names []string, failed int, width int) string {
	th := v.Theme
	text := i18n.TN(len(names), "%d tool call", "%d tool calls")
	if summary := summarizeToolNames(names); summary != "" {
		text += "  " + summary
	}
	line := strings.Repeat(" ", toolBoxOuterMargin) + th.FG256(th.Muted, "▸ "+text)
	if failed > 0 {
		line += " " + th.FG256(th.Error, "· "+i18n.TN(failed, "%d failed", "%d failed"))
	}
	return truncateToWidth(line, width)
}

// toolResultSummary sizes a tool result's content for the minimal
// display line: total text lines plus a note for image blocks.
func toolResultSummary(blocks []provider.Content) string {
	lines, images := 0, 0
	for _, b := range blocks {
		switch bb := b.(type) {
		case provider.TextBlock:
			if bb.Text != "" {
				lines += strings.Count(bb.Text, "\n") + 1
			}
		case provider.ImageBlock:
			images++
		}
	}
	return countLinesSummary(lines, images)
}

// countLinesSummary renders "N lines", "image", or a combination for
// the minimal line's suffix; empty when there is nothing to size.
func countLinesSummary(lines, images int) string {
	var parts []string
	switch {
	case lines == 1:
		parts = append(parts, "1 line")
	case lines > 1:
		parts = append(parts, fmt.Sprintf("%d lines", lines))
	}
	switch {
	case images == 1:
		parts = append(parts, "image")
	case images > 1:
		parts = append(parts, fmt.Sprintf("%d images", images))
	}
	return strings.Join(parts, " + ")
}

// ToolCallView is a pending tool invocation plus optional result.
type ToolCallView struct {
	ID     string
	Name   string
	Args   string // rendered argument summary
	Result string // rendered result preview (truncated)
	Error  bool
	Done   bool

	// Progress is the latest transient status a long-running tool reported via
	// its progress callback (EvToolProgress). Shown as a muted body while the
	// call is in flight (Result == ""); superseded by the result.
	Progress string

	// Streaming is true while the model is still typing the tool
	// call's JSON arguments. The TUI renders a live preview of any
	// interesting string fields (for `write`, the `content`; for
	// `bash`, the `command`) so the user can watch the file being
	// composed. Set to false as soon as EvToolUseEnd arrives.
	Streaming bool

	// RawJSONBuf is the accumulator of every EvToolUseArgs delta
	// the stream has delivered for this tool call. Used by the
	// partial-JSON extractor to peel off the live string value
	// of one named field on each render.
	RawJSONBuf string

	// LivePath is the `path` arg extracted as soon as it parses
	// out of RawJSONBuf. Shown next to the tool name in the header
	// so the user can see which file is being written to.
	LivePath string
}

// MessageAnchor records where a rendered message starts in the chat
// line slice. Used by /jump so the dialog can scroll the viewport to
// the row where a turn's user prompt begins.
type MessageAnchor struct {
	MessageIdx int // index into v.Messages
	Row        int // first row of that message in the Build() output
}

// Build returns the chat log lines for the given width.
func (v *View) Build(width int) []string {
	lines, _ := v.BuildWithAnchors(width)
	return lines
}

// BuildLive renders only in-flight assistant/tool/error state. Main-screen
// scrollback renderers can keep these rows outside the immutable transcript
// so native scrolling stays stable while a turn streams.
func (v *View) BuildLive(width int) []string {
	var out []string
	if v.StreamingActive && strings.TrimSpace(v.Streaming) != "" {
		const indent = "  "
		inner := assistantBodyWidth(width - len(indent))
		md := RenderMarkdown(v.Streaming, v.Theme, inner)
		for _, l := range strings.Split(md, "\n") {
			for _, w := range wrapANSILineKeepStyle(l, inner) {
				out = append(out, indent+w)
			}
		}
		// out = append(out, "")
	}
	finalised := map[string]bool{}
	for _, m := range v.Messages {
		for _, c := range m.Content {
			if b, ok := c.(provider.ToolResultBlock); ok {
				finalised[b.CallID] = true
			}
		}
	}
	insertedToolGap := false
	for _, tc := range v.ToolCalls {
		if finalised[tc.ID] {
			continue
		}
		if !insertedToolGap && len(out) > 0 {
			out = append(out, "")
		}
		insertedToolGap = true
		out = append(out, v.renderToolCall(tc, width)...)
		// out = append(out, "")
	}
	if v.Err != "" {
		out = append(out, v.renderErr(width)...)
		// out = append(out, "")
	}
	return out
}

// renderErr formats v.Err into one or more colored rows, word-wrapped
// to width so long provider error messages (e.g. a Bedrock 400 with an
// embedded JSON body) aren't cut off at the terminal edge. The first
// row carries the "✖ " marker; continuation rows align under the text
// with a two-cell indent.
func (v *View) renderErr(width int) []string {
	const marker = "✖ "
	const indent = "  "
	wrapWidth := width - len([]rune(marker))
	if wrapWidth < 8 {
		wrapWidth = 8
	}
	wrapped := wrapLine(v.Err, wrapWidth, "")
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	out := make([]string, 0, len(wrapped))
	for idx, line := range wrapped {
		prefix := marker
		if idx > 0 {
			prefix = indent
		}
		out = append(out, v.Theme.FG256(v.Theme.Error, prefix+line))
	}
	return out
}

// BuildWithAnchors is like Build but additionally reports the first
// row occupied by each message in v.Messages. Callers that need to
// scroll to a specific turn (the /jump dialog) use the anchor slice
// to map a message index back to a row offset.
func (v *View) BuildWithAnchors(width int) ([]string, []MessageAnchor) {
	v.refreshToolPaths()
	if v.renderCache == nil {
		v.renderCache = make(map[msgCacheKey][]string)
	}
	if v.liveBodyHigh == nil {
		v.liveBodyHigh = make(map[string]int)
	}
	// Drop high-water entries for calls that are gone or finalised so a
	// reservation never leaks into a later, shorter tool box.
	if len(v.liveBodyHigh) > 0 {
		active := make(map[string]bool, len(v.ToolCalls))
		for _, tc := range v.ToolCalls {
			if tc.Result == "" {
				active[tc.ID] = true
			}
		}
		for id := range v.liveBodyHigh {
			if !active[id] {
				delete(v.liveBodyHigh, id)
			}
		}
	}

	// Snapshot the transcript once. v.Messages can be replaced on another
	// goroutine mid-build (a turn appending, a session load), and this
	// function reads it repeatedly — sizing `rendered` from its length, then
	// re-ranging it for the anchor pass. When it grew between those reads the
	// anchor loop indexed past `rendered` and panicked ("index out of range").
	// A single local snapshot makes the whole build internally consistent (the
	// frame may be one redraw stale; the next redraw catches up).
	msgs := v.Messages

	// Pre-render every message (hits the cache for unchanged ones) so
	// we can allocate `out` in a single shot with the exact capacity.
	// Growing via append on a long transcript copies the backing array
	// log2(N) times; for a 2000-line scrollback that's enough memcpy
	// to visibly stutter while typing.
	rendered := make([][]string, len(msgs))
	total := 0
	// renderFrom is the first index we actually render. Anything
	// below it emits a zero-row placeholder; the message still gets
	// an anchor entry so /jump and scroll math keep working, and a
	// later Build with a higher TailLimit fills the gap.
	renderFrom := 0
	if v.TailLimit > 0 && len(msgs) > v.TailLimit {
		renderFrom = len(msgs) - v.TailLimit
	}
	for idx, m := range msgs {
		if idx < renderFrom {
			rendered[idx] = nil
			continue
		}
		// A turn is "open" once the assistant has started responding
		// to the most recent user prompt. Walk back over consecutive
		// assistant/tool messages: if any non-user message precedes
		// this one without a user message in between, we're inside
		// the same turn and should not draw a new "▍ terva" header.
		turnOpen := false
		if m.Role == provider.RoleAssistant {
			for j := idx - 1; j >= 0; j-- {
				prev := msgs[j]
				// Skip the dividers — a compaction summary or a /clear
				// boundary renders as a rule across the chat, not as a
				// speaker's message, so neither opens a turn.
				if prev.Meta["compaction"] == "true" || prev.Meta["clear"] != "" {
					continue
				}
				if prev.Role == provider.RoleAssistant || prev.Role == provider.RoleTool {
					turnOpen = true
				}
				break
			}
		}
		lines := v.renderMessageCached(m, width, turnOpen)
		rendered[idx] = lines
		total += len(lines) + 1 // +1 for the blank separator row
	}

	out := make([]string, 0, total+16)
	anchors := make([]MessageAnchor, 0, len(msgs))
	// In grouped mode a consecutive run of tool messages collapses to a
	// single summary line. ExpandAll (ctrl+o) makes effectiveToolDisplay
	// report Full, so grouping is off and the per-message boxes render —
	// the same escape hatch minimal/hidden use.
	display := v.effectiveToolDisplay()
	grouped := display == ToolDisplayGrouped
	// Reduced displays render a call as ONE muted row, which makes a run of
	// them a list — and a list is not separated by blank lines. The separator
	// below is per MESSAGE, so without this the spacing tracks how the model
	// happened to BATCH its calls: six results in one tool message render
	// flush, the same six in six messages render as six paragraphs. Nothing
	// about the transcript's meaning differs between those, so nothing about
	// its shape should. Full boxes are the opposite case and keep the blank —
	// two boxes flush against each other read as one malformed box.
	packed := display == ToolDisplayMinimal || display == ToolDisplayHidden
	appendMsg := func(idx int) {
		anchors = append(anchors, MessageAnchor{MessageIdx: idx, Row: len(out)})
		out = append(out, rendered[idx]...)
		// Skip the inter-message blank for messages that rendered
		// to nothing. An assistant message whose only content is
		// ToolCallBlock(s) has no rendered output of its own (each
		// tool result owns its box now), so emitting a blank for it
		// would compound with the blank from the next real message
		// and produce two blank rows between adjacent tool boxes.
		if len(rendered[idx]) == 0 {
			return
		}
		out = append(out, "")
	}
	for idx := 0; idx < len(msgs); {
		if grouped {
			if member, _ := classifyToolRun(msgs[idx]); member {
				// Consume the maximal run [idx, end) of tool messages and
				// gather the completed results in order.
				end := idx
				var names []string
				failed := 0
				for end < len(msgs) {
					mem, res := classifyToolRun(msgs[end])
					if !mem {
						break
					}
					for _, tr := range res {
						names = append(names, v.toolCallNames[tr.CallID])
						if tr.IsError {
							failed++
						}
					}
					end++
				}
				// Every message in the run anchors to the summary row so
				// /jump still lands on a visible line; the row is where the
				// summary will sit (or where the next real content begins
				// when the run produced no completed results yet — an
				// in-flight tail the live overlay renders instead).
				row := len(out)
				for j := idx; j < end; j++ {
					anchors = append(anchors, MessageAnchor{MessageIdx: j, Row: row})
				}
				if len(names) > 0 {
					out = append(out, v.groupedToolLine(names, failed, width))
					out = append(out, "")
				}
				idx = end
				continue
			}
		}
		if packed {
			if member, _ := classifyToolRun(msgs[idx]); member {
				// Same run boundaries grouped mode uses — classifyToolRun
				// already knows that prose breaks a run, that a tool-calls-only
				// assistant message renders nothing and stays inside it, and
				// that a compaction or /clear divider ends it. The only
				// difference here is what gets emitted: every member's own
				// rows, rather than one summary line standing in for them.
				end := idx
				for end < len(msgs) {
					if mem, _ := classifyToolRun(msgs[end]); !mem {
						break
					}
					end++
				}
				// Inside the run, split on the model's own grouping: a batch it
				// asked for in one message is one block, consecutive one-call
				// messages coalesce into another, and a blank row separates them.
				for _, block := range packToolBlocks(msgs[idx:end], idx) {
					wrote := false
					for _, j := range block {
						// Anchor to where this message's rows land. One that
						// renders nothing (the assistant's tool-calls-only half,
						// or a hidden success) anchors to the next row that does,
						// so /jump still reaches a visible line.
						anchors = append(anchors, MessageAnchor{MessageIdx: j, Row: len(out)})
						if len(rendered[j]) == 0 {
							continue
						}
						out = append(out, rendered[j]...)
						wrote = true
					}
					// A block that drew nothing gets no separator — hidden mode
					// swallows a run of successful calls entirely, and a blank
					// would be the only trace left of it.
					if wrote {
						out = append(out, "")
					}
				}
				idx = end
				continue
			}
		}
		appendMsg(idx)
		idx++
	}
	// Only render the streaming header/body when there's actual
	// text to show. An empty streaming block (streamOn=true,
	// Streaming="") appears when a turn starts with a tool_use
	// block instead of text — in that case the live tool-call
	// overlay below is the real content and a naked "terva" bar
	// above it reads as a stray empty message.
	if v.StreamingActive && strings.TrimSpace(v.Streaming) != "" {
		// Stream the partial assistant text through the same markdown
		// renderer used for finalised messages so code fences, diffs,
		// lists, and inline styles look the same while streaming and
		// don't suddenly reflow when the turn ends. No speaker header
		// is drawn; the indent matches the finalised assistant body in
		// renderMessage so the column stays consistent across the
		// stream/finalise transition. Width is capped so ultra-wide
		// terminals don't produce edge-to-edge code-fence rules or
		// unreadably long prose lines.
		const indent = "  "
		inner := assistantBodyWidth(width - len(indent))
		md := RenderMarkdown(v.Streaming, v.Theme, inner)
		for _, l := range strings.Split(md, "\n") {
			for _, w := range wrapANSILineKeepStyle(l, inner) {
				out = append(out, indent+w)
			}
		}
		out = append(out, "")
	}
	// Live tool-call overlay: keep the in-flight box visible after
	// the assistant tool_use is appended, then hide it only when the
	// matching tool_result reaches the transcript. Assistant tool_use
	// blocks render no rows of their own, so suppressing the overlay
	// at that point makes the box disappear while the tool is running.
	finalised := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if b, ok := c.(provider.ToolResultBlock); ok {
				finalised[b.CallID] = true
			}
		}
	}
	for _, tc := range v.ToolCalls {
		if finalised[tc.ID] {
			continue
		}
		out = append(out, v.renderToolCall(tc, width)...)
		out = append(out, "")
	}
	if v.Err != "" {
		out = append(out, v.renderErr(width)...)
		out = append(out, "")
	}
	return out, anchors
}

// toolArgInfo is the per-call data refreshToolPaths derives from a tool
// call's JSON arguments: the path (tool_result syntax language), the
// read offset (line-number gutter), and the box label. Memoised by
// tool_use_id in View.toolArgCache.
type toolArgInfo struct {
	// argsLen is the length of the arguments this was parsed from. A
	// streaming call's args only grow, so a length change is enough to
	// know the cached parse is stale; a finished call's args never change.
	argsLen int
	path    string
	offset  int
	label   string
}

// parseToolArgInfo decodes a tool call's arguments ONCE and derives the
// path, offset, and label together. It replaces three separate
// json.Unmarshal passes (pathFromToolArgs + offsetFromToolArgs +
// ShortArgs) — individually cheap, but they ran for every tool call on
// every redraw before refreshToolPaths began caching.
func parseToolArgInfo(name string, raw json.RawMessage) toolArgInfo {
	info := toolArgInfo{argsLen: len(raw), label: name + " "}
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return info
	}
	info.path = pathFromParsed(v)
	info.offset = offsetFromParsed(v)
	info.label = name + " " + shortArgsFromParsed(name, v)
	return info
}

// refreshToolPaths rebuilds the tool_use_id -> path/offset/label maps
// from the current transcript. Called once per Build() so tool result
// blocks (which may be cached) can look up their syntax language when
// they were originally rendered.
//
// The derived data for a given tool_use_id is immutable once the call's
// arguments stop streaming, so results are memoised in toolArgCache and
// a call's JSON is re-parsed only when it first appears or while its
// args are still growing (argsLen changes). Before this cache, every
// redraw re-unmarshalled every tool call's args three times — O(N) JSON
// work per frame that climbed as a session accumulated tool calls. The
// transcript walk itself stays O(N) but only touches maps.
func (v *View) refreshToolPaths() {
	v.toolPaths = map[string]string{}
	v.toolStartLines = map[string]int{}
	v.toolCallLabels = map[string]string{}
	v.toolCallNames = map[string]string{}
	// next is rebuilt each call so the cache stays pruned to the tool
	// calls currently in the transcript (compaction/fork can drop some).
	next := make(map[string]toolArgInfo, len(v.toolArgCache))
	for _, m := range v.Messages {
		for _, c := range m.Content {
			tc, ok := c.(provider.ToolCallBlock)
			if !ok {
				continue
			}
			info, cached := v.toolArgCache[tc.ID]
			if !cached || info.argsLen != len(tc.Arguments) {
				info = parseToolArgInfo(tc.Name, tc.Arguments)
			}
			next[tc.ID] = info
			if info.path != "" {
				v.toolPaths[tc.ID] = info.path
			}
			if info.offset >= 1 {
				v.toolStartLines[tc.ID] = info.offset
			}
			v.toolCallLabels[tc.ID] = info.label
			v.toolCallNames[tc.ID] = tc.Name
		}
	}
	v.toolArgCache = next
}

// renderMessageCached returns the rendered line slice for m, using the
// cache if the same (content hash, width, expandAll) combination has
// been rendered before. The slice returned is shared — callers must
// not mutate it; Build() only ever appends to its own `out` so the
// shared slice is safe.
func (v *View) renderMessageCached(m provider.Message, width int, turnOpen bool) []string {
	key := msgCacheKey{
		hash:        hashMessage(m),
		width:       width,
		expandAll:   v.ExpandAll,
		toolDisplay: v.ToolDisplay,
		turnOpen:    turnOpen,
	}
	if v.renderCache != nil {
		if lines, ok := v.renderCache[key]; ok {
			return lines
		}
	}
	lines := v.renderMessage(m, width, turnOpen)
	if v.renderCache != nil {
		// Bound the cache: 4x the current message count is enough to
		// survive /compact churn without leaking memory across a very
		// long session.
		max := len(v.Messages) * 4
		if max < 32 {
			max = 32
		}
		if len(v.renderCache) > max {
			// Drop half the entries. map iteration order gives us a
			// pseudo-LRU for free.
			dropped := 0
			target := len(v.renderCache) / 2
			for k := range v.renderCache {
				if dropped >= target {
					break
				}
				delete(v.renderCache, k)
				dropped++
			}
		}
		v.renderCache[key] = lines
	}
	return lines
}

// hashMessage returns a 64-bit FNV-1a over the role + content blocks
// of m, used as the renderCache key: two messages with the same role
// and content render identically. Prose and tool-call args are hashed in
// full (their content is their identity), but tool-result bodies — which
// are immutable and CallID-identified — are fingerprinted by length
// rather than hashed, since re-hashing large tool output on every redraw
// dominated render CPU. See the ToolResultBlock case for the trade-off.
func hashMessage(m provider.Message) uint64 {
	h := fnv64aInit
	h = fnv64aWrite(h, []byte(m.Role))
	h = fnv64aWriteByte(h, 0)
	// The Meta that renderMessage actually branches on. Content alone is not enough:
	// the two states of a /clear divider have IDENTICAL (empty) content and must
	// render differently, so hashing content only served the stale row back from the
	// cache forever. Same latent trap for a compaction whose token count changes
	// under an unchanged summary. Anything renderMessage reads must be hashed.
	// "shared" is here for a reason the others are not: it ARRIVES LATE. The
	// live fold appends a share to a tool-role message that is already in the
	// transcript, so without hashing it the cache serves back the row it
	// rendered a moment earlier and the card never appears at all.
	for _, k := range []string{"compaction", "tokens_before", "clear", MetaSharedKey} {
		if val := m.Meta[k]; val != "" {
			h = fnv64aWriteByte(h, 'm')
			h = fnv64aWrite(h, []byte(k))
			h = fnv64aWrite(h, []byte(val))
		}
	}
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.TextBlock:
			h = fnv64aWriteByte(h, 't')
			h = fnv64aWrite(h, []byte(b.Text))
		case provider.ImageBlock:
			h = fnv64aWriteByte(h, 'i')
			h = fnv64aWrite(h, []byte(b.MimeType))
			h = fnv64aWrite(h, b.Data)
		case provider.ToolCallBlock:
			h = fnv64aWriteByte(h, 'c')
			h = fnv64aWrite(h, []byte(b.ID))
			h = fnv64aWrite(h, []byte(b.Name))
			h = fnv64aWrite(h, []byte(b.Arguments))
		case provider.ToolResultBlock:
			// A tool result is immutable once produced and uniquely
			// identified by CallID, so its (often large) body never needs
			// hashing: CallID + IsError + a per-inner-block (type, length)
			// signature distinguishes every distinct result and still
			// detects a streaming result growing (its length changes),
			// without re-hashing megabytes of output on every redraw.
			// Trade-off: a same-length, different-content swap of a
			// finished result would not invalidate — which real tool
			// output (regenerated wholesale, monotonic while streaming)
			// doesn't do.
			h = fnv64aWriteByte(h, 'r')
			h = fnv64aWrite(h, []byte(b.CallID))
			if b.IsError {
				h = fnv64aWriteByte(h, 'E')
			}
			for _, inner := range b.Content {
				switch ib := inner.(type) {
				case provider.TextBlock:
					h = fnv64aWriteByte(h, 't')
					h = fnv64aWriteUint(h, uint64(len(ib.Text)))
				case provider.ImageBlock:
					h = fnv64aWriteByte(h, 'i')
					h = fnv64aWrite(h, []byte(ib.MimeType))
					h = fnv64aWriteUint(h, uint64(len(ib.Data)))
				}
			}
		}
		h = fnv64aWriteByte(h, 0)
	}
	return h
}

// FNV-1a implementation inlined so we don't pay the interface cost of
// hash.Hash64 on every Build(). The whole point here is speed.
const (
	fnv64aInit  uint64 = 0xcbf29ce484222325
	fnv64aPrime uint64 = 0x100000001b3
)

// assistantBodyRightPad is the blank gutter kept on the right
// side of every assistant prose line.
const assistantBodyRightPad = 2

// assistantBodyWidth returns the usable width for the assistant
// message body (markdown prose + code fences). Uses the full
// column width passed in minus assistantBodyRightPad, clamped
// at 1 so wrap helpers don't divide by zero on absurdly narrow
// terminals. The right-side padding keeps a small breathing
// column to the terminal edge that mirrors the left indent.
func assistantBodyWidth(outer int) int {
	outer -= assistantBodyRightPad
	if outer < 1 {
		return 1
	}
	return outer
}

func fnv64aWriteByte(h uint64, b byte) uint64 {
	h ^= uint64(b)
	h *= fnv64aPrime
	return h
}

func fnv64aWrite(h uint64, p []byte) uint64 {
	for _, b := range p {
		h ^= uint64(b)
		h *= fnv64aPrime
	}
	return h
}

// fnv64aWriteUint mixes an integer (e.g. a content length) into the hash
// without allocating. Used by hashMessage to fingerprint immutable tool
// output by length instead of re-hashing the whole body.
func fnv64aWriteUint(h uint64, n uint64) uint64 {
	for range 8 {
		h = fnv64aWriteByte(h, byte(n))
		n >>= 8
	}
	return h
}

func (v *View) renderMessage(m provider.Message, width int, turnOpen bool) []string {
	var lines []string

	// Compaction summary: a divider in the conversation, not a user message.
	// Collapsed it is a one-line rule; expanded (ctrl+o) it shows the summary
	// the model was actually left with.
	if m.Meta["compaction"] == "true" {
		return v.renderCompactionBlock(m, width)
	}
	// A /clear: a harder boundary than a compaction, and one the transcript cannot
	// show on its own (a clear leaves no message behind — the client mints this).
	if state := m.Meta["clear"]; state != "" {
		return v.renderClearBlock(state, width)
	}

	switch m.Role {
	case provider.RoleUser:
		// User rows: no speaker label. Each line is painted with a
		// faint bubble background tint (UserBubble) so the user's
		// turns visually segment the chat without needing a header.
		// One tinted blank row above and below approximates CSS
		// padding-top / padding-bottom (terminals can't do fractional
		// rows, so a full row of bubble-coloured whitespace is the
		// closest analogue and gives the bubble visible breathing
		// room).
		// User rows: a 1-cell "▌" accent bar at column 0 painted on
		// the bubble bg, so the bar's cell shares the panel tint and
		// there is no visible gap between bar and panel. (Some terminals
		// — iTerm2, Apple Terminal — may smear the cell-0 bg into the
		// window inset to the left; that's a terminal-side rendering
		// quirk we accept in exchange for a clean bar-to-panel join.)
		const leftGutter = 0                               // cells of bubble bg between bar and text
		const rightGutter = 2                              // cells of bubble bg between text and right edge
		innerWidth := width - 2 - leftGutter - rightGutter // 2 = bar's two cells (▌ + trailing space)
		if innerWidth < 1 {
			innerWidth = 1
		}
		row := func(content string) string {
			inner := strings.Repeat(" ", leftGutter) + content
			padded := v.Theme.UserBubble(inner, width-2)
			bar := v.Theme.BG(v.Theme.UserBubbleBG, v.Theme.FG256(v.Theme.Accent, "▌ "))
			return bar + padded
		}
		var bubble []string
		for _, c := range m.Content {
			switch b := c.(type) {
			case provider.TextBlock:
				for _, l := range strings.Split(b.Text, "\n") {
					l = sanitizeUserBubbleLine(l)
					for _, w := range wrapLine(l, innerWidth, "") {
						bubble = append(bubble, row(w))
					}
				}
			case provider.ImageBlock:
				bubble = append(bubble, row(fmt.Sprintf("[image %s, %d bytes]", b.MimeType, len(b.Data))))
			}
		}
		if len(bubble) > 0 {
			lines = append(lines, row(""))
			lines = append(lines, bubble...)
			lines = append(lines, row(""))
		}
	case provider.RoleAssistant:
		// Assistant rows: no speaker label either. Prose still gets a
		// small left indent so it visually aligns with tool box body
		// content, but no "terva" header.
		_ = turnOpen
		const indent = "  "
		inner := assistantBodyWidth(width - len(indent))
		for _, c := range m.Content {
			switch b := c.(type) {
			case provider.TextBlock:
				md := RenderMarkdown(strings.TrimLeft(b.Text, "\n"), v.Theme, inner)
				for _, l := range strings.Split(md, "\n") {
					for _, w := range wrapANSILineKeepStyle(l, inner) {
						lines = append(lines, indent+w)
					}
				}
			case provider.ToolCallBlock:
				// The whole box (top + body + bottom) is rendered by
				// the matching tool_result message so a single
				// assistant message that batches multiple tool_use
				// blocks doesn't produce a stack of unclosed top
				// edges. Each tool result owns its own box.
				_ = b
			case provider.ImageBlock:
				// An image the assistant drew inline (native image output).
				// Render it like a tool-result image but without box edges —
				// assistant prose isn't boxed — stripping the footprint
				// sentinel and applying the same left indent as the prose.
				for _, line := range v.renderImageBlock(b, inner) {
					_, stripped := parseImageFootprint(line)
					lines = append(lines, indent+stripped)
				}
			}
		}
	case provider.RoleTool:
		// The files this step's calls handed to the user. Parsed once for the
		// message and filtered per call below, because one tool-role message
		// batches a whole step's results.
		shares := parseSharedFiles(m.Meta)
		for _, c := range m.Content {
			if tr, ok := c.(provider.ToolResultBlock); ok {
				// Reduced tool display: swap the whole box for a one-line
				// muted summary, or for nothing at all. ctrl+o (ExpandAll)
				// restores full boxes via effectiveToolDisplay. Failures
				// surface as a minimal line even in hidden mode — tucking
				// mechanics away must never hide that something went wrong.
				switch v.effectiveToolDisplay() {
				case ToolDisplayGrouped:
					// The run's single summary line is emitted by the
					// assembly loop in BuildWithAnchors (it needs the
					// whole run at once); each result renders nothing of
					// its own here. Failures are surfaced by the group
					// head's "· N failed" count, not per box.
					continue
				case ToolDisplayHidden:
					if !tr.IsError {
						// Hidden suppresses the call's mechanics, not its
						// deliverable: the file the user asked for is the one
						// thing here that is not noise.
						lines = append(lines, v.sharedCardRows(sharedFilesFor(shares, tr.CallID), width)...)
						continue
					}
					fallthrough
				case ToolDisplayMinimal:
					label := ""
					if v.toolCallLabels != nil {
						label = v.toolCallLabels[tr.CallID]
					}
					lines = append(lines, minimalToolLine(v.Theme, label, toolResultSummary(tr.Content), tr.IsError, width))
					// The cards survive the reduced displays. Those modes hide
					// tool MECHANICS, which is not what a shared file is: the
					// user asked the agent for a thing, and "tool output is
					// noisy" is no reason to withhold the thing itself.
					lines = append(lines, v.sharedCardRows(sharedFilesFor(shares, tr.CallID), width)...)
					continue
				}
				color := v.Theme.ToolOut
				if tr.IsError {
					color = v.Theme.Error
				}
				path := ""
				if v.toolPaths != nil {
					path = v.toolPaths[tr.CallID]
				}
				startLine := 1
				if v.toolStartLines != nil {
					if s := v.toolStartLines[tr.CallID]; s > 0 {
						startLine = s
					}
				}
				label := ""
				if v.toolCallLabels != nil {
					label = v.toolCallLabels[tr.CallID]
				}
				// Each tool result owns a complete box: top edge with
				// the call's label, body rows wrapped in vertical
				// edges, bottom edge to close. The label is looked up
				// from the matching ToolCallBlock so multiple calls
				// in one assistant message render as N adjacent boxes
				// instead of stacking unclosed top edges.
				lines = append(lines, toolBoxTop(v.Theme, label, width))
				lines = append(lines, toolBoxSide(v.Theme, "", width))
				if tr.IsError {
					lines = append(lines, toolBoxSide(v.Theme, v.Theme.FG256(color, "  error"), width))
				}
				for _, line := range v.renderToolResultContent(tr.Content, width, color, path, startLine) {
					// Image-footprint rows (the escape row, the blank
					// reservation rows beneath it, and the gap row
					// before the metadata caption) are tagged with the
					// imageFootprintSentinel by renderImageBlock. Strip
					// the tag, parse the optional width hint, then wrap
					// the row in the usual │ ... │ box edges so the
					// frame stays continuous around the image.
					imgCells, stripped := parseImageFootprint(line)
					if hasImageEscapeLine(stripped) {
						lines = append(lines, toolBoxSideWithImage(v.Theme, stripped, imgCells, width))
					} else {
						lines = append(lines, toolBoxSide(v.Theme, stripped, width))
					}
				}
				lines = append(lines, toolBoxSide(v.Theme, "", width))
				lines = append(lines, toolBoxBottom(v.Theme, width))
				// Cards go BELOW the closed box: the box is what the call did,
				// the card is what the user got.
				lines = append(lines, v.sharedCardRows(sharedFilesFor(shares, tr.CallID), width)...)
			}
		}
	}
	return lines
}

// sharedCardRows renders one call's shared-file cards for the transcript,
// resolving the image-footprint tagging the card rows may carry.
//
// The cards are unboxed, so a preview's footprint rows are emitted bare rather
// than wrapped in │ edges — an image's graphics rectangle paints over anything
// drawn beside it, which is what the sentinel exists to prevent.
func (v *View) sharedCardRows(files []SharedFile, width int) []string {
	if len(files) == 0 {
		return nil
	}
	var out []string
	for _, row := range v.renderSharedFileCards(files, width) {
		_, stripped := parseImageFootprint(row)
		out = append(out, stripped)
	}
	return out
}

func (v *View) renderToolCall(tc ToolCallView, width int) []string {
	var lines []string

	// Header label. While the call is still streaming, prefer the
	// live path extracted from the partial args so the user sees
	// the target file as soon as it's known, even before the full
	// JSON arrived.
	arg := tc.Args
	if arg == "" && tc.LivePath != "" {
		arg = tc.LivePath
	}
	label := tc.Name + " " + arg

	// Reduced tool display applies to the live overlay too: hidden
	// shows nothing (the status-bar spinner already signals activity)
	// unless the call failed, minimal shows one muted line that tracks
	// the call's state.
	switch v.effectiveToolDisplay() {
	case ToolDisplayHidden:
		if !tc.Error {
			return nil
		}
		fallthrough
	case ToolDisplayMinimal, ToolDisplayGrouped:
		// Grouped mode collapses finished runs to one line, but an
		// in-flight call has no run yet — show it as a minimal line so
		// the user sees live progress; it folds into the run summary
		// once its result reaches the transcript.
		suffix := ""
		if tc.Result == "" && !tc.Done {
			suffix = "running…"
		} else if tc.Result != "" {
			suffix = countLinesSummary(strings.Count(tc.Result, "\n")+1, 0)
		}
		return []string{minimalToolLine(v.Theme, label, suffix, tc.Error, width)}
	}

	// No leading blank: Build()'s inter-message separator already
	// places one blank row between the previous transcript content
	// and the live overlay, matching the spacing the same call has
	// once it finalises into a transcript box. Adding another blank
	// here would double the gap during streaming and visibly tighten
	// when the overlay disappears.

	// Live body (write/edit): keep the streamed preview visible until
	// a real tool result arrives. The provider can finish the tool_use
	// JSON before terva has executed the tool, so keying this on
	// tc.Streaming makes write/edit boxes collapse for a moment between
	// EvToolUseEnd and EvToolResult.
	if tc.Result == "" {
		if body := v.renderLiveToolBody(tc, width); len(body) > 0 {
			// Pad the live preview up to the call's high-water height so
			// it never shrinks mid-stream (e.g. between edit 1 and the
			// start of edit 2). Padding sits inside the box as real
			// box-side rows so the surrounding layout stays put. The
			// high-water mark is per call id and is dropped the moment a
			// Result lands (this branch only runs while Result == "").
			high := len(body)
			if v.liveBodyHigh != nil {
				if prev := v.liveBodyHigh[tc.ID]; prev > high {
					high = prev
				}
				v.liveBodyHigh[tc.ID] = high
			}
			for len(body) < high {
				body = append(body, toolBoxSide(v.Theme, "", width))
			}
			lines = append(lines, toolBoxTop(v.Theme, label, width))
			lines = append(lines, toolBoxSide(v.Theme, "", width))
			lines = append(lines, body...)
			lines = append(lines, toolBoxSide(v.Theme, "", width))
			lines = append(lines, toolBoxBottom(v.Theme, width))
			return lines
		}
	}

	// Running / finished tool call with no streamed body. While in flight, a
	// long-running tool can report live progress (EvToolProgress) — show it as a
	// muted body until the result lands. Otherwise just the labelled top edge
	// directly closed by the bottom (no blank interior row for no-output tools).
	// The progress body is gated on !Done (EvToolResult never clears Progress,
	// so a tool finishing with empty text must not keep showing stale progress)
	// and collapsed like every other body — bash streams up to 4KB per chunk,
	// which would otherwise balloon and flicker the box on every read.
	if tc.Result == "" {
		lines = append(lines, toolBoxTop(v.Theme, label, width))
		if !tc.Done && strings.TrimSpace(tc.Progress) != "" {
			lines = append(lines, toolBoxSide(v.Theme, "", width))
			body := toolResultBlock(v.Theme, tc.Progress, toolBoxBodyRenderWidth(width), v.Theme.Muted)
			for _, l := range v.collapseToolBody(body, false) {
				lines = append(lines, toolBoxSide(v.Theme, l, width))
			}
			lines = append(lines, toolBoxSide(v.Theme, "", width))
		}
		lines = append(lines, toolBoxBottom(v.Theme, width))
		return lines
	}

	// Finished tool call with a result body. Top edge embeds the
	// label; body lines go through toolBoxSide so each row gets
	// vertical edges; bottom edge closes the box. Blank interior
	// rows after the top and before the bottom give the body a bit
	// of breathing room from the corners.
	lines = append(lines, toolBoxTop(v.Theme, label, width))
	lines = append(lines, toolBoxSide(v.Theme, "", width))
	color := v.Theme.ToolOut
	if tc.Error {
		color = v.Theme.Error
	}
	body := toolResultBlock(v.Theme, tc.Result, toolBoxBodyRenderWidth(width), color)
	for _, l := range v.collapseToolBody(body, false) {
		imgCells, stripped := parseImageFootprint(l)
		if hasImageEscapeLine(stripped) {
			lines = append(lines, toolBoxSideWithImage(v.Theme, stripped, imgCells, width))
			continue
		}
		lines = append(lines, toolBoxSide(v.Theme, stripped, width))
	}
	lines = append(lines, toolBoxSide(v.Theme, "", width))
	lines = append(lines, toolBoxBottom(v.Theme, width))
	return lines
}

// renderLiveToolBody renders the in-flight preview of a streaming
// tool call. Supported tools:
//
//   - write: shows the partial `content` field, syntax-highlighted
//     by the target path's language.
//   - edit:  shows the partial `newText` of the edit currently being
//     streamed, prefixed with a "editing foo.ts (edit 2)" header so
//     the user can see which of a multi-edit batch is in progress.
//   - bash:  shows the full command as soon as the JSON args are known,
//     before stdout/stderr arrives, so long-running commands are legible.
//
// Anything else returns nil and only the tool-call header shows.
func (v *View) renderLiveToolBody(tc ToolCallView, width int) []string {
	switch tc.Name {
	case "write", "Write":
		partial, ok, _ := ExtractPartialStringField(tc.RawJSONBuf, "content")
		if !ok || partial == "" {
			return nil
		}
		return v.wrapLiveBody(v.renderRawFile(partial, tc.LivePath, 1), width)
	case "edit", "Edit":
		partial, ok, _, idx := ExtractLastNewText(tc.RawJSONBuf)
		if !ok || partial == "" {
			return nil
		}
		// Header line hints which edit is streaming and, when more
		// than one has landed, how many the model is doing.
		hint := fmt.Sprintf("edit %d (streaming)", idx)
		body := []string{"    " + v.Theme.FG256(v.Theme.Muted, hint), ""}
		body = append(body, v.renderRawFile(partial, tc.LivePath, 1)...)
		return v.wrapLiveBody(body, width)
	case "bash", "Bash":
		command, ok, _ := ExtractPartialStringField(tc.RawJSONBuf, "command")
		if !ok || strings.TrimSpace(command) == "" {
			return nil
		}
		return v.wrapLiveBody(v.renderLiveBashCommand(command, width), width)
	}
	return nil
}

// renderLiveBashCommand renders a bash tool's `$ command` body (multi-line,
// wrapped) so a long-running command is legible in the live overlay before
// any stdout arrives.
func (v *View) renderLiveBashCommand(command string, width int) []string {
	inner := toolBoxBodyRenderWidth(width)
	prompt := v.Theme.FG256(v.Theme.Muted, "$ ")
	var out []string
	for i, line := range strings.Split(command, "\n") {
		firstPrefix := "    "
		if i == 0 {
			firstPrefix += prompt
		} else {
			firstPrefix += "  "
		}
		contPrefix := "      "
		wrapWidth := inner - visibleWidth(firstPrefix)
		if wrapWidth < 10 {
			wrapWidth = 10
		}
		for j, wrapped := range wrapANSILine(v.Theme.FG256(v.Theme.ToolOut, line), wrapWidth) {
			prefix := firstPrefix
			if j > 0 {
				prefix = contPrefix
			}
			out = append(out, prefix+wrapped)
		}
	}
	return out
}

// wrapLiveBody returns the streaming body content as a list of
// box-side rows: each line wrapped in │ ... │ with right padding so
// the closing edge sits at column width-1. The caller (renderToolCall)
// supplies the surrounding top/bottom edges so the live overlay
// renders as a closed box matching the finalised transcript form.
func (v *View) wrapLiveBody(body []string, width int) []string {
	body = v.collapseToolBody(body, false)
	out := make([]string, 0, len(body))
	for _, l := range body {
		imgCells, stripped := parseImageFootprint(l)
		if hasImageEscapeLine(stripped) {
			out = append(out, toolBoxSideWithImage(v.Theme, stripped, imgCells, width))
			continue
		}
		out = append(out, toolBoxSide(v.Theme, stripped, width))
	}
	return out
}

// toolResultBlock wraps text in thin horizontal rules (top + bottom),
// indenting the body with four spaces. The rules span the content column.

// toolBoxInnerPad is the number of blank cells kept between a box
// edge (┌, │, └) and the content inside it. One cell of breathing
// room makes the label read as part of the box rather than fused to
// the corner, and gives body lines a tiny gutter from the left side.
const toolBoxInnerPad = 1

// toolBoxOuterMargin is the number of blank cells kept to the left of
// the opening corner and to the right of the closing corner. Aligns
// the box's frame with the column where user-bubble text begins, so
// the conversation reads as one column instead of having tool boxes
// running edge-to-edge while user/assistant rows sit indented.
const toolBoxOuterMargin = 2

// toolBoxTop renders the labelled top edge of a tool block:
//
//	┌─  bash xcrun simctl list devices ─────────────────────────────┐
//
// The label is the tool name + short args (the same string previously
// shown on its own row beneath the rule). Padding fills the remainder
// of the line so the right corner sits at column width-1, matching the
// closing edge.
func toolBoxTop(th Theme, label string, width int) string {
	label = oneLineToolLabel(label)
	w := width - 2*toolBoxOuterMargin
	if w < 12 {
		w = 12
	}
	margin := strings.Repeat(" ", toolBoxOuterMargin)
	innerPad := strings.Repeat(" ", toolBoxInnerPad)
	// "┌─" + innerPad + " " + label + " " + innerPad = used; pad
	// with ─ to width-1, then "┐".
	prefix := "┌─" + innerPad + " "
	suffix := " " + innerPad
	used := visibleWidth(prefix) + visibleWidth(label) + visibleWidth(suffix)
	fill := w - used - 1 // -1 for the closing corner
	if fill < 0 {
		// Label overflows; truncate with an ellipsis so the right
		// corner still lands on the right edge. Rare on real
		// terminals (the chat column is usually wide enough).
		over := -fill
		runes := []rune(label)
		if over+3 < len(runes) {
			label = string(runes[:len(runes)-over-3]) + "..."
		} else {
			label = "..."
		}
		used = visibleWidth(prefix) + visibleWidth(label) + visibleWidth(suffix)
		fill = w - used - 1
		if fill < 0 {
			fill = 0
		}
	}
	fillStr := strings.Repeat("─", fill)
	name, rest := splitToolLabel(label)
	return margin + th.FG256(th.Muted, prefix) + th.FG256(th.FG, name) + th.FG256(th.Muted, rest+suffix+fillStr+"┐") + margin
}

func oneLineToolLabel(label string) string {
	return strings.Join(strings.Fields(label), " ")
}

func splitToolLabel(label string) (name, rest string) {
	label = strings.TrimLeft(label, " ")
	if label == "" {
		return "", ""
	}
	idx := strings.IndexAny(label, " \t")
	if idx < 0 {
		return label, ""
	}
	return label[:idx], label[idx:]
}

// toolBoxBottom renders the bottom edge of a tool block:
//
//	└─────────────────────────────────────────────────────────────────────────────┘
//
// Spans the same width as toolBoxTop so the corners line up.
func toolBoxBottom(th Theme, width int) string {
	w := width - 2*toolBoxOuterMargin
	if w < 12 {
		w = 12
	}
	margin := strings.Repeat(" ", toolBoxOuterMargin)
	line := "└" + strings.Repeat("─", w-2) + "┘"
	return margin + th.FG256(th.Muted, line) + margin
}

// hasImageEscapeLine reports whether s contains a Kitty (\x1b_G) or
// iTerm2 (\x1b]1337;File=) inline-image escape. Such rows draw into
// the terminal's graphics layer and shouldn't be wrapped with text
// box edges.
func hasImageEscapeLine(s string) bool {
	return strings.Contains(s, "\x1b]1337;File=") || strings.Contains(s, "\x1b_G")
}

// imageFootprintSentinel marks rows that belong to an inline-image's
// reserved footprint — the escape row plus the blank rows below it
// plus the gap row before the metadata caption. Any consumer that
// wraps content in box edges (│ ... │) detects the sentinel, strips
// it, and emits the row — the image graphics rectangle paints over
// whatever was drawn there. Uses a non-printing C0 control byte so
// it can never appear in normal text or in an ANSI escape sequence
// body.
//
// The escape row carries an extra width hint between two sentinels:
// "\x1e<cells>\x1e<spaces+escape>". Wrappers parse the cells count
// to know how wide the image is in terminal cells and how many cells
// of trailing padding are needed before the closing box edge.
const imageFootprintSentinel = "\x1e"

// parseImageFootprint splits a footprint-tagged row into (cellsWide,
// rest). cellsWide is 0 when the row carries no width hint (the
// reservation blanks and gap row don't, only the escape row does).
func parseImageFootprint(s string) (int, string) {
	if !strings.HasPrefix(s, imageFootprintSentinel) {
		return 0, s
	}
	rest := s[len(imageFootprintSentinel):]
	end := strings.Index(rest, imageFootprintSentinel)
	if end < 0 {
		return 0, rest
	}
	w, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, rest
	}
	return w, rest[end+len(imageFootprintSentinel):]
}

// toolBoxBodyTrimLeft is the number of leading literal spaces stripped
// from each body line as it enters toolBoxSide. Body renderers
// (renderToolText, renderRawFile, the diff/bash helpers, ...) all
// emit a 4-cell indent so the content aligns with the assistant body
// column when there's no box around it. Inside a box the box edge +
// inner pad already supply most of that gutter, so we drop a couple
// of cells from the body's own indent to keep the content from
// drifting too far right.
const toolBoxBodyTrimLeft = 2

// toolBoxBodyRenderWidth is the width body renderers should target before
// their rows are wrapped by toolBoxSide. Most body renderers emit a four-cell
// indent; toolBoxSide trims toolBoxBodyTrimLeft cells from that indent and
// then adds the box edge/padding. Passing the full terminal width lets long
// bash/heredoc lines overrun the inner box and makes terminals soft-wrap,
// visually breaking the right edge. This returns the maximum renderer width
// whose post-trim visible width fits inside the box.
func toolBoxBodyRenderWidth(width int) int {
	w := width - 2*toolBoxOuterMargin
	if w < 12 {
		w = 12
	}
	inner := w - 2 - 2*toolBoxInnerPad
	if inner < 1 {
		inner = 1
	}
	return inner + toolBoxBodyTrimLeft
}

// toolBoxSide wraps a single body line with vertical box edges:
//
//	│  foo bar baz                                            │
//
// The middle preserves the line verbatim (including any ANSI escapes)
// after stripping toolBoxBodyTrimLeft leading literal spaces. One cell
// of inner padding sits on each side so the content breathes from the
// edges; the right padding fills to column width-1.
func toolBoxSide(th Theme, line string, width int) string {
	w := width - 2*toolBoxOuterMargin
	if w < 12 {
		w = 12
	}
	margin := strings.Repeat(" ", toolBoxOuterMargin)
	left := th.FG256(th.Muted, "│") + strings.Repeat(" ", toolBoxInnerPad)
	right := strings.Repeat(" ", toolBoxInnerPad) + th.FG256(th.Muted, "│")
	inner := w - 2 - 2*toolBoxInnerPad // available between the two pads
	line = trimLeadingSpaces(line, toolBoxBodyTrimLeft)

	cur := visibleWidth(line)
	if cur > inner {
		// Last-resort truncation. Strips ANSI styling at the cut
		// point; rare path so the simpler implementation is fine.
		line = stripANSI(line)
		runes := []rune(line)
		if len(runes) > inner {
			line = string(runes[:inner])
		}
		cur = visibleWidth(line)
	}
	pad := inner - cur
	if pad < 0 {
		pad = 0
	}
	return margin + left + line + strings.Repeat(" ", pad) + right + margin
}

// toolBoxSideWithImage wraps a row that carries an inline-image
// escape sequence. The image protocols are configured to not move
// the cursor after rendering (iTerm: doNotMoveCursor=1, kitty/
// ghostty: C=1), so the only visible cell advance on the row is
// from the leading indent spaces before the escape. We pad from
// there to the right edge column and draw the closing │ there.
// imgCells is unused (kept in the signature for compatibility with
// the parseImageFootprint width hint).
func toolBoxSideWithImage(th Theme, line string, imgCells, width int) string {
	_ = imgCells
	w := width - 2*toolBoxOuterMargin
	if w < 12 {
		w = 12
	}
	margin := strings.Repeat(" ", toolBoxOuterMargin)
	left := th.FG256(th.Muted, "│") + strings.Repeat(" ", toolBoxInnerPad)
	right := strings.Repeat(" ", toolBoxInnerPad) + th.FG256(th.Muted, "│")
	inner := w - 2 - 2*toolBoxInnerPad

	line = trimLeadingSpaces(line, toolBoxBodyTrimLeft)

	// Count leading spaces (visible cells consumed before the escape).
	leading := 0
	for leading < len(line) && line[leading] == ' ' {
		leading++
	}

	pad := inner - leading
	if pad < 0 {
		pad = 0
	}
	return margin + left + line + strings.Repeat(" ", pad) + right + margin
}

// trimLeadingSpaces removes up to n literal space characters from the
// start of s without touching anything else. ANSI CSI escapes that
// appear before the spaces are skipped over (so a coloured indent
// like "\x1b[2m    text" still loses its first n cells of indent).
// Stops as soon as it hits a non-space character.
func trimLeadingSpaces(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	trimmed := 0
	i := 0
	for i < len(s) {
		// Pass through any ANSI escape sequence verbatim.
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := i + 2
			for end < len(s) {
				c := s[end]
				end++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if trimmed < n && s[i] == ' ' {
			trimmed++
			i++
			continue
		}
		b.WriteString(s[i:])
		return b.String()
	}
	return b.String()
}

// renderToolResultContent renders the body of a tool result block.
// Text blocks get the usual rules-wrapped treatment; text that looks
// like a unified diff gets +/- coloring. Image blocks are rendered
// inline when the terminal supports a protocol, else as a text
// placeholder with dimensions.
func (v *View) renderToolResultContent(blocks []provider.Content, width, color int, sourcePath string, startLine int) []string {
	var body []string
	hasImage := false
	for _, b := range blocks {
		switch bb := b.(type) {
		case provider.TextBlock:
			bodyWidth := toolBoxBodyRenderWidth(width)
			body = append(body, v.renderToolText(bb.Text, bodyWidth, color, sourcePath, startLine)...)
		case provider.ImageBlock:
			hasImage = true
			body = append(body, v.renderImageBlock(bb, width)...)
		}
	}
	return v.collapseToolBody(body, hasImage)
}

// collapseToolBody trims lines to the configured preview size when the
// view is not in ExpandAll mode, appending a muted "... ctrl+o to
// expand" footer. Image blocks never collapse — they're short in text
// rows but represent real content the user wants to see.
func (v *View) collapseToolBody(lines []string, hasImage bool) []string {
	if v.ExpandAll || hasImage {
		return lines
	}
	if len(lines) <= ToolCollapseLines {
		return lines
	}
	kept := lines[:ToolCollapsePreview]
	hidden := len(lines) - ToolCollapsePreview
	total := len(lines)
	footer := fmt.Sprintf("    ... (%d more lines, %d total, ctrl+o to expand)", hidden, total)
	footer = v.Theme.FG256(v.Theme.Muted, footer)
	out := append([]string(nil), kept...)
	out = append(out, "")
	return append(out, footer)
}

// renderToolText renders a text block inside a tool result. If the
// text contains a unified-diff section (lines starting with "--- " /
// "+++ " / "+" / "-"/" "), those rows are styled with add/remove
// colors matching git diff conventions.
func (v *View) renderToolText(text string, width, defaultColor int, sourcePath string, startLine int) []string {
	// Legacy path: transcripts saved before we dropped line numbers
	// from the read tool still carry "     1\t..." prefixes. Detect and
	// strip them, then fall through to the highlighter.
	if looksLikeNumberedFile(text) {
		return v.renderNumberedFile(text, sourcePath)
	}
	// If the result embeds a unified diff (the edit tool's output
	// starts with a short "applied N edit(s)" line and then a
	// standard --- / +++ / +/- patch), render the patch with
	// add/remove coloring. This takes priority over the file-like
	// detector below because a diff technically has many lines of
	// "file content" but what the user cares about is what changed,
	// not a dump of the post-edit file.
	if looksLikeUnifiedDiff(text) {
		return v.renderUnifiedDiff(text, width, sourcePath)
	}

	// Current path: text came from `read` as raw file bytes. When a
	// source path is known (the call had a `path` arg), render with
	// a synthetic line-number gutter starting at startLine so the
	// on-screen view still looks like cat -n. Doesn't apply to non-
	// file tool outputs (bash stdout, display notes, etc.).
	if sourcePath != "" && looksLikeFileContent(text) {
		return v.renderRawFile(text, sourcePath, startLine)
	}

	// No truncation — the full tool output is rendered into chat and
	// becomes part of the scrollback you can page back through.
	//
	// Normalize before wrapping: this branch also serves non-bash tools
	// (grep, MCP servers, extensions) whose output can carry tabs and
	// control bytes just like a subprocess. Left un-expanded, a tab in a
	// box row draws wider than visibleWidth counted and breaks the frame.
	// The special-cased paths above keep the raw text (the numbered-file
	// detector needs literal tabs); each expands tabs itself.
	lines := normalizeBashOutputLines(strings.Split(text, "\n"))

	// Bash-result styling: when the first row looks like a shell
	// prompt line ("$ ...") emitted by the bash tool, style the
	// prompt line in accent and the trailing "[exit N]  Took X.Ys"
	// line in muted type so the command + timing read at a glance.
	// Everything between is left on the default tool-output color.
	if len(lines) > 0 && strings.HasPrefix(lines[0], "$ ") {
		return v.renderBashResult(lines, width, defaultColor)
	}

	inDiff := false
	oldLine, newLine := 1, 1
	var out []string
	for _, l := range lines {
		// Detect diff header: "--- name" followed somewhere by "+++ name".
		if strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
			inDiff = true
			out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, l))
			continue
		}
		// Hunk header "@@ -a,b +c,d @@" resets the counters so patches
		// that skip around in the file still get correct numbering.
		if inDiff && strings.HasPrefix(l, "@@") {
			if o, n, ok := parseHunkHeader(l); ok {
				oldLine, newLine = o, n
			}
			out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, l))
			continue
		}
		if inDiff && strings.TrimSpace(l) == "..." {
			out = append(out, "")
			out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, "..."))
			continue
		}
		if inDiff && len(l) > 0 {
			switch l[0] {
			case '+':
				out = append(out, v.renderDiffRow(l, width, v.Theme.Tool, newLine, '+', sourcePath))
				newLine++
				continue
			case '-':
				out = append(out, v.renderDiffRow(l, width, v.Theme.Error, oldLine, '-', sourcePath))
				oldLine++
				continue
			case ' ':
				out = append(out, v.renderDiffRow(l, width, v.Theme.Muted, newLine, ' ', sourcePath))
				oldLine++
				newLine++
				continue
			}
		}
		// Regular line.
		for _, w := range wrapLine(l, width-4, "    ") {
			out = append(out, "    "+v.Theme.FG256(defaultColor, w))
		}
	}
	return out
}

// parseHunkHeader extracts the starting old/new line from a unified
// diff hunk header ("@@ -12,5 +12,7 @@ ..."). Returns ok=false if the
// header is malformed or missing numbers.
func parseHunkHeader(l string) (oldStart, newStart int, ok bool) {
	// Skip "@@ "
	rest := strings.TrimPrefix(l, "@@")
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "-") {
		return 0, 0, false
	}
	rest = rest[1:]
	space := strings.IndexByte(rest, ' ')
	if space < 0 {
		return 0, 0, false
	}
	oldPart := rest[:space]
	rest = strings.TrimSpace(rest[space+1:])
	if !strings.HasPrefix(rest, "+") {
		return 0, 0, false
	}
	rest = rest[1:]
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		rest = rest[:sp]
	}
	parseStart := func(s string) (int, bool) {
		if c := strings.IndexByte(s, ','); c >= 0 {
			s = s[:c]
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, false
		}
		return n, true
	}
	o, ok1 := parseStart(oldPart)
	n, ok2 := parseStart(rest)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return o, n, true
}

// renderDiffRow renders one unified-diff line with a read-style gutter
// (6-cell right-aligned line number, muted) followed by the +/-/space
// marker and the code. Code is syntax-highlighted if sourcePath hints
// at a known language; falls back to the plain diff color otherwise.
func (v *View) renderDiffRow(line string, width, color int, lineNo int, mark byte, sourcePath string) string {
	if len(line) == 0 {
		return ""
	}
	code := expandTabs(line[1:]) // strip the leading marker; expand tabs

	// Syntax-highlight the code half when we know the language. Use
	// the same HighlightCode pipeline as renderNumberedFile so the
	// palette matches.
	lang := LanguageFromPath(sourcePath)
	var codeRendered string
	if lang != "" {
		if h := v.Theme.HighlightCode(code, lang); len(h) == 1 {
			codeRendered = h[0]
		}
	}
	if codeRendered == "" {
		if mark == ' ' {
			codeRendered = v.Theme.FG256(v.Theme.Muted, code)
		} else {
			codeRendered = v.Theme.FG256(color, code)
		}
	}

	// Gutter shape: sign + number share a color so they read as one
	// visual token ("+123") instead of a neutral line number next to
	// a stray marker. Unchanged context lines get a muted gutter and
	// a leading space so column alignment stays consistent with +/-
	// rows.
	var gutterText string
	switch mark {
	case '+':
		gutterText = fmt.Sprintf("+%3d ", lineNo)
	case '-':
		gutterText = fmt.Sprintf("-%3d ", lineNo)
	default:
		gutterText = fmt.Sprintf(" %3d ", lineNo)
	}
	var gutter string
	if mark == ' ' {
		gutter = v.Theme.FG256(v.Theme.Muted, gutterText)
	} else {
		gutter = v.Theme.FG256(color, gutterText)
	}
	row := "    " + gutter + codeRendered

	// Cheap width clamp: truncate visible text if the raw code is too
	// long. We work on the pre-ANSI code string because measuring ansi
	// output is unreliable.
	maxCode := width - 4 /* indent */ - 7 /* gutter (sign+5 digits+tab) */
	if maxCode > 0 && len(code) > maxCode {
		trunc := strings.Repeat(".", maxCode)
		if maxCode > 3 {
			trunc = code[:maxCode-3] + "..."
		}
		if lang != "" {
			if h := v.Theme.HighlightCode(trunc, lang); len(h) == 1 {
				codeRendered = h[0]
			} else if mark == ' ' {
				codeRendered = v.Theme.FG256(v.Theme.Muted, trunc)
			} else {
				codeRendered = v.Theme.FG256(color, trunc)
			}
		} else if mark == ' ' {
			codeRendered = v.Theme.FG256(v.Theme.Muted, trunc)
		} else {
			codeRendered = v.Theme.FG256(color, trunc)
		}
		row = "    " + gutter + codeRendered
	}
	return row
}

// renderImageBlock returns the lines for one image, inline if possible.
//
// Inline image escapes paint into multiple terminal rows but the terva
// renderer treats each slice entry as a single row. To prevent chat
// content from being drawn on top of the image, we pad with blank rows
// so the image's real footprint is reflected in the frame height.
func (v *View) renderImageBlock(b provider.ImageBlock, width int) []string {
	return v.renderImageBlockData(b.Data, b.MimeType, width)
}

// renderImageBlockData is renderImageBlock over raw bytes rather than a
// transcript block. Split out so a surface that holds pixels which never were
// a provider.ImageBlock — a shared-file preview, fetched by id — draws them
// through exactly this path, footprint reservation and sentinel tagging
// included. Two copies of that escape/reservation dance is how one of them
// ends up painting over the rows below it.
func (v *View) renderImageBlockData(data []byte, mimeType string, width int) []string {
	w, h := ImageDimensions(data)
	kb := len(data) / 1024
	// Four-space indent matches every other tool-output row
	// (renderRawFile, renderBashResult, diff rows, "... N more
	// lines" footers). Keeps the image's metadata line vertically
	// aligned with the file content above / below it.
	info := fmt.Sprintf("    image - %s - %dx%d - %d KB", mimeType, w, h, kb)

	if v.ImageProto != ImageProtocolNone {
		// Clamp rendered width so the image never overflows the chat
		// column. Subtract a 4-cell indent for the tool result block,
		// cap at 60 cells, floor at 10.
		cells := width - 4
		if cells > 60 {
			cells = 60
		}
		if cells < 10 {
			cells = 10
		}
		const maxRows = 20
		if seq := RenderInlineImageScaled(v.ImageProto, data, mimeType, cells, maxRows); seq != "" {
			rows, actualCells := InlineImageFootprint(data, cells, maxRows)
			if rows < 1 {
				rows = 1
			}
			if actualCells < 1 {
				actualCells = cells
			}
			// Reserve the image footprint first, then place the escape
			// in the top-left of that blank rectangle. Keeping the
			// metadata above the image made some terminals blend text
			// and graphics layers during full-screen repaints; metadata
			// below the reserved rectangle is more stable and easier to
			// read. One extra blank row before the metadata gives the
			// caption breathing room from the image's last pixel row.
			//
			// Every footprint row (escape, reservations, gap) is tagged
			// with imageFootprintSentinel so callers wrapping content in
			// box edges can recognise these rows and emit them bare —
			// the image's graphics rectangle paints over them and any
			// │ character drawn alongside would visibly bleed through.
			// 4 leading cells push the escape past the box's left
			// edge plus a small interior gutter so the image rectangle
			// sits visibly inside the frame instead of kissing the │.
			// The escape row carries a width hint after the sentinel
			// ("\x1e<cells>\x1e...") so toolBoxSide knows how many
			// cells the image occupies and can pad to the right edge.
			widthHint := fmt.Sprintf("%s%d%s", imageFootprintSentinel, actualCells, imageFootprintSentinel)
			out := make([]string, 0, rows+3)
			out = append(out, widthHint+"    "+seq)
			for i := 1; i < rows; i++ {
				out = append(out, imageFootprintSentinel)
			}
			out = append(out, imageFootprintSentinel)
			out = append(out, v.Theme.FG256(v.Theme.Muted, info))
			return out
		}
	}
	return []string{v.Theme.FG256(v.Theme.Muted, info)}
}

// looksLikeNumberedFile returns true when text matches the `read`
// tool's "     N\tcontent" format for most of its lines.
func looksLikeNumberedFile(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return false
	}
	hits := 0
	scanned := 0
	for _, l := range lines {
		if l == "" {
			continue
		}
		scanned++
		if scanned > 20 {
			break
		}
		if numberedLineRE.MatchString(l) {
			hits++
		}
	}
	if scanned == 0 {
		return false
	}
	return hits*2 >= scanned // majority of non-empty lines match
}

var numberedLineRE = regexp.MustCompile(`^\s*\d+\t`)

// renderNumberedFile strips line numbers, highlights the code, and
// re-attaches the line numbers in muted color.
func (v *View) renderNumberedFile(text, sourcePath string) []string {
	lines := strings.Split(text, "\n")
	gutters := make([]string, 0, len(lines))
	codes := make([]string, 0, len(lines))
	for _, l := range lines {
		idx := strings.IndexByte(l, '\t')
		if idx < 0 || !numberedLineRE.MatchString(l) {
			// Non-code footer (e.g. "[truncated at 2000 lines]").
			gutters = append(gutters, "")
			codes = append(codes, l)
			continue
		}
		gutter := l[:idx] + " " // replace tab with single space
		code := expandTabs(l[idx+1:])
		gutters = append(gutters, gutter)
		codes = append(codes, code)
	}
	lang := LanguageFromPath(sourcePath)
	var highlighted []string
	if lang != "" {
		highlighted = v.Theme.HighlightCode(strings.Join(codes, "\n"), lang)
		// Chroma sometimes collapses the trailing empty line. Pad to
		// align with the gutter slice so per-line zipping works.
		for len(highlighted) < len(codes) {
			highlighted = append(highlighted, "")
		}
		if len(highlighted) > len(codes) {
			highlighted = highlighted[:len(codes)]
		}
	} else {
		// No lexer for this file type — render in the ToolOut color so
		// code is visually distinct from the muted gutter.
		highlighted = make([]string, len(codes))
		for i, c := range codes {
			highlighted[i] = v.Theme.FG256(v.Theme.ToolOut, c)
		}
	}
	out := make([]string, 0, len(codes))
	for i, code := range highlighted {
		g := gutters[i]
		if g == "" {
			out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, code))
			continue
		}
		out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, g)+code)
	}
	return out
}

// looksLikeFileContent is a cheap guard to distinguish a read-tool
// result from bash stdout or a status message. File content usually
// contains characters that status messages don't (code punctuation,
// longer lines, multiple lines) and rarely starts with the "  >"-
// or "error:"-style prefixes tools emit. False positives are OK,
// the worst case is a line-number gutter on something that isn't
// really code.
// renderBashResult styles a bash tool result: the "$ command" first
// line in the accent color, the trailing "[exit N]  Took X.Ys" line
// in muted type, everything else on the default tool-output color.
// Called from renderToolText when the first line starts with "$ ".
func (v *View) renderBashResult(lines []string, width, defaultColor int) []string {
	lines = normalizeBashOutputLines(lines)
	// Identify the footer line (exit + timing). The bash tool writes
	// it as the last non-empty line of the result.
	footerIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[exit ") {
			footerIdx = i
		}
		break
	}

	var out []string
	for i, l := range lines {
		switch {
		case i == 0 && strings.HasPrefix(l, "$ "):
			// Style the $ and the command text in accent. No
			// further processing / wrapping so the shell-style
			// prompt reads at a glance.
			for _, w := range wrapLine(l, width-4, "    ") {
				out = append(out, "    "+v.Theme.FG256(v.Theme.Accent, w))
			}
		case i == footerIdx:
			for _, w := range wrapLine(l, width-4, "    ") {
				out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, w))
			}
		default:
			for _, w := range wrapLine(l, width-4, "    ") {
				out = append(out, "    "+v.Theme.FG256(defaultColor, w))
			}
		}
	}
	return out
}

// normalizeBashOutputLines turns arbitrary terminal output into plain,
// box-safe rows before wrapping. Unlike read/write/edit output, bash
// stdout/stderr may contain tabs, carriage returns, ANSI/OSC escapes,
// and other C0 controls from subprocesses. If those reach the bordered
// tool box, the width calculator can undercount what the terminal will
// draw and the right edge appears broken. Keep printable text, expand
// tabs to spaces, split carriage-return progress rows, and drop escape
// / control sequences.
func normalizeBashOutputLines(lines []string) []string {
	var out []string
	for _, line := range lines {
		for _, part := range strings.Split(strings.ReplaceAll(line, "\r", "\n"), "\n") {
			out = append(out, normalizeBashOutputLine(part))
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func normalizeBashOutputLine(s string) string {
	var b strings.Builder
	col := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC: strip CSI/OSC/DCS and simple escapes.
			i = skipEscapeSequence(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		switch r {
		case '\t':
			spaces := 8 - (col % 8)
			if spaces == 0 {
				spaces = 8
			}
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		case '\b':
			// Backspace-overstrike output is common in spinners/progress bars.
			// Dropping it is safer than moving the TUI cursor backwards.
		case '\n', '\r':
			// Already split by normalizeBashOutputLines.
		default:
			if r < 0x20 || r == 0x7f {
				// Drop non-printing controls.
				break
			}
			b.WriteRune(r)
			col += runewidth.RuneWidth(r)
		}
		i += size
	}
	return b.String()
}

func skipEscapeSequence(s string, i int) int {
	if i >= len(s) || s[i] != 0x1b {
		return i + 1
	}
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[': // CSI: ESC [ ... final byte 0x40-0x7e
		j := i + 2
		for j < len(s) {
			c := s[j]
			j++
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		return j
	case ']': // OSC: ESC ] ... BEL or ST
		return skipStringEscape(s, i+2)
	case 'P', '_', '^', 'X': // DCS/APC/PM/SOS: ESC P ... ST, etc.
		return skipStringEscape(s, i+2)
	default:
		// Two-byte escape (cursor save/restore, charset select, etc.).
		return i + 2
	}
}

func skipStringEscape(s string, i int) int {
	for i < len(s) {
		if s[i] == 0x07 { // BEL
			return i + 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' { // ST
			return i + 2
		}
		i++
	}
	return len(s)
}

// looksLikeUnifiedDiff reports whether text is a context diff as
// emitted by the edit tool: rows start with '+', '-', ' ', or
// literal "..." (context-break marker). The presence of at least
// one '+' or '-' row distinguishes a real diff from an ordinary
// file whose lines happen to begin with a space.
func looksLikeUnifiedDiff(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return false
	}
	sawChange := false
	for _, l := range lines {
		if l == "" {
			continue
		}
		if l == "..." {
			continue
		}
		switch l[0] {
		case '+', '-':
			sawChange = true
		case ' ':
			// context, ok
		default:
			return false
		}
	}
	return sawChange
}

// renderUnifiedDiff renders the edit tool's context diff. Each
// kept row shows a line-number gutter plus a marker column: '+'
// for additions (colored like add), '-' for deletions (colored
// like remove), and unmarked context in muted type. A literal
// "..." line between hunks renders as an ellipsis in muted type,
// indicating skipped unchanged rows. The old and new line
// counters advance so each row carries its actual position in
// the pre- or post-edit file. Heuristic: when we hit a "...", we
// can't know where the next hunk starts, so we don't reset the
// counters — they stay approximate in the rare multi-hunk case.
func (v *View) renderUnifiedDiff(text string, width int, sourcePath string) []string {
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	oldLine, newLine := 1, 1
	var out []string
	for _, l := range lines {
		if l == "" {
			out = append(out, "")
			continue
		}
		if l == "..." {
			out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, "..."))
			continue
		}
		switch l[0] {
		case '+':
			out = append(out, v.renderDiffRow(l, width, v.Theme.Tool, newLine, '+', sourcePath))
			newLine++
		case '-':
			out = append(out, v.renderDiffRow(l, width, v.Theme.Error, oldLine, '-', sourcePath))
			oldLine++
		case ' ':
			out = append(out, v.renderDiffRow(l, width, v.Theme.Muted, newLine, ' ', sourcePath))
			oldLine++
			newLine++
		default:
			for _, w := range wrapLine(l, width-4, "    ") {
				out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, w))
			}
		}
	}
	return out
}

func looksLikeFileContent(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	lines := strings.Split(text, "\n")
	return len(lines) >= 2
}

// renderRawFile renders file content received without embedded line
// numbers (the current read-tool output). Draws a muted gutter like
// "%6d \t" starting at startLine, highlights the code using the
// source path's language, and returns the formatted lines.
func (v *View) renderRawFile(text, sourcePath string, startLine int) []string {
	lines := strings.Split(text, "\n")
	// Drop the trailing empty line that Split produces when text ends
	// in "\n" so the gutter doesn't show a phantom last number.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	// Split code from trailing footer lines ("... [truncated ...]")
	// so we don't number the footer. A single blank separator line
	// immediately before the footer is also pulled out so the
	// gutter doesn't render a phantom number for it.
	codeEnd := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "...") {
			codeEnd = i
			continue
		}
		break
	}
	if codeEnd > 0 && codeEnd < len(lines) && lines[codeEnd-1] == "" {
		codeEnd--
	}
	code := lines[:codeEnd]
	footer := lines[codeEnd:]

	// Expand tabs to 4 spaces so Go / Makefile code renders
	// at a consistent width.
	for i, c := range code {
		code[i] = expandTabs(c)
	}

	lang := LanguageFromPath(sourcePath)
	var highlighted []string
	if lang != "" {
		highlighted = v.Theme.HighlightCode(strings.Join(code, "\n"), lang)
		for len(highlighted) < len(code) {
			highlighted = append(highlighted, "")
		}
		if len(highlighted) > len(code) {
			highlighted = highlighted[:len(code)]
		}
	} else {
		highlighted = make([]string, len(code))
		for i, c := range code {
			highlighted[i] = v.Theme.FG256(v.Theme.ToolOut, c)
		}
	}

	out := make([]string, 0, len(lines))
	for i, c := range highlighted {
		gutter := fmt.Sprintf("%4d ", startLine+i)
		out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, gutter)+c)
	}
	for _, f := range footer {
		out = append(out, "    "+v.Theme.FG256(v.Theme.Muted, f))
	}
	return out
}

// toolResultBlock renders the live tool-call result body (shown
// while the turn is still in flight). The rules that used to
// bracket this block have been dropped so the live path looks
// identical to the transcript rendering that replaces it when
// the turn ends.
func toolResultBlock(th Theme, text string, width int, color int) []string {
	var out []string
	// The live overlay renders provider/tool text verbatim — including
	// bash progress chunks, which arrive as raw subprocess output. Tabs,
	// carriage returns, and escape sequences in a box row make the
	// terminal draw more (or different) cells than visibleWidth counted,
	// which desyncs the renderer's cursor tracking and corrupts the whole
	// frame. Normalize exactly like the finalised bash path does.
	for _, l := range normalizeBashOutputLines(strings.Split(text, "\n")) {
		for _, w := range wrapLine(l, width-4, "    ") {
			out = append(out, "    "+th.FG256(color, w))
		}
	}
	return out
}

// defaultToolArgWidth is the number of cells the tool-call header
// truncates the primary argument (path/command/query) to when
// TERVA_TOOL_ARG_WIDTH is unset or invalid.
const defaultToolArgWidth = 60

// toolArgWidth returns the maximum cell width for the truncated primary
// argument shown in a tool-call header. It defaults to defaultToolArgWidth
// but can be raised or lowered with the TERVA_TOOL_ARG_WIDTH environment
// variable, useful on wide terminals where the default clips long queries.
// Values outside [20, 500] are ignored so a stray setting can never produce
// an unreadable header or an out-of-range slice.
func toolArgWidth() int {
	v := strings.TrimSpace(os.Getenv("TERVA_TOOL_ARG_WIDTH"))
	if v == "" {
		return defaultToolArgWidth
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 20 || n > 500 {
		return defaultToolArgWidth
	}
	return n
}

// ShortArgs renders a tool call's arguments into a one-line
// suffix for the "tool name <args>" header. tool is the tool
// name so we can add shape-specific decorations: for read we
// append the requested line range (e.g. "path:1-200") pulled
// from the offset/limit args, which is useful context at a
// glance without expanding the result body. Other tools keep
// the legacy "path or command, truncated" shape (default 60 cells,
// tunable via TERVA_TOOL_ARG_WIDTH — see toolArgWidth).
//
// Exported because the interactive mode pre-populates the
// ToolCallView.Args field with this value as soon as the tool
// call is announced, so the live overlay's header matches what
// the finalised transcript will later render.
func ShortArgs(tool string, raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return shortArgsFromParsed(tool, v)
}

// shortArgsFromParsed is ShortArgs over an already-unmarshalled args
// value, so refreshToolPaths can share a single decode across the
// path, offset, and label it derives (see parseToolArgInfo).
func shortArgsFromParsed(tool string, v any) string {
	width := toolArgWidth()
	x, ok := v.(map[string]any)
	if !ok {
		b, _ := json.Marshal(v)
		s := oneLineToolLabel(string(b))
		if len(s) > width {
			s = s[:width-3] + "..."
		}
		return s
	}
	// The one argument worth putting in the header. A tool whose subject is
	// not a file or a command still has one — `raati_convene` spends six
	// sub-agent turns on a QUESTION, and without it here the header rendered
	// as `{"class":"advisory","converge":true,"evidence":"Reposi…`: the whole
	// point of the call, cut off behind the arguments that qualify it.
	var primary string
	for _, k := range []string{"path", "file_path", "command", "question"} {
		if s, ok := x[k].(string); ok {
			primary = s
			break
		}
	}
	if primary == "" {
		b, _ := json.Marshal(v)
		s := oneLineToolLabel(string(b))
		if len(s) > width {
			s = s[:width-3] + "..."
		}
		return s
	}
	primary = oneLineToolLabel(primary)

	// Tool-specific decoration. Only the read tool gets a range
	// suffix for now; other tools just truncate the primary arg.
	suffix := ""
	switch strings.ToLower(tool) {
	case "read":
		start := 1
		if n, ok := toInt(x["offset"]); ok && n >= 1 {
			start = n
		}
		if lim, ok := toInt(x["limit"]); ok && lim > 0 {
			end := start + lim - 1
			suffix = fmt.Sprintf(":%d-%d", start, end)
		} else if start > 1 {
			suffix = fmt.Sprintf(":%d-", start)
		}
	}

	// Truncate the primary arg leaving room for the suffix so the
	// range stays visible even on absurdly long paths.
	max := width - len(suffix)
	if max < 10 {
		max = 10
	}
	if len(primary) > max {
		primary = primary[:max-3] + "..."
	}
	return primary + suffix
}

// toInt coerces a json.Unmarshal'd number (float64) or a string
// containing a number into an int. Returns ok=false if the value
// is neither. Used by shortArgs to survive model quirks where
// numeric args come back as strings.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	}
	return 0, false
}

// renderCompactionBlock renders a compaction checkpoint as a divider in the
// conversation: the turns above it are no longer in the model's context, and
// the summary standing in for them is collapsed behind the rule until ctrl+o.
//
// It renders in BOTH states on purpose. The collapsed arm used to be
// unreachable — renderMessage skipped a compaction message outright — which,
// combined with provider.Message.Meta not surviving the wire, meant a
// carrier-backed TUI drew the summary as an ordinary user bubble full of raw
// "## Context Summary" markdown. A compaction that leaves no mark reads as if
// the conversation simply lost its history.
// transcriptRule draws a horizontal boundary across the chat with its label inset,
// so the eye reads a break in the conversation rather than a stray muted line:
//
//	──── compacted · ~112k tokens ──────────────────────────
//
// fill distinguishes the kinds without needing colour: a solid rule for a compaction
// (the conversation continues, only condensed) and a dashed one for a /clear (it
// does not).
func (v *View) transcriptRule(label string, fill rune, width int) string {
	const indent = "    "
	body := string([]rune{fill, fill}) + " " + label + " "
	if pad := width - len(indent) - runewidth.StringWidth(body); pad > 0 {
		body += strings.Repeat(string(fill), pad)
	}
	return indent + v.Theme.FG256(v.Theme.Muted, body)
}

// renderClearBlock draws the boundary where the conversation was /cleared.
//
// It has to be synthesized, unlike a compaction: a clear is an EMPTY checkpoint and
// leaves no message behind, so without a minted divider the history paged in from
// before one would just run out with nothing to say why. state is "true" while the
// clear still stands between you and what came before, "crossed" once you have said
// /reveal and looked anyway — and it keeps drawing either way, because the point is
// to show where the conversation was cut, not to pretend it wasn't.
func (v *View) renderClearBlock(state string, width int) []string {
	label := i18n.T("conversation cleared")
	if state != "crossed" {
		label += " · " + i18n.T("/reveal to show what came before")
	}
	return []string{v.transcriptRule(label, '╌', width)}
}

func (v *View) renderCompactionBlock(m provider.Message, width int) []string {
	th := v.Theme
	const indent = "    "

	label := i18n.T("compacted")
	if n, err := strconv.Atoi(m.Meta["tokens_before"]); err == nil && n > 0 {
		label = i18n.T("compacted · ~%s tokens", formatTokens(n))
	}
	if !v.ExpandAll {
		label += " · " + i18n.T("ctrl+o to expand")
	}

	if !v.ExpandAll {
		return []string{v.transcriptRule(label, '─', width)}
	}

	lines := []string{v.transcriptRule(label, '─', width), ""}
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			text := tb.Text
			if idx := strings.Index(text, "\n\n"); idx >= 0 && strings.HasPrefix(text, "## Context Summary") {
				text = text[idx+2:]
			}
			md := RenderMarkdown(text, th, width-4)
			for _, l := range strings.Split(md, "\n") {
				if len(l) > 0 && l[0] == FlushLeftSentinel {
					l = l[1:]
				}
				lines = append(lines, indent+l)
			}
		}
	}
	return lines
}

// formatTokens footer formatter:
//
//	< 1000      -> "42"
//	< 10_000    -> "2.7k"
//	< 1_000_000 -> "35k"
//	< 10M       -> "1.1M"
//	else        -> "12M"
func formatTokens(n int) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n < 10_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%dM", (n+500_000)/1_000_000)
	}
}

// shortenHome replaces the user's $HOME prefix with "~" for readability.
func shortenHome(p string) string {
	if p == "" {
		return ""
	}
	home := osUserHome()
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// osUserHome is a tiny wrapper around os.UserHomeDir so tests can mock it.
var osUserHome = func() string {
	if h, err := osUserHomeDir(); err == nil {
		return h
	}
	return ""
}
