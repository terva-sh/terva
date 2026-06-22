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
	Provider    string
	Model       string
	Credential  string // api key or oauth access token
	AuthMethod  string // "apikey" | "oauth" | "" (no credential yet)
	AccountID   string // ChatGPT account id (for openai oauth), "" otherwise
	BaseURL     string
	CWD         string
	Reasoning   string
	Temperature *float32
	// Insecure skips TLS verification for the inference client only
	// (gated to openai-compatible/ollama + explicit --base-url in Resolve).
	Insecure bool

	// VisionCapable is the resolved model's image-input verdict
	// (model.Has(CapImageInput)). The live registry rebuild on a /model
	// switch re-derives this, but the initial wiring threads it through
	// here so cli.go's setApprovalMode rebuild can pass it to
	// buildToolRegistry without re-resolving the model.
	VisionCapable bool

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

	// DisableContextExtensions is the resolved (user ∪ project) set of
	// extensions opted out of contributing model context. The host
	// passes it to the extension manager via SetContextDisabled.
	DisableContextExtensions []string

	// DisableExtensions is the resolved (user ∪ project) set of
	// extensions that must not be loaded at all. The host passes it to
	// the extension manager via SetDisabledExtensions BEFORE discovery.
	DisableExtensions []string

	// Trusted is the resolved Workspace Trust verdict for this launch's
	// cwd (resolveTrust: --trust flag, then the store, else untrusted).
	// When false the project is RESTRICTED: its project-local
	// extensions, skills, and context_files were NOT loaded (the safe
	// core still is). The host passes it to the extension manager via
	// SetProjectTrusted BEFORE discovery; Resolve already used it to gate
	// project skills + project context_files. See
	// docs/plans/workspace-trust.md.
	Trusted bool

	// Bookkeeping for MergeExtensionTools. Captured at Resolve time
	// so the system prompt can be rebuilt later without re-running
	// resolve.
	systemAppend     []string
	systemCustom     string
	toolDescriptions map[string]string
	// extensionContext is the extensions' aggregated static context
	// contribution (register_context), folded into the cached system
	// prompt addendum by MergeExtensionTools after extensions register.
	extensionContext string

	// approvalMode is the effective approval mode at resolve time.
	// Plan mode shrinks the registry to read-only tools and keeps
	// mutating extension tools out of the merge.
	approvalMode core.ApprovalMode

	// readOnlySet is the gate policy's dynamic read-only registry,
	// adopted via AdoptReadOnlySet so tool merges can extend it with
	// read_only-annotated extension/MCP tools. Nil when no gate
	// exists (pure yolo).
	readOnlySet *core.ReadOnlySet
}

// AdoptReadOnlySet hands the permission policy's read-only registry
// to the resolver, so merged extension/MCP tools that declare
// read_only join the classification the approval modes consult.
func (r *Resolved) AdoptReadOnlySet(s *core.ReadOnlySet) { r.readOnlySet = s }

// AddExtraTools folds embedder-supplied tools into r's ToolRegistry
// and re-renders the system prompt so the model sees them. nil/empty
// is a no-op.
//
// Conflict policy is the deliberate inverse of MergeExtensionTools:
// there, auto-discovered subprocess extensions lose a name collision
// so a built-in is never silently shadowed. Here the tools are
// in-process, compiled into the embedding binary, and injected
// explicitly through sdk.Config — maximally trusted — so an
// embedder-supplied tool OVERRIDES a built-in of the same name,
// letting an embedder swap, say, a custom bash.
func (r *Resolved) AddExtraTools(extra []core.Tool) {
	if len(extra) == 0 {
		return
	}
	if r.ToolRegistry == nil {
		r.ToolRegistry = core.Registry{}
	}
	for _, t := range extra {
		if t == nil {
			continue
		}
		r.ToolRegistry[t.Name()] = t
	}
	r.rebuildSystemPrompt()
}

// RefreshExtensionContext re-pulls the source's static context
// (register_context / refresh_context) and rebuilds the cached system
// prompt if it changed, WITHOUT touching the tool registry — so a live
// session can update its agent's prompt when an extension swaps its
// context block mid-session (protocol 3) without re-merging tools. It
// returns the (possibly rebuilt) system prompt and whether it changed,
// so the caller updates the running agent only when there is something
// to update. nil-safe on the source.
func (r *Resolved) RefreshExtensionContext(mgr interface{ StaticContext() string }) (string, bool) {
	if mgr == nil {
		return r.SystemPrompt, false
	}
	text := mgr.StaticContext()
	if text == r.extensionContext {
		return r.SystemPrompt, false
	}
	r.extensionContext = text
	r.rebuildSystemPrompt()
	return r.SystemPrompt, true
}

