package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// SessionInspectTool gives the agent a bounded, filterable view over a session's
// own JSONL transcript — the structured counterpart to the flattened role+text
// an out-of-tree search index sees. It answers "what happened / what went wrong
// in this session" (failed or oversized tool results, by tool) without exposing
// the raw transcript path (which the sandbox jails) or any other file under
// $TERVA_HOME: a session id is resolved inside the active project's sessions
// directory — or, when it names a swarm sub-agent, inside the swarm state root,
// confined to children spawned from this project — and rejected if it tries to
// escape.
type SessionInspectTool struct {
	TervaHome string
	CWD       string
	// Agent is the fallback conversation inspected when the dispatch context
	// carries no agent (direct calls, tests). Bound after the agent is built,
	// with the same ctx-wins semantics as terva_status.
	Agent *core.Agent
}

// sessionInspectArgs is the wire shape. Every optional field here is INERT at
// its zero value, and that is load-bearing rather than incidental.
//
// Cursor and Expand were both *int, and both chose behaviour by PRESENCE: a nil
// Expand listed and a non-nil one expanded; a nil Cursor gave the most recent
// window and `cursor: 0` paged from the oldest. A model that fills every
// optional key in a schema — a common and entirely reasonable habit, since JSON
// Schema gives it no way to see that presence is what matters — could therefore
// not reach list mode at all, and could silently be handed the wrong end of the
// transcript. That is exactly what happened: four rejected calls in a row, each
// carrying {"cursor":0,"expand":0,...}, until the agent gave up and wrote its
// review without the evidence it had gone looking for.
//
// The same root cause had already been diagnosed once (TW-013 F2 named this
// very field) and answered with a better error message, which the model could
// not act on because the correction it asked for was an OMISSION. So the fix
// here is representational, not diagnostic: the indices are 1-based on the
// wire, 0 means "unset", and a fully padded call lands on the sensible default
// instead of an error.
type sessionInspectArgs struct {
	SessionID    string   `json:"session_id"`
	EventKinds   []string `json:"event_kinds"`
	ToolName     string   `json:"tool_name"`
	FailuresOnly bool     `json:"failures_only"`
	Limit        int      `json:"limit"`
	Cursor       int      `json:"cursor"`
	Expand       int      `json:"expand"`
	TextOffset   int      `json:"text_offset"`
}

// The wire indices are 1-based (#1 is the first match) and the scan's are
// 0-based, because every ring and window computation below is an offset. These
// two convert at the boundary, which is the only place the bases meet.
//
// Cursor and Expand must move together: the listing prints an event's #n and
// cursor is an offset into that same match sequence, so the "more: pass cursor
// N" hint round-trips a coordinate a caller reads off a listing line. Rebasing
// one without the other would put an off-by-one straight into that loop.

// wireIndex renders a 0-based scan index as the 1-based coordinate a caller sees.
func wireIndex(idx int) int { return idx + 1 }

// cursorOffset converts a wire cursor to a scan offset. Nil means "the most
// recent window" — the default a caller gets by saying nothing, or by saying 0.
// A negative cursor cannot mean a page before the beginning, so it reads the
// same way rather than erroring on a value with no other sensible meaning.
func cursorOffset(cursor int) *int {
	if cursor <= 0 {
		return nil
	}
	off := cursor - 1
	return &off
}

// expandTarget converts a wire expand to a scan index. Nil is list mode. A
// positive index is 1-based and rebases; a negative one counts from the end and
// is unchanged, so -1 is still the most recent match.
func expandTarget(expand int) *int {
	if expand == 0 {
		return nil
	}
	idx := expand
	if idx > 0 {
		idx--
	}
	return &idx
}

func (t *SessionInspectTool) Name() string { return "session_inspect" }

