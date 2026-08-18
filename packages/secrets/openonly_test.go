package secrets

import (
	"errors"
	"testing"

	"filippo.io/age"
)

// Enabled answers "should this save encrypt?", and for an opening-only codec it
// answered (false, nil) — which every caller reads as "plaintext operation is
// correct here". Both save paths (secretstore.saveLocked and auth.Store.save)
// then wrote the credential file in the CLEAR and returned no error, while
// Encrypt one screen below refused the identical operation with "this codec can
// only open, not seal".
//
// The comment justified it as avoiding a caller error hidden behind a write. It
// hid a plaintext write behind one instead — the single outcome this package
// exists to prevent.
func TestAnOpeningOnlyCodecRefusesToDecideAboutSaving(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	c := NewOpeningCodec(id)

	on, err := c.Enabled()
	if err == nil {
		t.Fatalf("Enabled returned (%v, nil): a save through this codec writes the credential "+
			"in plaintext and reports success", on)
	}
	if !errors.Is(err, ErrOpenOnly) {
		t.Errorf("error is %v, want ErrOpenOnly", err)
	}
	if on {
		t.Error("Enabled reported true alongside its error; a caller checking the bool first would encrypt with nothing")
	}

	// Encrypt already refused, and must refuse with the SAME sentinel so the
	// two answers cannot drift apart again.
	if _, err := c.Encrypt([]byte("sk-live")); !errors.Is(err, ErrOpenOnly) {
		t.Errorf("Encrypt error is %v, want ErrOpenOnly", err)
	}
}

// The complement, twice over: the two codecs that CAN seal must still say so,
// and a genuinely keyless configuration must still report plaintext operation
// without an error. Without these, "always refuse" would pass the test above
// and break every save in the product.
func TestTheCodecsThatCanSealStillReportEnabled(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("resolving", func(t *testing.T) {
		c := NewCodec(func() (*age.X25519Identity, error) { return id, nil })
		on, err := c.Enabled()
		if err != nil || !on {
			t.Errorf("Enabled = (%v, %v), want (true, nil)", on, err)
		}
	})

	t.Run("rotation", func(t *testing.T) {
		c := NewRotationCodec(id, id.Recipient())
		on, err := c.Enabled()
		if err != nil || !on {
			t.Errorf("Enabled = (%v, %v), want (true, nil) — a rotation codec seals to explicit recipients", on, err)
		}
	})

	t.Run("no key configured", func(t *testing.T) {
		c := NewCodec(func() (*age.X25519Identity, error) { return nil, ErrNoKey })
		on, err := c.Enabled()
		if err != nil {
			t.Errorf("a home with no key must be plaintext operation, not an error: %v", err)
		}
		if on {
			t.Error("Enabled reported true with no key configured")
		}
	})

	t.Run("broken key", func(t *testing.T) {
		boom := errors.New("unreadable key file")
		c := NewCodec(func() (*age.X25519Identity, error) { return nil, boom })
		if _, err := c.Enabled(); !errors.Is(err, boom) {
			t.Errorf("a configured-but-broken key must fail the write, got %v", err)
		}
	})
}
