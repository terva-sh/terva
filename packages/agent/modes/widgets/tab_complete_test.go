package widgets

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/fswalk"
	"terva.sh/terva/packages/testsupport"
)

// TabComplete rewrites the live @-token in place over the picker's listing:
// recursive mode completes path-wise (a unique directory gains "/" and the
// next Tab descends into it), flat mode completes basenames and leaves
// descent to the popup's → key.
func TestFileSuggesterTabComplete(t *testing.T) {
	tmp := testsupport.TempDir(t)
	for _, rel := range []string{"src/main.go", "src/menu.go", "README.md"} {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := NewFileSuggester()
	s.SetCWD(tmp)
	s.SetRecursive(true)

	// Unique directory: complete + descend on the next press.
	got, ok := s.TabComplete("look at @s")
	if !ok || got != "look at @src/" {
		t.Fatalf("recursive @s → (%q, %v), want look at @src/", got, ok)
	}
	// Two files share a prefix: extend to the LCP, no commit.
	got, _ = s.TabComplete("look at @src/m")
	if got != "look at @src/m" {
		// LCP of main.go/menu.go is "m" — already there; a longer base extends.
		t.Fatalf("recursive @src/m → %q, want unchanged (LCP boundary)", got)
	}
	got, _ = s.TabComplete("look at @src/ma")
	if got != "look at @src/main.go" {
		t.Fatalf("recursive @src/ma → %q, want look at @src/main.go", got)
	}
	// No live @-token: not consumed, input untouched.
	if _, ok := s.TabComplete("plain text"); ok {
		t.Fatal("TabComplete consumed a Tab with no live @-token")
	}

	// Flat mode: basename completion, no trailing slash on directories.
	flat := NewFileSuggester()
	flat.SetCWD(tmp)
	got, ok = flat.TabComplete("@s")
	if !ok || got != "@src" {
		t.Fatalf("flat @s → (%q, %v), want @src (slash-less; → descends)", got, ok)
	}
}

// A remote-backed picker completes over the wire cache and never blocks: the
// first press (fill in flight) is a consumed no-op; once the fill lands the
// same press completes.
func TestFileSuggesterTabCompleteRemote(t *testing.T) {
	updated := make(chan struct{}, 4)
	s := NewFileSuggester()
	s.SetCWD("/daemon/ws")
	s.SetRecursive(true)
	s.SetRemoteLister(func(dir string, recursive, respectGitignore bool) ([]fswalk.Entry, bool, error) {
		return []fswalk.Entry{{Rel: "src", IsDir: true}, {Rel: "src/main.go"}}, false, nil
	}, func() { updated <- struct{}{} })

	got, ok := s.TabComplete("@sr")
	if !ok || got != "@sr" {
		t.Fatalf("pre-fill Tab → (%q, %v), want consumed no-op", got, ok)
	}
	waitUpdate(t, updated)
	got, _ = s.TabComplete("@sr")
	if got != "@src/" {
		t.Fatalf("post-fill Tab → %q, want @src/", got)
	}
}