func (t *SessionInspectTool) Description() string {
	return "Inspect THIS session's transcript in a structured, bounded way — to see what happened without re-reading everything. EVERY argument is optional and 0 means \"not set\", so it is always safe to send them all as zeros: that is the default listing. It has two MUTUALLY EXCLUSIVE modes; filters apply to both: failures_only (only failed/errored tool results), tool_name, or event_kinds ([\"tool_call\",\"tool_result\",\"message\"]). LIST MODE (the default — expand 0): shows a window of matching events (tool calls, tool results with pass/fail, and message text), most recent by default. Page with limit (default 40, cap 200) and cursor (a 1-based position in the matching events, oldest=1; cursor 0 means the most recent window; a next_cursor is returned when more remain — reuse the same filters). Each listed event carries its index (#n, starting at #1). EXPAND MODE (expand non-zero): reads ONE event's full text — pass an #n from a listing (paged with text_offset when long), e.g. a sub-agent's complete findings; negative counts from the end (event_kinds [\"message\"] with expand -1 is the most recent message in full, no listing needed). EXPAND ignores cursor/limit, so do NOT give both a real value — a call that sets expand AND a non-zero cursor/limit is rejected, not silently narrowed. session_id defaults to the current session; pass another id from this project (a filename without .jsonl, as terva_status prints) or a swarm sub-agent id (as swarm_spawn and the [auto-swarm update] recap print) to inspect that transcript. Secrets are redacted and output is size-bounded."
}

func (t *SessionInspectTool) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id":    map[string]any{"type": "string", "description": "Session to inspect (filename without .jsonl), or a swarm sub-agent id spawned from this project. Omit for the current session."},
			"failures_only": map[string]any{"type": "boolean", "description": "Only failed/errored tool results."},
			"tool_name":     map[string]any{"type": "string", "description": "Only events for this tool."},
			"event_kinds": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"tool_call", "tool_result", "message"}},
				"description": "Restrict to these event kinds.",
			},
			"limit":       map[string]any{"type": "integer", "description": "Max events (default 40, cap 200)."},
			"cursor":      map[string]any{"type": "integer", "description": "LIST MODE only. 1-based position in the matching events (oldest = 1). 0 (or omitted) means the most recent window. Do not combine with a non-zero expand (rejected)."},
			"expand":      map[string]any{"type": "integer", "description": "EXPAND MODE. One matching event to read in full: an #n from a listing with the SAME filters (1-based), or negative to count from the end (-1 = most recent match). 0 (or omitted) means list mode instead. Ignores — and must not be combined with — a non-zero limit/cursor (rejected)."},
			"text_offset": map[string]any{"type": "integer", "description": "With expand: byte offset into that event's text (default 0). Use the offset from the previous truncation notice to continue."},
		},
		"additionalProperties": false,
	})
	return b
}