// rebuildSystemPrompt re-renders SystemPrompt from the captured
// resolve-time materials plus the current tool registry and the
// extensions' static context contribution. The single render path for
// every post-resolve change (extra tools, extension-tool merge,
// extension static context) so the inputs can't drift.
func (r *Resolved) rebuildSystemPrompt() {
	appendBlocks := r.systemAppend
	if strings.TrimSpace(r.extensionContext) != "" {
		appendBlocks = append(append([]string{}, r.systemAppend...), r.extensionContext)
	}
	r.SystemPrompt = BuildSystemPrompt(SystemPromptOpts{
		CWD:          r.CWD,
		Tools:        toolSummariesFromRegistry(r.ToolRegistry, r.toolDescriptions),
		Custom:       r.systemCustom,
		Append:       appendBlocks,
		TervaDocsDir: filepath.Join(TervaHome(), "docs"),
		StatusTool:   r.ToolRegistry["terva_status"] != nil,
		PersonaName:  PersonaName(),
	})
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
	changed := MergeToolsForMode(r.ToolRegistry, r.approvalMode, r.readOnlySet, mgr)
	// Pull the source's static context contribution (register_context)
	// into the cached addendum. Optional interface so MCP sources (which
	// don't contribute context) are unaffected. Folding it here means
	// every existing merge call site picks it up with no extra wiring.
	if cs, ok := mgr.(interface{ StaticContext() string }); ok {
		if text := cs.StaticContext(); text != r.extensionContext {
			r.extensionContext = text
			changed = true
		}
	}
	if changed {
		// Re-render the system prompt with the merged tool list. Skill
		// addendum + extension static context are preserved by the
		// shared rebuild path.
		r.rebuildSystemPrompt()
	}
}

