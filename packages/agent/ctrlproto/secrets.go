package ctrlproto

import (
	"context"
	"time"
)

// The secrets group: terva's at-rest encryption posture, and the secret store's
// authorization model. Management, never material.
//
// Its own group rather than a corner of auth, for the reason auth is its own
// group rather than a corner of control (see [GroupAuth]) run one step further.
// `auth` manages the credential terva uses to reach a MODEL PROVIDER; this
// manages the key that opens everything — including material `auth` never
// touches: an extension's OAuth token, a connector's bot token, the values
// sealed into config.json. Granting a client the ability to see and reason about
// that posture is not the same act as granting it a login, so they are not the
// same group.
//
// # What is deliberately absent
//
// A verb that returns a secret VALUE. The same rule as auth, and for a sharper
// reason: names, paths, counts, states and reasons cross this wire; material
// never does. A panel is a screen someone else can see, and the CLI shows a
// value once to a TTY because a human is looking at a terminal they control.
//
// ROTATION, in either mode. `terva secret rotate` supersedes the key and
// `--revoke` destroys it, re-encrypting everything now; a bug in a client — or
// a hostile one — bricks the install. Rotation is rare and operator-initiated,
// so it stays on the CLI. Trivial to add later, impossible to un-ship.
//
// `init` and `migrate`, for a weaker form of the same reason: both rewrite every
// secret-bearing file in the home, and a fresh install now seals itself
// (initSecretsForFreshHome), so the on-ramp does not need a wire verb. The
// earlier draft of docs/proposals/secrets-at-rest.md §8.10 listed them; §8.13 Q8
// settled the surface as it stands here.
//
// # Gating
//
// Optional and off the base [ServerHello], like [GroupReplay] and [GroupAuth]: a
// carrier advertises it only when it will actually serve the verbs, so a client
// that negotiates it is guaranteed they work. `terva web` advertises it only
// under --web-allow-secrets, and refuses to on an unauthenticated listener.

// SecretsController is the optional interface behind [GroupSecrets]. A carrier
// that implements it can report the encryption posture and manage the store's
// grants; one that does not never advertises the group.
type SecretsController interface {
	// SecretsStatus reports the whole posture, shape-only.
	SecretsStatus(ctx context.Context) (SecretsStatus, error)
	// SecretsList names the scopes and the KEY NAMES under each. A key name is
	// schema, not material — "bot_token" says what a slot is for and nothing
	// about what is in it.
	SecretsList(ctx context.Context) (SecretsListResult, error)
	// SecretsGrant authorizes a principal against a scope.
	SecretsGrant(ctx context.Context, p SecretsGrantParams) error
	// SecretsRevoke withdraws one grant.
	SecretsRevoke(ctx context.Context, p SecretsRevokeParams) error
	// SecretsForget drops terva's record of a component.
	SecretsForget(ctx context.Context, p SecretsForgetParams) (SecretsForgetResult, error)
}

// Key states. The split between absent and missing is the one distinction in
// this whole struct that a user can act on wrongly: "absent" means encryption
// was never turned on and `terva secret init` is right, while "missing" means
// ciphertext exists that this key was supposed to open — generating a new one
// there would strand it permanently. A single "no key" would have collapsed
// them.
const (
	SecretsKeyPresent  = "present"
	SecretsKeyAbsent   = "absent"
	SecretsKeyMissing  = "missing"
	SecretsKeyUnusable = "unusable"
)

// SecretsKey is where the at-rest key came from and whether it is usable.
type SecretsKey struct {
	State string `json:"state"` // present | absent | missing | unusable
	Path  string `json:"path,omitempty"`
	// Mode is the key file's permission bits ("0600"), empty when the identity
	// did not come from a file.
	Mode string `json:"mode,omitempty"`
	// OwnerOnly is false when the key file is group- or other-accessible, which
	// is a finding rather than a fact — the panel should say so.
	OwnerOnly bool `json:"owner_only,omitempty"`
	// FromEnv reports an identity supplied through the environment rather than
	// read from Path, so a client does not render a mode nobody set.
	FromEnv bool `json:"from_env,omitempty"`
	// Reason explains a missing or unusable key in operator-facing words.
	Reason string `json:"reason,omitempty"`
}

