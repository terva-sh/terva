package build

// Which values in config.json are secrets, and whether they are sealed.
//
// This lives in build because answering it needs the extension SCHEMAS —
// config.json alone cannot say which of extensions.<name>.<key> is a password
// and which is a hostname, and only the manifest knows. config therefore owns
// the crypto (config/secrets.go) and build owns the inventory.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"filippo.io/age"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/secrets"
)

// ConfigSecret is one secret-bearing value in config.json.
type ConfigSecret struct {
	Path      string // dotted location, e.g. extensions.weather.api_key
	Encrypted bool   // carries the at-rest marker
}

// ScanConfigSecrets inventories every secret-bearing value config.json holds
// and reports whether each is sealed. Values are never read out — only their
// LOCATION and state, so this is safe to print.
//
// Coverage is schema-driven for extensions (a manifest field marked secret)
// plus the one hardcoded secret field in the config schema itself,
// image.backends.<id>.api_key. An extension whose manifest cannot be found is
// skipped: without a schema there is no way to tell its stored values apart,
// and guessing in either direction is worse than reporting nothing —
// ConfigSecretsUnknown answers that question separately.
func ScanConfigSecrets(cwd string) []ConfigSecret {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil
	}
	var out []ConfigSecret
	for _, name := range sortedKeys(cfg.Extensions) {
		schema := ExtensionConfigSchema(cwd, name)
		if len(schema) == 0 {
			continue
		}
		block := cfg.Extensions[name]
		for _, f := range schema {
			if !f.IsSecret() {
				continue
			}
			raw, ok := block[f.Key]
			if !ok || len(raw) == 0 {
				continue
			}
			out = append(out, ConfigSecret{
				Path:      config.ExtensionFieldPath(name, f.Key),
				Encrypted: rawIsEncrypted(raw),
			})
		}
	}
	if cfg.Image != nil {
		for _, id := range sortedImageBackends(cfg.Image.Backends) {
			bc := cfg.Image.Backends[id]
			if bc.APIKey == "" {
				continue
			}
			out = append(out, ConfigSecret{
				Path:      config.ImageBackendKeyPath(id),
				Encrypted: secrets.IsEncryptedField(bc.APIKey),
			})
		}
	}
	return out
}

// ConfigSecretsUnknown names the extensions holding saved config whose manifest
// could not be found, so no schema says which of their values are secrets.
//
// It exists because "no plaintext secrets found" and "nothing could be checked"
// look identical in a scan, and only one of them is safe to act on. An
// uninstalled extension whose config block survives is the normal way to reach
// this state.
func ConfigSecretsUnknown(cwd string) []string {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range sortedKeys(cfg.Extensions) {
		if len(cfg.Extensions[name]) == 0 {
			continue
		}
		if len(ExtensionConfigSchema(cwd, name)) == 0 {
			out = append(out, name)
		}
	}
	return out
}

