package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A background model refresh outlives its caller by up to 20 seconds of HTTP
// work, and $TERVA_HOME is mutable while it runs (t.Setenv in every test that
// scratches a home). Resolving the cache path at WRITE time therefore let a
// refresh leaked by one test drop models-cache.json into whatever home was
// live when its HTTP finished — including a later test's TempDir in the middle
// of cleanup, which is the "TempDir RemoveAll: directory not empty" flake seen
// on TestApplyResumedModel and TestResolvePersonaIntroSurvivesToolMerge.
//
// This test pins the launch-time capture: the refresh is started under one
// home, held inside the endpoint's /v1/models call, the process moves to a
// second home (in the real flake this is just the next test starting), and
// only then does the server answer. The cache must land in the home that was
// live at LAUNCH, and the second home must stay untouched.
func TestModelRefreshWritesToLaunchHome(t *testing.T) {
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		arrivedOnce.Do(func() { close(arrived) })
		<-release
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"pinned-model"}]}`)
	}))
	defer srv.Close()

	// No ambient provider credentials: the configured endpoint must be the
	// refresh's only discovery source, or this test would hit real provider
	// APIs from whatever machine runs it.
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY",
		"KIMI_API_KEY", "MOONSHOT_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "OPENROUTER_API_KEY",
	} {
		t.Setenv(k, "")
	}

	launchHome := testsupport.TempDir(t)
	laterHome := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", launchHome)
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{"racer": {BaseURL: srv.URL + "/v1"}},
	}); err != nil {
		t.Fatal(err)
	}

	RefreshModelsForceAsync()

	// Only once the request has ARRIVED is it proven the goroutine read the
	// launch home's config; flipping the env earlier would just make the
	// refresh discover nothing.
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("the refresh never reached the endpoint; nothing raced")
	}
	t.Setenv("TERVA_HOME", laterHome)
	close(release)
	waitModelRefresh()

	if _, err := os.Stat(filepath.Join(launchHome, "models-cache.json")); err != nil {
		t.Errorf("cache did not land in the launch-time home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(laterHome, "models-cache.json")); err == nil {
		t.Error("cache landed in the home live at WRITE time — a leaked refresh can pollute a later test's TempDir")
	}
}
