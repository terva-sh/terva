// Package agent wires the provider, core, tools, auth, and modes into a CLI.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/provider/auth"
)

// Config is the persisted user configuration.
type Config struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	Reasoning   string   `json:"reasoning"`
	Temperature *float32 `json:"temperature,omitempty"`
	// FavoriteModels are "provider/id" keys pinned to the top of the /model
	// picker (and surfaced as a cross-provider ★ Favorites view). Order is
	// not significant; membership is.
	FavoriteModels []string `json:"favorite_models,omitempty"`

	// Endpoints are user-defined OpenAI-compatible backends, each registered
	// as its own provider (the map key is the provider id) with its own
	// /v1/models discovery and row in the /model picker — the way to use
	// several local/remote OpenAI-compatible servers at once instead of the
	// single shared `openai-compatible` slot. User layer only: a project's
	// .terva/config.json must never redirect the agent to an arbitrary
	// endpoint. Keys are NEVER stored here — use APIKeyEnv or auth.json.
	Endpoints map[string]EndpointConfig `json:"endpoints,omitempty"`
	Theme     string                    `json:"theme"`

	// PersonaName overrides the agent's persona name — the name it
	// introduces itself by and the name shown in the welcome banner. Empty
	// means the default (DefaultPersonaName, "Mieli"). The TERVA_PERSONA_NAME
	// env var takes precedence over this. Setting it swaps only the identity
	// line; the rest of the system prompt (what terva means, the behaviour
	// guidance) is unchanged. For a fully custom identity, drop a SYSTEM.md
	// in $TERVA_HOME instead. User layer only.
	PersonaName string `json:"persona_name,omitempty"`

	// InlineImagesEnabled controls whether terva draws screenshots inline
	// when the terminal supports an image protocol. nil/missing means
	// auto (enabled when supported); false disables; true forces the
	// detected protocol when available.
	InlineImagesEnabled *bool `json:"inline_images_enabled,omitempty"`

	// AutoSwarmEnabled lets the main agent spawn background sub-agents
	// for parallel sub-tasks via a built-in swarm_spawn tool. Off by
	// default; nil/missing means disabled. Toggle from /settings.
	AutoSwarmEnabled *bool `json:"auto_swarm_enabled,omitempty"`

	// SwarmTiers maps a provider id to the concrete model ids used for the
	// swarm_spawn `tier` parameter (weak / medium / strong). It overrides the
	// built-in family guesses and, crucially, supplies tiers for providers
	// terva can't guess on its own (gateways like opencode-go, litellm,
	// openrouter), so `tier: weak` spawns a cheap sub-agent instead of falling
	// back to the full host model. User layer only — a project's config must
	// not redirect sub-agent model selection. Partial maps are fine: an unset
	// tier falls back to the built-in guess, then to the host model. Configure
	// and validate with `terva models tiers`.
	SwarmTiers map[string]TierConfig `json:"swarm_tiers,omitempty"`

	// SwarmWorktrees opts swarm sub-agents into per-agent git worktrees
	// instead of sharing the host's working tree. Off by default;
	// nil/missing means disabled. When on, each spawned agent leases an
	// isolated worktree via the terva-git-worktree extension (released
	// — not removed — on completion so you can review/merge via the
	// extension's `/worktree collect`). Requires that extension to be
	// installed; without it, spawning a sub-agent fails loudly. The
	// --swarm-worktrees flag overrides this for a single run.
	SwarmWorktrees *bool `json:"swarm_worktrees,omitempty"`

	// RecursiveFileSuggest controls the @-mention file picker. When true
	// the picker fuzzy-searches the whole project tree below the working
	// directory; nil/missing/false keeps the default directory-by-
	// directory browse. Toggle from /settings.
	RecursiveFileSuggest *bool `json:"recursive_file_suggest,omitempty"`

	// RespectGitignore controls whether the @-mention file picker hides
	// files and directories matched by the project's root .gitignore (in
	// both flat and recursive modes). nil/missing means the default,
	// which is on; false shows ignored entries. Toggle from /settings.
	RespectGitignore *bool `json:"respect_gitignore,omitempty"`

	// LastChangelogShown is the version whose release-notes
	// dialog the user has already seen. When the running binary's
	// version differs, the next interactive run shows the
	// changelog (fetched from the GitHub release page) once and
	// updates this field. Empty means "never shown".
	LastChangelogShown string `json:"last_changelog_shown,omitempty"`

	// ContextFiles are startup context files injected into the system
	// prompt, in order. This is the user-scope default; a project's
	// .terva/config.json overrides it (nearest-wins). Paths may be
	// relative (resolved against the config file's directory) or
	// absolute. See ResolveConfig / readStartupContextFiles.
	ContextFiles []string `json:"context_files,omitempty"`

	// DisableContextExtensions lists extensions whose context
	// contributions (register_context + context cards) the host ignores,
	// even though their tools, commands, and panels keep working.
	// Installing an extension is consent to run it; this opts a noisy or
	// untrusted extension out of injecting into the MODEL's context. A
	// project's .terva/config.json may add to this list (restrict-only,
	// union with the user layer) so a directory can run terva with a
	// different context posture. See docs/permissions.md.
	DisableContextExtensions []string `json:"disable_context_extensions,omitempty"`

	// DisableExtensions lists extensions that must NOT be loaded at all
	// (by manifest name) — they are never spawned, so their tools,
	// commands, panels, and context never appear. Stronger than
	// DisableContextExtensions (which loads the extension but suppresses
	// only its model context). A project's .terva/config.json may add to
	// this list (restrict-only, union with the user layer): a directory
	// can reject an extension entirely (e.g. a repo that doesn't want
	// terva-tasks running), but can never re-enable one the user
	// disabled. See docs/extensions.md.
	DisableExtensions []string `json:"disable_extensions,omitempty"`

	// Extensions holds per-extension configuration values, keyed by
	// extension name and then by the config field keys the extension
	// declares in its manifest `config` schema. The /extensions config
	// dialog writes here; the resolved values (manifest defaults overlaid
	// with these) are delivered to the extension over the wire. User layer
	// ONLY — a project's .terva/config.json may disable an extension but
	// never SET its values (a value is an escalation, not a restriction).
	// Secret values are stored PLAINTEXT here (user-scoped); the UI masks
	// them, the host never logs them. See extdialog.go / docs/extensions.md.
	Extensions map[string]map[string]json.RawMessage `json:"extensions,omitempty"`

	// DisableCorePackOffer suppresses the one-time first-run prompt that
	// offers to install the built-in core extension pack when no
	// extensions are present. For fleet/automation provisioning that sets
	// extensions up out of band; the prompt is also self-limiting (it
	// asks at most once and never on a non-TTY). User layer only. See
	// docs/plans/extension-packs.md.
	DisableCorePackOffer bool `json:"disable_core_pack_offer,omitempty"`

	// Approval is the default approval mode (plan / ask / auto-edit /
	// yolo) when no --approval / --no-yolo flag is given. Empty means
	// yolo, the historical default. See docs/permissions.md.
	Approval string `json:"approval,omitempty"`

	// Permissions are ordered tool-permission rules, first match
	// wins, evaluated after any project rules. The user layer is
	// trusted: allow, deny, and ask are all valid decisions here.
	Permissions []PermissionRuleConfig `json:"permissions,omitempty"`

	// Hooks configures pre/post tool-use hook programs. User config
	// only — a project must never be able to run code on session
	// start (see packages/agent/hooks and docs/hooks.md).
	Hooks *hooks.Config `json:"hooks,omitempty"`

	// MCP configures Model Context Protocol servers (stdio). The user
	// layer is trusted, so its servers always start. A project's
	// .terva/config.json MAY also declare MCP servers, but ONLY when the
	// workspace is trusted (Workspace Trust Phase 6): a project-scoped MCP
	// server is a subprocess spawn, so an UNtrusted cloned repo's servers
	// are never started. When trusted, the project's servers merge with
	// the user's — the user wins on a name collision; a trusted project
	// may only ADD servers, never replace a user-defined one (see
	// mergeMCPConfigs and docs/plans/workspace-trust.md). See docs/mcp.md.
	MCP *mcp.Config `json:"mcp,omitempty"`

	// DisableMCP lists MCP server names that must NOT be started, by the
	// name they carry under mcp.servers. It is the enable/disable surface
	// the /mcp dialog writes to: there is no per-server manifest, so
	// "enabled" simply means "absent from this list". A project's
	// .terva/config.json may add to this list (restrict-only, union with
	// the user layer) — honored even when the workspace is untrusted,
	// since refusing to spawn a server is always safe. See mcpdialog.go.
	DisableMCP []string `json:"disable_mcp,omitempty"`
}

