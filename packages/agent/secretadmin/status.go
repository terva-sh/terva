// Package secretadmin is the operator-facing view of terva's at-rest
// encryption, and the management of the secret store's grants.
//
// It exists so there is exactly ONE producer of that view. `terva secret status`
// and the ctrlproto secrets group answer the same question, and the obvious
// shape — each reads disk for itself — is how a panel and a terminal end up
// disagreeing about whether a file is encrypted. Here the CLI renders the same
// struct that crosses the wire, so a divergence would have to be a rendering
// bug rather than a difference of opinion.
//
// Nothing in this package returns a secret VALUE. It reports paths, modes,
// counts, names, states and reasons — the shape — because redaction that has to
// guess at field names fails open, and the only reliable way not to leak a
// value is never to hold one. See docs/proposals/secrets-at-rest.md §8.10.
package secretadmin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/discord"
	"terva.sh/terva/packages/agent/modes/telegram"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
)

// Status assembles the whole posture. cwd scopes the config.json scan, which
// resolves extension manifests relative to the workspace — the daemon's
// workspace, not the caller's process, which is why it is a parameter.
//
// Errors are reported IN the struct rather than returned: a status report whose
// job is to say what is wrong must not fail to render because something is
// wrong. A store that will not open is a finding, not an outage.
func Status(cwd string) ctrlproto.SecretsStatus {
	st := ctrlproto.SecretsStatus{
		Key:        keyState(),
		Files:      fileStates(),
		Store:      storeState(),
		Config:     configScan(cwd),
		Components: componentStates(),
		Reads:      readStates(cwd),
	}
	if cfg, err := config.LoadConfig(); err == nil && cfg.Secrets != nil {
		st.Recipient = cfg.Secrets.Recipient
	}
	if retired, err := config.RetiredSecretsIdentities(); err == nil {
		st.RetiredKeys = len(retired)
	}
	st.Grants = grantStates()
	return st
}

// keyState resolves the at-rest key and classifies the result.
//
// The absent/missing split is the one this function exists for. Both look like
// "no key resolves", and they call for opposite actions: absent means encryption
// was never turned on and `terva secret init` is right, while missing means
// ciphertext exists that this key was meant to open, and minting a new one would
// strand it permanently. establishedEncryptionWithoutKey is the evidence.
func keyState() ctrlproto.SecretsKey {
	path := config.SecretsKeyPath()
	_, err := config.SecretsIdentity()
	switch {
	case err == nil:
		k := ctrlproto.SecretsKey{State: ctrlproto.SecretsKeyPresent, Path: path}
		fi, statErr := os.Stat(path)
		if statErr != nil {
			// The identity resolved, but not from this path: it came from the
			// environment. Reporting a mode nobody set would be a fiction.
			k.FromEnv = true
			return k
		}
		// Windows carries no POSIX permission bits: Stat reports 0666 for every
		// file whatever its ACL says. Deriving an owner-only verdict from that
		// would fail every key on the platform and prescribe a chmod that does
		// not exist there — so report the same way an env-supplied identity
		// does, by declining to state a mode nobody set.
		if runtime.GOOS == "windows" {
			k.Reason = "permissions not checked on Windows — POSIX modes are not the access mechanism there"
			return k
		}
		mode := fi.Mode().Perm()
		k.Mode = fmt.Sprintf("%#o", mode)
		k.OwnerOnly = mode&0o077 == 0
		if !k.OwnerOnly {
			k.Reason = fmt.Sprintf("group/other-accessible — run: chmod 600 %s", path)
		}
		return k
	case errors.Is(err, secrets.ErrNoKey):
		if why := EstablishedEncryptionWithoutKey(); why != "" {
			return ctrlproto.SecretsKey{
				State: ctrlproto.SecretsKeyMissing,
				Path:  path,
				Reason: fmt.Sprintf("%s. Restore it from backup, or point --secrets-key-file / %s at it",
					why, config.SecretsKeyFileEnv),
			}
		}
		return ctrlproto.SecretsKey{
			State:  ctrlproto.SecretsKeyAbsent,
			Path:   path,
			Reason: "encryption off — run `terva secret init`",
		}
	default:
		return ctrlproto.SecretsKey{State: ctrlproto.SecretsKeyUnusable, Path: path, Reason: err.Error()}
	}
}

