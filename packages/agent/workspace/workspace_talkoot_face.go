package workspace

import (
	"context"
	"hash/fnv"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/talkoot/expression"
)

// Each running talkoot keeps an expression engine (TKT-01M3NSM8R3). It reads
// every sealed room line, so each member's status carries the pose its face
// shows. A beat goes to the room's address in the flush that takes its line,
// after the line's event when the line has one. docs/proposals/talkoot-members.md,
// "The engine decides when", is the design.

// faceSeed is the engine's seed for talkoot id. It follows from the id, so a
// restart replays the room with the seed the live engine had.
func faceSeed(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return h.Sum64()
}

// startFace builds the run's engine over rows, nil for the default table,
// and replays the room's lines into it, before the run observes a new line.
func (r *talkootRun) startFace(lines []talkoot.Line, rows []expression.Row) {
	e := expression.NewTable(faceSeed(r.id), rows)
	for _, l := range lines {
		e.Observe(l)
	}
	e.Replayed()
	r.face = e
}

// facesLocked reads each roster member's expression at now. It runs with
// evMu held, so the faces match the lines queued so far.
func (r *talkootRun) facesLocked(now time.Time) map[string]expression.Expression {
	roster := r.roster.Load()
	if r.face == nil || roster == nil {
		return nil
	}
	r.face.Advance(now)
	faces := make(map[string]expression.Expression, len(roster.Members))
	for _, m := range roster.Members {
		faces[m.ID] = r.face.Expression(m.ID, now)
	}
	return faces
}

// faceOverlay sets each status's expression from faces.
func faceOverlay(st []talkoot.Status, faces map[string]expression.Expression) {
	for i := range st {
		x := faces[st[i].Member]
		st[i].Expression, st[i].Intensity, st[i].ExpressionCause = x.Pose, x.Intensity, x.Cause
	}
}

// armFace sets the timer that flushes the run when an expression next
// changes with time alone, such as a held pose that ends or a card that has
// waited long enough to raise the brow. The flush sends the new statuses.
// It runs with evMu held.
func (r *talkootRun) armFaceLocked(now time.Time) {
	if r.faceTimer != nil {
		r.faceTimer.Stop()
		r.faceTimer = nil
	}
	if r.face == nil || r.faceStopped {
		return
	}
	if next := r.face.Next(now); !next.IsZero() {
		r.faceTimer = time.AfterFunc(next.Sub(now), r.flush)
	}
}

// stopFace stops the timer for good, as the run closes.
func (r *talkootRun) stopFace() {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	r.faceStopped = true
	if r.faceTimer != nil {
		r.faceTimer.Stop()
		r.faceTimer = nil
	}
}

func wireTalkootBeat(b expression.Beat) ctrlproto.TalkootBeat {
	return ctrlproto.TalkootBeat{Member: b.Member, Beat: b.Beat, Toward: b.Toward, Cause: b.Cause, At: b.At}
}

func (w *Workspace) TalkootTrace(ctx context.Context, p ctrlproto.TalkootTraceParams) (ctrlproto.TalkootTraceResult, error) {
	run, err := w.talkootRunOf(p.ID)
	if err != nil {
		return ctrlproto.TalkootTraceResult{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	out := ctrlproto.TalkootTraceResult{Member: p.Member, Entries: []ctrlproto.TalkootTraceEntry{}}
	if run.face == nil {
		return out, nil
	}
	out.Seed = run.face.Seed()
	for _, e := range run.face.Trace(p.Member) {
		out.Entries = append(out.Entries, ctrlproto.TalkootTraceEntry{
			At: e.At, Signal: e.Signal, Expression: e.Pose, Intensity: e.Intensity,
			Beat: e.Beat, Toward: e.Toward, Cause: e.Cause,
		})
	}
	return out, nil
}
