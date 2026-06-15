package core

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"terva.sh/terva/packages/provider"
)

// Session is a JSONL-backed conversation transcript tied to a cwd.
type Session struct {
	ID     string
	Path   string
	Meta   SessionMeta
	writer *os.File
	buf    *bufio.Writer

	// freshFile is true when the file was created by NewSession (this
	// process owns it) and false when OpenSession reopened an existing
	// transcript. Used by Close() to delete the file if the run never
	// appended any messages — prevents a flood of empty session files
	// from sessions the user opens then exits without prompting.
	freshFile bool

	// messagesAppended counts AppendMessage calls. Combined with
	// freshFile it tells Close() whether the session left any content
	// worth keeping.
	messagesAppended int

	// LoadWarnings describes everything OpenSession had to skip or
	// guess at while reading the file (corrupt rows, unknown block
	// types, a newer format version). Empty for clean loads. Callers
	// decide how to surface it; the data is never silently dropped.
	LoadWarnings []string
}

// sessionFormatVersion is the version of the on-disk session schema
// THIS build writes. History:
//
//	1 (implicit, format_version absent) — content blocks carry no
//	  type discriminator; readers classify by field presence.
//	2 — every content block is written with an explicit "type"
//	  ("text", "image", "tool_call", "tool_result", "reasoning").
//	  v1 files keep loading through the field-presence fallback.
//
// Readers warn (Session.LoadWarnings) when a file declares a NEWER
// version than this and load best-effort.
const sessionFormatVersion = 2

// SessionMeta is written as the first line of every session file.
type SessionMeta struct {
	ID       string    `json:"id"`
	CWD      string    `json:"cwd"`
	Model    string    `json:"model"`
	Provider string    `json:"provider"`
	Started  time.Time `json:"started"`
	// Version is the app version that created the session —
	// informational only. FormatVersion is the schema contract.
	Version string `json:"version"`
	// FormatVersion is the session-schema version (sessionFormatVersion
	// at write time). 0 means a legacy v1 file.
	FormatVersion int    `json:"format_version,omitempty"`
	Title         string `json:"title,omitempty"`

	// Parent is the ID of the session this one was forked from, or
	// empty for top-level sessions. The tree picker walks parents
	// upward and sibling files (same cwd dir, same parent ID)
	// laterally to render the branch topology.
	Parent string `json:"parent,omitempty"`
	// ForkPoint is the 0-indexed message position within the parent
	// transcript where this branch diverges. Messages 0..ForkPoint-1
	// are copied from the parent verbatim; the user's next turn on
	// the child session continues from there.
	ForkPoint int `json:"fork_point,omitempty"`
}

// sessionLine is the on-disk row type. Messages are written in the
// typed wire form (wireMessage) so every content block carries a
// "type" discriminator; reads go through hydrateMessageObject, which
// prefers the discriminator and falls back to field presence for v1
// files.
type sessionLine struct {
	Type       string          `json:"type"`
	Meta       *SessionMeta    `json:"meta,omitempty"`
	Message    *wireMessage    `json:"message,omitempty"`
	Messages   []wireMessage   `json:"messages,omitempty"`
	Usage      *provider.Usage `json:"usage,omitempty"`
	Cumulative *provider.Usage `json:"cumulative,omitempty"`
}

type sessionLineHead struct {
	Type string `json:"type"`
}

// wireMessage is the typed on-disk form of provider.Message. The
// outer shape (role/content/time/meta) is identical to v1; only the
// blocks gain a "type" field, so v1 readers (field presence, unknown
// fields ignored) read v2 files and vice versa.
type wireMessage struct {
	Role    provider.Role     `json:"role"`
	Content []wireBlock       `json:"content"`
	Time    time.Time         `json:"time"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// wireBlock is one typed content block. One flat struct (rather than
// per-kind types) keeps encoding/decoding a single switch; omitempty
// keeps each kind's row as small as v1's.
type wireBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	MimeType string `json:"mime_type,omitempty"`
	Data     []byte `json:"data,omitempty"`
	// tool_call
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// tool_result (Content nests text/image blocks)
	CallID  string      `json:"call_id,omitempty"`
	Content []wireBlock `json:"content,omitempty"`
	IsError bool        `json:"is_error,omitempty"`
	// reasoning
	ReasoningID string `json:"reasoning_id,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Encrypted   string `json:"encrypted_content,omitempty"`
}

