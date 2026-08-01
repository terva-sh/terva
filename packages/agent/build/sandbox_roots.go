package build

import (
	"path/filepath"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/tools"
)

// restrictSensitiveReads names the parts of $TERVA_HOME a jailed agent must
// never read, and nothing else.
//
// This used to be the inverse: an allowlist of the $TERVA_HOME dirs a jailed
// agent WAS permitted to read, on top of a read jail confined to the working
// directory. That jail was withdrawn because it never held — the bash tool is
// not path-jailed, so every refused read was one `cat` away, and the refusals
// cost turns without buying confinement (see tools.Sandbox.CheckPathRead, and
// docs/reviews/2026-07-30-session-harness-friction-review.md B1).
//
// Withdrawing it does NOT mean these paths become readable. Credentials and
// transcripts are worth denying on their own merits, not as a side effect of a
// cwd jail, so they move to an explicit deny list that survives it:
//
//   - auth.json, config.json, trusted.json, unjailed.json — credentials and the
//     trust state that governs the agent's own permissions.
//   - sessions/ and swarm/ — every transcript, this session's and every other's.
//   - logs/ — aggregates stderr from MCP servers, the bot, connectors and hooks,
//     and is a secret-leak sink. The extension logs the agent legitimately
//     debugs with are carved back out by name.
//   - shared/ — the OUTBOUND half of the attachment machinery: every other
//     session's deliverables. attachments/, the inbound half, is not denied;
//     it exists precisely so the agent can read what the user handed it.
//
// The deny list is absolute: unlike the old jail, /unjail does not lift it, and
// bash cannot reach these either (the bash tool applies the same denial before
// running a command). A named function rather than a run of calls inside
// Resolve so the list — and what it must NOT deny — can be asserted directly;
// see sandbox_roots_test.go.
func restrictSensitiveReads(sandbox *tools.Sandbox, home string) {
	sandbox.AddSecretRoot(
		filepath.Join(home, "auth.json"),
		filepath.Join(home, "config.json"),
		filepath.Join(home, "trusted.json"),
		filepath.Join(home, "unjailed.json"),
		filepath.Join(home, "sessions"),
		filepath.Join(home, "swarm"),
		filepath.Join(home, "logs"),
		filepath.Join(home, attach.ShareDirName),
	)
	// logs/ as a whole is denied, but the extension logs are the agent's own
	// debugging surface and carry no credentials — expose those by name.
	sandbox.AddSecretException(filepath.Join(home, "logs"), "ext-*.log")
}
