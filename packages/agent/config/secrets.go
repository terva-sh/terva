package config

import (
	"errors"
	"fmt"
	"path/filepath"

	"filippo.io/age"

	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
)

// SecretsConfig is the "secrets" config block: the at-rest encryption state
// `terva secret init` establishes. See docs/proposals/secrets-at-rest.md.
type SecretsConfig struct {
	// Recipient is the age public key (age1…) secret values are encrypted
	// to. NOT a secret: publishing it costs nothing, and carrying it here
	// lets tooling — `terva secret encrypt`, a setup script, even a model
	// editing this file — mint new encrypted values on a machine that does
	// not hold the private key.
	Recipient string `json:"recipient,omitempty"`
}

// Secrets-key source names, re-exported from packages/secrets so config
// callers need not import both. The lookup order itself lives down there
// (secrets.IdentityIn), because terva is no longer its only reader — see the
// note at the top of packages/secrets/keyfile.go.
const (
	SecretsKeyEnv     = secrets.KeyEnv
	SecretsKeyFileEnv = secrets.KeyFileEnv
)

// secretsKeyFileOverride is the --secrets-key-file flag's value, set once at
// startup by SetSecretsKeyFile. Highest-precedence key source, and the one
// piece of the chain that stays here: a flag is terva's, not the format's.
var secretsKeyFileOverride string

// SetSecretsKeyFile pins the secrets key file path (the --secrets-key-file
// flag). Call before anything resolves credentials.
func SetSecretsKeyFile(path string) { secretsKeyFileOverride = path }

// SecretsKeyPath returns the key FILE path in effect: the --secrets-key-file
// override, then whatever secrets.KeyPathIn resolves for the CREDENTIAL home —
// pinned global under project scoping, so a project-scoped run decrypts the
// same auth.json it inherits. This is the path `terva secret init` writes and
// doctor reports.
func SecretsKeyPath() string {
	if secretsKeyFileOverride != "" {
		return secretsKeyFileOverride
	}
	return secrets.KeyPathIn(CredentialHome())
}

// SecretsIdentity resolves the at-rest encryption key: the --secrets-key-file
// override first, then the shared chain (the key in the environment, a path in
// the environment, the default file beside the credential home).
//
// Deliberately NOT memoized — see secrets.IdentityIn for why a cache here is
// worse than no cache.
func SecretsIdentity() (*age.X25519Identity, error) {
	if p := secretsKeyFileOverride; p != "" {
		return secrets.LoadIdentityFile("--secrets-key-file", p, true)
	}
	return secrets.IdentityIn(CredentialHome())
}

// WebTokenName is the default web bearer-token file under the credential
// home. It sits beside auth.json rather than under the data home because it
// IS a credential: the token is the whole authentication for `terva web`, and
// a project-scoped run must present the same one as the daemon it attaches to.
const WebTokenName = "web-token"

// WebTokenPath returns the default web bearer-token file — what `terva secret
// web-token` writes and what `terva web` / `terva attach` fall back to when no
// flag or environment variable names one.
func WebTokenPath() string { return filepath.Join(CredentialHome(), WebTokenName) }

// SecretsRecipient resolves the public key new encrypted values are sealed to:
// the recipient recorded in config.json, else the one derived from the private
// key.
//
// Config first, deliberately. A recipient is NOT a secret, so a host that has
// only the public half — a provisioning script, a workstation whose key lives
// on a hardware token, a model editing config.json — can still SEAL a new
// value. Only reading it back needs the identity.
func SecretsRecipient() (age.Recipient, error) {
	if cfg, err := LoadConfig(); err == nil && cfg.Secrets != nil && cfg.Secrets.Recipient != "" {
		r, err := secrets.ParseRecipient(cfg.Secrets.Recipient)
		if err != nil {
			return nil, fmt.Errorf("config secrets.recipient: %w", err)
		}
		return r, nil
	}
	id, err := SecretsIdentity()
	if err != nil {
		return nil, err
	}
	return id.Recipient(), nil
}

// SecretsScope names config.json in a binding. The location half is the dotted
// path the scan and `terva secret status` already print, so a mismatch error
// reads in the same vocabulary the user was given.
const SecretsScope = "config"

// FieldBinding is the binding for a config.json value at a dotted scan path
// ("extensions.memory.api_key", "image.backends.local.api_key").
func FieldBinding(path string) string { return secrets.Binding(SecretsScope, path) }

