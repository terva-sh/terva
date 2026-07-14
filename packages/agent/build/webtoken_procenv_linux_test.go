//go:build linux

package build

// TW-005: the /proc/<pid>/environ secrecy contract, proven end to end on Linux.
//
// The token has to be present at execve() for the question to mean anything —
// /proc/<pid>/environ reflects the process's initial-stack env block, so a var
// installed later with os.Setenv (all t.Setenv does) never lands there, and a
// naive in-process check would read clean for the wrong reason. So the driver
// re-execs the test binary with the var (or a token file) in the child's launch
// environment, and the child resolves, then inspects its own /proc/self/environ
// via webTokenInProcEnviron — the exact function the shipped startup warning
// uses.
//
// The child asserts the SELF read, not a cross-process one. A sibling reading
// /proc/<pid>/environ needs the target process dumpable, and under `go test
// -race` the cgo race runtime makes the test binary non-dumpable, so a sibling
// check flakes there (self-reads always work). The cross-process recovery is the
// real attack — a separate agent-shell process reading the daemon's environ —
// and is exercised in the field on the shipped (non-race) binary; the self-read
// is the portable, race-robust regression check, and it is the same code path
// the daemon's warning fires on.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

const procEnvironCanary = "tw005-canary-2f9c1a7b"

func TestTW005_ProcEnvironContract(t *testing.T) {
	switch os.Getenv("TW005_ROLE") {
	case "env-child":
		tok, err := ResolveWebToken(Args{})
		if err != nil {
			t.Fatalf("env resolve: %v", err)
		}
		if tok != procEnvironCanary {
			t.Fatalf("env token = %q, want the canary", tok)
		}
		if _, ok := os.LookupEnv(WebTokenEnv); ok {
			t.Fatal("scrub failed: token still in os.Environ()")
		}
		if !webTokenInProcEnviron() {
			t.Fatal("env route: webTokenInProcEnviron()=false, but the value must remain in /proc/self/environ after the scrub")
		}
		return
	case "file-child":
		tok, err := ResolveWebToken(Args{WebTokenFile: os.Getenv("TW005_TOKEN_FILE")})
		if err != nil {
			t.Fatalf("file resolve: %v", err)
		}
		if tok != procEnvironCanary {
			t.Fatalf("file token = %q, want the canary", tok)
		}
		if webTokenInProcEnviron() {
			t.Fatal("file route: the token must not appear in /proc/self/environ")
		}
		return
	}

	// Driver — env route: canary on the child's stack at execve. The child
	// asserts the scrub cleared os.Environ() yet the value survives in
	// /proc/self/environ, and the daemon warns about it.
	env := runTokenChild(t, "env-child", []string{WebTokenEnv + "=" + procEnvironCanary})
	if !bytes.Contains(env, []byte("remains in /proc")) {
		t.Errorf("env route should warn that the token remains in /proc/<pid>/environ; child output:\n%s", env)
	}
	if bytes.Contains(env, []byte(procEnvironCanary)) {
		t.Errorf("the warning path leaked the token value into output; child output:\n%s", env)
	}

	// Driver — file route: canary in a 0600 file, nothing in the environment.
	tf := filepath.Join(testsupport.TempDir(t), "token")
	if err := os.WriteFile(tf, []byte(procEnvironCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	file := runTokenChild(t, "file-child", []string{"TW005_TOKEN_FILE=" + tf})
	if bytes.Contains(file, []byte("remains in /proc")) {
		t.Errorf("file route must not warn about /proc; child output:\n%s", file)
	}
}

// runTokenChild re-execs this test binary running only this test, in the given
// role, with extraEnv added to a base environment scrubbed of any ambient
// TERVA_WEB_TOKEN / TW005_* so the child's launch env is exactly what we set.
func runTokenChild(t *testing.T, role string, extraEnv []string) []byte {
	t.Helper()
	var base []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, WebTokenEnv+"=") || strings.HasPrefix(kv, "TW005_") {
			continue
		}
		base = append(base, kv)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTW005_ProcEnvironContract$", "-test.v")
	cmd.Env = append(base, append([]string{"TW005_ROLE=" + role}, extraEnv...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s child failed: %v\n%s", role, err, out)
	}
	return out
}
