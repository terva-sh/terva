package tui

import (
	"strings"
	"testing"
)

// TestStatusBarHideWorkspace pins the chat/play chrome suppression: the cwd
// path, the sandbox "jailed" badge, and the approval-mode tag are gone, while
// ambient extension status segments still show.
func TestStatusBarHideWorkspace(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:         Dark,
		Provider:      "anthropic",
		Model:         "claude-opus-4-7",
		CWD:           "/tmp/secret-workspace",
		Locked:        true,
		ApprovalMode:  "ask",
		ExtStatus:     []string{"🧭 (3,5) reed marsh"},
		HideWorkspace: true,
		Cols:          500,
	})
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "secret-workspace") {
		t.Errorf("cwd path should be hidden in experience mode: %q", joined)
	}
	if strings.Contains(joined, "jailed") {
		t.Errorf("jailed badge should be hidden in experience mode: %q", joined)
	}
	if strings.Contains(joined, "ask mode") {
		t.Errorf("approval-mode tag should be hidden in experience mode: %q", joined)
	}
	if !strings.Contains(joined, "reed marsh") {
		t.Errorf("extension status should still show: %q", joined)
	}
}

// TestStatusBarShowsWorkspaceByDefault is the converse: without HideWorkspace,
// the cwd + jailed badge appear as normal.
func TestStatusBarShowsWorkspaceByDefault(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme: Dark, Provider: "anthropic", Model: "m",
		CWD: "/tmp/work", Locked: true, Cols: 500,
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "/tmp/work") {
		t.Errorf("cwd should show by default: %q", joined)
	}
	if !strings.Contains(joined, "jailed") {
		t.Errorf("jailed should show by default: %q", joined)
	}
}
