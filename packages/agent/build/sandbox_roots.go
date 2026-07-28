package build

import (
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/tools"
)

// grantReadOnlyRoots widens a jailed sandbox to the parts of $TERVA_HOME the
// agent legitimately needs, and to nothing else.
//
// terva's own state lives under $TERVA_HOME, outside the cwd jail. A jailed
// agent still needs to read the non-sensitive, shared dirs — its docs
// (referenced in the system prompt), installed skills/themes, and installed
// extensions plus their data — so they are registered as read-only roots:
// readable by read/grep/glob, never writable.
//
// Deliberately EXCLUDED as sensitive: auth.json (credentials), config.json,
// sessions/ and swarm/ (transcripts), and logs/ — which aggregates stderr from
// MCP servers, the bot, connectors, and hooks and is a secret-leak sink. Also
// excluded, and less obviously: shared/, the OUTBOUND half of the same file
// machinery attachments/ is the inbound half of. See the note at the grant.
// Only specific subdirs are ever added, never $TERVA_HOME itself.
//
// This is a named function rather than a run of calls inside Resolve so the
// grant list — including everything it must NOT grant — can be asserted
// directly; see sandbox_roots_test.go. The exclusions above were previously a
// comment with nothing checking them.
func grantReadOnlyRoots(sandbox *tools.Sandbox, home, docsDir, examplesDir string) {
	// attachments/ is the odd one out: it holds files a USER handed this daemon
	// from a client, precisely so the agent would read them, so granting the
	// read is the feature rather than a concession. It stays read-only like the
	// rest — the agent copies what it wants into the workspace, and reaping the
	// staged original is the sweeper's job (packages/agent/attach), not the
	// model's.
	//
	// Note the granularity: this is the whole attachments root, not one
	// session's directory, so a jailed agent can read any session's staged files
	// rather than only its own. That is deliberate — read-only roots are set
	// once at setup and read concurrently without a lock (see tools.Sandbox),
	// and the workspace re-points its sandbox on rebuild (UseSandbox), so a
	// per-session grant added later would be both racy and liable to be swapped
	// away. Acceptable under terva's single-user identity model; tightening it
	// means putting a mutex on Sandbox first.
	sandbox.AddReadOnlyRoot(
		docsDir,
		examplesDir,
		filepath.Join(home, "extensions"),
		filepath.Join(home, "ext-data"),
		filepath.Join(home, "skills"),
		filepath.Join(home, "themes"),
		filepath.Join(home, attach.DirName),
	)
	// attach.ShareDirName is NOT here, and the asymmetry with the line above is
	// the point. Both areas are the same machinery, but the agent's relationship
	// to them is opposite: it READS the inbound one, which is why the grant
	// exists, and it only ever WRITES the outbound one — through the share_file
	// tool, which calls the store directly and needs no sandbox permission at
	// all. So a grant here would buy nothing the agent needs and hand a jailed
	// session the ability to enumerate every other session's deliverables.
	// Asserted as a refusal in sandbox_roots_test.go, in the company of
	// auth.json and sessions/.
	// logs/ as a whole is a secret-leak sink (MCP/bot/connector/hooks stderr can
	// carry tokens and chat content), so expose ONLY the extension logs the
	// agent needs for debugging, by name — not the dir.
	sandbox.AddReadOnlyGlob(filepath.Join(home, "logs"), "ext-*.log")
	// The bash tool spills over-long output to $TMPDIR/terva-bash-*.log and
	// points the model at it; allow reading those (the agent's own output) so a
	// jailed agent can page the spill via `read` without /unjail.
	sandbox.AddReadOnlyGlob(os.TempDir(), "terva-bash-*.log")
}
