package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// The verbs of `terva ext config`, driven against a stand-in store so the rules
// are asserted without a daemon or a config file. What the transports do with a
// submitted form is tested in build; what the CLI submits is tested here.

type fakeExtConfigStore struct {
	fields  []build.ConfigFormField
	saved   map[string]string
	cleared []string
	isLive  bool
}

func (f *fakeExtConfigStore) form(context.Context) ([]build.ConfigFormField, error) {
	return f.fields, nil
}

func (f *fakeExtConfigStore) save(_ context.Context, values map[string]string) error {
	f.saved = values
	return nil
}

func (f *fakeExtConfigStore) clear(_ context.Context, key string) error {
	f.cleared = append(f.cleared, key)
	return nil
}

func (f *fakeExtConfigStore) where() string { return "test" }
func (f *fakeExtConfigStore) live() bool    { return f.isLive }
func (f *fakeExtConfigStore) close()        {}

func sampleExtConfigFields() []build.ConfigFormField {
	return []build.ConfigFormField{
		{Key: "server", Type: "string", Saved: "imap.example.net"},
		{Key: "enable_sieve_tools", Type: "bool", Saved: "false", Default: "false"},
		{Key: "password", Type: "secret", Secret: true, HasSaved: true, Required: true},
		{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}, Default: "celsius"},
	}
}

func runVerb(t *testing.T, store extConfigStore, verb string, rest []string, o extConfigOpts) (string, string, error) {
	t.Helper()
	var out, msg bytes.Buffer
	err := runExtConfigVerb(context.Background(), store, extConfigIO{out: &out, msg: &msg}, "jmap-mail", verb, rest, o)
	return out.String(), msg.String(), err
}

func TestParseExtConfigArgs(t *testing.T) {
	// Flags after the verb and its operands, which is how anyone would type
	// this, and the reason the parser is hand-rolled: flag.FlagSet stops at the
	// first positional.
	pos, o, err := parseExtConfigArgs([]string{"jmap-mail", "set", "a=1", "--endpoint", "unix:/run/t.sock", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"jmap-mail", "set", "a=1"}; strings.Join(pos, " ") != strings.Join(want, " ") {
		t.Errorf("positionals = %v; want %v", pos, want)
	}
	if o.endpoint != "unix:/run/t.sock" || !o.endpointSet || !o.asJSON {
		t.Errorf("opts = %+v; want the endpoint and --json set", o)
	}

	// --flag=value is the same flag.
	_, o, err = parseExtConfigArgs([]string{"x", "--dir=/opt/ext/weather"})
	if err != nil || o.dir != "/opt/ext/weather" {
		t.Errorf("--dir=… = %q, %v", o.dir, err)
	}

	// The short aliases work, which they do not if only "--" introduces a flag.
	_, o, err = parseExtConfigArgs([]string{"x", "-e", "unix:/run/t.sock"})
	if err != nil || o.endpoint != "unix:/run/t.sock" {
		t.Errorf("-e = %q, %v", o.endpoint, err)
	}
	if _, _, err := parseExtConfigArgs([]string{"x", "-h"}); !errors.Is(err, errExtConfigHelp) {
		t.Errorf("-h should ask for the usage text, not fail: %v", err)
	}

	if _, _, err := parseExtConfigArgs([]string{"x", "--endpoint"}); err == nil {
		t.Error("a flag with no value should be an error, not an empty endpoint")
	}
	if _, _, err := parseExtConfigArgs([]string{"x", "--nope"}); err == nil {
		t.Error("an unknown flag should be an error")
	}
}

// The regression that matters most: `set` of ONE key submits the whole form, so
// the other saved values survive — and the secret goes back as blank, which the
// host reads as "keep what is stored" rather than "clear it".
func TestExtConfigSetSubmitsTheWholeFormAndNeverClearsTheSecret(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields(), isLive: true}
	if _, _, err := runVerb(t, store, "set", []string{"enable_sieve_tools=true"}, extConfigOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := store.saved["enable_sieve_tools"]; got != "true" {
		t.Errorf("enable_sieve_tools = %q; want \"true\"", got)
	}
	if got := store.saved["server"]; got != "imap.example.net" {
		t.Errorf("server = %q; a set of another key must not disturb it", got)
	}
	if got, ok := store.saved["password"]; !ok || got != "" {
		t.Errorf("password = %q (present=%v); a secret goes back blank, which means keep", got, ok)
	}
}

func TestExtConfigSetRefusesASecretOnTheCommandLine(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields()}
	_, _, err := runVerb(t, store, "set", []string{"password=hunter2"}, extConfigOpts{})
	if err == nil {
		t.Fatal("a secret on argv should be refused")
	}
	if !strings.Contains(err.Error(), "--stdin") {
		t.Errorf("the refusal should name the way in, got: %v", err)
	}
	if store.saved != nil {
		t.Error("nothing should have been submitted")
	}
}

