package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func readWebToken(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, config.WebTokenName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestWebTokenInitMintsAnOwnerOnlyToken(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	var out bytes.Buffer
	if err := runWebTokenInit(&out); err != nil {
		t.Fatal(err)
	}
	tok := readWebToken(t, home)
	if len(tok) != webTokenBytes*2 {
		t.Fatalf("token is %d chars, want %d hex chars", len(tok), webTokenBytes*2)
	}
	fi, err := os.Stat(filepath.Join(home, config.WebTokenName))
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 regardless of ACL; guard the mode half only, so the
	// token-secrecy assertions below still run on every platform.
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token file is not owner-only: %v", fi.Mode())
	}
	// Under `go test` stdout is not a terminal, so the value must NOT be
	// printed — the same protection that keeps it out of an agent's transcript.
	if strings.Contains(out.String(), tok) {
		t.Fatalf("the token value was printed to a non-terminal:\n%s", out.String())
	}

	if err := runWebTokenInit(&out); err == nil {
		t.Fatal("a second init must refuse rather than silently replace a live token")
	}
}

func TestWebTokenRotateReplacesAndWarns(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := runWebTokenInit(new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	before := readWebToken(t, home)

	var out bytes.Buffer
	if err := runWebTokenRotate(&out); err != nil {
		t.Fatal(err)
	}
	after := readWebToken(t, home)
	if after == before {
		t.Fatal("rotate produced the same token")
	}
	// The daemon does not reload a token file, and saying so is the whole
	// difference between a rotation that took effect and one that looks like
	// it did.
	if !strings.Contains(out.String(), "restart") {
		t.Fatalf("rotate does not say a running daemon keeps the old token:\n%s", out.String())
	}
}

func TestWebTokenRotateWithoutOnePointsAtInit(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	err := runWebTokenRotate(new(bytes.Buffer))
	if err == nil {
		t.Fatal("rotate with no token must fail")
	}
	if !strings.Contains(err.Error(), "init") {
		t.Fatalf("error does not point at init: %v", err)
	}
}

// Minting a token has to be enough to turn auth on, or an operator mints one
// and quietly keeps serving unauthenticated.
func TestMintedTokenIsResolvedByDefault(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv(build.WebTokenEnv, "")

	if tok, err := build.ResolveWebToken(build.Args{}); err != nil || tok != "" {
		t.Fatalf("precondition: no token yet (tok=%q err=%v)", tok, err)
	}
	if err := runWebTokenInit(new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	want := readWebToken(t, home)

	got, err := build.ResolveWebToken(build.Args{})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("daemon resolved %q, want the minted token", got)
	}
	// The attach client finds the same file, so a local attach needs nothing
	// passed to it.
	gotAttach, err := build.ResolveAttachToken(build.Args{})
	if err != nil {
		t.Fatal(err)
	}
	if gotAttach != want {
		t.Fatalf("attach resolved %q, want the minted token", gotAttach)
	}
}

// An explicit source still wins: the default file is last in the chain.
func TestExplicitTokenSourceBeatsTheDefaultFile(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := runWebTokenInit(new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(testsupport.TempDir(t), "explicit-token")
	if err := os.WriteFile(other, []byte("explicit-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := build.ResolveWebToken(build.Args{WebTokenFile: other})
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit-value" {
		t.Fatalf("resolved %q, want the flag-named file to win", got)
	}
}

// An empty default token file is a hard error, never a silent drop to no-auth
// — the same rule --web-token-file already follows.
func TestEmptyDefaultTokenFileIsAnError(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, config.WebTokenName), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := build.ResolveWebToken(build.Args{}); err == nil {
		t.Fatal("an empty token file must not fall through to no auth")
	}
}
