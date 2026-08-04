//go:build terva_web

package agent

import (
	"errors"
	"strings"
	"testing"
)

// A credential-less start is fatal only when nothing reachable can fix it. The
// daemon grew a login flow (the Providers pane) long after this check was
// written to assume it had none, and a machine with no terminal was left being
// told to log in at a TUI it does not have.
func TestWebCredentialBootFailsOnlyWhenNothingCanSignIn(t *testing.T) {
	credErr := errors.New("no credentials found for any provider")

	if err := webCredentialBoot(credErr, true); err != nil {
		t.Errorf("with provider login enabled the daemon must boot so the panel can sign in, got: %v", err)
	}
	if err := webCredentialBoot(nil, false); err != nil {
		t.Errorf("a daemon that HAS a credential must never be blocked: %v", err)
	}

	err := webCredentialBoot(credErr, false)
	if err == nil {
		t.Fatal("with no login flow there is no route to a credential — the failure must stand")
	}
	// The original cause survives, so the reader still learns what was missing.
	if !errors.Is(err, credErr) {
		t.Errorf("the credential error must be wrapped, not replaced: %v", err)
	}
	// And it names the flag: "start it differently" is the actionable half, and
	// the operator reading this is already at a shell.
	if !strings.Contains(err.Error(), "--web-allow-login") {
		t.Errorf("the refusal should name the flag that makes it recoverable, got: %v", err)
	}
}

// Both categorically-higher groups answer to ONE listener rule, so a third
// cannot arrive with a laxer one. The bar is self-restart's: a bounded audience
// (loopback, or a scoped CIDR) is fine; blanket --web-insecure with no auth is
// not, and a stranger reaching an open port must not inherit the operator's
// authority over credentials.
func TestPrivilegedGroupsAreRefusedOnAnOpenListener(t *testing.T) {
	for _, what := range []string{"provider login", "secret management"} {
		if servePrivilegedGroup(true, true, what) {
			t.Errorf("%s must be refused on an unauthenticated open listener", what)
		}
		if !servePrivilegedGroup(true, false, what) {
			t.Errorf("%s must be served when the listener authenticates", what)
		}
		// A flag that was never passed stays off either way — the refusal must
		// not be the only reason it is off, or the test above proves nothing.
		if servePrivilegedGroup(false, false, what) {
			t.Errorf("%s must stay off when its flag was not passed", what)
		}
	}
}
