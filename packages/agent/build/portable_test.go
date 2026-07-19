package build

import (
	"strings"
	"testing"
)

// TestPortableModeFiltersBySameTable pins the worker side of the sufficiency
// test: --portable strips terva's harness-local self-context, --portable=strict
// also strips discovery, and both key on the SAME PortabilityOf table the worker
// composer uses — so a segment's fate here matches its fate in a briefing, and
// the two consumers cannot drift.
func TestPortableModeFiltersBySameTable(t *testing.T) {
	opts := SystemPromptOpts{
		CWD:          "/repo",
		Tools:        []ToolSummary{{Name: "read"}}, // so the docs hint is emitted
		Custom:       "PORTABLE IDENTITY",           // --system-prompt: SourceCustom (portable)
		TervaDocsDir: "/home/.terva/docs",           // -> SourceTervaDocsHint (harness-local)
		StatusTool:   true,                          // -> SourceStatusToolHint (harness-local)
		Append: []PromptSegment{
			{Source: SourceUserAppend, Text: "USER APPEND"},                               // portable
			{Source: SourceAgentsMD, Text: "AGENTS", Origin: []string{"/repo/AGENTS.md"}}, // discovery-owned
			{Source: SourceAutoSwarm, Text: "SWARM ADVERT"},                               // harness-local
			{Source: SourceLoreConstant, Text: "LORE"},                                    // no-analog (terva's own)
			{Source: SourceExtensionContext, Text: "EXT CTX"},                             // no-analog
		},
		// Experience "" so the footer (harness-local) is emitted.
	}

	sources := func(portable string) map[string]bool {
		o := opts
		o.Portable = portable
		got := map[string]bool{}
		for _, s := range SystemSegments(o) {
			got[s.Source] = true
		}
		return got
	}

	// Off: everything is present — the baseline the two portable modes narrow.
	off := sources(PortableOff)
	for _, src := range []string{SourceCustom, SourceUserAppend, SourceAgentsMD,
		SourceAutoSwarm, SourceLoreConstant, SourceExtensionContext,
		SourceTervaDocsHint, SourceStatusToolHint, SourceFooter} {
		if !off[src] {
			t.Fatalf("baseline (portable off) is missing %q — test can't distinguish drop from never-there", src)
		}
	}

	// On: harness-local self-context gone; portable + discovery + no-analog kept.
	on := sources(PortableOn)
	for _, keep := range []string{SourceCustom, SourceUserAppend, SourceAgentsMD, SourceLoreConstant, SourceExtensionContext} {
		if !on[keep] {
			t.Errorf("--portable dropped %q (portability %s), which it should keep", keep, PortabilityOf(keep))
		}
	}
	for _, drop := range []string{SourceTervaDocsHint, SourceStatusToolHint, SourceAutoSwarm, SourceFooter} {
		if on[drop] {
			t.Errorf("--portable kept harness-local %q, which it must strip", drop)
		}
	}

	// Strict: only genuinely portable content survives — the briefing stands alone.
	strict := sources(PortableStrict)
	for _, keep := range []string{SourceCustom, SourceUserAppend} {
		if !strict[keep] {
			t.Errorf("--portable=strict dropped portable %q, which always survives", keep)
		}
	}
	for _, drop := range []string{SourceAgentsMD, SourceLoreConstant, SourceExtensionContext,
		SourceTervaDocsHint, SourceStatusToolHint, SourceAutoSwarm, SourceFooter} {
		if strict[drop] {
			t.Errorf("--portable=strict kept %q (portability %s), which it must strip", drop, PortabilityOf(drop))
		}
	}
}

// TestPortableFilterMatchesComposerForHarnessLocal is the anti-drift assertion:
// every source --portable strips must be one PortabilityOf calls harness-local,
// and vice versa. If someone reclassifies a source, both the composer and this
// filter move together — the "one table, two consumers" guarantee, checked.
func TestPortableFilterMatchesComposerForHarnessLocal(t *testing.T) {
	// A source kept by --portable is one whose class is NOT harness-local.
	// A source dropped is harness-local. Verify against the classifier directly.
	for _, src := range []string{SourceCustom, SourceUserAppend, SourceAgentsMD,
		SourceLoreConstant, SourceExtensionContext, SourceTervaDocsHint,
		SourceStatusToolHint, SourceAutoSwarm, SourceFooter, SourceVessel} {
		segs := filterPortable([]PromptSegment{{Source: src, Text: "x"}}, false)
		kept := len(segs) == 1
		wantKept := PortabilityOf(src) != PortabilityHarnessLocal
		if kept != wantKept {
			t.Errorf("%q (%s): --portable kept=%v, want kept=%v", src, PortabilityOf(src), kept, wantKept)
		}
	}
}

// A sanity check that the segments are actually stripped from the rendered text,
// not merely absent from the source list.
func TestPortableStripsTextToo(t *testing.T) {
	o := SystemPromptOpts{
		Custom:     "IDENTITY",
		StatusTool: true,
		Portable:   PortableOn,
	}
	var sb strings.Builder
	for _, s := range SystemSegments(o) {
		sb.WriteString(s.Text)
	}
	if strings.Contains(sb.String(), "terva_status") {
		t.Error("portable output still names terva_status (the status hint leaked)")
	}
	if !strings.Contains(sb.String(), "IDENTITY") {
		t.Error("portable output dropped the --system-prompt identity, which must survive")
	}
}
