package widgets

import (
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/fswalk"
)

// waitUpdate drains one onUpdate signal, the remote fill's "data landed"
// notification, with a deadline so a broken fill fails fast.
func waitUpdate(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("remote fill never signalled onUpdate")
	}
}

// A remote-backed picker never blocks the caller: the first scan returns
// nothing and kicks one background fill; once the fill lands (onUpdate),
// the same input matches — the popup pops in when the data does.
func TestFileSuggesterRemoteFillsAsync(t *testing.T) {
	updated := make(chan struct{}, 8)
	calls := make(chan string, 8)
	s := NewFileSuggester()
	s.SetCWD("/daemon/ws")
	s.SetRecursive(true)
	s.SetRemoteLister(func(dir string, recursive, respectGitignore bool) ([]fswalk.Entry, bool, error) {
		calls <- dir
		if !recursive || !respectGitignore {
			t.Errorf("lister called with recursive=%v respectGitignore=%v, want true/true", recursive, respectGitignore)
		}
		return []fswalk.Entry{{Rel: "src", IsDir: true}, {Rel: "src/main.go"}}, false, nil
	}, func() { updated <- struct{}{} })

	if got := s.matches("@"); len(got) != 0 {
		t.Fatalf("first scan should be empty while the fill is in flight, got %#v", got)
	}
	waitUpdate(t, updated)
	got := s.matches("@main")
	if !containsEntry(got, "src/main.go", false) {
		t.Fatalf("post-fill @main did not match src/main.go: %#v", got)
	}
	// The cache serves subsequent reads: no second lister call piled up.
	<-calls
	select {
	case d := <-calls:
		t.Fatalf("cache miss refired the lister (dir %q)", d)
	default:
	}
}

// A failed fetch settles (empty view, no per-frame refire); an explicit
// Invalidate retries.
func TestFileSuggesterRemoteFailureSettles(t *testing.T) {
	updated := make(chan struct{}, 8)
	calls := make(chan struct{}, 8)
	fail := true
	s := NewFileSuggester()
	s.SetCWD("/daemon/ws")
	s.SetRemoteLister(func(dir string, recursive, respectGitignore bool) ([]fswalk.Entry, bool, error) {
		calls <- struct{}{}
		if fail {
			return nil, false, errors.New("daemon went away")
		}
		return []fswalk.Entry{{Rel: "ok.txt"}}, false, nil
	}, func() { updated <- struct{}{} })

	_ = s.matches("@")
	waitUpdate(t, updated)
	<-calls
	// Settled: repeated reads serve the empty slot without refiring.
	if got := s.matches("@"); len(got) != 0 {
		t.Fatalf("failed fill should serve an empty view, got %#v", got)
	}
	select {
	case <-calls:
		t.Fatal("settled failure refired the lister on the next read")
	default:
	}

	fail = false
	s.Invalidate()
	_ = s.matches("@")
	waitUpdate(t, updated)
	if got := s.matches("@ok"); !containsEntry(got, "ok.txt", false) {
		t.Fatalf("post-Invalidate retry did not surface ok.txt: %#v", got)
	}
}

// Browsing into a directory (flat mode) must not show the parent's cached
// entries while the child listing is still in flight — the slot answers
// one (dir|mode) key at a time.
func TestFileSuggesterRemoteKeyedByBrowseDir(t *testing.T) {
	updated := make(chan struct{}, 8)
	s := NewFileSuggester()
	s.SetCWD("/daemon/ws")
	s.SetRemoteLister(func(dir string, recursive, respectGitignore bool) ([]fswalk.Entry, bool, error) {
		if dir == "" {
			return []fswalk.Entry{{Rel: "sub", IsDir: true}}, false, nil
		}
		return []fswalk.Entry{{Rel: "sub/inner.go"}}, false, nil
	}, func() { updated <- struct{}{} })

	_ = s.matches("@")
	waitUpdate(t, updated)
	s.lastMatches = s.matches("@")
	if !containsEntry(s.lastMatches, "sub", true) {
		t.Fatalf("root listing missing sub/: %#v", s.lastMatches)
	}
	s.cursor = 0
	if !s.Right() {
		t.Fatal("Right() did not open sub/")
	}
	// The child fetch is in flight: the parent's rows must not leak in.
	if got := s.matches("@"); len(got) != 0 {
		t.Fatalf("stale parent rows served for the child dir: %#v", got)
	}
	waitUpdate(t, updated)
	if got := s.matches("@"); !containsEntry(got, "inner.go", false) {
		t.Fatalf("child listing missing inner.go: %#v", got)
	}
}
