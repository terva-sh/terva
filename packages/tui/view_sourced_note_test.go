package tui

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func sourcedMessage(source, text string) provider.Message {
	m := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: text}},
	}
	if source != "" {
		m.Meta = map[string]string{"source": source}
	}
	return m
}

func renderPlain(v *View, m provider.Message) string {
	return stripANSI(strings.Join(v.renderMessage(m, 80, false, false), "\n"))
}

// A text that terva queued draws under a rule that names its writer. The same
// text from a person draws in the person's bubble, with the accent bar.
func TestASourcedMessageIsANoteAndNotTheUserBubble(t *testing.T) {
	v := &View{Theme: Dark}
	const text = "[auto-swarm update] 1 sub-agent(s) finished:"

	note := renderPlain(v, sourcedMessage("swarm:recap", text))
	if !strings.Contains(note, "from terva · sub-agent report") {
		t.Errorf("the note does not name its writer:\n%s", note)
	}
	if !strings.Contains(note, text) {
		t.Errorf("the note lost its text:\n%s", note)
	}
	if strings.Contains(note, "▌") {
		t.Errorf("the note drew the bar of the person's bubble:\n%s", note)
	}

	typed := renderPlain(v, sourcedMessage("", text))
	if !strings.Contains(typed, "▌") {
		t.Fatalf("the positive control failed: a person's message has no bubble bar:\n%s", typed)
	}
	if strings.Contains(typed, "from terva") {
		t.Errorf("a person's message drew as a note from terva:\n%s", typed)
	}
}

// A source with no name here still shows, as it is.
func TestAnUnknownSourceShowsItsOwnValue(t *testing.T) {
	v := &View{Theme: Dark}
	note := renderPlain(v, sourcedMessage("routine:nightly", "the nightly report"))
	if !strings.Contains(note, "from terva · routine:nightly") {
		t.Errorf("an unknown source is not shown:\n%s", note)
	}
	if got := SourceLabel("talkoot:delivery"); got == "talkoot:delivery" {
		t.Error("the team delivery source has no name")
	}
}

// A sub-agent's findings can hold escape bytes from a terminal. The note
// keeps them out of the row, as the person's bubble does.
func TestASourcedNoteKeepsEscapeBytesOut(t *testing.T) {
	v := &View{Theme: Dark}
	lines := v.renderMessage(sourcedMessage("swarm:recap", "before\x1b[2Jafter"), 80, false, false)
	if joined := strings.Join(lines, "\n"); strings.Contains(joined, "\x1b[2J") {
		t.Errorf("the note passed a clear-screen sequence through: %q", joined)
	}
}

// renderMessage reads the source, so the render cache key must hold it.
func TestHashMessageCoversTheSource(t *testing.T) {
	if hashMessage(sourcedMessage("", "same")) == hashMessage(sourcedMessage("swarm:recap", "same")) {
		t.Error("a note hashes the same as the user bubble it must not render as")
	}
}
