package build

import (
	"path/filepath"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/secrets"
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
//     trust state that governs the agent's own permissions. config.json is the
//     one CONDITIONAL entry: it is denied because it carries secrets inline,
//     so once every secret in it is encrypted at rest there is nothing left to
//     deny, and allowConfigRead lifts it (build.ConfigReadableByAgent decides,
//     fail-closed; see docs/proposals/secrets-at-rest.md). The other three are
//     absolute — auth.json holds credentials whether or not they are sealed,
//     and the trust files govern the agent's own permissions.
//   - sessions/ and swarm/ — every transcript, this session's and every other's.
//   - logs/ — aggregates stderr from MCP servers, the bot, connectors and hooks,
//     and is a secret-leak sink. The extension logs the agent legitimately
//     debugs with are carved back out by name.
//   - shared/ — the OUTBOUND half of the attachment machinery: every other
//     session's deliverables. attachments/, the inbound half, is not denied;
//     it exists precisely so the agent can read what the user handed it.
//   - secrets.key and secrets.keyring — the at-rest encryption key and the
//     retired keys a lazy rotation leaves behind, which still OPEN every file
//     not yet rewritten. Encrypting
//     auth.json buys nothing if the key sits one read away; denied both at the
//     home-relative default and wherever --secrets-key-file/TERVA_SECRETS_KEY_FILE
//     resolved it (config.SecretsKeyPath — under project scoping that is the
//     pinned global home, which is not under this home at all).
//
// The deny list is absolute: unlike the old jail, /unjail does not lift it, and
// bash cannot reach these either (the bash tool applies the same denial before
// running a command). A named function rather than a run of calls inside
// Resolve so the list — and what it must NOT deny — can be asserted directly;
// see sandbox_roots_test.go.
func restrictSensitiveReads(sandbox *tools.Sandbox, home, cwd string, allowConfigRead bool) {
	if !allowConfigRead {
		sandbox.AddSecretRoot(filepath.Join(home, "config.json"))
	}
	sandbox.AddSecretRoot(
		filepath.Join(home, "auth.json"),
		filepath.Join(home, "trusted.json"),
		filepath.Join(home, "unjailed.json"),
		filepath.Join(home, "secrets.key"),
		config.SecretsKeyPath(),
		// The retired-key ring is private keys too. It opens every file a lazy
		// rotation has not yet healed, so leaving it readable would hand back
		// exactly what rotating was meant to take away.
		filepath.Join(home, secrets.RetiredKeyFileName),
		config.RetiredSecretsKeyPath(),
		// The web bearer token is the whole authentication for `terva web`:
		// reading it is reading a live grant to drive this agent. Denied at
		// both the home-relative default and wherever CredentialHome resolved
		// it, the same pair as the secrets key.
		filepath.Join(home, config.WebTokenName),
		config.WebTokenPath(),
		filepath.Join(home, "sessions"),
		filepath.Join(home, "swarm"),
		filepath.Join(home, "logs"),
		filepath.Join(home, attach.ShareDirName),
		// terva-mcp-bridge's OAuth token store: an access token, a refresh
		// token and a client_secret per remote MCP server. Sealed whole when an
		// at-rest key is configured, plaintext when one is not — and this deny
		// list is what makes that difference not matter. Nothing but the bridge
		// reads it, so denying the whole tree costs the agent nothing.
		filepath.Join(home, "mcp-bridge"),
	)
	// Every key a COMPONENT mints for itself, wherever it put it. A standalone
	// connector holds its own age identity beside its config
	// (connectors/<name>/secrets.key) so that terva never has to hand out its
	// key — but that identity opens the connector's own secrets, and the
	// directory it lives in stays readable on purpose. Denying by name covers
	// connectors configured after startup, which an enumerated list cannot:
	// AddSecretRoot skips paths that do not exist yet.
	sandbox.AddSecretNameUnder(home, secrets.KeyFileName)

	// logs/ as a whole is denied, but the extension logs are the agent's own
	// debugging surface and carry no credentials — expose those by name.
	sandbox.AddSecretException(filepath.Join(home, "logs"), "ext-*.log")

	// The component trees stay READABLE — that is the point of §8.1a — but each
	// component has to earn it, per read, by being verifiably free of plaintext
	// secrets. A guard rather than a root because the answer is a property of
	// current CONTENT, not of the path, and a component is a live process that
	// can change it mid-session. See componentReadGuard.
	sandbox.AddGuardedRoot(filepath.Join(home, ConnectorDirName),
		componentReadGuard(home, cwd, connectorVerdict))
	sandbox.AddGuardedRoot(filepath.Join(home, ExtDataDirName),
		componentReadGuard(home, cwd, extensionDataVerdict))
}

// allowHandoffWrites carves out the one $TERVA_HOME surface a jailed agent
// may WRITE: handoffs/, where the built-in handoff skill parks its
// session-to-session documents. The write jail exists to protect the user's
// tree from stray mutation; handoffs/ is terva's own state, created to be
// agent-written, and requiring /unjail to use a built-in skill would turn the
// jail into an obstacle the model learns to route around. The read side needs
// no grant — reads are a deny list (above), and handoffs/ is not on it.
//
// The grant is the directory alone: siblings like config.json and skills/
// stay write-jailed, and a symlink planted inside handoffs/ cannot widen it
// (the sandbox resolves targets before matching).
func allowHandoffWrites(sandbox *tools.Sandbox, home string) {
	sandbox.AddWritableRoot(filepath.Join(home, "handoffs"))
}
