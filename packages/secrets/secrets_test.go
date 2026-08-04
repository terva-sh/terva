package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"anthropic":{"api_key":"sk-test"}}`)
	ct, err := Encrypt(plain, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if !IsAgeFile(ct) {
		t.Fatalf("ciphertext does not begin with the age header: %q", ct[:min(len(ct), 32)])
	}
	if IsAgeFile(plain) {
		t.Fatal("plaintext JSON sniffed as an age file")
	}
	got, err := Decrypt(id, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("round trip mismatch: %q", got)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	right, _ := GenerateIdentity()
	wrong, _ := GenerateIdentity()
	ct, err := Encrypt([]byte("secret"), right.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(wrong, ct); err == nil {
		t.Fatal("decrypt with the wrong identity succeeded")
	}
}

func TestFieldEncoding(t *testing.T) {
	id, _ := GenerateIdentity()
	enc, err := EncodeField(Binding("config", "extensions.weather.api_key"), "hunter2", id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedField(enc) {
		t.Fatalf("encoded field lacks prefix: %q", enc)
	}
	if strings.ContainsAny(enc, "\n\r") {
		t.Fatalf("encoded field is not single-line: %q", enc)
	}
	got, err := DecodeField(id, Binding("config", "extensions.weather.api_key"), enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("round trip mismatch: %q", got)
	}
	if _, err := DecodeField(id, Binding("config", "extensions.weather.api_key"), "hunter2"); err == nil {
		t.Fatal("decoding an unprefixed value succeeded; the caller must decide what plaintext means")
	}
}

// A value sealed for one path must not open at another. This is the whole
// point of v2: the files holding these values are writable (bash walks past
// the sandbox write jail), so an attacker who cannot OPEN a secret can still
// MOVE it to a path where terva opens it and hands it to someone else.
func TestFieldBindingRefusesAMovedValue(t *testing.T) {
	id, _ := GenerateIdentity()
	here := Binding("config", "image.backends.local.api_key")
	there := Binding("config", "extensions.weather.api_key")

	enc, err := EncodeField(here, "hunter2", id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeField(id, there, enc); err == nil {
		t.Fatal("a value sealed for one path opened at another; relocation is undetected")
	}
	// The same identity opens it at home, so the refusal is the binding and
	// not a broken key.
	if got, err := DecodeField(id, here, enc); err != nil || got != "hunter2" {
		t.Fatalf("value did not open at its own binding: %q, %v", got, err)
	}
}

// An unbound v1 value cannot prove it has not been moved, so a point of use
// must refuse it outright — while migration and rotation must still be able to
// open one in order to upgrade it.
func TestLegacyFieldOpensOnlyForMigration(t *testing.T) {
	id, _ := GenerateIdentity()
	ct, err := Encrypt([]byte("hunter2"), id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	legacy := LegacyFieldPrefix + base64.StdEncoding.EncodeToString(ct)

	if !IsEncryptedField(legacy) {
		t.Fatal("a v1 value must still read as encrypted, or a scan would call it plaintext")
	}
	if !IsLegacyField(legacy) {
		t.Fatal("IsLegacyField missed a v1 value")
	}
	if _, err := DecodeField(id, Binding("config", "extensions.weather.api_key"), legacy); err == nil {
		t.Fatal("point-of-use decode accepted an unbound value")
	}
	binding, value, err := DecodeAnyField(id, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if binding != "" || value != "hunter2" {
		t.Fatalf("migration decode of a v1 value = (%q, %q)", binding, value)
	}
}

// A binding is required at seal time. An empty one would mint a value that
// nothing can move-check, which is a v1 value wearing a v2 prefix.
func TestEncodeFieldRefusesAnEmptyBinding(t *testing.T) {
	id, _ := GenerateIdentity()
	if _, err := EncodeField("", "hunter2", id.Recipient()); err == nil {
		t.Fatal("sealed a value with no binding")
	}
}

func TestParseIdentityKeygenFormat(t *testing.T) {
	id, _ := GenerateIdentity()
	content := "# created: sometime\n# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"
	parsed, err := ParseIdentity(content)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Recipient().String() != id.Recipient().String() {
		t.Fatal("parsed identity derives a different recipient")
	}
	if _, err := ParseIdentity("# only comments\n"); err == nil {
		t.Fatal("comment-only content parsed as an identity")
	}
	if _, err := ParseIdentity(id.String() + "\n" + id.String() + "\n"); err == nil {
		t.Fatal("two keys in one file parsed; a confused state must be rejected")
	}
}

func TestParseRecipient(t *testing.T) {
	id, _ := GenerateIdentity()
	r, err := ParseRecipient("  " + id.Recipient().String() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.String() != id.Recipient().String() {
		t.Fatal("recipient round trip mismatch")
	}
}

func TestCodecNoKeyIsPlaintextOperation(t *testing.T) {
	c := NewCodec(func() (*age.X25519Identity, error) {
		return nil, fmt.Errorf("%w (expected at /nowhere)", ErrNoKey)
	})
	on, err := c.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Fatal("codec with no key reports enabled")
	}
	if _, err := c.Decrypt([]byte("age-encryption.org/v1\n...")); err == nil {
		t.Fatal("decrypt with no key succeeded")
	}
}

func TestCodecBrokenKeyFailsClosed(t *testing.T) {
	c := NewCodec(func() (*age.X25519Identity, error) { return nil, errors.New("key file unreadable") })
	if _, err := c.Enabled(); err == nil {
		t.Fatal("a configured-but-broken key must fail Enabled, not downgrade to plaintext")
	}
}

func TestCodecPicksUpLateKey(t *testing.T) {
	var id *age.X25519Identity
	c := NewCodec(func() (*age.X25519Identity, error) {
		if id == nil {
			return nil, ErrNoKey
		}
		return id, nil
	})
	if on, _ := c.Enabled(); on {
		t.Fatal("enabled before any key exists")
	}
	id, _ = GenerateIdentity()
	on, err := c.Enabled()
	if err != nil || !on {
		t.Fatalf("codec did not pick up a key created after first use (on=%v err=%v)", on, err)
	}
	ct, err := c.Encrypt([]byte("late"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(ct)
	if err != nil || string(got) != "late" {
		t.Fatalf("codec round trip failed (err=%v got=%q)", err, got)
	}
}
