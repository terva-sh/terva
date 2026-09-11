package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// seedHome points TERVA_HOME at an empty directory and, when registries is
// non-nil, writes a config naming them.
//
// fetchPackURL reads the real user config, so without this a test of it would
// consult whatever the developer happens to have configured and its verdict
// would depend on whose machine ran it. LoadConfig re-reads config.json on
// every call and caches nothing, so per-test isolation is just the env var.
func seedHome(t *testing.T, registries []string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if registries == nil {
		return
	}
	b, err := json.Marshal(map[string]any{"pack_registries": registries})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// packSrv is a loopback server standing in for a registry on a private
// address. Loopback is the whole blocked class for this purpose: the guard
// refuses it by the same rule that refuses 10/8 and the metadata endpoint.
func packSrv(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":"terva-extension-pack/v1"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// registryHost takes the host and only the host, because the host is the part
// of a URL that decides what the guard may dial. It accepts a bare host so a
// user who omits the scheme gets what they meant rather than silence.
func TestRegistryHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://packs.internal.corp", "packs.internal.corp"},
		{"https://packs.internal.corp/team-a/pack.json", "packs.internal.corp"},
		{"http://192.168.1.50:8080", "192.168.1.50"},
		{"packs.internal.corp", "packs.internal.corp"},
		{"packs.internal.corp:8080", "packs.internal.corp"},
		{"  https://packs.internal.corp  ", "packs.internal.corp"},
		{"", ""},
		{"   ", ""},
		{"://nonsense", ""},
	} {
		if got := registryHost(tc.in); got != tc.want {
			t.Errorf("registryHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The pack fetch reaches a configured registry on a private address, and
// nothing else. The three cases together are the whole claim: naming a host
// opens that host, an empty list keeps today's closed default, and naming a
// different host opens nothing here.
func TestPackFetchGuardAllowsOnlyConfiguredRegistries(t *testing.T) {
	srv := packSrv(t)
	get := func(registries []string) error {
		_, err := fetchPackWith(packFetchGuard(registries).Client(5*time.Second, 0), srv.URL)
		return err
	}

	// Empty is the default, and it must behave exactly as it did before this
	// key existed.
	err := get(nil)
	if err == nil {
		t.Fatal("with no configured registry a loopback pack URL must stay refused")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("want an egress-blocked error, got: %v", err)
	}

	// Naming this registry is the trust decision, so the fetch succeeds.
	if err := get([]string{srv.URL}); err != nil {
		t.Errorf("a configured registry must be reachable: %v", err)
	}

	// Naming a different registry does not open this one. Without this case
	// the test above would pass on a guard that allowlisted everything as
	// soon as the list was non-empty.
	err = get([]string{"https://packs.example.test"})
	if err == nil {
		t.Fatal("configuring one registry must not open an unrelated private host")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("want an egress-blocked error, got: %v", err)
	}
}

// The wiring, not just the helper: fetchPackURL reads pack_registries from the
// user config. packFetchGuard could be perfect and still be handed nothing.
func TestFetchPackURLHonoursConfiguredRegistry(t *testing.T) {
	srv := packSrv(t)
	seedHome(t, []string{srv.URL})

	body, err := fetchPackURL(srv.URL)
	if err != nil {
		t.Fatalf("a registry named in user config must be reachable: %v", err)
	}
	if !strings.Contains(string(body), "terva-extension-pack/v1") {
		t.Errorf("fetched body does not look like the manifest: %q", body)
	}
}

// A registry the user did not name stays refused even when the config has
// other entries in it.
func TestFetchPackURLRefusesUnnamedRegistry(t *testing.T) {
	srv := packSrv(t)
	seedHome(t, []string{"https://packs.example.test"})

	_, err := fetchPackURL(srv.URL)
	if err == nil {
		t.Fatal("a loopback pack URL that no config names must be refused")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("want an egress-blocked error, got: %v", err)
	}
}
