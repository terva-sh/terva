package workspace

// Session export: the played scene as something you can read outside terva.
//
// The transcript is already legible on screen, so what this adds is everything
// the screen supplies implicitly — who is speaking on a user row (Stage renders
// no name there at all), which take of a variant was the real one, and a header
// naming the story. Those are exactly the things that go missing when you paste
// a chat into a document.
//
// It renders SERVER-side, and not by preference. The client cannot see a card
// greeting (messageToWire drops the "card:greeting" source, so the opening line
// arrives indistinguishable from a reply), and a fourth speaker-attribution
// ladder in TypeScript is the last thing this codebase needs — there are three
// already and they have drifted. Rendering here also hands the same export to
// the TUI and any other client for free.

import (
	"context"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// exportMaxBytes caps a rendered export. A long play session is a few hundred
// KiB of prose; the cap exists so a pathological transcript cannot be turned
// into an unbounded frame by one click, not because any real session approaches
// it. Well under ctrlproto's frame limit even after base64's 4/3 inflation.
const exportMaxBytes = 8 << 20

// SessionsExport serializes a session for download.
func (w *Workspace) SessionsExport(ctx context.Context, sess string, p ctrlproto.SessionExportParams) (ctrlproto.SessionExport, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.SessionExport{}, err
	}
	format := strings.TrimSpace(strings.ToLower(p.Format))
	if format == "" {
		format = ctrlproto.ExportMarkdown
	}
	switch format {
	case ctrlproto.ExportMarkdown:
		return s.exportMarkdown()
	case ctrlproto.ExportTervaSession:
		return ctrlproto.SessionExport{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported,
			"the .tervasession round-trip is not wired to this verb yet; markdown is")
	}
	return ctrlproto.SessionExport{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest,
		"unknown export format %q (want %q or %q)", p.Format, ctrlproto.ExportMarkdown, ctrlproto.ExportTervaSession)
}

// exportMarkdown renders the live transcript. The live one, deliberately:
// s.agent.Messages() is already variant-resolved, so an export carries the take
// the author chose and not the ones they swiped past. Reading the file instead
// would mean re-deriving that, and getting it subtly wrong.
func (s *wsSession) exportMarkdown() (ctrlproto.SessionExport, error) {
	msgs := s.agent.Messages()
	charName, _ := s.boundCharacter()
	body := renderStoryMarkdown(msgs, storyMeta{
		Title:     strings.TrimSpace(s.sess.Meta.Title),
		SessionID: s.id,
		Started:   s.sess.Meta.Started,
		Player:    exportPlayerLabel(s.sess.Meta.UserName),
		Character: charName,
	})
	if len(body) > exportMaxBytes {
		return ctrlproto.SessionExport{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest,
			"this session renders to %d bytes, over the %d-byte export cap", len(body), exportMaxBytes)
	}
	return ctrlproto.SessionExport{
		Filename: exportFilename(storyStem(s.sess.Meta.Title, charName, s.id)) + ".md",
		MimeType: "text/markdown; charset=utf-8",
		Bytes:    []byte(body),
	}, nil
}

// exportPlayerLabel names the player in a story, which is not the same problem
// as naming them in a prompt.
//
// playerLabel() falls back to "Me" — right for the model, which is being told
// whose lines those are, and wrong on a page, where "Me" reads as a character
// called Me. A reader reading their own story is better served by second
// person. Setting a persona name in Steering → You replaces it, which is the
// outcome worth nudging toward anyway.
func exportPlayerLabel(userName string) string {
	if n := strings.TrimSpace(userName); n != "" {
		return n
	}
	return i18n.P("stage.export.player", "You")
}

// storyMeta is what the header needs that the messages do not carry.
type storyMeta struct {
	Title     string
	SessionID string
	Started   time.Time
	Player    string // the player's display name for their own lines
	Character string // the bound character, for anything unattributed
}

// renderStoryMarkdown is the whole format, and it is a set of opinions:
//
//   - YAML front matter, so the file is readable AND parseable. It names the
//     session id because we just shipped diagnostics that make that id worth
//     having, and a story you cannot trace back to its transcript is a dead end.
//   - Each turn under a "### Speaker" heading. Headings rather than inline bold
//     because a roleplay turn is usually a paragraph or three, and an inline
//     label stops separating them once they get long.
//   - CONSECUTIVE turns by the same speaker share one heading. A directed post
//     followed by that character's generated reply is one voice continuing, and
//     repeating the name would invent a beat that never happened.
//   - Prose only. Tool calls, reasoning, and empty messages are dropped —
//     mechanical texture, not story.
//
// Split from the verb so the format is testable without a session, a workspace,
// or a disk.
func renderStoryMarkdown(msgs []provider.Message, meta storyMeta) string {
	var b strings.Builder
	b.WriteString(renderStoryFrontMatter(msgs, meta))

	last := ""
	for _, m := range msgs {
		text := messageProse(m)
		if text == "" || isDirectionTurn(m, text) {
			continue
		}
		who := speakerLabel(m, meta.Player, meta.Character)
		if who != last {
			b.WriteString("\n" + speakerHeading + " " + who + "\n\n")
			last = who
		}
		b.WriteString(demoteHeadings(text) + "\n")
	}
	return b.String()
}

