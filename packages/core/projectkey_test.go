package core

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectKeyReadableAndUnique: the readable prefix flattens the
// path for humans, and the trailing hash guarantees uniqueness even
// when two distinct paths flatten to the same prefix.
func TestProjectKeyReadableAndUnique(t *testing.T) {
	// Lossy-flatten collision: a dir named "b c" (space -> _) and one
	// named "b_c" (literal _) both flatten to "a-b_c", so only the hash
	// distinguishes them.
	k1 := ProjectKey("/a/b c")
	k2 := ProjectKey("/a/b_c")
	if k1 == k2 {
		t.Fatalf("distinct paths must not collide: %q == %q", k1, k2)
	}
	if !strings.HasPrefix(k1, "a-b_c-") || !strings.HasPrefix(k2, "a-b_c-") {
		t.Fatalf("readable prefix should be a-b_c for both: %q / %q", k1, k2)
	}
}

// TestProjectKeyHashMatchesSessionsBucket: the key's trailing hash is
// exactly the cwd's SessionsDir bucket name, so ext-data and sessions
// correlate by eye.
func TestProjectKeyHashMatchesSessionsBucket(t *testing.T) {
	cwd := "/home/pat/dev/myrepo"
	key := ProjectKey(cwd)
	bucket := filepath.Base(SessionsDir("/root", cwd)) // == CWDHash(cwd)
	if !strings.HasSuffix(key, "-"+bucket) {
		t.Fatalf("ProjectKey %q should end in the session bucket hash %q", key, bucket)
	}
	if bucket != CWDHash(cwd) {
		t.Fatalf("SessionsDir bucket %q != CWDHash %q", bucket, CWDHash(cwd))
	}
}

func TestProjectSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/pat/dev/myrepo", "home-pat-dev-myrepo"},
		{"/a//b", "a-b"},                  // collapse a run of separators
		{"/a/b_c", "a-b_c"},               // special char -> _, kept distinct
		{"/weird name/x", "weird_name-x"}, // space -> _
		{"/", ""},                         // root flattens to nothing
		{"relative/path", "relative-path"},
	}
	for _, c := range cases {
		if got := projectSlug(c.in); got != c.want {
			t.Errorf("projectSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestProjectKeyRootDegradesToHash: a path that flattens to nothing
// ("/") yields just the hash, never a leading-dash dir name.
func TestProjectKeyRootDegradesToHash(t *testing.T) {
	k := ProjectKey("/")
	if strings.HasPrefix(k, "-") {
		t.Fatalf("key must not start with '-' (CLI footgun): %q", k)
	}
	if k != CWDHash("/") {
		t.Fatalf("root key should be the bare hash, got %q", k)
	}
}

// TestProjectSlugCapsLength: a very deep path is truncated tail-biased
// so the most specific components survive, and never starts with a sep.
func TestProjectSlugCapsLength(t *testing.T) {
	deep := "/" + strings.Repeat("segment/", 40) + "finalrepo"
	s := projectSlug(deep)
	if len(s) > 80 {
		t.Fatalf("slug not capped: len=%d", len(s))
	}
	if !strings.HasSuffix(s, "finalrepo") {
		t.Fatalf("tail-biased truncation should keep the end: %q", s)
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "_") {
		t.Fatalf("slug must not start with a separator: %q", s)
	}
}