// ConfigReadableByAgent reports whether config.json currently holds nothing
// secret, and so may be dropped from the agent's read deny-list — with the
// reason when it may not.
//
// config.json is denied by default because it carries credentials inline, but
// that denial is blunt: it also hides the ~45 ordinary settings a user might
// reasonably ask the agent to help with, and the agent's own bash tool made
// the refusal cost a turn without buying much. Once every secret in the file
// is sealed, reading it discloses ciphertext and settings — so the denial can
// lift for exactly that state, and snap back the moment a plaintext secret
// reappears.
//
// Every condition fails CLOSED. Unknown is not clean: an extension whose
// manifest is missing has values no schema can classify, and an MCP server
// with a literal env value or header may be carrying a token that this
// proposal deliberately does not encrypt (the sanctioned form there is
// ${ENV}/bearer_env indirection). Any of those keeps the file denied.
func ConfigReadableByAgent(cwd string) (bool, string) {
	for _, s := range ScanConfigSecrets(cwd) {
		if !s.Encrypted {
			return false, "a secret is stored in plaintext (" + s.Path + "); run `terva secret init`"
		}
	}
	if unknown := ConfigSecretsUnknown(cwd); len(unknown) > 0 {
		return false, "no manifest for extension config block(s): " + strings.Join(unknown, ", ") +
			" — their values cannot be classified"
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return false, "config.json could not be read"
	}
	if cfg.MCP != nil {
		for _, name := range sortedServers(cfg.MCP.Servers) {
			sc := cfg.MCP.Servers[name]
			for k, v := range sc.Env {
				if !isEnvReference(v) {
					return false, "mcp.servers." + name + ".env." + k +
						" holds a literal value, which may be a token"
				}
			}
			for k, v := range sc.Headers {
				if !isEnvReference(v) {
					return false, "mcp.servers." + name + ".headers." + k +
						" holds a literal value; use ${ENV} or auth.bearer_env"
				}
			}
			// A URL is a credential carrier: https://user:pass@host puts a
			// password in the file with no field named like one. Exact rather
			// than a heuristic — either the URL has userinfo or it does not.
			if hasURLUserinfo(sc.URL) {
				return false, "mcp.servers." + name + ".url carries credentials in its userinfo (scheme://user:pass@host)"
			}
		}
	}
	return true, ""
}

// hasURLUserinfo reports whether a URL embeds credentials before the host.
// A value that does not parse is not treated as carrying any — it is not a
// URL, and refusing on unparseable text would deny the file over a typo.
func hasURLUserinfo(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.User != nil
}

// isEnvReference reports whether a config value merely POINTS at a secret
// (${VAR}) rather than containing one. An empty value carries nothing either.
func isEnvReference(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	return strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") &&
		!strings.Contains(v[:len(v)-1], "}")
}

func sortedServers(m map[string]mcp.ServerConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EncryptConfigSecrets seals every plaintext secret the scan found, returning
// the paths it changed. A no-op (nil, nil) when everything is already sealed.
//
// It also UPGRADES a value still carrying the unbound v1 encoding, because
// migrate is the verb whose whole job is "get everything onto the current
// scheme" — leaving that to rotate would mean `terva secret migrate` reporting
// nothing to do while a point of use refuses the value for being unbound.
// Upgrading needs the private key, so a keyless host (recipient in config, key
// elsewhere) reports what it could not do instead of silently skipping it.
func EncryptConfigSecrets(cwd string) ([]string, error) {
	on, err := config.SecretsFieldEncryptionOn()
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, nil
	}
	// Resolve the schemas BEFORE taking the config lock: they come off disk
	// (extension manifests), and MutateConfig's callback must not do work that
	// can block other writers for longer than the read-modify-write needs.
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, err
	}
	secretKeys := map[string][]string{} // extension -> secret field keys
	for _, name := range sortedKeys(cfg.Extensions) {
		for _, f := range ExtensionConfigSchema(cwd, name) {
			if f.IsSecret() {
				secretKeys[name] = append(secretKeys[name], f.Key)
			}
		}
	}

	var changed []string
	var sealErr error
	// seal turns one stored value into a bound, sealed one — from plaintext, or
	// from the unbound v1 encoding, which is the only case that needs the key.
	// Reports (newValue, changed, error).
	seal := func(path, cur string) (string, bool, error) {
		if secrets.IsLegacyField(cur) {
			id, err := config.SecretsIdentity()
			if err != nil {
				return "", false, fmt.Errorf("%s uses the older unbound encoding and upgrading it needs the key: %w", path, err)
			}
			_, plain, err := secrets.DecodeAnyField(id, cur)
			if err != nil {
				return "", false, fmt.Errorf("%s: %w", path, err)
			}
			out, err := config.EncryptFieldValue(path, plain)
			return out, err == nil, err
		}
		if secrets.IsEncryptedField(cur) || cur == "" {
			return "", false, nil
		}
		out, err := config.EncryptFieldValue(path, cur)
		return out, err == nil, err
	}
	err = config.MutateConfig(func(c *config.Config) {
		for name, keys := range secretKeys {
			block := c.Extensions[name]
			for _, key := range keys {
				raw, ok := block[key]
				if !ok || len(raw) == 0 {
					continue
				}
				var plain string
				if json.Unmarshal(raw, &plain) != nil || plain == "" {
					continue // a non-string (or empty) value is not a secret we can seal
				}
				path := config.ExtensionFieldPath(name, key)
				sealed, did, err := seal(path, plain)
				if err != nil {
					sealErr = err
					return
				}
				if !did {
					continue
				}
				b, err := json.Marshal(sealed)
				if err != nil {
					sealErr = err
					return
				}
				block[key] = b
				changed = append(changed, path)
			}
		}
		if c.Image == nil {
			return
		}
		for id, bc := range c.Image.Backends {
			path := config.ImageBackendKeyPath(id)
			sealed, did, err := seal(path, bc.APIKey)
			if err != nil {
				sealErr = err
				return
			}
			if !did {
				continue
			}
			bc.APIKey = sealed
			c.Image.Backends[id] = bc // a map value is a copy; write it back
			changed = append(changed, path)
		}
	})
	if sealErr != nil {
		return nil, sealErr
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(changed)
	return changed, nil
}

