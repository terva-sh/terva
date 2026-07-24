package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// allModeConstants is every run mode terva has. Like allSourceConstants next
// door, it is written out rather than derived: adding a mode should make you
// answer "where do this mode's words land?" instead of inheriting an answer.
var allModeConstants = []Mode{
	ModeInteractive,
	ModePrint,
	ModeJSON,
	ModeRPC,
	ModeACP,
	ModeSwarmAgent,
	ModeWeb,
	ModeAttach,
	ModeReplay,
	ModeBot,
}

func TestSurfaceTableIsExhaustive(t *testing.T) {
	for _, m := range allModeConstants {
		if _, ok := modeSurface[m]; !ok {
			t.Errorf("mode %q has no surface row: every run mode must declare where its output lands", m)
		}
	}
	if got, want := len(modeSurface), len(allModeConstants); got != want {
		t.Errorf("surface table has %d rows but %d modes are declared; a stale row is as bad as a missing one", got, want)
	}
}

// The asymmetry that makes this type worth having. Telling a rendered surface
// "markdown will not render" costs prettiness; telling an unrendered one that it
// WILL is the bug — a bot posting markdown tables into Discord, an embedder
// handed headings it never asked for. So an unclassified mode must claim
// nothing, which is what SurfacePlain does.
func TestSurfaceFailsClosed(t *testing.T) {
	for _, m := range []Mode{"", "some-future-mode"} {
		if got := SurfaceOf(m); got != SurfacePlain {
			t.Errorf("SurfaceOf(%q) = %q, want %q — an unknown mode must not be promised a renderer", m, got, SurfacePlain)
		}
	}
}

// The regression this whole commit exists to prevent: the conventions segment
// asserting a TUI to an agent that does not have one.
func TestConventionsDoNotClaimAnUnrenderedSurfaceRenders(t *testing.T) {
	for _, m := range []Mode{ModePrint, ModeJSON, ModeRPC, ModeBot} {
		text := conventions(SystemPromptOpts{Surface: SurfaceOf(m)})
		if strings.Contains(text, "Use markdown freely") {
			t.Errorf("mode %q (surface %q) is told to use markdown freely, but nothing renders it:\n%s", m, SurfaceOf(m), text)
		}
	}
	// ...and the converse: the TUI must still be told it renders markdown, or
	// the fix has quietly cost every human reader their formatting.
	if text := conventions(SystemPromptOpts{Surface: SurfaceOf(ModeInteractive)}); !strings.Contains(text, "Use markdown freely") {
		t.Errorf("the TUI renders markdown and must still be told so:\n%s", text)
	}
}

// A bot's user is a chat room, and chat clients render a subset of markdown.
func TestBotIsToldItIsSpeakingIntoAChat(t *testing.T) {
	text := conventions(SystemPromptOpts{Surface: SurfaceOf(ModeBot)})
	for _, want := range []string{"chat message", "skip headings and tables"} {
		if !strings.Contains(text, want) {
			t.Errorf("bot conventions should mention %q:\n%s", want, text)
		}
	}
}

// The chat/play tail used to carry its own copy of the falsehood ("Your output
// renders in a terminal as Markdown") — and chat mode is precisely what a
// Discord bot runs, so it was the wronger of the two. The surface line leads
// now, in every Experience.
func TestExperienceConventionsAreAlsoSurfaceAware(t *testing.T) {
	for _, exp := range []string{ExperienceChat, ExperiencePlay} {
		bot := conventions(SystemPromptOpts{Experience: exp, Surface: SurfaceOf(ModeBot)})
		if !strings.Contains(bot, "chat message") {
			t.Errorf("%s in a bot should be told it posts chat messages:\n%s", exp, bot)
		}
		if !strings.Contains(bot, "in character") {
			t.Errorf("%s must keep its in-character discipline:\n%s", exp, bot)
		}
		tui := conventions(SystemPromptOpts{Experience: exp, Surface: SurfaceOf(ModeInteractive)})
		if !strings.Contains(tui, "Use markdown freely") {
			t.Errorf("%s in the TUI still renders markdown:\n%s", exp, tui)
		}
	}
}

// The segment names terva's edit and write tools, so it ships only when those
// tools do. Under --no-tools it used to name two tools the model could not call
// — the same class of falsehood as the TUI line, one paragraph down.
func TestFileEditConventionsOnlyWhenTheToolsExist(t *testing.T) {
	withTools := conventions(SystemPromptOpts{
		Surface: SurfaceRendered,
		Tools:   []ToolSummary{{Name: "edit"}, {Name: "write"}, {Name: "read"}},
	})
	if !strings.Contains(withTools, "prefer the edit tool") {
		t.Errorf("edit/write are registered; the guidance should ship:\n%s", withTools)
	}
	noTools := conventions(SystemPromptOpts{Surface: SurfaceRendered})
	if strings.Contains(noTools, "prefer the edit tool") {
		t.Errorf("--no-tools: the prompt must not name tools the model cannot call:\n%s", noTools)
	}
	// The output discipline is not about tools and survives either way.
	if !strings.Contains(noTools, "let tool calls carry the operational detail") {
		t.Errorf("output discipline is surface- and tool-independent:\n%s", noTools)
	}
}

// The wiring, end to end: Args.Mode -> Resolve -> SurfaceOf -> the rendered
// prompt. The conventions() tests above prove the text is right for a given
// Surface; this proves the Surface is right for a given run — which is the half
// that would silently rot if someone dropped the field from one of Resolve's
// two SystemSegments call sites.
//
// It is also the only coverage bot has: `bot run` is routed before the mode
// switch, so --dump-prompt cannot reach it from the command line.
func TestResolveDerivesTheSurfaceFromTheMode(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeInteractive, "Use markdown freely"},
		{ModeBot, "posted as a chat message"},
		{ModePrint, "plain-text stream"},
		{ModeRPC, "handed to a program"},
	}
	for _, c := range cases {
		r, err := Resolve(Args{Mode: c.mode, CWD: testsupport.TempDir(t)}, false)
		if err != nil {
			t.Fatalf("resolve %s: %v", c.mode, err)
		}
		if want := SurfaceOf(c.mode); r.Surface != want {
			t.Errorf("mode %q resolved to surface %q, want %q", c.mode, r.Surface, want)
		}
		if !strings.Contains(r.SystemPrompt, c.want) {
			t.Errorf("mode %q: system prompt should describe its surface (%q):\n%s", c.mode, c.want, r.SystemPrompt)
		}
	}
}

// The conventions segment stays home whatever surface it describes: a foreign
// agent has its own screen and its own tools, and hears about neither from us.
func TestConventionsStayHarnessLocalOnEverySurface(t *testing.T) {
	for _, m := range allModeConstants {
		segs := SystemSegments(SystemPromptOpts{
			Surface: SurfaceOf(m),
			Tools:   []ToolSummary{{Name: "edit"}, {Name: "write"}},
		})
		for _, s := range segs {
			if s.Source == SourceConventions && s.CrossesHarnessBoundary() {
				t.Errorf("mode %q: conventions crossed the harness boundary", m)
			}
		}
	}
}
