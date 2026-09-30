package workspace

import (
	"slices"
	"strings"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/worker"
)

// A worker member's reads.
//
// 🔑 A worker member takes an envelope's chain when its turn reads the text,
// as a native member does, on a backend that reports reads
// (worker.Backend.ReportsReads). The terva backends run each text as an rpc
// prompt, and core emits user_message as that prompt's turn starts. claude
// echoes each user turn as the turn takes it, with the texts that folded into
// the turn as blocks of one message. The run matches each text, which names
// its envelope, to the delivery that sent it.
//
// 🔑 Any other delivery reads at once, when the worker accepts the text: a
// spawn, whose text reaches the worker wrapped in its briefing as the first
// turn, and every delivery to a backend that reports no reads. That is exact
// for an idle worker and early for a busy one, as it was before reads.

// 🔑 Each read takes a number in the order of its delivery, and a member
// never reads an older delivery after a newer one. An event read waits on the
// run's queue while a later spawn reads at once, so without the order an old
// read could move the member back into an older chain.

// workerRead is one delivery that waits for its read: the text, the process
// it was sent to, and its number in the member's delivery order.
type workerRead struct {
	text  string
	token uint64
	seq   uint64
	r     talkoot.Receipt
}

// workerReportsReads reports whether the backend a worker member runs on says
// when a user turn enters its conversation.
func workerReportsReads(driver string) bool {
	b, err := worker.Lookup(driver)
	return err == nil && b.ReportsReads
}

// deliverWorkerRead delivers text to worker member m and moves the member into
// the delivery's chain when the worker reads it. A nil r is a plain delivery.
//
// 🚨 The router calls this inside dispatch, which always runs under run.mu,
// held by run.do or by an update. run.router is therefore the router that
// made r, and an immediate read calls it directly: run.do here would take
// run.mu again under itself.
func (w *Workspace) deliverWorkerRead(run *talkootRun, m talkoot.Member, text string, r *talkoot.Receipt) error {
	if r == nil {
		return w.deliverWorker(run, m, text)
	}
	reads := workerReportsReads(m.Driver)
	// The read waits from just before the send, after the binding, so it
	// names the process the text goes to.
	var waiting uint64
	spawned, err := w.deliverWorkerTo(run, m, text, func() {
		if reads {
			waiting = w.addWorkerRead(run, m.ID, text, *r)
		}
	})
	if err != nil {
		if waiting != 0 {
			w.dropWorkerRead(run, m.ID, waiting)
		}
		return err
	}
	if waiting != 0 && !spawned {
		return nil
	}
	w.talkoot.mu.Lock()
	run.readSeq++
	err = w.applyWorkerReadLocked(run, run.router, m.ID, run.readSeq, *r)
	w.talkoot.mu.Unlock()
	if err != nil {
		w.diagf("talkoot %s: worker member %s could not read a delivery: %v", run.id, m.ID, err)
	}
	return nil
}

// applyWorkerReadLocked moves member into r's chain, unless the member has
// read a newer delivery already. The caller holds w.talkoot.mu, and run.mu
// for the router.
func (w *Workspace) applyWorkerReadLocked(run *talkootRun, rt *talkoot.Router, member string, seq uint64, r talkoot.Receipt) error {
	if seq <= run.lastRead[member] {
		return nil
	}
	if run.lastRead == nil {
		run.lastRead = map[string]uint64{}
	}
	run.lastRead[member] = seq
	return rt.Read(r)
}

// addWorkerRead makes a delivery to member's current process wait for its
// read, and returns the delivery's number.
func (w *Workspace) addWorkerRead(run *talkootRun, member, text string, r talkoot.Receipt) uint64 {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if run.workerReads == nil {
		run.workerReads = map[string][]workerRead{}
	}
	run.readSeq++
	run.workerReads[member] = append(run.workerReads[member], workerRead{
		text: strings.TrimSpace(text), token: run.workerRun[member], seq: run.readSeq, r: r,
	})
	return run.readSeq
}

// dropWorkerRead forgets the delivery numbered seq, whose send failed.
func (w *Workspace) dropWorkerRead(run *talkootRun, member string, seq uint64) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	run.workerReads[member] = slices.DeleteFunc(run.workerReads[member], func(p workerRead) bool { return p.seq == seq })
}

// takeWorkerRead removes and returns the oldest delivery to member's process
// token whose text is text, if one waits. A report from another process
// reads nothing.
func (w *Workspace) takeWorkerRead(run *talkootRun, member string, token uint64, text string) (workerRead, bool) {
	text = strings.TrimSpace(text)
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	for i, p := range run.workerReads[member] {
		if p.token == token && p.text == text {
			run.workerReads[member] = append(run.workerReads[member][:i], run.workerReads[member][i+1:]...)
			return p, true
		}
	}
	return workerRead{}, false
}

// later runs fn after every fn queued before it, on a goroutine of the run's
// own. A caller that must not wait can still put its router calls in order.
func (r *talkootRun) later(fn func()) {
	r.laterMu.Lock()
	r.laterQ = append(r.laterQ, fn)
	if r.laterBusy {
		r.laterMu.Unlock()
		return
	}
	r.laterBusy = true
	r.laterMu.Unlock()
	go func() {
		for {
			r.laterMu.Lock()
			if len(r.laterQ) == 0 {
				r.laterBusy = false
				r.laterMu.Unlock()
				return
			}
			next := r.laterQ[0]
			r.laterQ = r.laterQ[1:]
			r.laterMu.Unlock()
			next()
		}
	}()
}
