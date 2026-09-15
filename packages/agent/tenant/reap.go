package tenant

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Reaping is the SAFE half of what D7 calls cleanup, and it is deliberately in
// a different file from anything that could destroy data.
//
// 🚨 The two things called "cleanup" have different risk and must never share a
// code path:
//
//  1. Stopping an idle CHILD PROCESS — cheap, reversible, and the right answer
//     to resource pressure. The next request starts it again and the tenant
//     sees a slow first response. That is all of it. This file.
//  2. Deleting a tenant's HOME — irreversible destruction of someone's work.
//     Not built, on purpose: D7 ships suspend → grace → delete, and an
//     environment that cannot be destroyed cannot be destroyed by a bug.
//
// Nothing here touches a home, a registry record, or a uid. A reaped tenant is
// one whose daemon is not currently running; it is not a tenant who lost
// anything.

// Hold marks a child as in use and returns the release.
//
// A child with a live connection is never idle, however long the person has
// been reading — "idle" has to mean "nobody is attached", not "nobody has typed
// recently", or a reaper would cut a websocket out from under someone thinking.
func (c *Child) Hold() func() {
	c.mu.Lock()
	c.holds++
	c.lastUsed = time.Now()
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.holds--
		// Stamped on release too: the idle clock starts when the last
		// connection drops, not when it was opened.
		c.lastUsed = time.Now()
		c.mu.Unlock()
	}
}

// idleFor reports how long the child has had no connection, and whether it is
// idle at all.
func (c *Child) idleFor(now time.Time) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.holds > 0 {
		return 0, false
	}
	return now.Sub(c.lastUsed), true
}

// ReapIdle stops every child that has had no connection for longer than idle,
// and reports how many it stopped.
//
// The record stays; only the process goes. The tenant's next request starts a
// fresh daemon over the same home, which is why this is safe to run on a timer
// and safe to get slightly wrong.
func (s *Supervisor) ReapIdle(idle time.Duration) int {
	if idle <= 0 {
		return 0
	}
	now := time.Now()

	s.mu.Lock()
	var stopping []*Child
	for id, c := range s.children {
		if !c.Alive() {
			delete(s.children, id)
			continue
		}
		if d, ok := c.idleFor(now); ok && d >= idle {
			delete(s.children, id)
			stopping = append(stopping, c)
		}
	}
	s.mu.Unlock()

	// Outside the lock: stopping a child waits on it to drain, and holding the
	// supervisor's lock through that would block every other tenant's request.
	for _, c := range stopping {
		fmt.Fprintf(os.Stderr, "terva serve: stopping idle environment %s (no connection for %s; it restarts on the next request)\n",
			c.ID, idle.Round(time.Second))
		c.shutdown()
	}
	return len(stopping)
}

// RunReaper reaps on a ticker until ctx is done. The composition root starts it;
// it returns when the supervisor is shutting down anyway.
func (s *Supervisor) RunReaper(ctx context.Context, idle, every time.Duration) {
	if idle <= 0 {
		return // reaping off: the operator asked for children to stay up
	}
	if every <= 0 {
		every = idle / 4
	}
	if every < time.Second {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ReapIdle(idle)
		}
	}
}
