package core

import (
	"fmt"
	"sync"
	"testing"
)

// The empty forms of a ReadOnlySet must be interchangeable. Before this they
// were not: a nil *ReadOnlySet absorbed Add silently while a &ReadOnlySet{}
// panicked on assignment to a nil map — the more constructed-looking value
// being the one that failed, and failing at the first Add rather than at
// construction.
//
// This is reachable from production rather than a purity exercise. Extension
// and MCP tools declaring read_only join the set when their managers merge into
// the registry, which the type comment says explicitly may happen mid-session,
// so the panic would land on a live turn well away from wherever the set was
// built.
func TestEveryEmptyReadOnlySetAcceptsAdd(t *testing.T) {
	cases := map[string]struct {
		set     *ReadOnlySet
		retains bool // a nil set is immutable: Add is a no-op, not an error
	}{
		"zero value":  {set: &ReadOnlySet{}, retains: true},
		"constructed": {set: NewReadOnlySet(), retains: true},
		"nil pointer": {set: nil, retains: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.set.Add("read") // must not panic in any form
			if got := tc.set.Has("read"); got != tc.retains {
				t.Errorf("after Add(\"read\") on a %s set, Has = %v; want %v", name, got, tc.retains)
			}
			if tc.set.Has("write") {
				t.Errorf("a %s set reported an unrelated tool as read-only", name)
			}
		})
	}
}

// Allocating the map on first use is the one place a lazy init can go wrong, so
// pin that it happens under the same lock as the writes. Concurrent first-Adds
// on a zero value are the racing case, and CI runs -race.
func TestZeroValueReadOnlySetAllocatesUnderTheLock(t *testing.T) {
	s := &ReadOnlySet{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Add(fmt.Sprintf("tool%d", i))
		}(i)
	}
	wg.Wait()

	for i := 0; i < 8; i++ {
		if name := fmt.Sprintf("tool%d", i); !s.Has(name) {
			t.Errorf("%q was lost — a racing first Add dropped an earlier one's map", name)
		}
	}
}
