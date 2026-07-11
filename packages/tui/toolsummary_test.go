package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSummarizeToolNamesGolden pins the Go summarizer to the shared golden
// fixtures. The web UI's summarizeToolNames test (toolsummary.test.ts) loads
// the SAME file, so a change to the format on either side breaks the other's
// test — the two implementations cannot silently drift.
func TestSummarizeToolNamesGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "tool_summary_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Names []string `json:"names"`
		Want  string   `json:"want"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no golden fixtures loaded")
	}
	for _, c := range cases {
		if got := summarizeToolNames(c.Names); got != c.Want {
			t.Errorf("summarizeToolNames(%v) = %q, want %q", c.Names, got, c.Want)
		}
	}
}
