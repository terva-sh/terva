package provider

import "testing"

// testReg is the registry this package's tests write layers into and read
// back. The wire holds no registry of its own, so a test that means a client
// to see these layers passes testReg with WithCatalog.
var testReg = NewRegistry()

// withCatalogState snapshots all of testReg's layers and restores them when
// the test ends, so tests can install synthetic layers without
// leaking into each other.
func withCatalogState(t *testing.T) {
	t.Helper()
	r := testReg
	r.mu.Lock()
	prevLive, prevExtra, prevUser, prevMerged := r.live, r.extra, r.user, r.merged
	r.mu.Unlock()
	t.Cleanup(func() {
		r.mu.Lock()
		r.live, r.extra, r.user, r.merged = prevLive, prevExtra, prevUser, prevMerged
		r.mu.Unlock()
	})
}