// Block type discriminator values (wireBlock.Type).
const (
	blockText       = "text"
	blockImage      = "image"
	blockToolCall   = "tool_call"
	blockToolResult = "tool_result"
	blockReasoning  = "reasoning"
)

// encodeWireMessage converts a provider.Message to its typed on-disk
// form. Unknown in-memory block kinds are impossible today (Content
// is a closed set); if one appears it is dropped here at write time,
// which is loud in tests rather than silent at read time.
func encodeWireMessage(m provider.Message) wireMessage {
	w := wireMessage{Role: m.Role, Time: m.Time, Meta: m.Meta}
	w.Content = encodeWireBlocks(m.Content)
	return w
}

func encodeWireBlocks(blocks []provider.Content) []wireBlock {
	out := make([]wireBlock, 0, len(blocks))
	for _, c := range blocks {
		switch b := c.(type) {
		case provider.TextBlock:
			out = append(out, wireBlock{Type: blockText, Text: b.Text})
		case provider.ImageBlock:
			out = append(out, wireBlock{Type: blockImage, MimeType: b.MimeType, Data: b.Data})
		case provider.ToolCallBlock:
			out = append(out, wireBlock{Type: blockToolCall, ID: b.ID, Name: b.Name, Arguments: b.Arguments})
		case provider.ToolResultBlock:
			out = append(out, wireBlock{
				Type:    blockToolResult,
				CallID:  b.CallID,
				Content: encodeWireBlocks(b.Content),
				IsError: b.IsError,
			})
		case provider.ReasoningBlock:
			out = append(out, wireBlock{Type: blockReasoning, ReasoningID: b.ID, Summary: b.Summary, Encrypted: b.Encrypted})
		}
	}
	return out
}

// CWDHash is the stable short hash of a working directory used to key
// per-cwd storage. It is exported so other per-project storage (e.g. an
// extension's data dir) can reuse the exact value SessionsDir buckets
// by, making the two correlate by eye. Pass an absolute cwd.
func CWDHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:8])
}

// SessionsDir returns the per-cwd sessions directory under root.
func SessionsDir(root, cwd string) string {
	return filepath.Join(root, "sessions", CWDHash(cwd))
}

// ProjectKey is a human-readable, collision-proof identifier for a
// working directory: the path flattened for readability, plus CWDHash as
// the disambiguator. The readable prefix is lossy on its own (two
// distinct paths can flatten the same), so the trailing hash is what
// guarantees uniqueness — which lets the prefix be freely collapsed and
// truncated.
//
// The key is computed from cwd verbatim (no absolutization), so its hash
// is byte-for-byte the cwd's SessionsDir bucket name and the two
// correlate. Pass the same absolute cwd you pass elsewhere (sessions
// already do); absolutizing here would both diverge from SessionsDir and
// graft the platform's volume (a Windows drive letter) into the key.
func ProjectKey(cwd string) string {
	slug := projectSlug(cwd)
	hash := CWDHash(cwd)
	if slug == "" {
		return hash
	}
	return slug + "-" + hash
}

// projectSlug flattens a path into a readable, filesystem-safe token:
// path separators become '-', any other non-alphanumeric becomes '_',
// and a run of separators collapses to the first one (so "a//b" -> "a-b",
// "a__b" -> "a_b"). Leading/trailing separators are stripped — a
// leading '-' would make the dir name look like a CLI flag — and the
// result is capped, tail-biased so the most specific path components
// survive (the hash in ProjectKey carries correctness, so truncation is
// purely cosmetic).
func projectSlug(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	prevSep := false
	for _, r := range p {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevSep = false
		case r == '/' || r == '\\':
			if !prevSep {
				b.WriteByte('-')
				prevSep = true
			}
		default:
			if !prevSep {
				b.WriteByte('_')
				prevSep = true
			}
		}
	}
	s := strings.Trim(b.String(), "-_")
	const maxLen = 80
	if len(s) > maxLen {
		s = strings.TrimLeft(s[len(s)-maxLen:], "-_")
	}
	return s
}

