package secrets

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"filippo.io/age"
)

// Finding the key, for any process that must open the same files.
//
// This lived in packages/agent/config, which is fine while terva is the only
// reader. It is not: `terva-mcp-bridge` deliberately imports ZERO core packages
// (it re-implements home resolution in thirty lines rather than depend on
// terva), standalone connectors are separate binaries by definition, and a
// connector may not even be written in Go. Every one of them needs the same
// answer to "where is the key", and a second implementation of a lookup order
// is a second thing to get wrong.
//
// So resolution lives here, one level below config. The package still knows
// nothing about how terva RESOLVES a home — callers hand it the directory, and
// the flag override (--secrets-key-file) stays in config, where the flag is.
// What does live here is the environment contract, because that is exactly the
// part a foreign process has to agree with us about.

// Key sources. The key is an age X25519 identity in the age-keygen file
// format; KeyEnv carries the identity ITSELF (for containers /
// EnvironmentFile=), KeyFileEnv a path to it. Deliberately TERVA_-only: the
// feature postdates the rename, so no legacy spelling exists to honor.
const (
	KeyEnv      = "TERVA_SECRETS_KEY"
	KeyFileEnv  = "TERVA_SECRETS_KEY_FILE"
	KeyFileName = "secrets.key"
)

// KeyPathIn returns the key FILE path in effect for a home: KeyFileEnv, then
// <home>/secrets.key. KeyEnv (the identity in the environment) has no path and
// is consulted only by IdentityIn.
func KeyPathIn(home string) string {
	if p := strings.TrimSpace(os.Getenv(KeyFileEnv)); p != "" {
		return p
	}
	return filepath.Join(home, KeyFileName)
}

// keyFromEnv reads KeyEnv once and scrubs it from the environment. The scrub
// is worth doing and is not a boundary: the value lingers in /proc/<pid>/environ
// on Linux, so the FILE routes remain the stronger posture.
var keyFromEnv = sync.OnceValue(func() string {
	v := strings.TrimSpace(os.Getenv(KeyEnv))
	if v != "" {
		os.Unsetenv(KeyEnv)
	}
	return v
})

// IdentityIn resolves the at-rest encryption key for a home, in order: the
// KeyEnv identity, the KeyFileEnv path, then <home>/secrets.key.
//
// An explicitly named source that is missing or unreadable is a hard error — an
// operator who pointed at a key must never silently run without it. Only the
// default path may be absent, which returns an error wrapping ErrNoKey:
// encryption is simply not configured, and stores run plaintext.
//
// Deliberately NOT memoized, and neither is Codec. A cached identity outlives
// the key it copied: after a rotation a stale copy fails to open what was just
// re-sealed and, far worse, SEALS a later save to the retired key. The file is
// a few hundred bytes; read it.
func IdentityIn(home string) (*age.X25519Identity, error) {
	if v := keyFromEnv(); v != "" {
		id, err := ParseIdentity(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", KeyEnv, err)
		}
		return id, nil
	}
	if p := strings.TrimSpace(os.Getenv(KeyFileEnv)); p != "" {
		return LoadIdentityFile(KeyFileEnv, p, true)
	}
	return LoadIdentityFile("", filepath.Join(home, KeyFileName), false)
}

// LoadIdentityFile reads and parses one key file. required distinguishes an
// operator-named source (absence is a failure) from the default probe (absence
// means encryption is off, reported as ErrNoKey with enough context to act on).
func LoadIdentityFile(source, path string, required bool) (*age.X25519Identity, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && !required {
		return nil, fmt.Errorf("%w (expected key file at %s; run `terva secret init`, or point %s at your key)",
			ErrNoKey, path, KeyFileEnv)
	}
	if err != nil {
		if source != "" {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		return nil, err
	}
	id, err := ParseIdentity(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return id, nil
}

// RetiredKeyFileName holds keys that may only OPEN, never seal.
//
// A separate file from secrets.key on purpose. secrets.key stays exactly one
// identity, because it is the file a foreign process reads — the mcp-bridge, a
// Rust connector, `age -d -i` at a shell — and "the active key" must not become
// "whichever of these lines you happen to pick". The ring is terva core's
// business and nothing else needs to parse it.
const RetiredKeyFileName = "secrets.keyring"

// RetiredKeyPathIn is where the ring lives for a home.
func RetiredKeyPathIn(home string) string {
	return filepath.Join(home, RetiredKeyFileName)
}

// ParseIdentities reads MANY identities out of age-keygen-format content, one
// per non-comment line. Unlike ParseIdentity — which rejects a second key,
// because two keys in a file meant to hold one is a confused state — several
// here is the point.
func ParseIdentities(content string) ([]*age.X25519Identity, error) {
	var out []*age.X25519Identity
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, fmt.Errorf("keyring line %d: %w", i+1, err)
		}
		out = append(out, id)
	}
	return out, nil
}

// RetiredIdentitiesIn reads a home's ring. An absent ring is not an error —
// most homes never rotate.
func RetiredIdentitiesIn(home string) ([]*age.X25519Identity, error) {
	b, err := os.ReadFile(RetiredKeyPathIn(home))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseIdentities(string(b))
}

// DecryptAny opens ciphertext with the first identity that fits. age tries each
// recipient block against each identity, so this is one call rather than a loop
// — and it is what lets a retired key still open a file nothing has rewritten
// since the rotation.
func DecryptAny(ciphertext []byte, ids ...age.Identity) ([]byte, error) {
	if len(ids) == 0 {
		return nil, ErrNoKey
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), ids...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// ring tries each identity in turn. age.Decrypt already accepts several
// identities, but every function in this repo that opens something takes ONE —
// VerifyConfigSecrets, ResealConfigSecrets, the store's Reseal — and a
// composite keeps all of those signatures while letting a caller hand over the
// whole keyring. Widening five signatures to variadic would have been the same
// change spelled five times.
type ring []age.Identity

func (r ring) Unwrap(stanzas []*age.Stanza) ([]byte, error) {
	for _, id := range r {
		key, err := id.Unwrap(stanzas)
		if err == nil {
			return key, nil
		}
	}
	return nil, age.ErrIncorrectIdentity
}

// Ring returns one age.Identity that opens anything ANY of ids opens. Used
// wherever a caller holds an active key plus a retired set — after a lazy
// rotation, a file nothing has rewritten still opens with a retired key, and
// the caller should not have to care which.
func Ring(ids ...age.Identity) age.Identity { return ring(ids) }
