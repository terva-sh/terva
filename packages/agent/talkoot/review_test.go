package talkoot

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *fixture) reviewLines() []Line {
	var out []Line
	for _, l := range f.lines() {
		if l.Type == LineReview {
			out = append(out, l)
		}
	}
	return out
}

// 🚨 Each review request of a chain that a routine began counts against the
// routine's limit for one day (decision 0029 rule 17). The request past the
// limit is refused, and it counts nothing.
func TestAReviewRequestCountsAgainstItsRoutineAndStopsAtTheLimit(t *testing.T) {
	f := newFixture(t, nil)
	r := f.taskOf("helm")
	run, _ := f.start(r)
	if got := f.router.ChainRoutine(run.ID); got != r.ID {
		t.Fatalf("the chain of a run must name its routine, got %q", got)
	}
	// Under the limit, so the control: the request counts.
	if got, err := f.router.CountReviews(run.ID, "helm", 1, 2); err != nil || got != 1 {
		t.Fatalf("the first request must count as 1, got %d, %v", got, err)
	}
	lines := f.reviewLines()
	if len(lines) != 1 || lines[0].Member != "helm" || lines[0].Chain != run.ID || lines[0].Ref != RoutinePrefix+r.ID || lines[0].Count != 1 {
		t.Fatalf("the room must hold the request, in its chain and under its routine: %+v", lines)
	}
	// The member that reads the run's report asks in the same chain.
	f.end(run, RunEnd{Report: "Done."})
	if got, err := f.router.CountReviews(run.ID, "helm", 1, 2); err != nil || got != 2 {
		t.Fatalf("the request of the member that reads the report must count as 2, got %d, %v", got, err)
	}
	got, err := f.router.CountReviews(run.ID, "helm", 1, 2)
	if !errors.Is(err, ErrReviewsSpent) || !strings.Contains(err.Error(), "its chains asked for 2 today, and its limit is 2 a day") {
		t.Fatalf("the request past the limit must be refused, and say the two counts, got %v", err)
	}
	if got != 2 || f.router.RoutineReviews(r.ID) != 2 || len(f.reviewLines()) != 2 {
		t.Fatalf("a refused request must count nothing: %d, %d, %d lines", got, f.router.RoutineReviews(r.ID), len(f.reviewLines()))
	}
	// The next run of the routine is a new chain, and the count is the
	// routine's.
	next, _ := f.start(r)
	if _, err := f.router.CountReviews(next.ID, "helm", 1, 2); !errors.Is(err, ErrReviewsSpent) {
		t.Fatalf("a new chain of the routine must not get the reviews again, got %v", err)
	}
	// Another routine of the same member has its own count.
	other := f.taskOf("helm")
	other.ID = NewProposalID(f.clock.now().Add(time.Second))
	orun, _ := f.start(other)
	if got, err := f.router.CountReviews(orun.ID, "helm", 1, 2); err != nil || got != 1 {
		t.Fatalf("the count of one routine must not hold another, got %d, %v", got, err)
	}
	// The room holds the count, so a restart keeps it.
	f.reopen()
	if got := f.router.RoutineReviews(r.ID); got != 2 {
		t.Fatalf("a restart must keep the count of today, got %d", got)
	}
	if _, err := f.router.CountReviews(run.ID, "helm", 1, 2); !errors.Is(err, ErrReviewsSpent) {
		t.Fatalf("a restart must not give the routine its reviews again, got %v", err)
	}
	// The limit is for one day, in memory and in a replay of the room.
	f.clock.advance(24 * time.Hour)
	if got := f.router.RoutineReviews(r.ID); got != 0 {
		t.Fatalf("a new day must empty the count, got %d", got)
	}
	f.reopen()
	if got := f.router.RoutineReviews(r.ID); got != 0 {
		t.Fatalf("a replay must not count the requests of the day before, got %d", got)
	}
	if got, err := f.router.CountReviews(run.ID, "helm", 1, 2); err != nil || got != 1 {
		t.Fatalf("the first request of a new day must count as 1, got %d, %v", got, err)
	}
}

