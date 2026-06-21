package tui

import (
	"os"
	"strings"
	"testing"
)

func TestReportCWDEmptyIsBlank(t *testing.T) {
	if got := ReportCWD(""); got != "" {
		t.Fatalf("ReportCWD(\"\") = %q; want empty", got)
	}
}

func TestReportCWDFormat(t *testing.T) {
	host, _ := os.Hostname()
	got := ReportCWD("/tmp/some dir/work")
	if !strings.HasPrefix(got, "\x1b]7;file://"+host+"/") {
		t.Fatalf("missing OSC 7 prefix: %q", got)
	}
	if !strings.HasSuffix(got, "\x07") {
		t.Fatalf("missing BEL terminator: %q", got)
	}
	// Spaces must be percent-encoded, slashes preserved.
	if !strings.Contains(got, "/tmp/some%20dir/work") {
		t.Fatalf("path not encoded as expected: %q", got)
	}
}

func TestOSC7EscapePath(t *testing.T) {
	cases := map[string]string{
		"/a/b":            "/a/b",
		"/a b/c":          "/a%20b/c",
		"/weird#?&":       "/weird%23%3F%26",
		"/unreserved-_.~": "/unreserved-_.~",
	}
	for in, want := range cases {
		if got := osc7EscapePath(in); got != want {
			t.Errorf("osc7EscapePath(%q) = %q; want %q", in, got, want)
		}
	}
}
