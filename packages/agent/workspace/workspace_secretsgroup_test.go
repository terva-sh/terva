package workspace

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// The group is off until a composition root turns it on, and a client that did
// not negotiate it gets CodeUnsupported rather than a half-answer. That is the
// contract the hello makes: a client which sees the group advertised is
// guaranteed the verbs work, so a workspace that would answer them without
// being enabled would make the advertisement meaningless in the other
// direction.
func TestSecretsVerbsAreUnsupportedUntilEnabled(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	ctx := context.Background()
	var w Workspace

	unsupported := func(t *testing.T, err error) {
		t.Helper()
		var ce *ctrlproto.Error
		if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeUnsupported {
			t.Fatalf("want CodeUnsupported, got %v", err)
		}
	}

	_, err := w.SecretsStatus(ctx)
	unsupported(t, err)
	_, err = w.SecretsList(ctx)
	unsupported(t, err)
	unsupported(t, w.SecretsGrant(ctx, ctrlproto.SecretsGrantParams{Principal: "p", Scope: "core:x", Mode: "read"}))
	unsupported(t, w.SecretsRevoke(ctx, ctrlproto.SecretsRevokeParams{Principal: "p", Scope: "core:x"}))
	_, err = w.SecretsForget(ctx, ctrlproto.SecretsForgetParams{Scope: "conn:x"})
	unsupported(t, err)

	// The other half. Without it this cannot tell "refused because disabled"
	// from "refused always" — which is exactly how a group ships dead.
	w.EnableSecrets()
	st, err := w.SecretsStatus(ctx)
	if err != nil {
		t.Fatalf("an enabled workspace must serve status: %v", err)
	}
	if st.Key.State == "" {
		t.Error("status came back empty; the report never ran")
	}
	if _, err := w.SecretsList(ctx); err != nil {
		t.Errorf("an enabled workspace must serve list: %v", err)
	}
	if err := w.SecretsGrant(ctx, ctrlproto.SecretsGrantParams{Principal: "ext:memory", Scope: "conn:matrix", Mode: "read"}); err != nil {
		t.Errorf("an enabled workspace must serve grant: %v", err)
	}
}

// A bad grant is a client error, not an internal one: the caller can fix a mode
// it spelled wrong, and CodeInternal would send them looking at the daemon.
func TestSecretsGrantRejectsAnUnknownMode(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	var w Workspace
	w.EnableSecrets()

	err := w.SecretsGrant(context.Background(), ctrlproto.SecretsGrantParams{
		Principal: "ext:memory", Scope: "conn:matrix", Mode: "write",
	})
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
		t.Fatalf("want CodeBadRequest for an unknown mode, got %v", err)
	}
}
