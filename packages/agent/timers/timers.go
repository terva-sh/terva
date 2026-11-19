// Package timers is the daemon's timer service. It keeps one-time and
// recurring timers in a file, fires each one's owner when a window comes,
// and survives a restart. Decision 0029 rule 3 put it here, in the harness,
// in place of the system timers that an earlier ruling used.
//
// The package knows nothing about what a timer is for. An owner registers a
// handler under a name, sets timers under that name, and decides what a
// firing means.
//
// # What a restart and a gap do
//
// 🔑 A window fires at most once, and a gap never replays. When the daemon
// was down, or the machine slept, or the clock moved forward, a recurring
// timer gets one firing for the latest window. That firing carries the count
// of the windows before it that passed. The owner decides whether a late
// firing still runs (decision 0029 rule 7).
//
// The service does not ask why the time is later than it expected. Downtime,
// a suspend, and a clock correction all look the same from inside, and they
// all get the same answer.
//
// 🔑 A window fires only when it is later than the last one that fired. So a
// clock that moves back delays a timer and never repeats one. A board with no
// battery clock starts on a stale time and corrects it a moment later, and a
// timer that restarted from the corrected time would run the morning's work
// twice.
//
// # What a crash does
//
// The service writes down that a firing is in flight before it calls the
// handler, and clears the mark after the handler returns. A daemon that dies
// between the two delivers the firing again at the next start, with
// Redelivered set. So a firing reaches its handler at least once, and the
// handler can tell a second delivery from a first.
//
// 🚨 A firing that cannot be written down does not happen. Without the mark,
// a daemon that crashes in a loop on a full disk would fire the same window
// at every start.
package timers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Limits on what the service holds. The file is read and written whole, and
// an owner's mistake must not grow it without bound.
const (
	MaxTimers  = 1024
	MaxPayload = 16 << 10
)

// pollMax is the longest the loop sleeps before it reads the wall clock
// again. A Go timer counts time the machine was awake, so after a suspend it
// would fire late by the length of the sleep.
const pollMax = 30 * time.Second

// Timer is one timer as its owner set it, with the state the service keeps.
type Timer struct {
	// Owner names the handler that the timer fires. ID is unique for one
	// owner.
	Owner string `json:"owner"`
	ID    string `json:"id"`
	// Label says what the timer is for, for a person who reads the list.
	Label    string   `json:"label,omitempty"`
	Schedule Schedule `json:"schedule"`
	// Data is the owner's own record, which the service keeps and returns
	// unread.
	Data json.RawMessage `json:"data,omitempty"`

	Created time.Time `json:"created"`
	// Next is the first window that has not fired.
	Next time.Time `json:"next,omitzero"`
	// LastDue is the latest window that fired, and LastFired is when.
	LastDue   time.Time `json:"last_due,omitzero"`
	LastFired time.Time `json:"last_fired,omitzero"`
	// Pending is the firing in flight. It is set from before the handler
	// runs until it returns.
	Pending *Pending `json:"pending,omitempty"`
}

// Pending is a firing that the service wrote down and whose handler has not
// returned.
type Pending struct {
	Due    time.Time `json:"due"`
	First  time.Time `json:"first"`
	Missed int       `json:"missed,omitempty"`
}

// Firing is what a handler receives.
type Firing struct {
	Timer Timer
	// Due is the window this firing is for, the latest one that has come.
	Due time.Time
	// Missed counts the windows before Due that passed without a firing,
	// and First is the earliest of them. With no gap, Missed is 0 and First
	// equals Due.
	Missed int
	First  time.Time
	// Late is how long after Due the firing happens.
	Late time.Duration
	// Redelivered is set when an earlier daemon wrote this firing down and
	// stopped before its handler returned.
	Redelivered bool
}

// Handler receives the firings of one owner. The service calls every handler
// from one goroutine, so a handler must return quickly. It hands the work to
// something else.
type Handler func(Firing)

