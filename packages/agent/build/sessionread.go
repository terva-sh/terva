package build

import (
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// sessionHistoryReader serves an extension's list_sessions /
// read_session (protocol 3) from the on-disk JSONL transcripts, scoped to
// the active project. It is the read-only bridge that lets an
// out-of-tree extension (e.g. a session-search index) see prior
// conversations without coupling to terva's session format — it gets a
// flattened role+text view.
type sessionHistoryReader struct {
	tervaHome string
	cwd       string
}

func newSessionHistoryReader(tervaHome, cwd string) *sessionHistoryReader {
	return &sessionHistoryReader{tervaHome: tervaHome, cwd: cwd}
}

// ListSessions returns the active project's sessions. A non-empty
// projectID that doesn't match the active project returns nothing —
// cross-project reads are not granted over this channel.
func (r *sessionHistoryReader) ListSessions(_ /*extName*/, projectID string) []extproto.SessionInfo {
	if projectID != "" && projectID != core.ProjectKey(r.cwd) {
		return nil
	}
	summaries := core.DescribeSessions(r.tervaHome, r.cwd)
	out := make([]extproto.SessionInfo, 0, len(summaries))
	for _, s := range summaries {
		info := extproto.SessionInfo{
			SessionID: SessionIDFromPath(s.Path),
			Title:     s.Title,
			Preview:   s.FirstUserText,
			Messages:  s.MessageCount,
		}
		if fi, err := os.Stat(s.Path); err == nil {
			info.ModTime = fi.ModTime().Unix()
		}
		out = append(out, info)
	}
	return out
}

// ReadSession returns one session's transcript flattened to role+text.
// The id is resolved inside the active project's sessions directory and
// rejected if it tries to escape it, so an extension cannot read
// arbitrary files by id.
func (r *sessionHistoryReader) ReadSession(_ /*extName*/, sessionID string) ([]extproto.SessionMessage, bool) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) || strings.Contains(sessionID, "..") {
		return nil, false
	}
	path := filepath.Join(core.SessionsDir(r.tervaHome, r.cwd), sessionID+".jsonl")
	_, msgs, err := core.OpenSession(path)
	if err != nil {
		return nil, false
	}
	out := make([]extproto.SessionMessage, 0, len(msgs))
	for _, m := range msgs {
		if text := flattenMessageText(m); text != "" {
			out = append(out, extproto.SessionMessage{Role: string(m.Role), Text: text})
		}
	}
	return out, true
}

// SessionIDFromPath turns a session file path into its stable id (the
// file name without the .jsonl extension). Kept as the name a dozen call
// sites already use; the implementation moved to core so the tool layer can
// share it without a second copy.
func SessionIDFromPath(path string) string {
	return core.SessionIDFromPath(path)
}

// LooksLikeSessionID reports whether s has the session filename-stem shape
// core.NewSession generates — YYYYMMDD-HHMMSS-xxxxxxxx (8 digits, dash, 6
// digits, dash, 8 lowercase hex), .jsonl suffix tolerated. The --resume flag
// consumes its optional value only on a match, so a positional prompt after
// -r is never mistaken for a session id.
func LooksLikeSessionID(s string) bool {
	s = strings.TrimSuffix(s, ".jsonl")
	if len(s) != 24 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 15:
			if r != '-' {
				return false
			}
		case i < 15:
			if r < '0' || r > '9' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}

// flattenMessageText concatenates a message's text blocks, dropping
// tool-call structure the way a text index wants.
func flattenMessageText(m provider.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok && tb.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// WireSessionReader gives extensions read-only, project-scoped access to
// past session transcripts (protocol 3 list_sessions / read_session).
// No-op when extensions are absent. Wired at each extension-manager
// construction site, where the terva home and cwd are known.
func WireSessionReader(extMgr *extensions.Manager, tervaHome, cwd string) {
	if extMgr == nil {
		return
	}
	extMgr.SetSessionReader(newSessionHistoryReader(tervaHome, cwd))
}