// UnopenableSecret is one sealed config value that will not open, and why.
type UnopenableSecret struct {
	Path   string
	Reason string // "sealed to a different key" | "bound to <other path>" | "unbound (older encoding)"
}

// VerifyConfigSecrets opens every sealed value in config.json with open, and
// reports the ones it could NOT, each with its reason. Rotation runs this
// before touching anything: a value it cannot open cannot be re-sealed, and
// finding that out halfway through would leave the home half-rotated.
//
// The reason is not decoration. There are now three ways to fail and they have
// nothing to do with each other — a lost key, a value someone MOVED here, and
// one still on the older unbound encoding — and an operator sent to look for a
// key problem when the real cause is relocation will not find anything.
func VerifyConfigSecrets(cwd string, open age.Identity) []UnopenableSecret {
	var bad []UnopenableSecret
	for _, s := range ScanConfigSecrets(cwd) {
		if !s.Encrypted {
			continue
		}
		_, err := openConfigSecretAt(cwd, s.Path, open)
		if err == nil {
			continue
		}
		bad = append(bad, UnopenableSecret{Path: s.Path, Reason: unopenableReason(cwd, s.Path, open)})
	}
	return bad
}

// unopenableReason distinguishes the three causes by retrying the open WITHOUT
// the binding check: if that succeeds, the ciphertext is fine and the problem
// is where it is sitting.
func unopenableReason(cwd, path string, open age.Identity) string {
	raw, err := rawConfigSecretAt(cwd, path)
	if err != nil {
		return "could not be read"
	}
	if secrets.IsLegacyField(raw) {
		return "sealed by an older terva without a path binding — `terva secret migrate` upgrades it"
	}
	boundTo, _, err := secrets.DecodeAnyField(open, raw)
	if err != nil {
		return "sealed to a different key"
	}
	return "sealed for " + strings.TrimPrefix(boundTo, config.SecretsScope+"|") + ", not for here — it was moved"
}

