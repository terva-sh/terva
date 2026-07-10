package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestReadAgentsContextLoadsGlobalAndAncestors(t *testing.T) {
	root := testsupport.TempDir(t)
	tervaHome := filepath.Join(root, "terva-home")
	project := filepath.Join(root, "repo")
	nested := filepath.Join(project, "packages", "app")
	if err := os.MkdirAll(tervaHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tervaHome, "AGENTS.md"), []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("repo rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte("app rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readAgentsContext(nested, tervaHome)
	for _, want := range []string{"global rule", "repo rule", "app rule"} {
		if !strings.Contains(got, want) {
			t.Fatalf("readAgentsContext missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "global rule") > strings.Index(got, "repo rule") || strings.Index(got, "repo rule") > strings.Index(got, "app rule") {
		t.Fatalf("AGENTS.md files loaded in wrong order:\n%s", got)
	}
}

func TestReadAgentsContextMissingFilesIsEmpty(t *testing.T) {
	got := readAgentsContext(testsupport.TempDir(t), testsupport.TempDir(t))
	if got != "" {
		t.Fatalf("expected no context, got %q", got)
	}
}

func TestResolveInjectsContextFilesBeforeAgents(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "brief.md"), []byte("CONTEXT-BRIEF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("AGENT-RULE"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{CWD: dir, ContextFiles: []string{"brief.md"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	ic := strings.Index(r.SystemPrompt, "CONTEXT-BRIEF")
	ia := strings.Index(r.SystemPrompt, "AGENT-RULE")
	if ic < 0 {
		t.Fatalf("context file not injected:\n%s", r.SystemPrompt)
	}
	if ia < 0 {
		t.Fatalf("AGENTS.md not injected:\n%s", r.SystemPrompt)
	}
	if ic > ia {
		t.Fatalf("context file should precede AGENTS.md (ctx=%d agents=%d)", ic, ia)
	}
}

func TestResolveLoadsProjectConfigContextFiles(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "playbook.md"), []byte("PLAYBOOK-X"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProjectConfig(t, dir, `{"context_files":["playbook.md"]}`)

	// Project context_files are gated on Workspace Trust: trust the dir
	// (one-shot --trust) so the project layer applies.
	r, err := Resolve(Args{CWD: dir, Trust: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.SystemPrompt, "PLAYBOOK-X") {
		t.Fatalf("project-config context file not injected:\n%s", r.SystemPrompt)
	}
}

// Workspace Trust: an UNTRUSTED project's context_files are NOT injected
// (a cloned repo can't steer the system prompt); the safe core still
// resolves. The same dir trusted injects them (covered above).
func TestResolveUntrustedDropsProjectContextFiles(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "playbook.md"), []byte("PLAYBOOK-X"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProjectConfig(t, dir, `{"context_files":["playbook.md"]}`)

	// No --trust, not in the store ⇒ untrusted by default.
	r, err := Resolve(Args{CWD: dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.SystemPrompt, "PLAYBOOK-X") {
		t.Fatalf("untrusted project context file leaked into the system prompt:\n%s", r.SystemPrompt)
	}
	if r.Trusted { // sanity: the verdict is recorded as restricted
		t.Errorf("expected Resolved.Trusted=false for an untrusted dir")
	}
}

func TestResolveMissingContextFileErrors(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Args{CWD: testsupport.TempDir(t), ContextFiles: []string{"missing.md"}}, false)
	if err == nil {
		t.Fatal("expected Resolve to fail fast on a missing --context-file")
	}
}

// TestResolveFallsBackWhenConfiguredModelIsGone reproduces the
// startup failure caught by the user's screenshot: the persisted
// config.json points at a model id that's no longer in the active
// catalogue (because they edited models.json or terva's bundled
// catalogue changed). Resolve must NOT error — strands the user
// with no way to fix it from the TUI — and should repair the config
// so the next launch is silent.
func TestResolveFallsBackWhenConfiguredModelIsGone(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	// Persist a stale model id.
	stale := "gpt-5.5-pro-not-real"
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: stale}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve refused to launch with stale model: %v", err)
	}
	if r.Model == stale {
		t.Fatalf("Resolve kept stale model %q", r.Model)
	}
	if r.Provider != "openai" {
		t.Errorf("provider drifted: got %q; want openai", r.Provider)
	}

	// Config on disk should now hold the fallback so subsequent
	// launches don't repeat the warning.
	cfg, _ := config.LoadConfig()
	if cfg.Model == stale {
		t.Errorf("config.json still pins the stale model %q", cfg.Model)
	}
	if cfg.Model == "" {
		t.Errorf("config.json was emptied; expected the fallback model id")
	}
}

// TestResolveExplicitFlagStaleDoesNotRepairConfig confirms the
// repair-on-disk happens ONLY when the stale id came from the
// persisted config. If the user passed --model X explicitly and X is
// unknown, we still fall back, but we don't touch their config.
func TestResolveExplicitFlagStaleDoesNotRepairConfig(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	good := "gpt-5"
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: good}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{Model: "gpt-totally-fake"}, false)
	if err != nil {
		t.Fatalf("Resolve errored on unknown --model: %v", err)
	}
	if r.Model == "gpt-totally-fake" {
		t.Errorf("Resolve kept the bogus --model value")
	}
	cfg, _ := config.LoadConfig()
	if cfg.Model != good {
		t.Errorf("config.json was clobbered (was %q; now %q)", good, cfg.Model)
	}
}

