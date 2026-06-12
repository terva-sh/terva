package agent

import (
	"context"
	"io"
	"sync"
	"testing"
)

// TestRPCBusyRaceFree exercises the busy() read concurrently with
// setCancel / takeCancel writes. Before the fix snapshotState read
// s.activeCancel without the writeMu the setters hold, so a get_state
// racing a prompt's setCancel was a data race. Run with -race to catch
// a regression.
func TestRPCBusyRaceFree(t *testing.T) {
	s := &rpcServer{out: io.Discard}

	var wg sync.WaitGroup
	const iters = 2000

	// Writer: flips activeCancel on/off the way runPrompt does.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_, cancel := context.WithCancel(context.Background())
			s.setCancel(cancel)
			if c := s.takeCancel(); c != nil {
				c()
			}
		}
	}()

	// Readers: hammer busy() the way snapshotState / get_state does.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = s.busy()
			}
		}()
	}

	wg.Wait()
}