// MergeToolsForMode folds an extension/MCP source's tools into reg for
// the given approval mode, registering read_only-annotated tools into
// roSet, and reports whether anything was added. Built-in tools (and
// already-merged names) win on conflict. In plan mode only tools that
// declare themselves side-effect-free join — the rest stay invisible
// so the model doesn't try them, with the confirm gate as backstop.
//
// Shared by the startup merge and the live approval-mode switch so the
// two cannot drift; the live switch rebuilds reg from scratch and
// re-merges, which is why this is registry-only (no system-prompt
// coupling).
func MergeToolsForMode(reg core.Registry, mode core.ApprovalMode, roSet *core.ReadOnlySet, mgr ExtensionToolSource) bool {
	if mgr == nil || reg == nil {
		return false
	}
	changed := false
	for _, info := range mgr.Tools() {
		// Read-only classification: a declared authority wins (local-read
		// and local-data are auto-allowable), so a network-read tool is not
		// mistaken for a local read even if it also set the legacy
		// read_only bool. An empty authority falls back to that bool.
		readOnly := info.ReadOnly
		if info.Authority != "" {
			readOnly = core.IsReadOnlyAuthority(info.Authority)
		}
		if mode == core.ApprovalPlan && !readOnly {
			continue
		}
		if _, exists := reg[info.Name]; exists {
			continue
		}
		reg[info.Name] = mgr.NewExtensionTool(info)
		if readOnly {
			roSet.Add(info.Name)
		}
		changed = true
	}
	return changed
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
	// ReadOnly carries the source's no-side-effects declaration
	// (extension register_tool read_only / MCP readOnlyHint). It
	// admits the tool in plan mode and feeds the permission
	// classification.
	ReadOnly bool
	// Authority carries the source's effect-class declaration
	// (extension register_tool authority; core.Authority). When set it
	// overrides ReadOnly for classification: only local-read is treated
	// as auto-allowable read-only, so a network-read tool is admitted
	// neither in plan nor by the read-only auto-allow. Empty falls back
	// to ReadOnly. MCP tools leave it empty (they have only readOnlyHint).
	Authority string
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
// caller didn't pick one. Reads the provider registry (provider_registry.go).
//
// Returns the empty string for providers with no built-in default
// (ollama, openai-compatible) — the caller special-cases those and
// errors or uses whatever the user passed.
func defaultModelForProvider(prov string) string {
	if spec, ok := providerByID[prov]; ok {
		if spec.noDefaultModel {
			return ""
		}
		if spec.defaultModel != "" {
			return spec.defaultModel
		}
	}
	return provider.DefaultModel.ID
}

// isKnownProvider reports whether name is a canonical provider id in
// the registry (provider_registry.go). knownProviders (the ordered id
// list) and the providerAliases map are derived there too.
func isKnownProvider(name string) bool {
	_, ok := providerByID[name]
	return ok
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
	// Workspace Trust verdict for this launch's cwd: --trust (one-shot)
	// or the persisted store, else untrusted (the default in every mode).
	// Gates project-local content that auto-acts — project skills,
	// project context_files, and (threaded onward to the extension
	// manager) project extensions. The safe core loads regardless. See
	// docs/plans/workspace-trust.md.
	trusted := resolveTrustState(args).IsTrusted()

	// eff is the layered (project-over-user) read view; cfg is the pristine,
	// writable user layer. Every existing read/repair below stays on cfg so
	// behaviour is unchanged and the model-repair paths only ever persist the
	// user config. eff.ContextFiles (project-or-user, resolved absolute) feeds
	// startup context-file injection; the project context_files layer is
	// dropped when the workspace is untrusted (trusted=false).
	eff := ResolveConfig(args.CWD, trusted)
	cfg := eff.User

	// User-requested provider (explicit > config > default).
	// Normalise common aliases (e.g. "bedrock" -> "amazon-bedrock")
	// before validation so an alias is never mistaken for an unknown
	// provider and silently downgraded to anthropic.
	argProvider := canonicalProvider(args.Provider)
	// eff.Config is project-over-user (the project layer is trusted-gated in
	// ResolveConfig), so this yields: --provider flag > project (trusted) >
	// user config > anthropic. Repairs below stay on cfg (the user layer).
	provName := firstNonEmpty(argProvider, canonicalProvider(eff.Config.Provider), "anthropic")
	// Gate --insecure: it skips TLS verification for the inference client
	// only, and only for a self-signed custom endpoint. Restrict it to the
	// openai-compatible / ollama providers (plain http.Client clients, no
	// wrapped transport) with an EXPLICIT --base-url, so it can never
	// silently weaken a built-in provider's verification. args.BaseURL is
	// still the raw flag here (model-default base URLs are applied later).
	if args.Insecure && (strings.TrimSpace(args.BaseURL) == "" || (provName != "openai-compatible" && provName != "ollama")) {
		return Resolved{}, fmt.Errorf("--insecure is only allowed for the openai-compatible or ollama provider with an explicit --base-url")
	}
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
	} else if ep, ok := cfg.Endpoints[provName]; ok {
		// A user-defined named OpenAI-compatible endpoint: like
		// openai-compatible, but its base URL + default context come from the
		// config entry, and its key (if any) resolves from APIKeyEnv/auth.json.
		// Keyless local servers fall back to the harmless sentinel bearer.
		compatCtx = ep.ContextWindow
		compatBaseURL = ep.BaseURL
		storedKey, _, _, _ := ResolveCredentialFull(provName, args.APIKey)
		cred = firstNonEmpty(storedKey, "openai-compatible")
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
		if args.Model == "" && eff.Config.Model == "" {
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
	openCatalogue := provName == "ollama" || provName == "openai-compatible" || isEndpointProvider(provName, cfg)
	// --model flag > project (trusted) > user config (eff.Config is the
	// project-over-user read view; cfg stays the user layer for repairs).
	model := firstNonEmpty(args.Model, eff.Config.Model)
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
	if resolveJail(args) {
		sandbox.Lock()
	}
	approval := effectiveApprovalMode(args)
	visionCapable := resolvedModel.Has(provider.CapImageInput)
	reg := buildToolRegistry(args, approval, args.CWD, sandbox, provName, method, visionCapable)

	docsDir, _ := tervadocs.EnsureInstalled(TervaHome())
	// terva's own state lives under $TERVA_HOME, outside the cwd jail. A
	// jailed agent still needs to read the non-sensitive, shared dirs —
	// its docs (referenced in the system prompt), installed skills/themes,
	// and installed extensions plus their data — so register them as
	// read-only roots: readable by read/grep/glob, never writable.
	//
	// Deliberately EXCLUDED as sensitive: auth.json (credentials),
	// config.json, sessions/ and swarm/ (transcripts), and logs/ — which
	// aggregates stderr from MCP servers, the bot, connectors, and hooks
	// and is a secret-leak sink. Only specific subdirs are added, never
	// $TERVA_HOME itself.
	home := TervaHome()
	sandbox.AddReadOnlyRoot(
		docsDir,
		filepath.Join(home, "extensions"),
		filepath.Join(home, "ext-data"),
		filepath.Join(home, "skills"),
		filepath.Join(home, "themes"),
	)
	// logs/ as a whole is a secret-leak sink (MCP/bot/connector/hooks
	// stderr can carry tokens and chat content), so expose ONLY the
	// extension logs the agent needs for debugging, by name — not the dir.
	sandbox.AddReadOnlyGlob(filepath.Join(home, "logs"), "ext-*.log")
	// The bash tool spills over-long output to $TMPDIR/terva-bash-*.log and
	// points the model at it; allow reading those (the agent's own output)
	// so a jailed agent can page the spill via `read` without /unjail.
	sandbox.AddReadOnlyGlob(os.TempDir(), "terva-bash-*.log")

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
		// trusted gates the PROJECT skill dirs: an untrusted workspace's
		// .terva|.claude|.agents/skills are skipped so a cloned repo can't
		// inject SKILL.md instructions. Built-in/user/global skills load
		// regardless.
		discovered, _ = skills.Discover(TervaHome(), args.CWD, homeDir, args.WithSkills, trusted)
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
	// Untrusted workspace with gated content: tell the model the project
	// extensions/skills/context were withheld so it can explain their
	// absence rather than hallucinate them (decision #2 / plan §5).
	if note := restrictedSystemNote(args.CWD, trusted); note != "" {
		append_ = append(append_, note)
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
	// Temperature fall-through: --temperature flag > per-model (models.json) >
	// global config default. AdaptiveThinking models reject sampling params,
	// so temperature is never sent for them regardless of the layers.
	temperature := args.Temperature
	if temperature == nil {
		temperature = resolvedModel.Temperature
	}
	if temperature == nil {
		temperature = cfg.Temperature
	}
	if resolvedModel.AdaptiveThinking {
		temperature = nil
	}

	max := args.MaxSteps // 0 = unlimited

	return Resolved{
		Provider:                 provName,
		Model:                    model,
		Credential:               cred,
		AuthMethod:               method,
		AccountID:                accountID,
		BaseURL:                  args.BaseURL,
		CWD:                      args.CWD,
		Reasoning:                reasoning,
		Temperature:              temperature,
		Insecure:                 args.Insecure,
		VisionCapable:            visionCapable,
		ToolRegistry:             reg,
		ToolSummary:              summaries,
		SystemPrompt:             sys,
		MaxSteps:                 max,
		MaxOutput:                resolvedModel.MaxOutput,
		Sandbox:                  sandbox,
		SkillTool:                skillTool,
		DisableContextExtensions: eff.Config.DisableContextExtensions,
		DisableExtensions:        eff.Config.DisableExtensions,
		Trusted:                  trusted,
		systemAppend:             append_,
		systemCustom:             custom,
		toolDescriptions:         descMapFromSummaries(summaries),
		approvalMode:             approval,
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

// NewClient returns a provider.Client for r via the provider registry
// (provider_registry.go). Panics if no credential is present; callers
// must check HasCredential() first. An unregistered provider falls
// back to the anthropic client, matching the historical default.
func (r Resolved) NewClient() provider.Client {
	if !r.HasCredential() {
		panic("NewClient called without credential; check HasCredential first")
	}
	c := r.dispatchClient()
	if r.Insecure {
		// Scope the cert-skipping client to the inference client alone.
		// Resolve already gated this to openai-compatible/ollama, whose
		// client is a plain openaiClient that WithHTTPClient can reach.
		c = provider.WithHTTPClient(c, provider.NewHTTPClient(true))
	}
	return c
}

func (r Resolved) dispatchClient() provider.Client {
	if spec, ok := providerByID[r.Provider]; ok {
		return spec.newClient(r)
	}
	if r.AuthMethod == "oauth" {
		return r.wrapWithRefresh(provider.NewAnthropicOAuth(r.Credential, r.BaseURL))
	}
	return provider.NewAnthropic(r.Credential, r.BaseURL)
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
		case *tools.GrepTool:
			v.Sandbox = s
		case *tools.GlobTool:
			v.Sandbox = s
		}
		_ = name
	}
}

// SetAsker wires the front-end question channel into the registered
// ask_user_question tool. The cli calls it once the interactive front
// end (the Asker) exists — the same construction-order unknot as
// ConfirmGate.SetConfirmer. Headless modes never call it, leaving the
// tool's Asker nil so it returns a model-readable "no channel" result
// instead of blocking. Nil-safe.
func (r *Resolved) SetAsker(a core.Asker) {
	if r == nil {
		return
	}
	bindAsker(r.ToolRegistry, a)
}

// bindAsker wires the front-end question channel into the
// ask_user_question tool of an arbitrary registry. Used both for the
// initial registry and for the fresh registry a live approval-mode
// switch builds, so the tool keeps its channel across that rebuild
// (without it the rebuilt tool's Asker is nil and the tool falls back to
// its "no channel" result). Nil-safe; no-op when the tool isn't present.
func bindAsker(reg core.Registry, a core.Asker) {
	if t, ok := reg["ask_user_question"].(*tools.AskUserTool); ok {
		t.Asker = a
	}
}

// NewAgent constructs a core.Agent from r. Requires a credential.
func (r Resolved) NewAgent() *core.Agent {
	a := core.NewAgent(r.NewClient(), r.Model, r.SystemPrompt, r.ToolRegistry)
	a.MaxSteps = r.MaxSteps
	a.MaxTokens = r.MaxOutput
	a.Reasoning = r.Reasoning
	a.Temperature = r.Temperature
	// Bind the live agent into terva_status so it can report current model,
	// reasoning, and token usage (the registry — and thus the tool — is
	// built before the agent exists).
	if st, ok := r.ToolRegistry["terva_status"].(*tools.StatusTool); ok {
		st.Agent = a
	}
	// Bind the agent's transcript epoch into read so it can dedup re-reads
	// of an unchanged file that is still in context (same late-binding
	// reason as terva_status above).
	if rt, ok := r.ToolRegistry["read"].(*tools.ReadTool); ok {
		rt.Epoch = a
	}
	return a
}

// buildToolRegistry assembles the built-in tool set. visionCapable is
// the active model's image-input verdict (model.Has(CapImageInput)); it
// flows into ReadTool so reading an image file returns inline pixels for
// a vision model but an actionable text result otherwise. Called on
// every agent rebuild, so a /model switch re-derives the verdict.
func buildToolRegistry(args Args, approval core.ApprovalMode, cwd string, sandbox *tools.Sandbox, provName, authMethod string, visionCapable bool) core.Registry {
	if args.NoTools {
		return core.Registry{}
	}
	all := map[string]core.Tool{
		"read":              &tools.ReadTool{CWD: cwd, Sandbox: sandbox, SupportsVision: visionCapable},
		"write":             &tools.WriteTool{CWD: cwd, Sandbox: sandbox},
		"edit":              &tools.EditTool{CWD: cwd, Sandbox: sandbox},
		"bash":              &tools.BashTool{CWD: cwd, Sandbox: sandbox, Env: map[string]string{"TERVA_HOME": TervaHome()}},
		"grep":              &tools.GrepTool{CWD: cwd, Sandbox: sandbox},
		"glob":              &tools.GlobTool{CWD: cwd, Sandbox: sandbox},
		"terva_status":      &tools.StatusTool{Provider: provName, CWD: cwd, AuthMethod: authMethod, BaseURL: args.BaseURL},
		"ask_user_question": &tools.AskUserTool{},
	}
	// Plan mode promises read-only: mutating tools don't enter the
	// registry at all (the model shouldn't even see them), with the
	// confirm gate as the backstop for anything that arrives later.
	// Interactive tools (ask_user_question) are kept: asking the user
	// is exactly what plan mode wants when requirements are unclear.
	if approval == core.ApprovalPlan {
		for name := range all {
			if !readOnlyTools[name] && !interactiveTools[name] {
				delete(all, name)
			}
		}
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
	order := []string{"read", "write", "edit", "bash", "grep", "glob", "ask_user_question"}
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

// envVarName returns the short token shown in "set <X>_API_KEY"
// guidance when a provider has no credential. Reads the registry
// (provider_registry.go); empty/unknown falls back to ANTHROPIC.
func envVarName(prov string) string {
	if spec, ok := providerByID[prov]; ok && spec.envHint != "" {
		return spec.envHint
	}
	return "ANTHROPIC"
}
