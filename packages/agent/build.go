package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tervadocs "terva.sh/terva"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Resolved is the effective configuration after merging CLI, config, defaults.
type Resolved struct {
	Provider   string
	Model      string
	Credential string // api key or oauth access token
	AuthMethod string // "apikey" | "oauth" | "" (no credential yet)
	AccountID  string // ChatGPT account id (for openai oauth), "" otherwise
	BaseURL    string
	CWD        string
	Reasoning  string

	ToolRegistry core.Registry
	ToolSummary  []ToolSummary
	SystemPrompt string
	MaxSteps     int
	Sandbox      *tools.Sandbox

	// MaxOutput is the resolved model's maximum output-token budget
	// (from the catalog). Passed to the agent so each turn requests
	// the model's full output capacity instead of the provider's
	// conservative default (e.g. Bedrock's 4096, which truncates
	// long writes/edits with stopReason=length).
	MaxOutput int

	// SkillTool is the on-demand skill loader registered with the
	// agent's tool registry, or nil if no SKILL.md files were
	// discovered. Exposed so the tui can list / preview skills.
	SkillTool *skills.Tool

	// Bookkeeping for MergeExtensionTools. Captured at Resolve time
	// so the system prompt can be rebuilt later without re-running
	// resolve.
	systemAppend     []string
	systemCustom     string
	toolDescriptions map[string]string
}

// HasCredential reports whether a credential was resolved.
func (r Resolved) HasCredential() bool { return r.Credential != "" }

// MergeExtensionTools folds every tool registered by an extension
// into r's ToolRegistry and re-renders the system prompt's tool
// summary so the model sees both built-in and extension tools.
//
// Idempotent: calling twice with the same manager state has no
// effect on the second pass (existing names are preserved). Built-in
// tools always win on conflict.
func (r *Resolved) MergeExtensionTools(mgr ExtensionToolSource) {
	if mgr == nil {
		return
	}
	infos := mgr.Tools()
	if len(infos) == 0 {
		return
	}
	changed := false
	for _, info := range infos {
		if _, exists := r.ToolRegistry[info.Name]; exists {
			continue
		}
		r.ToolRegistry[info.Name] = mgr.NewExtensionTool(info)
		changed = true
	}
	if !changed {
		return
	}
	// Re-render the system prompt with the merged tool list. Skill
	// addendum is preserved by walking the existing append slice.
	append_ := r.systemAppend
	r.SystemPrompt = BuildSystemPrompt(SystemPromptOpts{
		CWD:          r.CWD,
		Tools:        toolSummariesFromRegistry(r.ToolRegistry, r.toolDescriptions),
		Custom:       r.systemCustom,
		Append:       append_,
		TervaDocsDir: filepath.Join(TervaHome(), "docs"),
		StatusTool:   r.ToolRegistry["terva_status"] != nil,
	})
}

// ExtensionToolSource is the slice of the extension manager that
// MergeExtensionTools needs. Lives here as an interface so the
// build package doesn't import packages/agent/extensions (which
// imports core, which imports... avoid the cycle).
type ExtensionToolSource interface {
	Tools() []ExtensionToolInfo
	NewExtensionTool(info ExtensionToolInfo) core.Tool
}

// ExtensionToolInfo mirrors extensions.ToolInfo so we can declare
// ExtensionToolSource here without importing the extensions
// package. The cli wires a tiny adapter to bridge them.
type ExtensionToolInfo struct {
	Extension   string
	Name        string
	Description string
	Schema      []byte
}

// toolSummariesFromRegistry rebuilds the system-prompt tool list
// from a (possibly extended) registry, using cached descriptions for
// the human-readable summary text.
func toolSummariesFromRegistry(reg core.Registry, cached map[string]string) []ToolSummary {
	out := make([]ToolSummary, 0, len(reg))
	for name, t := range reg {
		desc := t.Description()
		if d, ok := cached[name]; ok && d != "" {
			desc = d
		}
		out = append(out, ToolSummary{Name: name, Description: desc})
	}
	return out
}

