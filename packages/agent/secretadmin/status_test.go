package secretadmin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
	"terva.sh/terva/packages/testsupport"
)

// sealedHome is a $TERVA_HOME after `terva secret init`: a key, and its public
// half recorded in config.json.
func sealedHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, secrets.KeyFileName), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: id.Recipient().String()}
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

// The one invariant this whole surface exists for: a report that names what is
// encrypted must never be a way to read it. Redaction that has to guess at
// field names fails open, so the property asserted is stronger — no value is
// ever HELD, in the struct or in the render.
func TestStatusNeverCarriesASecretValue(t *testing.T) {
	home := sealedHome(t)
	const storeSecret = "syt-live-matrix-token"
	const authSecret = "sk-live-deepseek"

	if err := config.SecretStoreIn(home).Set("conn:matrix", "access_token", storeSecret); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("deepseek", authSecret); err != nil {
		t.Fatal(err)
	}
	if err := config.SecretStoreIn(home).Grant(secretstore.Grant{
		Principal: "ext:memory", Scope: "conn:matrix", Mode: secretstore.ModeRead,
	}); err != nil {
		t.Fatal(err)
	}

	st := Status(home)
	// Sanity: the report must actually SEE the material, or this test proves
	// nothing — an empty struct trivially contains no secret.
	if len(st.Store.Scopes) == 0 {
		t.Fatal("the store scope never reached the report; the assertion below would be vacuous")
	}
	if len(st.Grants) == 0 {
		t.Fatal("the grant never reached the report; the assertion below would be vacuous")
	}

	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	WriteStatus(&rendered, st)
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	listRaw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var listRendered bytes.Buffer
	WriteList(&listRendered, list)

	for _, secret := range []string{storeSecret, authSecret} {
		for name, body := range map[string]string{
			"the status struct": string(raw),
			"the status render": rendered.String(),
			"the list struct":   string(listRaw),
			"the list render":   listRendered.String(),
		} {
			if strings.Contains(body, secret) {
				t.Errorf("%s carries a secret value (%q)", name, secret)
			}
		}
	}
	// The NAMES are the point of the list, and must survive — otherwise the
	// check above passes for a surface that reports nothing at all.
	if !strings.Contains(listRendered.String(), "access_token") {
		t.Errorf("the key name must be reported; it is schema, not material:\n%s", listRendered.String())
	}
}

// The one field in the report a reader can act on wrongly. Both states look
// like "no key resolves" and they call for opposite moves: init, or restore
// from backup. Collapsing them into a single "no key" is how someone generates
// a fresh key over ciphertext it will never open.
func TestAbsentKeyAndMissingKeyAreDifferentStates(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if got := Status(home).Key.State; got != ctrlproto.SecretsKeyAbsent {
		t.Fatalf("a home that never encrypted anything is `absent`, got %q", got)
	}

	// Now the same "no key file" situation, on a home where encryption WAS set
	// up: config.json still records the recipient.
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsxxxxxx"}
	}); err != nil {
		t.Fatal(err)
	}
	k := Status(home).Key
	if k.State != ctrlproto.SecretsKeyMissing {
		t.Fatalf("ciphertext exists but no key resolves — that is `missing`, got %q", k.State)
	}
	// And the reason has to carry the remedy, because "missing" alone tells a
	// user nothing about what to do next.
	if !strings.Contains(k.Reason, "backup") {
		t.Errorf("a missing key must point at the backup, got %q", k.Reason)
	}
}

