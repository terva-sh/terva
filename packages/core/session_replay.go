package core

import (
	"encoding/json"
	"os"

	"terva.sh/terva/packages/provider"
)

// ReplayRowKind identifies a transcript row the session player walks forward.
type ReplayRowKind string

const (
	// ReplayRowMessage is a persisted conversation message (user/assistant/tool).
	ReplayRowMessage ReplayRowKind = "message"
	// ReplayRowUsage is a per-turn usage row (with its running cumulative).
	ReplayRowUsage ReplayRowKind = "usage"
	// ReplayRowCompaction is a checkpoint: the summary the loader would honor by
	// resetting the transcript. The player animates it (effective mode) or
	// ignores it (raw mode) — it never rewrites earlier rows.
	ReplayRowCompaction ReplayRowKind = "compaction"
)

// ReplayRow is one transcript row preserved in file order. It is the forward
// twin of RevealCompaction's backward reconstruction: where OpenSession
// collapses history at each "compaction" checkpoint (resetting its message
// set), ReadReplayRows keeps every row so a player can re-emit the session as
// a live-looking scene — including the pre-compaction turns a checkpoint later
// summarized away. See docs/proposals/session-player.md.
type ReplayRow struct {
	Kind ReplayRowKind

	// Message is set when Kind == ReplayRowMessage.
	Message provider.Message

	// Usage/Cumulative are set when Kind == ReplayRowUsage, as recorded.
	Usage      provider.Usage
	Cumulative provider.Usage

	// Checkpoint is the summary output a compaction folded its input into,
	// set when Kind == ReplayRowCompaction. Honoring it replaces the live
	// transcript with these messages (what OpenSession does); the player uses
	// it to animate the compaction and resync the effective transcript.
	Checkpoint []provider.Message
}

// ReadReplayRows walks a session JSONL and returns every message, usage, and
// compaction row in file order, WITHOUT OpenSession's checkpoint collapse,
// plus the session's metadata. Read-only; the live session is untouched.
//
// meta/directive/rename rows carry no replay event and are skipped. Empty
// messages are dropped to mirror OpenSession's transcript build. Corrupt rows
// are skipped rather than failing the whole read (best-effort, like the
// loader), so a partially-written tail still plays up to the last good row.
func ReadReplayRows(path string) ([]ReplayRow, SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, SessionMeta{}, err
	}
	defer f.Close()

	var meta SessionMeta
	var rows []ReplayRow
	rep := &loadReport{}
	if err := forEachJSONLLine(f, func(line []byte) error {
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
				meta = row.Meta
			}
		case "message":
			m, err := hydrateMessage(line, rep)
			if err != nil || len(m.Content) == 0 {
				return nil
			}
			rows = append(rows, ReplayRow{Kind: ReplayRowMessage, Message: m})
		case "usage":
			var row struct {
				Usage      provider.Usage `json:"usage"`
				Cumulative provider.Usage `json:"cumulative"`
			}
			if err := json.Unmarshal(line, &row); err != nil {
				return nil
			}
			rows = append(rows, ReplayRow{
				Kind:       ReplayRowUsage,
				Usage:      row.Usage,
				Cumulative: row.Cumulative,
			})
		case "compaction":
			out, err := hydrateCompaction(line, rep)
			if err != nil {
				return nil
			}
			rows = append(rows, ReplayRow{Kind: ReplayRowCompaction, Checkpoint: out})
		}
		return nil
	}); err != nil {
		return nil, SessionMeta{}, err
	}
	return rows, meta, nil
}
