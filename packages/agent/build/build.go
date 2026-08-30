package build

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tervadocs "terva.sh/terva"
	tervaexamples "terva.sh/terva/examples"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/agent/worktree"
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
	// ProviderSwitch records that boot could not use the provider config PINS
	// and resolved a different one instead. Nil when nothing was overridden.
	//
	// config.Provider is only ever written by /login, /model, or a repair, so a
	// non-empty value is a CHOICE, not a default — and quietly spending a turn
	// on a different account than the one chosen is not terva's call to make.
	// Resolve still APPLIES the switch (Provider/Model below are the usable
	// pair), because a host with no login flow has nothing better to do with
	// the news; a host that CAN ask hands the choice back instead.
	ProviderSwitch *ProviderSwitch
	AccountID      string // ChatGPT account id (for openai oauth), "" otherwise
	BaseURL        string
	CWD            string
	Reasoning      string
	// ReasoningSet reports the global reasoning level was explicitly chosen
	// (--reasoning flag or config), so it wins over a model's DefaultReasoning.
	// Derived from the RAW value before normalizing: non-empty raw (incl.
	// "off"/"none") ⇒ set.
	ReasoningSet bool
	// ReasoningSummary is the normalized reasoning_summary setting ("auto" |
	// "concise" | "detailed"), or "" when off. Forwarded on every request; only
	// the codex client asks for it, and only where reasoning is itself enabled.
	ReasoningSummary string
	// ShowReasoning displays the model's reasoning summary while the turn runs
	// without recording it. It turns the request flag on by itself, so it works
	// with ReasoningSummary off; see core.Agent.ShowReasoning.
	ShowReasoning bool
	Temperature   *float32
	// ImageOutput carries the resolved native image-output config (from the
	// opt-in native_output block), or nil when off. The core agent forwards it
	// only on a model that advertises CapImageOutput.
	ImageOutput *provider.ImageOutputConfig
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

	// promptOpts is the exact SystemPromptOpts Resolve rendered the system
	// prompt with, captured whole so rebuildSystemPrompt starts from what
	// Resolve used and overwrites ONLY the deliberately-live inputs (tool
	// registry, extension context, persona). Any other field — including
	// one added tomorrow — flows through every rebuild untouched,
	// so the set-at-Resolve-but-dropped-on-rebuild bug class (#399's
	// examples hint) is closed structurally, not field by field.
	promptOpts SystemPromptOpts
	// Persona is the resolved active Persona, captured so rebuildSystemPrompt
	// re-renders with the same identity + charter rather than re-resolving.
	Persona          persona.Persona
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

	// Surface is where this run's output lands — derived from args.Mode at
	// resolve time (SurfaceOf) and captured so rebuildSystemPrompt re-renders
	// the conventions against the same audience. A mid-session rebuild does not
	// move the agent to a different screen.
	Surface Surface

	// Portable mirrors args.Portable (the --portable / --portable=strict worker
	// flag), captured so rebuildSystemPrompt keeps stripping terva's harness-local
	// self-context on a mid-session re-render. Empty for every normal run.
	Portable string

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
	// loreFired records the activation trace of the most recent turn (a shared
	// pointer so value-copies of Resolved agree); the steering surfaces read it
	// via LoreFired(). Allocated in Resolve.
	loreFired *LoreFiredRecord
	// worldLore holds the session's live World lore (Worlds L1) — authored
	// entries scoped to the session, merged into the per-turn lore scan alongside
	// the file/card triggered entries. A shared pointer like note: the workspace
	// writes it on world.lore.* edits and seeds it from persisted meta, the tail
	// closure reads it each turn, so an edit lands next turn with no cache bust
	// (which is also why World constants stay OUT of the cached prefix). Non-nil
	// only for immersive sessions; retained via Resolved.WorldLore().
	worldLore *WorldLoreRecord

	// CardGreeting is the SELECTED greeting (macros substituted), seeded as the
	// active opening assistant message of a fresh session. Empty when no card.
	CardGreeting string

	// CardGreetings is a --card's full opening set — first_mes + every
	// alternate_greeting (macros substituted), in card order — pre-seeded as
	// message-0 swipe variants so the user can swipe between openings. The
	// selected greeting (CardGreeting == CardGreetings[--greeting]) is the active
	// take. Length 1 (or nil) means a single opening, no swipe. Empty when no card.
	CardGreetings []string

	// postHistory is a --card's post_history_instructions (macros + {{original}}
	// resolved), injected by PerTurnContext into the uncached per-turn tail
	// after the lore block. Empty when no card / no PHI.
	postHistory string

	// note holds the session's live author's note (a shared pointer so
	// value-copies of Resolved agree), injected by PerTurnContext into the
	// uncached tail AFTER postHistory. Non-nil only for immersive sessions
	// (allocated in Resolve on Experience != ""), which keeps its tail live so a
	// later note.set takes effect; the workspace retains it via Resolved.Note().
	note *NoteRecord

	// userDesc holds the session's live user-persona description — who the user is
	// in the story — a live tail string (same NoteRecord shape as note), injected
	// by PerTurnContext BEFORE postHistory, framed with userName. Non-nil only for
	// immersive sessions; the workspace retains it via Resolved.User() and updates
	// it on user.bind. userName is the resolved {{user}} name (Args.As > config >
	// "User"), captured so the tail can attribute the description to the right name.
	userDesc *NoteRecord
	userName string
	// userGender and userPronouns are the persona's stated identity, captured from
	// Args and rendered by the tail's user-persona frame: stated → use them, unset
	// → steer away from inventing them.
	userGender   string
	userPronouns string

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
// resolve-time opts plus the deliberately-live inputs: the current tool
// registry (extra tools, extension-tool merge, MCP), the extensions'
// static context contribution, and the persona. The single render path
// for every post-resolve change so the inputs can't drift — everything
// not overwritten here renders exactly as Resolve rendered it.
func (r *Resolved) rebuildSystemPrompt() {
	opts := r.promptOpts
	appendBlocks := opts.Append
	// Durable memory rides the CACHED prefix, deliberately: it is a frozen block
	// refreshed at a session boundary and after a compaction (RefreshMemory),
	// never per write. Read live from the registry's tool so the prompt and the
	// tool can never disagree about what is stored.
	if mem := MemoryBlock(r); strings.TrimSpace(mem) != "" {
		appendBlocks = append(append([]PromptSegment{}, appendBlocks...), PromptSegment{Source: SourceMemory, Text: mem})
	}
	if strings.TrimSpace(r.extensionContext) != "" {
		appendBlocks = append(append([]PromptSegment{}, appendBlocks...), PromptSegment{Source: SourceExtensionContext, Text: r.extensionContext})
	}
	opts.Tools = toolSummariesFromRegistry(r.ToolRegistry, r.toolDescriptions)
	opts.Append = appendBlocks
	opts.StatusTool = r.ToolRegistry["terva_status"] != nil
	opts.PersonaName = r.Persona.Name
	opts.Charter = r.Persona.Charter
	opts.CharterOrigin = r.Persona.Source
	opts.BaseCharter = r.Persona.Inherited
	opts.BaseCharterOrigin = r.Persona.InheritedSource
	r.SystemSegments = SystemSegments(opts)
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
		// Stand down BEFORE the merge, not after. Both orders delete core's
		// memory tool; only this one lets the extension's replace it, because
		// the merge skips any name already in the registry (built-ins win on
		// conflict). Standing down afterwards left the name EMPTY — core gone,
		// the extension's already skipped, and nothing to re-run the merge —
		// so a user with the old extension installed had no memory tool at all.
		//
		// Core defers rather than winning so the transition is safe in EITHER
		// direction: a user who pins the old extension is never broken by
		// upgrading terva, and one who retires it gets core's memory on the
		// next launch. When the extension is gone for good this becomes dead
		// code and comes out.
		r.standDownForExtensionMemory(mgr)
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
	// Same optional-interface trick for the loaded set's identities, so
	// terva_status can name the extensions it is running with. Bound here
	// rather than at tool construction because the tools exist before any
	// extension has loaded — and because this is the one call every surface
	// already makes, so none of them can wire tools and forget it.
	//
	// A closure, not a snapshot: extensions reload and the approval-mode switch
	// re-merges, so a value captured now would go stale silently.
	if is, ok := mgr.(interface {
		Identities() []tools.ExtensionIdentity
	}); ok {
		if st, err := r.ToolRegistry.Get("terva_status"); err == nil {
			if status, isStatus := st.(*tools.StatusTool); isStatus {
				status.SetExtensions(is.Identities)
			}
		}
	}
	if changed {
		// Re-render the system prompt with the merged tool list. Skill
		// addendum + extension static context are preserved by the
		// shared rebuild path.
		r.rebuildSystemPrompt()
	}
}