// EndpointConfig defines one user-supplied OpenAI-compatible backend. The
// provider id is the map key in Config.Endpoints. The API key is NEVER stored
// here: leave it unset for keyless local servers, set APIKeyEnv to read it from
// the environment, or store it in auth.json under the endpoint name.
type EndpointConfig struct {
	BaseURL       string `json:"baseUrl"`                 // required, e.g. http://box:8000/v1
	APIKeyEnv     string `json:"apiKeyEnv,omitempty"`     // env var holding the key (optional)
	ContextWindow int    `json:"contextWindow,omitempty"` // default ctx for models lacking a hint
}

// TierConfig pins the weak/medium/strong swarm sub-agent models for one
// provider (the provider id is the map key in Config.SwarmTiers). Any field
// may be empty, in which case that tier falls back to terva's built-in
// family guess for the provider, then to the host model. The ids should be
// real models in that provider's catalog; `terva models tiers` validates
// them and shows what each tier currently resolves to.
type TierConfig struct {
	Weak   string `json:"weak,omitempty"`
	Medium string `json:"medium,omitempty"`
	Strong string `json:"strong,omitempty"`
}

// PermissionRuleConfig is the JSON shape of one permission rule. It
// compiles into a core.PermissionRule at load time (compilePermissionRules);
// invalid rules are dropped with a warning rather than failing startup.
type PermissionRuleConfig struct {
	// Tool is an exact tool name, or a prefix glob ending in '*'
	// (e.g. "mcp_*").
	Tool string `json:"tool"`
	// Args is an optional RE2 regexp matched against each top-level
	// string argument value and the raw args JSON (so "^git (status|diff)"
	// matches bash commands without JSON escaping).
	Args string `json:"args,omitempty"`
	// Decision is allow, deny, or ask.
	Decision string `json:"decision"`
	// Reason is shown to the model when a deny rule refuses a call.
	Reason string `json:"reason,omitempty"`
}

