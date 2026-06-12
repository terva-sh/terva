package provider

import "testing"

// withCatalogState snapshots all catalog layers and restores them when
// the test ends, so tests can install synthetic layers without
// leaking into each other.
func withCatalogState(t *testing.T) {
	t.Helper()
	activeMu.Lock()
	prevLive, prevExtra, prevUser, prevMerged := layerLive, layerExtra, layerUser, merged
	activeMu.Unlock()
	t.Cleanup(func() {
		activeMu.Lock()
		layerLive, layerExtra, layerUser, merged = prevLive, prevExtra, prevUser, prevMerged
		activeMu.Unlock()
	})
}
