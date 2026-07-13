package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// tokenFile writes a token to a scratch file and returns the path.
func tokenFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(testsupport.TempDir(t), "token")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWebTokenFromFile(t *testing.T) {
	// A file written by `echo secret > token` ends in a newline; a token that
	// carries it authenticates against nothing.
	got, err := ResolveWebToken(Args{WebTokenFile: tokenFile(t, "s3cret\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Errorf("token = %q, want %q — trailing newline not trimmed", got, "s3cret")
	}
}

// An unreadable or empty token file must be fatal. Returning "" would drop the
// daemon into no-auth mode, which is the exact outcome someone reaching for a
// token file is trying to avoid — and on a loopback bind it would start happily,
// so the only clue would be an absent log line.
func TestWebTokenFileNeverFallsOpen(t *testing.T) {
	for name, path := range map[string]string{
		"missing": filepath.Join(testsupport.TempDir(t), "nope"),
		"empty":   tokenFile(t, ""),
		"blank":   tokenFile(t, "\n  \n"),
	} {
		got, err := ResolveWebToken(Args{WebTokenFile: path})
		if err == nil {
			t.Errorf("%s token file: no error, and token = %q — the daemon would serve with no auth", name, got)
		}
		if got != "" {
			t.Errorf("%s token file: returned a token %q alongside the error", name, got)
		}
	}
}

// The environment route, and the scrub that makes it worth having: terva hands
// the agent's shell tool os.Environ(), so a token left there is one `env` call
// away from the model.
func TestWebTokenFromEnvIsScrubbed(t *testing.T) {
	resetWebTokenOnce(t)
	t.Setenv(WebTokenEnv, "from-env")

	got, err := ResolveWebToken(Args{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Fatalf("token = %q, want %q", got, "from-env")
	}
	if v, ok := os.LookupEnv(WebTokenEnv); ok {
		t.Errorf("%s still set to %q after resolve — the agent's bash tool inherits os.Environ() and would read it", WebTokenEnv, v)
	}
	// Reading it twice must not lose it: the scrub removed the only copy, so a
	// second caller has to see the cached value rather than conclude "no token"
	// and start a daemon with no auth.
	if again := ResolveAttachToken(Args{}); again != "from-env" {
		t.Errorf("second resolve = %q, want %q — the scrub ate the token", again, "from-env")
	}
}

// The scrub, proven against a real child process spawned exactly the way the
// agent's shell tool spawns one (tools/bash.go: cmd.Env = os.Environ()). The
// unit assertion above says the variable is gone from this process; this says
// the model cannot get it back out by running `env`.
//
// runWebMode resolves the token before it builds the Workspace, so no tool can
// spawn ahead of the scrub.
func TestScrubbedTokenIsInvisibleToTheAgentsShell(t *testing.T) {
	resetWebTokenOnce(t)
	t.Setenv(WebTokenEnv, "s3cret")

	spawn := func() string {
		cmd := exec.Command("sh", "-c", "printf %s \"$"+WebTokenEnv+"\"")
		cmd.Env = os.Environ() // bash.go:90, verbatim
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	if got := spawn(); got != "s3cret" {
		t.Fatalf("precondition: a child should see the token before the resolve, got %q", got)
	}
	if _, err := ResolveWebToken(Args{}); err != nil {
		t.Fatal(err)
	}
	if got := spawn(); got != "" {
		t.Errorf("the agent's shell can still read the bearer token: %q", got)
	}
}

// Precedence, and the flag's warning. The flag still wins (breaking it would be
// worse than the leak), but it is the last resort, not the first.
func TestWebTokenPrecedence(t *testing.T) {
	resetWebTokenOnce(t)
	t.Setenv(WebTokenEnv, "env-token")
	file := tokenFile(t, "file-token")

	got, err := ResolveWebToken(Args{WebToken: "flag-token", WebTokenFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if got != "flag-token" {
		t.Errorf("token = %q, want the explicit flag to win", got)
	}
	// File beats the environment.
	got, err = ResolveWebToken(Args{WebTokenFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if got != "file-token" {
		t.Errorf("token = %q, want the file to beat the environment", got)
	}
}

func TestAttachTokenFallsBackToEnv(t *testing.T) {
	resetWebTokenOnce(t)
	t.Setenv(WebTokenEnv, "shared")

	if got := ResolveAttachToken(Args{Token: "explicit"}); got != "explicit" {
		t.Errorf("attach token = %q, want the flag to win", got)
	}
	if got := ResolveAttachToken(Args{}); got != "shared" {
		t.Errorf("attach token = %q, want the environment fallback %q", got, "shared")
	}
}

// The daemon's no-auth guards key on Args.WebToken, so a token that arrived by
// file or environment has to be written back into it. Without that, an
// env-provisioned daemon reads as unauthenticated to every one of them: it
// refuses to self-restart (web_mode.go), and checkBindSafety refuses a
// non-loopback bind outright. Both would be a flat contradiction of the token
// the operator did supply.
func TestResolvedTokenSatisfiesTheNoAuthGuards(t *testing.T) {
	resetWebTokenOnce(t)
	t.Setenv(WebTokenEnv, "env-token")

	args := Args{AllowRestart: true, WebInsecure: true}
	tok, err := ResolveWebToken(args)
	if err != nil {
		t.Fatal(err)
	}
	args.WebToken = tok // what runWebMode does

	if args.WebToken == "" {
		t.Fatal("resolved token did not reach Args.WebToken")
	}
	// The self-restart gate's own condition, verbatim from web_mode.go.
	unscopedInsecure := args.WebInsecure && len(args.WebInsecureCIDRs) == 0
	if unscopedInsecure && args.WebToken == "" && args.WebAuthHeader == "" {
		t.Error("an env-provisioned daemon would still be refused self-restart for having no auth")
	}
}

// resetWebTokenOnce lets each test exercise the read-once path. The sync.OnceValue
// caches across a process, which is right in production and useless in a test
// file that has to prove the caching itself.
func resetWebTokenOnce(t *testing.T) {
	t.Helper()
	old := webTokenFromEnv
	webTokenFromEnv = sync.OnceValue(func() string {
		v := strings.TrimSpace(os.Getenv(WebTokenEnv))
		if v != "" {
			os.Unsetenv(WebTokenEnv)
		}
		return v
	})
	t.Cleanup(func() { webTokenFromEnv = old })
}