// NewSession creates and opens a new session file under
// SessionsDir(root, cwd) with an autogenerated, time-stamped name.
func NewSession(root, cwd, providerName, model, version string) (*Session, error) {
	dir := SessionsDir(root, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102-150405"), id[:8])
	p := filepath.Join(dir, name)
	return newSessionAt(p, cwd, providerName, model, version)
}

// NewSessionAtPath creates a session at an explicit file path. Used
// by callers (notably the swarm-agent child) that need the session
// file to live at a path chosen by their parent rather than under
// SessionsDir. Returns an error if the file already exists — use
// OpenSession for that case.
func NewSessionAtPath(path, cwd, providerName, model, version string) (*Session, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return newSessionAt(path, cwd, providerName, model, version)
}

// newSessionAt is the shared implementation. Both NewSession and
// NewSessionAtPath funnel through here so the meta-line layout,
// freshFile bookkeeping, and id format stay identical.
func newSessionAt(p, cwd, providerName, model, version string) (*Session, error) {
	id := uuid.NewString()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Session{
		ID:        id,
		Path:      p,
		Meta:      SessionMeta{ID: id, CWD: cwd, Provider: providerName, Model: model, Started: time.Now().UTC(), Version: version, FormatVersion: sessionFormatVersion},
		writer:    f,
		buf:       bufio.NewWriter(f),
		freshFile: true,
	}
	if err := s.writeLine(sessionLine{Type: "meta", Meta: &s.Meta}); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func forEachJSONLLine(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > 0 {
				if ferr := fn(line); ferr != nil {
					return ferr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// SessionUsage returns the most recent cumulative usage row stored in
// a session file. Sessions append one usage row per completed turn; the
// latest row's cumulative field is the session total. Missing usage rows
// are valid for old/empty sessions and return the zero value.
func SessionUsage(path string) (provider.Usage, error) {
	cum, _, err := SessionUsageDetail(path)
	return cum, err
}

// SessionUsageDetail returns the latest cumulative usage and the
// per-turn usage of the final completed turn. The per-turn row drives
// the live "context used" gauge in the status bar (input + cache
// approximates the prompt size the model just saw), letting the TUI
// rehydrate the gauge on resume instead of starting at 0% until the
// next turn lands.
func SessionUsageDetail(path string) (cumulative, lastTurn provider.Usage, err error) {
	f, ferr := os.Open(path)
	if ferr != nil {
		return provider.Usage{}, provider.Usage{}, ferr
	}
	defer f.Close()

	// Some historical sessions logged the per-turn `usage` field as a copy
	// of `cumulative` instead of the true delta. To recover an accurate
	// last-turn snapshot (used by the status-bar context gauge on resume),
	// we always derive lastTurn from the delta between the final two
	// cumulative rows. For prompt-size purposes, cache_read/cache_write
	// reflect the most recent prompt directly, so we take those from the
	// final cumulative row as-is rather than as a delta.
	var prevCum provider.Usage
	var haveCum bool
	if ierr := forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil || head.Type != "usage" {
			return nil
		}
		var row struct {
			Cumulative provider.Usage `json:"cumulative"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return nil
		}
		if haveCum {
			prevCum = cumulative
		}
		cumulative = row.Cumulative
		haveCum = true
		return nil
	}); ierr != nil {
		return provider.Usage{}, provider.Usage{}, ierr
	}
	if haveCum {
		// input/output are monotonic totals -> per-turn = delta.
		lastTurn.InputTokens = nonNegDelta(cumulative.InputTokens, prevCum.InputTokens)
		lastTurn.OutputTokens = nonNegDelta(cumulative.OutputTokens, prevCum.OutputTokens)
		// cache_read/write on the final row already represent the last prompt's
		// cache hit/creation, not a running total of bytes; use directly.
		lastTurn.CacheReadTokens = cumulative.CacheReadTokens - prevCum.CacheReadTokens
		if lastTurn.CacheReadTokens < 0 {
			lastTurn.CacheReadTokens = cumulative.CacheReadTokens
		}
		lastTurn.CacheWriteTokens = cumulative.CacheWriteTokens - prevCum.CacheWriteTokens
		if lastTurn.CacheWriteTokens < 0 {
			lastTurn.CacheWriteTokens = cumulative.CacheWriteTokens
		}
		lastTurn.CostUSD = cumulative.CostUSD - prevCum.CostUSD
		if lastTurn.CostUSD < 0 {
			lastTurn.CostUSD = 0
		}
	}
	return cumulative, lastTurn, nil
}

func nonNegDelta(cur, prev int) int {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// OpenSession opens an existing session for appending.
func OpenSession(path string) (*Session, []provider.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var meta SessionMeta
	var messages []provider.Message
	rep := &loadReport{}
	if err := forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			rep.corruptLines++
			return nil
		}
		switch head.Type {
		case "meta":
			var row struct {
				Meta SessionMeta `json:"meta"`
			}
			if err := json.Unmarshal(line, &row); err == nil {
				meta = row.Meta
			} else {
				rep.corruptLines++
			}
		case "message":
			msg, err := hydrateMessage(line, rep)
			if err != nil {
				rep.corruptLines++
				return nil
			}
			if len(msg.Content) > 0 {
				messages = append(messages, msg)
			}
		case "compaction":
			if compacted, err := hydrateCompaction(line, rep); err == nil {
				messages = compacted
			} else {
				rep.corruptLines++
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if meta.FormatVersion > sessionFormatVersion {
		rep.newerFormat = meta.FormatVersion
	}
	messages = repairToolUseResultPairs(messages)
	out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	s := &Session{ID: meta.ID, Path: path, Meta: meta, writer: out, buf: bufio.NewWriter(out), LoadWarnings: rep.warnings(path)}
	return s, messages, nil
}

// repairToolUseResultPairs walks a restored transcript and
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
func repairToolUseResultPairs(msgs []provider.Message) []provider.Message {
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
				Content: []provider.Content{provider.TextBlock{Text: "tool call was aborted; no result recorded."}},
				IsError: true,
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

// LatestSession returns the most recent session file for cwd, or "".
func LatestSession(root, cwd string) string {
	paths := ListSessions(root, cwd)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// SessionSummary describes one on-disk session at a glance for UI pickers.
type SessionSummary struct {
	Path          string
	Started       time.Time
	Model         string
	Provider      string
	MessageCount  int
	FirstUserText string
	TotalCost     float64
	Title         string
}

// RenameSession updates the title field in the session's meta line.
// It rewrites the first line of the file (the meta line) with the
// updated title.
// RenameSession appends a rename line to the session file. This is
// safe even for the currently active session because it opens the
// file independently and appends (doesn't rewrite).
func RenameSession(path, title string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, _ := json.Marshal(map[string]string{"type": "rename", "title": title})
	line = append(line, '\n')
	_, err = f.Write(line)
	return err
}

// DescribeSessions returns lightweight summaries for every session in
// cwd, newest first. Parses only the first few lines and the last usage
// line so it's cheap to run on every dialog open.
func DescribeSessions(root, cwd string) []SessionSummary {
	paths := ListSessions(root, cwd)
	summaries := make([]SessionSummary, 0, len(paths))
	for _, p := range paths {
		summaries = append(summaries, describeSession(p))
	}
	return summaries
}

func describeSession(path string) SessionSummary {
	s := SessionSummary{Path: path}
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	_ = forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		switch head.Type {
		case "meta":
			var row struct {
				Meta SessionMeta `json:"meta"`
			}
			if err := json.Unmarshal(line, &row); err == nil {
				s.Started = row.Meta.Started
				s.Model = row.Meta.Model
				s.Provider = row.Meta.Provider
				s.Title = row.Meta.Title
			}
		case "message":
			s.MessageCount++
			if s.FirstUserText == "" {
				s.FirstUserText = firstUserText(line)
			}
		case "compaction":
			if compacted, err := hydrateCompaction(line, nil); err == nil {
				s.MessageCount = len(compacted)
				if s.FirstUserText == "" && len(compacted) > 0 {
					s.FirstUserText = firstTextFromMessage(compacted[0])
				}
			}
		case "rename":
			var row struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(line, &row); err == nil && row.Title != "" {
				s.Title = row.Title
			}
		case "usage":
			var row struct {
				Cumulative provider.Usage `json:"cumulative"`
			}
			if err := json.Unmarshal(line, &row); err == nil {
				s.TotalCost = row.Cumulative.CostUSD
			}
		}
		return nil
	})
	return s
}

func firstUserText(line []byte) string {
	var row struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return ""
	}
	if row.Message.Role != "user" {
		return ""
	}
	for _, c := range row.Message.Content {
		if c.Text != "" {
			return c.Text
		}
	}
	return ""
}

func firstTextFromMessage(msg provider.Message) string {
	for _, c := range msg.Content {
		if tb, ok := c.(provider.TextBlock); ok && tb.Text != "" {
			return tb.Text
		}
	}
	return ""
}

// PruneEmptySessions deletes session files in cwd's session directory
// that contain only a meta line (no messages were ever appended).
// Cleans up the backlog of empty stubs created by old terva versions
// that wrote a meta line at NewSession time and never followed up.
// Errors are swallowed; the caller treats this as best-effort.
func PruneEmptySessions(root, cwd string) {
	dir := SessionsDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if sessionHasNoMessages(p) {
			_ = os.Remove(p)
		}
	}
}

// sessionHasNoMessages returns true when the file at path contains
// no lines of type "message". Meta-only / usage-only files count as
// empty. Used by PruneEmptySessions and the Describe path.
func sessionHasNoMessages(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hasMessage := false
	_ = forEachJSONLLine(f, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		if head.Type == "message" {
			hasMessage = true
			return io.EOF
		}
		return nil
	})
	return !hasMessage
}

// ListSessions returns session file paths for cwd, most-recently-
// modified first. Sorting on filesystem ModTime instead of the
// timestamp embedded in the filename means a long-running session
// the user actually returned to recently floats to the top of
// /sessions, /continue, and the resume picker, even when it was
// originally created days earlier than newer but idle sessions.
// Files with identical ModTime fall back to filename desc so the
// order stays stable across calls.
func ListSessions(root, cwd string) []string {
	dir := SessionsDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type rec struct {
		path string
		mod  time.Time
	}
	var files []rec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, rec{path: p, mod: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.After(files[j].mod)
		}
		return files[i].path > files[j].path
	})
	out := make([]string, 0, len(files))
	for _, r := range files {
		out = append(out, r.path)
	}
	return out
}

// AppendMessage writes a message to the session.
func (s *Session) AppendMessage(m provider.Message) error {
	if s == nil {
		return nil
	}
	w := encodeWireMessage(m)
	if err := s.writeLine(sessionLine{Type: "message", Message: &w}); err != nil {
		return err
	}
	s.messagesAppended++
	return nil
}

// AppendCompaction writes a checkpoint that replaces all earlier
// transcript rows when the session is resumed. The old rows remain in
// the JSONL file for audit/export, while loaders use the latest
// compaction row as the effective transcript.
func (s *Session) AppendCompaction(messages []provider.Message) error {
	if s == nil {
		return nil
	}
	wires := make([]wireMessage, 0, len(messages))
	for _, m := range messages {
		wires = append(wires, encodeWireMessage(m))
	}
	if err := s.writeLine(sessionLine{Type: "compaction", Messages: wires}); err != nil {
		return err
	}
	s.messagesAppended = len(messages)
	return nil
}

// UpdateModel records a provider/model switch in the session file.
// The reader keeps the most recent meta entry, so the session resumes
// with the updated model.
func (s *Session) UpdateModel(providerName, model string) error {
	if s == nil {
		return nil
	}
	s.Meta.Provider = providerName
	s.Meta.Model = model
	return s.writeLine(sessionLine{Type: "meta", Meta: &s.Meta})
}

// AppendUsage writes a usage row to the session.
func (s *Session) AppendUsage(u, cum provider.Usage) error {
	if s == nil {
		return nil
	}
	return s.writeLine(sessionLine{Type: "usage", Usage: &u, Cumulative: &cum})
}

// Close flushes and closes the session file. If the session was
// freshly created in this process and never had any messages
// appended (the user opened terva, looked around, and exited without
// prompting), the file is deleted on close so the sessions list
// doesn't fill up with empty meta-only stubs.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	flushErr := s.buf.Flush()
	closeErr := s.writer.Close()
	if s.freshFile && s.messagesAppended == 0 {
		// Best-effort cleanup. We deliberately don't propagate the
		// remove error: if it fails (file already gone, perms changed)
		// the worst case is one stale empty file in the listing.
		_ = os.Remove(s.Path)
	}
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (s *Session) writeLine(row sessionLine) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if _, err := s.buf.Write(b); err != nil {
		return err
	}
	if err := s.buf.WriteByte('\n'); err != nil {
		return err
	}
	return s.buf.Flush()
}

// ---- content (de)serialization ----
//
// provider.Content is an interface; encoding/json drops type
// information. v2 files carry an explicit "type" on every block
// (wireBlock); v1 files are rebuilt from discriminated fields. Both
// paths run through hydrateMessageObject, which reports (not
// swallows) corrupt and unknown blocks via loadReport.

// loadReport accumulates everything OpenSession had to skip or guess
// at, so callers can surface it instead of silently losing data.
type loadReport struct {
	corruptLines  int            // whole rows that failed to parse
	corruptBlocks int            // content blocks that failed to parse
	unknownBlocks map[string]int // typed blocks with an unrecognized type
	newerFormat   int            // file's format_version when > ours
}

func (r *loadReport) noteUnknown(blockType string) {
	if r == nil {
		return
	}
	if r.unknownBlocks == nil {
		r.unknownBlocks = make(map[string]int)
	}
	r.unknownBlocks[blockType]++
}

func (r *loadReport) noteCorruptBlock() {
	if r != nil {
		r.corruptBlocks++
	}
}

// warnings renders the report as human-readable lines, empty when
// nothing was skipped.
func (r *loadReport) warnings(path string) []string {
	if r == nil {
		return nil
	}
	var out []string
	base := filepath.Base(path)
	if r.newerFormat > 0 {
		out = append(out, fmt.Sprintf("session %s: written by a newer terva (format v%d, this build reads v%d); loaded best-effort", base, r.newerFormat, sessionFormatVersion))
	}
	if r.corruptLines > 0 {
		out = append(out, fmt.Sprintf("session %s: %d corrupt line(s) skipped", base, r.corruptLines))
	}
	if r.corruptBlocks > 0 {
		out = append(out, fmt.Sprintf("session %s: %d corrupt content block(s) skipped", base, r.corruptBlocks))
	}
	for typ, n := range r.unknownBlocks {
		out = append(out, fmt.Sprintf("session %s: %d block(s) of unknown type %q skipped", base, n, typ))
	}
	return out
}

func hydrateCompaction(lineBytes []byte, rep *loadReport) ([]provider.Message, error) {
	var row struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(lineBytes, &row); err != nil {
		return nil, err
	}
	messages := make([]provider.Message, 0, len(row.Messages))
	for _, raw := range row.Messages {
		msg, err := hydrateMessageObject(raw, rep)
		if err != nil {
			rep.noteCorruptBlock()
			continue
		}
		if len(msg.Content) > 0 {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}

func hydrateMessage(lineBytes []byte, rep *loadReport) (provider.Message, error) {
	var row struct {
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(lineBytes, &row); err != nil {
		return provider.Message{}, err
	}
	return hydrateMessageObject(row.Message, rep)
}

// decodeWireBlock rebuilds one v2 typed block. ok=false means the
// type is unrecognized (written by a newer terva) — the caller records
// it and skips, rather than degrading it to an empty text block.
func decodeWireBlock(b wireBlock) (provider.Content, bool) {
	switch b.Type {
	case blockText:
		return provider.TextBlock{Text: b.Text}, true
	case blockImage:
		return provider.ImageBlock{MimeType: b.MimeType, Data: b.Data}, true
	case blockToolCall:
		return provider.ToolCallBlock{ID: b.ID, Name: b.Name, Arguments: b.Arguments}, true
	case blockToolResult:
		block := provider.ToolResultBlock{CallID: b.CallID, IsError: b.IsError}
		for _, inner := range b.Content {
			if c, ok := decodeWireBlock(inner); ok {
				block.Content = append(block.Content, c)
			}
		}
		return block, true
	case blockReasoning:
		return provider.ReasoningBlock{ID: b.ReasoningID, Summary: b.Summary, Encrypted: b.Encrypted}, true
	}
	return nil, false
}

func hydrateMessageObject(rawMessage []byte, rep *loadReport) (provider.Message, error) {
	var row struct {
		Role    provider.Role     `json:"role"`
		Content []json.RawMessage `json:"content"`
		Time    time.Time         `json:"time"`
		Meta    map[string]string `json:"meta,omitempty"`
	}
	if err := json.Unmarshal(rawMessage, &row); err != nil {
		return provider.Message{}, err
	}
	msg := provider.Message{Role: row.Role, Time: row.Time, Meta: row.Meta}
	for _, raw := range row.Content {
		// v2 path: an explicit type discriminator decides the block.
		var typed wireBlock
		if err := json.Unmarshal(raw, &typed); err == nil && typed.Type != "" {
			c, ok := decodeWireBlock(typed)
			if !ok {
				rep.noteUnknown(typed.Type)
				continue
			}
			msg.Content = append(msg.Content, c)
			continue
		}

		// v1 fallback: discriminate by field presence.
		var head struct {
			Text        string `json:"text"`
			MimeType    string `json:"mime_type"`
			Data        []byte `json:"data"`
			ID          string `json:"id"`
			Name        string `json:"name"`
			CallID      string `json:"call_id"`
			ReasoningID string `json:"reasoning_id"`
			Summary     string `json:"summary"`
			Encrypted   string `json:"encrypted_content"`
			// ToolCallBlock also has Arguments, ToolResultBlock has Content + IsError
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			rep.noteCorruptBlock()
			continue
		}
		switch {
		case head.ReasoningID != "" || head.Encrypted != "":
			msg.Content = append(msg.Content, provider.ReasoningBlock{
				ID:        head.ReasoningID,
				Summary:   head.Summary,
				Encrypted: head.Encrypted,
			})
		case head.Name != "" && head.ID != "":
			var tc struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(raw, &tc)
			msg.Content = append(msg.Content, provider.ToolCallBlock{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		case head.CallID != "":
			var tr struct {
				CallID  string            `json:"call_id"`
				Content []json.RawMessage `json:"content"`
				IsError bool              `json:"is_error"`
			}
			_ = json.Unmarshal(raw, &tr)
			block := provider.ToolResultBlock{CallID: tr.CallID, IsError: tr.IsError}
			for _, c := range tr.Content {
				var inner struct {
					Text     string `json:"text"`
					MimeType string `json:"mime_type"`
					Data     []byte `json:"data"`
				}
				_ = json.Unmarshal(c, &inner)
				if inner.MimeType != "" {
					block.Content = append(block.Content, provider.ImageBlock{MimeType: inner.MimeType, Data: inner.Data})
				} else {
					block.Content = append(block.Content, provider.TextBlock{Text: inner.Text})
				}
			}
			msg.Content = append(msg.Content, block)
		case head.MimeType != "":
			msg.Content = append(msg.Content, provider.ImageBlock{MimeType: head.MimeType, Data: head.Data})
		default:
			msg.Content = append(msg.Content, provider.TextBlock{Text: head.Text})
		}
	}
	return msg, nil
}
