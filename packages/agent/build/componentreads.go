package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
)

// Whether a COMPONENT's directory is verifiably free of plaintext secrets, and
// so may stay readable by the agent.
//
// This is the payoff of docs/proposals/secrets-at-rest.md §8.1a. Those trees
// were never on the read deny-list: an extension's own state is the agent's
// legitimate debugging surface, and a test positively asserted
// ext-data/memory/state.json was readable. Meanwhile a standalone connector's
// bot token sat in connectors/<name>/config.json in the clear — the exact
// failure the whole workstream exists to stop, one door over.
//
// Denying the trees outright would have been the cheap fix and the wrong one:
// it takes away the debugging surface and still leaves the credential in the
// clear on disk, in backups, and in disk images. So the trees stay readable —
// and the price is that "readable" has to be EARNED per component, per read.
//
// # Why per read
//
// A component is a live process. It can write a plaintext credential into its
// own file at any moment, including one second after terva computed a verdict.
// The rest of the deny-list is a set-once-at-setup list because a path either
// holds credentials or does not; this is a property of CONTENT, so it is
// checked when the read happens. See [tools.ReadGuard].
//
// # Why a declaration, and not a scan
//
// Scanning for the enc:age: marker proves "these values are sealed". It can
// never prove "nothing else should have been" — an access_token left plaintext
// by a buggy or old component is indistinguishable from a legitimately public
// homeserver_url. Only a declaration closes that, which is why §8.5 puts the
// paths in the component's own hands and why an undeclared component is
// unknown, and unknown is not clean.

// ComponentVerdict is one component tree's readability, and the reason.
type ComponentVerdict struct {
	// Scope is the component's binding scope ("conn:matrix", "ext:memory").
	Scope string
	// Dir is the tree the verdict governs, absolute.
	Dir string
	// Readable is whether the agent's tools may read inside Dir.
	Readable bool
	// Reason names what is blocking, empty when readable.
	Reason string
	// Enforced is whether Readable is actually applied to the sandbox. False
	// for extension data dirs this release: the manifest field is new, so every
	// extension that predates it would go dark at once, and that deny is
	// absolute (/unjail does not lift it). They are reported instead — see
	// ExtensionDataDeclarationPending.
	Enforced bool
}

// ExtensionDataDeclarationPending records that ext-data/ verdicts are computed
// and reported but NOT yet enforced.
//
// A constant rather than a comment because two surfaces have to agree about it:
// the guard, which must not deny, and `terva secret status`, which must say
// "will be denied in a future release" rather than "denied". Flipping this to
// false is the whole of the follow-up.
const ExtensionDataDeclarationPending = true

// ConnectorDirName / ExtDataDirName are the two component trees under
// $TERVA_HOME. Named here because the guard has to recognise them by path.
const (
	ConnectorDirName = "connectors"
	ExtDataDirName   = "ext-data"
)

// componentReadGuard returns the [tools.ReadGuard] for one component tree.
//
// judge decides a single component by name; rel arrives relative to the tree
// root, so the first segment IS the component name and no path arithmetic is
// needed here. cwd scopes extension-manifest lookup to the daemon's workspace,
// the same parameter ScanConfigSecrets takes and for the same reason.
func componentReadGuard(home, cwd string, judge func(home, cwd, name string) ComponentVerdict) tools.ReadGuard {
	return func(rel string) (bool, string) {
		name, _, _ := strings.Cut(rel, "/")
		if name == "" || name == "." {
			// A stray file sitting directly in connectors/ or ext-data/ rather
			// than in a component's own subdir. It belongs to no component, so
			// no component's declaration can speak for it — but neither is it a
			// component's state, so the deny-list proper is the place to name it
			// if it ever matters.
			return true, ""
		}
		v := judge(home, cwd, name)
		if !v.Enforced {
			return true, ""
		}
		return v.Readable, v.Reason
	}
}