// defaultModelForProvider returns the model id terva prefers when the
// caller didn't pick one. Mirrors the per-provider switch used at
// multiple points in Resolve; centralised so the unknown-model
// recovery path and the no-model-configured path can't drift.
//
// Returns the empty string for "ollama", which has no built-in
// default — the caller is expected to special-case ollama and
// error or use whatever the user passed.
func defaultModelForProvider(prov string) string {
	switch prov {
	case "openai":
		return "gpt-5"
	case "openai-codex":
		return "gpt-5.5"
	case "openai-responses":
		return "gpt-5"
	case "kimi":
		return "kimi-for-coding"
	case "deepseek":
		return "deepseek-v4-pro"
	case "google":
		return "gemini-2.5-pro"
	case "ollama", "openai-compatible":
		return ""
	case "moonshotai", "moonshotai-cn":
		return "kimi-k2.6"
	case "cerebras":
		return "qwen-3-235b-a22b-instruct-2507"
	case "groq":
		return "llama-3.3-70b-versatile"
	case "xai":
		return "grok-code-fast-1"
	case "together":
		return "Qwen/Qwen3-Coder-480B-A35B-Instruct"
	case "huggingface":
		return "moonshotai/Kimi-K2-Instruct"
	case "openrouter":
		return "anthropic/claude-sonnet-4.5"
	case "mistral":
		return "mistral-large-latest"
	case "zai":
		return "glm-4.7"
	case "xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp":
		return "mimo-v2.5"
	case "minimax", "minimax-cn":
		return "MiniMax-M2.7"
	case "fireworks":
		return "accounts/fireworks/models/kimi-k2p6"
	case "vercel-ai-gateway":
		return "anthropic/claude-sonnet-4.5"
	case "opencode":
		return "claude-sonnet-4-5"
	case "opencode-go":
		return "kimi-k2.6"
	case "amazon-bedrock":
		return "anthropic.claude-sonnet-4-5-20250929-v1:0"
	case "google-vertex":
		return "gemini-2.5-pro"
	case "azure-openai-responses":
		return "gpt-5"
	case "github-copilot":
		return "claude-sonnet-4.5"
	default:
		return provider.DefaultModel.ID
	}
}

// knownProviders is the set of provider ids terva recognises. Used by
// Resolve to validate args.Provider, by extension-callers, and by the
// auto-fallback logic that picks any logged-in provider when the user's
// preferred one has no credentials.
var knownProviders = []string{
	"anthropic", "openai", "openai-codex", "openai-responses", "kimi", "deepseek", "google", "ollama",
	"moonshotai", "moonshotai-cn",
	"cerebras", "groq", "xai", "together", "huggingface", "openrouter",
	"mistral", "zai",
	"xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp",
	"minimax", "minimax-cn",
	"fireworks", "vercel-ai-gateway",
	"opencode", "opencode-go",
	"amazon-bedrock", "google-vertex", "azure-openai-responses",
	"github-copilot", "cloudflare-workers-ai", "cloudflare-ai-gateway",
	"openai-compatible",
}

func isKnownProvider(name string) bool {
	for _, p := range knownProviders {
		if p == name {
			return true
		}
	}
	return false
}

// providerAliases maps common short / alternate provider names to the
// canonical id in knownProviders. Users (and other agents) reach for
// "bedrock" or "vertex" far more naturally than the fully-qualified
// "amazon-bedrock" / "google-vertex"; without this mapping an alias is
// treated as an unknown provider and Resolve silently falls back to
// anthropic, producing a misleading "no credential for anthropic" error
// after the user explicitly picked, say, bedrock.
var providerAliases = map[string]string{
	"bedrock":      "amazon-bedrock",
	"aws-bedrock":  "amazon-bedrock",
	"amazon":       "amazon-bedrock",
	"vertex":       "google-vertex",
	"gcp-vertex":   "google-vertex",
	"gemini":       "google",
	"googleai":     "google",
	"google-ai":    "google",
	"azure":        "azure-openai-responses",
	"azure-openai": "azure-openai-responses",
	"copilot":      "github-copilot",
	"github":       "github-copilot",
	"codex":        "openai-codex",
	"moonshot":     "moonshotai",
	"kimi-code":    "kimi",
	"ai-gateway":   "vercel-ai-gateway",
	"vercel":       "vercel-ai-gateway",
	"cloudflare":   "cloudflare-workers-ai",
	"workers-ai":   "cloudflare-workers-ai",
	"hf":           "huggingface",
}

// canonicalProvider normalises a user-supplied provider name: trims
// surrounding whitespace, lower-cases it, and resolves any known alias
// to its canonical id. Unknown names are returned trimmed/lower-cased
// and unchanged so the existing unknown-provider handling still runs.
func canonicalProvider(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return n
	}
	if canon, ok := providerAliases[n]; ok {
		return canon
	}
	return n
}

