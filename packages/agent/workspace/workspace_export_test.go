package workspace

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func txt(role provider.Role, s string, meta map[string]string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: s}}, Meta: meta}
}

func storyFixture() ([]provider.Message, storyMeta) {
	return []provider.Message{
			// A card greeting: no actor, no directed/routed — it must fall all the
			// way through the ladder and read as the bound character.
			txt(provider.RoleAssistant, "*She looks up from the counter.*", map[string]string{"source": "card:greeting"}),
			txt(provider.RoleUser, "I sit down.", nil),
			txt(provider.RoleAssistant, "\"You're early.\"", nil),
			// A directed post the author wrote AS a walk-on, then that walk-on's
			// generated reply: two messages, one voice.
			txt(provider.RoleAssistant, "\"Bell's rung twice.\"", map[string]string{core.MetaSource: core.MetaDirected, core.MetaActor: "Elira"}),
			txt(provider.RoleAssistant, "*Elira sets down the crate.*", map[string]string{core.MetaSource: core.MetaRouted, core.MetaActor: "Elira"}),
			// Routed with no actor — the narrator.
			txt(provider.RoleAssistant, "*Rain starts against the shutters.*", map[string]string{core.MetaSource: core.MetaRouted}),
		}, storyMeta{
			Title:     "Kobeni's First Day",
			SessionID: "20260719-020341-fb6e45a6",
			Started:   time.Date(2026, 7, 19, 2, 3, 41, 0, time.UTC),
			Player:    "Kira",
			Character: "Kobeni",
		}
}

func TestRenderStoryMarkdown(t *testing.T) {
	msgs, meta := storyFixture()
	got := renderStoryMarkdown(msgs, meta)

	// Front matter parses as front matter: opens and closes on its own line.
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("no opening front-matter fence:\n%s", got)
	}
	if !strings.Contains(got, "\n---\n") {
		t.Fatalf("no closing front-matter fence:\n%s", got)
	}
	for _, want := range []string{
		`title: "Kobeni's First Day"`,
		`session: "20260719-020341-fb6e45a6"`,
		"started: 2026-07-19",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("front matter missing %q:\n%s", want, got)
		}
	}

	// The cast is who SPOKE, in first-appearance order, and never the player:
	// a story does not list its reader as a character.
	cast := got[strings.Index(got, "characters:"):strings.Index(got, "\n---\n")]
	if !strings.Contains(cast, "Kobeni") || !strings.Contains(cast, "Elira") || !strings.Contains(cast, "Narrator") {
		t.Errorf("cast should list every speaker:\n%s", cast)
	}
	if strings.Contains(cast, "Kira") {
		t.Errorf("cast must exclude the player:\n%s", cast)
	}
	if strings.Index(cast, "Kobeni") > strings.Index(cast, "Elira") {
		t.Errorf("cast should be in first-appearance order:\n%s", cast)
	}

	// Every speaker is attributed — including the player, whom Stage renders
	// with no name at all on screen.
	for _, want := range []string{"### Kobeni", "### Kira", "### Elira", "### Narrator"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing heading %q:\n%s", want, got)
		}
	}

	// The greeting is the bound character's line, not an unattributed one.
	body := got[strings.LastIndex(got, "\n---\n"):]
	if i := strings.Index(body, "*She looks up from the counter.*"); i < 0 {
		t.Fatal("greeting text missing from the body")
	} else if !strings.Contains(body[:i], "### Kobeni") {
		t.Errorf("greeting should sit under the bound character:\n%s", body)
	}

	// Consecutive turns by one voice share a heading: the directed post and the
	// reply it drew are Elira continuing, not Elira twice.
	if n := strings.Count(got, "### Elira"); n != 1 {
		t.Errorf("consecutive same-speaker turns should share one heading, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "\"Bell's rung twice.\"\n*Elira sets down the crate.*") {
		t.Errorf("both Elira turns should sit under the one heading:\n%s", got)
	}
}

