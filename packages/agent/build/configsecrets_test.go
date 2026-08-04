package build

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// installExtension writes a manifest declaring one secret and one plain field,
// so the scan has a schema to work from.
func installExtension(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"1.0.0","description":"t","entry":"main",` +
		`"config":[{"key":"api_key","type":"secret","label":"key"},{"key":"units","type":"string","label":"units"}]}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanConfigSecretsFindsPlaintextAndSealed(t *testing.T) {
	home := encryptingHome(t)
	installExtension(t, home, "weather")
	cwd := testsupport.TempDir(t)

	sealed, err := config.EncryptFieldValue(config.ExtensionFieldPath("weather", "api_key"), "sk-sealed")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Extensions = map[string]map[string]json.RawMessage{
			"weather": {
				"api_key": json.RawMessage(`"sk-plain"`),
				"units":   json.RawMessage(`"celsius"`),
			},
		}
		c.Image = &config.ImageConfig{Backends: map[string]config.ImageBackendConfig{
			"local": {APIKey: sealed},
		}}
	}); err != nil {
		t.Fatal(err)
	}

	got := ScanConfigSecrets(cwd)
	if len(got) != 2 {
		t.Fatalf("scan found %d secrets, want 2: %+v", len(got), got)
	}
	byPath := map[string]bool{}
	for _, s := range got {
		byPath[s.Path] = s.Encrypted
	}
	if enc, ok := byPath["extensions.weather.api_key"]; !ok || enc {
		t.Errorf("extension secret should be reported plaintext: %+v", byPath)
	}
	if enc, ok := byPath["image.backends.local.api_key"]; !ok || !enc {
		t.Errorf("image key should be reported encrypted: %+v", byPath)
	}
	// A non-secret field is never listed — that is what keeps the rest of
	// config.json readable rather than swept into the secret set.
	if _, ok := byPath["extensions.weather.units"]; ok {
		t.Error("a plain string field was reported as a secret")
	}
}