// Resolve merges args, config, and env into a Resolved set.
//
// Unlike the earlier version, Resolve NEVER returns an error for
// missing credentials: the TUI can start without them and launch a
// login flow. requireCred controls whether missing credentials are a
// hard error (used by print/json modes).
func Resolve(args Args, requireCred bool) (Resolved, error) {
	// eff is the layered (project-over-user) read view; cfg is the pristine,
	// writable user layer. Every existing read/repair below stays on cfg so
	// behaviour is unchanged and the model-repair paths only ever persist the
	// user config. eff.ContextFiles (project-or-user, resolved absolute) feeds
	// startup context-file injection.
	eff := ResolveConfig(args.CWD)
	cfg := eff.User

	// User-requested provider (explicit > config > default).
	// Normalise common aliases (e.g. "bedrock" -> "amazon-bedrock")
	// before validation so an alias is never mistaken for an unknown
	// provider and silently downgraded to anthropic.
	argProvider := canonicalProvider(args.Provider)
	provName := firstNonEmpty(argProvider, canonicalProvider(cfg.Provider), "anthropic")
	if !isKnownProvider(provName) {
		// Unknown provider (maybe removed or renamed). Fall back to
		// the first provider that has credentials, or anthropic.
		provName = "anthropic"
		if _, _, _, err := ResolveCredentialFull("openai", ""); err == nil {
			provName = "openai"
		}
		if _, _, _, err := ResolveCredentialFull("openai-codex", ""); err == nil {
			provName = "openai-codex"
		}
		if _, _, _, err := ResolveCredentialFull("kimi", ""); err == nil {
			provName = "kimi"
		}
		if _, _, _, err := ResolveCredentialFull("deepseek", ""); err == nil {
			provName = "deepseek"
		}
		if _, _, _, err := ResolveCredentialFull("anthropic", ""); err == nil {
			provName = "anthropic"
		}
		// Reset the saved config so this doesn't keep happening.
		cfg.Provider = provName
		cfg.Model = ""
		_ = SaveConfig(cfg)
	}

	var (
		cred      string
		method    string
		accountID string
		credErr   error
		// compatCtx is the user's default context window for an
		// openai-compatible endpoint, applied to a model id that isn't in
		// the active catalogue (not yet discovered). 0 = unknown.
		compatCtx int
		// compatBaseURL is the openai-compatible endpoint captured at
		// /login. It is the FALLBACK base URL — applied only after an
		// explicit --base-url flag and any per-model models.json baseUrl,
		// so a model can point at a different endpoint than the login one.
		compatBaseURL string
	)
	if provName == "ollama" {
		cred = firstNonEmpty(args.APIKey, "ollama")
		method = "apikey"
	} else if provName == "openai-compatible" {
		// A user-configured OpenAI-compatible endpoint (local model
		// server, gateway, ...). The base URL and model id were captured
		// in the login form and live in auth.json; the API key is
		// optional, so fall back to a harmless sentinel bearer token when
		// the server doesn't need one. Seed --base-url / --model from the
		// stored values when the caller didn't pass them explicitly.
		storedBaseURL, storedModel, storedCtx := AuthStoreFor().Extras(provName)
		compatCtx = storedCtx
		// Remember the login endpoint as a fallback, but DON'T assign it to
		// args.BaseURL yet: a per-model `baseUrl` in models.json must be able
		// to override it (applied after the model is resolved, below).
		compatBaseURL = storedBaseURL
		if args.Model == "" && cfg.Model == "" {
			args.Model = storedModel
		}
		storedKey, _, _, _ := ResolveCredentialFull(provName, args.APIKey)
		cred = firstNonEmpty(storedKey, "openai-compatible")
		method = "apikey"
	} else {
		cred, method, accountID, credErr = ResolveCredentialFull(provName, args.APIKey)
	}

	// If the user did NOT explicitly pick a provider and the default one
	// has no credentials, auto-fall-back to whichever provider is actually
	// logged in. That way running plain `terva` after `/login` (any provider)
	// never shows a "not logged in" banner.
	userPickedProvider := args.Provider != ""
	if credErr != nil && !userPickedProvider && provName != "ollama" {
		// Scan every known provider (not a hardcoded subset) so any
		// env-based credential is discovered, e.g. an env-only
		// amazon-bedrock setup (AWS_BEARER_TOKEN_BEDROCK / AWS_PROFILE /
		// IAM keys) when no config.json pins the provider, such as after
		// pointing TERVA_HOME at a fresh home dir. Iteration order of
		// knownProviders defines fallback priority. ollama is skipped:
		// it has no credential and would always "match".
		for _, other := range knownProviders {
			if other == provName || other == "ollama" || other == "openai-compatible" {
				continue
			}
			if c, m, a, err := ResolveCredentialFull(other, args.APIKey); err == nil {
				provName = other
				cred, method, accountID, credErr = c, m, a, err
				break
			}
		}
	}

	// ollama and openai-compatible are open-catalogue: the model id is
	// whatever the local/custom server understands and has no baked-in
	// catalog entry or default.
	openCatalogue := provName == "ollama" || provName == "openai-compatible"
	model := firstNonEmpty(args.Model, cfg.Model)
	if model == "" {
		switch provName {
		case "ollama":
			return Resolved{}, fmt.Errorf("ollama requires --model (e.g. --model llama3)")
		case "openai-compatible":
			return Resolved{}, fmt.Errorf("openai-compatible requires a model; set it during /login or pass --model")
		default:
			model = defaultModelForProvider(provName)
		}
	}
	// If the resolved model belongs to a different provider (e.g. config
	// says gpt-5 but we fell back to anthropic), pick that provider's default.
	if !openCatalogue {
		if m, err := provider.FindModel("", model); err == nil && m.Provider != provName {
			model = defaultModelForProvider(provName)
		}
	}
	resolvedModel, err := provider.FindModel(provName, model)
	if err != nil && openCatalogue {
		// Any model id the local/custom server understands is valid,
		// even if not in the baked-in catalog. For openai-compatible,
		// prefer the user's configured default context window over the
		// generic guess so auto-compaction and the context % are sane.
		ctxWin := 32768
		if provName == "openai-compatible" && compatCtx > 0 {
			ctxWin = compatCtx
		}
		resolvedModel = provider.Model{
			Provider:      provName,
			ID:            model,
			DisplayName:   model,
			ContextWindow: ctxWin,
			MaxOutput:     8192,
			BaseURL:       args.BaseURL,
			Source:        provName,
		}
		// Register it: every context-window consumer (auto-compaction,
		// the status-bar gauge, /status, chat status) looks the model
		// up in the active catalog by id, so an unregistered synthesized
		// model left them all seeing 0 — auto-compaction never fired for
		// open-catalogue models. The extra layer is upserted by compat
		// discovery and outranked by models.json, so better data still
		// wins when it exists.
		provider.RegisterExtraModel(resolvedModel)
		err = nil
	}
	if err != nil {
		// The model the user (or persisted config) asked for is no
		// longer in the active catalogue — they probably removed it
		// from their models.json or upgraded terva and the id changed.
		// Refusing to launch is the wrong move: it strands the user
		// with no way to even open the TUI and pick a new model.
		// Fall back to the provider's default, warn on stderr, and,
		// when the stale id came from the persisted config (not an
		// explicit --model flag), repair the config so the warning
		// doesn't repeat on every launch.
		fallback := defaultModelForProvider(provName)
		fm, ferr := provider.FindModel(provName, fallback)
		if ferr != nil {
			// Even the provider default is gone (catastrophic
			// catalogue trim). Last resort: any model on this
			// provider, then the global DefaultModel.
			if candidates := provider.ModelsForProvider(provName); len(candidates) > 0 {
				fm = candidates[0]
				ferr = nil
			} else {
				fm = provider.DefaultModel
				ferr = nil
			}
		}
		fmt.Fprintf(os.Stderr,
			"terva: model %q is not in the active catalogue; using %q instead. Pick a different model with --model or /model.\n",
			model, fm.ID)
		if args.Model == "" && cfg.Model == model {
			cfg.Model = fm.ID
			_ = SaveConfig(cfg)
		}
		resolvedModel = fm
		model = fm.ID
	}

	// Base-URL precedence (highest first):
	//   1. explicit --base-url flag (args.BaseURL is only the flag here)
	//   2. per-model baseUrl from models.json (resolvedModel.BaseURL)
	//   3. the openai-compatible endpoint captured at /login (compatBaseURL)
	//   4. provider default (ollama localhost)
	// This lets a models.json model point at a different endpoint than the
	// one stored at login — e.g. one model on a gateway, another local.
	if args.BaseURL == "" && resolvedModel.BaseURL != "" {
		args.BaseURL = resolvedModel.BaseURL
	}
	if args.BaseURL == "" && compatBaseURL != "" {
		args.BaseURL = compatBaseURL
	}
	if args.BaseURL == "" && provName == "ollama" {
		args.BaseURL = "http://localhost:11434"
	}
	if provName == "openai-compatible" && args.BaseURL == "" {
		return Resolved{}, fmt.Errorf("openai-compatible requires a base url; set it during /login or pass --base-url")
	}

	// If the model has a base URL, credentials are optional (local
	// models like ollama don't need real API keys).
	if resolvedModel.BaseURL != "" && credErr != nil {
		cred = "ollama"
		credErr = nil
		requireCred = false
	}

	if credErr != nil && requireCred {
		return Resolved{}, fmt.Errorf("%w; set %s_API_KEY, pass --api-key, or run `terva` and /login",
			credErr, envVarName(provName))
	}

	sandbox := tools.NewSandbox(args.CWD)
	reg := buildToolRegistry(args, args.CWD, sandbox, provName, method)

	docsDir, _ := tervadocs.EnsureInstalled(TervaHome())

	// Skill discovery: scan project + global locations + built-in
	// skills shipped with the binary. If any are found, register
	// the on-demand `skill` loader tool plus a system-prompt
	// manifest so the model knows what's available.
	//
	// --no-skill bypasses the entire mechanism: no manifest in the
	// system prompt, no `skill` tool in the registry. Useful for a
	// clean-room run with zero extra context biasing the model.
	var (
		discovered    []*skills.Skill
		skillTool     *skills.Tool
		skillAddendum string
	)
	if !args.NoSkill {
		homeDir, _ := os.UserHomeDir()
		discovered, _ = skills.Discover(TervaHome(), args.CWD, homeDir, args.WithSkills)
		if len(discovered) > 0 {
			skillTool = skills.NewTool(discovered)
			reg[skillTool.Name()] = skillTool
			skillAddendum = skills.SystemPromptAddendum(discovered)
		}
	}
	_ = skillTool

	summaries := toolSummaries(reg, args)

	append_ := append([]string(nil), args.AppendSystemPrompt...)
	// Startup context files (project/user config_files, then --context-file
	// flags) inject just before AGENTS.md so repo policy stays the most
	// specific layer. Fail-fast: an explicitly-named missing file is a typo.
	if ctxBlock, err := readStartupContextFiles(args.CWD, eff.ContextFiles, args.ContextFiles); err != nil {
		return Resolved{}, err
	} else if ctxBlock != "" {
		append_ = append(append_, ctxBlock)
	}
	if agentsAddendum := readAgentsContext(args.CWD, TervaHome()); agentsAddendum != "" {
		append_ = append(append_, agentsAddendum)
	}
	if skillAddendum != "" {
		append_ = append(append_, skillAddendum)
	}
	if AutoSwarmEnabled() {
		append_ = append(append_, AutoSwarmSystemAddendum)
	}

	// Custom system prompt resolution order:
	//   1. --system-prompt flag (highest priority; ad-hoc per run)
	//   2. $TERVA_HOME/SYSTEM.md (persistent user override)
	//   3. built-in default (defaultIdentity + defaultGuidelines)
	custom := args.SystemPrompt
	if custom == "" {
		custom = readUserSystemPrompt(TervaHome())
	}

	sys := BuildSystemPrompt(SystemPromptOpts{
		CWD:          args.CWD,
		Tools:        summaries,
		Custom:       custom,
		Append:       append_,
		TervaDocsDir: docsDir,
		StatusTool:   reg["terva_status"] != nil,
	})

	reasoning := provider.NormalizeReasoning(firstNonEmpty(args.Reasoning, cfg.Reasoning))

	max := args.MaxSteps // 0 = unlimited

	return Resolved{
		Provider:         provName,
		Model:            model,
		Credential:       cred,
		AuthMethod:       method,
		AccountID:        accountID,
		BaseURL:          args.BaseURL,
		CWD:              args.CWD,
		Reasoning:        reasoning,
		ToolRegistry:     reg,
		ToolSummary:      summaries,
		SystemPrompt:     sys,
		MaxSteps:         max,
		MaxOutput:        resolvedModel.MaxOutput,
		Sandbox:          sandbox,
		SkillTool:        skillTool,
		systemAppend:     append_,
		systemCustom:     custom,
		toolDescriptions: descMapFromSummaries(summaries),
	}, nil
}

