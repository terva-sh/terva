package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// Every dialog that reads typed text keeps it in a tui.LineBuf, so a value can
// be edited anywhere in the line and not only at its end. These pin that per
// dialog, and pin the precedence the change turned over: a dialog's own chord
// beats the filter's readline keys.

func sendRunes(handle func(tui.Key), s string) {
	for _, r := range s {
		handle(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
}

// The reported case: a long value in the model editor could only be fixed by
// deleting back to the typo.
func TestModelEditRowTakesAnInsertionMidValue(t *testing.T) {
	d := NewModelEditDialog()
	d.Open(provider.Model{Provider: "acme", ID: "m1"}, provider.UserModel{}, false, "")

	moveTo(d, "baseUrl")
	d.HandleKey(kind(tui.KeyEnter))
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "https://acme/v1")
	for i := 0; i < len("acme/v1"); i++ {
		d.HandleKey(kind(tui.KeyLeft))
	}
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "api.")
	d.HandleKey(kind(tui.KeyEnter)) // commit

	act := d.HandleKey(rn('s'))
	if !act.Save {
		t.Fatalf("expected a save action, got %+v", act)
	}
	if act.Entry.BaseURL != "https://api.acme/v1" {
		t.Errorf("base url = %q, want the host inserted mid-value", act.Entry.BaseURL)
	}
}

// The digits-only rule survived the move into LineBuf.Accept, and a rejected
// rune stays swallowed: 'r' reaching the dialog would arm the reset prompt.
func TestModelEditNumericRowStillRejectsLetters(t *testing.T) {
	d := NewModelEditDialog()
	d.Open(provider.Model{Provider: "acme", ID: "m1", ContextWindow: 1000}, provider.UserModel{}, false, "")

	moveTo(d, "contextWindow")
	d.HandleKey(kind(tui.KeyEnter))
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "12r34")
	if got := d.buf.Value(); got != "1234" {
		t.Errorf("buffer = %q, want the letter dropped", got)
	}
	if d.confirmingReset {
		t.Error("'r' typed into a number row armed the reset prompt")
	}
}

func TestModelEditRowDrawsTheCaretAtTheCursor(t *testing.T) {
	d := NewModelEditDialog()
	d.Open(provider.Model{Provider: "acme", ID: "m1"}, provider.UserModel{}, false, "")

	moveTo(d, "baseUrl")
	d.HandleKey(kind(tui.KeyEnter))
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "abc")
	d.HandleKey(kind(tui.KeyLeft))

	body := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(body, "ab"+tui.LineCaret+"c") {
		t.Errorf("caret is not drawn inside the value:\n%s", body)
	}
}

func TestExtConfigRowTakesAnInsertionMidValue(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("weather", []ConfigField{{Key: "host", Type: "string"}})

	d.HandleKey(kind(tui.KeyEnter))
	typeChars(d, "example.com")
	d.HandleKey(kind(tui.KeyHome))
	typeChars(d, "api.")
	d.HandleKey(kind(tui.KeyEnter)) // commit

	act := d.HandleKey(rn('s'))
	if !act.Save || act.Values["host"] != "api.example.com" {
		t.Fatalf("save = %+v, want host=api.example.com", act)
	}
}

// A secret row masks what it holds. The caret still has to say where the
// cursor is, or the row cannot be edited at all.
func TestExtConfigSecretRowMasksButKeepsTheCaret(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("weather", []ConfigField{{Key: "api_key", Type: "secret"}})

	d.HandleKey(kind(tui.KeyEnter))
	typeChars(d, "sk-1")
	d.HandleKey(kind(tui.KeyLeft))

	body := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(body, "•••"+tui.LineCaret+"•") {
		t.Errorf("masked row lost its caret:\n%s", body)
	}
	if strings.Contains(body, "sk-1") {
		t.Error("the secret is on screen in the clear")
	}
}

