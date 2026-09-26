package i18n

import (
	"testing"

	enginei18n "terva.sh/terva/packages/core/i18n"
)

// The engine's text must translate exactly as it did when packages/core
// imported this package. This performs the path a terva binary takes: link
// this package, Configure a language, and read through the engine's seam.
func TestEngineTranslatesThroughThisPackage(t *testing.T) {
	resetState(t)
	if err := Configure("fi", ""); err != nil {
		t.Fatalf("Configure(fi): %v", err)
	}
	// Positive control: this package translates the key.
	if got := T("interactive tui"); got != "interaktiivinen tui" {
		t.Fatalf("T fi = %q; the control failed, so the engine check below proves nothing", got)
	}
	if got := enginei18n.T("interactive tui"); got != "interaktiivinen tui" {
		t.Errorf("engine T fi = %q, want interaktiivinen tui: init did not install this package", got)
	}
	if got := enginei18n.P("prompts.no-such-key", "keep %s", "this"); got != "keep this" {
		t.Errorf("engine P fallback = %q, want keep this", got)
	}
}