// ExtToolReadOnly resolves an extension tool's read-only classification: a
// declared authority wins (local-read and local-data are auto-allowable), so a
// network-read tool is not mistaken for a local read even if it also set the
// legacy read_only bool. An empty authority falls back to that bool.
func ExtToolReadOnly(info ExtensionToolInfo) bool {
	if info.Authority != "" {
		return core.IsReadOnlyAuthority(info.Authority)
	}
	return info.ReadOnly
}

// ExtToolRegisters reports whether the merge would admit this tool under mode,
// ignoring name collisions. Exported and shared because a caller that decides
// something on the strength of "an extension supplies X" — the memory
// stand-down is the one that matters — must ask the same question the merge
// will, or it acts on a tool that never arrives.
func ExtToolRegisters(info ExtensionToolInfo, mode core.ApprovalMode) bool {
	return mode != core.ApprovalPlan || ExtToolReadOnly(info)
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
		readOnly := ExtToolReadOnly(info)
		if !ExtToolRegisters(info, mode) {
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
	// Essential carries the source's load-bearing declaration (extension
	// register_tool essential): the tool stays advertised every turn even
	// under lazy tool visibility, rather than deferring behind activate_tools
	// with the rest of its group. Extension-only; MCP tools leave it false.
	Essential bool
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
	if spec, ok := specFor(prov); ok {
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
// the registry (provider_registry.go). The ordered id list (ProviderIDs)
// and the providerAliases map are derived there too.
func IsKnownProvider(name string) bool {
	_, ok := specFor(name)
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
	if canon, ok := aliasFor(n); ok {
		return canon
	}
	return n
}

// ProviderSwitch describes a boot that could not use the configured provider
// and resolved a different one instead.
//
// 🪤 It is recorded rather than recomputed, and that is load-bearing. The
// lapse that matters most — a refresh_token the SERVER rejects (invalid_grant)
// — is invisible to any cheap presence check: the token is right there on disk
// and `hasCredential` says yes. Only an actual refresh ATTEMPT discovers it,
// which is exactly what the fallback scan's ResolveCredentialFull already did.
// Asking again later means either a second network round-trip and auth.json
// write per session, or a wrong answer.
type ProviderSwitch struct {
	// From/FromModel is what config pins — the pair the user chose.
	From, FromModel string
	// To/ToModel is what boot resolved instead, on a credential it PROVED
	// usable rather than merely present.
	To, ToModel string
	// Err is why From was unusable (an *ExpiredLoginError for a lapsed
	// subscription; a plain "no credential" otherwise).
	Err error
}

// Lapsed reports whether the pin failed because its login EXPIRED, as opposed
// to never having been configured. Only the former is worth offering a switch
// over: the user had this working, so "log in again" is a real remedy standing
// beside "use the other provider just this once".
func (s *ProviderSwitch) Lapsed() bool {
	if s == nil {
		return false
	}
	var expired *ExpiredLoginError
	return errors.As(s.Err, &expired)
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
	trusted := permissions.ResolveTrustState(args.CWD, args.Trust).IsTrusted()
	// A fixed-trust host pins the verdict so a re-resolve cannot read a store
	// that moved under it and hand back a tool set gated differently from the
	// extensions and hooks the process is actually running. See Args.TrustPin.
	if args.TrustPin != nil {
		trusted = *args.TrustPin
	}

	// THE RULE, and it is one sentence: read through eff.Config, write through
	// cfg. Nothing below reads a setting off cfg to decide behaviour.
	//
	// eff.Config is the layered project-over-user view; cfg is the pristine,
	// writable user layer, and it exists here only so the model-repair paths
	// persist to the USER config rather than writing a project's value back
	// into someone's home.
	//
	// This comment used to say the opposite — "every existing read/repair below
	// stays on cfg" — and was already false when written: Provider, Model,
	// LazyToolsOn, EngineFeatures and Escalation all read eff.Config. The two
	// views were interchangeable for everything ResolveConfig does not layer,
	// so ~15 reads picked one arbitrarily and nothing noticed.
	//
	// What that cost, latently: ResolveConfig layers seven fields today. Add an
	// eighth — "pin the thinking level per repo" is the obvious request — and
	// every read still on cfg silently ignores the project's value. No error,
	// no warning, and build tests seed the USER config, so nothing fails.
	//
	// Whether a project may override a setting is therefore decided in exactly
	// one place, config.ResolveConfig, where the trust gate already lives.
	// TestResolveReadsSettingsThroughTheLayeredView holds this.
	eff := config.ResolveConfig(args.CWD, trusted)
	cfg := eff.User // repairs only — see the rule above

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
		// Reset the saved config so this doesn't keep happening. Only these two
		// fields: cfg is this run's in-memory copy and writing all of it back
		// would undo whatever another instance changed while we were resolving.
		cfg.Provider = provName
		cfg.Model = ""
		_ = config.MutateConfig(func(c *config.Config) { c.Provider, c.Model = provName, "" })
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
	} else if ep, ok := eff.Config.Endpoints[provName]; ok {
		// A user-defined named OpenAI-compatible endpoint: like
		// openai-compatible, but its base URL + default context come from the
		// config entry, and its Key (if any) resolves from APIKeyEnv/auth.json.
		// Keyless local servers fall back to the harmless sentinel bearer.
		compatCtx = ep.ContextWindow
		compatBaseURL = ep.BaseURL
		cred, method = endpointCredential(provName, args.APIKey)
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
	// Whether this provider is a PIN — the user's own choice — or merely the
	// built-in default nobody selected. config.Provider is written only by
	// /login, /model, or a repair, so a non-empty value means chosen; an empty
	// one leaves provName at "anthropic" by default, which no one picked.
	// Overriding a default is housekeeping; overriding a choice is a decision
	// the user should get to make, so the two are recorded differently.
	pinnedProvider := !userPickedProvider && canonicalProvider(eff.Config.Provider) != ""
	// The failure that made the pin unusable, captured before the scan below
	// overwrites credErr with the replacement's (nil) one.
	pinnedErr := credErr
	var switched *ProviderSwitch
	if credErr != nil && !userPickedProvider && provName != "ollama" {
		// Scan every known provider (not a hardcoded subset) so any
		// env-based credential is discovered, e.g. an env-only
		// amazon-bedrock setup (AWS_BEARER_TOKEN_BEDROCK / AWS_PROFILE /
		// IAM keys) when no config.json pins the provider, such as after
		// pointing TERVA_HOME at a fresh home dir. Iteration order of
		// ProviderIDs defines fallback priority. ollama is skipped:
		// it has no credential and would always "match".
		for _, other := range ProviderIDs() {
			if other == provName || other == "ollama" || other == "openai-compatible" {
				continue
			}
			// A named endpoint is reachable WITHOUT a credential — that is the
			// whole point of pointing terva at your own server. Asking
			// ResolveCredentialFull about a keyless one gets "no credential", so
			// the scan used to walk straight past the only backend the operator
			// could actually reach and report "no credential for anthropic".
			// Endpoints sort after the built-ins in ProviderIDs, so this is the
			// last resort it should be.
			if ep, ok := eff.Config.Endpoints[other]; ok {
				// Only if it can actually run something. An endpoint whose models
				// have not been discovered has no model id to offer, and falling
				// back onto it would trade "not logged in" — which boots, and
				// which /login can fix — for a hard refusal to start at all.
				if EndpointDefaultModel(other) == "" {
					continue
				}
				if pinnedProvider {
					switched = &ProviderSwitch{From: provName, FromModel: eff.Config.Model, Err: pinnedErr}
				}
				provName = other
				cred, method = endpointCredential(other, args.APIKey)
				accountID, credErr = "", nil
				compatCtx, compatBaseURL = ep.ContextWindow, ep.BaseURL
				break
			}
			if c, m, a, err := ResolveCredentialFull(other, args.APIKey); err == nil {
				if pinnedProvider {
					switched = &ProviderSwitch{From: provName, FromModel: eff.Config.Model, Err: pinnedErr}
				}
				provName = other
				cred, method, accountID, credErr = c, m, a, err
				break
			}
		}
	}

	// ollama and openai-compatible are open-catalogue: the model id is
	// whatever the local/custom server understands and has no baked-in
	// catalog entry or default.
	openCatalogue := provName == "ollama" || provName == "openai-compatible" || IsEndpointProvider(provName, cfg)
	// --model flag > project (trusted) > user config (eff.Config is the
	// project-over-user read view; cfg stays the user layer for repairs).
	model := firstNonEmpty(args.Model, eff.Config.Model)
	// A hidden model that is ALSO the configured default is a contradiction only
	// the user can settle, so say so rather than guess which half they meant.
	//
	// Scoped deliberately to the CONFIG default. An explicit --model is exempt:
	// naming a model is a clearer statement of intent than a visibility rule
	// written earlier, and refusing it would leave a hidden model unreachable
	// from the command line for no gain. A resume is exempt for the same reason
	// mechanically — it re-enters Resolve with args.Model set from the session —
	// and that matters more, because failing to reopen a conversation over a
	// picker preference would be destructive out of all proportion. The built-in
	// per-provider fallback is exempt too: nobody chose it, so erroring on it
	// would brick a launch over a config the user never wrote.
	if args.Model == "" && model != "" && model == eff.Config.Model {
		if hidden, rule := config.NewModelVisibility(eff.Config.HiddenModels).HiddenBy(provName, model); hidden {
			return Resolved{}, fmt.Errorf(
				"model %q is your configured default but is hidden by the rule %q.\n"+
					"Un-hide it in the model picker (ctrl+k, or type :hidden to reveal hidden models), "+
					"choose a different default, or edit hidden_models in %s.\n"+
					"To use it just this once, pass --model %s.",
				config.ModelKey(provName, model), rule, config.ConfigPath(), model)
		}
	}
	if model == "" {
		switch {
		case provName == "ollama":
			return Resolved{}, fmt.Errorf("ollama requires --model (e.g. --model llama3)")
		case provName == "openai-compatible":
			return Resolved{}, fmt.Errorf("openai-compatible requires a model; set it during /login or pass --model")
		case IsEndpointProvider(provName, cfg):
			// A named endpoint has no baked-in default: DefaultModelForProvider
			// returns "" for it, and an empty model id is not a harmless
			// placeholder — it is serialized straight into the request body, and
			// the server rejects the turn. Take what discovery found instead.
			model = EndpointDefaultModel(provName)
			if model == "" {
				return Resolved{}, fmt.Errorf("endpoint %q has no model yet; pick one with /model or pass --model (terva lists an endpoint's models from its /v1/models)", provName)
			}
		default:
			model = DefaultModelForProvider(provName)
		}
	}
	// If the model belongs to a different provider (e.g. config says gpt-5 but
	// we fell back to anthropic), the pair is incoherent — take that provider's
	// default instead.
	//
	// "Belongs to a different provider" has to be asked of THIS provider first.
	// A bare FindModel returns the catalogue's first match, and plenty of ids
	// are listed under several providers: gpt-5-pro and gpt-5-codex sit under
	// azure-openai-responses before openai. Asking bare, the answer for
	// openai/gpt-5-pro was "that is an azure model", and a session that
	// explicitly named it was silently moved to openai's default — at launch,
	// and again on every rebuild after a switch to one. It is the same
	// provider-hop the /model verb guards against (switchModel resolves a bare
	// id against the current provider first); Resolve knows the provider
	// outright, so it never needed to guess.
	if !openCatalogue {
		if _, err := provider.FindModel(provName, model); err != nil {
			if m, err := provider.FindModel("", model); err == nil && m.Provider != provName {
				repaired := DefaultModelForProvider(provName)
				// Say so, unless a provider switch is already being reported —
				// that notice names both halves of the move, and a second line
				// about the model would describe the same event twice.
				//
				// Silent until now, and it is the visible half of the failure:
				// the chrome read "(openai) gpt-5" for someone who had chosen
				// claude-opus-5, with nothing anywhere connecting the two. The
				// sibling repair below (id not in the catalogue at all) has
				// warned on stderr all along; this one never did.
				if switched == nil && model != repaired {
					fmt.Fprintf(os.Stderr,
						"terva: model %q belongs to %q, not %q; using %q instead. Pick a different model with --model or /model.\n",
						model, m.Provider, provName, repaired)
				}
				model = repaired
			}
		}
	}
	resolvedModel, err := provider.FindModel(provName, model)
	if err != nil && openCatalogue {
		// Any model id the local/custom server understands is valid,
		// even if not in the baked-in catalog. For openai-compatible,
		// prefer the user's configured default context window over the
		// generic guess so auto-compaction and the context % are sane.
		// compatCtx is the window the operator configured for THIS backend —
		// the shared slot's from auth.json, a named endpoint's from its config
		// entry. Either way it beats the generic guess, and using it is what
		// keeps auto-compaction and the context gauge honest.
		ctxWin := 32768
		if compatCtx > 0 && (provName == "openai-compatible" || IsEndpointProvider(provName, cfg)) {
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
			// The guard re-runs inside the lock: another instance may have
			// changed the model since, and repairing THEIR choice to ours is
			// the lost update this whole path exists to avoid.
			_ = config.MutateConfig(func(c *config.Config) {
				if c.Model == model {
					c.Model = fm.ID
				}
			})
		}
		resolvedModel = fm
		model = fm.ID
	}

	// The destination half of a provider switch, filled once the model is
	// final — the fallback picks the PROVIDER, and the model that goes with it
	// is only known after the repair above has run.
	if switched != nil {
		switched.To, switched.ToModel = provName, model
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

	// Credentials are optional only where the ENDPOINT is one terva reaches
	// without them: ollama, an openai-compatible login, a named endpoint, or a
	// models.json entry whose baseUrl pins a server the user runs.
	//
	// "The model has a base URL" was far too wide a test for that. Nearly every
	// BUILT-IN catalog row carries one — it is how kimi, deepseek, fireworks,
	// minimax and the rest name their HOSTED api — so a missing or lapsed
	// credential for any of them resolved to the sentinel below and shipped the
	// literal string "ollama" as the API key. What came back was a 401 from the
	// vendor, and the "not logged in" the user should have been shown at
	// resolution time never appeared at all.
	keyless := openCatalogue || resolvedModel.Source == "user"
	if keyless && resolvedModel.BaseURL != "" && credErr != nil {
		cred = "ollama"
		credErr = nil
		requireCred = false
	}

	var credFailure error
	if credErr != nil {
		// A LAPSED login keeps its own words. noCredentialError's two phrasings
		// both describe an account that was never configured — "set KIMI_API_KEY",
		// "no credentials found for any provider" — which is the wrong errand to
		// send someone on when the credential is right there on disk and only the
		// grant has expired. The remedy is one /login, and saying so is the
		// difference between a 30-second fix and hunting an API key that was
		// never the mechanism.
		var expired *ExpiredLoginError
		if errors.As(credErr, &expired) {
			credFailure = &CredentialError{credErr}
		} else {
			credFailure = &CredentialError{noCredentialError(provName, userPickedProvider)}
		}
		if requireCred {
			return Resolved{}, credFailure
		}
	}

	sandbox := tools.NewSandbox(args.CWD)
	perms := args.PermInputs()
	if permissions.ResolveJail(perms) {
		sandbox.Lock()
	}
	approval := permissions.EffectiveApprovalMode(perms)
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
	home := config.TervaHome()
	// docsDir/examplesDir no longer need a grant: a jailed agent may read
	// anywhere except the secret roots registered here. config.json comes off
	// that list only when it holds no plaintext secret — decided once here,
	// for the life of this session's sandbox.
	configReadable, _ := ConfigReadableByAgent(args.CWD)
	restrictSensitiveReads(sandbox, home, args.CWD, configReadable)
	allowHandoffWrites(sandbox, home)

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
		discovered, _ = skills.Discover(config.TervaHome(), args.CWD, homeDir, args.WithSkills, !args.NoBuiltinSkills,
			skills.Gate{TrustProject: trusted, Disabled: eff.Config.DisableExtensions})
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
		append_ = append(append_, PromptSegment{Source: SourceUserAppend, Text: a})
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
	if ctxBlock, ctxOrigin, err := readStartupContextFiles(args.CWD, cfgCtxFiles, args.ContextFiles); err != nil {
		return Resolved{}, err
	} else if ctxBlock != "" {
		append_ = append(append_, PromptSegment{Source: SourceContextFiles, Text: ctxBlock, Origin: ctxOrigin})
	}
	// AGENTS.md is repo coding policy; chat/play sessions aren't working in the
	// repo, so don't let a stray AGENTS.md steer a conversation or a roleplay.
	if args.Experience == "" {
		if agentsAddendum, agentsOrigin := readAgentsContext(args.CWD, config.TervaHome()); agentsAddendum != "" {
			append_ = append(append_, PromptSegment{Source: SourceAgentsMD, Text: agentsAddendum, Origin: agentsOrigin})
		}
	}
	if skillAddendum != "" {
		append_ = append(append_, PromptSegment{Source: SourceSkills, Text: skillAddendum})
	}
	// Lore: authored keyed-context entries. Always-on (constant) entries are
	// baked into the cached prefix here, alongside skills + context files;
	// keyword-triggered entries ride Resolved and are injected into the
	// per-turn tail by loreEphemeral. Lore is context, not a tool, so
	// --no-tools does not disable it; --no-lore / config `lore: false` do.
	var loreTriggered []lore.Entry
	var loreConfig lore.Config
	var loreActive []lore.Entry
	if !args.NoLore && (eff.Config.Lore == nil || *eff.Config.Lore) {
		loreEntries, lc, _ := lore.Discover(config.TervaHome(), args.CWD,
			lore.Gate{TrustProject: trusted, Disabled: eff.Config.DisableExtensions})
		loreConfig = lc
		var constant []lore.Entry
		constant, loreTriggered = lore.Partition(loreEntries)
		loreActive = loreEntries
		if block := lore.PlaceConstant(constant); block != "" {
			append_ = append(append_, PromptSegment{Source: SourceLoreConstant, Text: block})
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
			append_ = append(append_, PromptSegment{Source: SourceRestrictedWorkspace, Text: note})
		}
	}
	// Auto-swarm is a coding-workflow skin: only in a session with base
	// workspace tools (never chat/play/--no-tools). See HasBaseWorkspaceTools.
	// The swarm_spawn tool (Toggle 1: AutoSwarmEnabled) and the proactive nudge
	// (Toggle 2: AutoSwarmNudge) are separate — the addendum rides only when
	// both are on, so a session can keep the tool without the nudge.
	if HasBaseWorkspaceTools(args) && config.AutoSwarmEnabled() && config.AutoSwarmNudgeEnabled() {
		logPersonaRosterTripwire()
		append_ = append(append_, PromptSegment{Source: SourceAutoSwarm, Text: autoSwarmAddendum()})
	}
	// Where the sub-agents' files land. Gated on the swarm TOOL (Toggle 1), not
	// on the nudge: this is not a disposition the user can find pushy, it is a
	// fact about the filesystem the coordinator is about to reason over, and a
	// coordinator that spawns without the nudge needs it exactly as much. Gated
	// on isolation actually being on, because with a shared tree every sentence
	// in it is false.
	if HasBaseWorkspaceTools(args) && config.AutoSwarmEnabled() && SwarmWorktreesActive(args) {
		append_ = append(append_, PromptSegment{Source: SourceSwarmWorktrees, Text: SwarmWorktreeAddendum()})
	}
	// The play director's cast pacing nudge — the twin of the coding proactive
	// nudge, gated by the same AutoSwarmNudge toggle. Only in --play with a cast
	// (--cast and/or a trusted project's .terva/cast.json) and never under
	// --no-tools. The actor_spawn tool itself (and its `actor` enum) is registered
	// regardless; this is just the disposition.
	if CastSkinActive(args) && config.AutoSwarmNudgeEnabled() {
		if refs := MergedCastRefs(args, args.CWD, trusted); len(refs) > 0 {
			append_ = append(append_, PromptSegment{Source: SourceCast, Text: castAddendum()})
		}
	}
	// Swarm children (--swarm-agent) report back through the auto-swarm
	// recap, which surfaces ONLY the final assistant message — pin the
	// deliverable contract so a child never closes on task housekeeping
	// instead of its findings (plan R2/E.2; the review-crew charters carry
	// the same contract in their own voice, this covers Persona-less
	// children too).
	if args.Mode == mode.SwarmAgent {
		append_ = append(append_, PromptSegment{Source: SourceSwarmChild, Text: SwarmChildAddendum()})
		// A schema-carrying spawn (the structured-deliverable contract)
		// gets the deliver_result tool bound to its schema plus the
		// addendum requiring it. Validation happens at the tool layer so
		// the model fixes and retries in-turn — the supervisor re-validates
		// what lands on disk regardless (swarm.captureDeliverable). Not
		// filtered by --tools: the dispatcher asked for the contract, so
		// the tool must exist.
		if len(args.DeliverableSchema) > 0 {
			reg["deliver_result"] = &tools.DeliverResultTool{ArgSchema: args.DeliverableSchema}
			append_ = append(append_, PromptSegment{Source: SourceSwarmChild, Text: DeliverResultAddendum()})
		}
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
		append_ = append(append_, PromptSegment{Source: SourceTasks, Text: tasksCtrl.Policy()})
	}

	// activate_tools lets the model bring a hidden capability group into the
	// advertised set under lazy tool visibility (retro H2·b). It exists only when
	// lazy mode is on — otherwise every group is already advertised and there is
	// nothing to activate — and only with the base coding tools. Visibility only:
	// it never grants authority (EnableLazyTools + the permission gate keep the
	// full registry callable/gated), so it is classed read-only (no confirm).
	// lazyVisibilityEngages keys on this registration: no reveal path, no
	// hiding — a session outside this condition never gets EnableLazyTools.
	if eff.Config.LazyToolsOn() && HasBaseWorkspaceTools(args) {
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
	active, err := persona.Resolve(args.Persona)
	if err != nil {
		return Resolved{}, err
	}

	// --card overrides the resolved Persona with an immersive identity built
	// from a Character Card, seeds the opening greeting, and merges the card's
	// character_book onto this run's lore (constant -> prefix, triggered ->
	// tail — the same seam as file lore). ParseArgs already implied --chat.
	var CardGreeting, cardIntroOverride, cardIntroSource, cardPostHistory string
	var cardGreetings []string
	if args.Card != "" {
		// The reference may be a filesystem path (classic --card) or a library id
		// (a session spawned from the card store); resolve either to a loadable
		// card document.
		cardPath, rerr := ResolveCardRef(args.Card)
		if rerr != nil {
			return Resolved{}, fmt.Errorf("--card: %w", rerr)
		}
		ci, cerr := loadCardIdentity(cardPath, args.Experience, args.Greeting, resolveCardUserName(args, eff.Config))
		if cerr != nil {
			return Resolved{}, cerr
		}
		active = ci.Persona
		CardGreeting = ci.greeting
		cardGreetings = ci.greetings
		cardIntroOverride = ci.introOverride
		cardIntroSource = ci.introSource
		cardPostHistory = ci.postHistory
		if !args.NoLore && (eff.Config.Lore == nil || *eff.Config.Lore) {
			cc, ct := lore.Partition(ci.lore)
			if blk := lore.PlaceConstant(cc); blk != "" {
				append_ = append(append_, PromptSegment{Source: SourceCardCharacterBook, Text: blk})
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
	if c := persona.ImmersiveCustom(custom, active); c != "" {
		custom = c
	}

	// Intro-slot override (ignored when Custom is set). A card supplies it via
	// its system_prompt/framing; a native additive Persona supplies it verbatim
	// via agent_introduction. A run is one or the other, so a non-card Persona's
	// field only applies when there's no card override.
	introOverride, introSource := cardIntroOverride, cardIntroSource
	if introOverride == "" && strings.TrimSpace(active.Introduction) != "" {
		introOverride = strings.TrimSpace(active.Introduction)
		introSource = "persona:introduction"
	}

	// Captured whole on Resolved (promptOpts) so rebuildSystemPrompt starts
	// from exactly this and overwrites only its deliberately-live inputs.
	sysOpts := SystemPromptOpts{
		CWD:               args.CWD,
		Tools:             summaries,
		Custom:            custom,
		Append:            append_,
		TervaDocsDir:      docsDir,
		TervaExamplesDir:  examplesDir,
		StatusTool:        reg["terva_status"] != nil,
		PersonaName:       active.Name,
		Charter:           active.Charter,
		CharterOrigin:     active.Source,
		BaseCharter:       active.Inherited,
		BaseCharterOrigin: active.InheritedSource,
		Experience:        args.Experience,
		Surface:           SurfaceOf(args.Mode),
		Portable:          args.Portable,
		IntroOverride:     introOverride,
		IntroSource:       introSource,
	}
	sysSegs := SystemSegments(sysOpts)
	sys := joinSegmentTexts(sysSegs)

	// Reasoning fall-through: --reasoning / session override > per-model
	// (models.json `defaultReasoning`) > global config > the model's CATALOG
	// default > off. The last two rungs live in provider.EffectiveReasoning,
	// which sees the model; the first three are resolved here, where the
	// explicit level and the global one are still distinguishable.
	//
	// 🪤 Only an OPERATOR's per-model value sits above the global setting.
	// A catalog DefaultReasoning must stay below it — the k3 rows carry one so
	// the endpoint stops downgrading to K2, and yielding to an explicit global
	// choice is exactly what their comment promises. Reading the raw field here
	// instead of DefaultReasoningSet would silently make a global "low"
	// unreachable on those models. See Model.DefaultReasoningSet.
	//
	// The set-signal is the RAW value (before normalizing): a non-empty raw
	// level — including "off"/"none", which normalize to "" — is an explicit
	// choice. Empty raw ⇒ unset ⇒ fall through to the catalog default.
	rawReasoning := resolveRawReasoning(args.Reasoning, resolvedModel, eff.Config.Reasoning)
	reasoning := provider.NormalizeReasoning(rawReasoning)
	reasoningSet := rawReasoning != ""
	// Temperature fall-through: --temperature flag > per-model (models.json) >
	// global config default. AdaptiveThinking models reject sampling params,
	// so temperature is never sent for them regardless of the layers.
	temperature := args.Temperature
	if temperature == nil {
		temperature = resolvedModel.Temperature
	}
	if temperature == nil {
		temperature = eff.Config.Temperature
	}
	if resolvedModel.AdaptiveThinking {
		temperature = nil
	}

	// Native image output (opt-in native_output block). Carry the config when
	// enabled; the per-model CapImageOutput gate is applied per-turn in the
	// core agent so a model swap toggles it without a rebuild.
	var imageOutput *provider.ImageOutputConfig
	if eff.Config.NativeOutput.IsEnabled() {
		imageOutput = &provider.ImageOutputConfig{
			Size:        eff.Config.NativeOutput.Size,
			Quality:     eff.Config.NativeOutput.Quality,
			EditHistory: eff.Config.NativeOutput.EditHistoryOr(1),
		}
	}

	max := args.MaxSteps // 0 = unlimited

	return Resolved{
		Provider:                 provName,
		Model:                    model,
		Credential:               cred,
		AuthMethod:               method,
		CredentialErr:            credFailure,
		ProviderSwitch:           switched,
		AccountID:                accountID,
		BaseURL:                  args.BaseURL,
		CWD:                      args.CWD,
		Reasoning:                reasoning,
		ReasoningSet:             reasoningSet,
		ReasoningSummary:         provider.NormalizeReasoningSummary(eff.Config.ReasoningSummaryMode()),
		ShowReasoning:            eff.Config.ShowReasoning,
		Temperature:              temperature,
		ImageOutput:              imageOutput,
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
		LazyTools:                eff.Config.LazyToolsOn(),
		LazyToolActive:           eff.Config.LazyToolActive,
		EngineFeatures:           eff.Config.EngineFeatures,
		EscalateAuto:             eff.Config.Escalation != nil && eff.Config.Escalation.Auto,
		Trusted:                  trusted,
		promptOpts:               sysOpts,
		SystemSegments:           sysSegs,
		Persona:                  active,
		toolDescriptions:         descMapFromSummaries(summaries),
		ApprovalMode:             approval,
		Experience:               args.Experience,
		Surface:                  SurfaceOf(args.Mode),
		Portable:                 args.Portable,
		NoTools:                  args.NoTools,
		loreTriggered:            loreTriggered,
		loreConfig:               loreConfig,
		loreActive:               loreActive,
		loreFired:                &LoreFiredRecord{},
		worldLore:                newWorldLoreRecord(args.Experience != ""),
		CardGreeting:             CardGreeting,
		CardGreetings:            cardGreetings,
		postHistory:              cardPostHistory,
		note:                     newNoteRecord(args.Experience != ""),
		userDesc:                 newNoteRecord(args.Experience != ""),
		userName:                 resolveCardUserName(args, eff.Config),
		userGender:               strings.TrimSpace(args.UserGender),
		userPronouns:             strings.TrimSpace(args.UserPronouns),
	}, nil
}

// newNoteRecord allocates a live author's-note record for an immersive session
// (which keeps its per-turn tail live so a later note.set injects), or nil for a
// coding session, which has no author's note and no note tail.
func newNoteRecord(immersive bool) *NoteRecord {
	if !immersive {
		return nil
	}
	return &NoteRecord{}
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
//
// It returns the rendered block and the paths that went into it, in order —
// the segment's Origin. The block names them in prose already; returning them
// as paths is what lets the dump list them and lets a foreign worker be pointed
// at the files instead of pasted their contents.
func readAgentsContext(cwd, tervaHome string) (string, []string) {
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
		return "", nil
	}
	var sb strings.Builder
	origin := make([]string, 0, len(files))
	sb.WriteString("Project context instructions loaded from AGENTS.md files. Follow them when working in this repository. Later files are more specific and may override earlier ones.\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "\n## %s\n\n%s\n", f.path, f.content)
		origin = append(origin, f.path)
	}
	return strings.TrimSpace(sb.String()), origin
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

// clientConfig is how Resolved presents itself to a registry entry.
func (r Resolved) clientConfig() clientConfig {
	return clientConfig{
		Provider:   r.Provider,
		Credential: r.Credential,
		BaseURL:    r.BaseURL,
		AuthMethod: r.AuthMethod,
		AccountID:  r.AccountID,
	}
}

func (r Resolved) dispatchClient() provider.Client {
	if spec, ok := specFor(r.Provider); ok {
		return spec.newClient(r.clientConfig())
	}
	if r.AuthMethod == "oauth" {
		return provider.NewAnthropicOAuthSource(r.clientConfig().credentialSource(), r.BaseURL)
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

// UseTasks replaces this resolve's task controller with a caller-owned one and
// re-points the registered task tools at it — the sandbox treatment for the task
// board, and for the same reason.
//
// Every Resolve mints a fresh controller over a fresh store, which is right for
// a NEW session and wrong for a rebuild: the host's pointer (the pane, the
// status glance) and the agent's per-turn task card are both bound to the
// controller from session build, while a rebuild would install tools writing
// somewhere else. The model then records work that nothing renders — and, worse,
// that its own card no longer shows, so it cannot see the list it is working
// from. One board per session, surviving every rebuild, is the only arrangement
// where those three agree.
//
// Only names the resolve actually registered are replaced, so a --tools
// allowlist or a plan-mode withdrawal that dropped a task tool keeps it dropped.
func (r *Resolved) UseTasks(ctrl *tasktool.Controller) {
	if r == nil || ctrl == nil {
		return
	}
	r.Tasks = ctrl
	for _, t := range ctrl.Tools() {
		if _, ok := r.ToolRegistry[t.Name()]; ok {
			r.ToolRegistry[t.Name()] = t
		}
	}
}

// UseFiles keeps a session's file-state tracker across a tool rebuild.
//
// Same rule as UseTasks and UseMemory. A rebuild fires on any extension policy
// assertion and on entering plan mode; a fresh tracker there would forget every
// file the model has read, and the edit tool's staleness note would then tell it
// "this session has not read x.go" about a file it read a minute earlier —
// confidently wrong, which is worse than silent.
func (r *Resolved) UseFiles(fs *tools.FileState) {
	if r == nil || fs == nil || r.ToolRegistry == nil {
		return
	}
	if rt, ok := r.ToolRegistry["read"].(*tools.ReadTool); ok {
		rt.Files = fs
	}
	if wt, ok := r.ToolRegistry["write"].(*tools.WriteTool); ok {
		wt.Files = fs
	}
	if et, ok := r.ToolRegistry["edit"].(*tools.EditTool); ok {
		et.Files = fs
	}
}

// Files returns this resolve's shared file-state tracker, or nil when the
// registry has no file tools (chat/play/--no-tools). The accessor exists so a
// host can hand the SAME tracker to the next rebuild without reaching into the
// registry itself.
func (r *Resolved) Files() *tools.FileState {
	if r == nil || r.ToolRegistry == nil {
		return nil
	}
	if rt, ok := r.ToolRegistry["read"].(*tools.ReadTool); ok {
		return rt.Files
	}
	return nil
}

// UseMemory keeps a session's long-lived memory stores across a tool rebuild.
//
// Same rule as UseTasks, and the same reason: the injected block, the /memory
// pane, and the model's own writes all read the stores bound at session build.
// Adopting a rebuild's freshly-minted ones would point the tool at stores
// nothing else renders — memory would appear to work, and every write would
// land where no surface is looking. A rebuild fires on any extension policy
// assertion and on entering plan mode, so this is not a corner case.
func (r *Resolved) UseMemory(mt *tools.MemoryTool) {
	if r == nil || mt == nil {
		return
	}
	if _, ok := r.ToolRegistry["memory"]; ok {
		r.ToolRegistry["memory"] = mt
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
	a.ReasoningSet = r.ReasoningSet
	a.ReasoningSummary = r.ReasoningSummary
	a.ShowReasoning = r.ShowReasoning
	a.Temperature = r.Temperature
	a.ImageOutput = r.ImageOutput
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
	// The directory bash runs in, for the ledger's `cd`-to-nowhere elision. Same
	// args.CWD BuildToolRegistry hands BashTool, so the two cannot drift into
	// eliding a `cd` that actually moved the command.
	a.CWD = r.CWD
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
	// so the opt-in is universal (TUI, web, acp, chat, swarm children) — but it
	// engages only where the reveal path exists (lazyVisibilityEngages): hiding
	// groups in a session that never registered activate_tools (chat, play,
	// --no-tools, --no-workspace-tools) would bury extension and world tools
	// with no way for the model to bring them back. Play acts ONLY through
	// world-extension tools, so that is a hard break, not a degradation.
	if lazyVisibilityEngages(&r) {
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

// extraBuiltinTools holds optional built-in constructors contributed by
// build-tag gated features; empty in a default build. Appended from init()
// in a tag's _on file (see scripting_on.go), so ordering is package-init
// and the slice is never mutated after startup.
var extraBuiltinTools []func(cwd string, sandbox *tools.Sandbox) (string, core.Tool)

// BuildToolRegistry assembles the built-in tool set. visionCapable is
// the active model's image-input verdict (model.Has(CapImageInput)); it
// flows into ReadTool so reading an image file returns inline pixels for
// a vision model but an actionable text result otherwise. Called on
// every agent rebuild, so a /model switch re-derives the verdict.
func BuildToolRegistry(args Args, approval core.ApprovalMode, cwd string, sandbox *tools.Sandbox, provName, authMethod string, visionCapable bool, imageReg *imagegen.Registry) core.Registry {
	// chat and play both drop the built-in coding tools (read/write/edit/bash/
	// …): chat is pure conversation, play acts only through a world extension's
	// tools. --no-tools does the same. --no-workspace-tools also drops them, but
	// (unlike the above) leaves extension/MCP/skill tools and the normal identity
	// in place — an agent with its integrations but no host filesystem/shell.
	if !HasBaseWorkspaceTools(args) {
		return core.Registry{}
	}
	// One FileState shared by read/write/edit: the point is that an edit can ask
	// what a READ saw, so three separate trackers would answer nothing. Survives
	// tool rebuilds via Resolved.UseFiles — a rebuild minting a fresh one would
	// silently forget every file the model had read, and the staleness note
	// would start claiming files were never read at all.
	files := tools.NewFileState()
	all := map[string]core.Tool{
		"read":         &tools.ReadTool{CWD: cwd, Sandbox: sandbox, SupportsVision: visionCapable, Files: files},
		"write":        &tools.WriteTool{CWD: cwd, Sandbox: sandbox, Files: files},
		"edit":         &tools.EditTool{CWD: cwd, Sandbox: sandbox, Files: files},
		"bash":         &tools.BashTool{CWD: cwd, Sandbox: sandbox, Env: map[string]string{"TERVA_HOME": config.TervaHome()}},
		"grep":         &tools.GrepTool{CWD: cwd, Sandbox: sandbox},
		"glob":         &tools.GlobTool{CWD: cwd, Sandbox: sandbox},
		"terva_status": &tools.StatusTool{Provider: provName, CWD: cwd, AuthMethod: authMethod, BaseURL: args.BaseURL, Build: buildinfo.Get()},
		// The SAME sandbox the file tools get: `path` is bounded by the read
		// policy, so inspecting a transcript by path reaches exactly what `read`
		// reaches — a downloaded session yes, another project's sessions no.
		"session_inspect": &tools.SessionInspectTool{TervaHome: config.TervaHome(), CWD: cwd, Sandbox: sandbox},
		// Cross-session recall. No sandbox field because there is no path to
		// bound: it only ever opens this cwd's sessions directory, so its reach
		// is fixed by construction rather than by policy.
		"session_search":    &tools.SessionSearchTool{TervaHome: config.TervaHome(), CWD: cwd},
		"ask_user_question": &tools.AskUserTool{},
	}
	// Durable memory. The stores are bound here (not lazily) so the tool and the
	// injected block read one instance per session and cannot diverge; Adopt
	// carries a retired extension's files forward on first touch, the same shape
	// the worktree tools used when they were folded in. A bind failure is not
	// fatal — the store falls back to in-memory, so a session still runs.
	if !args.NoMemory {
		if mt := newMemoryTool(cwd); mt != nil {
			all["memory"] = mt
		}
	}
	// generate_image is opt-in and only useful with a backend, so it enters
	// the set only when one is configured. It's mutating (not read-only), so
	// it's approval-gated and dropped in plan mode like write/bash.
	if imageReg != nil && imageReg.Len() > 0 {
		all["generate_image"] = &tools.GenerateImageTool{CWD: cwd, Sandbox: sandbox, Registry: imageReg}
	}
	// The worktree tools (folded in from the terva-git-worktree extension)
	// register only where they can do anything: a git repo at — or one
	// directory below — the session cwd. Decided once per registry build, the
	// native twin of the extension's per-session tool withdrawal; a session
	// started outside any repo pays no tokens for them. worktree_list alone is
	// read-only, so it alone survives the plan-mode prune below.
	if worktree.GitAvailable(cwd) {
		wc := &tools.WorktreeCore{
			Manager: worktree.NewManager(),
			Base:    worktree.HostEnv(config.TervaHome(), cwd, ""),
		}
		all["worktree_list"] = &tools.WorktreeListTool{WorktreeCore: wc}
		all["worktree_create"] = &tools.WorktreeCreateTool{WorktreeCore: wc}
		all["worktree_claim"] = &tools.WorktreeClaimTool{WorktreeCore: wc}
		all["worktree_release"] = &tools.WorktreeReleaseTool{WorktreeCore: wc}
		all["worktree_remove"] = &tools.WorktreeRemoveTool{WorktreeCore: wc}
	}
	// Build-tag-gated optional built-ins (terva_scripting's code_execution,
	// …) contribute here from their _on file's init(). They pass through
	// the same plan-mode prune and --tools filter below, and classify via
	// the same permissions maps their init() extends — a tag adds a tool,
	// never a parallel registration path.
	for _, add := range extraBuiltinTools {
		if name, t := add(cwd, sandbox); t != nil {
			all[name] = t
		}
	}
	// Recognized built-in tool names, captured BEFORE the plan-mode prune below
	// so a --tools entry for a tool that plan mode legitimately drops (write,
	// bash, …) isn't mistaken for a typo. skill and activate_tools are added to
	// the registry by the caller after this --tools filter, so they count as
	// recognized here too. Backs the unknown-name warning at the bottom.
	recognized := make(map[string]bool, len(all)+2)
	for name := range all {
		recognized[name] = true
	}
	recognized["skill"] = true
	recognized["activate_tools"] = true
	// Plan mode promises read-only: mutating tools don't enter the
	// registry at all (the model shouldn't even see them), with the
	// confirm gate as the backstop for anything that arrives later.
	// Interactive tools (ask_user_question) are kept: asking the user
	// is exactly what plan mode wants when requirements are unclear.
	if approval == core.ApprovalPlan {
		for name := range all {
			if !permissions.IsReadOnly(name) && !permissions.IsInteractive(name) {
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
	// --tools is an allowlist; an entry that matches no known tool would
	// otherwise be dropped silently, so a typo like `--tools edt` quietly
	// removes the `edit` capability with no feedback. Warn (don't fail — the
	// available set is runtime-dependent) naming the unmatched entries and the
	// tools that were actually on offer this run.
	if unknown := unrecognizedTools(args.Tools, recognized); len(unknown) > 0 {
		known := make([]string, 0, len(recognized))
		for name := range recognized {
			known = append(known, name)
		}
		sort.Strings(known)
		fmt.Fprintf(os.Stderr, "note: --tools: unknown tool name(s) %s ignored (known: %s)\n",
			strings.Join(unknown, ", "), strings.Join(known, ", "))
	}
	return reg
}

// unrecognizedTools returns the --tools entries in requested that name no
// recognized built-in tool, preserving their given order. It backs the startup
// warning that keeps a typo (`--tools edt`) from silently dropping a capability.
func unrecognizedTools(requested []string, recognized map[string]bool) []string {
	var out []string
	for _, name := range requested {
		if !recognized[name] {
			out = append(out, name)
		}
	}
	return out
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

// envAPIKeyName returns the environment variable that credentials a provider
// ("OPENAI_API_KEY"), or "" when the registry declares none.
//
// Empty rather than a fallback, deliberately. This used to answer ANTHROPIC for
// anything it did not recognise, so a named endpoint or a provider added without
// an env hint was told to set ANTHROPIC_API_KEY — advice that cannot work, for a
// vendor the operator may have no account with. A provider whose credential does
// not come from the environment has no env var to name, and saying nothing is
// the honest answer; the caller drops the clause.
func envAPIKeyName(prov string) string {
	if spec, ok := specFor(prov); ok && spec.envHint != "" {
		return spec.envHint + "_API_KEY"
	}
	return ""
}

// noCredentialError phrases a missing credential around the one thing that
// changes what the reader should do: whether a provider was actually CHOSEN.
//
// When one was, naming it and its environment variable is exactly the help
// wanted. When one was not, the name in hand is only the default, and by the
// time this runs the fallback scan above has already asked every other provider
// and been told no — so the true statement is that nothing is configured
// anywhere. Naming the default there reads as "terva requires this vendor",
// which is both false and the least useful thing to tell someone whose account
// is with another one.
func noCredentialError(prov string, picked bool) error {
	if !picked {
		return fmt.Errorf("no credentials found for any provider — sign in with /login (in the terminal, or the control panel's Providers pane), export the API key of a provider you have an account with, or pass --provider with --api-key")
	}
	if env := envAPIKeyName(prov); env != "" {
		return fmt.Errorf("no credential for %s; set %s, pass --api-key, or sign in with /login", prov, env)
	}
	return fmt.Errorf("no credential for %s; pass --api-key, or sign in with /login", prov)
}

// resolveRawReasoning picks the raw reasoning level from the layers only the
// builder can tell apart: an explicit choice (--reasoning or a session
// override), then an operator's per-model models.json value, then the global
// config. Returns "" when the user chose none of them, which lets
// provider.EffectiveReasoning fall through to the model's CATALOG default.
//
// The ORDER is the policy, and it is not spelled out here: provider.ResolveReasoning
// owns it, and this asks that symbol which rung won rather than walking the
// layers a second time. Two hand-written copies of a precedence chain is how
// the display surfaces came to disagree with the turn they were describing.
//
// The mapping back to a RAW value is what this adds. The turn path needs the
// unnormalized string plus a set-signal — "off" and "none" are explicit choices
// that normalize to "" — so the rungs the builder owns return their own raw
// input, and the two below it return "" so provider.EffectiveReasoning applies
// the catalog default underneath. See provider.Model.DefaultReasoningSet.
func resolveRawReasoning(explicit string, m provider.Model, global string) string {
	switch _, from := provider.ResolveReasoning(explicit, m, global); from {
	case provider.ReasoningFromSession:
		return explicit
	case provider.ReasoningFromModelOperator:
		return m.DefaultReasoning
	case provider.ReasoningFromGlobal:
		return global
	}
	// Catalog default or nothing at all: unset, so the provider decides.
	return ""
}