// EstablishedEncryptionWithoutKey reports, in operator-facing words, the
// evidence that encryption was already set up on this home — or "" when there is
// none. Exported because `terva secret init` refuses on exactly this signal, and
// the two must agree about what counts as evidence.
//
// Both signals are checked because either can stand alone: a home restored
// without config.json still has ciphertext, and a key lost before the first
// login has a recipient but nothing encrypted yet.
func EstablishedEncryptionWithoutKey() string {
	if cfg, err := config.LoadConfig(); err == nil && cfg.Secrets != nil && cfg.Secrets.Recipient != "" {
		return "config.json records a recipient"
	}
	if FileState(config.AuthPath()) == ctrlproto.SecretsFileEncrypted {
		return "auth.json is encrypted"
	}
	return ""
}

// FileState sniffs a file's leading bytes. It never parses the content, so
// nothing secret can escape into a report — and it answers about what is ON
// DISK, which is the only thing that matters: a home configured for encryption
// can still hold plaintext written before it was.
func FileState(path string) string {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ctrlproto.SecretsFileAbsent
	}
	if err != nil {
		return ctrlproto.SecretsFileUnreadable
	}
	if secrets.IsAgeFile(b) {
		return ctrlproto.SecretsFileEncrypted
	}
	return ctrlproto.SecretsFilePlaintext
}

// fileStates covers the whole-file-sealed stores, plus the legacy chat configs.
//
// bot.json and discord.json are reported for their TOKEN rather than their file
// state, because their file is not sealed whole and never will be — only the
// token moved into the store. Without this row, "migrated" and "never swept"
// look identical, which is exactly how a plaintext credential survives on a home
// the user believes is encrypted. Found by running the binary; the suite was
// green.
func fileStates() []ctrlproto.SecretsFile {
	out := []ctrlproto.SecretsFile{{Name: "auth.json", State: FileState(config.AuthPath())}}
	home := config.TervaHome()
	if c, err := telegram.LoadConfig(home); err == nil && c.LegacyToken != "" {
		out = append(out, ctrlproto.SecretsFile{
			Name:  "bot.json",
			State: ctrlproto.SecretsFilePlaintext,
			Note:  "bot token — run `terva secret migrate`",
		})
	}
	if c, err := discord.LoadConfig(home); err == nil && c.LegacyToken != "" {
		out = append(out, ctrlproto.SecretsFile{
			Name:  "discord.json",
			State: ctrlproto.SecretsFilePlaintext,
			Note:  "bot token — run `terva secret migrate`",
		})
	}
	return out
}

// storeState summarizes secrets.json by scope and count — never a key name, and
// never a value, so this row stays safe to paste into an issue. Key names are
// secrets.list, which is a deliberate second call.
func storeState() ctrlproto.SecretsStore {
	store := config.SecretStoreIn(config.TervaHome())
	scopes, err := store.Scopes()
	if err != nil {
		return ctrlproto.SecretsStore{Error: err.Error()}
	}
	if len(scopes) == 0 {
		return ctrlproto.SecretsStore{}
	}
	out := ctrlproto.SecretsStore{Present: true}
	if sealed, err := store.Encrypted(); err != nil {
		out.Error = err.Error()
	} else {
		out.Encrypted = sealed
	}
	for _, sc := range scopes {
		keys, err := store.Keys(sc)
		if err != nil {
			continue
		}
		out.Scopes = append(out.Scopes, ctrlproto.SecretsScope{Scope: sc, Keys: len(keys)})
	}
	return out
}

// grantStates reads the grants and settles Expired against the DAEMON's clock,
// so a client in another timezone cannot render a live grant as dead.
func grantStates() []ctrlproto.SecretsGrant {
	gs, err := config.SecretStoreIn(config.TervaHome()).Grants()
	if err != nil {
		return nil
	}
	now := time.Now()
	out := make([]ctrlproto.SecretsGrant, 0, len(gs))
	for _, g := range gs {
		row := ctrlproto.SecretsGrant{
			Principal: g.Principal,
			Scope:     g.Scope,
			Mode:      string(g.Mode),
		}
		if !g.Expires.IsZero() {
			expires := g.Expires
			row.Expires = &expires
			row.Expired = !now.Before(expires)
		}
		out = append(out, row)
	}
	return out
}

