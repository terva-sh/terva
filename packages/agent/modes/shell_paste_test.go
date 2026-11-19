package modes

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestShellPasteRemainsLiteral(t *testing.T) {
	for _, tc := range []struct{ name, draft, paste, want string }{
		{"tilde", "!ls ", "~/", "!ls ~/"},
		{"whitespace prefix", " \t!ls ", "~/", " \t!ls ~/"},
		{"quoted path", "!cat ", `"~/two files"`, `!cat "~/two files"`},
		{"multiline", "!printf ", "one\ntwo\nthree\n", "!printf one\ntwo\nthree\n"},
		{"whole command", "", "!ls ~/", "!ls ~/"},
		{"whole command after spaces", " \t", "!ls ~/", " \t!ls ~/"},
		{"file picker prefix", "!@", "~/", "!@~/"},
		{"ordinary chat", "explain ", "~/", "explain '~/'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newCtrlprotoTestInteractive()
			i.inputHistoryIndex = -1
			i.ed.SetValue(tc.draft)
			if i.handleKey(t.Context(), tui.Key{Kind: tui.KeyPaste, Paste: tc.paste}) {
				t.Fatal("paste ended the interactive session")
			}
			if got := i.ed.SubmitValue(); got != tc.want {
				t.Fatalf("draft = %q, want %q", got, tc.want)
			}
			if i.shellRunning || len(i.clipboardImages) != 0 {
				t.Fatal("text paste started a command or attached an image")
			}
		})
	}
}
