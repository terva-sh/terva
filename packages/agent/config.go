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

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/provider/auth"
)

// Config is the persisted user configuration.
type Config struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
	Theme     string `json:"theme"`

	// InlineImagesEnabled controls whether terva draws screenshots inline
	// when the terminal supports an image protocol. nil/missing means
	// auto (enabled when supported); false disables; true forces the
	// detected protocol when available.
	InlineImagesEnabled *bool `json:"inline_images_enabled,omitempty"`

	// AutoSwarmEnabled lets the main agent spawn background sub-agents
	// for parallel sub-tasks via a built-in swarm_spawn tool. Off by
	// default; nil/missing means disabled. Toggle from /settings.
	AutoSwarmEnabled *bool `json:"auto_swarm_enabled,omitempty"`

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

// AuthPath returns the path to auth.json.
func AuthPath() string { return filepath.Join(TervaHome(), "auth.json") }

// KimiCLIFallbackDisabledPath returns a sentinel that disables falling
// back to the official Kimi Code CLI token after `terva /logout kimi`.
func KimiCLIFallbackDisabledPath() string {
	return filepath.Join(TervaHome(), "kimi-cli-fallback-disabled")
}

// SessionsPath returns the directory holding session files.
func SessionsPath() string { return filepath.Join(TervaHome(), "sessions") }

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
// A malformed project config is surfaced on stderr and then ignored, so a
// broken .terva/config.json can never strand the user with no way to launch.
func ResolveConfig(cwd string) EffectiveConfig {
	user, _ := LoadConfig()
	eff := EffectiveConfig{Config: user, User: user}
	pc, err := LoadProjectConfig(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: ignoring project config: %v\n", err)
	}
	if pc != nil && pc.ContextFiles != nil {
		eff.Config.ContextFiles = pc.ContextFiles // already absolute
	} else {
		eff.Config.ContextFiles = absolutizeContextFiles(TervaHome(), user.ContextFiles)
	}
	return eff
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
	switch provider {
	case "anthropic":
		// ANTHROPIC_OAUTH_TOKEN takes precedence over ANTHROPIC_API_KEY.
		// Useful when both are set and the user wants subscription auth
		// without editing auth.json.
		if v := os.Getenv("ANTHROPIC_OAUTH_TOKEN"); v != "" {
			return v, "oauth", "", nil
		}
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "openai":
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "openai-codex":
		// ChatGPT/Codex subscription route. It intentionally ignores
		// OPENAI_API_KEY so users can keep both OpenAI API and Codex
		// subscription credentials configured and choose by provider.
	case "openai-responses":
		// Public OpenAI Responses API. Same env var as the chat-completions
		// `openai` provider; users pick the wire format by provider id.
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "kimi":
		if v := os.Getenv("KIMI_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
		if v := os.Getenv("MOONSHOT_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "google":
		// Both env names are widely-used in the Google ecosystem;
		// GEMINI_API_KEY is the AI Studio default, GOOGLE_API_KEY
		// is the older / generic name. Either works.
		if v := os.Getenv("GEMINI_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
		if v := os.Getenv("GOOGLE_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "deepseek":
		if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "moonshotai", "moonshotai-cn":
		// Moonshot direct API (separate from kimi-coding, which is the
		// Anthropic-Messages-fronted /coding endpoint with subscription OAuth).
		if v := os.Getenv("MOONSHOT_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "groq":
		if v := os.Getenv("GROQ_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "xai":
		if v := os.Getenv("XAI_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "cerebras":
		if v := os.Getenv("CEREBRAS_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "together":
		if v := os.Getenv("TOGETHER_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "huggingface":
		if v := os.Getenv("HF_TOKEN"); v != "" {
			return v, "apikey", "", nil
		}
	case "openrouter":
		if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "mistral":
		if v := os.Getenv("MISTRAL_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "zai":
		if v := os.Getenv("ZAI_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp":
		envVar := "XIAOMI_API_KEY"
		switch provider {
		case "xiaomi-token-plan-ams":
			envVar = "XIAOMI_TOKEN_PLAN_AMS_API_KEY"
		case "xiaomi-token-plan-cn":
			envVar = "XIAOMI_TOKEN_PLAN_CN_API_KEY"
		case "xiaomi-token-plan-sgp":
			envVar = "XIAOMI_TOKEN_PLAN_SGP_API_KEY"
		}
		if v := os.Getenv(envVar); v != "" {
			return v, "apikey", "", nil
		}
	case "minimax":
		if v := os.Getenv("MINIMAX_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "minimax-cn":
		if v := os.Getenv("MINIMAX_CN_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
		if v := os.Getenv("MINIMAX_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "fireworks":
		if v := os.Getenv("FIREWORKS_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "vercel-ai-gateway":
		if v := os.Getenv("AI_GATEWAY_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "opencode", "opencode-go":
		if v := os.Getenv("OPENCODE_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "github-copilot":
		if v := os.Getenv("COPILOT_GITHUB_TOKEN"); v != "" {
			return v, "apikey", "", nil
		}
		if v := os.Getenv("GITHUB_COPILOT_TOKEN"); v != "" {
			return v, "apikey", "", nil
		}
	case "cloudflare-workers-ai", "cloudflare-ai-gateway":
		if v := os.Getenv("CLOUDFLARE_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "amazon-bedrock":
		// Bedrock has many credential sources (AWS_PROFILE, IAM keys,
		// container creds, IRSA, bearer token). We surface a sentinel so
		// Resolve doesn't error on missing key; the real client (when
		// implemented) will resolve credentials through aws-sdk-go-v2.
		if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" ||
			os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
			return "<aws>", "apikey", "", nil
		}
	case "google-vertex":
		if v := os.Getenv("GOOGLE_CLOUD_API_KEY"); v != "" {
			return v, "apikey", "", nil
		}
	case "azure-openai-responses":
		if v := os.Getenv("AZURE_OPENAI_API_KEY"); v != "" {
			return v, "apikey", "", nil
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
