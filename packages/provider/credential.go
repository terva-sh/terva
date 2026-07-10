package provider

import "context"

// CredentialSource yields the auth credential (an API key or an OAuth access
// token) a client should present on a request. A client resolves it ONCE per
// Stream, so a long-lived client can rotate its credential — an OAuth refresh
// — without being reconstructed. This replaced an earlier model that rebuilt
// the whole client to swap a token, which discarded per-client state (usage
// snapshots, connection pools) on every rotation.
//
// Implementations must be safe for concurrent use: one client can serve
// several sessions (bot mode mints an agent per chat), and their turns resolve
// the credential concurrently. A refreshing source should single-flight so
// concurrent callers coalesce onto one refresh rather than stampeding the
// token endpoint.
//
// The ctx bounds a refresh that has to hit the network; a source that never
// refreshes (a static key) ignores it.
type CredentialSource func(ctx context.Context) (string, error)

// StaticCredential is a CredentialSource for a fixed, non-rotating credential
// (every API-key client). It never errors and ignores the context.
func StaticCredential(cred string) CredentialSource {
	return func(context.Context) (string, error) { return cred, nil }
}
