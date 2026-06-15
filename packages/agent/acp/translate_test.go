//go:build terva_acp

package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolTitle(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"bash command", "bash", `{"command":"echo hi","timeout":120}`, "echo hi"},
		{"bash multiline", "bash", `{"command":"set -e\nmake build"}`, "set -e …"},
		{"bash missing command", "bash", `{}`, "bash"},
		{"read path", "read", `{"path":"/a/b.go"}`, "read /a/b.go"},
		{"edit path", "edit", `{"path":"/x.go","content":"..."}`, "edit /x.go"},
		{"write path", "write", `{"path":"/y"}`, "write /y"},
		{"unknown tool keeps name", "mcp_server_tool", `{"q":"x"}`, "mcp_server_tool"},
		{"no args keeps name", "bash", ``, "bash"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toolTitle(tc.tool, json.RawMessage(tc.args))
			if got != tc.want {
				t.Errorf("toolTitle(%q, %q) = %q; want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

func TestTitleLineCaps(t *testing.T) {
	got := titleLine(strings.Repeat("x", 250))
	if r := []rune(got); len(r) != 101 { // 100 chars + the ellipsis
		t.Errorf("titleLine cap = %d runes; want 101", len(r))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("titleLine should end with an ellipsis; got %q", got)
	}
}