func (t *SessionInspectTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a sessionInspectArgs
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
		}
	}
	if a.Expand < -siExpandTailMax {
		return toolErr(fmt.Sprintf("session_inspect: expand %d is too far back — negative expand reaches at most -%d; use the absolute #n from a listing (cursor pages older events)", a.Expand, siExpandTailMax)), nil
	}
	// EXPAND MODE reads ONE event in full and ignores the listing paging fields,
	// so a call naming a real expand target AND a real cursor/limit is
	// contradictory — the caller put one mode's field into the other. Reject it
	// with both corrected forms rather than silently dropping the listing intent.
	//
	// This no longer fires on a padded call: with 0 meaning "unset" on both
	// fields, {"cursor":0,"expand":0,"limit":0} is now simply the default
	// listing. It fires only when both fields carry a value the caller had to
	// choose, which is the case worth explaining.
	if a.Expand != 0 && (a.Cursor != 0 || a.Limit != 0) {
		return toolErr("session_inspect: EXPAND MODE (expand set) reads one event in full and ignores cursor/limit — do not combine them:\n" +
			"  LIST MODE:   leave expand at 0 — e.g. {\"event_kinds\":[\"tool_result\"],\"cursor\":1,\"limit\":40}\n" +
			"  EXPAND MODE: leave cursor/limit at 0 — e.g. {\"event_kinds\":[\"tool_result\"],\"expand\":1}"), nil
	}
	path, sessID, swarmChild, err := t.resolvePath(ctx, a.SessionID)
	if err != nil {
		return toolErr("session_inspect: " + err.Error()), nil
	}

	// Authorize a swarm child's project ownership BEFORE scanning its payload. A
	// swarm child's transcript lives under the shared swarm root, not the project
	// sessions dir, so the sessions-dir jail can't confine it — and a known
	// cross-project child id must not be able to impose transcript-parsing work
	// before rejection. The child recorded the cwd it was spawned with; only
	// children of THIS project's cwd are inspectable. Reads just the meta row;
	// fails closed on a missing meta (empty cwd ≠ this project).
	if swarmChild {
		childMeta, merr := core.ReadSessionMeta(path)
		if merr != nil {
			return toolErr("session_inspect: could not read the session transcript"), nil
		}
		if core.SessionsDir(t.TervaHome, childMeta.CWD) != core.SessionsDir(t.TervaHome, t.CWD) {
			return toolErr(fmt.Sprintf("session_inspect: swarm sub-agent %q was not spawned from this project", sessID)), nil
		}
	}

	scan := newSessScan(a)
	// One bounded streaming pass: the transcript is never hydrated whole.
	// Retained state is the match count, at most one page of snippets (or the
	// single expand target's text), and the bounded call-id correlations.
	_, scanTrunc, err := streamReplay(ctx, path, siScanCeiling, scan.addMessage)
	if err != nil {
		if ctx.Err() != nil {
			return core.ToolResult{}, ctx.Err() // propagate cancellation, don't bury it
		}
		return toolErr("session_inspect: could not read the session transcript"), nil
	}

	total := scan.total
	// An empty transcript on a swarm child is a timing state, not a filter
	// miss, and reporting it as one sends the caller off re-filtering — which
	// cannot fix it. Since the child now streams its transcript as it works
	// (swarm_agent.go wires WireHeadlessSessionPersist), this window is short:
	// it means the child has not produced its first message yet, or died
	// before it could. Say which, rather than blaming the filters.
	if swarmChild && scan.seen == 0 {
		return toolErr(emptyChildTranscriptMsg(t.TervaHome, sessID)), nil
	}
	if a.Expand != 0 {
		if total == 0 {
			return toolErr("session_inspect: no events match these filters, nothing to expand"), nil
		}
		ev, idx := scan.resolveExpand()
		if ev == nil {
			return toolErr(fmt.Sprintf("session_inspect: expand %d is out of range — %d event(s) match these filters (#1–#%d, or -1 back to -%d)", a.Expand, total, total, total)), nil
		}
		return expandSessionEvent(sessID, *ev, idx, a.TextOffset), nil
	}

	window, start, end := scan.window()
	body := renderSessionEvents(sessID, window, total, start, end, scan.limit)
	if scanTrunc {
		body += scanCeilingNotice
	}
	details := map[string]any{"session_id": sessID, "total": total, "start": wireIndex(start), "count": end - start}
	if end < total {
		details["next_cursor"] = wireIndex(end)
	}
	if scanTrunc {
		details["scan_truncated"] = true
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: body}},
		Details: details,
	}, nil
}

// siScanCeiling caps the transcript bytes one call will scan — a guard against
// a pathological multi-hundred-MiB file, far above any real session. A var so
// tests can lower it without writing a giant fixture.
var siScanCeiling int64 = 64 << 20

// emptyChildTranscriptMsg explains a swarm child whose transcript has nothing
// in it yet. The child's event log is the discriminator: it streams from the
// first thing the child does, so a non-empty log with an empty transcript means
// the child is alive and has simply not finished a message, while an empty log
// means it never got that far.
//
// The recap promise is deliberately conditioned on swarm_spawn rather than
// stated flat: trackSwarmAgent is wired through SwarmSpawnTool.OnSpawned only
// when auto-swarm is on, so a child started from the TUI's /swarm gets no
// [auto-swarm update]. Telling that caller to sit and wait for a push that
// never arrives would trade one dead end for a worse one.
func emptyChildTranscriptMsg(tervaHome, id string) string {
	working := false
	if tervaHome != "" {
		if fi, err := os.Stat(swarm.AgentEventLogPath(swarm.DefaultRoot(tervaHome), id)); err == nil && fi.Size() > 0 {
			working = true
		}
	}
	state := "has not started writing a transcript — it may have failed before its first turn"
	if working {
		state = "is running but has not completed its first message yet"
	}
	return fmt.Sprintf("session_inspect: sub-agent %q %s. A sub-agent streams its transcript as it works, so this is a timing state, not a filter miss — no combination of event_kinds, limit, or cursor changes it, and inspecting again shortly will show whatever it has produced by then. If you spawned it with swarm_spawn you do not need to watch it at all: its findings are pushed to you as an [auto-swarm update] when the task ends.", id, state)
}

