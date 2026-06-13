package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Print mode persists a real session, so a session-keyed extension must
// receive a session_start carrying the real (non-empty) session id —
// not the "no active session" empty sentinel. Regression for the
// headless session-identity bug: headless modes used to fire only a bare
// session_start, silently degrading per-session extension state.
func TestPrintMode_DeliversSessionIdentityToExtension(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	fp := newFakeProvider(t, sseScript{
		chunks:   []map[string]any{textChunk("ok"), finishChunk("stop", 5, 1)},
		sendDone: true,
	})
	ws := t.TempDir()
	home := t.TempDir()

	// A session-keyed recorder extension: subscribe to session_start and
	// write the frame it receives into its own dir (cmd.Dir == ext dir,
	// so a relative path lands there).
	extDir := filepath.Join(home, "extensions", "recorder")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' '{"type":"hello","name":"recorder","version":"1.0","capabilities":["events"]}'
printf '%s\n' '{"type":"subscribe","events":["session_start"]}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"event":"session_start"'*) printf '%s' "$line" > recorded.json ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(extDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "recorder", "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	// Run print mode WITH a session and extensions enabled (the opposite
	// of the default runTerva helper, which forces --no-session/--no-ext).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tervaBin,
		"--provider", "openai-compatible", "--model", "test-model",
		"--base-url", fp.url(), "--api-key", "e2e-test-key",
		"--cwd", ws, "-p", "say ok")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TERVA_HOME=" + home,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run terva: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// The recorder must have seen session_start with a real session id.
	data, err := os.ReadFile(filepath.Join(extDir, "recorded.json"))
	if err != nil {
		t.Fatalf("recorder never received session_start: %v\nstderr:\n%s", err, stderr.String())
	}
	var ev struct {
		Event     string `json:"event"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("bad recorded frame %q: %v", data, err)
	}
	if ev.Event != "session_start" {
		t.Fatalf("recorded frame is not session_start: %q", data)
	}
	if ev.SessionID == "" {
		t.Errorf("session_start carried an empty session id — extension would lose per-session state: %q", data)
	}
}
