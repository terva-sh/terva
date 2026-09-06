package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func submitKeyForm(t *testing.T, s *Server, formURL string, alter func(url.Values, *http.Request)) int {
	t.Helper()
	u, err := url.Parse(formURL)
	if err != nil {
		t.Fatal(err)
	}
	v := u.Query()
	v.Set("api_key", "synthetic-key")
	r, err := http.NewRequest(http.MethodPost, s.URL()+"/apikey", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", s.URL())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if alter != nil {
		alter(v, r)
	}
	r.Body = io.NopCloser(strings.NewReader(v.Encode()))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Error(err)
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func keyServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s
}

func beginKeyForm(t *testing.T, s *Server, flow FlowID, provider string) string {
	t.Helper()
	u, err := s.BeginAPIKey(flow, provider)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestKeyFormRejectsForgedRequests(t *testing.T) {
	s := keyServer(t)
	var probes atomic.Int32
	s.probeFn = func(context.Context, string, string) error { probes.Add(1); return nil }
	formURL := beginKeyForm(t, s, "test", "anthropic")
	for _, tc := range []struct {
		name  string
		alter func(url.Values, *http.Request)
	}{
		{"missing token", func(v url.Values, _ *http.Request) { v.Del("token") }},
		{"wrong token", func(v url.Values, _ *http.Request) { v.Set("token", "forged") }},
		{"wrong flow", func(v url.Values, _ *http.Request) { v.Set("flow", "other") }},
		{"wrong provider", func(v url.Values, _ *http.Request) { v.Set("provider", "openai") }},
		{"unknown provider", func(v url.Values, _ *http.Request) { v.Set("provider", "unknown") }},
		{"foreign origin", func(_ url.Values, r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }},
		{"null origin", func(_ url.Values, r *http.Request) { r.Header.Set("Origin", "null") }},
		{"missing origin", func(_ url.Values, r *http.Request) { r.Header.Del("Origin") }},
		{"forged host", func(_ url.Values, r *http.Request) { r.Host = "example.invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := submitKeyForm(t, s, formURL, tc.alter); status != http.StatusForbidden {
				t.Fatalf("status %d, want forbidden", status)
			}
		})
	}
	if probes.Load() != 0 {
		t.Fatal("a forged request reached the provider probe")
	}
	if status := submitKeyForm(t, s, formURL, nil); status != http.StatusOK {
		t.Fatalf("legitimate form failed after forged requests: %d", status)
	}
	if status := submitKeyForm(t, s, formURL, nil); status != http.StatusForbidden {
		t.Fatalf("replayed form status %d", status)
	}
	if probes.Load() != 1 {
		t.Fatalf("probe count %d, want one", probes.Load())
	}
}

func TestKeyFormCarriesTokenAndRejectsUnboundGET(t *testing.T) {
	s := keyServer(t)
	for _, provider := range []string{"anthropic", compatProvider} {
		u := beginKeyForm(t, s, FlowID(provider), provider)
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(u)
		for _, name := range []string{"flow", "token", "provider"} {
			want := `name="` + name + `" value="` + parsed.Query().Get(name) + `"`
			if !strings.Contains(string(body), want) {
				t.Errorf("%s form omitted %s", provider, name)
			}
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "strict-origin" || resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Error("form can leak its token through cache, referrer, or framing")
		}
	}
	resp, err := http.Get(s.URL() + "/apikey?provider=anthropic")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unbound GET status %d", resp.StatusCode)
	}
}

func TestKeyFormCanRetryARejectedProbe(t *testing.T) {
	s := keyServer(t)
	var calls atomic.Int32
	s.probeFn = func(context.Context, string, string) error {
		if calls.Add(1) == 1 {
			return errors.New("synthetic rejection")
		}
		return nil
	}
	u := beginKeyForm(t, s, "retry", "anthropic")
	if status := submitKeyForm(t, s, u, nil); status != http.StatusBadRequest {
		t.Fatalf("rejection status %d", status)
	}
	if status := submitKeyForm(t, s, u, nil); status != http.StatusOK {
		t.Fatalf("retry status %d", status)
	}
}