// readUserSystemPrompt looks for $TERVA_HOME/SYSTEM.md and returns its
// trimmed contents, or "" when the file is missing / unreadable /
// empty. Errors are intentionally swallowed: the file is optional,
// and any failure to read it should fall back to the built-in
// default system prompt rather than crash the run.
func readUserSystemPrompt(tervaHome string) string {
	if tervaHome == "" {
		return ""
	}
	path := filepath.Join(tervaHome, "SYSTEM.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readAgentsContext loads optional AGENTS.md instruction files. No
// default file is created or required: terva only includes files that
// already exist. Global instructions ($TERVA_HOME/AGENTS.md) come first,
// followed by project instructions from the top-most parent down to cwd.
func readAgentsContext(cwd, tervaHome string) string {
	type contextFile struct {
		path    string
		content string
	}
	var files []contextFile
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
		if seen[path] {
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return
		}
		content := strings.TrimSpace(string(raw))
		if content == "" {
			return
		}
		seen[path] = true
		files = append(files, contextFile{path: path, content: content})
	}
	addFirstFromDir := func(dir string) {
		if dir == "" {
			return
		}
		for _, name := range []string{"AGENTS.md", "AGENTS.MD"} {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				add(path)
				return
			}
		}
	}

	addFirstFromDir(tervaHome)

	if cwd != "" {
		abs, err := filepath.Abs(cwd)
		if err == nil {
			cwd = abs
		}
		var dirs []string
		for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
			dirs = append(dirs, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			addFirstFromDir(dirs[i])
		}
	}

	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Project context instructions loaded from AGENTS.md files. Follow them when working in this repository. Later files are more specific and may override earlier ones.\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "\n## %s\n\n%s\n", f.path, f.content)
	}
	return strings.TrimSpace(sb.String())
}

