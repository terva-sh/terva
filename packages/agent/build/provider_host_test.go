package build

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/cliversion"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// sawMessages is an endpoint that ends every stream at once, recording whether
// a request reached the Anthropic Messages path.
func sawMessages(t *testing.T) (url string, saw func() bool) {
	t.Helper()
	var mu sync.Mutex
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/messages") {
			mu.Lock()
			hit = true
			mu.Unlock()
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() bool { mu.Lock(); defer mu.Unlock(); return hit }
}

func streamOnce(c provider.Client) { streamModel(c, "claude-sonnet-4.5") }

func streamModel(c provider.Client, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.Stream(ctx, provider.Request{Model: model, Messages: []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
	}})
	if err != nil {
		return
	}
	for range ch {
	}
}

// Every client terva builds that speaks the Anthropic wire appends its request
// to TERVA_DEBUG_ANTHROPIC, as it did when the wire opened the file itself.
// The rows are found by what they send, not by a list, so a row added later is
// covered, and one that forgets hostOptions fails here. An operator-defined
// Anthropic endpoint takes the same path.
func TestEveryAnthropicWireClientWritesTheDebugDump(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	const ep = "dump-probe-endpoint"
	if err := RegisterOrReplaceEndpoint(ep, config.EndpointConfig{API: config.EndpointAPIAnthropic, BaseURL: "http://unused"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterEndpoint(ep) })
	spec, _ := specFor(ep)
	specs := append(append([]providerSpec(nil), providerSpecs...), *spec)

	covered := 0
	for _, s := range specs {
		for _, auth := range []string{"apikey", "oauth"} {
			dump := filepath.Join(testsupport.TempDir(t), "dump.jsonl")
			t.Setenv("TERVA_DEBUG_ANTHROPIC", dump)
			url, saw := sawMessages(t)
			streamOnce(s.newClient(clientConfig{Provider: s.id, Credential: "test-key", BaseURL: url, AuthMethod: auth}))
			if !saw() {
				continue
			}
			covered++
			b, err := os.ReadFile(dump)
			if err != nil || !strings.Contains(string(b), `"messages"`) || !strings.HasSuffix(string(b), "\n") {
				t.Errorf("%s (%s) sent an Anthropic request but dumped %q (err %v)", s.id, auth, b, err)
			}
		}
	}
	// anthropic x2, kimi x2, minimax, minimax-cn, fireworks, vercel, the
	// shared compatible slot, and the endpoint, each at least once.
	if covered < 10 {
		t.Errorf("only %d Anthropic-wire clients reached the endpoint; the probe is not finding them", covered)
	}
}

// With the variable unset, nothing is written and nothing breaks.
func TestTheDebugDumpIsOffByDefault(t *testing.T) {
	t.Setenv("TERVA_DEBUG_ANTHROPIC", "")
	dir := testsupport.TempDir(t)
	t.Chdir(dir)
	url, saw := sawMessages(t)
	streamOnce(provider.NewAnthropic("k", url, clientConfig{}.hostOptions()...))
	if !saw() {
		t.Fatal("the request never reached the endpoint")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("an unset TERVA_DEBUG_ANTHROPIC wrote %v", ents)
	}
}

// terva's own registry rows present the installed CLI versions cliversion
// finds, and the compiled baselines before it has found any. The Anthropic
// OAuth row is the one Anthropic version-gates; the Codex row asks only under
// native identity.
func TestTervasClientsClaimTheInstalledCLIVersions(t *testing.T) {
	isolate(t)
	// The codex row sends nothing without an OAuth token. A fake one, live so
	// no refresh is attempted.
	if err := config.AuthStoreFor().SetOAuth("openai", liveToken("fake-test-token")); err != nil {
		t.Fatal(err)
	}
	claude, codex := cliversion.Claude, cliversion.Codex
	t.Cleanup(func() { cliversion.Claude, cliversion.Codex = claude, codex })

	userAgent := func(id, model string, cfg clientConfig) string {
		var mu sync.Mutex
		var ua string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if ua == "" {
				ua = r.Header.Get("user-agent")
			}
			mu.Unlock()
			w.Header().Set("content-type", "text/event-stream")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}))
		defer srv.Close()
		spec, _ := specFor(id)
		cfg.Provider, cfg.BaseURL = id, srv.URL
		streamModel(spec.newClient(cfg), model)
		mu.Lock()
		defer mu.Unlock()
		return ua
	}
	oauth := clientConfig{AuthMethod: "oauth"}
	native := clientConfig{AuthMethod: "oauth", ClientIdentity: provider.CodexIdentityNative}

	cliversion.Claude = func() string { return "99.0.0" }
	cliversion.Codex = func() string { return "99.1.2" }
	if got := userAgent("anthropic", "claude-sonnet-4.5", oauth); got != "claude-cli/99.0.0" {
		t.Errorf("anthropic OAuth user-agent %q, want the installed 99.0.0", got)
	}
	if got := userAgent("openai-codex", "gpt-6-sol", native); got != "codex_cli_rs/99.1.2" {
		t.Errorf("native codex user-agent %q, want the installed 99.1.2", got)
	}

	cliversion.Claude = func() string { return "" }
	cliversion.Codex = func() string { return "" }
	if got := userAgent("anthropic", "claude-sonnet-4.5", oauth); !regexp.MustCompile(`^claude-cli/\d+\.\d+\.\d+$`).MatchString(got) || got == "claude-cli/99.0.0" {
		t.Errorf("anthropic OAuth user-agent %q, want the compiled baseline", got)
	}
	if got := userAgent("openai-codex", "gpt-6-sol", native); !regexp.MustCompile(`^codex_cli_rs/\d+\.\d+\.\d+$`).MatchString(got) || got == "codex_cli_rs/99.1.2" {
		t.Errorf("native codex user-agent %q, want the compiled baseline", got)
	}
}