func TestEncryptConfigSecretsSealsPlaintextOnly(t *testing.T) {
	home := encryptingHome(t)
	installExtension(t, home, "weather")
	cwd := testsupport.TempDir(t)

	if err := config.MutateConfig(func(c *config.Config) {
		c.Extensions = map[string]map[string]json.RawMessage{
			"weather": {"api_key": json.RawMessage(`"sk-plain"`), "units": json.RawMessage(`"celsius"`)},
		}
	}); err != nil {
		t.Fatal(err)
	}

	changed, err := EncryptConfigSecrets(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "extensions.weather.api_key" {
		t.Fatalf("changed = %v, want just the secret field", changed)
	}
	for _, s := range ScanConfigSecrets(cwd) {
		if !s.Encrypted {
			t.Errorf("%s still plaintext after the sweep", s.Path)
		}
	}
	// Non-secret values survive untouched.
	cfg, _ := config.LoadConfig()
	if string(cfg.Extensions["weather"]["units"]) != `"celsius"` {
		t.Errorf("a non-secret value was rewritten: %s", cfg.Extensions["weather"]["units"])
	}

	// Idempotent: a second sweep finds nothing to do and does not double-seal.
	again, err := EncryptConfigSecrets(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep changed %v, want nothing", again)
	}
}

// An extension whose manifest is gone has values no schema can classify.
// Reporting "clean" there would be a false negative with security weight, so
// the unknowns are named separately.
func TestConfigSecretsUnknownNamesUnclassifiableBlocks(t *testing.T) {
	encryptingHome(t)
	cwd := testsupport.TempDir(t)
	if err := config.MutateConfig(func(c *config.Config) {
		c.Extensions = map[string]map[string]json.RawMessage{
			"ghost": {"api_key": json.RawMessage(`"sk-orphan"`)},
		}
	}); err != nil {
		t.Fatal(err)
	}
	if got := ScanConfigSecrets(cwd); len(got) != 0 {
		t.Fatalf("scan classified a schema-less block: %+v", got)
	}
	unknown := ConfigSecretsUnknown(cwd)
	if len(unknown) != 1 || unknown[0] != "ghost" {
		t.Fatalf("unknown = %v, want [ghost]", unknown)
	}
}

// The payoff, and every way it must refuse. Each case is a state where
// config.json could still disclose a secret, so the verdict has to be false —
// a false "clean" here hands the agent a credential.
func TestConfigReadableByAgentFailsClosed(t *testing.T) {
	t.Run("clean config is readable", func(t *testing.T) {
		home := encryptingHome(t)
		installExtension(t, home, "weather")
		cwd := testsupport.TempDir(t)
		sealed, err := config.EncryptFieldValue(config.ExtensionFieldPath("weather", "api_key"), "sk-sealed")
		if err != nil {
			t.Fatal(err)
		}
		blob, _ := json.Marshal(sealed)
		if err := config.MutateConfig(func(c *config.Config) {
			c.Extensions = map[string]map[string]json.RawMessage{
				"weather": {"api_key": blob, "units": json.RawMessage(`"celsius"`)},
			}
			c.MCP = &mcp.Config{Servers: map[string]mcp.ServerConfig{
				"files": {Command: "mcp-files", Headers: map[string]string{"X-Key": "${MCP_KEY}"}},
			}}
		}); err != nil {
			t.Fatal(err)
		}
		if ok, why := ConfigReadableByAgent(cwd); !ok {
			t.Fatalf("a fully sealed config should be readable, refused: %s", why)
		}
	})

	t.Run("a plaintext secret keeps it denied", func(t *testing.T) {
		home := encryptingHome(t)
		installExtension(t, home, "weather")
		cwd := testsupport.TempDir(t)
		if err := config.MutateConfig(func(c *config.Config) {
			c.Extensions = map[string]map[string]json.RawMessage{
				"weather": {"api_key": json.RawMessage(`"sk-plain"`)},
			}
		}); err != nil {
			t.Fatal(err)
		}
		ok, why := ConfigReadableByAgent(cwd)
		if ok {
			t.Fatal("a plaintext secret must keep config.json denied")
		}
		if !strings.Contains(why, "extensions.weather.api_key") {
			t.Errorf("reason does not name the offending path: %s", why)
		}
	})

	t.Run("an unclassifiable block keeps it denied", func(t *testing.T) {
		encryptingHome(t)
		cwd := testsupport.TempDir(t)
		if err := config.MutateConfig(func(c *config.Config) {
			c.Extensions = map[string]map[string]json.RawMessage{
				"ghost": {"api_key": json.RawMessage(`"sk-orphan"`)},
			}
		}); err != nil {
			t.Fatal(err)
		}
		ok, why := ConfigReadableByAgent(cwd)
		if ok {
			t.Fatal("a block with no manifest must keep config.json denied — unknown is not clean")
		}
		if !strings.Contains(why, "ghost") {
			t.Errorf("reason does not name the block: %s", why)
		}
	})

	t.Run("a literal mcp env value keeps it denied", func(t *testing.T) {
		encryptingHome(t)
		cwd := testsupport.TempDir(t)
		if err := config.MutateConfig(func(c *config.Config) {
			c.MCP = &mcp.Config{Servers: map[string]mcp.ServerConfig{
				"api": {Command: "x", Env: map[string]string{"TOKEN": "literal-secret"}},
			}}
		}); err != nil {
			t.Fatal(err)
		}
		ok, why := ConfigReadableByAgent(cwd)
		if ok {
			t.Fatal("a literal MCP env value may be a token — must keep config.json denied")
		}
		if !strings.Contains(why, "mcp.servers.api.env.TOKEN") {
			t.Errorf("reason does not name the value: %s", why)
		}
	})

	// A URL carries credentials in a field named nothing like one. The census
	// (configsecrets_census_test.go) is what surfaced this: mcp.servers.*.url
	// is a string nobody had classified, and classifying it honestly meant
	// admitting scheme://user:pass@host is a password in config.json.
	t.Run("an mcp url with userinfo keeps it denied", func(t *testing.T) {
		encryptingHome(t)
		cwd := testsupport.TempDir(t)
		if err := config.MutateConfig(func(c *config.Config) {
			c.MCP = &mcp.Config{Servers: map[string]mcp.ServerConfig{
				"remote": {Transport: "http", URL: "https://alice:hunter2@mcp.example.com/v1"},
			}}
		}); err != nil {
			t.Fatal(err)
		}
		ok, why := ConfigReadableByAgent(cwd)
		if ok {
			t.Fatal("a URL with userinfo holds a password — must keep config.json denied")
		}
		if !strings.Contains(why, "mcp.servers.remote.url") {
			t.Errorf("reason does not name the value: %s", why)
		}
	})

	t.Run("an ordinary mcp url is fine", func(t *testing.T) {
		encryptingHome(t)
		cwd := testsupport.TempDir(t)
		if err := config.MutateConfig(func(c *config.Config) {
			c.MCP = &mcp.Config{Servers: map[string]mcp.ServerConfig{
				"remote": {Transport: "http", URL: "https://mcp.example.com/v1"},
			}}
		}); err != nil {
			t.Fatal(err)
		}
		if ok, why := ConfigReadableByAgent(cwd); !ok {
			t.Fatalf("a URL with no userinfo carries no credential: %s", why)
		}
	})

	t.Run("an env reference is not a secret", func(t *testing.T) {
		encryptingHome(t)
		cwd := testsupport.TempDir(t)
		if err := config.MutateConfig(func(c *config.Config) {
			c.MCP = &mcp.Config{Servers: map[string]mcp.ServerConfig{
				"api": {Command: "x", Env: map[string]string{"TOKEN": "${REAL_TOKEN}"}},
			}}
		}); err != nil {
			t.Fatal(err)
		}
		if ok, why := ConfigReadableByAgent(cwd); !ok {
			t.Fatalf("a ${ENV} reference points at a secret rather than holding one: %s", why)
		}
	})
}

func TestEncryptConfigSecretsNoKeyIsNoOp(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	installExtension(t, home, "weather")
	cwd := testsupport.TempDir(t)
	if err := config.MutateConfig(func(c *config.Config) {
		c.Extensions = map[string]map[string]json.RawMessage{
			"weather": {"api_key": json.RawMessage(`"sk-plain"`)},
		}
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := EncryptConfigSecrets(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("sweep changed %v with no key configured", changed)
	}
}

// A value can fail to open for three unrelated reasons, and rotation's refusal
// names one of them to the operator. Sending someone to hunt for a lost key
// when the real cause is a value somebody MOVED is a wasted afternoon, so the
// classification is asserted rather than assumed.
func TestVerifyConfigSecretsExplainsWhyAValueWillNotOpen(t *testing.T) {
	home := encryptingHome(t)
	installExtension(t, home, "weather")
	cwd := testsupport.TempDir(t)

	id, err := config.SecretsIdentity()
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	here := config.ExtensionFieldPath("weather", "api_key")
	elsewhere := config.ImageBackendKeyPath("local")

	legacyCT, err := secrets.Encrypt([]byte("sk-old"), id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := secrets.EncodeField(config.FieldBinding(here), "sk-foreign", stranger.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	moved, err := secrets.EncodeField(config.FieldBinding(elsewhere), "sk-moved", id.Recipient())
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		stored string
		want   string
	}{
		{"a lost key", foreign, "different key"},
		{"a moved value", moved, "it was moved"},
		{"the older unbound encoding", secrets.LegacyFieldPrefix + base64.StdEncoding.EncodeToString(legacyCT), "migrate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob, _ := json.Marshal(tc.stored)
			if err := SetExtensionConfigValues("weather", map[string]json.RawMessage{"api_key": blob}); err != nil {
				t.Fatal(err)
			}
			bad := VerifyConfigSecrets(cwd, id)
			if len(bad) != 1 {
				t.Fatalf("verify reported %d problems, want 1: %+v", len(bad), bad)
			}
			if bad[0].Path != here {
				t.Errorf("reported path %q, want %q", bad[0].Path, here)
			}
			if !strings.Contains(bad[0].Reason, tc.want) {
				t.Errorf("reason %q does not mention %q", bad[0].Reason, tc.want)
			}
		})
	}
}
