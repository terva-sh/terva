package worker

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// flagArg returns the value after flag in args, and whether flag appears.
func flagArg(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", i >= 0
	}
	return args[i+1], true
}

// 🚨 A member's tools list reaches Claude Code as --tools, which restricts
// the built-in set, never as --allowedTools, which would pre-approve. With no
// bridge, --strict-mcp-config keeps the person's own MCP servers out, so no
// tool arrives past the list.
func TestClaudeCommandNarrowsToTheToolsList(t *testing.T) {
	b := Compose(loadedRepo(t), demoTask(), Workspace{Path: "/w"})
	b.Policy = Policy{Posture: "plan"}
	cmd, err := claudeCommand(Dispatch{Briefing: b, Dir: "/w", Tools: []string{"read", "bash", "talkoot_send", "read"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := flagArg(cmd.Args, "--tools"); got != "Read,Bash" {
		t.Errorf("--tools %q, want Read,Bash", got)
	}
	if !slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("no --strict-mcp-config without a bridge: %v", cmd.Args)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "allowedTools") {
		t.Errorf("an allowlist must not pre-approve: %v", cmd.Args)
	}

	// Seat tools alone disable every built-in tool.
	cmd, err = claudeCommand(Dispatch{Briefing: b, Dir: "/w", Tools: []string{"talkoot_send"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := flagArg(cmd.Args, "--tools"); !ok || got != "" {
		t.Errorf("seat tools alone gave --tools %q (present %v), want an empty value", got, ok)
	}

	// No list, no flag: the posture's full set.
	cmd, err = claudeCommand(Dispatch{Briefing: b, Dir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := flagArg(cmd.Args, "--tools"); ok || slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("no tools list must add neither flag: %v", cmd.Args)
	}

	// With the approval bridge, --strict-mcp-config still holds, so the
	// bridge is the only MCP server.
	cmd, err = claudeCommand(Dispatch{Briefing: b, Dir: "/w", ApprovalSocket: "/w/approval.sock", Tools: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := flagArg(cmd.Args, "--tools"); got != "Read" || !slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("with the bridge, --tools %q and args %v", got, cmd.Args)
	}

	// A list that arrives empty narrows to nothing, rather than to the full
	// set, so a list lost on the way fails closed.
	cmd, err = claudeCommand(Dispatch{Briefing: b, Dir: "/w", Tools: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := flagArg(cmd.Args, "--tools"); !ok || got != "" {
		t.Errorf("an empty list gave --tools %q (present %v), want an empty value", got, ok)
	}

	// A name Claude has no tool for is refused, not dropped.
	if _, err := claudeCommand(Dispatch{Briefing: b, Dir: "/w", Tools: []string{"read", "mcp_github_*"}}); err == nil {
		t.Error("a pattern reached Claude")
	}
}

// A roster reads each backend's Tools to decide whether a list can reach it.
// The terva backends have none yet, because terva's --tools narrows the
// built-in tools only.
func TestEachShippedBackendDeclaresItsAllowlist(t *testing.T) {
	for name, want := range map[string]bool{BackendClaude: true, "terva": false, "terva:portable": false} {
		b, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := b.Tools != nil; got != want {
			t.Errorf("%s has an allowlist: %v, want %v", name, got, want)
		}
	}
}

// 🚨 A tools list on a backend that cannot narrow fails the dispatch. A
// backend that dropped it would run the worker with the full set.
func TestADispatchListNeedsABackendThatNarrows(t *testing.T) {
	b, err := Lookup("terva")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.command(Dispatch{Dir: "/w", Tools: []string{"read"}}); err == nil || !strings.Contains(err.Error(), "cannot narrow") {
		t.Fatalf("want the dispatch refused, got %v", err)
	}
	if _, err := b.command(Dispatch{Dir: "/w", Tools: []string{}}); err == nil {
		t.Error("an empty list reached a backend that cannot narrow")
	}
}

// Installed follows PATH for claude, and the terva backends, which run this
// binary, are always installed.
func TestInstalledFollowsPath(t *testing.T) {
	claude, err := Lookup(BackendClaude)
	if err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)
	t.Setenv("PATH", dir)
	if claude.Installed() {
		t.Error("claude reports installed with no claude on PATH")
	}
	bin := filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !claude.Installed() {
		t.Error("claude reports not installed with claude on PATH")
	}
	for _, name := range Names() {
		b, _ := Lookup(name)
		if name != BackendClaude && b.Installed != nil {
			t.Errorf("%s: a backend that runs this binary needs no Installed check", name)
		}
	}
}