func TestExtConfigSetFromFileTakesASecretAndTrimsOneNewline(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeExtConfigStore{fields: sampleExtConfigFields(), isLive: true}
	out, msg, err := runVerb(t, store, "set", []string{"password"}, extConfigOpts{fromFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.saved["password"]; got != "hunter2" {
		t.Errorf("password = %q; want the trailing newline dropped", got)
	}
	if strings.Contains(out+msg, "hunter2") {
		t.Errorf("the secret was echoed back: out=%q msg=%q", out, msg)
	}
}

func TestExtConfigSetRejectsAValueTheSchemaForbids(t *testing.T) {
	for _, tc := range []struct{ name, arg string }{
		{"bool", "enable_sieve_tools=soon"},
		{"select", "units=kelvin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeExtConfigStore{fields: sampleExtConfigFields()}
			if _, _, err := runVerb(t, store, "set", []string{tc.arg}, extConfigOpts{}); err == nil {
				t.Fatalf("%q should be refused by the schema", tc.arg)
			}
			if store.saved != nil {
				t.Error("a rejected value must not submit a form")
			}
		})
	}
}

func TestExtConfigSetNamesTheKeysWhenOneIsUnknown(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields()}
	_, _, err := runVerb(t, store, "set", []string{"enabl_sieve_tools=true"}, extConfigOpts{})
	if err == nil {
		t.Fatal("an unknown key should be refused")
	}
	if !strings.Contains(err.Error(), "enable_sieve_tools") {
		t.Errorf("the error should list the real keys, got: %v", err)
	}
}

func TestExtConfigGetPrintsTheEffectiveValueAndNeverASecret(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields()}

	out, _, err := runVerb(t, store, "get", []string{"server"}, extConfigOpts{})
	if err != nil || out != "imap.example.net\n" {
		t.Errorf("get server = %q, %v; want the saved value alone on stdout", out, err)
	}
	// Unset falls back to the declared default: what the extension will see.
	out, _, err = runVerb(t, store, "get", []string{"units"}, extConfigOpts{})
	if err != nil || out != "celsius\n" {
		t.Errorf("get units = %q, %v; want the declared default", out, err)
	}
	out, _, err = runVerb(t, store, "get", []string{"password"}, extConfigOpts{})
	if err == nil {
		t.Fatal("get of a secret should be refused")
	}
	if out != "" {
		t.Errorf("get of a secret wrote %q to stdout", out)
	}
}

func TestExtConfigShowMasksSecretsInBothRenderings(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields()}

	out, msg, err := runVerb(t, store, "show", nil, extConfigOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(set)") {
		t.Errorf("the table should report that a secret exists:\n%s", out)
	}
	if !strings.Contains(msg, "jmap-mail") || !strings.Contains(msg, "test") {
		t.Errorf("the header should name the extension and the transport, got %q", msg)
	}

	jsonOut, _, err := runVerb(t, store, "show", nil, extConfigOpts{asJSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut, `"key": "password"`) {
		t.Errorf("json should still list the secret field:\n%s", jsonOut)
	}
	if strings.Contains(jsonOut, `"value"`) && strings.Contains(jsonOut, "password") {
		// value may legitimately appear for other fields; assert the secret's
		// own row carries set=true and no value of its own.
		for _, chunk := range strings.Split(jsonOut, "{") {
			if strings.Contains(chunk, `"key": "password"`) && strings.Contains(chunk, `"value"`) {
				t.Errorf("the secret row carried a value:\n%s", chunk)
			}
		}
	}
}

// A typo in the third key should not leave the first two cleared.
func TestExtConfigUnsetValidatesEveryKeyBeforeClearingAny(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields(), isLive: true}
	if _, _, err := runVerb(t, store, "unset", []string{"server", "units", "nope"}, extConfigOpts{}); err == nil {
		t.Fatal("an unknown key should be refused")
	}
	if len(store.cleared) != 0 {
		t.Errorf("cleared %v before validating them all", store.cleared)
	}
	if _, msg, err := runVerb(t, store, "unset", []string{"password"}, extConfigOpts{}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(msg, "required") {
		// Clearing a required field with no default is legal and worth saying.
		t.Errorf("clearing a required field should warn, got %q", msg)
	}
	if len(store.cleared) != 1 || store.cleared[0] != "password" {
		t.Errorf("cleared = %v; want the secret cleared through the delete path", store.cleared)
	}
}