// TestResolveEnvOnlyBedrockDiscoveredWithoutConfig reproduces issue
// #15: pointing TERVA_HOME at a fresh dir drops the persisted
// config.json (which pinned provider=amazon-bedrock). Resolve must
// still discover bedrock from the AWS env vars instead of falling back
// to anthropic and reporting "not logged in".
func TestResolveEnvOnlyBedrockDiscoveredWithoutConfig(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t)) // fresh home: no config.json
	// Disable the Kimi CLI token fallback so a developer machine with a
	// real Kimi CLI login doesn't pre-empt bedrock in the scan.
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-bedrock-token")
	t.Setenv("AWS_REGION", "us-east-1")
	// Make sure no other provider's env credential pre-empts bedrock.
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY"} {
		t.Setenv(k, "")
	}

	r, err := Resolve(Args{}, true)
	if err != nil {
		t.Fatalf("Resolve errored with env-only bedrock: %v", err)
	}
	if r.Provider != "amazon-bedrock" {
		t.Fatalf("provider = %q, want amazon-bedrock", r.Provider)
	}
	if !r.HasCredential() {
		t.Fatalf("bedrock credential not resolved from env")
	}
}

func TestResolveOllamaUsesModelBaseURLBeforeDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{{
		Provider:      "ollama",
		ID:            "qwen-local",
		DisplayName:   "Qwen Local",
		ContextWindow: 32768,
		MaxOutput:     8192,
		BaseURL:       "http://localhost:8000/v1",
	}})

	r, err := Resolve(Args{Provider: "ollama", Model: "qwen-local"}, false)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if r.BaseURL != "http://localhost:8000/v1" {
		t.Fatalf("BaseURL = %q, want models.json baseUrl", r.BaseURL)
	}
}

func TestResolveOllamaFallsBackToDefaultBaseURL(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	r, err := Resolve(Args{Provider: "ollama", Model: "any-local-model"}, false)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if r.BaseURL != "http://localhost:11434" {
		t.Fatalf("BaseURL = %q, want ollama default", r.BaseURL)
	}
}

// Temperature fall-through: --temperature flag > per-model (models.json) >
// global config; AdaptiveThinking models always omit it.
func TestResolveTemperaturePrecedence(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	temp := float32(0.4)
	provider.SetUserModels([]provider.Model{
		{Provider: "ollama", ID: "warm-local", MaxOutput: 8192, Temperature: &temp},
	})

	// No flag, no config: the per-model temperature applies.
	r, err := Resolve(Args{Provider: "ollama", Model: "warm-local"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Temperature == nil || *r.Temperature != float32(0.4) {
		t.Errorf("per-model temperature should apply, got %v", r.Temperature)
	}

	// The --temperature flag wins over the per-model value.
	flag := float32(0.9)
	r, err = Resolve(Args{Provider: "ollama", Model: "warm-local", Temperature: &flag}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Temperature == nil || *r.Temperature != float32(0.9) {
		t.Errorf("--temperature flag should win, got %v", r.Temperature)
	}
}

func TestResolveTemperatureOmittedForAdaptiveThinking(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	temp := float32(0.5)
	provider.SetUserModels([]provider.Model{
		{Provider: "ollama", ID: "adaptive-local", MaxOutput: 8192, Temperature: &temp, AdaptiveThinking: true},
	})
	// Even with a per-model temperature AND an explicit flag, an
	// adaptive-thinking model omits temperature (it would 400 the request).
	flag := float32(0.9)
	r, err := Resolve(Args{Provider: "ollama", Model: "adaptive-local", Temperature: &flag}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Temperature != nil {
		t.Errorf("adaptive-thinking model must omit temperature, got %v", *r.Temperature)
	}
}

func TestCanonicalProviderResolvesAliases(t *testing.T) {
	cases := map[string]string{
		"bedrock":         "amazon-bedrock",
		"AWS-Bedrock":     "amazon-bedrock",
		"  bedrock  ":     "amazon-bedrock",
		"vertex":          "google-vertex",
		"gemini":          "google",
		"azure":           "azure-openai-responses",
		"copilot":         "github-copilot",
		"codex":           "openai-codex",
		"moonshot":        "moonshotai",
		"vercel":          "vercel-ai-gateway",
		"hf":              "huggingface",
		"anthropic":       "anthropic",       // canonical passes through
		"amazon-bedrock":  "amazon-bedrock",  // already canonical
		"totally-unknown": "totally-unknown", // unknown returned unchanged (lowered)
		"Totally-UNKNOWN": "totally-unknown",
		"":                "",
	}
	for in, want := range cases {
		if got := canonicalProvider(in); got != want {
			t.Errorf("canonicalProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalProviderAliasesAreKnown(t *testing.T) {
	for alias, canon := range providerAliases {
		if !IsKnownProvider(canon) {
			t.Errorf("alias %q maps to %q which is not a known provider", alias, canon)
		}
	}
}
