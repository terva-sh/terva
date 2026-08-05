package provider

import "testing"

// The live identity probe (codex_identity_ab_test.go) lives in the EXTERNAL
// test package, because resolving a real credential means importing
// agent/build, which imports this package. That puts the endpoint it targets
// out of reach of codexDefaultBaseURL, so the probe carries its own literal.
//
// This pins the two together. If the default base URL ever moves, the probe
// would otherwise keep aiming at the old one and quietly measure a different
// backend than the one terva ships against — the failure mode being a
// perfectly plausible-looking result about nothing.
func TestCodexDefaultBaseURLMatchesTheLiveProbeTarget(t *testing.T) {
	const probeTarget = "https://chatgpt.com/backend-api/codex/responses"
	if codexDefaultBaseURL != probeTarget {
		t.Fatalf("codexDefaultBaseURL = %q, but the live identity probe targets %q.\n"+
			"Update liveProbeBaseURL in codex_identity_ab_test.go to match.",
			codexDefaultBaseURL, probeTarget)
	}
}
