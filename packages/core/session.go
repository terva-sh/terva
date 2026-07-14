package core

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// Session is a JSONL-backed conversation transcript tied to a cwd.
type Session struct {
	ID     string
	Path   string
	Meta   SessionMeta
	writer *os.File
	buf    *bufio.Writer
	// writeMu serializes every durable write (writeLine) and the messagesAppended
	// counter. A session's transcript can have concurrent writers — e.g. a web
	// client's clear/compact writing a checkpoint while a turn on another
	// connection is persisting messages — and the bufio.Writer is not
	// goroutine-safe, so unsynchronized writers would interleave bytes / corrupt
	// the JSONL. All Append*/Update*/writeLine paths take this lock.
	writeMu sync.Mutex

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

	// errMu guards the lazily-opened error sidecar (errFile). Provider/turn
	// failures are recorded in a SEPARATE file alongside the transcript, never
	// in the transcript itself — the .jsonl has a fixed record vocabulary that
	// replay, resume, and compaction depend on, and an error row would be noise
	// there. Its own mutex (not writeMu) because it writes a different file and
	// is called off the turn goroutine, independent of transcript writes.
	errMu   sync.Mutex
	errFile *os.File

	// LoadWarnings describes everything OpenSession had to skip or
	// guess at while reading the file (corrupt rows, unknown block
	// types, a newer format version). Empty for clean loads. Callers
	// decide how to surface it; the data is never silently dropped.
	LoadWarnings []string

	// TitleGenerated reports whether the title OpenSession loaded (the last
	// rename row, reflected into Meta.Title) was machine-generated
	// (RenameSessionGenerated) rather than a user rename. Provenance decides
	// whether automatic re-titling may replace the title — a manual rename
	// is never clobbered. Meta-line titles and legacy source-less rename
	// rows count as manual: the conservative reading.
	TitleGenerated bool
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
	Type       string            `json:"type"`
	Meta       *SessionMeta      `json:"meta,omitempty"`
	Message    *wireMessage      `json:"message,omitempty"`
	Messages   []wireMessage     `json:"messages,omitempty"`
	Usage      *provider.Usage   `json:"usage,omitempty"`
	Cumulative *provider.Usage   `json:"cumulative,omitempty"`
	Directive  *sessionDirective `json:"directive,omitempty"`
}

// sessionDirective is an append-only mutation instruction: a JSONL line that
// tells a loader to transform the reconstructed transcript without rewriting
// earlier rows (which the append-only log can't do). The only op today is
// exclude_image — drop an image the provider rejected (content-addressed by
// sha256) so a resumed session doesn't re-send it and re-fail.
type sessionDirective struct {
	Op     string `json:"op"`               // e.g. "exclude_image"
	SHA256 string `json:"sha256,omitempty"` // content hash of the image to drop
	Reason string `json:"reason,omitempty"`
}

const (
	recordDirective       = "directive"
	directiveExcludeImage = "exclude_image"
)

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
	if err := privfs.MkdirAll(dir); err != nil {
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
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return newSessionAt(path, cwd, providerName, model, version)
}

// newSessionAt is the shared implementation. Both NewSession and
// NewSessionAtPath funnel through here so the meta-line layout,
// freshFile bookkeeping, and id format stay identical.
func newSessionAt(p, cwd, providerName, model, version string) (*Session, error) {
	id := uuid.NewString()
	f, err := privfs.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
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

// jsonlPerLineCeiling bounds the bytes forEachJSONLLineBounded will materialize
// for a SINGLE row before handing it to fn. A row longer than this is drained to
// its newline and skipped without ever being allocated whole — an oversized or
// unterminated row can't force an allocation past this bound. It sits below the
// cumulative scan ceiling (session_inspect's 64 MiB) yet well above any real
// transcript row (a base64 image block or a whole-session compaction
// checkpoint), so only pathological input trips it. A var so tests can lower it.
var jsonlPerLineCeiling int64 = 16 << 20

// errJSONLCumulative stops the bounded walk when the cumulative byte budget is
// spent. It never escapes forEachJSONLLineBounded's callers, who map it to a
// truncation flag.
var errJSONLCumulative = errors.New("jsonl: cumulative byte ceiling reached")

// forEachJSONLLineBounded is forEachJSONLLine with two input bounds enforced at
// the READ boundary — before a row is trimmed, unmarshaled, or handed to fn:
//
//   - perLineMax caps a single row's bytes. A longer row is drained to its
//     newline and skipped (onOversize is called with the raw byte count so the
//     caller can flag truncation); fn never sees it, so no oversized or
//     unterminated row is ever materialized whole. Memory stays ~perLineMax.
//   - cumulativeMax caps total row bytes read across the file, enforced even
//     mid-row so one unterminated row can't read the whole file. Reaching it
//     stops the walk with errJSONLCumulative.
//
// A max <= 0 disables that bound; onOversize may be nil. fn MUST NOT retain the
// slice it is handed (the backing buffer is reused across rows).
func forEachJSONLLineBounded(r io.Reader, perLineMax, cumulativeMax int64, onOversize func(n int64), fn func([]byte) error) error {
	br := bufio.NewReader(r)
	var cumulative, rowBytes int64
	var line []byte
	oversized := false
	for {
		frag, err := br.ReadSlice('\n')
		cumulative += int64(len(frag))
		rowBytes += int64(len(frag))
		if perLineMax > 0 && !oversized && int64(len(line))+int64(len(frag)) > perLineMax {
			oversized = true // stop retaining; drain the rest of this row
			line = line[:0]
		}
		if !oversized {
			line = append(line, frag...) // append copies frag out of br's buffer
		}

		if err == bufio.ErrBufferFull {
			// Row continues past bufio's buffer; enforce the budget mid-row.
			if cumulativeMax > 0 && cumulative > cumulativeMax {
				return errJSONLCumulative
			}
			continue
		}

		// Row complete: delimiter found, or EOF closed the file's final row.
		if oversized {
			if onOversize != nil {
				onOversize(rowBytes)
			}
		} else if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
			if ferr := fn(trimmed); ferr != nil {
				return ferr
			}
		}
		line = line[:0]
		rowBytes = 0
		oversized = false

		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err // a real read error
		}
		if cumulativeMax > 0 && cumulative > cumulativeMax {
			return errJSONLCumulative
		}
	}
}

