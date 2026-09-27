package persona

import "testing"

// Blanking rather than deleting the code regions: splicing the text around a
// removal can join two halves into a macro nobody wrote.
func TestStripCodeRegionsDoesNotManufactureAMacro(t *testing.T) {
	got := stripCodeRegions("{{ch`x`ar}}")
	if personaMacroRe.MatchString(got) {
		t.Errorf("stripping code spans invented a macro: %q", got)
	}
}
