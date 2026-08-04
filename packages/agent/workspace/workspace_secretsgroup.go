package workspace

import (
	"context"
	"sync/atomic"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/secretadmin"
	"terva.sh/terva/packages/i18n"
)

// The secrets group: reporting terva's at-rest posture and managing the store's
// grants, over the wire.
//
// The in-process carrier serves it, so the TUI reaches these methods with no
// serialization; `terva web --web-allow-secrets` reaches the same ones over a
// socket. One backend, one report — which is the point of routing the CLI
// through the same producer (see packages/agent/secretadmin).
var _ ctrlproto.SecretsController = (*Workspace)(nil)

// wsSecrets is the group's enablement. A bool rather than a handle because
// secretadmin resolves the home and the key itself on every call, deliberately:
// a memoized identity outlives a rotation, and rotation is the one operation
// that happens underneath this surface while it is live.
type wsSecrets struct {
	on atomic.Bool
}

// EnableSecrets turns on the secrets group: the Workspace becomes a
// ctrlproto.SecretsController and can report the encryption posture and manage
// grants.
//
// Called by both composition roots — the in-process TUI and
// `terva web --web-allow-secrets` — because they are the same feature. A
// workspace without it answers every secrets verb with CodeUnsupported, so a
// client renders no pane rather than a pane that fails.
//
// There is nothing to pass. Unlike EnableAuth, which needs a manager and the
// host's apply hooks, everything here is derived from the credential home at
// call time; the flag is the whole configuration.
func (w *Workspace) EnableSecrets() { w.secrets.on.Store(true) }

func (w *Workspace) secretsEnabled() bool { return w.secrets.on.Load() }

func (w *Workspace) noSecrets() error {
	return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("this daemon does not serve secret management"))
}

// --- ctrlproto.SecretsController ---

// SecretsStatus reports the whole posture, shape-only: paths, modes, counts,
// names, states and reasons. No value crosses this wire.
func (w *Workspace) SecretsStatus(context.Context) (ctrlproto.SecretsStatus, error) {
	if !w.secretsEnabled() {
		return ctrlproto.SecretsStatus{}, w.noSecrets()
	}
	// The DAEMON's workspace directory, not the caller's: extension manifests
	// resolve against the tree terva is actually running in, and a remote client
	// has no cwd worth consulting.
	return secretadmin.Status(w.CWD()), nil
}

// SecretsList names the scopes and their key names.
func (w *Workspace) SecretsList(context.Context) (ctrlproto.SecretsListResult, error) {
	if !w.secretsEnabled() {
		return ctrlproto.SecretsListResult{}, w.noSecrets()
	}
	res, err := secretadmin.List()
	if err != nil {
		return ctrlproto.SecretsListResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", err.Error())
	}
	return res, nil
}

// SecretsGrant authorizes a principal against a scope.
func (w *Workspace) SecretsGrant(_ context.Context, p ctrlproto.SecretsGrantParams) error {
	if !w.secretsEnabled() {
		return w.noSecrets()
	}
	if err := secretadmin.Grant(p); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", err.Error())
	}
	return nil
}

// SecretsRevoke withdraws one grant.
func (w *Workspace) SecretsRevoke(_ context.Context, p ctrlproto.SecretsRevokeParams) error {
	if !w.secretsEnabled() {
		return w.noSecrets()
	}
	if err := secretadmin.Revoke(p); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", err.Error())
	}
	return nil
}

// SecretsForget drops terva's record of a component. Destructive only with
// Purge, which is why the caller has to say so.
func (w *Workspace) SecretsForget(_ context.Context, p ctrlproto.SecretsForgetParams) (ctrlproto.SecretsForgetResult, error) {
	if !w.secretsEnabled() {
		return ctrlproto.SecretsForgetResult{}, w.noSecrets()
	}
	res, err := secretadmin.Forget(p)
	if err != nil {
		return ctrlproto.SecretsForgetResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", err.Error())
	}
	return res, nil
}
