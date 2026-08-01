package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// SessionSearchTool searches THIS project's past transcripts — the cross-session
// counterpart to session_inspect, which reads one.
//
// It exists because the out-of-tree alternative could not be made correct. A
// search extension reads transcripts over the protocol-3 bridge
// (build/sessionread.go), which serves messages FLATTENED TO ROLE+TEXT: tool
// calls, their arguments, and their results are dropped before they cross the
// wire. On a measured 690-row coding session that is 2.0% of the searchable
// bytes — 98% discarded, including 106 distinct file paths that appear ONLY in
// tool arguments. "Which session touched candidate.go" is the central question
// of cross-session recall, and it could not be answered at all unless the model
// had happened to type the filename in prose.
//
// Fixing that behind the bridge means changing a published protocol, forcing
// every consumer to re-implement flattening, and negotiating index size across a
// process boundary. In core the question does not arise: this reads the JSONL
// directly, at full fidelity, and bounds at QUERY time instead.
//
// There is deliberately NO INDEX. A full-fidelity linear scan of the largest
// project on the machine that motivated this — 11.9 MB, 4 files, 2,339 messages
// — takes 85ms in Python and is several times faster here; the whole corpus was
// 47 MB across 45 projects. An FTS index would have bought nothing and cost a
// storage-engine dependency (core carries none), an index lifecycle, and the
// ownership/staleness machinery an incremental index drags behind it. Revisit
// only when a scan is actually slow, and then behind this same tool surface so
// nothing the model sees has to change.
type SessionSearchTool struct {
	TervaHome string
	CWD       string
}

func (t *SessionSearchTool) Name() string { return "session_search" }

func (t *SessionSearchTool) Description() string {
	return "Search THIS project's past sessions — and the swarm sub-agents they spawned — for a phrase. Cross-session recall for decisions, file locations, commands, and errors from earlier work, including work that was delegated out. Searches the FULL transcript: message text, tool-call arguments (so a file path, command, or grep pattern from a previous session is findable even when nobody wrote it in prose), and tool-result output. Pass `query`; matching is case-insensitive substring, so prefer a distinctive fragment (a filename, an identifier, an error string) over a sentence. Optional `kinds` narrows to any of [\"message\",\"tool_call\",\"tool_result\"], `session_id` restricts to one session, and `limit` caps hits (default 20, max 100). Results are grouped newest session first and name the session id and row of each hit — pass that session_id to session_inspect to read around it. Secrets are redacted and every snippet is length-bounded. This session's own transcript is included; use session_inspect for a structured view of it instead."
}