// ProjectConfig is the subset of configuration a project (.terva/config.json)
// is permitted to set. It is intentionally NOT the full Config: the type is
// the guard, so a cloned repo can only influence what this struct exposes —
// it cannot redirect base_url, swap providers, change the user's theme, etc.
// Widen this deliberately (see docs/plans/startup-context-files.md).
type ProjectConfig struct {
	// ContextFiles are startup context files to inject, in order. The
	// project layer is UNTRUSTED for path targets: a cloned repo's
	// .terva/config.json could otherwise point at ~/.ssh/id_ed25519 and
	// exfiltrate it into the system prompt. So entries here must be
	// project-relative and stay within the project root (the directory
	// containing .terva/). Absolute paths and root-escapes (../) are
	// rejected at load time (see containedContextFiles); the surviving
	// entries are resolved to absolute against the project root and only
	// absolute paths are retained. The trusted user layer (Config) has no
	// such restriction.
	ContextFiles []string `json:"context_files,omitempty"`

	// Permissions are project-scoped permission rules. The project
	// layer is UNTRUSTED, so it may only *restrict*: deny and ask are
	// honored, allow is rejected at load time — a cloned repo must
	// never be able to grant itself tool access the user didn't
	// (the self-approval ban; see docs/permissions.md). Project rules
	// are evaluated before user rules.
	Permissions []PermissionRuleConfig `json:"permissions,omitempty"`

	// DisableContextExtensions opts extensions out of contributing
	// model context for this project. Restrict-only and union'd with the
	// user layer: a project can only ADD to the disabled set, never
	// re-enable an extension the user disabled — consistent with the
	// untrusted project layer's "restrict, never escalate" rule.
	DisableContextExtensions []string `json:"disable_context_extensions,omitempty"`

	// DisableExtensions refuses to load specific extensions entirely for
	// this project. Restrict-only and union'd with the user layer (adds,
	// never re-enables) — a cloned repo can keep an extension from
	// running here but can never make one run the user didn't install.
	DisableExtensions []string `json:"disable_extensions,omitempty"`

	// DisableMCP refuses to start specific MCP servers for this project.
	// Restrict-only and union'd with the user layer (adds, never
	// re-enables). UNLIKE the MCP field below it carries no trust gate:
	// "don't spawn this server" is always safe to honor, so an untrusted
	// repo can still disable a user-defined server for this directory.
	DisableMCP []string `json:"disable_mcp,omitempty"`

	// Hooks lets a TRUSTED project define pre/post tool-use hook programs
	// (Workspace Trust Phase 6). These run arbitrary commands, so they are
	// honored ONLY when the workspace is trusted — an untrusted cloned
	// repo's hooks are never loaded (the engine sees only the user's). When
	// trusted, the project's hooks are appended to the user's: BOTH fire,
	// user hooks first (a union, never a replacement). See buildHookEngine
	// and docs/plans/workspace-trust.md.
	Hooks *hooks.Config `json:"hooks,omitempty"`

	// MCP lets a TRUSTED project declare Model Context Protocol servers
	// (stdio) (Workspace Trust Phase 6). Starting a server is a subprocess
	// spawn, so the project's servers are honored ONLY when the workspace
	// is trusted — an untrusted cloned repo's servers are never started.
	// When trusted, the project's servers merge with the user's: the user
	// wins on a name collision, so a trusted project may only ADD servers
	// (mergeMCPConfigs). See setupMCP and docs/plans/workspace-trust.md.
	MCP *mcp.Config `json:"mcp,omitempty"`

	// Provider and Model set this project's default provider/model. UNLIKE
	// the restrict-only fields above, these change runtime behaviour (which
	// model runs, possibly using the user's credentials and budget), so —
	// like Hooks and MCP — they are honored ONLY when the workspace is
	// trusted; an untrusted/cloned repo's values are ignored. Resolve order:
	// --provider/--model flag > project (trusted) > user config default.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Project-local config lives in the same per-project directory terva
// already uses for skills and extensions. Both spellings are read
// (".terva" preferred — envcompat.ProjectDirNames); nothing writes
// these directories, users author them.

// TervaHome returns the user data dir: $TERVA_HOME / $TERVA_HOME or the
// OS default, with rename-aware fallback (envcompat.Home is the one
// resolver — docs/plans/rename-terva.md, phase 1).
//
// All terva state (config.json, auth.json, sessions/, logs/) lives under
// this directory.
func TervaHome() string {
	return envcompat.Home()
}

// ConfigPath returns the path to config.json.
func ConfigPath() string { return filepath.Join(TervaHome(), "config.json") }

// DefaultPersonaName is the agent's out-of-the-box persona name. "Mieli" is
// Finnish for "mind" — the mind housed inside terva (Finnish for pine tar, the
// preservative that sealed boats and kept them seaworthy): a mind in a
// preserved vessel. Override per install with the TERVA_PERSONA_NAME env var
// or the persona_name field in config.json.
const DefaultPersonaName = "Mieli"

// defaultPersonaPhonetic is the English-speaker pronunciation hint shown
// after the default name in greetings. Emitted only for DefaultPersonaName:
// a user-supplied name has no pronunciation we can guess.
const defaultPersonaPhonetic = "MYEH-lee"

// PersonaName resolves the agent's persona name: the TERVA_PERSONA_NAME env
// var wins, then the persona_name config field, then DefaultPersonaName.
func PersonaName() string {
	if v := strings.TrimSpace(envcompat.Get("PERSONA_NAME")); v != "" {
		return v
	}
	if cfg, err := LoadConfig(); err == nil {
		if v := strings.TrimSpace(cfg.PersonaName); v != "" {
			return v
		}
	}
	return DefaultPersonaName
}

// personaPhonetic returns the pronunciation hint for the resolved persona, or
// "" for a custom name (whose pronunciation we can't guess).
func personaPhonetic() string {
	if PersonaName() == DefaultPersonaName {
		return defaultPersonaPhonetic
	}
	return ""
}

// personaLabel is the self-introduction label for greetings: the persona
// name with its pronunciation hint when known ("Mieli (MYEH-lee)"), else the
// bare name.
func personaLabel() string {
	if ph := personaPhonetic(); ph != "" {
		return PersonaName() + " (" + ph + ")"
	}
	return PersonaName()
}

// AuthPath returns the path to auth.json.
func AuthPath() string { return filepath.Join(TervaHome(), "auth.json") }

// KimiCLIFallbackDisabledPath returns a sentinel that disables falling
// back to the official Kimi Code CLI token after `terva /logout kimi`.
func KimiCLIFallbackDisabledPath() string {
	return filepath.Join(TervaHome(), "kimi-cli-fallback-disabled")
}

// LogsPath returns the directory holding log files.
func LogsPath() string { return filepath.Join(TervaHome(), "logs") }

// LoadConfig reads the config file, returning defaults if missing.
func LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

// SaveConfig writes the config file, creating parent dirs.
func SaveConfig(c Config) error {
	if err := os.MkdirAll(TervaHome(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), b, 0o644)
}

// toggleStringMember returns list with key present (on) or absent (off), with
// any duplicates collapsed. Used for membership lists like FavoriteModels.
func toggleStringMember(list []string, key string, on bool) []string {
	out := make([]string, 0, len(list)+1)
	for _, s := range list {
		if s != key {
			out = append(out, s)
		}
	}
	if on {
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// absolutizeContextFiles resolves each non-absolute entry against baseDir and
// cleans the result, so the returned slice holds absolute paths only. Empty
// entries are dropped. Returns nil for an empty input so callers can treat
// "nil" as "this layer set nothing".
func absolutizeContextFiles(baseDir string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		if !filepath.IsAbs(f) {
			f = filepath.Join(baseDir, f)
		}
		out = append(out, filepath.Clean(f))
	}
	return out
}

// containedContextFiles is the project-layer (UNTRUSTED) counterpart to
// absolutizeContextFiles. A project's .terva/config.json may come from a cloned
// repo, so its context_files paths must not be allowed to point outside the
// project root — otherwise a malicious repo could ship
// {"context_files":["/home/user/.ssh/id_ed25519"]} (or "../../etc/passwd")
// and exfiltrate arbitrary local files into the system prompt on launch.
//
// root is the project root (the directory containing .terva/). For each entry we:
//   - drop empties;
//   - reject absolute paths (they escape the repo-relative contract);
//   - resolve the relative path against root and reject anything that, after
//     filepath.Clean, escapes root via .. — including symlinked components that
//     resolve outside root, which is cheap to check with filepath.EvalSymlinks.
//
// Rejected entries are skipped with a clear stderr warning naming the offending
// path; launch is NOT aborted (degrade gracefully, matching how a malformed
// project config is already tolerated in ResolveConfig). Surviving entries are
// returned as absolute, cleaned paths. Returns nil for empty input.
func containedContextFiles(root string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		if filepath.IsAbs(f) {
			fmt.Fprintf(os.Stderr, "terva: ignoring project-config context file %q: absolute paths are not allowed; project-config context files must stay within the project\n", f)
			continue
		}
		abs := filepath.Clean(filepath.Join(root, f))
		if !withinRoot(root, abs) {
			fmt.Fprintf(os.Stderr, "terva: ignoring project-config context file %q: resolves outside the project root %q; project-config context files must stay within the project\n", f, root)
			continue
		}
		out = append(out, abs)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withinRoot reports whether abs (already absolute + cleaned) is the same as,
// or nested under, root. It guards against ../ escapes lexically and against
// symlinked components that resolve outside root by also checking the
// real (symlink-evaluated) path when the target — or its nearest existing
// ancestor — can be resolved. A path that doesn't yet exist still passes the
// lexical check; the loader (readStartupContextFiles) reports a clean
// not-found error later.
func withinRoot(root, abs string) bool {
	if !lexicallyWithin(root, abs) {
		return false
	}
	// Evaluate symlinks on the deepest existing ancestor of abs (abs itself
	// may not exist yet) and on root, then re-check containment so a symlink
	// pointing out of the tree can't sneak through.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return true // can't resolve root; rely on the lexical check
	}
	probe := abs
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			return lexicallyWithin(realRoot, real)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return true // reached the filesystem root without resolving; trust lexical
		}
		probe = parent
	}
}

