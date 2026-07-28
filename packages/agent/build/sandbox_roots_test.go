package build

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

// grantedSandbox builds a locked sandbox over a throwaway home whose every
// candidate directory exists, so a denial is a real policy decision rather than
// a path that simply wasn't there.
func grantedSandbox(t *testing.T) (sb *tools.Sandbox, home, cwd string) {
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
	grantReadOnlyRoots(sb, home, filepath.Join(home, "docs"), filepath.Join(home, "examples"))
	sb.Lock()
	return sb, home, cwd
}

// The shared, non-sensitive state a jailed agent is meant to reach — including
// the attachments staging area, which exists precisely so the agent can read
// what the user handed it.
func TestGrantReadOnlyRootsAllowsSharedState(t *testing.T) {
	sb, home, _ := grantedSandbox(t)

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
			t.Errorf("CheckPathRead(%s) = %v, want it granted", rel, err)
		}
	}
}

// The exclusions were a comment with nothing enforcing them. Credentials,
// config, and transcripts stay out of reach, and logs/ is readable only through
// the ext-*.log glob — never as a directory and never for its other files,
// which aggregate MCP/bot/connector stderr.
//
// shared/ is here rather than in the grants above, one line away from
// attachments/, because the two areas run the same machinery in opposite
// directions: the agent reads what it was handed and only writes what it hands
// back — and it writes that through share_file, which talks to the store, not
// through the sandbox. A grant would add nothing but the ability to read every
// other session's deliverables.
func TestGrantReadOnlyRootsRefusesSensitiveState(t *testing.T) {
	sb, home, _ := grantedSandbox(t)

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
		"",                  // $TERVA_HOME itself is never a root
	} {
		if err := sb.CheckPathRead(filepath.Join(home, rel)); err == nil {
			t.Errorf("CheckPathRead(%s) was granted, want it refused", rel)
		}
	}
}

// Every grant is read-only. The agent copies a staged attachment into its
// workspace; it never writes back into $TERVA_HOME, and in particular the
// staging area is the sweeper's to reap, not the model's.
func TestGrantReadOnlyRootsGrantsNoWrites(t *testing.T) {
	sb, home, _ := grantedSandbox(t)

	for _, rel := range []string{
		"docs/web.md",
		"skills/release/SKILL.md",
		attach.DirName + "/ses_1/att_abc-filters.xml",
		"logs/ext-memory.log",
	} {
		if err := sb.CheckPath(filepath.Join(home, rel)); err == nil {
			t.Errorf("CheckPath(%s) was granted, want every read-only root to refuse writes", rel)
		}
	}
}

// The jail root itself is unaffected by the grants: still readable, still
// writable. A regression here would be far worse than a missing grant.
func TestGrantReadOnlyRootsLeavesJailRootWritable(t *testing.T) {
	sb, _, cwd := grantedSandbox(t)
	target := filepath.Join(cwd, "src", "main.go")

	if err := sb.CheckPathRead(target); err != nil {
		t.Errorf("CheckPathRead(jail root) = %v, want nil", err)
	}
	if err := sb.CheckPath(target); err != nil {
		t.Errorf("CheckPath(jail root) = %v, want nil", err)
	}
}
