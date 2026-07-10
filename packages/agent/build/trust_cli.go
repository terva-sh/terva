package build

import (
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/config"
)

// Trust decisions driven by parsed CLI Args. The store, the paths and the
// on-disk state live in packages/agent/config; only this Args-shaped policy
// layer is agent-side.

// resolveTrust is the per-launch resolver, a sibling of
// ResolveApprovalMode / resolveJail (permissions.go). Unlike those two,
// the DEFAULT is the same in every mode — untrusted — because there is
// no safe way to silently trust a directory (a headless mode has no one
// to consent). Only the notice differs by mode (interactive reminder vs
// logged warning), handled by the callers.
//
// Resolution order:
//  1. --trust flag (one-shot, NOT persisted) → trusted.
//  2. the persisted store (exact or trusted-parent prefix) → trusted.
//  3. otherwise → restricted (untrusted-by-default, all modes).
func resolveTrust(args Args, store config.TrustStore) config.TrustState {
	if args.Trust {
		return config.TrustGranted
	}
	if ok, _ := store.IsTrusted(args.CWD); ok {
		return config.TrustGranted
	}
	return config.TrustRestricted
}

// ResolveTrustState loads the store and resolves the trust verdict for
// args. A store that can't be read is treated as empty (restricted is
// the safe failure) with a stderr note, so an unreadable security store
// never accidentally grants trust.
func ResolveTrustState(args Args) config.TrustState {
	store, err := config.LoadTrustStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: trust store unreadable, treating workspace as untrusted: %v\n", err)
		store = config.TrustStore{Version: config.TrustStoreVersion}
	}
	return resolveTrust(args, store)
}

// WarnRestrictedWorkspace logs a one-line stderr warning when the
// workspace is untrusted AND ships gated content, for the
// non-interactive modes (print/json/rpc/swarm, and ACP for now) that
// have no human to prompt. It names how to enable trust. A no-op when
// trusted or when nothing is gated (decision #2). Called once per launch.
func WarnRestrictedWorkspace(args Args, trusted bool) {
	if trusted || !config.HasGatedProjectContent(args.CWD) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"terva: workspace %s is untrusted — its project extensions, skills, and context files were NOT loaded. "+
			"Run `terva trust` to trust it, or pass --trust for this run.\n",
		args.CWD)
}
