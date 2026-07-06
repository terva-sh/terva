package replay

import (
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

func zeroFrames(n int) []Frame {
	f := make([]Frame, n)
	for i := range f {
		f[i] = Frame{Event: core.EvTextDelta{Delta: "x"}}
	}
	return f
}

func TestPlayerStep(t *testing.T) {
	frames := []Frame{
		{Event: core.EvUserMessage{}},
		{Event: core.EvTurnStart{Step: 1}},
		{Event: core.EvDone{}},
	}
	var got []int
	p := NewPlayer(frames, func(idx int, f Frame) { got = append(got, idx) })
	defer p.Close()

	if st := p.State(); st.Total != 3 || st.Pos != 0 || st.Playing {
		t.Fatalf("initial state = %+v", st)
	}
	for p.Step() {
	}
	if len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Fatalf("stepped indices = %v", got)
	}
	if st := p.State(); st.Pos != 3 {
		t.Errorf("pos after stepping = %d, want 3", st.Pos)
	}
	if p.Step() {
		t.Error("Step past end returned true")
	}
}

func TestPlayerSeekClamps(t *testing.T) {
	var got []int
	p := NewPlayer(zeroFrames(5), func(idx int, f Frame) { got = append(got, idx) })
	defer p.Close()

	p.Seek(3)
	if st := p.State(); st.Pos != 3 {
		t.Errorf("pos after seek = %d, want 3", st.Pos)
	}
	p.Step()
	if len(got) != 1 || got[0] != 3 {
		t.Errorf("emitted after seek+step = %v, want [3]", got)
	}
	p.Seek(100)
	if st := p.State(); st.Pos != 5 {
		t.Errorf("seek past end clamped to %d, want 5", st.Pos)
	}
	p.Seek(-5)
	if st := p.State(); st.Pos != 0 {
		t.Errorf("seek below zero clamped to %d, want 0", st.Pos)
	}
}

func TestPlayerPlayEmitsAllInOrder(t *testing.T) {
	const n = 5
	frames := make([]Frame, n)
	for i := range frames {
		frames[i] = Frame{Event: core.EvTextDelta{Delta: "x"}, Delay: time.Millisecond}
	}
	var mu sync.Mutex
	var got []int
	done := make(chan struct{})
	p := NewPlayer(frames, func(idx int, f Frame) {
		mu.Lock()
		got = append(got, idx)
		last := len(got) == n
		mu.Unlock()
		if last {
			close(done)
		}
	})
	defer p.Close()

	p.Play()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("play did not emit all frames in time")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, idx := range got {
		if idx != i {
			t.Fatalf("out-of-order emission: got %v", got)
		}
	}
	if st := p.State(); st.Playing || st.Pos != n {
		t.Errorf("end state = %+v, want stopped at %d", st, n)
	}
}

// TestPlayerConcurrentStepStaysOrdered reproduces the emit-ordering race: a
// Play() run loop racing repeated Step()s over zero-delay frames. Every frame
// must reach the sink exactly once, in strictly increasing index order — never a
// lower index after a higher one. Before the emitMu serialization, a Step could
// call the sink while a timer-driven emit sat between its guard and its own sink,
// producing sequences like [7 8 9 10 5 11].
func TestPlayerConcurrentStepStaysOrdered(t *testing.T) {
	const (
		iters  = 300
		n      = 50
		nsteps = 10
	)
	for iter := range iters {
		var mu sync.Mutex
		var got []int
		p := NewPlayer(zeroFrames(n), func(idx int, f Frame) {
			mu.Lock()
			got = append(got, idx)
			mu.Unlock()
		})

		p.Play()
		for range nsteps {
			p.Step()
		}

		deadline := time.Now().Add(2 * time.Second)
		for {
			mu.Lock()
			done := len(got) >= n
			mu.Unlock()
			if done || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		p.Close()

		mu.Lock()
		seq := append([]int(nil), got...)
		mu.Unlock()
		if len(seq) != n {
			t.Fatalf("iter %d: emitted %d frames, want %d: %v", iter, len(seq), n, seq)
		}
		prev := -1
		for _, idx := range seq {
			if idx <= prev {
				t.Fatalf("iter %d: out-of-order or duplicate emission (idx %d after %d): %v", iter, idx, prev, seq)
			}
			prev = idx
		}
	}
}

// TestPlayerPauseHolds verifies pause prevents further emission without
// asserting an exact pre-pause count (timing-robust for -race/CI).
func TestPlayerPauseHolds(t *testing.T) {
	const n = 10
	frames := make([]Frame, n)
	for i := range frames {
		frames[i] = Frame{Event: core.EvTextDelta{Delta: "x"}, Delay: 40 * time.Millisecond}
	}
	var mu sync.Mutex
	count := 0
	p := NewPlayer(frames, func(idx int, f Frame) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	defer p.Close()

	p.Play()
	time.Sleep(100 * time.Millisecond)
	p.Pause()
	mu.Lock()
	c1 := count
	mu.Unlock()
	if c1 >= n {
		t.Fatalf("scene finished before pause (%d frames) — increase delays", c1)
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	c2 := count
	mu.Unlock()
	if c2 != c1 {
		t.Errorf("emitted while paused: %d -> %d", c1, c2)
	}
}