// lexicallyWithin reports whether abs is root or a descendant of root, purely
// by path arithmetic (no filesystem access).
func lexicallyWithin(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// LoadProjectConfig walks from cwd toward the filesystem root and returns the
// FIRST .terva/config.json found (nearest-wins, stop on first) — so a repo with
// its own config uses it alone, and a repo without one inherits the nearest
// ancestor's. This differs deliberately from readAgentsContext, which cascades
// (collects an AGENTS.md at every level): config fields are scalars where
// merging up the tree is confusing, whereas AGENTS.md is additive prose.
//
// ContextFiles in the returned config are resolved to absolute paths against
// the project root (the directory that contains .terva/). Because that config
// may come from a cloned repo, ContextFiles entries are containment-checked:
// absolute paths and root-escapes are dropped with a stderr warning (see
// containedContextFiles), so the project layer can never read files outside
// its own tree. Returns (nil, nil) when no project config exists anywhere up
// the tree.
func LoadProjectConfig(cwd string) (*ProjectConfig, error) {
	if cwd == "" {
		return nil, nil
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	for {
		// Both project-dir spellings are checked at each level,
		// new-name first, before moving up — a directory's own
		// .terva beats its own .terva beats any ancestor's.
		for _, dirName := range envcompat.ProjectDirNames() {
			path := filepath.Join(dir, dirName, "config.json")
			b, err := os.ReadFile(path)
			if err == nil {
				var pc ProjectConfig
				if err := json.Unmarshal(b, &pc); err != nil {
					return nil, fmt.Errorf("parse %s: %w", path, err)
				}
				// Paths are repo-relative AND untrusted: resolve against dir
				// (the project root that contains the config dir), not against
				// the config dir itself, and reject absolute paths /
				// root-escapes so a cloned repo cannot exfiltrate files
				// outside its own tree.
				pc.ContextFiles = containedContextFiles(dir, pc.ContextFiles)
				return &pc, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				// Exists but unreadable (perms, etc.) — surface it; it's an
				// explicit, authored file the user expects to take effect.
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // hit the filesystem root
		}
		dir = parent
	}
	return nil, nil
}

// EffectiveConfig is the read-time view of configuration. Reads consult the
// project layer (nearest .terva/config.json) overlaid on the user layer
// ($TERVA_HOME/config.json); the embedded Config is that merged view.
//
// Writes must target the User layer only — never the merged view — so a
// project value can never be persisted into the user's config.json. v1
// overlays only ContextFiles, so every other embedded field equals User.
type EffectiveConfig struct {
	Config        // merged read view (ContextFiles resolved to absolute)
	User   Config // pristine, writable user layer
}

// ResolveConfig builds the EffectiveConfig for cwd: the user config overlaid
// with the allowlisted fields from the nearest .terva/config.json. ContextFiles
// resolve nearest-wins (the project's list if it set one, else the user's) and
// are held as absolute paths.
//
// trustProject gates the project's context_files: a project's context_files
// inject prose into the system prompt, so an untrusted workspace's list is
// dropped and only the user layer injects (Workspace Trust, plan row 6). The
// restrict-only project fields (disable_extensions / disable_context_extensions)
// are NOT gated — they can only TIGHTEN, so honoring an untrusted repo's "don't
// run this extension" is always safe.
//
// A malformed project config is surfaced on stderr and then ignored, so a
// broken .terva/config.json can never strand the user with no way to launch.
func ResolveConfig(cwd string, trustProject bool) EffectiveConfig {
	user, _ := LoadConfig()
	eff := EffectiveConfig{Config: user, User: user}
	pc, err := LoadProjectConfig(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: ignoring project config: %v\n", err)
	}
	if pc != nil && pc.ContextFiles != nil && trustProject {
		eff.Config.ContextFiles = pc.ContextFiles // already absolute
	} else {
		eff.Config.ContextFiles = absolutizeContextFiles(TervaHome(), user.ContextFiles)
	}
	// Disabled-context list is restrict-only: union the user and project
	// layers so a project can only add extensions to opt out of model
	// context, never re-enable one the user disabled.
	if pc != nil && len(pc.DisableContextExtensions) > 0 {
		eff.Config.DisableContextExtensions = unionStrings(user.DisableContextExtensions, pc.DisableContextExtensions)
	}
	// Same restrict-only union for the load-disable list: a project can
	// refuse to load an extension, never re-enable one the user disabled.
	if pc != nil && len(pc.DisableExtensions) > 0 {
		eff.Config.DisableExtensions = unionStrings(user.DisableExtensions, pc.DisableExtensions)
	}
	// And for the MCP disable list — restrict-only, NOT trust-gated: a
	// project may refuse to start a server (its own or a user's) here, but
	// can never make one start that the user disabled.
	if pc != nil && len(pc.DisableMCP) > 0 {
		eff.Config.DisableMCP = unionStrings(user.DisableMCP, pc.DisableMCP)
	}
	// Project default provider/model: trusted-only, because they change which
	// model (and credentials/budget) a launch uses — same gate as Hooks/MCP.
	// Layered onto the read view only; eff.User (the writable layer) is
	// untouched, so config repairs and saves stay on the user config.
	if pc != nil && trustProject {
		if pc.Provider != "" {
			eff.Config.Provider = pc.Provider
		}
		if pc.Model != "" {
			eff.Config.Model = pc.Model
		}
	}
	return eff
}

// trustedProjectHooks returns the nearest project config's hooks ONLY when
// the workspace is trusted; otherwise nil. Project hooks run arbitrary
// commands, so an untrusted (default) cloned repo must never reach the hook
// engine — this is the trust gate the historical "user-config only" ban was a
// proxy for (Workspace Trust Phase 6). A malformed project config degrades to
// nil with a stderr note rather than aborting launch, matching ResolveConfig.
func trustedProjectHooks(cwd string, trustProject bool) *hooks.Config {
	if !trustProject {
		return nil
	}
	pc, err := LoadProjectConfig(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: ignoring project config hooks: %v\n", err)
		return nil
	}
	if pc == nil {
		return nil
	}
	return pc.Hooks
}

// mergeHookConfigs unions user and project hooks (append, never replace): both
// sets fire, user hooks first within each phase. Returns nil when both are
// empty so callers can treat nil as "no hooks". The project argument is
// expected to be nil unless the workspace is trusted (trustedProjectHooks).
func mergeHookConfigs(user, project *hooks.Config) *hooks.Config {
	if user == nil && project == nil {
		return nil
	}
	merged := &hooks.Config{}
	if user != nil {
		merged.PreToolUse = append(merged.PreToolUse, user.PreToolUse...)
		merged.PostToolUse = append(merged.PostToolUse, user.PostToolUse...)
	}
	if project != nil {
		merged.PreToolUse = append(merged.PreToolUse, project.PreToolUse...)
		merged.PostToolUse = append(merged.PostToolUse, project.PostToolUse...)
	}
	if len(merged.PreToolUse) == 0 && len(merged.PostToolUse) == 0 {
		return nil
	}
	return merged
}

// trustedProjectMCP returns the nearest project config's MCP servers ONLY when
// the workspace is trusted; otherwise nil. Starting an MCP server is a
// subprocess spawn, so an untrusted (default) cloned repo's servers must never
// start (Workspace Trust Phase 6). A malformed project config degrades to nil
// with a stderr note rather than aborting launch.
func trustedProjectMCP(cwd string, trustProject bool) *mcp.Config {
	if !trustProject {
		return nil
	}
	pc, err := LoadProjectConfig(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: ignoring project config mcp: %v\n", err)
		return nil
	}
	if pc == nil {
		return nil
	}
	return pc.MCP
}

// mergeMCPConfigs merges user and project MCP server sets, USER WINS on a name
// collision: a trusted project may ADD servers but never replace one the user
// defined, so a repo can't shadow a user's server name with its own subprocess.
// Returns nil when neither set has servers. The project argument is expected to
// be nil unless the workspace is trusted (trustedProjectMCP).
func mergeMCPConfigs(user, project *mcp.Config) *mcp.Config {
	if (user == nil || len(user.Servers) == 0) && (project == nil || len(project.Servers) == 0) {
		return nil
	}
	merged := &mcp.Config{Servers: map[string]mcp.ServerConfig{}}
	// Project first so user entries overwrite on collision (user wins).
	if project != nil {
		for name, sc := range project.Servers {
			merged.Servers[name] = sc
		}
	}
	if user != nil {
		for name, sc := range user.Servers {
			merged.Servers[name] = sc
		}
	}
	if len(merged.Servers) == 0 {
		return nil
	}
	return merged
}

// unionStrings returns the de-duplicated union of a and b, preserving
// a's order then b's new entries.
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// AuthStoreFor returns the auth.Store backed by AuthPath().
func AuthStoreFor() *auth.Store { return auth.NewStore(AuthPath()) }

// ResolveCredential returns the credential (api key or oauth access
// token), the method ("apikey"/"oauth"), and an error when no
// credential is available.
//
// Lookup order:
//  1. explicit (e.g. --api-key): treated as API key
//  2. provider-specific env var: treated as API key
//  3. auth.json: api key OR oauth, whichever is present
func ResolveCredential(provider, explicit string) (cred, method string, err error) {
	cred, method, _, err = ResolveCredentialFull(provider, explicit)
	return cred, method, err
}

// ResolveCredentialFull is like ResolveCredential but also returns a
// provider-specific accountID when the credential is an OpenAI OAuth
// token (the ChatGPT account id extracted from the stored id_token).
// accountID is "" for API-key auth and for anthropic.
func ResolveCredentialFull(provider, explicit string) (cred, method, accountID string, err error) {
	if explicit != "" {
		return explicit, "apikey", "", nil
	}
	// Environment lookup, driven by the provider registry's apiKeyEnv
	// list (provider_registry.go). Two providers need handling beyond
	// a flat ordered key list:
	//   - anthropic: ANTHROPIC_OAUTH_TOKEN yields subscription ("oauth")
	//     auth and takes precedence over its API key.
	//   - amazon-bedrock: credentials come from the AWS chain (profile,
	//     IAM keys, container creds, bearer token); we surface a
	//     sentinel so Resolve doesn't error on a "missing" key.
	if provider == "anthropic" {
		if v := os.Getenv("ANTHROPIC_OAUTH_TOKEN"); v != "" {
			return v, "oauth", "", nil
		}
	}
	if spec, ok := providerByID[provider]; ok {
		for _, ev := range spec.apiKeyEnv {
			if v := os.Getenv(ev); v != "" {
				return v, "apikey", "", nil
			}
		}
	}
	if provider == "amazon-bedrock" {
		if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" ||
			os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
			return "<aws>", "apikey", "", nil
		}
	}
	c, err := AuthStoreFor().Load()
	if err != nil {
		return "", "", "", err
	}
	if pc, ok := c.AdditionalAPIKeyCreds[provider]; ok && pc.APIKey != "" {
		return pc.APIKey, "apikey", "", nil
	}
	switch provider {
	case "anthropic":
		if c.Anthropic.APIKey != "" {
			return c.Anthropic.APIKey, "apikey", "", nil
		}
		if c.Anthropic.OAuth != nil && c.Anthropic.OAuth.AccessToken != "" {
			tok, _ := refreshIfExpired("anthropic", c.Anthropic.OAuth)
			return tok.AccessToken, "oauth", "", nil
		}
	case "openai":
		if c.OpenAI.APIKey != "" {
			return c.OpenAI.APIKey, "apikey", "", nil
		}
	case "openai-codex":
		if c.OpenAI.OAuth != nil && c.OpenAI.OAuth.AccessToken != "" {
			tok, _ := refreshIfExpired("openai", c.OpenAI.OAuth)
			return tok.AccessToken, "oauth", tok.AccountID, nil
		}
	case "kimi":
		if c.Kimi.APIKey != "" {
			return c.Kimi.APIKey, "apikey", "", nil
		}
		if c.Kimi.OAuth != nil && c.Kimi.OAuth.AccessToken != "" {
			tok, _ := refreshIfExpired("kimi", c.Kimi.OAuth)
			return tok.AccessToken, "oauth", "", nil
		}
		if kimiCLIFallbackDisabled() {
			break
		}
		if tok := loadKimiCodeCLIToken(); tok != nil && tok.AccessToken != "" {
			tok, _ = refreshIfExpired("kimi", tok)
			return tok.AccessToken, "oauth", "", nil
		}
	case "deepseek":
		if c.DeepSeek.APIKey != "" {
			return c.DeepSeek.APIKey, "apikey", "", nil
		}
	case "google":
		// Google is API-key only — no OAuth path. We still load
		// auth.json so /login api-key flows work without exporting
		// an env var.
		if c.Google.APIKey != "" {
			return c.Google.APIKey, "apikey", "", nil
		}
	case "github-copilot":
		if c.GithubCopilot.APIKey != "" {
			return c.GithubCopilot.APIKey, "apikey", "", nil
		}
		if c.GithubCopilot.OAuth != nil && c.GithubCopilot.OAuth.AccessToken != "" {
			return c.GithubCopilot.OAuth.AccessToken, "oauth", "", nil
		}
	}
	return "", "", "", fmt.Errorf("no credential for %s", provider)
}

func kimiCLIFallbackDisabled() bool {
	_, err := os.Stat(KimiCLIFallbackDisabledPath())
	return err == nil
}

func SetKimiCLIFallbackDisabled(disabled bool) error {
	path := KimiCLIFallbackDisabledPath()
	if !disabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("disabled\n"), 0o600)
}

func loadKimiCodeCLIToken() *auth.OAuthToken {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(home, ".kimi", "credentials", "kimi-code.json"))
	if err != nil {
		return nil
	}
	var raw struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		TokenType    string  `json:"token_type"`
		ExpiresAt    float64 `json:"expires_at"`
		Scope        string  `json:"scope"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.AccessToken == "" {
		return nil
	}
	sec := int64(raw.ExpiresAt)
	nsec := int64((raw.ExpiresAt - float64(sec)) * 1e9)
	return &auth.OAuthToken{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		TokenType:    raw.TokenType,
		Scope:        raw.Scope,
		ClientID:     auth.KimiOAuth.ClientID,
		Expiry:       time.Unix(sec, nsec),
	}
}

// loadOAuthToken reads the current OAuth token from auth.json for the
// given provider. Returns nil if no token is stored.
func loadOAuthToken(providerName string) *auth.OAuthToken {
	c, err := AuthStoreFor().Load()
	if err != nil {
		return nil
	}
	switch providerName {
	case "anthropic":
		if c.Anthropic.OAuth != nil {
			return c.Anthropic.OAuth
		}
	case "openai":
		if c.OpenAI.OAuth != nil {
			return c.OpenAI.OAuth
		}
	case "kimi":
		if c.Kimi.OAuth != nil {
			return c.Kimi.OAuth
		}
		if kimiCLIFallbackDisabled() {
			return nil
		}
		return loadKimiCodeCLIToken()
	case "github-copilot":
		if c.GithubCopilot.OAuth != nil {
			return c.GithubCopilot.OAuth
		}
	}
	return nil
}

// refreshIfExpired returns a usable OAuth token for the given provider,
// refreshing it synchronously when it's past (or near) expiry. The
// refreshed token is persisted to auth.json.
//
// Failures return the original token unchanged — the caller then makes
// a request with the stale access_token, which will 401. That's still
// better than crashing at credential-resolution time.
func refreshIfExpired(providerName string, tok *auth.OAuthToken) (*auth.OAuthToken, error) {
	if tok == nil {
		return &auth.OAuthToken{}, fmt.Errorf("nil token")
	}
	if !tok.Expired() {
		return tok, nil
	}
	if tok.RefreshToken == "" {
		return tok, fmt.Errorf("%s oauth token expired and no refresh_token available — run /login again", providerName)
	}

	var op auth.OAuthProvider
	switch providerName {
	case "anthropic":
		op = auth.AnthropicOAuth
	case "openai":
		op = auth.OpenAIOAuth
	case "kimi":
		op = auth.KimiOAuth
	default:
		return tok, fmt.Errorf("unknown provider %q", providerName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	next, err := op.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		return tok, fmt.Errorf("refresh %s: %w", providerName, err)
	}
	// Preserve the refresh token if the server omitted it (Anthropic often does).
	if next.RefreshToken == "" {
		next.RefreshToken = tok.RefreshToken
	}
	// Carry over account id (openai) / id_token across refreshes.
	if next.AccountID == "" {
		next.AccountID = tok.AccountID
	}
	if next.IDToken == "" {
		next.IDToken = tok.IDToken
	}
	if err := AuthStoreFor().SetOAuth(providerName, *next); err != nil {
		return next, fmt.Errorf("persist refreshed token: %w", err)
	}
	return next, nil
}
