package core

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// UsageToWire is the single converter both wire paths go through — the comment
// above it records that a private twin once let a field reach one path and not
// the other. This pins the reasoning split to that converter.
func TestUsageToWireCarriesTheReasoningSplit(t *testing.T) {
	got := UsageToWire(provider.Usage{
		InputTokens: 100, OutputTokens: 700,
		ReasoningTokens: 512, ReasoningTokensKnown: true,
	})
	if got.Reasoning != 512 || !got.ReasoningKnown {
		t.Fatalf("reasoning did not survive the converter: %+v", got)
	}
	// Still inside Output, exactly as in provider.Usage — a wire shape that
	// separated them would make every client's arithmetic disagree with the
	// bill.
	if got.Output != 700 {
		t.Errorf("Output = %d, want 700 with reasoning still inside it", got.Output)
	}
}

// A provider that does not report the split must serialize to nothing at all,
// so a client cannot read absence as a measured zero. Both fields are
// omitempty for this reason.
func TestUnreportedReasoningIsAbsentFromTheWire(t *testing.T) {
	b, err := json.Marshal(UsageToWire(provider.Usage{InputTokens: 100, OutputTokens: 700}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "reasoning") {
		t.Errorf("unreported reasoning still appears on the wire: %s", b)
	}
}
