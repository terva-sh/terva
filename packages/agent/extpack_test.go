package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cloneArgs adds --branch only when a ref is supplied; otherwise the
// shallow clone takes the remote's default HEAD.
func TestCloneArgs(t *testing.T) {
	got := cloneArgs("https://example.com/x.git", "/tmp/x", "")
	want := []string{"clone", "--depth", "1", "https://example.com/x.git", "/tmp/x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("no-ref: got %v, want %v", got, want)
	}

	got = cloneArgs("https://example.com/x.git", "/tmp/x", "v1.2.0")
	want = []string{"clone", "--depth", "1", "--branch", "v1.2.0", "https://example.com/x.git", "/tmp/x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ref: got %v, want %v", got, want)
	}
}

// The built-in core pack embed parses, validates, and is non-empty.
func TestResolvePackBuiltin(t *testing.T) {
	for _, arg := range []string{"core", "builtin", "builtin:core"} {
		p, label, err := resolvePack(arg)
		if err != nil {
			t.Fatalf("resolvePack(%q): %v", arg, err)
		}
		if p.Schema != packSchemaV1 {
			t.Errorf("%q: schema = %q", arg, p.Schema)
		}
		if len(p.Extensions) == 0 {
			t.Errorf("%q: core pack has no extensions", arg)
		}
		if label != "built-in core pack" {
			t.Errorf("%q: label = %q", arg, label)
		}
	}
}

func TestResolvePackUnknownBuiltin(t *testing.T) {
	if _, _, err := resolvePack("builtin:nope"); err == nil {
		t.Fatal("expected error for unknown built-in pack")
	}
}

// http:// is refused outright; only https is allowed for a hosted pack.
func TestResolvePackRejectsHTTP(t *testing.T) {
	_, _, err := resolvePack("http://example.com/pack.json")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected an https-required error, got %v", err)
	}
}

// A local .json file resolves and parses.
func TestResolvePackFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pack.json")
	body := `{"schema":"terva-extension-pack/v1","name":"demo","extensions":[{"source":"https://example.com/a.git"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, label, err := resolvePack(path)
	if err != nil {
		t.Fatalf("resolvePack(file): %v", err)
	}
	if p.Name != "demo" || len(p.Extensions) != 1 {
		t.Errorf("parsed pack = %+v", p)
	}
	if label != path {
		t.Errorf("label = %q, want %q", label, path)
	}
}

// A hosted manifest is fetched over https (httptest serves TLS).
func TestResolvePackURL(t *testing.T) {
	body := `{"schema":"terva-extension-pack/v1","name":"hosted","extensions":[{"source":"https://example.com/a.git"}]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// fetchPackURL uses its own client; point it at the test server's
	// client so the self-signed TLS cert is trusted.
	raw, err := fetchPackWith(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetchPackWith: %v", err)
	}
	if !strings.Contains(string(raw), "hosted") {
		t.Errorf("body = %q", raw)
	}
}

// An oversize body is refused rather than read unbounded.
func TestFetchPackURLSizeCap(t *testing.T) {
	big := strings.Repeat("x", maxPackManifestBytes+10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	if _, err := fetchPackWith(srv.Client(), srv.URL); err == nil {
		t.Fatal("expected an oversize error")
	}
}

func TestPackValidate(t *testing.T) {
	cases := []struct {
		name string
		p    Pack
		ok   bool
	}{
		{"good", Pack{Schema: packSchemaV1, Extensions: []PackEntry{{Source: "x"}}}, true},
		{"bad schema", Pack{Schema: "nope", Extensions: []PackEntry{{Source: "x"}}}, false},
		{"empty", Pack{Schema: packSchemaV1}, false},
		{"no source", Pack{Schema: packSchemaV1, Extensions: []PackEntry{{Name: "a"}}}, false},
	}
	for _, c := range cases {
		err := c.p.validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestEntryName(t *testing.T) {
	if got := (PackEntry{Name: "explicit"}).entryName(); got != "explicit" {
		t.Errorf("explicit name = %q", got)
	}
	if got := (PackEntry{Source: "https://example.com/terva-ext-index.git"}).entryName(); got != "terva-ext-index" {
		t.Errorf("derived name = %q", got)
	}
}

// The install loop lands a local-dir source under TERVA_HOME, and a
// second run skips the already-present extension rather than failing.
func TestPackInstallLoopLocalSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	// A local extension source the loop will copy in.
	src := filepath.Join(t.TempDir(), "demoext")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extension.json"), []byte(`{"name":"demoext"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	p := Pack{
		Schema:     packSchemaV1,
		Name:       "demo",
		Extensions: []PackEntry{{Name: "demoext", Source: src}},
	}

	if err := p.install(true); err != nil {
		t.Fatalf("first install: %v", err)
	}
	installed := filepath.Join(home, "extensions", "demoext", "extension.json")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("expected installed extension at %s: %v", installed, err)
	}

	// Second run: already present -> skipped, not an error.
	if err := p.install(true); err != nil {
		t.Fatalf("second install should skip cleanly, got %v", err)
	}
}

// installOne reports errExtAlreadyInstalled (not a generic error) when the
// destination exists, so the pack loop can distinguish a skip.
func TestInstallOneAlreadyInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	src := filepath.Join(t.TempDir(), "dup")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extension.json"), []byte(`{"name":"dup"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installOne(src, "", ""); err != nil {
		t.Fatalf("first installOne: %v", err)
	}
	_, err := installOne(src, "", "")
	if !errors.Is(err, errExtAlreadyInstalled) {
		t.Fatalf("second installOne err = %v, want errExtAlreadyInstalled", err)
	}
}