func TestRenderStoryMarkdownDropsNonProse(t *testing.T) {
	// Tool calls and empty messages are mechanical texture, not story. An empty
	// message must not mint a stray heading either.
	msgs := []provider.Message{
		txt(provider.RoleAssistant, "Real prose.", nil),
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.ToolCallBlock{Name: "world_note"}}},
		txt(provider.RoleAssistant, "", nil),
		txt(provider.RoleUser, "  ", nil),
	}
	got := renderStoryMarkdown(msgs, storyMeta{SessionID: "s", Player: "Me", Character: "Kobeni"})
	if strings.Contains(got, "world_note") {
		t.Errorf("tool calls must not reach the story:\n%s", got)
	}
	if n := strings.Count(got, "\n### "); n != 1 {
		t.Errorf("empty messages must not mint headings, got %d:\n%s", n, got)
	}
}

func TestRenderStoryMarkdownDropsDirections(t *testing.T) {
	// A [Direction] steer and the reply it drew are one beat, not two: printing
	// the steer makes the page stutter and credits the player with narrating it.
	msgs := []provider.Message{
		txt(provider.RoleUser, "I sit down.", nil),
		txt(provider.RoleUser, directionDirective("Kael storms out."), nil),
		// What cast.speak synthesizes — the same convention, equally not story.
		txt(provider.RoleUser, directionDirective("Bring Elira into the scene now."), nil),
		txt(provider.RoleUser, "I follow him.", nil),
		txt(provider.RoleAssistant, "*The door bangs shut.*", nil),
	}
	got := renderStoryMarkdown(msgs, storyMeta{SessionID: "s", Player: "Kira", Character: "Kobeni"})

	if strings.Contains(got, "[Direction]") {
		t.Errorf("the marker must never reach the page:\n%s", got)
	}
	for _, gone := range []string{"Kael storms out", "Bring Elira into the scene"} {
		if strings.Contains(got, gone) {
			t.Errorf("steer %q must not reach the page:\n%s", gone, got)
		}
	}
	// The subtle half: dropping a steer BETWEEN two player turns leaves them
	// adjacent, so they share one heading. Skipping the row without skipping the
	// heading decision would split one voice into two.
	if n := strings.Count(got, "### Kira"); n != 1 {
		t.Errorf("player turns around a dropped steer should share one heading, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "I sit down.\nI follow him.") {
		t.Errorf("both player turns should sit under the one heading:\n%s", got)
	}
	// The story itself is untouched.
	if !strings.Contains(got, "*The door bangs shut.*") {
		t.Errorf("the reply the steer produced is still story:\n%s", got)
	}
}

func TestDirectionBody(t *testing.T) {
	// Round-trips with the writer. Same name as the Stage store's TS function so
	// one grep finds both ends of the convention.
	body, ok := directionBody(directionDirective("Kael storms out."))
	if !ok || body != "Kael storms out." {
		t.Errorf("directionBody = %q, %v", body, ok)
	}
	if _, ok := directionBody("I sit down."); ok {
		t.Error("ordinary dialogue is not a steer")
	}
	// The marker only means "steer" on a PLAYER turn. A character who says the
	// word in dialogue is speaking, and dropping their line would be data loss.
	inFiction := txt(provider.RoleAssistant, "[Direction] is a stage term.", nil)
	if isDirectionTurn(inFiction, messageProse(inFiction)) {
		t.Error("an assistant line beginning with the marker is dialogue, not a steer")
	}
}

func TestStoryFrontMatterEscapes(t *testing.T) {
	// A title with a colon and a quote is ordinary — "Chapter 2: the "deal"" —
	// and unquoted would produce YAML that does not parse.
	got := renderStoryFrontMatter(nil, storyMeta{
		Title:     `Chapter 2: the "deal"`,
		SessionID: "20260719-020341-fb6e45a6",
	})
	if !strings.Contains(got, `title: "Chapter 2: the \"deal\""`) {
		t.Errorf("title not escaped:\n%s", got)
	}
	// Falls back to the character, then to Untitled — never an empty title.
	if s := renderStoryFrontMatter(nil, storyMeta{Character: "Kobeni"}); !strings.Contains(s, `title: "Kobeni"`) {
		t.Errorf("untitled session should fall back to the character:\n%s", s)
	}
	if s := renderStoryFrontMatter(nil, storyMeta{}); !strings.Contains(s, `title: "Untitled"`) {
		t.Errorf("nameless session should fall back to Untitled:\n%s", s)
	}
	// A zero start time is omitted rather than written as year 1.
	if strings.Contains(got, "started:") {
		t.Errorf("zero start time should be omitted:\n%s", got)
	}
}

func TestStoryStem(t *testing.T) {
	// Two chats with one character routinely share a title, so the id rides
	// along and the second download does not silently overwrite the first.
	if got := storyStem("Kobeni's First Day", "Kobeni", "20260719-020341-fb6e45a6"); got != "Kobeni's First Day-fb6e45a6" {
		t.Errorf("titled stem = %q", got)
	}
	if got := storyStem("", "Kobeni", "20260719-020341-fb6e45a6"); got != "Kobeni-fb6e45a6" {
		t.Errorf("untitled stem = %q", got)
	}
	if got := storyStem("", "", "20260719-020341-fb6e45a6"); got != "20260719-020341-fb6e45a6" {
		t.Errorf("bare stem = %q", got)
	}
}

func TestSpeakerLabelLadder(t *testing.T) {
	// The ladder the doctors' scene tails and the export now share. A greeting
	// (an unmodelled source with no actor) must reach the bound character.
	cases := []struct {
		name string
		m    provider.Message
		want string
	}{
		{"user is the player", txt(provider.RoleUser, "x", nil), "Kira"},
		{"actor names itself", txt(provider.RoleAssistant, "x", map[string]string{core.MetaActor: "Elira"}), "Elira"},
		{"directed without an actor is the narrator", txt(provider.RoleAssistant, "x", map[string]string{core.MetaSource: core.MetaDirected}), narratorName()},
		{"routed without an actor is the narrator", txt(provider.RoleAssistant, "x", map[string]string{core.MetaSource: core.MetaRouted}), narratorName()},
		{"greeting is the bound character", txt(provider.RoleAssistant, "x", map[string]string{"source": "card:greeting"}), "Kobeni"},
		{"plain reply is the bound character", txt(provider.RoleAssistant, "x", nil), "Kobeni"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := speakerLabel(c.m, "Kira", "Kobeni"); got != c.want {
				t.Errorf("speakerLabel = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDemoteHeadings(t *testing.T) {
	// The bug this exists for: a structured persona answers with h3, which under
	// an h3 speaker heading closes the section it was meant to sit inside.
	if got := demoteHeadings("### 1. Apprentice, Apparently"); got != "#### 1. Apprentice, Apparently" {
		t.Errorf("h3 content should sink below the speaker heading, got %q", got)
	}
	// Relative depth survives: an h1/h2 pair stays a pair.
	if got := demoteHeadings("# Top\n## Under"); got != "#### Top\n##### Under" {
		t.Errorf("relative depth not preserved, got %q", got)
	}
	// Markdown has no h7 — clamp rather than emit one.
	if got := demoteHeadings("###### Deep"); got != "###### Deep" {
		t.Errorf("h6 should clamp, got %q", got)
	}
	// Not headings: a hashtag has no space, and a bare rule has no text.
	for _, s := range []string{"#hashtag", "###", "not # a heading"} {
		if got := demoteHeadings(s); got != s {
			t.Errorf("%q is not a heading but was rewritten to %q", s, got)
		}
	}
	// Inside a fence a leading # is a comment or a shebang, not a heading.
	fenced := "```sh\n# not a heading\n```\n## but this is"
	want := "```sh\n# not a heading\n```\n#### but this is"
	if got := demoteHeadings(fenced); got != want {
		t.Errorf("fenced code must be left alone:\ngot  %q\nwant %q", got, want)
	}
	// Prose without a hash is returned untouched (the common case).
	plain := "*She sets down the cup.* \"You're early.\""
	if got := demoteHeadings(plain); got != plain {
		t.Errorf("plain prose was rewritten to %q", got)
	}
}

func TestExportPlayerLabel(t *testing.T) {
	// A bound persona names the player; an unbound one gets second person
	// rather than the prompt-side "Me", which on a page reads as a character.
	if got := exportPlayerLabel("Kira"); got != "Kira" {
		t.Errorf("bound persona = %q", got)
	}
	if got := exportPlayerLabel("   "); got == "Me" || got == "" {
		t.Errorf("unbound persona should not fall back to the prompt label, got %q", got)
	}
}