// Info is a timer as List reports it.
type Info struct {
	Timer
	// Problem says why the timer cannot fire, such as an owner with no
	// handler. It is empty for a timer in order.
	Problem string `json:"problem,omitempty"`
}

// Clock is the time source. The daemon uses the system clock, and a test
// supplies its own.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configures a Service.
type Options struct {
	// Clock defaults to the system clock.
	Clock Clock
	// Logf receives what the service cannot report any other way: a save
	// that failed, a handler that panicked. It defaults to nothing.
	Logf func(format string, args ...any)
	// OnChange runs after the set of timers, or a timer's next window,
	// changed. A host uses it to tell its clients to read the list again.
	// It runs with no lock held, and it must not block.
	OnChange func()
}

// Service holds the timers of one daemon.
type Service struct {
	path    string
	clock   Clock
	logf    func(string, ...any)
	changed func()

	mu     sync.Mutex
	timers map[string]*Timer
	// flying holds the timers whose handler this process is running now. A
	// mark in the file means a dead daemon left the firing, unless the timer
	// is here. A Poll beside the one that runs the handler leaves the timer
	// alone.
	flying map[string]bool
	// dirty is set when what List or Problem returns changed, and announce
	// clears it.
	dirty    bool
	handlers map[string]Handler
	problem  string
	kick     chan struct{}

	stop func()
	done chan struct{}
}

func key(owner, id string) string { return owner + "/" + id }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Open reads the timers in the file at path. A file that does not exist is an
// empty set. A file that does not read is an error, and Open does not replace
// it, because it may hold timers that a repair can still recover.
func Open(path string, opts Options) (*Service, error) {
	s := &Service{
		path:     path,
		clock:    opts.Clock,
		logf:     opts.Logf,
		changed:  opts.OnChange,
		timers:   map[string]*Timer{},
		flying:   map[string]bool{},
		handlers: map[string]Handler{},
		kick:     make(chan struct{}, 1),
	}
	if s.clock == nil {
		s.clock = systemClock{}
	}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	if s.changed == nil {
		s.changed = func() {}
	}
	list, err := load(path)
	if err != nil {
		return nil, err
	}
	for i := range list {
		t := list[i]
		s.timers[key(t.Owner, t.ID)] = &t
	}
	return s, nil
}

// Handle registers the handler for an owner's timers. A timer whose owner
// has no handler waits, and fires once the handler arrives.
func (s *Service) Handle(owner string, h Handler) {
	s.mu.Lock()
	s.handlers[owner] = h
	s.mu.Unlock()
	s.wake()
}

// Start runs the service until ctx ends or Stop is called. Register the
// handlers first, so that a timer that came due while the daemon was down
// finds its owner.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.done != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.stop, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.run(ctx)
	}()
}

// Stop ends the loop and waits for it. A handler that is running finishes
// first.
func (s *Service) Stop() {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	<-done
}

func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) run(ctx context.Context) {
	for {
		wait := s.Poll()
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-s.clock.After(wait):
		}
	}
}

// Set adds a timer, or replaces the one with the same owner and ID. The
// service fills Created and Next. A replaced timer keeps its Created time and
// starts its schedule again from now.
func (s *Service) Set(t Timer) (Timer, error) {
	if !nameRE.MatchString(t.Owner) || !nameRE.MatchString(t.ID) {
		return Timer{}, fmt.Errorf("timers: an owner and an id are 1 to 128 letters, digits, and . _ : -")
	}
	if len(t.Data) > MaxPayload {
		return Timer{}, fmt.Errorf("timers: a timer's data is at most %d bytes", MaxPayload)
	}
	if len(t.Data) > 0 && !json.Valid(t.Data) {
		return Timer{}, errors.New("timers: a timer's data is JSON")
	}
	if err := t.Schedule.Validate(); err != nil {
		return Timer{}, err
	}
	t.Schedule = t.Schedule.normalized()
	now := s.clock.Now().Round(0)

	// After the unlock below, whichever way Set returns.
	defer s.announce()
	s.mu.Lock()
	k := key(t.Owner, t.ID)
	old := s.timers[k]
	if old == nil && len(s.timers) >= MaxTimers {
		s.mu.Unlock()
		return Timer{}, fmt.Errorf("timers: the service holds %d timers, which is its limit", MaxTimers)
	}
	t.Created = now
	if old != nil {
		t.Created = old.Created
	}
	t.Next = t.Schedule.first(now)
	t.LastDue, t.LastFired, t.Pending = time.Time{}, time.Time{}, nil
	s.timers[k] = &t
	err := s.saveLocked()
	if err != nil {
		// The file is the record. A timer that only memory holds would be
		// gone after the next restart, with nothing to say it ever existed.
		if old != nil {
			s.timers[k] = old
		} else {
			delete(s.timers, k)
		}
	} else {
		s.dirty = true
	}
	out := t
	s.mu.Unlock()
	if err != nil {
		return Timer{}, err
	}
	s.wake()
	return out, nil
}

