package extdriver

import (
	"sync"
	"testing"
)

// TestNewCorrelationIDUniqueUnderConcurrency pins the fix for the
// wall-clock-microsecond collision: many IDs minted concurrently (the
// shape of a concurrent InvokeTool burst) must all be distinct, or the
// pending-map registration of one request clobbers another's reply
// channel and a caller hangs.
func TestNewCorrelationIDUniqueUnderConcurrency(t *testing.T) {
	const n = 4000
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i] = newCorrelationID()
		}(i)
	}
	wg.Wait()
	seen := make(map[string]struct{}, n)
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate correlation id %q minted concurrently", id)
		}
		seen[id] = struct{}{}
	}
}
