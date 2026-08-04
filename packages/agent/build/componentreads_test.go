package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
	"terva.sh/terva/packages/testsupport"
)

// componentHome is a $TERVA_HOME with a jailed sandbox wired the way build.go
// wires it, so every assertion below goes through the real check.
func componentHome(t *testing.T) (*tools.Sandbox, string, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	sb := tools.NewSandbox(cwd)
	restrictSensitiveReads(sb, home, cwd, true)
	sb.Lock()
	return sb, home, cwd
}

// writeConnector plants a connector state file, sealed or not.
func writeConnector(t *testing.T, home, name, token string) string {
	t.Helper()
	dir := filepath.Join(home, "connectors", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"bot_token": token, "chat_id": 42}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func register(t *testing.T, home, name string, paths []string) {
	t.Helper()
	if err := secretstore.NewRegistry(home).Record(secretstore.Component{
		Name: name, Kind: "conn", Recipient: "age1matrixrecipient",
		Paths: paths, File: filepath.Join("connectors", name, "config.json"),
	}); err != nil {
		t.Fatal(err)
	}
}

// A connector that has never handshaked declared nothing, so nothing can vouch
// for what is in its file — and its file is exactly where a bot token sits.
// This is §8.1a's case: the tree was readable and the token was in the clear.
func TestUnregisteredConnectorStateIsDenied(t *testing.T) {
	sb, home, _ := componentHome(t)
	p := writeConnector(t, home, "matrix", secrets.FieldPrefix+"sealed")

	err := sb.CheckPathRead(p)
	if err == nil {
		t.Fatal("an unregistered connector's state must not be readable, even when it LOOKS sealed")
	}
	if !strings.Contains(err.Error(), "start it once") {
		t.Errorf("the refusal must name the fix, got: %v", err)
	}

	// The other half, without which this cannot tell a targeted denial from a
	// blanket one: registering makes the same file readable.
	register(t, home, "matrix", []string{"/bot_token"})
	if err := sb.CheckPathRead(p); err != nil {
		t.Fatalf("a registered connector with every declared path sealed must be readable: %v", err)
	}
}

// A registered connector whose declared value is plaintext is the buggy-or-old
// component case, and it must name the path rather than just refusing.
func TestConnectorWithAPlaintextDeclaredValueIsDenied(t *testing.T) {
	sb, home, _ := componentHome(t)
	p := writeConnector(t, home, "matrix", "8888:AAAA-a-real-bot-token")
	register(t, home, "matrix", []string{"/bot_token"})

	err := sb.CheckPathRead(p)
	if err == nil {
		t.Fatal("a plaintext declared value must keep the tree denied")
	}
	if !strings.Contains(err.Error(), "/bot_token") {
		t.Errorf("the refusal must name the offending path, got: %v", err)
	}
}

// Registering while declaring NOTHING is not the same as not registering, and
// must not be a way to buy readability by handshaking with an empty list.
func TestConnectorThatDeclaresNoPathsIsDenied(t *testing.T) {
	sb, home, _ := componentHome(t)
	p := writeConnector(t, home, "matrix", "8888:AAAA-a-real-bot-token")
	register(t, home, "matrix", nil)

	if err := sb.CheckPathRead(p); err == nil {
		t.Fatal("declaring no paths vouches for nothing; the tree must stay denied")
	}
}

// The property that makes this a guard rather than a startup snapshot.
//
// A component is a live process: it can write a plaintext credential into its
// own file at any moment. A verdict computed when the sandbox was built would
// still be answering "clean" for the rest of the session — stale in exactly the
// direction that leaks.
func TestTheVerdictIsRecomputedOnEveryRead(t *testing.T) {
	sb, home, _ := componentHome(t)
	p := writeConnector(t, home, "matrix", secrets.FieldPrefix+"sealed")
	register(t, home, "matrix", []string{"/bot_token"})

	if err := sb.CheckPathRead(p); err != nil {
		t.Fatalf("precondition: the sealed file must start readable: %v", err)
	}

	// The connector writes a plaintext token — no restart, no re-wiring.
	writeConnector(t, home, "matrix", "8888:AAAA-written-mid-session")

	if err := sb.CheckPathRead(p); err == nil {
		t.Fatal("the tree went dirty mid-session and the guard still allowed it — the verdict is a stale snapshot")
	}

	// And back: healing the file re-opens the tree, so the guard is not simply
	// latching to denied once it has ever said no.
	writeConnector(t, home, "matrix", secrets.FieldPrefix+"sealed-again")
	if err := sb.CheckPathRead(p); err != nil {
		t.Fatalf("a re-sealed file must be readable again: %v", err)
	}
}

// A guard may only ever ADD a denial. The component's own private key lives
// inside a tree that can be clean and readable, and it must stay denied there —
// otherwise sealing the token while handing back the key that opens it would be
// worse than not sealing it at all.
func TestAGuardCannotLiftAnAbsoluteDeny(t *testing.T) {
	sb, home, _ := componentHome(t)
	writeConnector(t, home, "matrix", secrets.FieldPrefix+"sealed")
	register(t, home, "matrix", []string{"/bot_token"})

	key := filepath.Join(home, "connectors", "matrix", secrets.KeyFileName)
	if err := os.WriteFile(key, []byte("AGE-SECRET-KEY-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Precondition: the tree really is readable, so a denial below is about the
	// key and not about the tree.
	if err := sb.CheckPathRead(filepath.Join(home, "connectors", "matrix", "config.json")); err != nil {
		t.Fatalf("precondition: the tree must be readable here: %v", err)
	}
	if err := sb.CheckPathRead(key); err == nil {
		t.Fatal("the component's private key must stay denied inside a clean tree")
	}
}

// bash is the route that matters: the file tools refusing a path the shell can
// `cat` buys nothing (see Sandbox.CheckCommand).
func TestBashCannotReadADeniedComponentTree(t *testing.T) {
	sb, home, _ := componentHome(t)
	writeConnector(t, home, "matrix", "8888:AAAA-a-real-bot-token")
	register(t, home, "matrix", []string{"/bot_token"})

	cmd := "cat " + filepath.Join(home, "connectors", "matrix", "config.json")
	if err := sb.CheckCommand(cmd); err == nil {
		t.Fatal("bash reached a denied component tree; the file-tool denial alone is a speed bump")
	}
}

// ext-data/ verdicts are computed and reported but NOT enforced this release:
// the manifest field is new, so every extension predating it would go dark at
// once, and that denial is absolute (/unjail does not lift it).
func TestExtensionDataIsReportedRatherThanDenied(t *testing.T) {
	sb, home, cwd := componentHome(t)
	// An INSTALLED extension whose manifest simply predates the field — the
	// case every existing install is in, and the one the grace period is for.
	extDir := filepath.Join(home, "extensions", "memory")
	if err := os.MkdirAll(extDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"),
		[]byte(`{"name":"memory","exec":"./memory"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "ext-data", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"notes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sb.CheckPathRead(state); err != nil {
		t.Fatalf("an undeclared extension's data dir stays readable this release: %v", err)
	}
	v := extensionDataVerdict(home, cwd, "memory")
	if v.Readable {
		t.Error("...but the VERDICT must say it is not clean, or the follow-up flip would be a surprise")
	}
	if v.Enforced {
		t.Error("the extension verdict must not be enforced while the declaration is pending")
	}
	if !strings.Contains(v.Reason, "data_secrets") {
		t.Errorf("the reason must name the field an author has to add, got: %q", v.Reason)
	}
}

// The declaration is a tri-state: absent, false, and true are three different
// answers, and a bool would have collapsed the first two — making the claim
// "there are none" on behalf of every extension that never considered it.
func TestDataSecretsDeclarationIsTriState(t *testing.T) {
	_, home, cwd := componentHome(t)
	extDir := filepath.Join(home, "extensions", "memory")
	if err := os.MkdirAll(extDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(extDir, "extension.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	manifest(`{"name":"memory","exec":"./memory"}`)
	if v := extensionDataVerdict(home, cwd, "memory"); v.Readable {
		t.Error("absent means undeclared, and unknown is not clean")
	}

	manifest(`{"name":"memory","exec":"./memory","data_secrets":true}`)
	v := extensionDataVerdict(home, cwd, "memory")
	if v.Readable {
		t.Error(`"data_secrets": true means the dir holds secrets; it must not be readable`)
	}
	if !strings.Contains(v.Reason, "secret_* frames") {
		t.Errorf("the reason should point at the broker, which is where they belong: %q", v.Reason)
	}

	manifest(`{"name":"memory","exec":"./memory","data_secrets":false}`)
	if v := extensionDataVerdict(home, cwd, "memory"); !v.Readable {
		t.Errorf(`"data_secrets": false is the author's claim that there are none: %q`, v.Reason)
	}
}

// The status report walks what is ON DISK, so the connector with a directory
// and no registry entry — the case that matters — cannot be missed by walking
// the registry instead.
func TestVerdictsWalkDiskNotTheRegistry(t *testing.T) {
	_, home, cwd := componentHome(t)
	writeConnector(t, home, "unregistered-one", "plaintext")
	register(t, home, "registered-one", []string{"/bot_token"})
	writeConnector(t, home, "registered-one", secrets.FieldPrefix+"sealed")

	var scopes []string
	for _, v := range ComponentReadVerdicts(home, cwd) {
		scopes = append(scopes, v.Scope)
	}
	want := map[string]bool{"conn:unregistered-one": true, "conn:registered-one": true}
	for _, s := range scopes {
		delete(want, s)
	}
	if len(want) > 0 {
		t.Errorf("verdicts missed %v; got %v", want, scopes)
	}
}
