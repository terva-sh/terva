package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
	"terva.sh/terva/packages/testsupport"
)

// installSecretExtension writes a manifest declaring one secret field, so the
// config scan has a schema to work from.
func installSecretExtension(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", "weather")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"weather","version":"1.0.0","description":"t","entry":"main",` +
		`"config":[{"key":"api_key","type":"secret","label":"key"}]}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readKeyFile(t *testing.T, home string) *age.X25519Identity {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := secrets.ParseIdentity(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSecretRotateMovesEverythingOntoANewKey(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-provider"); err != nil {
		t.Fatal(err)
	}
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	before := readKeyFile(t, home)

	if err := runSecretRotate([]string{"--revoke"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	after := readKeyFile(t, home)
	if after.String() == before.String() {
		t.Fatal("rotate left the same key on disk")
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secrets.Recipient != after.Recipient().String() {
		t.Fatalf("config recipient %q does not match the new key", cfg.Secrets.Recipient)
	}

	// The credential survives, readable through the normal path.
	creds, err := config.AuthStoreFor().Load()
	if err != nil {
		t.Fatal(err)
	}
	if creds.DeepSeek.APIKey != "sk-provider" {
		t.Fatalf("credential lost across rotation: %+v", creds.DeepSeek)
	}

	// And the OLD key no longer opens it — that is the point of rotating.
	if _, err := auth.NewStoreWithCodec(config.AuthPath(),
		secrets.NewRotationCodec(before, before.Recipient())).Load(); err == nil {
		t.Fatal("the previous key still opens auth.json after rotation")
	}
}

// Phase 1 of the rotation seals to BOTH keys. Interrupting there must leave a
// home the OLD key still opens, because the key file has not moved yet — this
// is what makes an interrupted rotation cost a re-run and not a re-login.
func TestRotationPhaseOneLeavesBothKeysWorking(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-provider"); err != nil {
		t.Fatal(err)
	}
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	old := readKeyFile(t, home)
	next, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	// Exactly what runSecretRotate does before touching the key file.
	if err := resealAll(testsupport.TempDir(t), old, []age.Recipient{old.Recipient(), next.Recipient()}, true); err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]*age.X25519Identity{"old": old, "new": next} {
		store := auth.NewStoreWithCodec(config.AuthPath(), secrets.NewRotationCodec(id, id.Recipient()))
		creds, err := store.Load()
		if err != nil {
			t.Fatalf("%s key cannot open auth.json mid-rotation: %v", name, err)
		}
		if creds.DeepSeek.APIKey != "sk-provider" {
			t.Fatalf("%s key opened a wrong value: %+v", name, creds.DeepSeek)
		}
	}
	// The key file is untouched at this point, so a crash here is recoverable
	// by simply running rotate again.
	if readKeyFile(t, home).String() != old.String() {
		t.Fatal("phase 1 replaced the key file; it must not")
	}
}

func TestSecretRotateWithoutKeyRefuses(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	err := runSecretRotate([]string{"--revoke"}, io.Discard)
	if err == nil {
		t.Fatal("rotate with no key configured must fail")
	}
	if !strings.Contains(err.Error(), "terva secret init") {
		t.Fatalf("error does not point at init: %v", err)
	}
}

// A value sealed to a key we no longer hold cannot be re-sealed. Rotation must
// find that out BEFORE it writes anything, or it strands the home halfway.
func TestSecretRotateRefusesUnopenableValueBeforeWriting(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	installSecretExtension(t, home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	keyBefore := readKeyFile(t, home)

	// A value sealed to somebody else's key.
	stranger, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	stranded, err := secrets.EncodeField(config.FieldBinding(config.ExtensionFieldPath("weather", "api_key")), "sk-unreachable", stranger.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(stranded)
	if err != nil {
		t.Fatal(err)
	}
	if err := build.SetExtensionConfigValues("weather", map[string]json.RawMessage{"api_key": blob}); err != nil {
		t.Fatal(err)
	}

	err = runSecretRotate([]string{"--revoke"}, io.Discard)
	if err == nil {
		t.Fatal("rotate proceeded past a value it cannot open")
	}
	if !strings.Contains(err.Error(), "extensions.weather.api_key") {
		t.Errorf("refusal does not name the offending value: %v", err)
	}
	if readKeyFile(t, home).String() != keyBefore.String() {
		t.Error("a refused rotation still replaced the key file")
	}
}

// The hygiene rotation: a new active key, the old one retired into the ring,
// and NOTHING rewritten. Existing files must still open — that is the whole
// difference from --revoke, and what makes a lazy rotation survivable for a
// file terva does not own.
func TestLazyRotateRetiresTheOldKeyAndRewritesNothing(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	installSecretExtension(t, home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-before-rotate"); err != nil {
		t.Fatal(err)
	}
	authBefore, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	keyBefore := readKeyFile(t, home).String()

	if err := runSecretRotate(nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	// The active key changed, and the old one is in the ring.
	if readKeyFile(t, home).String() == keyBefore {
		t.Fatal("the active key did not change")
	}
	retired, err := config.RetiredSecretsIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 1 || retired[0].String() != keyBefore {
		t.Fatalf("the old key was not retired: %d key(s)", len(retired))
	}

	// auth.json was NOT rewritten...
	authAfter, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(authAfter) != string(authBefore) {
		t.Error("a lazy rotation rewrote auth.json; that is --revoke's job")
	}
	// ...and still opens, through the retired key.
	c, err := config.AuthStoreFor().Load()
	if err != nil || c.DeepSeek.APIKey != "sk-before-rotate" {
		t.Fatalf("the credential did not survive a lazy rotation: %+v %v", c.DeepSeek, err)
	}

	// The next write heals it onto the new key: the retired key alone no
	// longer opens it.
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-after-rotate"); err != nil {
		t.Fatal(err)
	}
	healed, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Decrypt(retired[0], healed); err == nil {
		t.Error("after a write the file still opens with the retired key alone; it did not heal")
	}
}

// Rotating twice must accumulate, not replace: a file sealed before either
// rotation still has to open.
func TestLazyRotateTwiceKeepsBothRetiredKeys(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-oldest"); err != nil {
		t.Fatal(err)
	}

	if err := runSecretRotate(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := runSecretRotate(nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	retired, err := config.RetiredSecretsIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 2 {
		t.Fatalf("ring holds %d key(s), want 2", len(retired))
	}
	c, err := config.AuthStoreFor().Load()
	if err != nil || c.DeepSeek.APIKey != "sk-oldest" {
		t.Fatalf("a credential two rotations old stopped opening: %+v %v", c.DeepSeek, err)
	}
}

// --revoke after a lazy rotation must re-seal what the RETIRED key protects,
// not just what the active one does — otherwise it strands exactly the files it
// exists to rescue — and must then destroy the ring.
func TestRevokeAfterALazyRotateResealsAndDestroysTheRing(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-stranded"); err != nil {
		t.Fatal(err)
	}
	// A lazy rotation leaves auth.json sealed to the now-retired key.
	if err := runSecretRotate(nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	if err := runSecretRotate([]string{"--revoke"}, io.Discard); err != nil {
		t.Fatalf("--revoke could not rescue a file on a retired key: %v", err)
	}

	c, err := config.AuthStoreFor().Load()
	if err != nil || c.DeepSeek.APIKey != "sk-stranded" {
		t.Fatalf("credential lost across revoke: %+v %v", c.DeepSeek, err)
	}
	if retired, err := config.RetiredSecretsIdentities(); err != nil || len(retired) != 0 {
		t.Fatalf("--revoke left %d retired key(s) able to open files: %v", len(retired), err)
	}
	if _, err := os.Stat(config.RetiredSecretsKeyPath()); !os.IsNotExist(err) {
		t.Error("the ring file survived --revoke")
	}
}

// Rotation must re-seal a registered component's file to the NEW terva key AND
// the component's own — re-sealing to terva's key alone would lock the
// component out of its own state, which is the one outcome dual-recipient
// exists to prevent.
func TestRevokeResealsAComponentFileKeepingItsOwnRecipient(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	tervaOld, err := config.SecretsIdentity()
	if err != nil {
		t.Fatal(err)
	}

	// A connector with its own key, sealed to both parties, and registered the
	// way a handshake would.
	connKey, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env := secrets.Envelope{Scope: "conn:discord-ext", Paths: []string{"/bot_token"}}
	sealed, err := env.Seal([]byte(`{"bot_token":"dc-live","allowed_user_id":"7"}`),
		connKey.Recipient(), tervaOld.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("connectors", "discord-ext", "config.json")
	if err := privfs.MkdirAll(filepath.Dir(filepath.Join(home, rel))); err != nil {
		t.Fatal(err)
	}
	if err := privfs.WriteFile(filepath.Join(home, rel), sealed); err != nil {
		t.Fatal(err)
	}
	if err := secretstore.NewRegistry(home).Record(secretstore.Component{
		Name: "discord-ext", Kind: "conn", Recipient: connKey.Recipient().String(),
		Paths: []string{"/bot_token"}, File: rel,
	}); err != nil {
		t.Fatal(err)
	}

	if err := runSecretRotate([]string{"--revoke"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(filepath.Join(home, rel))
	if err != nil {
		t.Fatal(err)
	}
	// The connector still opens it with the key it has always had.
	opened, err := env.Open(after, connKey)
	if err != nil {
		t.Fatalf("the connector was locked out of its own file: %v", err)
	}
	if !strings.Contains(string(opened), "dc-live") {
		t.Fatal("the value did not survive the re-seal")
	}
	// And terva's NEW key opens it too.
	tervaNew, err := config.SecretsIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Open(after, tervaNew); err != nil {
		t.Fatalf("terva's new key cannot open the component file: %v", err)
	}
	// The retired terva key no longer does.
	if _, err := env.Open(after, tervaOld); err == nil {
		t.Error("the revoked key still opens the component file")
	}
}

// A sealed component file that never registered cannot be re-sealed, and status
// has to say so — otherwise the user learns about it when the credential stops
// working after a --revoke.
func TestStatusFlagsAnUnregisteredSealedComponent(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("connectors", "mystery", "config.json")
	if err := privfs.MkdirAll(filepath.Dir(filepath.Join(home, rel))); err != nil {
		t.Fatal(err)
	}
	if err := privfs.WriteFile(filepath.Join(home, rel),
		[]byte(`{"bot_token":"`+secrets.FieldPrefix+`abc"}`)); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runSecretStatus(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "connectors/mystery") || !strings.Contains(out.String(), "NOT registered") {
		t.Fatalf("status did not flag the unregistered sealed component:\n%s", out.String())
	}
}
