package build

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

// restrictedSandbox builds a locked sandbox over a throwaway home whose every
// candidate directory exists, so a denial is a real policy decision rather than
// a path that simply wasn't there.
func restrictedSandbox(t *testing.T) (sb *tools.Sandbox, home, cwd string) {
	t.Helper()
	home = testsupport.TempDir(t)
	cwd = testsupport.TempDir(t)
	for _, d := range []string{
		"extensions", "ext-data", "skills", "themes", attach.DirName,
		attach.ShareDirName, "docs", "examples", "sessions", "swarm", "logs",
	} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"auth.json", "config.json", "trusted.json", "unjailed.json"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sb = tools.NewSandbox(cwd)
	restrictSensitiveReads(sb, home)
	sb.Lock()
	return sb, home, cwd
}

// The shared, non-sensitive state a jailed agent is meant to reach — including
// the attachments staging area, which exists precisely so the agent can read
// what the user handed it. These needed an explicit grant when reads were
// confined to the working directory; now they simply are not denied.
func TestRestrictSensitiveReadsAllowsSharedState(t *testing.T) {
	sb, home, _ := restrictedSandbox(t)

	for _, rel := range []string{
		"docs/web.md",
		"examples/deploy/terva.service",
		"extensions/memory/manifest.json",
		"ext-data/memory/state.json",
		"skills/release/SKILL.md",
		"themes/dark.json",
		attach.DirName + "/ses_1/att_abc-filters.xml",
		"logs/ext-memory.log",
	} {
		if err := sb.CheckPathRead(filepath.Join(home, rel)); err != nil {
			t.Errorf("CheckPathRead(%s) = %v, want it readable", rel, err)
		}
	}
}

// Credentials, trust state, and transcripts stay out of reach. logs/ is
// readable only through the ext-*.log exception — never as a directory and
// never for its other files, which aggregate MCP/bot/connector stderr.
//
// shared/ is denied one line away from attachments/ being allowed, because the
// two areas run the same machinery in opposite directions: the agent reads what
// it was handed and only writes what it hands back — and it writes that through
// share_file, which talks to the store, not through the sandbox. Reading it
// would add nothing but the ability to enumerate every other session's
// deliverables.
func TestRestrictSensitiveReadsRefusesSensitiveState(t *testing.T) {
	sb, home, _ := restrictedSandbox(t)

	for _, rel := range []string{
		"auth.json",
		"config.json",
		"trusted.json",
		"unjailed.json",
		"sessions/abc/session.jsonl",
		"swarm/agent-1/events.jsonl",
		"logs/bot.log",
		"logs/mcp-foo.log",
		"logs", // the dir itself, so grep/glob can't sweep it
		attach.ShareDirName + "/ses_1/shr_abc-report.pdf",
		attach.ShareDirName, // and not as a directory either
	} {
		if err := sb.CheckPathRead(filepath.Join(home, rel)); err == nil {
			t.Errorf("CheckPathRead(%s) was allowed, want it refused", rel)
		}
	}
}

// The deny list binds bash too. Enforcing it on the file tools alone would
// rebuild, for the one case that matters, the split posture this sandbox
// abandoned: refused by `read`, handed over by `cat`.
func TestRestrictSensitiveReadsBindsBash(t *testing.T) {
	sb, home, _ := restrictedSandbox(t)

	for _, cmd := range []string{
		"cat " + filepath.Join(home, "auth.json"),
		"grep -r token " + filepath.Join(home, "logs"),
		"head -5 " + filepath.Join(home, "sessions/abc/session.jsonl"),
		"ls " + filepath.Join(home, attach.ShareDirName),
	} {
		if err := sb.CheckCommand(cmd); err == nil {
			t.Errorf("CheckCommand(%q) was allowed, want it refused", cmd)
		}
	}

	// ...and does not fire on the paths it never denied, nor on bare words
	// that merely look like denied directory names.
	for _, cmd := range []string{
		"cat " + filepath.Join(home, "docs/web.md"),
		"tail " + filepath.Join(home, "logs/ext-memory.log"),
		"go test ./packages/agent/sessions",
		"echo sessions swarm logs",
		"git log --oneline -5",
	} {
		if err := sb.CheckCommand(cmd); err != nil {
			t.Errorf("CheckCommand(%q) = %v, want it allowed", cmd, err)
		}
	}
}

// Reads outside the working directory are no longer refused: the bash tool was
// never path-jailed, so every such refusal cost a turn and confined nothing.
// This is B1 of docs/reviews/2026-07-30-session-harness-friction-review.md — a
// batch of eight parallel reads under /tmp was rejected, and the same bytes
// arrived one turn later through `git show | awk`.
func TestJailedReadsReachOutsideTheRoot(t *testing.T) {
	sb, _, _ := restrictedSandbox(t)
	scratch := testsupport.TempDir(t)
	target := filepath.Join(scratch, "game", "03-JavaScript", "time.js")

	if err := sb.CheckPathRead(target); err != nil {
		t.Errorf("CheckPathRead(%s) = %v, want reads unconfined when jailed", target, err)
	}
	// WRITES are unchanged — that is the half the jail still enforces.
	if err := sb.CheckPath(target); err == nil {
		t.Errorf("CheckPath(%s) was allowed; the write jail must still hold", target)
	}
}

// The jail root itself: still readable, still writable. A regression here
// would be far worse than an over-broad read.
func TestRestrictSensitiveReadsLeavesJailRootWritable(t *testing.T) {
	sb, _, cwd := restrictedSandbox(t)
	target := filepath.Join(cwd, "src", "main.go")

	if err := sb.CheckPathRead(target); err != nil {
		t.Errorf("CheckPathRead(jail root) = %v, want nil", err)
	}
	if err := sb.CheckPath(target); err != nil {
		t.Errorf("CheckPath(jail root) = %v, want nil", err)
	}
}