// descMapFromSummaries indexes the human-readable descriptions for
// the renderToolsSection rebuild path.
func descMapFromSummaries(summaries []ToolSummary) map[string]string {
	out := make(map[string]string, len(summaries))
	for _, s := range summaries {
		out[s.Name] = s.Description
	}
	return out
}

// NewClient returns a provider.Client for r, choosing the auth mode
// based on r.AuthMethod. Panics if no credential is present; callers
// must check HasCredential() first.
func (r Resolved) NewClient() provider.Client {
	if !r.HasCredential() {
		panic("NewClient called without credential; check HasCredential first")
	}
	switch r.Provider {
	case "ollama", "openai-compatible":
		return provider.NewOpenAI(r.Credential, r.BaseURL)
	case "kimi":
		// kimi-coding speaks anthropic-messages on api.kimi.com/coding.
		// Subscription OAuth (refreshed) wraps the same Anthropic-shaped client.
		inner := provider.NewKimiCodingWithHeaders(r.Credential, r.BaseURL, kimiCodeHeaders())
		if r.AuthMethod == "oauth" {
			return r.wrapWithRefresh(inner)
		}
		return inner
	case "moonshotai":
		return provider.NewMoonshot(r.Credential, r.BaseURL)
	case "moonshotai-cn":
		return provider.NewMoonshotCN(r.Credential, r.BaseURL)
	case "deepseek":
		return provider.NewDeepSeek(r.Credential, r.BaseURL)
	case "openai":
		return provider.NewOpenAI(r.Credential, r.BaseURL)
	case "openai-codex":
		inner := provider.NewOpenAICodex(r.Credential, r.AccountID, r.BaseURL)
		return r.wrapWithRefresh(inner)
	case "openai-responses":
		// Public OpenAI Responses API (api.openai.com/v1/responses) via
		// API key. Separate provider from `openai` (Chat Completions) and
		// from `openai-codex` (ChatGPT subscription OAuth).
		return provider.NewOpenAIResponses(r.Credential, r.BaseURL)
	case "google":
		return provider.NewGemini(r.Credential, r.BaseURL)
	case "cerebras":
		return provider.NewCerebras(r.Credential, r.BaseURL)
	case "groq":
		return provider.NewGroq(r.Credential, r.BaseURL)
	case "xai":
		return provider.NewXAI(r.Credential, r.BaseURL)
	case "together":
		return provider.NewTogether(r.Credential, r.BaseURL)
	case "huggingface":
		return provider.NewHuggingFace(r.Credential, r.BaseURL)
	case "openrouter":
		return provider.NewOpenRouter(r.Credential, r.BaseURL)
	case "zai":
		return provider.NewZAI(r.Credential, r.BaseURL)
	case "xiaomi":
		return provider.NewXiaomi(r.Credential, r.BaseURL)
	case "xiaomi-token-plan-ams":
		return provider.NewXiaomiTokenPlan("ams", r.Credential, r.BaseURL)
	case "xiaomi-token-plan-cn":
		return provider.NewXiaomiTokenPlan("cn", r.Credential, r.BaseURL)
	case "xiaomi-token-plan-sgp":
		return provider.NewXiaomiTokenPlan("sgp", r.Credential, r.BaseURL)
	case "opencode":
		return provider.NewOpenCode(r.Credential, r.BaseURL)
	case "opencode-go":
		return provider.NewOpenCodeGo(r.Credential, r.BaseURL)
	case "minimax":
		return provider.NewMinimaxAnthropic(r.Credential, r.BaseURL)
	case "minimax-cn":
		return provider.NewMinimaxCNAnthropic(r.Credential, r.BaseURL)
	case "fireworks":
		return provider.NewFireworksAnthropic(r.Credential, r.BaseURL)
	case "vercel-ai-gateway":
		return provider.NewVercelGatewayAnthropic(r.Credential, r.BaseURL)
	case "mistral":
		return provider.NewMistral(r.Credential, r.BaseURL)
	case "amazon-bedrock":
		return provider.NewBedrock(r.Credential, r.BaseURL)
	case "google-vertex":
		return provider.NewGoogleVertex(r.Credential, r.BaseURL)
	case "azure-openai-responses":
		return provider.NewAzureOpenAIResponses(r.Credential, r.BaseURL)
	case "github-copilot":
		return provider.NewGithubCopilot(r.Credential, r.BaseURL)
	case "cloudflare-workers-ai":
		return provider.NewCloudflareWorkersAI(r.Credential, r.BaseURL)
	case "cloudflare-ai-gateway":
		return provider.NewCloudflareAIGateway(r.Credential, r.BaseURL)
	default:
		if r.AuthMethod == "oauth" {
			inner := provider.NewAnthropicOAuth(r.Credential, r.BaseURL)
			return r.wrapWithRefresh(inner)
		}
		return provider.NewAnthropic(r.Credential, r.BaseURL)
	}
}

