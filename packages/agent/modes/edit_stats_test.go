package modes

import (
	"testing"

	"terva.sh/terva/packages/core"
)

func TestEditStats(t *testing.T) {
	editRes := core.ToolResult{Details: map[string]any{
		"path": "/x/a.go",
		"diff": "--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,4 @@\n context\n-old line\n+new line\n+added line\n context\n",
	}}
	if a, r := editStats("edit", editRes); a != 2 || r != 1 {
		t.Errorf("edit diff: +%d -%d, want +2 -1", a, r)
	}

	writeRes := core.ToolResult{Details: map[string]any{"path": "/x/b.go", "total_lines": 42}}
	if a, r := editStats("write", writeRes); a != 42 || r != 0 {
		t.Errorf("write: +%d -%d, want +42 -0", a, r)
	}

	// Errors, unknown tools, and detail-less results count nothing.
	if a, r := editStats("edit", core.ToolResult{IsError: true, Details: editRes.Details}); a != 0 || r != 0 {
		t.Errorf("error result counted: +%d -%d", a, r)
	}
	if a, r := editStats("bash", core.ToolResult{Details: map[string]any{"diff": "+x"}}); a != 0 || r != 0 {
		t.Errorf("non-edit tool counted: +%d -%d", a, r)
	}
	if a, r := editStats("edit", core.ToolResult{}); a != 0 || r != 0 {
		t.Errorf("nil details counted: +%d -%d", a, r)
	}
}