func (t *SessionSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "case-insensitive substring to find; prefer a distinctive fragment"},
    "kinds": {"type": "array", "items": {"type": "string", "enum": ["message", "tool_call", "tool_result"]}, "description": "restrict to these event kinds (default: all three)"},
    "session_id": {"type": "string", "description": "restrict to one session (a filename without .jsonl)"},
    "limit": {"type": "integer", "description": "max hits to return (default 20, max 100)"}
  },
  "required": ["query"]
}`)
}

type sessionSearchArgs struct {
	Query     string   `json:"query"`
	Kinds     []string `json:"kinds"`
	SessionID string   `json:"session_id"`
	Limit     int      `json:"limit"`
}

const (
	ssDefaultLimit = 20
	ssMaxLimit     = 100
	// ssSnippetMax bounds one hit's rendered text. Matches session_inspect's
	// listing width so the two tools read alike.
	ssSnippetMax = 200
	// ssCorpusCeiling bounds the TOTAL bytes one call will scan across every
	// session. The per-file ceiling below bounds any single pathological file.
	// Both exist so a corpus that grew while nobody was looking degrades into a
	// truncation notice rather than a hang.
	ssCorpusCeiling int64 = 512 << 20
	ssFileCeiling   int64 = 64 << 20
)

// ssSource is one searchable transcript. It exists instead of reusing
// core.SessionSummary because a summary derives its id from its FILE NAME, and
// every swarm child's transcript is literally named session.json — the id has to
// come from the agent directory, so it is carried explicitly.
type ssSource struct {
	ID       string
	Path     string
	Title    string
	Started  time.Time
	SubAgent bool
}

// ssHit is one match, carrying what a follow-up needs: which session, where in
// it, and enough text to judge relevance without opening it.
type ssHit struct {
	SessionID string
	Title     string
	Started   time.Time
	Row       int
	Kind      string
	Role      string
	Tool      string
	IsError   bool
	SubAgent  bool
	Snippet   string
}

func (t *SessionSearchTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var in sessionSearchArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return toolErr("session_search: invalid arguments: " + err.Error()), nil
		}
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return toolErr(`session_search: provide a non-empty "query" — a distinctive fragment (a filename, an identifier, an error string) finds more than a sentence`), nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = ssDefaultLimit
	}
	if limit > ssMaxLimit {
		limit = ssMaxLimit
	}
	kinds, err := ssParseKinds(in.Kinds)
	if err != nil {
		return toolErr(err.Error()), nil
	}

	// Project-scoped by construction: every path opened comes either from this
	// cwd's sessions directory or from a swarm child this project spawned. There
	// is no path argument to bound, which is why this tool needs none of
	// session_inspect's resolution machinery.
	sources := t.projectSessions()
	sources = append(sources, t.subAgentSessions()...)
	if in.SessionID != "" {
		sources = ssFilterByID(sources, in.SessionID)
		if len(sources) == 0 {
			return toolErr(fmt.Sprintf("session_search: no session %q in this project — pass an id as terva_status prints it, or a sub-agent id, or omit session_id to search them all", in.SessionID)), nil
		}
	}
	if len(sources) == 0 {
		return ssText("session_search: this project has no recorded sessions yet"), nil
	}

	needle := strings.ToLower(query)
	var hits []ssHit
	budget := ssCorpusCeiling
	scanned, truncated := 0, false
	for _, src := range sources {
		if ctx.Err() != nil {
			return core.ToolResult{}, ctx.Err()
		}
		if budget <= 0 {
			truncated = true
			break
		}
		// The remaining corpus budget caps this file, so the LAST session read
		// before exhaustion is partially scanned and reported truncated, rather
		// than the budget being a number nothing enforces.
		fileCap := ssFileCeiling
		if fileCap > budget {
			fileCap = budget
		}
		found, used, cut := ssScanSession(ctx, src, needle, kinds, fileCap)
		budget -= used
		scanned++
		if cut {
			truncated = true
		}
		hits = append(hits, found...)
	}

	// Newest session first, then transcript order within it — reading order for
	// "what did we do most recently about X".
	sort.SliceStable(hits, func(i, j int) bool {
		if !hits[i].Started.Equal(hits[j].Started) {
			return hits[i].Started.After(hits[j].Started)
		}
		if hits[i].SessionID != hits[j].SessionID {
			return hits[i].SessionID > hits[j].SessionID
		}
		return hits[i].Row < hits[j].Row
	})

	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return ssText(ssRender(query, hits, total, scanned, len(sources), truncated)), nil
}

// projectSessions lists this cwd's own transcripts, newest first.
func (t *SessionSearchTool) projectSessions() []ssSource {
	summaries := core.DescribeSessions(t.TervaHome, t.CWD)
	out := make([]ssSource, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, ssSource{
			ID:      core.SessionIDFromPath(s.Path),
			Path:    s.Path,
			Title:   s.Title,
			Started: s.Started,
		})
	}
	return out
}

// subAgentSessions returns the transcripts of swarm children THIS project
// spawned, so a search covers the work delegated out of a session as well as the
// session itself.
//
// This is the half an out-of-tree search extension could not reach at all: a
// child's transcript lives under the swarm state root, not the project sessions
// directory, and the protocol-3 bridge only ever lists the latter. Delegated
// work is where a coordinator's most expensive findings end up — one measured
// run spent $24.49 through sub-agents against $5.36 of its own turns — so a
// recall tool blind to it misses the answer precisely when the answer was
// expensive to produce.
//
// Live agents only. Archived records are unreachable from terva by standing
// rule (swarm/archive.go); reaching them here is the erosion that rule names.
//
// A child is included only when its SPAWN RECORD names this project — the same
// predicate session_inspect authorizes single-id reads with, so enumeration and
// authorization cannot disagree.
func (t *SessionSearchTool) subAgentSessions() []ssSource {
	root := swarm.DefaultRoot(t.TervaHome)
	origins := swarm.AgentIDsWithOrigin(root)
	if len(origins) == 0 {
		return nil
	}
	ids := make([]string, 0, len(origins))
	for id, origin := range origins {
		if swarmChildFromProject(t.TervaHome, t.CWD, origin) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]ssSource, 0, len(ids))
	for _, id := range ids {
		path := swarm.AgentSessionPath(root, id)
		fi, err := os.Stat(path)
		if err != nil || fi.Size() == 0 {
			continue // spawned but not yet writing, or already cleaned up
		}
		// mtime, not a parsed Started: the file is about to be streamed in full
		// for the search itself, and re-reading it here to recover a title would
		// double the scan for a label.
		out = append(out, ssSource{
			ID:       id,
			Path:     path,
			Title:    "sub-agent spawned by this project",
			Started:  fi.ModTime(),
			SubAgent: true,
		})
	}
	return out
}

// ssParseKinds validates the kind filter. An unknown kind is refused rather than
// silently ignored: a filter that quietly matches nothing reads as "no results",
// which is the one answer a search tool must never fake.
func ssParseKinds(in []string) (map[string]bool, error) {
	if len(in) == 0 {
		return nil, nil
	}
	valid := map[string]bool{"message": true, "tool_call": true, "tool_result": true}
	out := map[string]bool{}
	for _, k := range in {
		k = strings.TrimSpace(strings.ToLower(k))
		if !valid[k] {
			return nil, fmt.Errorf("session_search: unknown kind %q — valid kinds are message, tool_call, tool_result", k)
		}
		out[k] = true
	}
	return out, nil
}

// ssText wraps rendered output as a successful tool result.
func ssText(msg string) core.ToolResult {
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: msg}}}
}

// ssFileSize reports what a transcript cost the corpus budget. Errors count as
// zero rather than aborting: the budget is a guard, not an accounting record.
func ssFileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func ssFilterByID(all []ssSource, id string) []ssSource {
	id = strings.TrimSuffix(strings.TrimSpace(id), ".jsonl")
	for _, s := range all {
		if s.ID == id {
			return []ssSource{s}
		}
	}
	return nil
}

// ssScanSession streams one transcript and collects matches. Returns the hits,
// the bytes it was allowed to read, and whether the scan was cut short.
//
// The three event kinds mirror session_inspect's addMessage exactly — the same
// flattening, so a hit here is expandable there — and they are the whole point:
// tool_call carries the arguments (paths, commands, patterns) and tool_result
// the output, neither of which survives the extension bridge.
func ssScanSession(ctx context.Context, s ssSource, needle string, kinds map[string]bool, maxBytes int64) ([]ssHit, int64, bool) {
	var hits []ssHit
	consider := func(row int, kind, role, tool string, isErr bool, text string) {
		if kinds != nil && !kinds[kind] {
			return
		}
		if text == "" || !strings.Contains(strings.ToLower(text), needle) {
			return
		}
		hits = append(hits, ssHit{
			SessionID: s.ID,
			Title:     s.Title,
			Started:   s.Started,
			SubAgent:  s.SubAgent,
			Row:       row,
			Kind:      kind,
			Role:      role,
			Tool:      tool,
			IsError:   isErr,
			Snippet:   ssSnippet(text, needle),
		})
	}
	calls := map[string]string{}
	_, cut, err := core.StreamReplayRows(ctx, s.Path, maxBytes, func(row int, r core.ReplayRow) {
		if r.Kind != core.ReplayRowMessage {
			return
		}
		role := string(r.Message.Role)
		for _, c := range r.Message.Content {
			switch b := c.(type) {
			case provider.ToolCallBlock:
				if b.ID != "" {
					calls[b.ID] = b.Name
				}
				consider(row, "tool_call", role, b.Name, false, string(b.Arguments))
			case provider.ToolResultBlock:
				name := calls[b.CallID]
				delete(calls, b.CallID)
				consider(row, "tool_result", role, name, b.IsError, flattenResultText(b.Content))
			case provider.TextBlock:
				consider(row, "message", role, "", false, b.Text)
			}
		}
	})
	if err != nil {
		// An unreadable session is skipped, not fatal: one corrupt file must not
		// deny the model every other session's answer.
		return nil, 0, true
	}
	return hits, ssFileSize(s.Path), cut
}

// ssSnippet centres a bounded window on the FIRST match, so the returned text
// contains the thing that was searched for. A leading-bytes snippet would show
// the head of a 40KB tool result and omit the match entirely, which is how a
// search tool ends up reporting hits nobody can see.
//
// Redaction runs on the WIDER window before the cut, so a secret straddling the
// boundary is still masked (the rule session_inspect follows for the same
// reason).
func ssSnippet(text, needle string) string {
	i := strings.Index(strings.ToLower(text), needle)
	if i < 0 {
		i = 0
	}
	start := i - ssSnippetMax/3
	if start < 0 {
		start = 0
	}
	end := start + ssSnippetMax
	if end > len(text) {
		end = len(text)
	}
	// Widen before redacting, then cut back — see above.
	wide := text[ssBackTo(text, start, 64):ssForwardTo(text, end, 64)]
	red := core.RedactSecrets(wide)
	if len(red) > ssSnippetMax {
		red = red[:ssSnippetMax]
	}
	red = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(red, "\r", " "), "\n", " "))
	var out strings.Builder
	if start > 0 {
		out.WriteString("…")
	}
	out.WriteString(red)
	if end < len(text) {
		out.WriteString("…")
	}
	return out.String()
}

func ssBackTo(s string, i, n int) int {
	if i-n < 0 {
		return 0
	}
	return i - n
}

func ssForwardTo(s string, i, n int) int {
	if i+n > len(s) {
		return len(s)
	}
	return i + n
}

func ssRender(query string, hits []ssHit, total, scanned, available int, truncated bool) string {
	var b strings.Builder
	if total == 0 {
		fmt.Fprintf(&b, "session_search: no match for %q across %d session(s) in this project.\n", query, scanned)
		b.WriteString("The whole transcript is searched — message text, tool-call arguments, and tool results — so a miss means the phrase is genuinely absent. Try a shorter or more distinctive fragment.\n")
		if truncated {
			b.WriteString("⚠️ the scan hit its size ceiling, so older sessions may not have been read.\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "session_search: %d match(es) for %q across %d of %d session(s)", total, query, scanned, available)
	if len(hits) < total {
		fmt.Fprintf(&b, " — showing the %d most recent", len(hits))
	}
	b.WriteString("\n")
	if truncated {
		b.WriteString("⚠️ the scan hit its size ceiling; older sessions may be missing.\n")
	}

	lastSession := ""
	for _, h := range hits {
		if h.SessionID != lastSession {
			lastSession = h.SessionID
			title := strings.TrimSpace(h.Title)
			if title == "" {
				title = "(untitled)"
			}
			b.WriteString("\n")
			// Sub-agent hits are marked because the provenance changes what the
			// text means: a finding in a child's transcript was produced by
			// delegated work this session commissioned, not by this session.
			if h.SubAgent {
				fmt.Fprintf(&b, "[sub-agent] %s — %s", h.SessionID, title)
			} else {
				fmt.Fprintf(&b, "%s — %s", h.SessionID, title)
			}
			if !h.Started.IsZero() {
				fmt.Fprintf(&b, " (%s)", h.Started.Format("2006-01-02 15:04"))
			}
			b.WriteString("\n")
		}
		label := h.Kind
		if h.Tool != "" {
			label = h.Kind + ":" + h.Tool
		}
		if h.IsError {
			label += " FAILED"
		}
		fmt.Fprintf(&b, "  row %d  %-22s %s\n", h.Row, label, h.Snippet)
	}
	b.WriteString("\nPass a session_id above to session_inspect to read around a hit.\n")
	return b.String()
}
