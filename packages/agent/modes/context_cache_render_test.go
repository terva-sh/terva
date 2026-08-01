package modes

import (
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// sgr strips colour so assertions read the text, not the escapes.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func testTheme() tui.Theme {
	return tui.Theme{Muted: 8, Warning: 3, Error: 1, Accent: 4, MeterLow: 2, MeterMid: 3, MeterHigh: 1}
}

func plain(lines []string) string { return sgr.ReplaceAllString(strings.Join(lines, "\n"), "") }

func cacheLines(c *ctrlproto.ContextCache) string {
	return plain(renderCacheSection(testTheme(), c))
}

// A daemon that predates the field sends nothing. The section must vanish
// rather than render a cache that reads as broken.
func TestNoCacheFieldRendersNoSection(t *testing.T) {
	if got := renderCacheSection(testTheme(), nil); got != nil {
		t.Errorf("nil cache rendered %d lines; want none", len(got))
	}
}

// The three zero-ish states are different facts and must not share a rendering.
func TestTheZeroStatesReadDifferently(t *testing.T) {
	fresh := cacheLines(&ctrlproto.ContextCache{})
	if !strings.Contains(fresh, "no requests yet") {
		t.Errorf("a session with no traffic rendered:\n%s", fresh)
	}

	// Real traffic, and the provider reported no cache at all. Not 0%.
	uncached := cacheLines(&ctrlproto.ContextCache{
		Session: core.WireUsage{Input: 40_000, Output: 900},
	})
	if !strings.Contains(uncached, "no cache activity") {
		t.Errorf("an uncached provider rendered:\n%s", uncached)
	}
	if strings.Contains(uncached, "0%") {
		t.Errorf("an uncached provider was reported as a 0%% hit rate:\n%s", uncached)
	}

	// A provider that DOES cache, on a session that is missing. This one is a
	// real 0% and must say so — it is the actionable state of the three.
	missing := cacheLines(&ctrlproto.ContextCache{
		Supported:   true,
		Session:     core.WireUsage{Input: 100_000, CacheWrite: 1},
		LastRequest: core.WireUsage{Input: 100_000, CacheWrite: 1},
	})
	if !strings.Contains(missing, "0% hit") {
		t.Errorf("a genuinely missing cache did not report 0%%:\n%s", missing)
	}
}

func TestTheHeadlineIsTheSessionHitRateAndSaving(t *testing.T) {
	got := cacheLines(&ctrlproto.ContextCache{
		Supported: true,
		Session: core.WireUsage{
			Input: 10_000, CacheRead: 190_000, Output: 5_000,
			CostUSD: 0.7, CacheSavedUSD: 0.486,
		},
		LastRequest: core.WireUsage{Input: 2_000, CacheRead: 180_000, CacheWrite: 1_500},
	})
	// 190k / 200k = 95%.
	if !strings.Contains(got, "95% hit") {
		t.Errorf("session hit rate missing from:\n%s", got)
	}
	if !strings.Contains(got, "saved $0.49") {
		t.Errorf("session saving missing from:\n%s", got)
	}
	// The last request breaks out all three prompt shares, so a reader can see
	// WHY the rate is what it is.
	for _, want := range []string{"180k read", "2k written", "2k fresh"} {
		if !strings.Contains(got, want) {
			t.Errorf("last-request line missing %q:\n%s", want, got)
		}
	}
}

// A cache that costs money is the finding this panel exists to surface, and
// "saved -$0.42" is a phrase people read as a saving. It has to change words.
func TestANegativeSavingSaysCostExtraNotSavedMinus(t *testing.T) {
	got := cacheLines(&ctrlproto.ContextCache{
		Supported: true,
		Session: core.WireUsage{
			Input: 1_000, CacheWrite: 100_000, CacheSavedUSD: -0.42,
		},
		LastRequest: core.WireUsage{Input: 1_000, CacheWrite: 100_000},
	})
	if !strings.Contains(got, "cost extra $0.42") {
		t.Errorf("a negative saving did not read as a cost:\n%s", got)
	}
	if strings.Contains(got, "saved") {
		t.Errorf("a negative saving still used the word 'saved':\n%s", got)
	}
}

// The strip is per-request and its whole value is showing WHERE the cache
// broke. One bar per sample, in order, and the bad one visibly shorter.
func TestTheStripDrawsOneBarPerRequestInOrder(t *testing.T) {
	c := &ctrlproto.ContextCache{
		Supported: true,
		Session:   core.WireUsage{Input: 10_000, CacheRead: 90_000},
		Recent: []ctrlproto.CacheSample{
			{HitRate: 1.0, PromptTokens: 100_000},
			{HitRate: 0.0, PromptTokens: 100_000}, // the prefix broke here
			{HitRate: 1.0, PromptTokens: 100_000},
		},
	}
	got := cacheLines(c)
	var strip string
	for _, line := range strings.Split(got, "\n") {
		if strings.ContainsAny(line, string(sparkLevels)) {
			strip = strings.TrimSpace(line)
		}
	}
	if strip == "" {
		t.Fatalf("no strip drawn:\n%s", got)
	}
	bars := []rune(strings.TrimPrefix(strip, "last 3"))
	bars = []rune(strings.TrimSpace(string(bars)))
	if len(bars) != 3 {
		t.Fatalf("strip has %d bars, want 3: %q", len(bars), string(bars))
	}
	if bars[0] != '█' || bars[2] != '█' {
		t.Errorf("a full hit did not draw a full bar: %q", string(bars))
	}
	if bars[1] != sparkLevels[0] {
		t.Errorf("a total miss did not draw the shortest bar: %q", string(bars))
	}
}

// One sample is not a shape. Drawing a single bar invites reading its height as
// a value, which is the one thing this strip does not mean.
func TestASingleRequestDrawsNoStrip(t *testing.T) {
	got := cacheLines(&ctrlproto.ContextCache{
		Supported:   true,
		Session:     core.WireUsage{Input: 1_000, CacheRead: 9_000},
		LastRequest: core.WireUsage{Input: 1_000, CacheRead: 9_000},
		Recent:      []ctrlproto.CacheSample{{HitRate: 0.9, PromptTokens: 10_000}},
	})
	if strings.ContainsAny(got, string(sparkLevels)) {
		t.Errorf("a single sample drew a strip:\n%s", got)
	}
}

// A rate of exactly 1 must not index past the level table.
func TestSparkClampsTheTopAndBottom(t *testing.T) {
	th := testTheme()
	for _, s := range []ctrlproto.CacheSample{
		{HitRate: 1.0}, {HitRate: 1.5}, {HitRate: -0.2}, {HitRate: 0},
	} {
		out := sgr.ReplaceAllString(cacheSpark(th, []ctrlproto.CacheSample{s}), "")
		if len([]rune(out)) != 1 {
			t.Errorf("HitRate %v drew %q; want one cell", s.HitRate, out)
		}
	}
}