// File states, from a content sniff rather than a configuration check: what
// matters is what is ON DISK, and a home can be configured for encryption while
// holding plaintext written before it was.
const (
	SecretsFileAbsent     = "absent"
	SecretsFileEncrypted  = "encrypted"
	SecretsFilePlaintext  = "plaintext"
	SecretsFileUnreadable = "unreadable"
)

// SecretsFile is one whole-file-sealed store's state.
type SecretsFile struct {
	Name  string `json:"name"`  // "auth.json"
	State string `json:"state"` // absent | encrypted | plaintext | unreadable
	// Note carries the actionable half when there is one ("holds a plaintext bot
	// token — run `terva secret migrate`").
	Note string `json:"note,omitempty"`
}

// SecretsScope is one scope in the store, by name and count. Never key names —
// that is secrets.list, which is a separate call because a status pane wants a
// summary and an inspector wants the detail.
type SecretsScope struct {
	Scope string `json:"scope"`
	Keys  int    `json:"keys"`
}

// SecretsStore summarizes secrets.json.
type SecretsStore struct {
	Present   bool           `json:"present"`
	Encrypted bool           `json:"encrypted"`
	Scopes    []SecretsScope `json:"scopes,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// SecretsConfigScan is config.json's secret-bearing values, by location and
// state. Paths only — this is built to be safe to paste into an issue.
type SecretsConfigScan struct {
	Total int `json:"total"`
	// Plaintext names the dotted locations still unsealed.
	Plaintext []string `json:"plaintext,omitempty"`
	// Unclassified names values no manifest could classify, so nothing can say
	// whether they are secret. Reported because unknown is not clean.
	Unclassified []string `json:"unclassified,omitempty"`
	// AgentCanRead is the payoff verdict: with nothing secret left in the file,
	// the agent's own tools may read it. Reason names what is blocking when not.
	AgentCanRead bool   `json:"agent_can_read"`
	Reason       string `json:"reason,omitempty"`
}

// SecretsComponent is one secret-holding component terva knows about.
type SecretsComponent struct {
	Scope string `json:"scope"` // "conn:discord-ext"
	// Paths is how many values in File are sealed, from the component's own
	// declaration.
	Paths int `json:"paths"`
	// File is the component's state file, relative to the data home.
	File string `json:"file,omitempty"`
	// LastSeen is when the component last handshaked, nil when it never has.
	//
	// A POINTER because `omitempty` does nothing to a time.Time — it is a
	// struct, so a never-seen component would ship "0001-01-01T00:00:00Z" and a
	// client calling new Date() on it renders a real-looking date in year 1.
	// Found by driving the actual wire; the struct tag reads correct.
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// Registered is false for a component whose file holds sealed values while
	// no registry entry claims a recipient for it. That is the operationally
	// important row: a rotation cannot re-seal it, so `--revoke` would leave
	// terva unable to read it. Starting the component once fixes it.
	Registered bool `json:"registered"`
}

// SecretsComponentRead is one component DIRECTORY's readability verdict: may
// the agent's own tools read inside it?
//
// A different axis from [SecretsComponent], which answers "can terva rotate
// this component's secrets?". A component can be perfectly registered for
// rotation and still hold a plaintext value that keeps its tree denied, and an
// extension has no registry entry at all yet still has a data directory the
// agent may or may not read. Merging the two would have made "not registered"
// mean two unrelated things.
type SecretsComponentRead struct {
	Scope string `json:"scope"` // "conn:matrix", "ext:memory"
	// Dir is the tree the verdict governs, absolute.
	Dir      string `json:"dir,omitempty"`
	Readable bool   `json:"readable"`
	// Reason names what is blocking, and what to do about it. Empty when
	// readable.
	Reason string `json:"reason,omitempty"`
	// Enforced is whether Readable is actually applied. False while a
	// declaration requirement is still in its grace period — the verdict is
	// reported so the change is visible before it bites, rather than arriving
	// as a directory that silently went dark.
	Enforced bool `json:"enforced"`
}

// SecretsGrant is one authorization. Expired is computed by the DAEMON: a
// client's clock is not the daemon's, and a grant that reads as live in one
// timezone and dead in another is a bug waiting for a support thread.
type SecretsGrant struct {
	Principal string `json:"principal"`
	Scope     string `json:"scope"`
	Mode      string `json:"mode"` // use | read
	// Expires is nil for a grant that does not, for the reason
	// SecretsComponent.LastSeen is a pointer.
	Expires *time.Time `json:"expires,omitempty"`
	Expired bool       `json:"expired,omitempty"`
}

// SecretsStatus is `terva secret status` as a struct — the same paths, modes,
// counts and reasons, with no value anywhere.
//
// It is the ONE producer: the CLI renders this struct rather than reading disk
// a second time, so the pane and the terminal cannot disagree about what is
// encrypted. That is the same ownership rule [AuthFlowStep] applies to a login
// form, pointed at a report.
type SecretsStatus struct {
	Key SecretsKey `json:"key"`
	// Recipient is the PUBLIC half recorded in config.json — safe to display,
	// and the thing a component seals to.
	Recipient string `json:"recipient,omitempty"`
	// RetiredKeys are keys that still OPEN files not yet rewritten and never
	// seal. A non-zero count is normal after a lazy rotation.
	RetiredKeys int                `json:"retired_keys,omitempty"`
	Files       []SecretsFile      `json:"files,omitempty"`
	Store       SecretsStore       `json:"store"`
	Config      SecretsConfigScan  `json:"config"`
	Components  []SecretsComponent `json:"components,omitempty"`
	// Reads is whether the agent may read each component's own directory —
	// the §8.1a payoff. Those trees stay readable, and each component earns it
	// by being verifiably free of plaintext secrets.
	Reads  []SecretsComponentRead `json:"reads,omitempty"`
	Grants []SecretsGrant         `json:"grants,omitempty"`
}

// SecretsScopeKeys is one scope and the names of the keys under it.
type SecretsScopeKeys struct {
	Scope string   `json:"scope"`
	Keys  []string `json:"keys,omitempty"`
}

// SecretsListResult is the store's contents by name. No values, ever.
type SecretsListResult struct {
	Scopes []SecretsScopeKeys `json:"scopes,omitempty"`
}

// SecretsGrantParams authorizes Principal against Scope.
//
// TTL is a DURATION ("720h"), not a timestamp, so the daemon computes the
// deadline against its own clock. A client that sent an absolute time would be
// asserting agreement about now, which is exactly what a phone in another
// timezone — or one with a slow clock — does not have.
type SecretsGrantParams struct {
	Principal string `json:"principal"`
	Scope     string `json:"scope"`
	Mode      string `json:"mode"`          // use | read
	TTL       string `json:"ttl,omitempty"` // Go duration; empty = no expiry
}

// SecretsRevokeParams withdraws one grant, named by both halves: a principal
// may hold grants on several scopes, and revoking all of them because one was
// named would be a surprise.
type SecretsRevokeParams struct {
	Principal string `json:"principal"`
	Scope     string `json:"scope"`
}

// SecretsForgetParams drops terva's record of a component.
//
// Purge separates two different acts that share a noun, the same way
// [AuthLogoutParams] and [AuthEndpointRemoveParams] do. Without it, forget stops
// TRACKING the component — the registry entry and its grants go, so an
// uninstalled component stops pinning retired keys open (§8.13 Q10) — and every
// value it stored is left in place. With it, those values are deleted too.
//
// Defaulting to the destructive form would mean a user unblocking a rotation
// silently loses a still-installed component's credential.
type SecretsForgetParams struct {
	Scope string `json:"scope"` // "conn:discord-ext"
	Purge bool   `json:"purge,omitempty"`
}

// SecretsForgetResult says what was actually removed, because "forget" on a
// scope terva never knew about must not read the same as one that worked.
type SecretsForgetResult struct {
	// Component reports that a registry entry was removed.
	Component bool `json:"component,omitempty"`
	// Grants is how many grants naming this scope — on either side — were
	// dropped.
	Grants int `json:"grants,omitempty"`
	// Values is how many stored values were deleted (purge only).
	Values int `json:"values,omitempty"`
	// Remaining is how many stored values were LEFT, so a non-purging caller
	// learns there is something still there to decide about.
	Remaining int `json:"remaining,omitempty"`
}
