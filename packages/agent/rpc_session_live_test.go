package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestLiveRPCSessionResume proves resume on the rpc carrier end to end with the
// REAL parts: two separate `terva rpc --session <path>` processes sharing one
// session file. The first is told a codeword and exits; the second — a fresh
// process, the way a revived worker comes back — reopens the same session and is
// asked to recall it. A correct recall can only come from the restored
// transcript, so it proves the conversation survived the process dying.
//
// Guarded behind TERVA_LIVE_RESUME=1 because it drives a real model. It defaults
// to the machine's configured provider/model (like the other live smokes); set
// TERVA_LIVE_RESUME_PROVIDER / TERVA_LIVE_RESUME_MODEL to pin a cheap or free one
// (validated against openrouter openai/gpt-oss-20b:free — zero spend). Re-run:
//
//	TERVA_LIVE_RESUME=1 go test ./packages/agent -run TestLiveRPCSessionResume -v
func TestLiveRPCSessionResume(t *testing.T) {
	if os.Getenv("TERVA_LIVE_RESUME") != "1" {
		t.Skip("live model; set TERVA_LIVE_RESUME=1 to run")
	}

	tervaBin := buildTervaForResume(t)
	cwd := testsupport.TempDir(t)
	sessPath := filepath.Join(testsupport.TempDir(t), "worker.json")
	const codeword = "BANANA42"

	// Turn 1: a first process establishes the session and the fact.
	turn1 := rpcResumeTurn(t, tervaBin, sessPath, cwd,
		"Remember this codeword for later: "+codeword+". Reply with just OK.")
	t.Logf("turn 1 (establish) assistant: %q", turn1)
	if _, err := os.Stat(sessPath); err != nil {
		t.Fatalf("the first turn did not persist a session at %s: %v", sessPath, err)
	}

	// Turn 2: a SECOND process — the revival — reopens the same session and must
	// recall the codeword from the restored transcript alone.
	turn2 := rpcResumeTurn(t, tervaBin, sessPath, cwd,
		"What was the codeword I asked you to remember earlier? Reply with only the word.")
	t.Logf("turn 2 (resume) assistant: %q", turn2)
	if !strings.Contains(turn2, codeword) {
		t.Fatalf("the revived process did not recall the codeword: reply %q lacks %q — the session did not resume", turn2, codeword)
	}
}

// rpcResumeTurn runs one `terva rpc --session` process, feeds it a single prompt,
// reads the stream until `done`, and returns the concatenated assistant text.
// Closing stdin after the prompt is EOF, which lets the process finish its turn
// and exit — the same clean shutdown a supervisor's stdin close performs.
func rpcResumeTurn(t *testing.T, tervaBin, sessPath, cwd, prompt string) string {
	t.Helper()
	args := []string{"rpc", "--session", sessPath, "--cwd", cwd, "--approval", "yolo"}
	if p := os.Getenv("TERVA_LIVE_RESUME_PROVIDER"); p != "" {
		args = append(args, "--provider", p)
	}
	if m := os.Getenv("TERVA_LIVE_RESUME_MODEL"); m != "" {
		args = append(args, "--model", m)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tervaBin, args...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start terva rpc: %v", err)
	}

	line, _ := json.Marshal(map[string]any{"type": "prompt", "message": prompt})
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	_ = stdin.Close() // EOF after the one prompt: the turn runs, then the process exits

	var assistant strings.Builder
	var streamErr string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 4<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Error   string `json:"error"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant_message":
			for _, b := range ev.Message.Content {
				if b.Type == "text" {
					assistant.WriteString(b.Text)
				}
			}
		case "error":
			streamErr = ev.Error
		case "done":
			// terminal; keep draining until stdout closes
		}
	}
	_ = cmd.Wait()
	if streamErr != "" {
		t.Fatalf("terva rpc turn errored (provider/model configured?): %s", streamErr)
	}
	return strings.TrimSpace(assistant.String())
}

func buildTervaForResume(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "terva")
	cmd := exec.Command("go", "build", "-o", out, "terva.sh/terva/cmd/terva")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build terva: %v\n%s", err, b)
	}
	return out
}
