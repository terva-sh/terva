package core

import (
	"context"
	"encoding/json"
	"errors"
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

// StreamReplayMessages walks a session JSONL like ReadReplayRows but retains
// nothing: each message row is hydrated, handed to fn with its replay-row
// index, then discarded — so a caller inspecting a large transcript holds one
// message at a time instead of the whole file. Usage and compaction rows are
// counted (keeping row numbers aligned with ReadReplayRows' indexes, except
// for corrupt rows, which that reader drops) but never decoded — skipping
// checkpoint hydration entirely. Input is bounded at the read boundary: any
// single row past jsonlPerLineCeiling is skipped without being materialized
// whole (flagging truncated), and maxBytes > 0 caps the cumulative row bytes
// scanned; hitting either stops the walk with truncated=true and everything
// streamed so far still delivered. ctx is checked per delivered row so a long
// scan aborts promptly (returning ctx.Err()). Corrupt rows are skipped
// (best-effort, like the loader). The meta row is returned whenever present.
func StreamReplayMessages(ctx context.Context, path string, maxBytes int64, fn func(row int, m provider.Message)) (meta SessionMeta, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, false, err
	}
	defer f.Close()

	row := 0
	rep := &loadReport{}
	// An oversized row is skipped by the reader (never hydrated); it still means
	// the window is incomplete, so flag truncation.
	onOversize := func(int64) { truncated = true }
	walkErr := forEachJSONLLineBounded(f, jsonlPerLineCeiling, maxBytes, onOversize, func(line []byte) error {
		// Cancellation is checked once per delivered row so a long scan aborts
		// promptly; the drain/cumulative bounds cap the only path fn doesn't see.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		var head sessionLineHead
		if err := json.Unmarshal(line, &head); err != nil {
			return nil
		}
		switch head.Type {
		case "meta":
			var mrow struct {
				Meta SessionMeta `json:"meta"`
			}
			if err := json.Unmarshal(line, &mrow); err == nil {
				meta = mrow.Meta
			}
		case "message":
			m, err := hydrateMessage(line, rep)
			if err != nil || len(m.Content) == 0 {
				return nil
			}
			fn(row, m)
			row++
		case "usage", "compaction":
			row++
		}
		return nil
	})
	if errors.Is(walkErr, errJSONLCumulative) {
		truncated = true
		walkErr = nil
	}
	if walkErr != nil {
		return SessionMeta{}, truncated, walkErr // e.g. context.Canceled or an I/O error
	}
	return meta, truncated, nil
}