// ExtensionFieldPath and ImageBackendKeyPath spell the two dotted paths that
// name a config secret. Constructors rather than inline concatenation because
// the same string is now three things at once — what the scan reports, what
// `terva secret status` prints, and what a sealed value is BOUND to — and a
// drift between any two of them would show up as an unopenable secret.
func ExtensionFieldPath(name, key string) string { return "extensions." + name + "." + key }

// ImageBackendKeyPath names an image backend's api_key.
func ImageBackendKeyPath(id string) string { return "image.backends." + id + ".api_key" }

// EncryptFieldValue seals one config value into the single-line field encoding,
// bound to the dotted path it is stored at — so it cannot later be moved to
// another path and opened there. See the secrets package doc.
func EncryptFieldValue(path, plain string) (string, error) {
	r, err := SecretsRecipient()
	if err != nil {
		return "", err
	}
	return secrets.EncodeField(FieldBinding(path), plain, r)
}

// DecryptFieldValue opens a field-encoded config value stored at the given
// dotted path, and returns anything UNMARKED unchanged.
//
// The pass-through is the migration story: config.json holds a mix of
// plaintext and ciphertext between `terva secret init` and the moment every
// value has been rewritten, and every consumer would otherwise need the same
// two-line check. A marked value that cannot be opened is an error — never the
// ciphertext itself, which would reach an extension as a password.
//
// The path is not decoration: it is checked against the binding sealed INSIDE
// the value, so a value moved to a path where it would be handed to a different
// consumer fails to open rather than leaking. Every caller therefore has to
// name where it read the value from.
func DecryptFieldValue(path, v string) (string, error) {
	if !secrets.IsEncryptedField(v) {
		return v, nil
	}
	id, err := SecretsIdentity()
	if err != nil {
		return "", fmt.Errorf("decrypt config secret: %w", err)
	}
	return secrets.DecodeField(id, FieldBinding(path), v)
}

// SecretsFieldEncryptionOn reports whether new secret values should be sealed.
// ErrNoKey means encryption is simply not set up (write plaintext, as before);
// any other error is a configured-but-broken key and must fail the write
// rather than silently downgrade it — the Codec.Enabled contract, for fields.
func SecretsFieldEncryptionOn() (bool, error) {
	_, err := SecretsRecipient()
	if errors.Is(err, secrets.ErrNoKey) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SecretsCodec returns the whole-file codec stores thread through their load
// and save paths. One shared codec would cache one identity; minting per call
// is cheap (the codec itself memoizes through SecretsIdentity's cache) and
// keeps path-redirecting tests honest.
func SecretsCodec() *secrets.Codec {
	return secrets.NewCodec(SecretsIdentity).WithRetired(RetiredSecretsIdentities)
}

// RetiredSecretsIdentities reads the keys that may only OPEN — what a lazy
// `terva secret rotate` leaves behind so files nothing has rewritten since
// still load. Beside the active key, in the credential home, for the same
// reason the active key lives there.
func RetiredSecretsIdentities() ([]*age.X25519Identity, error) {
	return secrets.RetiredIdentitiesIn(CredentialHome())
}

// RetiredSecretsKeyPath is the ring file.
func RetiredSecretsKeyPath() string { return secrets.RetiredKeyPathIn(CredentialHome()) }

// SecretsEncryptionConfigured reports whether an at-rest key is currently
// resolvable, without loading it into an error path. Shape-only helpers
// (status, doctor) use it; stores use the codec, which distinguishes
// no-key from broken-key.
func SecretsEncryptionConfigured() bool {
	_, err := SecretsIdentity()
	return err == nil
}

// SecretStoreIn returns the scoped secret store for a data home, wired to the
// same codec everything else uses.
//
// The home is a PARAMETER rather than resolved here because the callers that
// need it — the telegram and discord connectors — already receive theirs, and
// a helper that ignored what it was handed in favour of the ambient home is how
// a project-scoped run ends up writing to the global one.
func SecretStoreIn(home string) *secretstore.Store {
	return secretstore.New(filepath.Join(home, secretstore.FileName), SecretsCodec())
}

// SecretStorePath is where SecretStoreIn keeps its file.
func SecretStorePath(home string) string {
	return filepath.Join(home, secretstore.FileName)
}
