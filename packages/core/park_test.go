package core

import (
	"sync"
	"testing"
)

func TestParkTableDeliverAndRelease(t *testing.T) {
	var p ParkTable[int]
	ch, release, ok := p.Park("a")
	if !ok {
		t.Fatal("fresh id should park")
	}
	if !p.Deliver("a", 42) {
		t.Fatal("deliver to a parked id should land")
	}
	if got := <-ch; got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	if p.Deliver("a", 7) {
		t.Fatal("second answer for the same id must report false, not land")
	}
	release()
	if p.Len() != 0 {
		t.Fatalf("Len = %d after release, want 0", p.Len())
	}
}

func TestParkTableRefusesDuplicateID(t *testing.T) {
	var p ParkTable[string]
	_, release, ok := p.Park("dup")
	if !ok {
		t.Fatal("first park should succeed")
	}
	defer release()
	if _, _, ok := p.Park("dup"); ok {
		t.Fatal("a duplicate id must be refused, never overwrite a live waiter")
	}
	// After release the id is free again.
	release()
	if _, r2, ok := p.Park("dup"); !ok {
		t.Fatal("a released id should be parkable again")
	} else {
		r2()
	}
}

func TestParkTableCancelAllAnswersEveryWaiter(t *testing.T) {
	var p ParkTable[string]
	var chans []<-chan string
	for _, id := range []string{"x", "y", "z"} {
		ch, release, ok := p.Park(id)
		if !ok {
			t.Fatalf("park %s failed", id)
		}
		defer release()
		chans = append(chans, ch)
	}
	p.CancelAll("cancelled")
	for i, ch := range chans {
		if got := <-ch; got != "cancelled" {
			t.Fatalf("waiter %d got %q, want cancelled", i, got)
		}
	}
	if p.Len() != 0 {
		t.Fatalf("Len = %d after CancelAll, want 0", p.Len())
	}
	// Releases after CancelAll are harmless no-ops (deferred above).
}

func TestParkTableConcurrentParksStayDistinct(t *testing.T) {
	var p ParkTable[int]
	const n = 32
	var wg sync.WaitGroup
	got := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := string(rune('A' + i))
			ch, release, ok := p.Park(id)
			if !ok {
				t.Errorf("park %s refused", id)
				return
			}
			defer release()
			got[i] = <-ch
		}()
	}
	for i := range n {
		id := string(rune('A' + i))
		for !p.Deliver(id, i) {
			// The goroutine may not have parked yet; retry until it has.
		}
	}
	wg.Wait()
	for i, v := range got {
		if v != i {
			t.Fatalf("waiter %d got %d — answers crossed", i, v)
		}
	}
}
