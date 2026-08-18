package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// recordingHost captures what the bridge actually submits to the agent.
type recordingHost struct {
	prompts []string
	images  [][]provider.ImageBlock
	status  string
}

func (h *recordingHost) SubmitOrQueue(prompt string, images []provider.ImageBlock) {
	h.prompts = append(h.prompts, prompt)
	h.images = append(h.images, images)
}
func (h *recordingHost) Status() string     { return h.status }
func (h *recordingHost) CancelTurn()        {}
func (h *recordingHost) Notify(_, _ string) {}

// stagedFile writes a file where a connector would have staged it and returns
// the attachment describing it.
func stagedFile(t *testing.T, name string) FileAttachment {
	t.Helper()
	dir := filepath.Join(testsupport.TempDir(t), "msg-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	return FileAttachment{Path: path, Name: name, Kind: "document", MimeType: "application/pdf", Size: 7}
}

// The gate was factored out so the Loop and the Bridge could not drift on what
// is ADMITTED. Everything after the gate did drift.
//
// Bridge.handle forwarded m.Text and m.Images and dropped m.Files. The
// connector had already staged the document on disk, the gate had already
// approved it, and the agent was never told it existed — so a user who sent a
// PDF to a bridged chat got an answer about their sentence and nothing about
// their file.
func TestTheBridgeTellsTheAgentAboutAttachedFiles(t *testing.T) {
	host := &recordingHost{}
	b := &Bridge{Host: host}
	f := stagedFile(t, "report.pdf")

	got := bridgePrompt(Message{Text: "what does this say?", Files: []FileAttachment{f}})

	if !strings.Contains(got, "report.pdf") {
		t.Errorf("the prompt never names the attached file:\n%s", got)
	}
	if !strings.Contains(got, f.Path) {
		t.Errorf("the prompt never gives the path the agent must read:\n%s", got)
	}
	if !strings.Contains(got, "what does this say?") {
		t.Errorf("the user's own text was lost:\n%s", got)
	}
	_ = b
}

// The complement: a message with no files must be forwarded unchanged. A
// manifest prepended to every prompt would put boilerplate in front of every
// sentence a user types.
func TestAFilelessPromptIsForwardedVerbatim(t *testing.T) {
	if got := bridgePrompt(Message{Text: "hello"}); got != "hello" {
		t.Errorf("bridgePrompt(%q) = %q, want it unchanged", "hello", got)
	}
}

// The Loop owns its files through a deferred cleanup that runs whichever way
// the turn ends. The Bridge had none, so every attachment a bridged chat
// received stayed in its host-owned directory for the life of the machine.
func TestTheBridgeCleansUpStagedFilesAfterTheTurn(t *testing.T) {
	f := stagedFile(t, "voice.ogg")
	b := &Bridge{Host: &recordingHost{}}

	b.mu.Lock()
	b.pendingFiles = append(b.pendingFiles, f)
	b.mu.Unlock()

	if _, err := os.Stat(f.Path); err != nil {
		t.Fatalf("fixture file missing before the turn: %v", err)
	}

	b.releasePendingFiles()

	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Errorf("%s survived the turn — a bridged chat leaks every attachment it receives", f.Path)
	}
	if _, err := os.Stat(filepath.Dir(f.Path)); !os.IsNotExist(err) {
		t.Errorf("the per-message directory survived: %s", filepath.Dir(f.Path))
	}
}

// Releasing must be idempotent and must not reach files from a later prompt: a
// second call after the list is drained has nothing to remove.
func TestReleasingTwiceRemovesNothingExtra(t *testing.T) {
	f := stagedFile(t, "a.pdf")
	b := &Bridge{Host: &recordingHost{}}
	b.mu.Lock()
	b.pendingFiles = append(b.pendingFiles, f)
	b.mu.Unlock()

	b.releasePendingFiles()
	b.releasePendingFiles() // must not panic, must not touch anything else

	b.mu.Lock()
	left := len(b.pendingFiles)
	b.mu.Unlock()
	if left != 0 {
		t.Errorf("%d file(s) still pending after release", left)
	}
}
