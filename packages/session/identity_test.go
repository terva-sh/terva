package session

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// A session's identity is its file's basename, its path, and its meta UUID as
// the cache key; a nil session is the zero identity, which an agent reads as
// live-only.
func TestIdentityDerivesFromTheFile(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "prov", "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id := s.Identity()
	if id.Path != s.Path {
		t.Errorf("path = %q, want %q", id.Path, s.Path)
	}
	if want := strings.TrimSuffix(filepath.Base(s.Path), ".jsonl"); id.ID != want || id.ID == "" {
		t.Errorf("id = %q, want %q", id.ID, want)
	}
	if id.CacheKey != s.ID || id.CacheKey == "" {
		t.Errorf("cache key = %q, want the meta UUID %q", id.CacheKey, s.ID)
	}
	if (*Session)(nil).Identity() != (core.TranscriptIdentity{}) {
		t.Error("a nil session's identity is not the zero value")
	}
}