// speakerHeading is the level a speaker's turn sits at. Anything a turn's own
// prose contains is pushed below it by demoteHeadings, so the document nests
// the way it reads: speakers are sections, what they said is inside.
const speakerHeading = "###"

// isDirectionTurn reports whether a message is an out-of-character steer rather
// than a line in the story: a [Direction] the player gave (or the one cast.speak
// synthesizes to bring a walk-on on stage).
//
// A story EXCLUDES it, and the reason is stronger than "it is out of character."
// A direction and the reply it produced are the same beat twice — "[Direction]
// Kael storms out" followed by the prose of Kael storming out. Rendering both
// does not add stage-direction texture to the page; it makes the story stutter,
// and it attributes the steer to the player as though they had narrated it.
// Dropping it loses nothing, because its entire effect is the message after it.
//
// Deliberately NOT folded into messageProse, which the doctors also call: a
// doctor WANTS the steer. It is direct evidence of what the author was reaching
// for, which is most of what a doctor is reading the transcript to find. The
// exclusion is this renderer's opinion about stories, not a fact about messages.
func isDirectionTurn(m provider.Message, prose string) bool {
	if m.Role != provider.RoleUser {
		return false
	}
	_, ok := directionBody(prose)
	return ok
}

// demoteHeadings pushes every ATX heading in a turn's prose below the speaker
// heading, preserving relative depth.
//
// Found by exporting a real session rather than by a test: a persona that
// answers with "### 1. Apprentice, Apparently" produced content headings
// OUTRANKING the speaker heading they sat under, so every document it exported
// had an inverted outline — each turn silently closing the section it was
// supposed to be inside. Roleplay prose rarely uses headings; structured
// personas use them constantly.
//
// Fenced code is skipped, where a leading # is a comment or a shebang and not a
// heading at all. Anything already at h6 stays there — markdown has no h7, and
// clamping is better than emitting one.
func demoteHeadings(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	lines := strings.Split(s, "\n")

	// ONE shift for the whole turn, derived from its shallowest heading. Shifting
	// each line to a fixed depth instead would flatten an h1/h2 outline into two
	// h4s — the structure the author wrote, destroyed while claiming to preserve
	// it. (A test caught exactly that.)
	shift := 0
	forEachHeading(lines, func(_, level int) {
		if d := len(speakerHeading) + 1 - level; d > shift {
			shift = d
		}
	})
	if shift <= 0 {
		return s
	}
	forEachHeading(lines, func(i, level int) {
		add := shift
		if level+add > 6 {
			add = 6 - level // markdown has no h7; clamp rather than emit one
		}
		lines[i] = strings.Repeat("#", add) + lines[i]
	})
	return strings.Join(lines, "\n")
}

// forEachHeading calls fn for every ATX heading line outside a code fence,
// where a leading # is a comment or a shebang rather than a heading. Factored
// out so the measure pass and the rewrite pass cannot disagree about what
// counts as a heading.
func forEachHeading(lines []string, fn func(i, level int)) {
	fenced := false
	for i, ln := range lines {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		n := 0
		for n < len(ln) && ln[n] == '#' {
			n++
		}
		// 1-6 hashes then a space. "#hashtag" has no space; "###" has no text.
		if n == 0 || n > 6 || n >= len(ln) || ln[n] != ' ' {
			continue
		}
		fn(i, n)
	}
}

func renderStoryFrontMatter(msgs []provider.Message, meta storyMeta) string {
	title := meta.Title
	if title == "" {
		title = meta.Character
	}
	if title == "" {
		title = "Untitled"
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + yamlScalar(title) + "\n")
	b.WriteString("session: " + yamlScalar(meta.SessionID) + "\n")
	if !meta.Started.IsZero() {
		b.WriteString("started: " + meta.Started.UTC().Format("2006-01-02") + "\n")
	}
	if names := storySpeakers(msgs, meta); len(names) > 0 {
		b.WriteString("characters:\n")
		for _, n := range names {
			b.WriteString("  - " + yamlScalar(n) + "\n")
		}
	}
	b.WriteString("---\n")
	return b.String()
}

// storySpeakers lists who actually spoke, in first-appearance order — the cast
// of THIS scene rather than the roster, so a cast member who never got a line
// does not appear in a story they are not in. The player is excluded: they are
// the reader, and a story does not list its reader as a character.
func storySpeakers(msgs []provider.Message, meta storyMeta) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs {
		if messageProse(m) == "" || m.Role == provider.RoleUser {
			continue
		}
		who := speakerLabel(m, meta.Player, meta.Character)
		if who == "" || seen[who] {
			continue
		}
		seen[who] = true
		out = append(out, who)
	}
	return out
}

// yamlScalar quotes a value so a title containing a colon, a quote, or a leading
// character YAML treats as syntax cannot break the front matter. Always quoting
// is simpler to reason about than deciding when it is needed.
func yamlScalar(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", " ")
	return "\"" + s + "\""
}

// storyStem picks the download name: the session's title, else the character,
// else the id — the same fallback ladder the header uses, so the file is named
// what the document calls itself. The id is appended to a title-derived stem
// because two chats with one character routinely share a title.
func storyStem(title, character, id string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t + "-" + shortID(id)
	}
	if c := strings.TrimSpace(character); c != "" {
		return c + "-" + shortID(id)
	}
	return id
}

func shortID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