// wrapWithRefresh wraps an OAuth client so the access token is
// refreshed automatically before each API call. Without this, long
// sessions (hours) silently fail when the 1-hour token expires.
func (r Resolved) wrapWithRefresh(inner provider.Client) provider.Client {
	provName := r.Provider
	tokenProvider := provName
	if provName == "openai-codex" {
		tokenProvider = "openai"
	}
	baseURL := r.BaseURL
	accountID := r.AccountID

	refreshFn := func(ctx context.Context) (string, error) {
		tok, err := refreshIfExpired(tokenProvider, loadOAuthToken(tokenProvider))
		if err != nil {
			return "", err
		}
		return tok.AccessToken, nil
	}

	factory := func(token string) provider.Client {
		switch provName {
		case "openai-codex":
			return provider.NewOpenAICodex(token, accountID, baseURL)
		case "kimi":
			// anthropic-messages on api.kimi.com/coding.
			return provider.NewKimiCodingWithHeaders(token, baseURL, kimiCodeHeaders())
		default:
			return provider.NewAnthropicOAuth(token, baseURL)
		}
	}

	return provider.NewRefreshingClient(inner, refreshFn, factory)
}

// UseSandbox replaces the sandbox pointer that every tool in r's
// registry references. Used to keep the /jail state stable across
// agent rebuilds (e.g. /login, /model switching providers).
func (r *Resolved) UseSandbox(s *tools.Sandbox) {
	if s == nil || r == nil {
		return
	}
	r.Sandbox = s
	for name, t := range r.ToolRegistry {
		switch v := t.(type) {
		case *tools.ReadTool:
			v.Sandbox = s
		case *tools.WriteTool:
			v.Sandbox = s
		case *tools.EditTool:
			v.Sandbox = s
		case *tools.BashTool:
			v.Sandbox = s
		}
		_ = name
	}
}