func TestSessionRenameTakesAnInsertionMidTitle(t *testing.T) {
	saved := ""
	d := NewSessionDialog()
	d.List = func() []core.SessionSummary {
		return []core.SessionSummary{{Path: "/s/a.jsonl", Title: "the fix", MessageCount: 2}}
	}
	d.Rename = func(path, title string) error {
		saved = title
		return nil
	}
	d.Open("/root", "/cwd")

	d.HandleKey(rn('r'))
	d.HandleKey(kind(tui.KeyHome))
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "about ")
	// The host paints the real terminal cursor from CursorPos, so it has to
	// follow the buffer rather than sit at the end of the text.
	if _, col := d.CursorPos(); col != 2+len("about ") {
		t.Errorf("cursor column = %d, want %d", col, 2+len("about "))
	}
	d.HandleKey(kind(tui.KeyEnter))

	if saved != "about the fix" {
		t.Errorf("saved title = %q, want the words inserted at the front", saved)
	}
}

// The model list binds ctrl+e to edit and ctrl+k to hide. The filter now owns
// the readline chords, so the dialog has to claim its own first.
func TestModelListChordsBeatTheFilterKeys(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, nil)
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "llama")

	act := d.HandleKey(kind(tui.KeyCtrlE))
	if !act.Edit {
		t.Fatalf("ctrl+e with a filter typed = %+v, want the model editor", act)
	}

	d = NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, nil)
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "llama")
	if act := d.HandleKey(kind(tui.KeyCtrlK)); !act.Hide {
		t.Fatalf("ctrl+k with a filter typed = %+v, want the hide toggle", act)
	}
}

// Moving the cursor inside a filter must not re-run the search: re-filtering
// snaps the highlighted row back to the top, under the user's fingers.
func TestModelFilterCursorMoveHoldsTheSelectedRow(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, nil)
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "e")
	if len(d.p.view) < 2 {
		t.Fatalf("need at least two matches to move within, got %d", len(d.p.view))
	}
	d.HandleKey(kind(tui.KeyDown))
	row := d.p.cursor

	d.HandleKey(kind(tui.KeyLeft))
	if d.p.cursor != row {
		t.Errorf("cursor row moved from %d to %d on a left arrow", row, d.p.cursor)
	}
	if d.p.query.Cursor() != 0 {
		t.Errorf("filter cursor = %d, want it moved to the start", d.p.query.Cursor())
	}
}

func TestJumpFilterTakesAnInsertionMidString(t *testing.T) {
	d := NewJumpDialog()
	d.Open([]provider.Message{jdUser("alpha"), jdAsst("a"), jdUser("beta"), jdAsst("b")}, "", JumpScroll)

	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "ea")
	d.HandleKey(kind(tui.KeyLeft))
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "t")

	if got := d.filter.Value(); got != "eta" {
		t.Fatalf("filter = %q, want %q", got, "eta")
	}
	if len(d.visible) != 1 || d.visible[0].Preview != "beta" {
		t.Errorf("visible = %+v, want the beta turn alone", d.visible)
	}
}

func TestCopyFilterTakesAnInsertionAndCtrlYStillCopies(t *testing.T) {
	d := openCopy(t)
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "wrp")
	d.HandleKey(kind(tui.KeyLeft))
	sendRunes(func(k tui.Key) { d.HandleKey(k) }, "a")
	if got := d.turnFilter.Value(); got != "wrap" {
		t.Fatalf("filter = %q, want %q", got, "wrap")
	}
	if len(d.visibleTurn) != 1 {
		t.Fatalf("got %d turns for \"wrap\", want 1", len(d.visibleTurn))
	}
	// ctrl+y is claimed by the dialog, so the filter never sees it.
	act := d.HandleKey(kind(tui.KeyCtrlY))
	if !act.Copy || !act.Whole {
		t.Errorf("ctrl+y with a filter typed = %+v, want the whole reply", act)
	}
}
