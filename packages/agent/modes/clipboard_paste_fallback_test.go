package modes

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

// newFallbackHarness builds an Interactive wired for key dispatch with a
// fake clipboard, so no test spawns PowerShell or touches a real clipboard.
// calls counts how often the clipboard was read, which is the assertion for
// the paths that must not read it at all.
func newFallbackHarness(t *testing.T, png []byte, ok bool) (*Interactive, *int) {
	t.Helper()
	calls := 0
	i := &Interactive{ed: tui.NewEditor("> ")}
	i.keymap = i.buildGlobalKeymap()
	i.readClipboardImage = func() ([]byte, bool, error) {
		calls++
		return png, ok, nil
	}
	return i, &calls
}

// The bug this fixes: Windows Terminal binds ctrl+v to its own paste action
// and eats the chord, so keyPasteClipboard never runs. With an image-only
// clipboard the terminal has no text to send and forwards an empty bracketed
// paste (measured: ESC[200~ ESC[201~ with nothing between). Treat that as the
// image paste the user meant.
func TestEmptyPasteAttachesClipboardImage(t *testing.T) {
	i, calls := newFallbackHarness(t, []byte("png-bytes"), true)

	handled, _ := i.dispatchGlobalKey(t.Context(), tui.Key{Kind: tui.KeyPaste, Paste: ""})
	if !handled {
		t.Fatal("an empty bracketed paste was not claimed; ctrl+v stays dead on terminals that eat the chord")
	}
	if *calls != 1 {
		t.Fatalf("clipboard read %d times, want exactly 1", *calls)
	}
	if len(i.clipboardImages) != 1 {
		t.Fatalf("attached %d images, want 1 — the empty paste did not attach the clipboard image", len(i.clipboardImages))
	}
	if got := string(i.clipboardImages[0].image.Data); got != "png-bytes" {
		t.Fatalf("attached data = %q, want %q", got, "png-bytes")
	}
	if got := i.ed.Value(); got != "[clipboard image #1] " {
		t.Fatalf("editor = %q, want the marker inserted", got)
	}
}

// A paste carrying text is somebody's actual text. It must reach the editor
// untouched, and must never cost a clipboard read — under WSL that read is a
// PowerShell spawn of about a second, on every ordinary text paste.
func TestPasteWithTextIsDeclinedAndNeverReadsClipboard(t *testing.T) {
	i, calls := newFallbackHarness(t, []byte("png-bytes"), true)

	handled, _ := i.dispatchGlobalKey(t.Context(), tui.Key{Kind: tui.KeyPaste, Paste: "hello world"})
	if handled {
		t.Fatal("a text paste was claimed by the image fallback; it must fall through to the editor")
	}
	if *calls != 0 {
		t.Fatalf("clipboard read %d times on a text paste, want 0", *calls)
	}
	if len(i.clipboardImages) != 0 {
		t.Fatalf("attached %d images on a text paste, want 0", len(i.clipboardImages))
	}
}

// An empty paste with no image on the clipboard is an ordinary empty paste.
// Declining keeps it a no-op that reaches the editor, rather than a key the
// fallback swallows on every terminal that has nothing to paste.
func TestEmptyPasteWithNoImageDeclines(t *testing.T) {
	i, calls := newFallbackHarness(t, nil, false)

	handled, _ := i.dispatchGlobalKey(t.Context(), tui.Key{Kind: tui.KeyPaste, Paste: ""})
	if handled {
		t.Fatal("an empty paste with no clipboard image was claimed; it must fall through")
	}
	if *calls != 1 {
		t.Fatalf("clipboard read %d times, want exactly 1", *calls)
	}
	if len(i.clipboardImages) != 0 {
		t.Fatalf("attached %d images, want 0", len(i.clipboardImages))
	}
}

// The ctrl+v row in /help already documents this action, so the fallback is
// an alias and must stay out of the help table. TestEveryBindingIsDocumented
// enforces the invariant; this pins the specific binding, because a desc
// added here would print a second, confusing row for a chord nobody presses.
func TestPasteFallbackIsHiddenFromHelp(t *testing.T) {
	for _, b := range (&Interactive{}).buildGlobalKeymap() {
		if b.name != "paste-clipboard-image-empty-fallback" {
			continue
		}
		if !b.hideFromHelp {
			t.Error("the empty-paste fallback must set hideFromHelp; ctrl+v already documents it")
		}
		if b.kind != tui.KeyPaste {
			t.Errorf("fallback bound to key kind %v, want tui.KeyPaste", b.kind)
		}
		return
	}
	t.Fatal("no paste-clipboard-image-empty-fallback binding in the keymap")
}
