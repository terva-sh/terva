package workspace

import (
	"fmt"
	"strings"
	"unicode"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/session"
)

// A member's presence has two halves. The router knows turns and pauses. The
// host knows a worker's idle stop, the inbox, and whether anything listens
// for the member, and overlay adds that half.

// overlay fills in the host's half of each status: a worker stopped for
// idleness, a question or approval open in the inbox, and a member nothing
// listens for.
//
// 🔑 Offline reads memberUnbound, the check a worker delivery and an
// introduction read, so a status never says a member listens when a
// delivery would refuse it.
func (r *talkootRun) overlay(st []talkoot.Status) []talkoot.Status {
	roster := r.roster.Load()
	offline := make([]bool, len(st))
	if r.unbound != nil && roster != nil {
		for i := range st {
			if m, ok := memberOf(*roster, st[i].Member); ok {
				offline[i] = r.unbound(m) != nil
			}
		}
	}
	r.evMu.Lock()
	defer r.evMu.Unlock()
	for i := range st {
		st[i].Idle = r.idle[st[i].Member]
		st[i].Waiting = len(r.waiting[st[i].Member]) > 0
		st[i].Offline = offline[i]
	}
	return st
}

// maxFailureReason bounds a failed pause's reason, as a person's pause reason
// is bounded.
const maxFailureReason = 1024

// failureReason makes a driver's error into a failed pause's reason: one
// line, with secrets redacted, and bounded.
//
// 🚨 The reason reaches the room file, every status event, and the roster
// tool of every member, an outside worker's too. A provider's error can
// carry its request URL and its response body, so it is redacted as the
// error sidecar redacts, before the bound cuts it.
func failureReason(text string) string {
	out, dropped := session.RedactAndBound(text, maxFailureReason)
	out = strings.Join(strings.FieldsFunc(out, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if dropped > 0 {
		out += fmt.Sprintf("… [%d more bytes]", dropped)
	}
	return out
}

// waitOn records that member's card key is open, or with open false that it
// closed.
//
// 🔑 It keeps the set of open cards, not a count. A card that opens twice
// under one key, or a close for a card it never saw, then cannot leave the
// member waiting after its last card closes.
func (r *talkootRun) waitOn(member, key string, open bool) {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	if !open {
		delete(r.waiting[member], key)
		if len(r.waiting[member]) == 0 {
			delete(r.waiting, member)
		}
		return
	}
	if r.waiting == nil {
		r.waiting = map[string]map[string]bool{}
	}
	if r.waiting[member] == nil {
		r.waiting[member] = map[string]bool{}
	}
	r.waiting[member][key] = true
}

// talkootWaiting records a question or approval, named by key, that opens or
// closes for a member, and sends the member's new status. It returns the card
// bound to the run it counted in, which the session keeps. A card that opened
// outside a seat counts nothing.
//
// 🔑 An open binds the card to the talkoot's run then, and the close counts
// down on that run. A later run of the same talkoot starts with no waits, so
// a card from an earlier run must not end one in it.
func (w *Workspace) talkootWaiting(c openCard, key string, open bool) openCard {
	if w == nil || c.talkoot == "" || c.member == "" {
		return c
	}
	if c.run == nil && open {
		c.run, _ = w.talkootRunOf(c.talkoot)
	}
	if c.run == nil {
		return c
	}
	c.run.waitOn(c.member, key, open)
	// The card can open inside a member's turn. The flush waits for run.mu,
	// so it runs on a goroutine of its own, as tryDo's does.
	go c.run.flush()
	return c
}