// ReadSessionMeta reads only a session file's meta row — the cheap
// authorization primitive a caller uses BEFORE committing to a full scan (e.g.
// session_inspect confirming a swarm child's project ownership before parsing
// its payload). The loader writes meta as the first row, and a later UpdateModel
// only rewrites provider/model (never cwd), so the first meta row's cwd is
// authoritative; the scan stops there. Per-line and cumulative bounded so a
// damaged/crafted first row can't force unbounded work. A missing meta returns
// the zero value (empty cwd), which authorization treats as fail-closed.
func ReadSessionMeta(path string) (SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, err
	}
	defer f.Close()
	var meta SessionMeta
	werr := forEachJSONLLineBounded(f, jsonlPerLineCeiling, jsonlPerLineCeiling, nil, func(line []byte) error {
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		if head.Type != "meta" {
			return nil
		}
		var row struct {
			Meta SessionMeta `json:"meta"`
		}
		if err := json.Unmarshal(line, &row); err == nil {
			meta = row.Meta
			return io.EOF // first meta row is authoritative for cwd; stop
		}
		return nil
	})
	if werr != nil && werr != io.EOF {
		return SessionMeta{}, werr
	}
	return meta, nil
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
	var titleGenerated bool
	var messages []provider.Message
	excludeImages := map[string]bool{}
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
				titleGenerated = false
			} else {
				rep.corruptLines++
			}
		case "rename":
			// The latest rename row IS the session's title; without this a
			// session renamed while cold would materialize untitled and the
			// automatic titling pass could clobber the user's name.
			var row struct {
				Title  string `json:"title"`
				Source string `json:"source"`
			}
			if err := json.Unmarshal(line, &row); err == nil && row.Title != "" {
				meta.Title = row.Title
				titleGenerated = row.Source == renameSourceGenerated
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
		case recordDirective:
			var row struct {
				Directive sessionDirective `json:"directive"`
			}
			if err := json.Unmarshal(line, &row); err == nil &&
				row.Directive.Op == directiveExcludeImage && row.Directive.SHA256 != "" {
				excludeImages[strings.ToLower(row.Directive.SHA256)] = true
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if meta.FormatVersion > sessionFormatVersion {
		rep.newerFormat = meta.FormatVersion
	}
	// Apply append-only directives before repair so the rebuilt transcript
	// already reflects them (e.g. a provider-rejected image is gone, so a
	// resume can't re-send it and re-fail).
	if len(excludeImages) > 0 {
		messages = applyImageExclusions(messages, excludeImages)
	}
	messages = repairToolUseResultPairs(messages)
	out, err := privfs.OpenFile(path, os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return nil, nil, err
	}
	s := &Session{ID: meta.ID, Path: path, Meta: meta, TitleGenerated: titleGenerated, writer: out, buf: bufio.NewWriter(out), LoadWarnings: rep.warnings(path)}
	return s, messages, nil
}

// applyImageExclusions replaces every ImageBlock whose content sha256 is in the
// excluded set with the standard rejected-image note — directly in a message
// and nested in a tool result. Content-addressed, so one exclude_image
// directive covers every copy of the image (tool result + codex mirror) and
// survives reordering. Mutates and returns msgs.
func applyImageExclusions(msgs []provider.Message, excluded map[string]bool) []provider.Message {
	isExcluded := func(b provider.ImageBlock) bool { return excluded[imageSHA256(b.Data)] }
	for mi := range msgs {
		content := msgs[mi].Content
		for ci := range content {
			switch v := content[ci].(type) {
			case provider.ImageBlock:
				if isExcluded(v) {
					content[ci] = provider.TextBlock{Text: imageRejectedNote}
				}
			case provider.ToolResultBlock:
				changed := false
				for ii := range v.Content {
					if ib, ok := v.Content[ii].(provider.ImageBlock); ok && isExcluded(ib) {
						v.Content[ii] = provider.TextBlock{Text: imageRejectedNote}
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
	// TitleGenerated reports machine provenance for Title (see
	// Session.TitleGenerated); false for user renames and meta-line titles.
	TitleGenerated bool
}

// renameSourceGenerated marks a rename row written by machine titling
// (settleTitle / sessions.generate_title / the post-compaction refresh).
// User renames carry no source. The distinction is provenance: automatic
// re-titling may replace a generated title, never a manual one.
const renameSourceGenerated = "generated"

// RenameSession appends a USER rename line to the session file. This is
// safe even for the currently active session because it opens the
// file independently and appends (doesn't rewrite).
func RenameSession(path, title string) error {
	return appendRename(path, title, "")
}

// RenameSessionGenerated appends a machine-generated rename line — same row
// as RenameSession plus the provenance marker automatic re-titling keys on.
func RenameSessionGenerated(path, title string) error {
	return appendRename(path, title, renameSourceGenerated)
}

func appendRename(path, title, source string) error {
	f, err := privfs.OpenFile(path, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	defer f.Close()
	row := map[string]string{"type": "rename", "title": title}
	if source != "" {
		row["source"] = source
	}
	line, _ := json.Marshal(row)
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
				s.TitleGenerated = false
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
				Title  string `json:"title"`
				Source string `json:"source"`
			}
			if err := json.Unmarshal(line, &row); err == nil && row.Title != "" {
				s.Title = row.Title
				s.TitleGenerated = row.Source == renameSourceGenerated
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
		if e.IsDir() || !isSessionTranscriptName(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if sessionHasNoMessages(p) {
			_ = os.Remove(p)
		}
	}
}

// isSessionTranscriptName reports whether a directory entry name is a
// session transcript. Error sidecars (<session>.errors.jsonl, see
// LogError) share the .jsonl extension so they sort next to their
// transcript, but they are NOT sessions: listing them would surface
// blank entries in /sessions and /continue, and pruning them would
// silently destroy the failure record (sidecar rows carry no "message"
// lines, so sessionHasNoMessages reads them as empty). Every scan of a
// sessions directory must use this filter, not a bare .jsonl check.
func isSessionTranscriptName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".errors.jsonl")
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
		if e.IsDir() || !isSessionTranscriptName(e.Name()) {
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.writeLineLocked(sessionLine{Type: "message", Message: &w}); err != nil {
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.writeLineLocked(sessionLine{Type: "compaction", Messages: wires}); err != nil {
		return err
	}
	s.messagesAppended = len(messages)
	return nil
}

// UpdateModel records a provider/model switch in the session file.
// The reader keeps the most recent meta entry, so the session resumes
// with the updated model.
//
// A switch that changes nothing writes nothing. Startup applies the resolved
// model unconditionally, so without this a session opened three times over
// began with three byte-identical meta rows saying the same thing — noise in a
// file whose meta rows are supposed to read as a timeline of what changed.
func (s *Session) UpdateModel(providerName, model string) error {
	if s == nil {
		return nil
	}
	if s.Meta.Provider == providerName && s.Meta.Model == model {
		return nil
	}
	s.Meta.Provider = providerName
	s.Meta.Model = model
	return s.writeLine(sessionLine{Type: "meta", Meta: &s.Meta})
}

// StampVersion records the running build in the session file, so a session that
// spans an upgrade can say which terva wrote which part of it.
//
// The meta rows already form a timeline: they are append-only, the loader keeps
// the last one, and UpdateModel writes a fresh one on every model switch — which
// is what lets a reader say "these turns ran on codex, those on anthropic".
// Version was the one field that never joined it. It was stamped once by
// NewSession and then re-emitted verbatim forever, so every row a resumed
// session wrote CLAIMED the build that had created the session, however many
// upgrades ago. Attribution across an upgrade was not missing so much as wrong.
//
// Callers are the paths that RESUME a session to keep talking in it. Opening a
// session to read it — an inspector, a sub-agent lifting its transcript — must
// not stamp: it isn't writing any of the rows it would be claiming.
//
// A no-op when the version is unchanged, which is the common case, so a session
// that never crosses an upgrade grows no extra rows.
func (s *Session) StampVersion(version string) error {
	if s == nil || version == "" || s.Meta.Version == version {
		return nil
	}
	s.Meta.Version = version
	return s.writeLine(sessionLine{Type: "meta", Meta: &s.Meta})
}

// AppendImageExclusion writes a directive telling future loads to drop the
// image whose raw bytes hash to sha256Hex — every copy of it (the tool result
// and the codex mirror both match) — replacing it with a short note. Append
// only: the original image rows stay in the file for audit, but the loader
// applies the exclusion when it rebuilds the transcript, so a resumed session
// never re-sends a provider-rejected image and pays the recovery only once.
func (s *Session) AppendImageExclusion(sha256Hex, reason string) error {
	if s == nil || sha256Hex == "" {
		return nil
	}
	return s.writeLine(sessionLine{Type: recordDirective, Directive: &sessionDirective{
		Op: directiveExcludeImage, SHA256: sha256Hex, Reason: reason,
	}})
}

// AppendUsage writes a usage row to the session.
func (s *Session) AppendUsage(u, cum provider.Usage) error {
	if s == nil {
		return nil
	}
	return s.writeLine(sessionLine{Type: "usage", Usage: &u, Cumulative: &cum})
}

// sessionError is one row of the error sidecar (see LogError).
type sessionError struct {
	Time     time.Time `json:"time"`
	Error    string    `json:"error"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
}

// ErrorLogPath returns the path of the session's error sidecar — the
// transcript path with its .jsonl suffix replaced by .errors.jsonl, so the
// two sort together in a directory listing. Empty when the session has no
// file (live-only conversations).
func (s *Session) ErrorLogPath() string {
	if s == nil || s.Path == "" {
		return ""
	}
	return ErrorLogPathFor(s.Path)
}

// ErrorLogPathFor derives the error-sidecar path for a transcript path,
// for callers that hold only the path (e.g. deleting a session that isn't
// open). Keep in sync with ErrorLogPath; empty in, empty out.
func ErrorLogPathFor(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	return strings.TrimSuffix(transcriptPath, ".jsonl") + ".errors.jsonl"
}

// LogError records a turn/provider failure to the session's error sidecar — a
// file ALONGSIDE the transcript, never inside it (the transcript's record
// vocabulary is a contract for replay/resume/compaction). The file is created
// lazily on the first error, so a clean session never leaves an empty sidecar.
// Stamped with the session's current provider/model. Best-effort and
// non-fatal: a failure to record an error must not compound the original one,
// so the write error is returned for callers that care but is safe to ignore.
func (s *Session) LogError(errText string) error {
	if s == nil || s.Path == "" || strings.TrimSpace(errText) == "" {
		return nil
	}
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.errFile == nil {
		f, err := privfs.OpenFile(s.ErrorLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY)
		if err != nil {
			return err
		}
		s.errFile = f
	}
	// Redact secret-shaped substrings and bound the length before persisting:
	// provider/auth errors can embed Authorization headers, tokened callback
	// URLs, or whole response bodies, and the sidecar is a durable local file.
	row := sessionError{Time: time.Now().UTC(), Error: redactErrorForSidecar(errText), Provider: s.Meta.Provider, Model: s.Meta.Model}
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	// Direct write + newline (no bufio): errors are rare and must survive a
	// crash that skips Close, so we never leave a half-recorded failure buffered.
	if _, err := s.errFile.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
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
	// Hold writeMu so a Close racing a late Append (multi-connection web) neither
	// flushes a half-written line nor reads messagesAppended mid-update.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	flushErr := s.buf.Flush()
	closeErr := s.writer.Close()
	// Close the error sidecar if this session ever opened one (its writes are
	// unbuffered, so there's nothing to flush — just release the handle).
	s.errMu.Lock()
	if s.errFile != nil {
		_ = s.errFile.Close()
		s.errFile = nil
	}
	s.errMu.Unlock()
	if s.freshFile && s.messagesAppended == 0 {
		// Best-effort cleanup. We deliberately don't propagate the
		// remove error: if it fails (file already gone, perms changed)
		// the worst case is one stale empty file in the listing.
		_ = os.Remove(s.Path)
		// Keep the sidecar paired with the transcript: if the empty transcript
		// is discarded, drop its error log too rather than orphan it.
		_ = os.Remove(s.ErrorLogPath())
	}
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (s *Session) writeLine(row sessionLine) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeLineLocked(row)
}

// writeLineLocked is writeLine's body; the caller must hold writeMu. Used by the
// Append* methods that also mutate messagesAppended under the same lock, so the
// buffer write and the counter update are one atomic critical section.
func (s *Session) writeLineLocked(row sessionLine) error {
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