// An unregistered component is the operationally important row: a rotation
// cannot re-seal a file whose recipient terva does not know, so `--revoke`
// leaves terva unable to read it. Detection is a content sniff, because a
// connector may be configured long after the daemon started.
func TestSealedComponentWithNoRegistryEntryIsFlagged(t *testing.T) {
	home := sealedHome(t)
	dir := filepath.Join(home, "connectors", "matrix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sealed := `{"bot_token":"` + secrets.FieldPrefix + `abc123","chat_id":42}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(sealed), 0o600); err != nil {
		t.Fatal(err)
	}

	comps := Status(home).Components
	if len(comps) != 1 {
		t.Fatalf("want exactly the unregistered connector, got %+v", comps)
	}
	if comps[0].Registered {
		t.Errorf("a connector with no registry entry must not read as registered: %+v", comps[0])
	}
	if got := UnregisteredSealedComponents(); len(got) != 1 || got[0] != "connectors/matrix" {
		t.Errorf("rotate's warning list must name it: %v", got)
	}

	// The other half, without which this cannot tell a targeted finding from a
	// blanket one: once the component registers, it stops being flagged.
	if err := secretstore.NewRegistry(home).Record(secretstore.Component{
		Name: "matrix", Kind: "conn", Recipient: "age1matrixrecipient",
		Paths: []string{"/bot_token"}, File: filepath.Join("connectors", "matrix", "config.json"),
	}); err != nil {
		t.Fatal(err)
	}
	comps = Status(home).Components
	if len(comps) != 1 || !comps[0].Registered {
		t.Fatalf("a registered component must be reported as registered exactly once: %+v", comps)
	}
	if got := UnregisteredSealedComponents(); len(got) != 0 {
		t.Errorf("a registered component must leave rotate's warning list: %v", got)
	}
}

// A connector state file with nothing sealed in it is not a finding. Without
// this, every connector anyone ever configured would be reported as a rotation
// hazard, and the row that matters would be lost in the noise.
func TestPlaintextComponentFileIsNotFlagged(t *testing.T) {
	home := sealedHome(t)
	dir := filepath.Join(home, "connectors", "matrix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"chat_id":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if comps := Status(home).Components; len(comps) != 0 {
		t.Errorf("a file with nothing sealed in it is not a rotation hazard: %+v", comps)
	}
}