// NewAgent constructs a core.Agent from r. Requires a credential.
func (r Resolved) NewAgent() *core.Agent {
	a := core.NewAgent(r.NewClient(), r.Model, r.SystemPrompt, r.ToolRegistry)
	a.MaxSteps = r.MaxSteps
	a.MaxTokens = r.MaxOutput
	a.Reasoning = r.Reasoning
	// Bind the live agent into terva_status so it can report current model,
	// reasoning, and token usage (the registry — and thus the tool — is
	// built before the agent exists).
	if st, ok := r.ToolRegistry["terva_status"].(*tools.StatusTool); ok {
		st.Agent = a
	}
	return a
}

func buildToolRegistry(args Args, cwd string, sandbox *tools.Sandbox, provName, authMethod string) core.Registry {
	if args.NoTools {
		return core.Registry{}
	}
	all := map[string]core.Tool{
		"read":         &tools.ReadTool{CWD: cwd, Sandbox: sandbox},
		"write":        &tools.WriteTool{CWD: cwd, Sandbox: sandbox},
		"edit":         &tools.EditTool{CWD: cwd, Sandbox: sandbox},
		"bash":         &tools.BashTool{CWD: cwd, Sandbox: sandbox},
		"terva_status": &tools.StatusTool{Provider: provName, CWD: cwd, AuthMethod: authMethod, BaseURL: args.BaseURL},
	}
	reg := core.Registry{}
	if len(args.Tools) == 0 {
		for _, t := range all {
			reg[t.Name()] = t
		}
		return reg
	}
	for _, name := range args.Tools {
		if t, ok := all[name]; ok {
			reg[name] = t
		}
	}
	return reg
}

