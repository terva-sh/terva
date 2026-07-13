package widgets

import (
	"encoding/json"
	"os"
	"testing"
)

// The golden fixtures are shared with the web composer's TypeScript port
// (client/src/features/conversation/atcomplete.test.ts reads this same
// file), so the two implementations cannot drift — the summarizeToolNames
// pattern.
type atGoldenCase struct {
	Name    string `json:"name"`
	Entries []struct {
		Path string `json:"path"`
		Dir  bool   `json:"dir"`
	} `json:"entries"`
	Query string `json:"query"`
	Want  string `json:"want"`
	N     int    `json:"n"`
}

func TestAtCompleteGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/at_complete_golden.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var cases []atGoldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d fixture cases; the file is not being read fully", len(cases))
	}
	for _, c := range cases {
		entries := make([]AtCandidate, 0, len(c.Entries))
		for _, e := range c.Entries {
			entries = append(entries, AtCandidate{Path: e.Path, Dir: e.Dir})
		}
		got, n := AtComplete(entries, c.Query)
		if got != c.Want || n != c.N {
			t.Errorf("%s: AtComplete(%q) = (%q, %d), want (%q, %d)", c.Name, c.Query, got, n, c.Want, c.N)
		}
	}
}
