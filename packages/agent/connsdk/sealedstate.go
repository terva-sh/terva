package connsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/secrets"
)

// SealedState is how a standalone connector keeps its OWN credentials without
// handing terva its whole state, and without terva handing it a private key.
//
// The connector holds its own age key, in its own state directory, and seals
// each declared secret to TWO recipients: its own, and terva's. Both can open
// exactly the declared values; neither can open the other's key. Everything
// else in the file — chat ids, pairing claims, poll offsets — stays plaintext,
// so a connector's state remains as inspectable as it ever was.
//
// The dual recipient is not politeness. terva has to be able to open these
// values to rotate or audit them, and it must be able to do that while the
// connector is stopped, disabled, or uninstalled — which rules out asking the
// connector to do it. See docs/proposals/secrets-at-rest.md §8.3–8.4.
//
// # Bootstrap needs no handshake
//
// terva's recipient is a PUBLIC key recorded in config.json, so a connector can
// read it during its host-less `setup` verb, before it has ever spoken to a
// host. That is why sealing works on first configuration rather than only after
// a connection.
//
// # How terva learns the recipient
//
// From the HANDSHAKE, and only from there. Set Config.Secrets to this same
// value and the SDK declares the recipient and the declared paths in hello; the
// host records them and can then re-seal during a rotation. The state file is
// never a source for that: it is writable by anything that can write
// $TERVA_HOME, so trusting a recipient from it would turn write access into
// future read access.
type SealedState struct {
	// Name is the connector's registered name; it decides the state directory
	// and the binding scope.
	Name string
	// Paths are JSON Pointers to the secret values in this connector's config,
	// e.g. "/bot_token". Declaring them is what lets the file be called clean
	// rather than merely unscanned.
	Paths []string
}

// keyFileName is the connector's own identity, beside its config.
const keyFileName = "secrets.key"

// Dir is the connector's state directory.
func (s SealedState) Dir() string { return StateDir(s.Name) }

// Path is the connector's config file.
func (s SealedState) Path() string { return filepath.Join(s.Dir(), "config.json") }

// KeyPath is the connector's own key. Separate from terva's: a compromised
// connector must not be able to open the host's files, which is exactly what
// sharing terva's key would have allowed.
func (s SealedState) KeyPath() string { return filepath.Join(s.Dir(), keyFileName) }

func (s SealedState) envelope() secrets.Envelope {
	return secrets.Envelope{Scope: "conn:" + s.Name, Paths: s.Paths}
}

// Load reads the config and opens every declared secret with the connector's
// own key, returning the document with plaintext back in place. Unmarshal it
// into whatever shape the connector uses.
//
// An absent file is an empty document, not an error: a connector that has never
// been configured is a normal state.
func (s SealedState) Load() ([]byte, error) {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return []byte("{}"), nil
	}
	if err != nil {
		return nil, err
	}
	id, err := s.identity(false)
	if err != nil {
		if errors.Is(err, secrets.ErrNoKey) {
			// No key of our own yet, so nothing we wrote is sealed. Anything
			// sealed in there was not sealed by us and we could not open it
			// anyway — Open leaves unsealed values alone and would fail loudly
			// on a sealed one, which is the right noise.
			return b, nil
		}
		return nil, err
	}
	return s.envelope().Open(b, id)
}

// Save seals every declared secret and writes the config, owner-only.
//
// Sealing is best-effort in exactly one direction: with no terva recipient
// available the file is written PLAINTEXT, as it always was, because a
// connector must remain configurable on a host that has never run
// `terva secret init`. Any other failure fails the write rather than silently
// downgrading it.
func (s SealedState) Save(doc []byte) error {
	if err := privfs.MkdirAll(s.Dir()); err != nil {
		return err
	}
	tervaRecipient, err := s.hostRecipient()
	if err != nil {
		if !errors.Is(err, secrets.ErrNoKey) {
			return err
		}
		return privfs.WriteFile(s.Path(), normalize(doc))
	}
	id, err := s.identity(true)
	if err != nil {
		return err
	}
	sealed, err := s.envelope().Seal(doc, id.Recipient(), tervaRecipient)
	if err != nil {
		return err
	}
	return privfs.WriteFile(s.Path(), sealed)
}

// Clean reports whether every declared secret in the file on disk is sealed.
// An absent file is clean — there is nothing in it to leak.
func (s SealedState) Clean() error {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.envelope().Clean(b)
}

// identity loads the connector's own key, creating one when create is set.
// Never memoized: a cached identity outlives a rotation, and the connector's
// key can rotate independently of terva's.
func (s SealedState) identity(create bool) (*age.X25519Identity, error) {
	b, err := os.ReadFile(s.KeyPath())
	switch {
	case err == nil:
		return secrets.ParseIdentity(string(b))
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	case !create:
		return nil, fmt.Errorf("%w (no connector key at %s)", secrets.ErrNoKey, s.KeyPath())
	}
	id, err := secrets.GenerateIdentity()
	if err != nil {
		return nil, err
	}
	if err := privfs.MkdirAll(s.Dir()); err != nil {
		return nil, err
	}
	content := fmt.Sprintf("# %s connector key — opens this connector's own secrets\n# public key: %s\n%s\n",
		s.Name, id.Recipient(), id)
	if err := privfs.WriteFile(s.KeyPath(), []byte(content)); err != nil {
		return nil, err
	}
	return id, nil
}

// hostRecipient reads terva's PUBLIC recipient out of config.json.
//
// Deliberately a hand-rolled read of one field rather than an import of
// packages/agent/config: a connector is its own process — possibly not even a
// Go one — and the contract it depends on has to be "a public key at a known
// path in a JSON file", not "link terva's config package".
func (s SealedState) hostRecipient() (age.Recipient, error) {
	b, err := os.ReadFile(filepath.Join(envcompat.Home(), "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, secrets.ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Secrets struct {
			Recipient string `json:"recipient"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("read terva recipient: %w", err)
	}
	if cfg.Secrets.Recipient == "" {
		return nil, secrets.ErrNoKey
	}
	return secrets.ParseRecipient(cfg.Secrets.Recipient)
}

// normalize re-marshals a document so an unsealed write looks like a sealed
// one — same indentation, same trailing newline, so turning encryption on does
// not show up as a whole-file diff.
func normalize(doc []byte) []byte {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return doc
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return doc
	}
	return append(b, '\n')
}

// Recipient returns this connector's PUBLIC key, or "" when it has no key yet
// (nothing has been sealed). Never creates one: minting a key as a side effect
// of being asked for its public half would register a component that holds no
// secrets.
func (s SealedState) Recipient() (string, error) {
	id, err := s.identity(false)
	if errors.Is(err, secrets.ErrNoKey) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// RelPath is the state file relative to the data home — what the host records
// in its registry, since the registry travels with the home.
func (s SealedState) RelPath() string {
	return filepath.Join("connectors", s.Name, "config.json")
}