// connectorVerdict decides one connector's state directory.
//
// Two conditions, both necessary. The connector must be REGISTERED — the
// registry is populated from the handshake, so an entry means a live process
// told terva which paths it seals, and it is the only trustworthy source (the
// file itself is writable by anything that can write $TERVA_HOME, including the
// model through bash). And every declared path must currently be sealed.
//
// An unregistered connector is the operationally common case on a home that
// predates this: start it once and it registers.
func connectorVerdict(home, _, name string) ComponentVerdict {
	dir := filepath.Join(home, ConnectorDirName, name)
	v := ComponentVerdict{Scope: "conn:" + name, Dir: dir, Enforced: true}

	components, err := secretstore.NewRegistry(home).List()
	if err != nil {
		v.Reason = "the component registry could not be read: " + err.Error()
		return v
	}
	var found *secretstore.Component
	for i, c := range components {
		if c.Kind == "conn" && c.Name == name {
			found = &components[i]
			break
		}
	}
	if found == nil {
		v.Reason = "it has not registered which of its values are secret — start it once, then `terva secret status`"
		return v
	}
	if len(found.Paths) == 0 {
		// Registered while declaring nothing. Distinct from unregistered, and it
		// gets its own words: the fix is the connector's declaration, not
		// starting it again.
		v.Reason = "it registered no secret paths, so nothing can vouch for what is in it"
		return v
	}

	stateFile := filepath.Join(home, filepath.FromSlash(found.File))
	doc, err := os.ReadFile(stateFile)
	if errors.Is(err, os.ErrNotExist) {
		// Declared paths, no file yet: there is nothing in it to leak.
		v.Readable = true
		return v
	}
	if err != nil {
		v.Reason = fmt.Sprintf("%s could not be read: %v", found.File, err)
		return v
	}
	env := secrets.Envelope{Scope: found.Scope(), Paths: found.Paths}
	if err := env.Clean(doc); err != nil {
		v.Reason = fmt.Sprintf("%s: %v", found.File, err)
		return v
	}
	v.Readable = true
	return v
}

// extensionDataVerdict decides one extension's data directory.
//
// The declaration is a manifest field rather than a path list, because an
// extension has nowhere to seal a value in its own data dir — step 4 gave it a
// BROKERED store instead (extproto's secret_* frames), which keeps its runtime
// credentials in terva's own sealed secrets.json. So the only two honest
// answers about ext-data/ are "it holds none" and "it does", and the second is
// a bug the extension should fix by moving them to the broker.
//
// Reported, not enforced, this release — see ExtensionDataDeclarationPending.
func extensionDataVerdict(home, cwd, name string) ComponentVerdict {
	v := ComponentVerdict{
		Scope:    "ext:" + name,
		Dir:      filepath.Join(home, ExtDataDirName, name),
		Enforced: !ExtensionDataDeclarationPending,
	}
	dir := ExtensionDirFor(nil, cwd, name)
	if dir == "" {
		v.Reason = "no manifest found for it, so nothing can say whether its data dir holds secrets"
		return v
	}
	m, err := ExtensionManifestIn(dir)
	if err != nil {
		v.Reason = "its manifest could not be read: " + err.Error()
		return v
	}
	switch {
	case m.DataSecrets == nil:
		v.Reason = `its manifest does not declare "data_secrets"; add "data_secrets": false if its data dir holds no secret material`
	case *m.DataSecrets:
		v.Reason = `its manifest declares "data_secrets": true — move them to the host with the secret_* frames (docs/extensions.md)`
	default:
		v.Readable = true
	}
	return v
}

// ExtensionManifestIn reads an extension.json from a KNOWN directory.
//
// The sibling of ExtensionConfigSchemaIn, which reads the same file for one
// field. Separate rather than folded together because the schema helper returns
// only Config and callers of it want exactly that.
func ExtensionManifestIn(dir string) (extdriver.Manifest, error) {
	var m extdriver.Manifest
	if strings.TrimSpace(dir) == "" {
		return m, errors.New("no extension directory")
	}
	b, err := os.ReadFile(filepath.Join(dir, "extension.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}

// ComponentReadVerdicts is every component tree's verdict, for `terva secret
// status` and the ctrlproto secrets group. Sorted by scope.
//
// It walks what is ON DISK rather than what is registered, so a connector with
// a state directory and no registry entry — the case that matters — appears.
func ComponentReadVerdicts(home, cwd string) []ComponentVerdict {
	var out []ComponentVerdict
	for _, tree := range []struct {
		name  string
		judge func(home, cwd, name string) ComponentVerdict
	}{
		{ConnectorDirName, connectorVerdict},
		{ExtDataDirName, extensionDataVerdict},
	} {
		entries, err := os.ReadDir(filepath.Join(home, tree.name))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			out = append(out, tree.judge(home, cwd, e.Name()))
		}
	}
	return out
}