// Cancel removes a timer. It reports whether the timer existed.
func (s *Service) Cancel(owner, id string) (bool, error) {
	defer s.announce()
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(owner, id)
	old := s.timers[k]
	if old == nil {
		return false, nil
	}
	delete(s.timers, k)
	if err := s.saveLocked(); err != nil {
		s.timers[k] = old
		return false, err
	}
	s.dirty = true
	return true, nil
}

// announce tells the host that the list is out of date, when it is. Each
// method that can change what List or Problem returns defers it before it
// takes the lock, so it runs with no lock held.
func (s *Service) announce() {
	s.mu.Lock()
	dirty := s.dirty
	s.dirty = false
	s.mu.Unlock()
	if dirty {
		s.changed()
	}
}

// Get returns one timer.
func (s *Service) Get(owner, id string) (Timer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.timers[key(owner, id)]
	if t == nil {
		return Timer{}, false
	}
	return *t, true
}

// List returns every timer, the soonest first. A timer that waits on
// something says what in Problem.
func (s *Service) List() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.timers))
	for _, t := range s.timers {
		in := Info{Timer: *t}
		switch {
		case s.handlers[t.Owner] == nil:
			in.Problem = fmt.Sprintf("nothing in this daemon handles the timers of %q", t.Owner)
		case t.Schedule.zoneProblem() != "":
			in.Problem = t.Schedule.zoneProblem()
		case s.problem != "":
			in.Problem = s.problem
		}
		out = append(out, in)
	}
	slices.SortFunc(out, func(a, b Info) int {
		if c := a.Next.Compare(b.Next); c != 0 {
			return c
		}
		return strings.Compare(key(a.Owner, a.ID), key(b.Owner, b.ID))
	})
	return out
}

// Problem says why the service cannot fire, such as a file it cannot write.
// It is empty while the service is in order.
func (s *Service) Problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problem
}