// ResealConfigSecrets re-seals every ENCRYPTED value in config.json — opening
// with `open`, sealing to `sealTo` — and returns the paths it rewrote.
//
// Plaintext values are left alone: rotation moves what is already sealed onto
// a new key, and `terva secret migrate` is what seals what never was. Doing
// both here would make a rotation quietly change which values are protected.
func ResealConfigSecrets(cwd string, open age.Identity, sealTo ...age.Recipient) ([]string, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, err
	}
	secretKeys := map[string][]string{}
	for _, name := range sortedKeys(cfg.Extensions) {
		for _, f := range ExtensionConfigSchema(cwd, name) {
			if f.IsSecret() {
				secretKeys[name] = append(secretKeys[name], f.Key)
			}
		}
	}

	var changed []string
	var failed error
	// Rotation is the one place that opens a value WITHOUT trusting its
	// binding, because it must be able to rewrite both of the cases a point of
	// use refuses: an unbound v1 value (adopt this path's binding — the upgrade)
	// and a v2 value bound elsewhere. The second is not repaired: a value that
	// claims another home was moved there, and silently re-binding it to where
	// the attacker put it is exactly the outcome binding exists to prevent.
	// Rotation is also the right moment to find out, since it reads everything.
	reseal := func(path, v string) (string, bool, error) {
		if !secrets.IsEncryptedField(v) {
			return "", false, nil
		}
		want := config.FieldBinding(path)
		got, plain, err := secrets.DecodeAnyField(open, v)
		if err != nil {
			return "", false, err
		}
		if got != "" && got != want {
			return "", false, fmt.Errorf("sealed value is bound to %q — it did not come from here; rotation will not re-bind it", got)
		}
		out, err := secrets.EncodeField(want, plain, sealTo...)
		if err != nil {
			return "", false, err
		}
		return out, true, nil
	}

	err = config.MutateConfig(func(c *config.Config) {
		for name, keys := range secretKeys {
			block := c.Extensions[name]
			for _, key := range keys {
				raw, ok := block[key]
				if !ok || len(raw) == 0 {
					continue
				}
				var cur string
				if json.Unmarshal(raw, &cur) != nil {
					continue
				}
				path := config.ExtensionFieldPath(name, key)
				next, did, err := reseal(path, cur)
				if err != nil {
					failed = fmt.Errorf("%s: %w", path, err)
					return
				}
				if !did {
					continue
				}
				b, err := json.Marshal(next)
				if err != nil {
					failed = err
					return
				}
				block[key] = b
				changed = append(changed, path)
			}
		}
		if c.Image == nil {
			return
		}
		for id, bc := range c.Image.Backends {
			path := config.ImageBackendKeyPath(id)
			next, did, err := reseal(path, bc.APIKey)
			if err != nil {
				failed = fmt.Errorf("%s: %w", path, err)
				return
			}
			if !did {
				continue
			}
			bc.APIKey = next
			c.Image.Backends[id] = bc
			changed = append(changed, path)
		}
	})
	if failed != nil {
		return nil, failed
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(changed)
	return changed, nil
}

// openConfigSecretAt opens the single value at a scan path, to prove it can be
// opened. The value is discarded — only the error matters.
func openConfigSecretAt(cwd, path string, open age.Identity) (string, error) {
	raw, err := rawConfigSecretAt(cwd, path)
	if err != nil {
		return "", err
	}
	return secrets.DecodeField(open, config.FieldBinding(path), raw)
}

// rawConfigSecretAt returns the STORED string at a scan path, unopened — the
// shared path resolution behind both opening a value and explaining why it
// would not open.
func rawConfigSecretAt(cwd, path string) (string, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return "", err
	}
	if rest, ok := strings.CutPrefix(path, "extensions."); ok {
		// name.key, where an extension name cannot contain a dot (install
		// dirs are single path segments) so the LAST dot splits it.
		i := strings.LastIndex(rest, ".")
		if i < 0 {
			return "", fmt.Errorf("malformed path %q", path)
		}
		var v string
		if err := json.Unmarshal(cfg.Extensions[rest[:i]][rest[i+1:]], &v); err != nil {
			return "", err
		}
		return v, nil
	}
	if rest, ok := strings.CutPrefix(path, "image.backends."); ok {
		id := strings.TrimSuffix(rest, ".api_key")
		if cfg.Image == nil {
			return "", fmt.Errorf("no image config for %q", path)
		}
		return cfg.Image.Backends[id].APIKey, nil
	}
	return "", fmt.Errorf("unknown secret path %q", path)
}

// rawIsEncrypted reports whether a stored JSON value is a sealed string.
func rawIsEncrypted(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	return secrets.IsEncryptedField(s)
}

func sortedKeys(m map[string]map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedImageBackends(m map[string]config.ImageBackendConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