// 🚨 One command line can hold more than one request. The line counts whole
// or not at all: a line of three requests must not pass a limit of 2 because
// one of them is still free.
func TestALineOfRequestsPastTheLimitCountsNothing(t *testing.T) {
	f := newFixture(t, nil)
	r := f.taskOf("helm")
	run, _ := f.start(r)
	if got, err := f.router.CountReviews(run.ID, "helm", 3, 2); !errors.Is(err, ErrReviewsSpent) || got != 0 {
		t.Fatalf("three requests against a limit of 2 must be refused whole, got %d, %v", got, err)
	}
	if got, err := f.router.CountReviews(run.ID, "helm", 2, 2); err != nil || got != 2 {
		t.Fatalf("two requests against a limit of 2 must count, got %d, %v", got, err)
	}
	if lines := f.reviewLines(); len(lines) != 1 || lines[0].Count != 2 {
		t.Fatalf("the room must hold one line with the count 2: %+v", lines)
	}
	f.reopen()
	if got := f.router.RoutineReviews(r.ID); got != 2 {
		t.Fatalf("a replay must add the count of the line and not the number of lines, got %d", got)
	}
	for _, n := range []int{0, -1} {
		if _, err := f.router.CountReviews(run.ID, "helm", n, 2); err == nil || errors.Is(err, ErrReviewsSpent) {
			t.Fatalf("a count of %d must be refused as no count, got %v", n, err)
		}
	}
	if got := f.router.RoutineReviews(r.ID); got != 2 {
		t.Fatalf("a count below 1 must not move the routine's count, got %d", got)
	}
}

// 🚨 A limit of 0 means no review request. It must not read as no limit.
func TestALimitOfZeroReviewsRefusesTheFirstRequest(t *testing.T) {
	f := newFixture(t, nil)
	run, _ := f.start(f.taskOf("helm"))
	if _, err := f.router.CountReviews(run.ID, "helm", 1, 0); !errors.Is(err, ErrReviewsSpent) {
		t.Fatalf("a limit of 0 must refuse the first request, got %v", err)
	}
}

// The first request of a new day finds the day changed by itself. No other
// call of the router has to come first.
func TestTheFirstRequestOfANewDayStartsFromZero(t *testing.T) {
	f := newFixture(t, nil)
	run, _ := f.start(f.taskOf("helm"))
	if _, err := f.router.CountReviews(run.ID, "helm", 2, 2); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(24 * time.Hour)
	if got, err := f.router.CountReviews(run.ID, "helm", 1, 2); err != nil || got != 1 {
		t.Fatalf("the first request of a new day must count as 1, got %d, %v", got, err)
	}
}

// A chain that a person began has no routine, so nothing counts there.
func TestAChainThatAPersonBeganCountsNoReview(t *testing.T) {
	f := newFixture(t, nil)
	e := f.post("helm")
	if got := f.router.ChainRoutine(e.Chain.Root); got != "" {
		t.Fatalf("a chain that a person began names no routine, got %q", got)
	}
	if _, err := f.router.CountReviews(e.Chain.Root, "helm", 1, 2); !errors.Is(err, ErrNoRoutineChain) {
		t.Fatalf("a count in a person's chain must be refused, got %v", err)
	}
	if _, err := f.router.CountReviews("no-such-root", "helm", 1, 2); !errors.Is(err, ErrNoRoutineChain) {
		t.Fatalf("a count in an unknown chain must be refused, got %v", err)
	}
	if n := len(f.reviewLines()); n != 0 {
		t.Fatalf("a refused count must write no line, got %d", n)
	}
}

// 🚨 A request that the room did not record counts nothing, and the caller
// gets the error. A count in memory alone is one that a restart forgets.
func TestARequestThatTheRoomRefusedCountsNothing(t *testing.T) {
	f := newFixture(t, nil)
	r := f.taskOf("helm")
	run, _ := f.start(r)
	restore := f.plantDir(HeadFile)
	got, err := f.router.CountReviews(run.ID, "helm", 1, 2)
	restore()
	if err == nil || errors.Is(err, ErrReviewsSpent) {
		t.Fatalf("want the count to fail when the room cannot record it, got %v", err)
	}
	if got != 0 || f.router.RoutineReviews(r.ID) != 0 {
		t.Fatalf("a request that the room refused must count nothing, got %d and %d", got, f.router.RoutineReviews(r.ID))
	}
}

// 🚨 The check and the count are one step. Twenty calls at one time against
// a limit of 5 take five requests and no more.
func TestRequestsAtOneTimeDoNotPassTheLimit(t *testing.T) {
	f := newFixture(t, nil)
	r := f.taskOf("helm")
	run, _ := f.start(r)
	var wg sync.WaitGroup
	var mu sync.Mutex
	took := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.router.CountReviews(run.ID, "helm", 1, 5); err == nil {
				mu.Lock()
				took++
				mu.Unlock()
			} else if !errors.Is(err, ErrReviewsSpent) {
				t.Errorf("a call past the limit must be refused as spent, got %v", err)
			}
		}()
	}
	wg.Wait()
	if took != 5 || f.router.RoutineReviews(r.ID) != 5 || len(f.reviewLines()) != 5 {
		t.Fatalf("five requests fit the limit: %d took one, the count is %d, the room holds %d", took, f.router.RoutineReviews(r.ID), len(f.reviewLines()))
	}
}