func toolSummaries(reg core.Registry, args Args) []ToolSummary {
	order := []string{"read", "write", "edit", "bash"}
	var out []ToolSummary
	for _, name := range order {
		if t, ok := reg[name]; ok {
			out = append(out, ToolSummary{Name: t.Name(), Description: t.Description()})
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func kimiCodeHeaders() map[string]string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	deviceID := ""
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".kimi", "device_id")); err == nil {
			deviceID = strings.TrimSpace(string(b))
		}
	}
	if deviceID == "" {
		deviceID = "zot" // rename:keep — bound to existing kimi device sessions
	}
	return map[string]string{
		"User-Agent":         "KimiCLI/1.41.0",
		"X-Msh-Platform":     "kimi_cli",
		"X-Msh-Version":      "1.41.0",
		"X-Msh-Device-Name":  host,
		"X-Msh-Device-Model": runtime.GOOS + "-" + runtime.GOARCH,
		"X-Msh-Os-Version":   runtime.GOOS,
		"X-Msh-Device-Id":    deviceID,
	}
}

func envVarName(provider string) string {
	switch provider {
	case "openai", "openai-codex", "openai-responses":
		return "OPENAI"
	case "kimi":
		return "KIMI"
	case "deepseek":
		return "DEEPSEEK"
	case "google":
		return "GEMINI"
	case "ollama":
		return "OLLAMA"
	case "moonshotai", "moonshotai-cn":
		return "MOONSHOT"
	case "groq":
		return "GROQ"
	case "xai":
		return "XAI"
	case "cerebras":
		return "CEREBRAS"
	case "together":
		return "TOGETHER"
	case "huggingface":
		return "HF"
	case "openrouter":
		return "OPENROUTER"
	case "mistral":
		return "MISTRAL"
	case "zai":
		return "ZAI"
	case "xiaomi":
		return "XIAOMI"
	case "xiaomi-token-plan-ams":
		return "XIAOMI_TOKEN_PLAN_AMS"
	case "xiaomi-token-plan-cn":
		return "XIAOMI_TOKEN_PLAN_CN"
	case "xiaomi-token-plan-sgp":
		return "XIAOMI_TOKEN_PLAN_SGP"
	case "minimax":
		return "MINIMAX"
	case "minimax-cn":
		return "MINIMAX_CN"
	case "fireworks":
		return "FIREWORKS"
	case "vercel-ai-gateway":
		return "AI_GATEWAY"
	case "opencode", "opencode-go":
		return "OPENCODE"
	case "github-copilot":
		return "COPILOT_GITHUB_TOKEN"
	case "cloudflare-workers-ai", "cloudflare-ai-gateway":
		return "CLOUDFLARE"
	case "amazon-bedrock":
		return "AWS"
	case "google-vertex":
		return "GOOGLE_CLOUD"
	case "azure-openai-responses":
		return "AZURE_OPENAI"
	default:
		return "ANTHROPIC"
	}
}
