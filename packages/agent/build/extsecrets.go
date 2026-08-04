package build

import (
	"errors"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/secretstore"
)

// Brokering an extension's runtime secrets into terva's own store.
//
// The class this closes: a secret an extension ACQUIRES while running — an
// OAuth token it negotiated, a key a user pasted into its own UI. Config
// secrets were handled in phase 2, but a runtime one could only be written to
// the extension's own ext-data/ directory, in the clear, at whatever mode it
// chose — and ext-data/ is readable by the agent on purpose.
//
// Brokered rather than dual-recipient, and the dividing line is: does this
// component run when terva does not? An extension never does — the driver
// spawns it and it dies with the driver — so a key of its own would be one more
// thing to generate, store, back up and rotate for a process that is never
// awake alone. It also means a rotation reaches a STOPPED extension's secrets,
// because they were never in a file the extension owned.

// extSecretBroker adapts the scoped store to the driver's interface.
type extSecretBroker struct{ store *secretstore.Store }

// GetSecret returns the value and whether one is stored. ErrNotFound is
// "absent", not a failure: an extension asking for a token it has not saved yet
// is the normal first-run path.
func (b extSecretBroker) GetSecret(scope, key string) (string, bool, error) {
	v, err := b.store.Get(scope, key)
	if errors.Is(err, secretstore.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v.Value, true, nil
}

func (b extSecretBroker) SetSecret(scope, key, value string) error {
	return b.store.Set(scope, key, value)
}

func (b extSecretBroker) DeleteSecret(scope, key string) error {
	return b.store.Delete(scope, key)
}

func (b extSecretBroker) SecretKeys(scope string) ([]string, error) {
	return b.store.Keys(scope)
}

// WireExtensionSecrets lets extensions store runtime credentials through the
// host (protocol 6 secret_* frames), sealed into terva's own store rather than
// written into ext-data/ in the clear. No-op when extensions are absent.
//
// The scope is applied by the driver from the extension's manifest name; this
// side never sees a scope from the wire.
func WireExtensionSecrets(extMgr *extensions.Manager, tervaHome string) {
	if extMgr == nil {
		return
	}
	extMgr.SetSecretBroker(extSecretBroker{store: config.SecretStoreIn(tervaHome)})
}