// A write that reached no running instance says so — the change is on disk and
// the running extension still holds the old value, which is the exact failure
// this command exists to end.
func TestExtConfigReportsWhenNothingLiveWasReached(t *testing.T) {
	store := &fakeExtConfigStore{fields: sampleExtConfigFields(), isLive: false}
	_, msg, err := runVerb(t, store, "set", []string{"server=imap.other.net"}, extConfigOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "nothing serving this home was contacted") {
		t.Errorf("a local write should say what it did not do, got %q", msg)
	}

	live := &fakeExtConfigStore{fields: sampleExtConfigFields(), isLive: true}
	_, msg, err = runVerb(t, live, "set", []string{"server=imap.other.net"}, extConfigOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "nothing serving this home") {
		t.Errorf("a write through a daemon should not carry the warning, got %q", msg)
	}
}

func TestExtConfigOfflineAndEndpointAreContradictory(t *testing.T) {
	_, err := openExtConfigStore(context.Background(), "x", extConfigOpts{offline: true, endpointSet: true, endpoint: "unix:/run/t.sock"}, "test")
	if err == nil {
		t.Fatal("--offline with --endpoint should be refused rather than silently picking one")
	}
}

// An explicitly named daemon that does not answer is an error, never a quiet
// fall back to the file: naming one asserts that a terva is running, and writing
// behind it is what this command exists to avoid.
func TestExtConfigNamedEndpointThatIsDownDoesNotFallBackToDisk(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	dead := filepath.Join(testsupport.TempDir(t), "nothing.sock")
	store, err := openExtConfigStore(context.Background(), "weather", extConfigOpts{
		endpoint: "unix:" + dead, endpointSet: true, dialTimeout: 200 * time.Millisecond,
	}, "test")
	if err == nil {
		store.close()
		t.Fatal("a dead endpoint should fail, not fall back to the config file")
	}
	if !strings.Contains(err.Error(), dead) {
		t.Errorf("the error should name the endpoint, got: %v", err)
	}
}

// Discovery: with no --endpoint and no TERVA_ATTACH, the daemon record this
// home's `terva web` publishes is what points the command at a running instance.
// Safe where a port probe was not, because the record lives INSIDE the home —
// a daemon named there is serving this home by construction.
func TestExtConfigDiscoversTheDaemonFromTheHomesRecord(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dead := filepath.Join(testsupport.TempDir(t), "nothing.sock")
	stop, err := config.PublishListenRecord(config.ListenRecord{Endpoint: "unix:" + dead})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// The record is chosen, so the dead socket surfaces as a dial failure
	// naming it — rather than a silent fall back to writing the file.
	store, err := openExtConfigStore(context.Background(), "weather", extConfigOpts{
		dialTimeout: 200 * time.Millisecond,
	}, "test")
	if err == nil {
		store.close()
		t.Fatal("the discovered endpoint should have been dialled")
	}
	if !strings.Contains(err.Error(), dead) {
		t.Errorf("error should name the discovered endpoint, got: %v", err)
	}
}

func TestExtConfigOfflineIgnoresTheRecord(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	stop, err := config.PublishListenRecord(config.ListenRecord{Endpoint: "unix:/nope/nothing.sock"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// --offline means the file, and the extension has to be findable for that.
	writeTestExtManifest(t, filepath.Join(home, "extensions", "weather"), "weather")
	store, err := openExtConfigStore(context.Background(), "weather", extConfigOpts{offline: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if store.live() {
		t.Error("--offline must not reach a daemon")
	}
}

// A discovered daemon that needs a token says so before the dial: an opaque
// handshake failure does not distinguish "wrong endpoint" from "no credential".
func TestExtConfigNamesTheMissingTokenForADiscoveredDaemon(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_WEB_TOKEN", "")
	stop, err := config.PublishListenRecord(config.ListenRecord{
		Endpoint: "ws://127.0.0.1:65535/ws", Auth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := openExtConfigStore(context.Background(), "weather", extConfigOpts{
		dialTimeout: 200 * time.Millisecond,
	}, "test"); err == nil {
		t.Fatal("want a refusal naming the missing token")
	} else if !strings.Contains(err.Error(), "TERVA_WEB_TOKEN") {
		t.Errorf("error should name how to supply a token, got: %v", err)
	}
}

// writeTestExtManifest drops a minimal installed extension so the local store
// can resolve one by name.
func writeTestExtManifest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"` + name + `","exec":"./run.sh","enabled":true,"config":[{"key":"units","type":"string"}]}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
