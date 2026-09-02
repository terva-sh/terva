package build

import (
	"testing"
	"time"
)

// allSourceConstants is every source label the build package emits. Keeping it
// here (rather than deriving it) is deliberate: adding a segment means adding a
// row to this list, which forces the author to answer "may this leave the
// harness?" instead of inheriting an answer by accident.
var allSourceConstants = []string{
	SourceCustom,
	SourceIdentityIntro,
	SourceVessel,
	SourceIntroOverride,
	SourcePersonaIntro,
	SourceCardSystem,
	SourceCardFraming,
	SourceCharter,
	SourceConventions,
	SourceFooter,
	SourceTervaDocsHint,
	SourceTervaExamplesHint,
	SourceStatusToolHint,
	SourceUserAppend,
	SourceContextFiles,
	SourceAgentsMD,
	SourceSkills,
	SourcePinnedSkills,
	SourceLoreConstant,
	SourceCardCharacterBook,
	SourceRestrictedWorkspace,
	SourceAutoSwarm,
	SourceSwarmWorktrees,
	SourceCast,
	SourceSwarmChild,
	SourceTasks,
	SourceMemory,
	SourceExtensionContext,
	SourceCardPostHistory,
}

func TestPortabilityTableIsExhaustive(t *testing.T) {
	for _, src := range allSourceConstants {
		if _, ok := segmentPortability[src]; !ok {
			t.Errorf("source %q has no portability row: every segment must declare whether it may leave the harness", src)
		}
	}
	if got, want := len(segmentPortability), len(allSourceConstants); got != want {
		t.Errorf("portability table has %d rows but %d sources are declared; a stale row is as bad as a missing one", got, want)
	}
}

// The whole design rests on this: a segment nobody classified must be withheld,
// not leaked. An over-strip is recoverable and detectable (the terva:portable
// conformance run finds it); a leak is silent and makes a foreign agent
// hallucinate tool calls.
func TestPortabilityFailsClosed(t *testing.T) {
	for _, src := range []string{"", "brand-new-segment", "some-future-addendum"} {
		if got := PortabilityOf(src); got != PortabilityHarnessLocal {
			t.Errorf("PortabilityOf(%q) = %q, want %q — unknown sources must fail closed", src, got, PortabilityHarnessLocal)
		}
	}
}

func TestPortabilityPrefixFamilies(t *testing.T) {
	// Triggered lore fires under a per-entry label; it is the same mechanism as
	// constant lore and has the same (absent) foreign hook.
	if got := PortabilityOf("lore:waterdeep"); got != PortabilityNoAnalog {
		t.Errorf("triggered lore = %q, want %q", got, PortabilityNoAnalog)
	}
	// A backend's own augmentation is written for that agent, so it travels.
	if got := PortabilityOf("backend:claude"); got != PortabilityPortable {
		t.Errorf("backend augmentation = %q, want %q", got, PortabilityPortable)
	}
}

// The segments that would make a foreign agent reach for a tool it does not
// have. If any of these ever classifies portable, a worker will be told about
// swarm_spawn, terva_status, or a TUI it does not render to.
func TestHarnessMechanismsNeverCross(t *testing.T) {
	for _, src := range []string{
		SourceVessel,
		SourceConventions,
		SourceTervaDocsHint,
		SourceTervaExamplesHint,
		SourceStatusToolHint,
		SourceAutoSwarm,
		SourceSwarmWorktrees,
		SourceCast,
		SourceSwarmChild,
		SourceTasks,
		SourceRestrictedWorkspace,
	} {
		seg := PromptSegment{Source: src, Text: "x"}
		if seg.CrossesHarnessBoundary() {
			t.Errorf("segment %q crosses the harness boundary; it names terva machinery a foreign agent does not have", src)
		}
	}
}

// A persona charter is portable *by contract* (docs/personas.md: a persona
// "never grants tools or changes permissions"), which is exactly what lets an
// identity cross a harness boundary intact.
func TestCharterAndUserContentArePortable(t *testing.T) {
	for _, src := range []string{SourceCharter, SourceUserAppend, SourceCustom} {
		seg := PromptSegment{Source: src, Text: "x"}
		if !seg.CrossesHarnessBoundary() {
			t.Errorf("segment %q should be portable: it is authored by the user or the persona, not by terva", src)
		}
	}
}

// The real gate: every source that a fully-loaded assembly actually emits must
// be classified. Without this, a new segment silently inherits the fail-closed
// default and quietly stops reaching workers.
func TestEveryEmittedSystemSourceIsClassified(t *testing.T) {
	segs := SystemSegments(SystemPromptOpts{
		CWD:          "/repo",
		Tools:        []ToolSummary{{Name: "read", Description: "read a file"}},
		Now:          time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC),
		TervaDocsDir: "/usr/share/terva/docs",
		StatusTool:   true,
		PersonaName:  "Mieli",
		Charter:      "be terse",
		Append: []PromptSegment{
			{Source: SourceUserAppend, Text: "user says hi"},
			{Source: SourceContextFiles, Text: "ctx"},
			{Source: SourceAgentsMD, Text: "agents"},
			{Source: SourceSkills, Text: "skills"},
			{Source: SourceLoreConstant, Text: "lore"},
			{Source: SourceRestrictedWorkspace, Text: "restricted"},
			{Source: SourceAutoSwarm, Text: "swarm"},
			{Source: SourceSwarmWorktrees, Text: "worktrees"},
			{Source: SourceCast, Text: "cast"},
			{Source: SourceSwarmChild, Text: "child"},
			{Source: SourceTasks, Text: "tasks"},
			{Source: SourceExtensionContext, Text: "ext"},
		},
	})
	if len(segs) == 0 {
		t.Fatal("no segments assembled")
	}
	for _, s := range segs {
		if _, ok := segmentPortability[s.Source]; !ok {
			t.Errorf("assembled segment %q has no portability row — it will be withheld from workers by the fail-closed default, silently", s.Source)
		}
	}
}
