package workspace

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// History pages backward through the live transcript — the part a windowed snapshot
// did not carry.
//
// It reads from memory, because the effective transcript IS in memory: the agent
// holds it, bounded by the model's context window. Windowing was never about what the
// daemon can hold; it is about what a serialized carrier should have to ship down a
// phone's WebSocket at the end of every turn.
//
// The epoch check is the load-bearing part. An index only names a message within the
// transcript it was taken from, and transcriptEpoch changes precisely when that
// transcript is replaced (compact, /clear, SetMessages). A client that asks for
// "messages 40-80" against an epoch that has since moved is asking about a
// conversation that no longer exists — so say so, rather than hand back whatever now
// happens to sit at those indexes. The client resyncs from its next snapshot, which
// carries the new epoch.
func (w *Workspace) History(_ context.Context, sess string, before, limit int, epoch uint64) (ctrlproto.HistoryResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.HistoryResult{}, err
	}
	msgs := s.agent.Messages()
	now := s.agent.TranscriptEpoch()
	if epoch != 0 && epoch != now {
		return ctrlproto.HistoryResult{}, ctrlproto.Errorf(ctrlproto.CodeConflict,
			"the conversation changed while you were scrolling (it was compacted or cleared) — reload to see it")
	}
	if limit <= 0 {
		limit = ctrlproto.HistoryWindow
	}
	// Clamp rather than error: "give me what is above what I have" is a perfectly
	// sensible thing for a client at the top of its window to ask, even when the
	// answer is "nothing".
	end := min(max(before, 0), len(msgs))
	start := max(end-limit, 0)

	out := make([]core.WireMessage, 0, end-start)
	for _, m := range msgs[start:end] {
		out = append(out, core.MessageToWireFull(m))
	}
	return ctrlproto.HistoryResult{
		Epoch:    now,
		Base:     start,
		Total:    len(msgs),
		Messages: out,
	}, nil
}

// Reveal returns the turns a compaction checkpoint summarized away, so a client
// can keep them in the scrollback above the divider rather than lose them to what
// is, after all, only a context-management act.
//
// Nothing is reconstructed or re-summarized: a compaction row is an append-only
// checkpoint the loader honors, never a rewrite, so the superseded "message" rows
// are still sitting in the session file. core.RevealCompaction replays the
// loader's walk and snapshots the transcript at the instant before the target
// checkpoint resets it.
//
// The span it hands back EXCLUDES the tail the checkpoint kept verbatim (inferred
// from the checkpoint's own output length, not from AutoCompactKeepTail — orphan-
// tool repair can shorten it). So the result abuts the client's live transcript
// without overlapping it: prepend and done. TestRevealDoesNotOverlapLiveTranscript
// pins that, because the day it stops holding, every client silently doubles the
// turns around each compaction.
//
// Read-only. The live transcript, and the agent, are untouched.
func (w *Workspace) Reveal(_ context.Context, sess string, ordinal int) (ctrlproto.RevealResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.RevealResult{}, err
	}
	if s.sess == nil {
		// An ephemeral session has no file to read back. Not an error the user did
		// anything to cause: the client says "earlier turns unavailable" and the
		// divider simply does not expand.
		return ctrlproto.RevealResult{}, ctrlproto.Errorf(ctrlproto.CodeNotFound,
			"this session was never written to disk, so the turns before the compaction are not recoverable")
	}
	span, err := core.RevealCompaction(s.sess.Path, ordinal)
	if err != nil {
		return ctrlproto.RevealResult{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "reveal: %v", err)
	}

	msgs := make([]core.WireMessage, 0, len(span.Replaced))
	for _, m := range span.Replaced {
		// Full form: the revealed turns are real transcript, images and all. The
		// serialization boundary strips image data for a client that did not
		// negotiate image-data, exactly as it does for a snapshot.
		msgs = append(msgs, core.MessageToWireFull(m))
	}
	return ctrlproto.RevealResult{
		Ordinal:     span.Ordinal,
		PrevOrdinal: span.PrevOrdinal,
		Total:       span.Total,
		Messages:    msgs,
		// A clear is served, not refused — but a client is expected to stop the
		// automatic walk on PrevClear and cross only when the user says so. A
		// deliberate act should take a deliberate act to undo, and the daemon's job
		// is to report the boundary, not to decide for every frontend what it means.
		Clear:     span.Clear,
		PrevClear: span.PrevClear,
	}, nil
}
