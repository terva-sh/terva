package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TW-011: `write` can set an explicit file mode in one reviewable operation.
// POSIX permission-bit assertions live in write_mode_unix_test.go; these are
// the cross-platform ones (parsing + rejection, no mode-bit observation).

// An invalid or unsafe mode is rejected before the file is created.
func TestWriteModeRejectsInvalid(t *testing.T) {
	dir := testsupport.TempDir(t)
	tool := &WriteTool{CWD: dir}
	for _, bad := range []string{"abc", "999", "4755", "1000"} { // non-octal, bad digit, setuid, sticky
		name := "f-" + bad
		if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
			"path": name, "content": "x", "mode": bad,
		}), nil); err == nil {
			t.Errorf("mode %q was accepted; want a hard error", bad)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("mode %q: file was created despite the bad mode (parse must precede write)", bad)
		}
	}
}

// parseFileMode accepts the common octal spellings and rejects the rest.
func TestParseFileMode(t *testing.T) {
	for in, want := range map[string]os.FileMode{
		"0755": 0o755, "755": 0o755, "0o644": 0o644, "600": 0o600, "0": 0,
	} {
		got, err := parseFileMode(in)
		if err != nil || got != want {
			t.Errorf("parseFileMode(%q) = (%#o, %v), want (%#o, nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "0o", "888", "4000", "10000"} {
		if _, err := parseFileMode(bad); err == nil {
			t.Errorf("parseFileMode(%q) = nil error, want rejection", bad)
		}
	}
}
