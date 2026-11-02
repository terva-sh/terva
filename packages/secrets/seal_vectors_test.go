package secrets

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"terva.sh/seal"
)

// terva.sh/seal's testdata/vectors.json is the compatibility contract for the
// field format, and terva's packages/secrets wrote every field in it. seal's
// own tests run the vectors through seal.Open. This runs them through terva's
// layer, which adds the v1 refusal, the migration reader and the identity
// ring, so a seal upgrade that changes an outcome fails here and not in a
// user's config.
//
// The file is read from the module cache rather than copied in, so it is
// always the one the required version ships.

type sealVector struct {
	Name       string   `json:"name"`
	Field      string   `json:"field"`
	OpenAt     string   `json:"open_at"`
	Identities []string `json:"identities"`
	Expect     string   `json:"expect"`
	Value      *string  `json:"value"`
}

func TestSealVectorsOpenThroughTervasLayer(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "terva.sh/seal").Output()
	if err != nil {
		t.Fatalf("go list -m terva.sh/seal: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Seeds      map[string]string `json:"seeds"`
		Recipients map[string]string `json:"recipients"`
		Vectors    []sealVector      `json:"vectors"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	// Vacuity floor: an empty or renamed list would pass every case below.
	if len(f.Vectors) < 10 {
		t.Fatalf("vectors.json holds %d vectors; the test is not reading the contract", len(f.Vectors))
	}
	ids := map[string]*age.X25519Identity{}
	for name, s := range f.Seeds {
		ids[name] = vectorIdentity(t, s)
		if got := ids[name].Recipient().String(); got != f.Recipients[name] {
			t.Fatalf("identity %s derives recipient %s, the file records %s", name, got, f.Recipients[name])
		}
	}

	sentinels := map[string]error{"mismatch": seal.ErrMismatch, "version": seal.ErrVersion, "not_sealed": seal.ErrNotSealed}
	seen := map[string]bool{}
	for _, v := range f.Vectors {
		seen[v.Expect] = true
		t.Run(v.Name, func(t *testing.T) {
			var open []age.Identity
			for _, name := range v.Identities {
				open = append(open, ids[name])
			}
			id := Ring(open...)

			if want := v.Expect != "not_sealed"; IsEncryptedField(v.Field) != want {
				t.Errorf("IsEncryptedField = %v, want %v", !want, want)
			}

			got, err := DecodeField(id, v.OpenAt, v.Field)
			switch v.Expect {
			case "ok":
				if err != nil {
					t.Fatalf("DecodeField: %v", err)
				}
				if v.Value == nil || got != *v.Value {
					t.Fatalf("DecodeField = %q, want %v", got, v.Value)
				}
				// The migration reader opens the same field and reports the
				// binding it was sealed for, which here is where it was found.
				binding, value, err := DecodeAnyField(id, v.Field)
				if err != nil || binding != v.OpenAt || value != *v.Value {
					t.Fatalf("DecodeAnyField = (%q, %q, %v), want (%q, %q)", binding, value, err, v.OpenAt, *v.Value)
				}
			case "no_identity":
				var none *age.NoIdentityMatchError
				if !errors.As(err, &none) {
					t.Fatalf("DecodeField error = %v, want no matching identity", err)
				}
			case "malformed":
				if err == nil {
					t.Fatalf("DecodeField accepted a malformed field and returned %q", got)
				}
				for _, s := range sentinels {
					if errors.Is(err, s) {
						t.Fatalf("DecodeField error %v is a sentinel; a malformed field is none of them", err)
					}
				}
			default:
				s, ok := sentinels[v.Expect]
				if !ok {
					t.Fatalf("unknown expectation %q", v.Expect)
				}
				if !errors.Is(err, s) {
					t.Fatalf("DecodeField error = %v, want %v", err, s)
				}
			}
			if err != nil && v.Value != nil && *v.Value != "" && strings.Contains(err.Error(), *v.Value) {
				t.Fatalf("error quotes the value: %v", err)
			}
		})
	}
	for _, want := range []string{"ok", "mismatch", "version", "not_sealed", "malformed", "no_identity"} {
		if !seen[want] {
			t.Errorf("no vector expects %q; the contract lost a case", want)
		}
	}
}

// vectorIdentity derives a vector identity the way seal's tests do: the
// X25519 scalar is SHA-256 of the public seed, Bech32-encoded (BIP 173) as an
// AGE-SECRET-KEY-1 line. The keys protect nothing, and deriving them keeps
// any key line out of the tree.
func vectorIdentity(t *testing.T, s string) *age.X25519Identity {
	t.Helper()
	k := sha256.Sum256([]byte(s))
	id, err := age.ParseX25519Identity(strings.ToUpper(bech32Encode("age-secret-key-", k[:])))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func bech32Encode(hrp string, data []byte) string {
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	polymod := func(values []byte) uint32 {
		gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
		chk := uint32(1)
		for _, v := range values {
			top := chk >> 25
			chk = (chk&0x1ffffff)<<5 ^ uint32(v)
			for i := 0; i < 5; i++ {
				if (top>>uint(i))&1 == 1 {
					chk ^= gen[i]
				}
			}
		}
		return chk
	}
	var five []byte
	acc, bits := 0, 0
	for _, b := range data {
		acc = acc<<8 | int(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			five = append(five, byte(acc>>bits&31))
		}
	}
	if bits > 0 {
		five = append(five, byte(acc<<(5-bits)&31))
	}
	var exp []byte
	for _, c := range hrp {
		exp = append(exp, byte(c>>5))
	}
	exp = append(exp, 0)
	for _, c := range hrp {
		exp = append(exp, byte(c&31))
	}
	mod := polymod(append(append(exp, five...), 0, 0, 0, 0, 0, 0)) ^ 1
	var sb strings.Builder
	sb.WriteString(hrp + "1")
	for _, v := range five {
		sb.WriteByte(charset[v])
	}
	for i := 0; i < 6; i++ {
		sb.WriteByte(charset[mod>>uint(5*(5-i))&31])
	}
	return sb.String()
}