// Poll fires every timer that is due, and returns how long until the service
// should look again. The loop calls it, and a test calls it to move the
// service one step with no goroutine. Two calls at once are safe: a timer
// whose handler runs waits for the next call.
func (s *Service) Poll() time.Duration {
	defer s.announce()
	now := s.clock.Now().Round(0)

	type job struct {
		k string
		f Firing
		h Handler
		// What the timer held before this poll marked it, to put back when
		// the mark cannot be written down.
		lastDue, lastFired time.Time
	}
	var jobs []job

	s.mu.Lock()
	for k, t := range s.timers {
		h := s.handlers[t.Owner]
		if h == nil || s.flying[k] || t.Schedule.zoneProblem() != "" {
			continue
		}
		switch {
		case t.Pending != nil:
			// A daemon before this one wrote the firing down and did not
			// finish it.
			jobs = append(jobs, job{k: k, h: h, f: Firing{
				Due: t.Pending.Due, First: t.Pending.First, Missed: t.Pending.Missed,
				Late: now.Sub(t.Pending.Due), Redelivered: true,
			}})
		case !t.Next.IsZero() && !t.Next.After(now):
			latest, n := t.Schedule.due(t.Next, now)
			j := job{k: k, h: h, lastDue: t.LastDue, lastFired: t.LastFired, f: Firing{
				Due: latest, First: t.Next, Missed: n - 1, Late: now.Sub(latest),
			}}
			t.Pending = &Pending{Due: latest, First: t.Next, Missed: n - 1}
			t.LastDue, t.LastFired = latest, now
			// From now and not from latest: every window up to now is spent,
			// whatever the count above stopped at.
			t.Next = t.Schedule.next(now)
			jobs = append(jobs, j)
		}
	}
	// With a problem and nothing due, the save is a retry. A save that
	// failed after a handler returned left the file behind what memory
	// holds, and the problem would otherwise stay until the next firing.
	if len(jobs) > 0 || s.problem != "" {
		if err := s.saveLocked(); err != nil {
			// Take the marks back. The windows stay due, and the next poll
			// tries again.
			for _, j := range jobs {
				if j.f.Redelivered {
					continue
				}
				t := s.timers[j.k]
				t.Next, t.Pending = j.f.First, nil
				t.LastDue, t.LastFired = j.lastDue, j.lastFired
			}
			s.mu.Unlock()
			return pollMax
		}
	}
	for i := range jobs {
		jobs[i].f.Timer = *s.timers[jobs[i].k]
		s.flying[jobs[i].k] = true
	}
	s.mu.Unlock()

	slices.SortFunc(jobs, func(a, b job) int {
		if c := a.f.Due.Compare(b.f.Due); c != 0 {
			return c
		}
		return strings.Compare(a.k, b.k)
	})
	for _, j := range jobs {
		s.call(j.h, j.f)
	}

	s.mu.Lock()
	for _, j := range jobs {
		delete(s.flying, j.k)
		// The handler may have replaced or cancelled its own timer. Then the
		// mark went with the old record.
		t := s.timers[j.k]
		if t == nil || t.Pending == nil || !t.Pending.Due.Equal(j.f.Due) {
			continue
		}
		t.Pending = nil
		if !t.Schedule.Recurs() {
			delete(s.timers, j.k)
		}
	}
	if len(jobs) > 0 {
		// A failure here leaves the marks in the file, and the next start
		// delivers those firings again as redelivered. That is the promise.
		_ = s.saveLocked()
		s.dirty = true
	}
	wait := pollMax
	for k, t := range s.timers {
		// A timer whose handler another Poll runs is that Poll's to finish.
		// To count it here would return no wait at all, over and over, for
		// as long as that handler runs.
		if s.handlers[t.Owner] == nil || t.Next.IsZero() || s.flying[k] || t.Schedule.zoneProblem() != "" {
			continue
		}
		if d := t.Next.Sub(now); d < wait {
			wait = d
		}
	}
	s.mu.Unlock()
	if len(jobs) > 0 {
		// A Poll beside this one skipped these timers. The loop looks again
		// now, and does not wait out its sleep.
		s.wake()
	}
	return max(wait, 0)
}

func (s *Service) call(h Handler, f Firing) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("timers: the handler of %s/%s panicked: %v", f.Timer.Owner, f.Timer.ID, r)
		}
	}()
	h(f)
}

// saveLocked writes the file and records a failure as the service's problem.
// The caller holds s.mu.
func (s *Service) saveLocked() error {
	list := make([]Timer, 0, len(s.timers))
	for _, t := range s.timers {
		list = append(list, *t)
	}
	slices.SortFunc(list, func(a, b Timer) int { return strings.Compare(key(a.Owner, a.ID), key(b.Owner, b.ID)) })
	err := save(s.path, list)
	was := s.problem
	defer func() {
		// A person who has the list open must see the service go out of
		// order, and come back.
		if s.problem != was {
			s.dirty = true
		}
	}()
	if err != nil {
		s.problem = fmt.Sprintf("the timers file does not write: %v", err)
		s.logf("timers: %s", s.problem)
		return fmt.Errorf("timers: %w", err)
	}
	s.problem = ""
	return nil
}
