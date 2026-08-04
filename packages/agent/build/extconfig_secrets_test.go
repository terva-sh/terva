package build

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// encryptingHome pins TERVA_HOME to a fresh dir holding a secrets key, so
// field encryption is live for the test.
func encryptingHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "secrets.key"), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: id.Recipient().String()}
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

var secretSchema = []extdriver.ConfigField{
	{Key: "api_key", Type: "secret"},
	{Key: "units", Type: "string"},
}

// A submitted secret is sealed on disk and arrives at the extension in the
// clear — the whole point: config.json becomes safe to read without the
// extension noticing any difference.
func TestExtensionSecretSealedOnDiskOpenedOnDelivery(t *testing.T) {
	encryptingHome(t)

	typed, err := TypeExtensionConfigValues("weather", secretSchema,
		map[string]string{"api_key": "sk-live-42", "units": "celsius"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetExtensionConfigValues("weather", typed); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-live-42") {
		t.Fatal("the secret is plaintext in config.json")
	}
	if !strings.Contains(string(raw), secrets.FieldPrefix) {
		t.Fatalf("no encrypted field marker in config.json:\n%s", raw)
	}
	// A non-secret field stays readable — the reason this is field-level and
	// not whole-file: a model can still work on the rest of the config.
	if !strings.Contains(string(raw), "celsius") {
		t.Fatal("a non-secret value was encrypted too")
	}

	got := ResolveExtensionConfig("weather", secretSchema)
	var key string
	if err := json.Unmarshal(got["api_key"], &key); err != nil {
		t.Fatal(err)
	}
	if key != "sk-live-42" {
		t.Fatalf("extension received %q, want the cleartext secret", key)
	}
}

// A blank secret keeps what is stored, and must not re-seal (double-encrypt)
// or open it in the process.
func TestBlankSecretPassesSealedValueThrough(t *testing.T) {
	encryptingHome(t)

	first, err := TypeExtensionConfigValues("weather", secretSchema, map[string]string{"api_key": "sk-original"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TypeExtensionConfigValues("weather", secretSchema,
		map[string]string{"api_key": "", "units": "celsius"}, first)
	if err != nil {
		t.Fatal(err)
	}
	if string(second["api_key"]) != string(first["api_key"]) {
		t.Fatal("a blank secret rewrote the stored ciphertext")
	}
	if err := SetExtensionConfigValues("weather", second); err != nil {
		t.Fatal(err)
	}
	got := ResolveExtensionConfig("weather", secretSchema)
	var key string
	if err := json.Unmarshal(got["api_key"], &key); err != nil {
		t.Fatal(err)
	}
	if key != "sk-original" {
		t.Fatalf("delivered %q after a blank resubmit, want the original secret", key)
	}
}

// Pre-migration config: plaintext values still resolve, so enabling encryption
// never strands an extension that was configured before the key existed.
func TestPlaintextValuesStillResolve(t *testing.T) {
	encryptingHome(t)
	if err := SetExtensionConfigValues("weather", map[string]json.RawMessage{
		"api_key": json.RawMessage(`"sk-legacy"`),
	}); err != nil {
		t.Fatal(err)
	}
	got := ResolveExtensionConfig("weather", secretSchema)
	var key string
	if err := json.Unmarshal(got["api_key"], &key); err != nil {
		t.Fatal(err)
	}
	if key != "sk-legacy" {
		t.Fatalf("delivered %q, want the legacy plaintext value", key)
	}
}

// With no key configured nothing changes: values are stored and delivered
// exactly as before, so the feature is inert until someone opts in.
func TestNoKeyStoresPlaintextAsBefore(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	typed, err := TypeExtensionConfigValues("weather", secretSchema, map[string]string{"api_key": "sk-plain"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(typed["api_key"]) != `"sk-plain"` {
		t.Fatalf("stored %s, want plaintext when no key is configured", typed["api_key"])
	}
}

// A value sealed to a DIFFERENT key must never reach the extension as
// ciphertext — a password-shaped string that would fail far from here.
func TestUnopenableSecretIsDroppedNotDelivered(t *testing.T) {
	encryptingHome(t)
	other, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	stranded, err := secrets.EncodeField(config.FieldBinding(config.ExtensionFieldPath("weather", "api_key")), "sk-unreachable", other.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(stranded)
	if err := SetExtensionConfigValues("weather", map[string]json.RawMessage{"api_key": blob}); err != nil {
		t.Fatal(err)
	}
	got := ResolveExtensionConfig("weather", secretSchema)
	if v, ok := got["api_key"]; ok {
		t.Fatalf("delivered an unopenable value: %s", v)
	}
}

// The image backend's key follows the same rule, opened where the backend is
// built rather than where the config loads.
func TestImageBackendKeyIsOpenedAtConstruction(t *testing.T) {
	encryptingHome(t)
	sealed, err := config.EncryptFieldValue(config.ImageBackendKeyPath("img"), "sk-image-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildImageBackend("img", config.ImageBackendConfig{
		Protocol: protocolOpenAIImages, BaseURL: "https://example.invalid", APIKey: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	oi, ok := b.(*imagegen.OpenAIImages)
	if !ok {
		t.Fatalf("built %T, want *imagegen.OpenAIImages", b)
	}
	if oi.APIKey != "sk-image-1" {
		t.Fatalf("backend holds %q, want the opened key", oi.APIKey)
	}
}

// The attack binding exists to stop, end to end. An attacker who cannot OPEN a
// sealed value can still WRITE the file it lives in — the sandbox write jail
// covers the file tools only, bash walks past it, and /unjail lifts it — so
// they move the ciphertext to a path whose consumer will hand it somewhere
// they can see. Point-of-use decryption is what makes that pay.
//
// Here the image backend's key is relocated into an extension's config field.
// Without binding the host would open it and deliver it to the extension.
func TestRelocatedSecretIsNotDeliveredToAnotherConsumer(t *testing.T) {
	encryptingHome(t)

	sealed, err := config.EncryptFieldValue(config.ImageBackendKeyPath("local"), "sk-image-key")
	if err != nil {
		t.Fatal(err)
	}
	// Prove the value is genuinely openable where it belongs, so the refusal
	// below is the binding and not a broken key or a stray typo.
	if got, err := config.DecryptFieldValue(config.ImageBackendKeyPath("local"), sealed); err != nil || got != "sk-image-key" {
		t.Fatalf("sealed value does not open at its own path: %q, %v", got, err)
	}

	blob, _ := json.Marshal(sealed)
	if err := SetExtensionConfigValues("weather", map[string]json.RawMessage{"api_key": blob}); err != nil {
		t.Fatal(err)
	}
	got := ResolveExtensionConfig("weather", secretSchema)
	if v, ok := got["api_key"]; ok {
		t.Fatalf("delivered a value sealed for a different path: %s", v)
	}
}

// migrate is the verb that gets everything onto the current scheme, so it must
// upgrade a value still carrying the unbound v1 encoding. Leaving that to
// rotate would mean migrate reporting nothing to do while the point of use
// refuses the value.
func TestMigrateUpgradesAnUnboundValue(t *testing.T) {
	home := encryptingHome(t)
	installExtension(t, home, "weather")
	cwd := testsupport.TempDir(t)

	id, err := config.SecretsIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := secrets.Encrypt([]byte("sk-legacy"), id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	legacy := secrets.LegacyFieldPrefix + base64.StdEncoding.EncodeToString(ct)
	blob, _ := json.Marshal(legacy)
	if err := SetExtensionConfigValues("weather", map[string]json.RawMessage{"api_key": blob}); err != nil {
		t.Fatal(err)
	}

	// Before: the value is encrypted, so a scan calls it sealed — but delivery
	// drops it, because unbound cannot prove it has not been moved.
	if v, ok := ResolveExtensionConfig("weather", secretSchema)["api_key"]; ok {
		t.Fatalf("an unbound value was delivered: %s", v)
	}

	changed, err := EncryptConfigSecrets(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != config.ExtensionFieldPath("weather", "api_key") {
		t.Fatalf("migrate did not upgrade the unbound value, changed = %v", changed)
	}
	got := ResolveExtensionConfig("weather", secretSchema)
	var delivered string
	if err := json.Unmarshal(got["api_key"], &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != "sk-legacy" {
		t.Fatalf("after migrate the extension received %q", delivered)
	}
}
