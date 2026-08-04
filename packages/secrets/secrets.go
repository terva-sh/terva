// Package secrets wraps the age encryption primitives that keep terva's
// secret material safe to READ at rest.
//
// The threat this package answers is not a stolen disk: it is a model — the
// agent's own tools, or an outside agent pointed at $TERVA_HOME — legitimately
// reading a config file and copying its contents into a session transcript,
// where they persist forever. Encrypted material makes that read harmless, so
// files can stay inspectable instead of denied wholesale.
//
// Two shapes, one key:
//
//   - whole-file: auth.json becomes a single binary age stream (detected by
//     IsAgeFile — every age v1 stream begins with an ASCII header line).
//   - field: an individual JSON string value becomes "enc:age:v2:<base64>",
//     single-line and round-trip-safe through any load/save that treats it as
//     an opaque string.
//
// The key is an X25519 identity (the age-keygen file format). The asymmetry is
// deliberate even though encrypt and decrypt usually happen in one process:
// the RECIPIENT is not a secret, so tooling — or a model editing config — can
// mint a new encrypted value with no access to the private key.
//
// # Values are BOUND to where they live
//
// age authenticates each value, so a hostile writer cannot forge or alter one.
// What it cannot stop on its own is RELOCATION: the files holding these values
// are writable (the sandbox write jail covers the file tools only — bash walks
// past it, and /unjail lifts it), so an attacker who cannot open a value can
// still MOVE it to a path where terva will open it and hand it somewhere else.
// Point-of-use decryption is what makes that pay: move a sealed image key into
// an extension's config field and the host dutifully delivers it.
//
// So a field seals a small JSON object, {"b":binding,"v":value}, rather than
// the bare string, and DecodeField refuses when the binding does not match
// where the value was found — the AAD trick, done by hand because age's Go API
// exposes no associated-data parameter. A whole-file MAC would also close it,
// but only by requiring byte-identical canonical JSON from every language that
// may ever write a shared file, which is a far worse bargain (see §8.13 Q11 of
// the proposal). Binding costs one nested object and needs no second key.
//
// It does not close everything, deliberately: DELETING a value is a denial of
// service no crypto prevents (the declared-path schema is what detects it), and
// REPLAYING an older ciphertext at its own binding survives until
// `terva secret rotate --revoke` destroys the key that opens it.
//
// This package knows nothing about terva's homes, flags, or config; callers
// hand it a resolver (see NewCodec) and policy lives with them. See
// docs/proposals/secrets-at-rest.md.
package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// FieldPrefix marks a field-encrypted value: "enc:age:v2:" + base64 of the
// binary age ciphertext, whose plaintext is a bound value (see the package
// doc). Versioned so a future scheme can coexist during a migration instead of
// guessing from shape.
const FieldPrefix = "enc:age:v2:"

// LegacyFieldPrefix marks the original UNBOUND encoding, whose plaintext was
// the raw value. It is still openable — migration and rotation must be able to
// upgrade one — but never at a point of use: an unbound value cannot prove it
// has not been moved, which is the whole point of v2.
const LegacyFieldPrefix = "enc:age:v1:"

// fileHeader begins every age v1 stream, binary format included.
const fileHeader = "age-encryption.org/v1"

// ErrNoKey reports that no secrets key is configured anywhere. Callers that
// resolve keys wrap it with where they looked; Codec.Enabled treats it as
// "encryption off" while any OTHER resolver error stays a hard failure, so a
// present-but-broken key can never silently degrade to plaintext writes.
var ErrNoKey = errors.New("no secrets key configured")

// IsAgeFile reports whether data begins an age v1 stream.
func IsAgeFile(data []byte) bool { return bytes.HasPrefix(data, []byte(fileHeader)) }

// IsEncryptedField reports whether s carries EITHER field encoding. Callers
// asking "is this still plaintext?" want both; callers that must open a value
// go through DecodeField, which accepts only the bound one.
func IsEncryptedField(s string) bool {
	return strings.HasPrefix(s, FieldPrefix) || strings.HasPrefix(s, LegacyFieldPrefix)
}

// IsLegacyField reports whether s carries the unbound v1 encoding.
func IsLegacyField(s string) bool { return strings.HasPrefix(s, LegacyFieldPrefix) }

// Binding names the one location a sealed value belongs to. The location
// vocabulary is the scope's own — config.json uses the dotted paths its scan
// and status output already speak ("extensions.memory.api_key"), a shared
// component file uses a JSON Pointer — because a binding only has to be stable
// and unambiguous within its scope, and reusing the vocabulary the surface
// already prints keeps the mismatch error readable.
func Binding(scope, location string) string { return scope + "|" + location }

// boundValue is what a v2 field actually seals. Short keys because this is
// encrypted overhead on every secret, and the shape is closed — an unknown
// field would mean a v3.
type boundValue struct {
	B string `json:"b"`
	V string `json:"v"`
}

// GenerateIdentity mints a fresh X25519 identity.
func GenerateIdentity() (*age.X25519Identity, error) { return age.GenerateX25519Identity() }

