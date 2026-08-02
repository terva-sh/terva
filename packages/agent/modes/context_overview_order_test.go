package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// overviewFixture is a breakdown shaped like the sessions this panel is opened
// on: a transcript that dwarfs everything else, and a per-message list long
// enough that anything below it is off the end of the modal.
func overviewFixture() ctrlproto.ContextBreakdown {
	msgs := make([]ctrlproto.ContextMessage, 40)
	for i := range msgs {
		msgs[i] = ctrlproto.ContextMessage{Index: i, Kind: "tool", Bytes: 1000 + i}
	}
	return ctrlproto.ContextBreakdown{
		SystemBytes: 10026, ToolBytes: 27173, ToolCount: 22,
		ExtBytes: 4165, TranscriptBytes: 812340, TotalBytes: 853704,
		Window: 400000, ContextTokens: 189780,
		Messages: msgs,
		Cache: &ctrlproto.ContextCache{
			Supported:   true,
			Session:     core.WireUsage{Input: 10_000, CacheRead: 190_000, CacheSavedUSD: 4.87},
			LastRequest: core.WireUsage{Input: 2_000, CacheRead: 104_000},
		},
	}
}

// The two summary numbers must precede the breakdown they summarize.
//
// They used to be last, under a list that is one line per message — so on any
// session where context is worth inspecting, the size and the hit rate were
// behind a scroll. The breakdown answers "why"; it is only worth reading once
// the headline says there is a why to look for.
func TestTheHeadlineAndCachePrecedeTheBreakdown(t *testing.T) {
	got := plain(renderContextOverview(testTheme(), overviewFixture()))

	ctx := strings.Index(got, "context ")
	cache := strings.Index(got, "prompt cache")
	sys := strings.Index(got, "system prompt")
	transcript := strings.Index(got, "transcript ")
	for _, c := range []struct {
		name string
		at   int
	}{{"context headline", ctx}, {"prompt cache", cache}, {"system prompt", sys}, {"transcript", transcript}} {
		if c.at < 0 {
			t.Fatalf("%s missing from the panel:\n%s", c.name, got)
		}
	}
	if first := strings.SplitN(got, "\n", 2)[0]; !strings.Contains(first, "context ") {
		t.Errorf("the context headline must be the FIRST line, got %q:\n%s", first, got)
	}
	if !(ctx < cache && cache < sys && sys < transcript) {
		t.Errorf("want context < cache < system < transcript, got %d/%d/%d/%d:\n%s",
			ctx, cache, sys, transcript, got)
	}
}

// The headline reports the provider's measured context, not terva's byte guess.
// A /context modal that disagreed with the status bar two lines above it would
// be worse than no modal — and the estimate is reliably the wrong one, since it
// counts bytes terva is about to send rather than tokens the provider counted.
func TestTheHeadlinePrefersTheMeasuredTokenCount(t *testing.T) {
	b := overviewFixture()
	// The byte estimate (853704/4 ≈ 213k, 53%) and the measurement (190k, 47%)
	// must be far enough apart that the rendered percentage names a winner.
	got := plain(renderContextOverview(testTheme(), b))
	head := strings.SplitN(got, "\n", 2)[0]

	if !strings.Contains(head, "190k") || !strings.Contains(head, "(47%)") {
		t.Errorf("headline did not use the measured token count:\n%s", head)
	}
	if strings.Contains(head, "213k") || strings.Contains(head, "53%") {
		t.Errorf("headline fell back to the byte estimate despite a measurement:\n%s", head)
	}
	// No tilde: this number is measured, and marking it approximate would throw
	// away the only thing that makes it better than the row at the bottom.
	if strings.Contains(head, "~") {
		t.Errorf("a measured count must not be marked approximate:\n%s", head)
	}
}

// Before the first turn there is nothing to measure, so the estimate is all
// there is — but it must SAY so, or a number that moves when the first response
// lands reads as a bug.
func TestTheHeadlineMarksTheEstimateBeforeAnyTurn(t *testing.T) {
	b := overviewFixture()
	b.ContextTokens = 0
	head := strings.SplitN(plain(renderContextOverview(testTheme(), b)), "\n", 2)[0]

	if !strings.Contains(head, "~213k") {
		t.Errorf("want the byte estimate marked with ~, got:\n%s", head)
	}
	if !strings.Contains(head, "estimated by size") {
		t.Errorf("an estimated headline must label itself:\n%s", head)
	}
}

// A model with no declared window cannot have a percentage. Rendering one would
// require inventing the denominator.
func TestTheHeadlineOmitsThePercentWithNoWindow(t *testing.T) {
	b := overviewFixture()
	b.Window = 0
	head := strings.SplitN(plain(renderContextOverview(testTheme(), b)), "\n", 2)[0]

	if strings.Contains(head, "%") {
		t.Errorf("no window means no percentage, got:\n%s", head)
	}
	if !strings.Contains(head, "190k") {
		t.Errorf("the size itself is still known and must still show:\n%s", head)
	}
}

// The byte TOTAL stays at the foot of the rows it sums. It is a different fact
// from the headline — what terva is about to send, by size — and losing it would
// leave the breakdown rows adding up to nothing stated.
func TestTheByteTotalStaysUnderTheRowsItSums(t *testing.T) {
	got := plain(renderContextOverview(testTheme(), overviewFixture()))
	total := strings.Index(got, "TOTAL")
	sys := strings.Index(got, "system prompt")
	note := strings.Index(got, "sizes are bytes")
	if total < 0 || note < 0 {
		t.Fatalf("TOTAL or the estimate note missing:\n%s", got)
	}
	if !(sys < total && total < note) {
		t.Errorf("want system < TOTAL < note, got %d/%d/%d:\n%s", sys, total, note, got)
	}
}

// renderCacheSection is placed by its caller and must not carry its own leading
// separator: it sits directly under the headline now, and a section that brings
// its own blank line and rule can only be right in one position.
func TestTheCacheSectionBringsNoLeadingSeparator(t *testing.T) {
	lines := renderCacheSection(testTheme(), overviewFixture().Cache)
	if len(lines) == 0 {
		t.Fatal("cache section rendered nothing")
	}
	if strings.TrimSpace(sgr.ReplaceAllString(lines[0], "")) == "" {
		t.Errorf("cache section leads with a blank line:\n%q", lines[0])
	}
	if strings.Contains(lines[0], "─") {
		t.Errorf("cache section leads with a rule it does not own:\n%q", lines[0])
	}
}