// streamReplay is core.StreamReplayMessages behind a package var so a test can
// assert the swarm-child project-authorization gate runs BEFORE any transcript
// scan (the cross-project parsing-DoS the reorder closes).
var streamReplay = core.StreamReplayMessages

const scanCeilingNotice = "note: part of the transcript was skipped — an oversized row, or the 64 MiB scan ceiling was reached; totals and windows may not cover the whole file (some events may be missing)\n"

// siMaxOutstandingCalls bounds the tool-call→name correlations retained during a
// scan. In a healthy transcript a call's result lands in the very next message,
// so at most one turn's parallel calls are ever outstanding — orders of
// magnitude under this cap, which is therefore never reached and never evicts.
// Eviction only ever fires on a transcript with >1024 calls outstanding at once
// (damaged input), where a dropped correlation merely leaves that one result's
// tool name blank — it never misattributes a name.
const siMaxOutstandingCalls = 1024

// siExpandTailMax is the deepest negative expand index (-1 = most recent
// match). It matches the listing page cap, and bounds the full-text tail ring
// a negative expand retains during the scan.
const siExpandTailMax = 200

// sessScan accumulates the bounded state one streaming pass retains. Exactly
// one of the retention modes is active: the latest-N ring / an exact cursor
// page (listing), the single expand target's full text, or — for a negative
// expand, whose target is unknown until the matches are counted — a full-text
// ring of the last |expand| matches.
type sessScan struct {
	kinds    map[string]bool
	tool     string
	failOnly bool

	// callName correlates a tool_result back to its call's tool name. Entries are
	// consumed as results match them, so in a well-formed transcript it holds only
	// the current turn's outstanding calls. A DAMAGED transcript (many calls, no
	// results) would otherwise grow it with the event count, so it is capped at
	// siMaxOutstandingCalls with oldest-first eviction (a bounded recent window).
	// callOrder tracks insertion order for eviction and is compacted of consumed
	// ids so it, too, stays O(cap), not O(call count).
	callName  map[string]string
	callOrder []string

	// seen counts every flattened event BEFORE the filters, so an empty
	// result can be attributed: seen>0 with total==0 means the filters
	// excluded everything, while seen==0 means the transcript itself carried
	// nothing. Only the latter says anything about the session's state.
	seen   int
	total  int
	limit  int
	cursor *int
	expand *int

	ring       []sessEvent // page/ring storage; nil in expand mode
	expandEv   *sessEvent
	expandRing []sessEvent // tail ring for a negative expand; nil otherwise
}

func newSessScan(a sessionInspectArgs) *sessScan {
	limit := a.Limit
	if limit <= 0 {
		limit = 40
	}
	if limit > 200 {
		limit = 200
	}
	s := &sessScan{
		tool:     strings.TrimSpace(a.ToolName),
		failOnly: a.FailuresOnly,
		callName: map[string]string{},
		limit:    limit,
		cursor:   cursorOffset(a.Cursor),
		expand:   expandTarget(a.Expand),
	}
	if len(a.EventKinds) > 0 {
		s.kinds = make(map[string]bool, len(a.EventKinds))
		for _, k := range a.EventKinds {
			s.kinds[k] = true
		}
	}
	return s
}