func TestKeyFormResultBackpressureDoesNotBlockShutdown(t *testing.T) {
	s := keyServer(t)
	s.probeFn = func(context.Context, string, string) error { return nil }
	for i := 0; i <= cap(s.results); i++ {
		u := beginKeyForm(t, s, FlowID(strconv.Itoa(i+1)), "anthropic")
		want := http.StatusOK
		if i == cap(s.results) {
			want = http.StatusServiceUnavailable
		}
		if status := submitKeyForm(t, s, u, nil); status != want {
			t.Fatalf("submit %d: got %d, want %d", i, status, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRejectsUnrelatedKeyResults(t *testing.T) {
	m := flowManager(t)
	t.Cleanup(m.Close)
	f, err := m.StartAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetAPIKey("anthropic", "original"); err != nil {
		t.Fatal(err)
	}
	s := m.keyServer
	for _, res := range []LoginResult{
		{Flow: f.ID, Provider: "anthropic", Method: "oauth", Code: "unrelated"},
		{Flow: f.ID, Provider: "openai", Method: "apikey", APIKey: "wrong-provider"},
		{Provider: "anthropic", Method: "apikey", APIKey: "missing-flow"},
		{Flow: f.ID, Provider: "anthropic", Method: "apikey"},
	} {
		m.consumeKeyResult(s, res)
	}
	c, err := m.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Anthropic.APIKey != "original" || c.OpenAI.APIKey != "" {
		t.Fatal("an unrelated result changed credentials")
	}
	m.CancelAPIKey(f.ID)
	m.consumeKeyResult(s, LoginResult{Flow: f.ID, Provider: "anthropic", Method: "apikey", APIKey: "canceled"})
	m.Close()
	m.consumeKeyResult(s, LoginResult{Flow: f.ID, Provider: "anthropic", Method: "apikey", APIKey: "closed"})
	c, err = m.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Anthropic.APIKey != "original" {
		t.Fatal("a canceled result changed credentials")
	}
}

func TestManagerKeyFormStoresWithItsFlow(t *testing.T) {
	m := flowManager(t)
	t.Cleanup(m.Close)
	m.probeAPIKey = func(context.Context, string, string) error { return nil }
	f, err := m.StartAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if status := submitKeyForm(t, m.keyServer, f.URL, nil); status != http.StatusOK {
		t.Fatalf("submit status %d", status)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-m.Events():
			if e.Kind != "success" {
				continue
			}
			if e.Flow != f.ID || e.Provider != f.Provider {
				t.Fatal("success belonged to the wrong flow")
			}
			c, err := m.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if c.Anthropic.APIKey != "synthetic-key" {
				t.Fatal("success did not store the key")
			}
			return
		case <-timer.C:
			t.Fatal("no success event")
		}
	}
}

func TestManagerCancelOrCloseDuringKeyProbe(t *testing.T) {
	for _, closeManager := range []bool{false, true} {
		t.Run(strconv.FormatBool(closeManager), func(t *testing.T) {
			m := flowManager(t)
			t.Cleanup(m.Close)
			started := make(chan struct{})
			m.probeAPIKey = func(ctx context.Context, _, _ string) error {
				close(started)
				<-ctx.Done()
				return nil // A late successful probe still cannot revive the flow.
			}
			f, err := m.StartAPIKey("anthropic")
			if err != nil {
				t.Fatal(err)
			}
			s := m.keyServer
			done := make(chan int, 1)
			go func() { done <- submitKeyForm(t, s, f.URL, nil) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("probe never started")
			}
			if status := submitKeyForm(t, s, f.URL, nil); status != http.StatusConflict {
				t.Fatalf("concurrent submit status %d", status)
			}
			if closeManager {
				m.Close()
			} else {
				m.CancelAPIKey(f.ID)
			}
			select {
			case status := <-done:
				if status != http.StatusForbidden {
					t.Fatalf("canceled submit status %d", status)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not release the request")
			}
			c, err := m.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if c.Has("anthropic") {
				t.Fatal("canceled probe stored a key")
			}
		})
	}
}

func TestDirectKeyCompletionInvalidatesBrowserForm(t *testing.T) {
	m := flowManager(t)
	t.Cleanup(m.Close)
	m.probeAPIKey = func(context.Context, string, string) error { return nil }
	f, err := m.StartAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAPIKey(context.Background(), "anthropic", "direct-key"); err != nil {
		t.Fatal(err)
	}
	if status := submitKeyForm(t, m.keyServer, f.URL, nil); status != http.StatusForbidden {
		t.Fatalf("old browser form status %d", status)
	}
	c, err := m.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Anthropic.APIKey != "direct-key" {
		t.Fatal("browser overwrote the direct submission")
	}
}

func TestKeyResultConsumerStopsOnShutdown(t *testing.T) {
	s := keyServer(t)
	m := flowManager(t)
	done := make(chan struct{})
	go func() { m.consumeKeyServerResults(s); close(done) }()
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("result consumer leaked after shutdown")
	}
}

func TestCompatKeyFormKeepsEndpointFieldsAndOptionalKey(t *testing.T) {
	s := keyServer(t)
	var probes atomic.Int32
	s.probeCompatFn = func(_ context.Context, baseURL, key string) error {
		if baseURL != "http://example.invalid/v1" || key != "" {
			return errors.New("unexpected endpoint fields")
		}
		probes.Add(1)
		return nil
	}
	u := beginKeyForm(t, s, "compat", compatProvider)
	status := submitKeyForm(t, s, u, func(v url.Values, _ *http.Request) {
		v.Set("api_key", "")
		v.Set("base_url", "http://example.invalid/v1")
		v.Set("model", "synthetic-model")
		v.Set("context_window", "32768")
	})
	if status != http.StatusOK || probes.Load() != 1 {
		t.Fatalf("status=%d probes=%d", status, probes.Load())
	}
	res := <-s.Result()
	if res.Flow != "compat" || res.Provider != compatProvider || res.BaseURL != "http://example.invalid/v1" || res.Model != "synthetic-model" || res.ContextWindow != 32768 || res.APIKey != "" {
		t.Fatal("compat form lost endpoint fields")
	}
}

func TestConcurrentKeyStartAndClose(t *testing.T) {
	m := flowManager(t)
	t.Cleanup(m.Close)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := m.StartAPIKey("anthropic"); err != nil {
				t.Error(err)
			}
		}()
		go func() { defer wg.Done(); m.Close() }()
	}
	wg.Wait()
}

func TestKeyServerRejectsOAuthCallbacks(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	for _, query := range []string{"code=synthetic&state=anthropic:unrelated", "error=access_denied&provider=openai"} {
		resp, err := http.Get(s.URL() + "/callback?" + query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("key server accepted an OAuth callback: status %d", resp.StatusCode)
		}
		select {
		case res := <-s.Result():
			t.Errorf("OAuth callback reached the key result channel: method=%s", res.Method)
		default:
		}
	}
}