// ParseIdentity reads an identity out of key-file content in the age-keygen
// format: any number of blank or #-comment lines around a single
// AGE-SECRET-KEY-1… line. Trailing material after the first key line is
// rejected rather than ignored — two keys in one file is a confused state,
// not a choice.
func ParseIdentity(content string) (*age.X25519Identity, error) {
	var found *age.X25519Identity
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if found != nil {
			return nil, errors.New("secrets key holds more than one non-comment line")
		}
		id, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, fmt.Errorf("parse secrets key: %w", err)
		}
		found = id
	}
	if found == nil {
		return nil, errors.New("secrets key holds no AGE-SECRET-KEY line")
	}
	return found, nil
}

// ParseRecipient parses an age1… recipient string.
func ParseRecipient(s string) (*age.X25519Recipient, error) {
	return age.ParseX25519Recipient(strings.TrimSpace(s))
}

// Encrypt seals plaintext in the binary age format, readable by ANY of the
// given recipients.
//
// Several recipients is what makes key rotation safe to interrupt: a file
// sealed to both the old and the new key opens with either, so there is no
// moment where the key on disk cannot read the files beside it. See
// NewRotationCodec.
func Encrypt(plaintext []byte, recipients ...age.Recipient) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("encrypt: no recipients")
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decrypt opens an age stream with id.
func Decrypt(id age.Identity, ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// EncodeField seals value into the single-line field encoding, BOUND to the
// location it is being stored at (see Binding) and readable by any of the given
// recipients.
//
// An empty binding is refused rather than treated as "unbound": every caller
// knows where it is writing, and a silent unbound seal would be a value that
// can be moved.
func EncodeField(binding, value string, recipients ...age.Recipient) (string, error) {
	if binding == "" {
		return "", errors.New("encode field: refusing to seal a value with no binding")
	}
	plain, err := json.Marshal(boundValue{B: binding, V: value})
	if err != nil {
		return "", err
	}
	ct, err := Encrypt(plain, recipients...)
	if err != nil {
		return "", err
	}
	return FieldPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// DecodeField opens a field-encoded value and verifies it was sealed FOR the
// location it was found at. This is the point-of-use entry point, so it is
// strict on both counts: an unbound v1 value is refused (it cannot prove it has
// not been moved) and a binding mismatch is refused (it has been). A string
// without either prefix is an error too — the caller decides whether unprefixed
// means legacy plaintext, this package does not guess.
func DecodeField(id age.Identity, binding, s string) (string, error) {
	if IsLegacyField(s) {
		return "", fmt.Errorf("value uses the unbound %q encoding and cannot prove it belongs at %q; re-seal it with `terva secret migrate`", LegacyFieldPrefix, binding)
	}
	got, plain, err := decodeAny(id, s)
	if err != nil {
		return "", err
	}
	if got != binding {
		return "", fmt.Errorf("encrypted value is bound to %q but was found at %q — it has been moved, and opening it here would hand it to the wrong consumer", got, binding)
	}
	return plain, nil
}

// DecodeAnyField opens a field-encoded value of EITHER version without checking
// the binding, and reports the binding it carried ("" for a v1 value).
//
// Only migration and rotation may use it: they rewrite a value in place, so
// they need to open one whose binding is absent (v1) or wrong (moved) in order
// to report it. Every other caller wants DecodeField.
func DecodeAnyField(id age.Identity, s string) (binding, value string, err error) {
	if b64, ok := strings.CutPrefix(s, LegacyFieldPrefix); ok {
		ct, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", "", fmt.Errorf("decode encrypted field: %w", err)
		}
		plain, err := Decrypt(id, ct)
		if err != nil {
			return "", "", err
		}
		return "", string(plain), nil
	}
	return decodeAny(id, s)
}

// decodeAny opens a v2 field and returns its declared binding, without judging
// it.
func decodeAny(id age.Identity, s string) (binding, value string, err error) {
	b64, ok := strings.CutPrefix(s, FieldPrefix)
	if !ok {
		return "", "", fmt.Errorf("not a %q-encoded value", FieldPrefix)
	}
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", fmt.Errorf("decode encrypted field: %w", err)
	}
	plain, err := Decrypt(id, ct)
	if err != nil {
		return "", "", err
	}
	var bv boundValue
	if err := json.Unmarshal(plain, &bv); err != nil {
		return "", "", fmt.Errorf("encrypted value is not a bound value: %w", err)
	}
	if bv.B == "" {
		return "", "", errors.New("encrypted value carries an empty binding")
	}
	return bv.B, bv.V, nil
}

