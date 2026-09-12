package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// SessionListTool enumerates recorded sessions. It is the discovery half of the
// session surface, and it exists because resolution alone was not enough.
//
// session_inspect reads any session it is HANDED, by id or by path, in any
// project. session_search finds text, and only in this project. Neither answers
// "what exists", so a reader holding a question rather than an id had nowhere
// to start, and a corpus sweep could not assemble its corpus. That gap is what
// this closes.
//
// Listing and searching stay separate tools on purpose. A search opens every
// transcript and scans it; this opens at most one bounded row per listed
// session. Folding the two together would have made the cheap question pay the
// expensive question's price.
type SessionListTool struct {
	TervaHome string
	CWD       string
}

func (t *SessionListTool) Name() string { return "session_list" }

// sessionListDesc is the English default for tool.session_list.description. A
// const for the same reason sessionInspectDesc is one: the extractor resolves a
// named const, not an inline concatenation.
const sessionListDesc = "List the sessions that terva has recorded, and put the most recent first. Use this tool to find a session id, and then read that session with session_inspect.\n\n" +
	"The default scope is this project. Set scope to \"all\" for the sessions of every project in $TERVA_HOME. Use the wider scope to diagnose a run from another directory. A session id alone does not say which project holds it.\n\n" +
	"Each row gives the session id, the time of the last change, the size, and the working directory. The tool opens no transcript, so it reports no title and no message count. Ask session_inspect for the content of one session.\n\n" +
	"Use limit and offset to move through the list. The default limit is 20, and the maximum is 100. This tool lists the sessions of a project. It does not list swarm sub-agents, and swarm_spawn reports those ids."

func (t *SessionListTool) Description() string {
	return i18n.D("tool.session_list.description", sessionListDesc)
}

func (t *SessionListTool) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"project", "all"},
				"description": "The sessions to list. The value \"project\" is the default, and it gives the sessions of this working directory. The value \"all\" gives the sessions of every project in $TERVA_HOME.",
			},
			"limit":  map[string]any{"type": "integer", "description": "The maximum number of sessions to list. The default is 20, and the maximum is 100."},
			"offset": map[string]any{"type": "integer", "description": "The number of sessions to skip before the tool returns results. Use the offset that a cut result reports to read the next page."},
		},
		"additionalProperties": false,
	})
	return b
}

type sessionListArgs struct {
	Scope  string `json:"scope"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

const (
	slDefaultLimit = 20
	slMaxLimit     = 100
)

func (t *SessionListTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a sessionListArgs
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return toolErr("session_list: invalid arguments: " + err.Error()), nil
		}
	}
	scope := strings.TrimSpace(strings.ToLower(a.Scope))
	if scope == "" {
		scope = "project"
	}
	// An unknown scope is refused rather than silently narrowed. A caller that
	// asked for "global" and received this project's sessions would read the
	// short list as "there is nothing else", which is the one answer an
	// enumeration must never fake.
	if scope != "project" && scope != "all" {
		return toolErr(fmt.Sprintf("session_list: unknown scope %q — use \"project\" for this working directory, or \"all\" for every project in $TERVA_HOME", a.Scope)), nil
	}
	limit := a.Limit
	if limit <= 0 {
		limit = slDefaultLimit
	}
	if limit > slMaxLimit {
		limit = slMaxLimit
	}
	offset := a.Offset
	if offset < 0 {
		offset = 0
	}

	refs := core.ListSessionsAcrossProjects(t.TervaHome)
	// The current project's bucket, so a "project" scope filters on the same
	// key the store buckets by rather than re-deriving a path.
	mine := filepath.Base(core.SessionsDir(t.TervaHome, t.CWD))
	if scope == "project" {
		kept := refs[:0:0]
		for _, r := range refs {
			if r.Bucket == mine {
				kept = append(kept, r)
			}
		}
		refs = kept
	}

	total := len(refs)
	if total == 0 {
		if scope == "project" {
			return ssText("session_list: this project has no recorded sessions yet — pass scope \"all\" to list every project in $TERVA_HOME"), nil
		}
		return ssText("session_list: $TERVA_HOME holds no recorded sessions yet"), nil
	}
	if offset >= total {
		return toolErr(fmt.Sprintf("session_list: offset %d is past the end — %d session(s) match this scope", offset, total)), nil
	}
	end := min(offset+limit, total)
	page := refs[offset:end]

	var b strings.Builder
	label := "this project"
	if scope == "all" {
		label = "every project"
	}
	fmt.Fprintf(&b, "sessions in %s — %d total; showing %d–%d\n", label, total, offset+1, end)
	ids := make([]string, 0, len(page))
	for i, r := range page {
		ids = append(ids, r.ID)
		// The opening meta row, which is bounded to one line and carries the
		// working directory. It is the only read this tool performs, and it
		// runs for the listed page alone. A title would need the FOLDED meta
		// and therefore a full scan, because a rename lands in a later row.
		where := "(unknown directory)"
		if c, err := core.ReadSessionCreation(r.Path); err == nil && strings.TrimSpace(c.CWD) != "" {
			where = c.CWD
			if r.Bucket == mine {
				where += "  (this project)"
			}
		}
		fmt.Fprintf(&b, "[#%d] %s  %s  %8s  %s\n",
			offset+i+1, r.ID, r.Modified.Format("2006-01-02 15:04"), slSize(r.Size), where)
	}
	if end < total {
		fmt.Fprintf(&b, "more: pass offset %d for the next %d\n", end, limit)
	}
	if scope == "project" && total > 0 {
		b.WriteString("note: this is one project. Pass scope \"all\" to list every project in $TERVA_HOME.\n")
	}

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: b.String()}},
		Details: map[string]any{
			"scope":       scope,
			"total":       total,
			"offset":      offset,
			"count":       len(page),
			"session_ids": ids,
		},
	}, nil
}

// slSize renders a byte count for a human reading a column. Exact bytes are
// noise at this scale: the number answers "is this a big session", and the
// transcript's real cost is a question for session_inspect stats.
func slSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
