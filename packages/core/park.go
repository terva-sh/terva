package core

import "sync"

// ParkTable correlates asynchronous answers with waiters parked by id — the
// shape every approval/ask carrier in the tree had reimplemented by hand:
// make a buffered channel, store it in a mutexed map, deliver the first
// answer, delete on the way out. Each copy picked its own duplicate-id and
// double-answer semantics, and one of them (the workspace confirmer's
// pre-#408 curCallID variant) picked overwrite, which wedged a concurrent
// park forever. This is the one canonical copy.
//
// Semantics:
//   - Park refuses a duplicate id rather than overwriting a live waiter.
//   - Deliver is first-answer-wins and never blocks (channels are cap 1);
//     a second answer for the same id reports false instead of landing.
//   - CancelAll answers every parked waiter at once (turn abort, shutdown).
//
// The zero value is ready to use. Safe for concurrent use.
type ParkTable[T any] struct {
	mu   sync.Mutex
	pend map[string]chan T
}

// Park registers id and returns the channel the answer will arrive on,
// plus a release the caller must defer (it forgets the id; harmless after
// a Deliver or CancelAll already removed it). ok=false means the id is
// already parked: the caller minted a colliding id and must mint another —
// silently replacing a live waiter is exactly the wedge this type exists
// to prevent.
func (p *ParkTable[T]) Park(id string) (ch <-chan T, release func(), ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pend == nil {
		p.pend = map[string]chan T{}
	}
	if _, exists := p.pend[id]; exists {
		return nil, nil, false
	}
	c := make(chan T, 1)
	p.pend[id] = c
	return c, func() {
		p.mu.Lock()
		delete(p.pend, id)
		p.mu.Unlock()
	}, true
}

// Deliver hands v to the waiter parked under id and unparks it. Reports
// false for an unknown or already-answered id, so a carrier can surface
// "no pending approval with id X" instead of dropping the answer silently.
func (p *ParkTable[T]) Deliver(id string, v T) bool {
	p.mu.Lock()
	c, ok := p.pend[id]
	if ok {
		delete(p.pend, id)
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	c <- v
	return true
}

// CancelAll answers every parked waiter with v and empties the table.
func (p *ParkTable[T]) CancelAll(v T) {
	p.mu.Lock()
	pend := p.pend
	p.pend = nil
	p.mu.Unlock()
	for _, c := range pend {
		c <- v
	}
}

// Len reports how many waiters are currently parked.
func (p *ParkTable[T]) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pend)
}