// Forget composes two stores — the registry and the value store — and has to
// report which of them it actually touched.
func TestForgetRemovesTheRegistryEntryAndSaysSo(t *testing.T) {
	home := sealedHome(t)
	if err := secretstore.NewRegistry(home).Record(secretstore.Component{
		Name: "matrix", Kind: "conn", Recipient: "age1matrixrecipient",
		Paths: []string{"/bot_token"}, File: filepath.Join("connectors", "matrix", "config.json"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.SecretStoreIn(home).Set("conn:matrix", "access_token", "syt-live"); err != nil {
		t.Fatal(err)
	}

	res, err := Forget(ctrlproto.SecretsForgetParams{Scope: "conn:matrix"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Component {
		t.Error("the registry entry was removed; the result must say so")
	}
	if res.Remaining != 1 || res.Values != 0 {
		t.Errorf("without --purge the stored value stays and is counted: %+v", res)
	}
	left, err := secretstore.NewRegistry(home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("registry entry survived the forget: %+v", left)
	}

	// A second forget has nothing left to do, and must not claim otherwise.
	res, err = Forget(ctrlproto.SecretsForgetParams{Scope: "conn:matrix"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Component {
		t.Error("a forget of an already-forgotten component must not claim to have removed one")
	}
	var out bytes.Buffer
	WriteForget(&out, "conn:matrix", ctrlproto.SecretsForgetResult{})
	if !strings.Contains(out.String(), "nothing to forget") {
		t.Errorf("an empty forget must say so rather than reading as success: %q", out.String())
	}
}

// TTL is a duration parsed against the DAEMON's clock. A caller that sent an
// absolute time would be asserting agreement about now with a machine it may
// share neither a timezone nor an accurate clock with.
func TestGrantTTLIsADurationAndIsValidated(t *testing.T) {
	home := sealedHome(t)

	if err := Grant(ctrlproto.SecretsGrantParams{
		Principal: "ext:memory", Scope: "conn:matrix", Mode: "read", TTL: "720h",
	}); err != nil {
		t.Fatal(err)
	}
	gs, err := config.SecretStoreIn(home).Grants()
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Expires.IsZero() {
		t.Fatalf("the ttl must become a deadline on the stored grant: %+v", gs)
	}

	for _, bad := range []string{"tomorrow", "2026-08-04T00:00:00Z", "-1h"} {
		if err := Grant(ctrlproto.SecretsGrantParams{
			Principal: "ext:memory", Scope: "conn:matrix", Mode: "read", TTL: bad,
		}); err == nil {
			t.Errorf("ttl %q must be refused, not silently ignored into a grant that never expires", bad)
		}
	}
	// The refusals must not have replaced the good grant with a worse one.
	gs, err = config.SecretStoreIn(home).Grants()
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Expires.IsZero() {
		t.Fatalf("a refused ttl must leave the existing grant alone: %+v", gs)
	}
}

// Revoke needs both halves. A principal may hold grants on several scopes, and
// dropping all of them because one was named is a surprise in the direction
// that matters.
func TestRevokeNeedsBothHalves(t *testing.T) {
	sealedHome(t)
	if err := Revoke(ctrlproto.SecretsRevokeParams{Principal: "ext:memory"}); err == nil {
		t.Error("a revoke with no scope must be refused rather than guessed at")
	}
	if err := Revoke(ctrlproto.SecretsRevokeParams{Scope: "conn:matrix"}); err == nil {
		t.Error("a revoke with no principal must be refused rather than guessed at")
	}
}

// The report has to render on a home where things are wrong — that is when it
// is read. A store that will not open is a finding, not an outage.
func TestStatusRendersOnABrokenHome(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, secrets.KeyFileName), []byte("not-an-age-identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := Status(home)
	if st.Key.State != ctrlproto.SecretsKeyUnusable {
		t.Fatalf("an unparseable key is `unusable`, got %q", st.Key.State)
	}
	var out bytes.Buffer
	WriteStatus(&out, st)
	if !strings.Contains(out.String(), "UNUSABLE") {
		t.Errorf("the render must surface the unusable key, got:\n%s", out.String())
	}
}

// `omitempty` does nothing to a time.Time: it is a struct, so a never-seen
// component and a never-expiring grant both shipped "0001-01-01T00:00:00Z",
// which a client calling new Date() on renders as a real-looking date in year 1.
// The struct tags read correct — this was found by driving the actual wire.
func TestNeverSeenAndNeverExpiresAreAbsentOnTheWire(t *testing.T) {
	home := sealedHome(t)
	if err := secretstore.NewRegistry(home).Record(secretstore.Component{
		Name: "matrix", Kind: "conn", Recipient: "age1matrixrecipient",
		File: filepath.Join("connectors", "matrix", "config.json"),
		// LastSeen deliberately zero: registered, never handshaked.
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.SecretStoreIn(home).Grant(secretstore.Grant{
		Principal: "ext:memory", Scope: "conn:matrix", Mode: secretstore.ModeRead,
	}); err != nil {
		t.Fatal(err)
	}

	st := Status(home)
	if len(st.Components) != 1 || len(st.Grants) != 1 {
		t.Fatalf("precondition: want one component and one grant, got %d/%d", len(st.Components), len(st.Grants))
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "0001-01-01") {
		t.Errorf("a zero time reached the wire:\n%s", raw)
	}
	if st.Components[0].LastSeen != nil {
		t.Error("a component that never handshaked must report no last_seen at all")
	}
	if st.Grants[0].Expires != nil || st.Grants[0].Expired {
		t.Error("a grant with no expiry must not carry one, nor read as expired")
	}

	// The other half: a real timestamp must still cross. Without this, dropping
	// the field entirely would satisfy everything above.
	seen := time.Now().UTC().Truncate(time.Second)
	if err := secretstore.NewRegistry(home).Record(secretstore.Component{
		Name: "matrix", Kind: "conn", Recipient: "age1matrixrecipient",
		File: filepath.Join("connectors", "matrix", "config.json"), LastSeen: seen,
	}); err != nil {
		t.Fatal(err)
	}
	if err := Grant(ctrlproto.SecretsGrantParams{
		Principal: "ext:memory", Scope: "conn:matrix", Mode: "read", TTL: "1h",
	}); err != nil {
		t.Fatal(err)
	}
	st = Status(home)
	if st.Components[0].LastSeen == nil || !st.Components[0].LastSeen.Equal(seen) {
		t.Errorf("a real last_seen must survive: %+v", st.Components[0].LastSeen)
	}
	if st.Grants[0].Expires == nil {
		t.Error("a real expiry must survive")
	}
}
