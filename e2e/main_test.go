// Package e2e drives the COMPILED terva binary end to end: headless
// print/json modes against a fake openai-compatible provider served
// from httptest. This is the integration layer the June 2026 deep
// review found untested — args→Resolve→agent loop→provider wire→tool
// side effects — and where its four worst bug clusters lived (wrapper
// composition, abnormal stream termination, turn lifecycle, catalog
// refresh). Unit tests can't catch those; running the real binary can.
//
// Zero new dependencies: the fake provider is net/http/httptest, the
// binary is built once per `go test` run by TestMain below.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// tervaBin is the freshly-built binary under test, set by TestMain.
var tervaBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "terva-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		os.Exit(1)
	}
	tervaBin = filepath.Join(tmp, "terva")

	// Plain build (no -race): the test binary itself runs under
	// whatever flags `go test` got; the subprocess just needs to be
	// the real product. Inherits the environment so the module/build
	// caches are reused.
	build := exec.Command("go", "build", "-o", tervaBin, "terva.sh/terva/cmd/terva")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build terva: %v\n%s", err, out)
		os.RemoveAll(tmp)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// tervaResult is one finished subprocess run.
type tervaResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runTerva runs the built binary against the given fake-provider base
// URL with a fresh, isolated TERVA_HOME. The environment is constructed
// from scratch (not inherited) so the developer's real credentials,
// config, and provider env vars can never leak into a test, and runs
// are deterministic anywhere.
func runTerva(t *testing.T, baseURL, workspace string, args ...string) tervaResult {
	t.Helper()

	home := t.TempDir()
	full := append([]string{
		"--provider", "openai-compatible",
		"--model", "test-model",
		"--base-url", baseURL,
		"--api-key", "e2e-test-key",
		"--cwd", workspace,
		"--no-session",
		"--no-ext",
	}, args...)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, tervaBin, full...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TERVA_HOME=" + home,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := tervaResult{stdout: stdout.String(), stderr: stderr.String()}
	switch e := err.(type) {
	case nil:
		res.exitCode = 0
	case *exec.ExitError:
		res.exitCode = e.ExitCode()
	default:
		t.Fatalf("run terva: %v (ctx err: %v)\nstderr:\n%s", err, ctx.Err(), stderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("terva timed out\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	return res
}