// addMessage flattens one transcript message into per-block events, feeding
// each through the filters into the bounded retention. The signature matches
// core.StreamReplayMessages' callback.
// noteCall records a tool_call id→name, capped at siMaxOutstandingCalls with
// oldest-first eviction. callOrder is compacted of consumed ids once it carries
// far more than the live set, so retained state is O(cap), not O(call count).
func (s *sessScan) noteCall(id, name string) {
	if id == "" {
		return
	}
	if _, ok := s.callName[id]; ok {
		s.callName[id] = name
		return
	}
	if len(s.callName) >= siMaxOutstandingCalls {
		for len(s.callOrder) > 0 { // evict the oldest still-live id
			oldest := s.callOrder[0]
			s.callOrder = s.callOrder[1:]
			if _, live := s.callName[oldest]; live {
				delete(s.callName, oldest)
				break
			}
		}
	}
	s.callName[id] = name
	s.callOrder = append(s.callOrder, id)
	if len(s.callOrder) > 2*siMaxOutstandingCalls { // drop consumed tombstones, keep order
		live := s.callOrder[:0]
		for _, cid := range s.callOrder {
			if _, ok := s.callName[cid]; ok {
				live = append(live, cid)
			}
		}
		s.callOrder = live
	}
}

// takeCall returns and consumes the recorded name for a result's call id, or ""
// when the call was never seen or its correlation was evicted.
func (s *sessScan) takeCall(id string) string {
	name := s.callName[id]
	delete(s.callName, id)
	return name
}

func (s *sessScan) addMessage(row int, m provider.Message) {
	role := string(m.Role)
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.ToolCallBlock:
			s.noteCall(b.ID, b.Name)
			s.add(sessEvent{Row: row, Kind: "tool_call", Role: role, Tool: b.Name, Bytes: len(b.Arguments), Text: string(b.Arguments)})
		case provider.ToolResultBlock:
			name := s.takeCall(b.CallID)
			txt := flattenResultText(b.Content)
			s.add(sessEvent{Row: row, Kind: "tool_result", Role: role, Tool: name, IsError: b.IsError, Bytes: len(txt), Text: txt})
		case provider.TextBlock:
			if strings.TrimSpace(b.Text) != "" {
				s.add(sessEvent{Row: row, Kind: "message", Role: role, Bytes: len(b.Text), Text: b.Text})
			}
		}
	}
}

// add applies the filters and retains the event only if the active mode needs
// it: the expand target keeps its full text; a listed event keeps only its
// snippet source. Everything else contributes to the count and is dropped.
func (s *sessScan) add(e sessEvent) {
	s.seen++
	if s.kinds != nil && !s.kinds[e.Kind] {
		return
	}
	if s.tool != "" && e.Tool != s.tool {
		return
	}
	if s.failOnly && !(e.Kind == "tool_result" && e.IsError) {
		return
	}
	idx := s.total
	s.total++

	if s.expand != nil {
		if *s.expand >= 0 {
			if idx == *s.expand {
				ev := e
				s.expandEv = &ev
			}
			return
		}
		// Negative index: the target is only known once the matches are
		// counted, so keep the last |expand| matches at full length and let
		// resolveExpand pick one after the scan.
		size := -*s.expand
		if len(s.expandRing) < size {
			s.expandRing = append(s.expandRing, e)
		} else {
			s.expandRing[idx%size] = e
		}
		return
	}

	e.Text = clipSnippetSource(e.Text)
	if s.cursor == nil {
		// Latest-N ring: match idx lands at idx%limit, so the final window
		// [total-len, total) reads back in order without shifting.
		if len(s.ring) < s.limit {
			s.ring = append(s.ring, e)
		} else {
			s.ring[idx%s.limit] = e
		}
		return
	}
	if start := max(*s.cursor, 0); idx >= start && idx < start+s.limit {
		s.ring = append(s.ring, e)
	}
}

// resolveExpand maps the requested expand index to the retained event and its
// absolute #n coordinate, resolving a negative index against the final match
// count (-1 = most recent). Nil when the index falls outside [0, total). The
// absolute coordinate is what the rendered event and its paging hints carry:
// -1 can name a different event once the session grows, #n cannot.
func (s *sessScan) resolveExpand() (*sessEvent, int) {
	idx := *s.expand
	if idx < 0 {
		idx += s.total
	}
	if idx < 0 || idx >= s.total {
		return nil, idx
	}
	if *s.expand >= 0 {
		return s.expandEv, idx
	}
	return &s.expandRing[idx%(-*s.expand)], idx
}

