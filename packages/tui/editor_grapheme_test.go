package tui

// Grapheme-cluster and paste-hardening coverage (TUI plan Phase 3).

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Backspace, Delete, and ←/→ operate on grapheme clusters, not
// runes: combining accents, ZWJ emoji families, and flag pairs are
// single units.
func TestEditorGraphemeClusters(t *testing.T) {
	const (
		accented = "é"                               // é as e + combining acute (2 runes)
		family   = "\U0001F468‍\U0001F469‍\U0001F467" // 👨‍👩‍👧 (5 runes)
		flag     = "\U0001F1E9\U0001F1EA"             // 🇩🇪 (2 runes)
	)

	e := NewEditor("> ")
	typeRunes(e, "x"+accented+"y")
	// Cursor at end (4 runes). Left over 'y', then over the cluster.
	e.HandleKey(Key{Kind: KeyLeft})
	if e.CursorC != 3 {
		t.Fatalf("after Left over y: CursorC = %d, want 3", e.CursorC)
	}
	e.HandleKey(Key{Kind: KeyLeft})
	if e.CursorC != 1 {
		t.Fatalf("after Left over é: CursorC = %d, want 1 (cluster start)", e.CursorC)
	}
	e.HandleKey(Key{Kind: KeyRight})
	if e.CursorC != 3 {
		t.Fatalf("after Right over é: CursorC = %d, want 3", e.CursorC)
	}
	// Backspace from the end removes y, then the whole accent
	// cluster — never leaving a bare combining mark behind.
	e.HandleKey(Key{Kind: KeyEnd})
	e.HandleKey(Key{Kind: KeyBackspace}) // remove y
	e.HandleKey(Key{Kind: KeyBackspace}) // remove é entirely
	if e.Value() != "x" {
		t.Fatalf("after backspaces: Value = %q, want x", e.Value())
	}

	e.Clear()
	typeRunes(e, family)
	if e.CursorC != 5 {
		t.Fatalf("family CursorC = %d, want 5 runes", e.CursorC)
	}
	e.HandleKey(Key{Kind: KeyBackspace})
	if e.Value() != "" {
		t.Fatalf("one backspace must remove the whole ZWJ family; Value = %q", e.Value())
	}

	e.Clear()
	typeRunes(e, flag+"z")
	e.HandleKey(Key{Kind: KeyHome})
	e.HandleKey(Key{Kind: KeyRight})
	if e.CursorC != 2 {
		t.Fatalf("Right over flag: CursorC = %d, want 2", e.CursorC)
	}
	e.HandleKey(Key{Kind: KeyDelete}) // deletes z
	if e.Value() != flag {
		t.Fatalf("after Delete: Value = %q, want flag only", e.Value())
	}
	e.HandleKey(Key{Kind: KeyHome})
	e.HandleKey(Key{Kind: KeyDelete}) // deletes the whole flag pair
	if e.Value() != "" {
		t.Fatalf("Delete at flag start must remove both runes; Value = %q", e.Value())
	}
}

// Oversize bracketed pastes are kept up to the cap, drained to the
// end marker (so trailing input still parses), and visibly marked as
// truncated.
func TestReaderPasteSizeCap(t *testing.T) {
	old := maxPasteBytes
	maxPasteBytes = 64
	defer func() { maxPasteBytes = old }()

	body := strings.Repeat("a", 200)
	input := []byte("\x1b[200~" + body + "\x1b[201~x")
	pos := 0
	read := func() (byte, error) {
		if pos >= len(input) {
			return 0, io.EOF
		}
		b := input[pos]
		pos++
		return b, nil
	}
	r := NewReader(read)

	k, err := r.Read()
	if err != nil || k.Kind != KeyPaste {
		t.Fatalf("first key = %+v, %v; want KeyPaste", k, err)
	}
	if !strings.HasSuffix(k.Paste, "[paste truncated: exceeded the size limit]") {
		t.Fatalf("truncation marker missing: %q", k.Paste)
	}
	if kept := strings.TrimSuffix(k.Paste, "\n[paste truncated: exceeded the size limit]"); len(kept) != 64 {
		t.Fatalf("kept %d bytes, want 64", len(kept))
	}
	// The stream was drained exactly to the end marker: the following
	// byte arrives as an ordinary key.
	k, err = r.Read()
	if err != nil || k.Kind != KeyRune || k.Rune != 'x' {
		t.Fatalf("key after paste = %+v, %v; want rune x", k, err)
	}
}

// A paste with more path-shaped tokens than a plausible drag-drop is
// inserted verbatim — no os.Stat storm on slow mounts — while a real
// small drop still gets quoted.
func TestPasteStatBound(t *testing.T) {
	dir := testsupport.TempDir(t)
	real := filepath.Join(dir, "with space.txt")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Small drop: the real path (space escaped the way terminals
	// deliver drags) is quoted. POSIX-only: the drag-drop heuristic
	// recognizes "/", "~", and file:// paths and single-quotes them for
	// a POSIX shell. A Windows drive-letter path (C:\…) is deliberately
	// left alone — single quotes aren't Windows-shell syntax — so this
	// assertion only holds where the path is POSIX-shaped.
	if runtime.GOOS != "windows" {
		escaped := strings.ReplaceAll(real, " ", "\\ ")
		quoted := quotePastedFilePaths(escaped)
		if !strings.Contains(quoted, "'") {
			t.Fatalf("small drop not quoted: %q", quoted)
		}
	}

	// 20 path-shaped tokens (including the real one): verbatim.
	tokens := make([]string, 0, 20)
	for i := 0; i < 19; i++ {
		tokens = append(tokens, "/nonexistent/path"+strings.Repeat("x", i+1))
	}
	tokens = append(tokens, real)
	paste := strings.Join(tokens, " ")
	if got := quotePastedFilePaths(paste); got != paste {
		t.Fatalf("oversized candidate set must pass through verbatim;\n got: %q\nwant: %q", got, paste)
	}
}
