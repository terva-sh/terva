package modes

// Recall (Left/Right from an empty editor) used to be derived purely from
// the transcript's user messages. A `!shell` escape and a `/slash` command
// never become messages, so neither was ever recallable — the one class of
// input most worth re-running was the one class recall could not reach.
// These tests pin the splice that puts them back inline with the prompts.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui/tuitest"
)

// editorRow returns the composer's own row — the last line carrying the
// input bar glyph — so an assertion about what is in the editor can't be
// satisfied by identical text elsewhere on screen.
func editorRow(s *tuitest.Screen) string {
	for _, row := range s.Rows() {
		if strings.HasPrefix(strings.TrimSpace(row), "▌") {
			return row
		}
	}
	return ""
}

func userMsg(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func TestInputHistorySplicesCommandsInline(t *testing.T) {
	i := &Interactive{}
	// The session opened on a resumed transcript holding one cold prompt.
	i.carrierMessages = []provider.Message{userMsg("cold prompt")}

	// Then, this session: a prompt, a shell escape, a slash command,
	// another prompt. Only the prompts reach the transcript.
	i.recordInput("first prompt", true)
	i.carrierMessages = append(i.carrierMessages, userMsg("first prompt"))
	i.recordInput("!git status", false)
	i.recordInput("/model", false)
	i.recordInput("second prompt", true)
	i.carrierMessages = append(i.carrierMessages, userMsg("second prompt"))

	want := []string{"cold prompt", "first prompt", "!git status", "/model", "second prompt"}
	got := i.inputHistory()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("inputHistory() = %q, want %q", got, want)
	}
}

// The transcript owns everything from before this process started, and a
// prompt another client sent lands there too. Neither may be dropped.
func TestInputHistoryKeepsForeignTranscriptPrompts(t *testing.T) {
	i := &Interactive{}
	i.carrierMessages = []provider.Message{userMsg("from the web client"), userMsg("mine")}
	i.recordInput("mine", true)

	want := []string{"from the web client", "mine"}
	if got := i.inputHistory(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("inputHistory() = %q, want %q", got, want)
	}
}

// /clear empties the transcript while the log keeps its entries. The
// truncation must clamp rather than slice past zero.
func TestInputHistorySurvivesClearedTranscript(t *testing.T) {
	i := &Interactive{}
	i.recordInput("a prompt", true)
	i.recordInput("!ls", false)
	i.carrierMessages = nil

	want := []string{"a prompt", "!ls"}
	if got := i.inputHistory(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("inputHistory() = %q, want %q", got, want)
	}
}

func TestRecordInputCollapsesRepeatsAndSkipsBlanks(t *testing.T) {
	i := &Interactive{}
	i.recordInput("/help", false)
	i.recordInput("/help", false)
	i.recordInput("   ", false)
	i.recordInput("", false)
	i.recordInput("/model", false)
	if got := i.inputHistory(); strings.Join(got, "|") != "/help|/model" {
		t.Fatalf("inputHistory() = %q", got)
	}
}

// The log's join is to one thread's tail: inputHistory counts its
// in-thread entries off the transcript. Carried into another session
// that count is meaningless, and would hide the tail of the new
// session's own prompts behind the old session's typing.
func TestSwitchCarrierSessionClearsInputLog(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.infos = map[string]ctrlproto.SessionInfo{"s2": {ID: "s2"}}
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"

	i.recordInput("s1 prompt", true)
	i.recordInput("!s1 command", false)
	i.carrierMessages = []provider.Message{userMsg("s1 prompt")}
	if got := i.inputHistory(); len(got) != 2 {
		t.Fatalf("precondition: history = %q, want 2 entries", got)
	}

	if err := i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("SwitchCarrierSession: %v", err)
	}
	// s2's transcript arrives from the new binding's snapshot; the old
	// session's typing must not be spliced over it.
	i.carrierMessages = []provider.Message{userMsg("s2 prompt a"), userMsg("s2 prompt b")}
	want := []string{"s2 prompt a", "s2 prompt b"}
	if got := i.inputHistory(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("post-switch history = %q, want %q", got, want)
	}
}

func TestRecordInputBoundsTheLog(t *testing.T) {
	i := &Interactive{}
	for n := range maxInputLog + 50 {
		i.recordInput(string(rune('a'+n%26))+"-"+strings.Repeat("x", n%3), false)
	}
	if len(i.inputLog) > maxInputLog {
		t.Fatalf("inputLog grew to %d, want <= %d", len(i.inputLog), maxInputLog)
	}
}

// End-to-end through the real key loop: a shell escape and a slash
// command typed at the keyboard come back on Left.
//
// The slash half matters twice over. A slash command typed with the
// suggest popup open — the normal way — never reaches handleKey's submit
// branch at all: the popup claims the Enter and runs the highlighted
// command itself. Recording only at the submit branch left every
// popup-run command missing from recall, which is most of them.
func TestInteractiveRecallsShellEscapeAndSlash(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("!true\r")
	h.waitText("[exit 0]") // the escape ran
	h.term.Type("/help\r")
	h.waitText("redraw the screen") // the help block rendered

	// Left from an empty editor walks back, newest first. Assert on the
	// editor row (the last "▌ " line) so the /help block's own text
	// can't satisfy the match.
	h.term.Type("\x1b[D")
	h.waitScreen("/help back in the editor", func(s *tuitest.Screen) bool {
		return strings.Contains(editorRow(s), "/help")
	})
	h.term.Type("\x1b[D")
	h.waitScreen("!true back in the editor", func(s *tuitest.Screen) bool {
		return strings.Contains(editorRow(s), "!true")
	})
}

// Enter on a partial prefix runs the highlighted completion, so that is
// what recall must offer — re-running "/hel" would just be an error.
func TestInteractiveRecallsCompletedSlashName(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("/hel")
	h.waitText("/help")
	h.term.Type("\r")
	h.waitText("redraw the screen")

	h.term.Type("\x1b[D")
	h.waitScreen("the completed name in the editor", func(s *tuitest.Screen) bool {
		row := editorRow(s)
		return strings.Contains(row, "/help") && !strings.Contains(row, "/hel ")
	})
}