// window assembles the render slice and its [start, end) coordinates from the
// retained page/ring.
func (s *sessScan) window() (events []sessEvent, start, end int) {
	if s.cursor == nil {
		start = s.total - len(s.ring)
		for p := start; p < s.total; p++ {
			events = append(events, s.ring[p%s.limit])
		}
		return events, start, s.total
	}
	start = max(*s.cursor, 0)
	if start > s.total {
		start = s.total
	}
	return s.ring, start, start + len(s.ring)
}

// siSnippetSourceMax bounds the text a listed event retains during the scan.
// It is deliberately larger than the rendered snippet (siSnippetMax) so
// render-time redaction sees well past the display cut — a secret spanning
// the 200-byte boundary still falls inside the redacted window.
const siSnippetSourceMax = 512

func clipSnippetSource(s string) string {
	if len(s) > siSnippetSourceMax {
		return s[:siSnippetSourceMax]
	}
	return s
}

// resolvePath maps a session_id (or the current session) to a transcript path,
// refusing any id that escapes the active project's sessions directory. An id
// that is not a project session but names a swarm sub-agent resolves to that
// child's transcript under the swarm root (swarmChild=true); the caller must
// then enforce the project confinement the sessions-dir jail provides here,
// against the transcript's own recorded cwd.
func (t *SessionInspectTool) resolvePath(ctx context.Context, sessionID string) (path, id string, swarmChild bool, err error) {
	if sessionID != "" {
		if strings.ContainsAny(sessionID, `/\`) || strings.Contains(sessionID, "..") {
			return "", "", false, fmt.Errorf("invalid session_id")
		}
		p := filepath.Join(core.SessionsDir(t.TervaHome, t.CWD), sessionID+".jsonl")
		if _, e := os.Stat(p); e == nil {
			return p, sessionID, false, nil
		}
		if t.TervaHome != "" {
			sp := swarm.AgentSessionPath(swarm.DefaultRoot(t.TervaHome), sessionID)
			if _, e := os.Stat(sp); e == nil {
				return sp, sessionID, true, nil
			}
		}
		return "", "", false, fmt.Errorf("no such session or swarm sub-agent %q in this project — session ids are transcript filenames without .jsonl (terva_status prints the current one); sub-agent ids are what swarm_spawn and the [auto-swarm update] recap print", sessionID)
	}
	ag := core.AgentFromContext(ctx)
	if ag == nil {
		ag = t.Agent
	}
	if ag == nil {
		return "", "", false, fmt.Errorf("no active session to inspect")
	}
	sid, p := ag.SessionIdentity()
	if p == "" {
		return "", "", false, fmt.Errorf("this session has no transcript file (e.g. running with --no-session)")
	}
	return p, sid, false, nil
}

// sessEvent is one flattened, inspectable transcript event.
type sessEvent struct {
	Row     int    // source transcript row (a stable reference, not the cursor)
	Kind    string // tool_call | tool_result | message
	Role    string
	Tool    string // tool name (a call's own, or a result's via call_id correlation)
	IsError bool
	Bytes   int
	Text    string
}

func flattenResultText(blocks []provider.Content) string {
	var b strings.Builder
	for _, c := range blocks {
		if tb, ok := c.(provider.TextBlock); ok && tb.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

const (
	siSnippetMax = 200   // bytes of event text shown per line
	siOutputMax  = 8192  // total rendered body cap (listing mode)
	siExpandMax  = 16384 // bytes of one event's text per expand call
)

func renderSessionEvents(sessID string, window []sessEvent, total, start, end, limit int) string {
	var b strings.Builder
	// The header counted from 1 while the #n lines counted from 0, so "showing
	// 1–5" listed #0–#4. Both are 1-based now, and a caller reading a coordinate
	// off a line gets the one the header promised.
	//
	// Zero matches has no range to show: the old form rendered "showing 1–0",
	// which reads as a bug in the tool rather than as an empty result.
	if total == 0 {
		fmt.Fprintf(&b, "session %s — no events match these filters\n", sessID)
		return b.String()
	}
	fmt.Fprintf(&b, "session %s — %d matching event(s); showing %d–%d\n", sessID, total, wireIndex(start), end)
	for i, e := range window {
		line := fmt.Sprintf("[#%d row %d] %-12s %-16s %-4s %6dB  %s\n", wireIndex(start+i), e.Row, e.Kind, e.Tool, eventStatus(e), e.Bytes, eventSnippet(e.Text))
		if b.Len()+len(line) > siOutputMax {
			b.WriteString("…(output truncated; narrow with filters or a smaller limit)\n")
			return b.String()
		}
		b.WriteString(line)
	}
	if end < total {
		fmt.Fprintf(&b, "more: pass cursor %d (with the same filters) for the next %d\n", wireIndex(end), limit)
	}
	if start > 0 {
		// Floors at the OLDEST page, which is wire cursor 1 — not 0, which now
		// means "unset" and would hand back the most recent window instead of
		// the earliest one the caller asked to walk back to.
		earlier := start - limit
		if earlier < 0 {
			earlier = 0
		}
		fmt.Fprintf(&b, "earlier: pass cursor %d\n", wireIndex(earlier))
	}
	return b.String()
}

func eventStatus(e sessEvent) string {
	if e.Kind != "tool_result" {
		return ""
	}
	if e.IsError {
		return "FAIL"
	}
	return "ok"
}

// expandSessionEvent renders ONE matching event's full redacted text with
// newlines preserved — the recovery path for a long deliverable a 200-byte
// snippet can't carry (e.g. a swarm sub-agent's final report). Bounded per
// call at siExpandMax and paged by textOffset, so even a huge event is read
// in a few explicit, size-known steps. idx addresses the same filtered,
// oldest-first coordinate space cursor uses (the #n each listing line shows);
// the caller resolves it to the one retained event (range-checked there).
func expandSessionEvent(sessID string, e sessEvent, idx, textOffset int) core.ToolResult {
	text := core.RedactSecrets(e.Text)
	if textOffset < 0 {
		textOffset = 0
	}
	if textOffset > len(text) {
		textOffset = len(text)
	}
	chunk := text[textOffset:]
	truncated := len(chunk) > siExpandMax
	if truncated {
		// Cut on a rune boundary WITHOUT dropping bytes (ToValidUTF8 removes
		// them), so textOffset+len(chunk) stays an exact continuation offset.
		cut := siExpandMax
		for cut > 0 && !utf8.RuneStart(chunk[cut]) {
			cut--
		}
		chunk = chunk[:cut]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session %s — event #%d (row %d, %s", sessID, wireIndex(idx), e.Row, e.Kind)
	if e.Tool != "" {
		fmt.Fprintf(&b, " %s", e.Tool)
	}
	if s := eventStatus(e); s != "" {
		fmt.Fprintf(&b, " %s", s)
	}
	fmt.Fprintf(&b, "; bytes %d–%d of %d redacted)\n", textOffset, textOffset+len(chunk), len(text))
	b.WriteString(chunk)
	if truncated {
		fmt.Fprintf(&b, "\n…more: pass expand %d with text_offset %d (same filters)\n", wireIndex(idx), textOffset+len(chunk))
	}
	details := map[string]any{"session_id": sessID, "expand": wireIndex(idx), "text_offset": textOffset, "text_total": len(text)}
	if truncated {
		details["next_text_offset"] = textOffset + len(chunk)
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: b.String()}},
		Details: details,
	}
}

// eventSnippet redacts secrets, collapses whitespace to one line, and bounds
// length on a valid UTF-8 boundary.
func eventSnippet(s string) string {
	s = core.RedactSecrets(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > siSnippetMax {
		s = strings.ToValidUTF8(s[:siSnippetMax], "") + "…"
	}
	return s
}
