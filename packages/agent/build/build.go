package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	tervadocs "terva.sh/terva"
	tervaexamples "terva.sh/terva/examples"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Resolved is the effective configuration after merging CLI, config, defaults.
type Resolved struct {
	Provider   string
	Model      string
	Credential string // api Key or oauth access token
	AuthMethod string // "apikey" | "oauth" | "" (no credential yet)
	// CredentialErr is the credential-resolution failure a
	// Resolve(requireCred=false) call swallowed (a *CredentialError with the
	// full user-facing message), nil when a credential resolved. Lets a host
	// that boots credential-less (the TUI's Workspace) report or defer the
	// failure without a second Resolve.
	CredentialErr error
	AccountID     string // ChatGPT account id (for openai oauth), "" otherwise
	BaseURL       string
	CWD           string
	Reasoning     string
	Temperature   *float32
	// Insecure skips TLS verification for the inference client only
	// (gated to openai-compatible/ollama + explicit --base-url in Resolve).
	Insecure bool

	// VisionCapable is the resolved model's image-input verdict
	// (model.Has(CapImageInput)). The live registry rebuild on a /model
	// switch re-derives this, but the initial wiring threads it through
	// here so cli.go's setApprovalMode rebuild can pass it to
	// BuildToolRegistry without re-resolving the model.
	VisionCapable bool

	// ImageRegistry holds the resolved image-generation backends (empty when
	// image generation is off). Threaded through like VisionCapable so the
	// live registry rebuild (cli.go setApprovalMode) keeps generate_image.
	ImageRegistry *imagegen.Registry

	ToolRegistry core.Registry
	// Tasks is the built-in task controller (nil in chat/play/--no-tools). The
	// host wires its per-turn context card, its open-work gate, and its
	// per-session store rebind.
	Tasks        *tasktool.Controller
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

	// LazyTools enables lazy tool visibility (retro H2·b) on the agent:
	// NewAgent calls EnableLazyTools(LazyToolActive...) so only the core group
	// plus the always-active groups are advertised, and the activate_tools tool
	// is registered so the model can bring hidden groups in on demand. Off by
	// default. From the user config (config.LazyTools).
	LazyTools      bool
	LazyToolActive []string

	// EngineFeatures carries the config `engine_features` overrides to the
	// NewAgent funnel, where every declared EngineFeature is applied (its
	// default unless overridden here). From config.EngineFeatures.
	EngineFeatures map[string]bool

	// EscalateAuto is config escalation.auto: escalate a stuck loop to the
	// stronger model WITHOUT asking first. From the user layer only (a project
	// config can never force silent transcript egress). Applied at NewAgent.
	EscalateAuto bool

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
	systemAppend []PromptSegment
	systemCustom string
	// Persona is the resolved active Persona, captured so rebuildSystemPrompt
	// re-renders with the same identity + charter rather than re-resolving.
	Persona          Persona
	toolDescriptions map[string]string
	// extensionContext is the extensions' aggregated static context
	// contribution (register_context), folded into the cached system
	// prompt addendum by MergeExtensionTools after extensions register.
	extensionContext string

	// ApprovalMode is the effective approval mode at resolve time.
	// Plan mode shrinks the registry to read-only tools and keeps
	// mutating extension tools out of the merge.
	ApprovalMode core.ApprovalMode

	// Experience is the meta-mode ("", chat, play). chat additionally keeps
	// extension tools out of the merge (pure conversation); play keeps them.
	Experience string

	// NoTools mirrors args.NoTools. Like chat it suppresses ALL tools — but
	// the tools only: built-ins are dropped in BuildToolRegistry, and the
	// skill tool + extension/MCP merges are skipped (MergeExtensionTools),
	// WITHOUT chat's identity/intro changes. Without this gate the now-full-
	// agent bot's `--no-tools` would still expose every extension/MCP/skill
	// tool — it only dropped the built-ins.
	NoTools bool

	// readOnlySet is the gate policy's dynamic read-only registry,
	// adopted via AdoptReadOnlySet so tool merges can extend it with
	// read_only-annotated extension/MCP tools. Nil when no gate
	// exists (pure yolo).
	readOnlySet *core.ReadOnlySet

	// loreTriggered holds this run's keyword-triggered lore entries (the
	// non-constant ones); loreConfig carries the collection scan/budget
	// settings. Constant entries are already baked into the cached prefix
	// (systemAppend); loreEphemeral scans these against recent messages each
	// turn. Empty when lore is off (--no-lore or config `lore: false`).
	loreTriggered []lore.Entry
	loreConfig    lore.Config
	// loreActive is every lore entry loaded for this run (file + card,
	// constant + triggered), captured so /lore can list what's active.
	loreActive []lore.Entry
	// loreFired records which triggered entries matched on the most recent
	// turn (a shared pointer so value-copies of Resolved agree); /lore reads
	// it via LoreFired. Allocated in Resolve.
	loreFired *loreFiredRecord

	// CardGreeting is a --card's first_mes (macros substituted), seeded as the
	// opening assistant message of a fresh session. Empty when no card.
	CardGreeting string

	// introOverride is the fully-resolved intro-slot override — a --card's
	// system_prompt/framing ({{original}} + macros resolved) or, for a native
	// additive Persona, its agent_introduction — captured so rebuildSystemPrompt
	// reproduces the SAME intro on a mid-session re-render (e.g. after an
	// extension merges tools). introSource is its provenance label. Empty when
	// nothing overrides the default intro; both are ignored when systemCustom is
	// set (an immersive Persona / --system-prompt owns the whole prompt).
	introOverride string
	introSource   string

	// postHistory is a --card's post_history_instructions (macros + {{original}}
	// resolved), injected by PerTurnContext into the uncached per-turn tail
	// after the lore block. Empty when no card / no PHI.
	postHistory string

	// SystemSegments is the labeled provenance of the current system prompt
	// (the source SystemSegments produced), captured for the prompt-dump
	// manifest. Kept in sync by rebuildSystemPrompt.
	SystemSegments []PromptSegment

	// asker is the front end's question channel, retained by SetAsker so
	// NewAgent can give the agent loop its own handle on it (the prefix-change
	// guard asks from the turn policy, not from a tool). Nil for hosts that never
	// call SetAsker — every headless mode.
	asker core.Asker

	// escalator hands the live session to a stronger model when a tool loop
	// persists past the detector's nudge (rung 3). Retained by SetEscalator and
	// bound at NewAgent, mirroring asker. Nil for hosts with no swap target — the
	// detector still nudges, nothing escalates.
	escalator core.Escalator
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

// rebuildSystemPrompt re-renders SystemPrompt from the captured
// resolve-time materials plus the current tool registry and the
// extensions' static context contribution. The single render path for
// every post-resolve change (extra tools, extension-tool merge,
// extension static context) so the inputs can't drift.
func (r *Resolved) rebuildSystemPrompt() {
	appendBlocks := r.systemAppend
	if strings.TrimSpace(r.extensionContext) != "" {
		appendBlocks = append(append([]PromptSegment{}, r.systemAppend...), PromptSegment{Source: "extension-context", Text: r.extensionContext})
	}
	r.SystemSegments = SystemSegments(SystemPromptOpts{
		CWD:           r.CWD,
		Tools:         toolSummariesFromRegistry(r.ToolRegistry, r.toolDescriptions),
		Custom:        r.systemCustom,
		Append:        appendBlocks,
		TervaDocsDir:  filepath.Join(config.TervaHome(), "docs"),
		StatusTool:    r.ToolRegistry["terva_status"] != nil,
		PersonaName:   r.Persona.Name,
		Charter:       r.Persona.Charter,
		Experience:    r.Experience,
		IntroOverride: r.introOverride,
		IntroSource:   r.introSource,
	})
	r.SystemPrompt = joinSegmentTexts(r.SystemSegments)
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
	// chat mode is pure conversation: no tools from anywhere, so extension
	// tools don't merge (their commands/panels/context still work). play keeps
	// extension tools — that's the simulated world the agent acts through.
	// --no-tools suppresses the merge for the same reason (every source off);
	// it also covers MCP, which routes through this same merge. Either way the
	// extension's commands/panels/static context below still register.
	var changed bool
	if r.Experience != ExperienceChat && !r.NoTools {
		changed = MergeToolsForMode(r.ToolRegistry, r.ApprovalMode, r.readOnlySet, mgr)
	}
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

// DefaultModelForProvider returns the model id terva prefers when the
// caller didn't pick one. Reads the provider registry (provider_registry.go).
//
// Returns the empty string for providers with no built-in default
// (ollama, openai-compatible) — the caller special-cases those and
// errors or uses whatever the user passed.
func DefaultModelForProvider(prov string) string {
	if spec, ok := ProviderByID[prov]; ok {
		if spec.noDefaultModel {
			return ""
		}
		if spec.defaultModel != "" {
			return spec.defaultModel
		}
	}
	return provider.DefaultModel.ID
}

// IsKnownProvider reports whether name is a canonical provider id in
// the registry (provider_registry.go). KnownProviders (the ordered id
// list) and the providerAliases map are derived there too.
func IsKnownProvider(name string) bool {
	_, ok := ProviderByID[name]
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

// CredentialError marks a credential-resolution failure: returned by
// Resolve(requireCred=true), and carried on Resolved.CredentialErr by a
// requireCred=false call. Hosts with a login flow (the interactive TUI) defer
// it and open /login; hosts without one (the web daemon) fail fast on it.
type CredentialError struct{ Err error }

func (e *CredentialError) Error() string { return e.Err.Error() }
func (e *CredentialError) Unwrap() error { return e.Err }

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
	trusted := ResolveTrustState(args).IsTrusted()

	// eff is the layered (project-over-user) read view; cfg is the pristine,
	// writable user layer. Every existing read/repair below stays on cfg so
	// behaviour is unchanged and the model-repair paths only ever persist the
	// user config. eff.ContextFiles (project-or-user, resolved absolute) feeds
	// startup context-file injection; the project context_files layer is
	// dropped when the workspace is untrusted (trusted=false).
	eff := config.ResolveConfig(args.CWD, trusted)
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
	if !IsKnownProvider(provName) {
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
		_ = config.SaveConfig(cfg)
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
		// config entry, and its Key (if any) resolves from APIKeyEnv/auth.json.
		// Keyless local servers fall back to the harmless sentinel bearer.
		compatCtx = ep.ContextWindow
		compatBaseURL = ep.BaseURL
		storedKey, _, _, _ := ResolveCredentialFull(provName, args.APIKey)
		cred = firstNonEmpty(storedKey, "openai-compatible")
		method = "apikey"
	} else if provName == "openai-compatible" {
		// A user-configured OpenAI-compatible endpoint (local model
		// server, gateway, ...). The base URL and model id were captured
		// in the login form and live in auth.json; the API Key is
		// optional, so fall back to a harmless sentinel bearer token when
		// the server doesn't need one. Seed --base-url / --model from the
		// stored values when the caller didn't pass them explicitly.
		storedBaseURL, storedModel, storedCtx := config.AuthStoreFor().Extras(provName)
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
		// KnownProviders defines fallback priority. ollama is skipped:
		// it has no credential and would always "match".
		for _, other := range KnownProviders {
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
			model = DefaultModelForProvider(provName)
		}
	}
	// If the resolved model belongs to a different provider (e.g. config
	// says gpt-5 but we fell back to anthropic), pick that provider's default.
	if !openCatalogue {
		if m, err := provider.FindModel("", model); err == nil && m.Provider != provName {
			model = DefaultModelForProvider(provName)
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
		fallback := DefaultModelForProvider(provName)
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
			_ = config.SaveConfig(cfg)
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

	var credFailure error
	if credErr != nil {
		credFailure = &CredentialError{fmt.Errorf("%w; set %s_API_KEY, pass --api-key, or run `terva` and /login",
			credErr, envVarName(provName))}
		if requireCred {
			return Resolved{}, credFailure
		}
	}

	sandbox := tools.NewSandbox(args.CWD)
	if resolveJail(args) {
		sandbox.Lock()
	}
	approval := effectiveApprovalMode(args)
	visionCapable := resolvedModel.Has(provider.CapImageInput)
	// Image generation is opt-in (an `image` config block); the generate_image
	// tool is registered only when a backend resolves. Separate from the chat
	// model's CapImageOutput — this is a tool calling an image backend, gated on
	// backend availability, not on the model emitting images natively.
	imageReg, _ := buildImageRegistry(eff.Config)
	reg := BuildToolRegistry(args, approval, args.CWD, sandbox, provName, method, visionCapable, imageReg)

	docsDir, _ := tervadocs.EnsureInstalled(config.TervaHome())
	// Deployment/setup examples (systemd units, reverse-proxy configs) ride in
	// the binary and install alongside the docs, so an agent bootstrapping a
	// host has them on disk even with no source checkout — and the docs' own
	// ../examples/deploy/… links resolve against them.
	examplesDir, _ := tervaexamples.EnsureInstalled(config.TervaHome())
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
	home := config.TervaHome()
	sandbox.AddReadOnlyRoot(
		docsDir,
		examplesDir,
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
	// Skills are a coding-workflow concept; chat/play modes drop them along
	// with the built-in tools. --no-tools likewise drops the skill tool (it's
	// a tool the model can call), so `--no-tools` means no tools, full stop.
	if !args.NoSkill && !args.NoTools && args.Experience == "" {
		homeDir, _ := os.UserHomeDir()
		// trusted gates the PROJECT skill dirs: an untrusted workspace's
		// .terva|.claude|.agents/skills are skipped so a cloned repo can't
		// inject SKILL.md instructions. Built-in/user/global skills load
		// regardless.
		discovered, _ = skills.Discover(config.TervaHome(), args.CWD, homeDir, args.WithSkills, trusted)
		if len(discovered) > 0 {
			skillTool = skills.NewTool(discovered)
			reg[skillTool.Name()] = skillTool
			skillAddendum = skills.SystemPromptAddendum(discovered)
		}
	}
	_ = skillTool

	summaries := toolSummaries(reg, args)

	var append_ []PromptSegment
	for _, a := range args.AppendSystemPrompt {
		append_ = append(append_, PromptSegment{Source: "user-append", Text: a})
	}
	// Startup context files (project/user config_files, then --context-file
	// flags) inject just before AGENTS.md so repo policy stays the most
	// specific layer. Fail-fast: an explicitly-named missing file is a typo.
	// Config-layer files are ambient coding context: like AGENTS.md below
	// (whose gate drops its user-global layer too), they stay out of
	// chat/play — only an explicit per-run --context-file injects there. A
	// project that wants immersive context ships .terva/lore/ (or a cast),
	// the channels built for it.
	cfgCtxFiles := eff.ContextFiles
	if args.Experience != "" {
		cfgCtxFiles = nil
	}
	if ctxBlock, err := readStartupContextFiles(args.CWD, cfgCtxFiles, args.ContextFiles); err != nil {
		return Resolved{}, err
	} else if ctxBlock != "" {
		append_ = append(append_, PromptSegment{Source: "context-files", Text: ctxBlock})
	}
	// AGENTS.md is repo coding policy; chat/play sessions aren't working in the
	// repo, so don't let a stray AGENTS.md steer a conversation or a roleplay.
	if args.Experience == "" {
		if agentsAddendum := readAgentsContext(args.CWD, config.TervaHome()); agentsAddendum != "" {
			append_ = append(append_, PromptSegment{Source: "agents-md", Text: agentsAddendum})
		}
	}
	if skillAddendum != "" {
		append_ = append(append_, PromptSegment{Source: "skills", Text: skillAddendum})
	}
	// Lore: authored keyed-context entries. Always-on (constant) entries are
	// baked into the cached prefix here, alongside skills + context files;
	// keyword-triggered entries ride Resolved and are injected into the
	// per-turn tail by loreEphemeral. Lore is context, not a tool, so
	// --no-tools does not disable it; --no-lore / config `lore: false` do.
	var loreTriggered []lore.Entry
	var loreConfig lore.Config
	var loreActive []lore.Entry
	if !args.NoLore && (cfg.Lore == nil || *cfg.Lore) {
		loreEntries, lc, _ := lore.Discover(config.TervaHome(), args.CWD, trusted)
		loreConfig = lc
		var constant []lore.Entry
		constant, loreTriggered = lore.Partition(loreEntries)
		loreActive = loreEntries
		if block := lore.PlaceConstant(constant); block != "" {
			append_ = append(append_, PromptSegment{Source: "lore:constant", Text: block})
		}
	}
	// Untrusted workspace with gated content: tell the model the project
	// extensions/skills/context were withheld so it can explain their absence
	// rather than hallucinate them (decision #2 / plan §5). This is a workspace
	// signal — drop it in chat/play, where it's immersion-breaking OOC chrome
	// with no purpose (the agent isn't referencing project files, and the
	// interactive TUI already nudges the user to /trust when content is gated).
	if args.Experience == "" {
		if note := config.RestrictedSystemNote(args.CWD, trusted); note != "" {
			append_ = append(append_, PromptSegment{Source: "restricted-workspace", Text: note})
		}
	}
	// Auto-swarm is a coding-workflow skin: only in a session with base
	// workspace tools (never chat/play/--no-tools). See HasBaseWorkspaceTools.
	// The swarm_spawn tool (Toggle 1: AutoSwarmEnabled) and the proactive nudge
	// (Toggle 2: AutoSwarmNudge) are separate — the addendum rides only when
	// both are on, so a session can keep the tool without the nudge.
	if HasBaseWorkspaceTools(args) && config.AutoSwarmEnabled() && config.AutoSwarmNudgeEnabled() {
		logPersonaRosterTripwire()
		append_ = append(append_, PromptSegment{Source: "auto-swarm", Text: autoSwarmAddendum()})
	}
	// The play director's cast pacing nudge — the twin of the coding proactive
	// nudge, gated by the same AutoSwarmNudge toggle. Only in --play with a cast
	// (--cast and/or a trusted project's .terva/cast.json) and never under
	// --no-tools. The actor_spawn tool itself (and its `actor` enum) is registered
	// regardless; this is just the disposition.
	if CastSkinActive(args) && config.AutoSwarmNudgeEnabled() {
		if refs := MergedCastRefs(args, args.CWD, trusted); len(refs) > 0 {
			append_ = append(append_, PromptSegment{Source: "cast", Text: castAddendum()})
		}
	}
	// Swarm children (--swarm-agent) report back through the auto-swarm
	// recap, which surfaces ONLY the final assistant message — pin the
	// deliverable contract so a child never closes on task housekeeping
	// instead of its findings (plan R2/E.2; the review-crew charters carry
	// the same contract in their own voice, this covers Persona-less
	// children too).
	if args.Mode == ModeSwarmAgent {
		append_ = append(append_, PromptSegment{Source: "swarm-child", Text: SwarmChildAddendum()})
	}

	// The task tools are a built-in coding-workflow capability, folded in from
	// the former terva-tasks extension. Present exactly when the base coding
	// tools are — chat/play/--no-tools drop it with them. The store reads its
	// per-session board through a layered FS (the built-in's own dir over the old
	// extension data dir), so boards written by terva-tasks migrate forward on
	// their next write. A fresh controller per resolve is fine: Rebind reloads
	// the board from disk, so it survives /model and /cd rebuilds.
	var tasksCtrl *tasktool.Controller
	if HasBaseWorkspaceTools(args) {
		fs := tasks.NewLayeredFS(filepath.Join(home, "tasks"), filepath.Join(home, "ext-data", "tasks"))
		tasksCtrl = tasktool.New(tasks.NewStore(fs, "agent"))
		for _, t := range tasksCtrl.Tools() {
			if len(args.Tools) > 0 {
				allowed := false
				for _, n := range args.Tools {
					if n == t.Name() {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}
			reg[t.Name()] = t
		}
		append_ = append(append_, PromptSegment{Source: "tasks", Text: tasksCtrl.Policy()})
	}

	// activate_tools lets the model bring a hidden capability group into the
	// advertised set under lazy tool visibility (retro H2·b). It exists only when
	// lazy mode is on — otherwise every group is already advertised and there is
	// nothing to activate — and only with the base coding tools. Visibility only:
	// it never grants authority (EnableLazyTools + the permission gate keep the
	// full registry callable/gated), so it is classed read-only (no confirm).
	if eff.Config.LazyTools && HasBaseWorkspaceTools(args) {
		reg["activate_tools"] = &tools.ActivateToolsTool{}
	}

	// Custom system prompt resolution order:
	//   1. --system-prompt flag (highest priority; ad-hoc per run)
	//   2. $TERVA_HOME/SYSTEM.md (persistent user override)
	//   3. built-in default (defaultIdentity + defaultGuidelines)
	custom := args.SystemPrompt
	if custom == "" {
		custom = readUserSystemPrompt(config.TervaHome())
	}

	// Resolve the active Persona (--Persona flag → $TERVA_HOME/Persona.md →
	// default_persona config → embedded Mieli). Its name + charter shape the
	// default identity; a --system-prompt/SYSTEM.md Custom prompt still wins
	// (BuildSystemPrompt ignores PersonaName/Charter when Custom is set).
	Persona, err := ResolvePersona(args.Persona)
	if err != nil {
		return Resolved{}, err
	}

	// --card overrides the resolved Persona with an immersive identity built
	// from a Character Card, seeds the opening greeting, and merges the card's
	// character_book onto this run's lore (constant -> prefix, triggered ->
	// tail — the same seam as file lore). ParseArgs already implied --chat.
	var CardGreeting, cardIntroOverride, cardIntroSource, cardPostHistory string
	if args.Card != "" {
		ci, cerr := loadCardIdentity(args.Card, args.Experience, args.Greeting, resolveCardUserName(args, eff.Config))
		if cerr != nil {
			return Resolved{}, cerr
		}
		Persona = ci.Persona
		CardGreeting = ci.greeting
		cardIntroOverride = ci.introOverride
		cardIntroSource = ci.introSource
		cardPostHistory = ci.postHistory
		if !args.NoLore && (cfg.Lore == nil || *cfg.Lore) {
			cc, ct := lore.Partition(ci.lore)
			if blk := lore.PlaceConstant(cc); blk != "" {
				append_ = append(append_, PromptSegment{Source: "card:character_book", Text: blk})
			}
			loreTriggered = append(loreTriggered, ct...)
			loreActive = append(loreActive, ci.lore...)
			// The card's book-level config is primary (the proposal's
			// card-budget-primary rule); a lore.json from another tier fills
			// only the fields the card leaves unset.
			loreConfig = mergeCardLoreConfig(loreConfig, ci.loreCfg)
		}
	}

	// An immersive Persona owns the identity: its charter REPLACES the default
	// coding-assistant intro + conventions, routed through the same Custom path
	// as --system-prompt. An explicit --system-prompt / SYSTEM.md still wins (we
	// only fill an empty custom), so the precedence stays
	// flag > SYSTEM.md > immersive-Persona > additive-Persona > default.
	if c := immersiveCustom(custom, Persona); c != "" {
		custom = c
	}

	// Intro-slot override (ignored when Custom is set). A card supplies it via
	// its system_prompt/framing; a native additive Persona supplies it verbatim
	// via agent_introduction. A run is one or the other, so a non-card Persona's
	// field only applies when there's no card override.
	introOverride, introSource := cardIntroOverride, cardIntroSource
	if introOverride == "" && strings.TrimSpace(Persona.Introduction) != "" {
		introOverride = strings.TrimSpace(Persona.Introduction)
		introSource = "persona:introduction"
	}

	sysSegs := SystemSegments(SystemPromptOpts{
		CWD:              args.CWD,
		Tools:            summaries,
		Custom:           custom,
		Append:           append_,
		TervaDocsDir:     docsDir,
		TervaExamplesDir: examplesDir,
		StatusTool:       reg["terva_status"] != nil,
		PersonaName:      Persona.Name,
		Charter:          Persona.Charter,
		Experience:       args.Experience,
		IntroOverride:    introOverride,
		IntroSource:      introSource,
	})
	sys := joinSegmentTexts(sysSegs)

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
		CredentialErr:            credFailure,
		AccountID:                accountID,
		BaseURL:                  args.BaseURL,
		CWD:                      args.CWD,
		Reasoning:                reasoning,
		Temperature:              temperature,
		Insecure:                 args.Insecure,
		VisionCapable:            visionCapable,
		ImageRegistry:            imageReg,
		ToolRegistry:             reg,
		Tasks:                    tasksCtrl,
		ToolSummary:              summaries,
		SystemPrompt:             sys,
		MaxSteps:                 max,
		MaxOutput:                resolvedModel.MaxOutput,
		Sandbox:                  sandbox,
		SkillTool:                skillTool,
		DisableContextExtensions: eff.Config.DisableContextExtensions,
		DisableExtensions:        eff.Config.DisableExtensions,
		LazyTools:                eff.Config.LazyTools,
		LazyToolActive:           eff.Config.LazyToolActive,
		EngineFeatures:           eff.Config.EngineFeatures,
		EscalateAuto:             eff.Config.Escalation != nil && eff.Config.Escalation.Auto,
		Trusted:                  trusted,
		systemAppend:             append_,
		SystemSegments:           sysSegs,
		systemCustom:             custom,
		Persona:                  Persona,
		toolDescriptions:         descMapFromSummaries(summaries),
		ApprovalMode:             approval,
		Experience:               args.Experience,
		NoTools:                  args.NoTools,
		loreTriggered:            loreTriggered,
		loreConfig:               loreConfig,
		loreActive:               loreActive,
		loreFired:                &loreFiredRecord{},
		CardGreeting:             CardGreeting,
		introOverride:            introOverride,
		introSource:              introSource,
		postHistory:              cardPostHistory,
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
	if spec, ok := ProviderByID[r.Provider]; ok {
		return spec.newClient(r)
	}
	if r.AuthMethod == "oauth" {
		return provider.NewAnthropicOAuthSource(r.credentialSource(), r.BaseURL)
	}
	return provider.NewAnthropic(r.Credential, r.BaseURL)
}

// credentialSource yields the current OAuth access token for this provider,
// refreshing it (and persisting to auth.json) when it has expired. The client
// is constructed ONCE with this source and reads it per request, so a token
// rotation never rebuilds the client or discards its accumulated state (usage
// snapshots, connection pool) — the model that replaced an earlier
// wrap-and-rebuild scheme. Without refresh, long sessions (hours) silently
// fail when the ~1-hour token expires.
//
// Concurrency-safe: the mutex single-flights concurrent turns (bot mode shares
// one client across chats) onto one refresh rather than stampeding the token
// endpoint. It reloads from auth.json each turn so a refresh done by another
// terva process is picked up, and on refresh failure falls through with the
// stale token so the request surfaces a clear provider 401 rather than a bare
// refresh error (the prior wrapper's behavior).
func (r Resolved) credentialSource() provider.CredentialSource {
	tokenProvider := r.Provider
	if tokenProvider == "openai-codex" {
		tokenProvider = "openai"
	}
	var mu sync.Mutex
	return func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		tok, _ := config.RefreshIfExpired(tokenProvider, config.LoadOAuthToken(tokenProvider))
		if tok == nil {
			return "", nil
		}
		return tok.AccessToken, nil
	}
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
	// Retained as well as bound, so NewAgent can hand it to the agent LOOP: the
	// prefix-change guard asks its question from the turn policy, not from a
	// tool. Hosts call SetAsker before NewAgent (the front end exists first), so
	// the agent picks it up; a host that never calls it leaves the agent's Asker
	// nil and the guard simply stays quiet.
	r.asker = a
	bindAsker(r.ToolRegistry, a)
}

// SetEscalator retains the host's model-escalation channel so NewAgent can bind
// it onto the agent loop (rung 3 of the stuck-loop hatch). Mirrors SetAsker;
// unlike the asker it drives no tool, so there is nothing to bind into the
// registry. Nil-safe; hosts with no swap target never call it.
func (r *Resolved) SetEscalator(e core.Escalator) {
	if r == nil {
		return
	}
	r.escalator = e
}

// bindAsker wires the front-end question channel into the
// ask_user_question tool of an arbitrary registry.
//
// It must be called for EVERY registry handed to a live agent, not just the
// first. The asker lives on the tool instance, so a rebuild that re-resolves
// the registry mints a fresh ask_user_question with a nil Asker and silently
// drops the channel — the tool then reports "no interactive channel" for the
// rest of the session. (The confirmer has no such hazard: it lives on the
// long-lived gate, which a rebuild never touches.) Every rebuild path is
// therefore a call site: see wsSession.rebuildTools.
//
// Nil-safe; no-op when the tool isn't present.
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
	// The front end's question channel, when there is one. The prefix-change
	// guard asks from inside the turn policy; hosts with nobody to ask (one-shot
	// runs, swarm children) leave this nil and the guard stays quiet.
	a.Asker = r.asker
	// The model-escalation channel, when the host wired one. Nil (headless, swarm
	// children, or a host with no configured target) leaves rung 3 inert — the
	// stuck-loop detector still nudges.
	a.Escalator = r.escalator
	// Auto-escalate policy (config escalation.auto): swap without asking. Off by
	// default, so a persistent loop prompts first before egressing the transcript.
	a.SetEscalateAuto(r.EscalateAuto)
	// The same read-only registry the permission policy uses, so compaction's
	// executed-actions ledger and `plan` mode agree on what "side-effect-free"
	// means rather than each keeping its own list. Nil here (a host that never
	// adopted one) makes the ledger treat every tool as state-changing, which
	// over-reports rather than under-reports — the right way to be wrong.
	a.ReadOnly = r.readOnlySet
	// The auto_compact knob, read live per threshold check — every agent
	// funnels through here, so the policy is universal (TUI, web, acp,
	// chat, swarm children).
	a.AutoCompactPolicy = config.AutoCompactPolicy
	// Bind the live agent into terva_status so it can report current model,
	// reasoning, and token usage (the registry — and thus the tool — is
	// built before the agent exists). This is only the FALLBACK for direct
	// Execute calls: real dispatches resolve the calling agent from ctx
	// (core.AgentFromContext), so a later NewAgent from the same Resolved
	// (bot mode mints an agent per chat over one shared registry) rebinding
	// this field cannot misattribute another conversation's status.
	if st, ok := r.ToolRegistry["terva_status"].(*tools.StatusTool); ok {
		st.Agent = a
	}
	// Same late-binding fallback for session_inspect's current-session default.
	if si, ok := r.ToolRegistry["session_inspect"].(*tools.SessionInspectTool); ok {
		si.Agent = a
	}
	// Bind the agent's transcript epoch into read so it can dedup re-reads
	// of an unchanged file that is still in context (same late-binding —
	// and same ctx-wins fallback semantics — as terva_status above).
	if rt, ok := r.ToolRegistry["read"].(*tools.ReadTool); ok {
		rt.Epoch = a
	}
	// Lazy tool visibility (retro H2·b): advertise only the core group plus the
	// configured always-active groups; the model brings the rest in on demand
	// with activate_tools (registered above). Every host funnels through here,
	// so the opt-in is universal (TUI, web, acp, chat, swarm children).
	if r.LazyTools {
		a.EnableLazyTools(r.LazyToolActive...)
	}
	// Engine features (docs/proposals/activation-continuation.md stage 3):
	// declared defaults overlaid with the config `engine_features` overrides.
	// Applied unconditionally — a feature's own semantics decide when it
	// matters (activation continuation is inert without lazy tools) — and at
	// this funnel so every host and every headless run resolves identically.
	// The workspace settings surface flips live agents through the same Apply.
	for _, f := range EngineFeatures {
		f.Apply(a, EngineFeatureOn(r.EngineFeatures, f))
	}
	// Lore's per-turn provider scans this run's triggered lore entries
	// against recent messages each turn (nil when lore is off / has no
	// triggered entries). Every agent funnels through NewAgent, so lore is
	// universal; the ext-hook wiring composes extension ephemeral context
	// on top of this.
	a.ContextProvider = r.PerTurnContext(a)
	a.ContextProviderPeek = r.PerTurnContextPeek(a)
	return a
}

// freshTasksRegistry clones r's tool registry with the task tools rebound to a
// fresh, never-persisting controller. Both nil when r carries no task
// controller (chat/play/--no-tools). Only tool names the resolve actually
// registered are replaced, so a --tools allowlist that dropped some task tools
// keeps them dropped in the clone.
func (r Resolved) freshTasksRegistry() (core.Registry, *tasktool.Controller) {
	if r.Tasks == nil {
		return nil, nil
	}
	// Live-only by construction: a nil FileStore can never write, so even a
	// stray Rebind cannot key this board to the owner's session file.
	ctrl := tasktool.New(tasks.NewStore(nil, "agent"))
	reg := make(core.Registry, len(r.ToolRegistry))
	for name, t := range r.ToolRegistry {
		reg[name] = t
	}
	for _, t := range ctrl.Tools() {
		if _, ok := reg[t.Name()]; ok {
			reg[t.Name()] = t
		}
	}
	return reg, ctrl
}

// NewAgentWithFreshTasks constructs a per-conversation agent whose task tools
// are bound to a fresh in-memory board instead of the shared r.Tasks — the
// isolation seam for bot mode's admitted groups. r.Tasks stays bound to the
// owner DM's durable session; the returned controller (nil when tasks are off)
// is what the caller must wire into the agent's ephemeral card and open-work
// gate, and it dies with the agent (group boards are live-only working state).
func (r Resolved) NewAgentWithFreshTasks() (*core.Agent, *tasktool.Controller) {
	reg, ctrl := r.freshTasksRegistry()
	if ctrl == nil {
		return r.NewAgent(), nil
	}
	r.ToolRegistry = reg // r is a value copy; the caller's Resolved keeps the shared registry
	return r.NewAgent(), ctrl
}

// BuildToolRegistry assembles the built-in tool set. visionCapable is
// the active model's image-input verdict (model.Has(CapImageInput)); it
// flows into ReadTool so reading an image file returns inline pixels for
// a vision model but an actionable text result otherwise. Called on
// every agent rebuild, so a /model switch re-derives the verdict.
// HasBaseWorkspaceTools reports whether this session ships the built-in coding
// tool registry (read/write/edit/bash/…). Chat and play (Experience != "") start
// pure — chat is conversation, play acts only through a world extension's tools —
// and --no-tools / --no-workspace-tools drop them too. Auto-swarm keys off this:
// its sub-agents exist to orchestrate that workspace, so the coding skin (the
// swarm_spawn tool + its coding-framed addendum) must not appear when there is no
// workspace to act on. Play gets its own dispatch skin later
// (docs/proposals/agent-dispatch.md); it must NOT inherit the coding one.
func HasBaseWorkspaceTools(args Args) bool {
	return args.Experience == "" && !args.NoTools && !args.NoWorkspaceTools
}

func BuildToolRegistry(args Args, approval core.ApprovalMode, cwd string, sandbox *tools.Sandbox, provName, authMethod string, visionCapable bool, imageReg *imagegen.Registry) core.Registry {
	// chat and play both drop the built-in coding tools (read/write/edit/bash/
	// …): chat is pure conversation, play acts only through a world extension's
	// tools. --no-tools does the same. --no-workspace-tools also drops them, but
	// (unlike the above) leaves extension/MCP/skill tools and the normal identity
	// in place — an agent with its integrations but no host filesystem/shell.
	if !HasBaseWorkspaceTools(args) {
		return core.Registry{}
	}
	all := map[string]core.Tool{
		"read":              &tools.ReadTool{CWD: cwd, Sandbox: sandbox, SupportsVision: visionCapable},
		"write":             &tools.WriteTool{CWD: cwd, Sandbox: sandbox},
		"edit":              &tools.EditTool{CWD: cwd, Sandbox: sandbox},
		"bash":              &tools.BashTool{CWD: cwd, Sandbox: sandbox, Env: map[string]string{"TERVA_HOME": config.TervaHome()}},
		"grep":              &tools.GrepTool{CWD: cwd, Sandbox: sandbox},
		"glob":              &tools.GlobTool{CWD: cwd, Sandbox: sandbox},
		"terva_status":      &tools.StatusTool{Provider: provName, CWD: cwd, AuthMethod: authMethod, BaseURL: args.BaseURL, Build: buildinfo.Get()},
		"session_inspect":   &tools.SessionInspectTool{TervaHome: config.TervaHome(), CWD: cwd},
		"ask_user_question": &tools.AskUserTool{},
	}
	// generate_image is opt-in and only useful with a backend, so it enters
	// the set only when one is configured. It's mutating (not read-only), so
	// it's approval-gated and dropped in plan mode like write/bash.
	if imageReg != nil && imageReg.Len() > 0 {
		all["generate_image"] = &tools.GenerateImageTool{CWD: cwd, Sandbox: sandbox, Registry: imageReg}
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
	if spec, ok := ProviderByID[prov]; ok && spec.envHint != "" {
		return spec.envHint
	}
	return "ANTHROPIC"
}