// configScan inventories config.json's secret-bearing values by location.
func configScan(cwd string) ctrlproto.SecretsConfigScan {
	found := build.ScanConfigSecrets(cwd)
	out := ctrlproto.SecretsConfigScan{Total: len(found)}
	for _, s := range found {
		if !s.Encrypted {
			out.Plaintext = append(out.Plaintext, s.Path)
		}
	}
	out.Unclassified = build.ConfigSecretsUnknown(cwd)
	out.AgentCanRead, out.Reason = build.ConfigReadableByAgent(cwd)
	return out
}

// componentStates lists the registered components, then the sealed component
// files that NO registry entry claims.
//
// The second half is the operationally important one and the reason this is not
// just a registry dump: an unregistered component cannot be re-sealed by a
// rotation, so `rotate --revoke` would leave terva unable to read it. That is a
// fact the user can act on (start the component once) and one they would
// otherwise discover only when a credential stopped working.
func componentStates() []ctrlproto.SecretsComponent {
	home := config.TervaHome()
	registered, err := secretstore.NewRegistry(home).List()
	if err != nil {
		return nil
	}
	out := make([]ctrlproto.SecretsComponent, 0, len(registered))
	for _, c := range registered {
		row := ctrlproto.SecretsComponent{
			Scope:      c.Scope(),
			Paths:      len(c.Paths),
			File:       filepath.ToSlash(c.File),
			Registered: true,
		}
		if !c.LastSeen.IsZero() {
			seen := c.LastSeen
			row.LastSeen = &seen
		}
		out = append(out, row)
	}
	for _, name := range unregisteredSealedConnectors(home, registered) {
		out = append(out, ctrlproto.SecretsComponent{
			Scope: "conn:" + name,
			File:  filepath.ToSlash(filepath.Join("connectors", name, "config.json")),
		})
	}
	return out
}

// UnregisteredSealedComponents names the component state files — relative to the
// data home — that hold sealed values while no registry entry claims a recipient
// for them.
//
// Exported for `terva secret rotate`, which warns at the MOMENT access is lost
// rather than leaving it to a status anyone might run later. The component keeps
// working (it opens with its own key); terva is the one that loses access.
func UnregisteredSealedComponents() []string {
	home := config.TervaHome()
	registered, err := secretstore.NewRegistry(home).List()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range unregisteredSealedConnectors(home, registered) {
		out = append(out, "connectors/"+name)
	}
	return out
}

// readStates is each component directory's readability verdict — whether the
// agent's own tools may read inside it.
//
// Reported even when a verdict is not yet enforced, because a directory that
// silently goes dark in a later release is exactly the surprise this row
// exists to prevent: the reason names the declaration an author has to add.
func readStates(cwd string) []ctrlproto.SecretsComponentRead {
	verdicts := build.ComponentReadVerdicts(config.TervaHome(), cwd)
	out := make([]ctrlproto.SecretsComponentRead, 0, len(verdicts))
	for _, v := range verdicts {
		out = append(out, ctrlproto.SecretsComponentRead{
			Scope:    v.Scope,
			Dir:      v.Dir,
			Readable: v.Readable,
			Reason:   v.Reason,
			Enforced: v.Enforced,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out
}

// unregisteredSealedConnectors names connectors whose state file carries sealed
// values while no registry entry claims them.
func unregisteredSealedConnectors(home string, known []secretstore.Component) []string {
	entries, err := os.ReadDir(filepath.Join(home, "connectors"))
	if err != nil {
		return nil
	}
	claimed := map[string]bool{}
	for _, c := range known {
		claimed[filepath.ToSlash(c.File)] = true
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("connectors", e.Name(), "config.json"))
		if claimed[rel] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(home, "connectors", e.Name(), "config.json"))
		if err != nil || !strings.Contains(string(b), secrets.FieldPrefix) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}