// Codec is the whole-file encrypt/decrypt handle a store threads through its
// load and save paths. It resolves the identity per operation, through the
// resolver it was built with — so a key generated after process start (terva
// secret init) is picked up without rebuilding any store, and a store on a
// home with no key configured stays plaintext.
//
// Nothing is memoized, deliberately. A cached identity survives a key that
// does not: after `terva secret rotate` a stale copy would fail to open the
// files it just re-sealed and, far worse, would SEAL a later save to the
// retired key — writing a credential only the old key can read, which is
// exactly the stranding this feature exists to avoid. The cost of correctness
// is re-reading a few hundred bytes per auth-file load or save.
type Codec struct {
	resolve func() (*age.X25519Identity, error)
	// sealTo, when set, is who saves are encrypted to instead of the
	// resolved identity's own recipient. Only rotation sets it — see
	// NewRotationCodec.
	sealTo []age.Recipient

	// retired resolves keys that may only OPEN. A file sealed before a lazy
	// rotation still opens, without the rotation having had to rewrite it —
	// which is the whole point of a hygiene rotation, and the only way a file
	// terva does not own can survive one at all.
	retired func() ([]*age.X25519Identity, error)

	// open, when set, is the ONLY identity used for decryption — resolve and
	// retired are ignored. Rotation and pre-flight checks use it to prove a
	// file opens with one specific key or ring.
	open func() (age.Identity, error)
}

// WithRetired returns a copy of c that also opens with the keys resolve
// reports. Sealing is untouched: retired keys never receive anything, or
// "retired" would mean nothing.
func (c *Codec) WithRetired(resolve func() ([]*age.X25519Identity, error)) *Codec {
	if c == nil {
		return nil
	}
	next := *c
	next.retired = resolve
	return &next
}

// NewRotationCodec returns a Codec that OPENS with one identity and SEALS to
// different recipients — the asymmetry key rotation is made of.
//
// Rotation runs it twice: once sealing to {old, new} so every file opens with
// either key while the key file is swapped, then once sealing to {new} alone.
// Neither pass ever leaves a file that the key on disk cannot open, so an
// interrupted rotation costs a re-run and not the credentials.
func NewRotationCodec(open age.Identity, sealTo ...age.Recipient) *Codec {
	return &Codec{
		open:   func() (age.Identity, error) { return open, nil },
		sealTo: sealTo,
	}
}

// NewOpeningCodec returns a Codec that can only OPEN — it seals to nothing.
// For a pre-flight check that has to prove a file is readable without deciding
// what it would be re-sealed to.
func NewOpeningCodec(open age.Identity) *Codec {
	return &Codec{open: func() (age.Identity, error) { return open, nil }}
}

// NewCodec builds a Codec over resolve. resolve returns the identity, or an
// error wrapping ErrNoKey when no key is configured anywhere (Enabled treats
// exactly that as "off"); any other error is a configured-but-broken key and
// stays fatal to the operation that needed it.
func NewCodec(resolve func() (*age.X25519Identity, error)) *Codec {
	return &Codec{resolve: resolve}
}

func (c *Codec) identity() (*age.X25519Identity, error) { return c.resolve() }

// opener returns the identity to decrypt with: an explicit one when this codec
// was built to open with a specific key or ring, otherwise the active key plus
// the retired ring.
func (c *Codec) opener() (age.Identity, error) {
	if c.open != nil {
		return c.open()
	}
	id, err := c.identity()
	if err != nil {
		return nil, err
	}
	ids := []age.Identity{id}
	if c.retired != nil {
		old, err := c.retired()
		if err != nil {
			return nil, fmt.Errorf("read retired keys: %w", err)
		}
		for _, r := range old {
			ids = append(ids, r)
		}
	}
	return Ring(ids...), nil
}

// Enabled reports whether saves should encrypt. ErrNoKey means plaintext
// operation (false, nil); any other resolver error is returned so a broken
// key configuration fails the write instead of silently downgrading it.
func (c *Codec) Enabled() (bool, error) {
	if c.resolve == nil {
		// An opening-only codec never seals; saving through one is a caller
		// error, and reporting "enabled" would hide it behind a write.
		return len(c.sealTo) > 0, nil
	}
	_, err := c.identity()
	if errors.Is(err, ErrNoKey) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Encrypt seals plain to the resolved identity's recipient, or to sealTo when
// this is a rotation codec.
func (c *Codec) Encrypt(plain []byte) ([]byte, error) {
	if len(c.sealTo) > 0 {
		return Encrypt(plain, c.sealTo...)
	}
	if c.resolve == nil {
		return nil, errors.New("secrets: this codec can only open, not seal")
	}
	id, err := c.identity()
	if err != nil {
		return nil, err
	}
	return Encrypt(plain, id.Recipient())
}

// Decrypt opens data with the active identity, falling back to the retired
// ring. Called only on data that IsAgeFile matched, so ErrNoKey here is a hard
// error — the file is unreadable without the key, and the resolver's wrapping
// says where the key was expected.
//
// A retired key opening a file is not a failure and is not reported as one: it
// is exactly what a lazy rotation leaves behind, and the file heals on its next
// write, which seals to the active key.
func (c *Codec) Decrypt(data []byte) ([]byte, error) {
	id, err := c.opener()
	if err != nil {
		return nil, err
	}
	return DecryptAny(data, id)
}
