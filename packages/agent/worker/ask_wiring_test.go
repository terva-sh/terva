package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// recordingSink captures everything the runner reports, so a test can assert on
// what did NOT reach the transcript as well as what did.
type recordingSink struct {
	mu   sync.Mutex
	tran []string
}

func (s *recordingSink) Activity(string) {}
func (s *recordingSink) Transcript(chunk string) {
	s.mu.Lock()
	s.tran = append(s.tran, chunk)
	s.mu.Unlock()
}
func (s *recordingSink) Result(string) {}
func (s *recordingSink) GuardNudge()   {}

func (s *recordingSink) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tran...)
}

// TestAnAskLineFromTheWorkerReachesTheConfirmer drives the rpc-native ask
// carrier through the path that ships: a real child process writes an `ask` line
// on stdout, Run's drain loop reads it, RecognizeAsk claims it, and the reply
// goes back down the child's stdin.
//
// handleAsk itself was well covered — by tests that CALL IT DIRECTLY. Nothing
// exercised the three lines in the drain loop that decide whether it is called
// at all, so a refactor could stop recognising asks entirely and every approval
// test would still pass while a gated worker hung forever waiting for a verdict
// nobody was listening for.
func TestAnAskLineFromTheWorkerReachesTheConfirmer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts a POSIX shell child")
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	resolved := loadedRepo(t)
	dir := testsupport.TempDir(t)
	replyPath := filepath.Join(dir, "reply.json")

	var askedTool, askedPreview string
	backend := Backend{
		Name:          "fake-ask",
		SelfAssembles: true,
		Translate: func(line []byte) []Event {
			var m map[string]any
			if json.Unmarshal(line, &m) != nil {
				return nil
			}
			typ, _ := m["type"].(string)
			return []Event{{Type: typ, Data: m}}
		},
		// Required, and load-bearing here: pumpStdin CLOSES stdin outright for a
		// backend with no Steer (it took its task on argv), and the child would
		// then hit EOF before any verdict could be written.
		Steer: func(text string) ([]byte, error) {
			b, err := json.Marshal(map[string]any{"type": "turn", "text": text})
			return append(b, '\n'), err // newline-terminated, as EncodeApprove must be
		},
		RecognizeAsk: func(ev Event) (Ask, bool) {
			if ev.Type != "ask" {
				return Ask{}, false
			}
			id, _ := ev.Data["id"].(string)
			tool, _ := ev.Data["tool"].(string)
			preview, _ := ev.Data["preview"].(string)
			return Ask{ID: id, Tool: tool, Preview: preview}, true
		},
		EncodeApprove: func(id string, d core.ConfirmDecision) ([]byte, error) {
			b, err := json.Marshal(map[string]any{"type": "approve", "id": id, "allow": d.Allow})
			// NEWLINE-TERMINATED. writeStdin writes the frame verbatim, so a
			// child reading lines never sees one without it — this fake was
			// written without the newline first and the worker blocked forever,
			// which is the failure a real backend would produce too.
			return append(b, '\n'), err
		},
		Command: func(Dispatch) (*exec.Cmd, error) {
			// Emit one ask, then block on stdin for the verdict and record it.
			// Exactly the shape a gated worker has: it cannot proceed until the
			// runner answers, so a runner that never recognises the ask hangs it.
			// Read until the APPROVE frame: stdin has two writers (pumpStdin
			// sends the opening turn down the same pipe), so the first line the
			// child sees is the prompt, not the verdict.
			return exec.Command("sh", "-c",
				`printf '{"type":"ask","id":"a-1","tool":"bash","preview":"rm -rf /tmp/x"}\n'
while IFS= read -r reply; do
  case "$reply" in
    *'"type":"approve"'*) printf '%s' "$reply" > `+replyPath+`; exit 0 ;;
  esac
done`), nil
		},
	}

	confirmer := confirmFunc(func(_ context.Context, tool, preview string) core.ConfirmDecision {
		askedTool, askedPreview = tool, preview
		return core.ConfirmDecision{Allow: true}
	})

	a := &swarm.Agent{
		ID:           "ask-wire-1",
		Dir:          dir,
		InboxPath:    filepath.Join(shortSocketDir(t), "in.sock"),
		EventLogPath: filepath.Join(dir, "events.jsonl"),
	}
	sink := &recordingSink{}
	r := NewRunner(a, backend, resolved, confirmer)

	// A backend WITH a Steer keeps pumpStdin alive relaying inbox steers, so Run
	// returns only when the context ends. Run it in the background, wait for the
	// evidence, then cancel — the shape a live worker has.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, sink) }()

	// Either outcome ends the wait: the child writes its reply and exits (so Run
	// returns), or it never gets one and we time out. The assertion is the FILE
	// in both cases — "Run returned" says nothing about whether the worker was
	// answered.
	deadline := time.Now().Add(20 * time.Second)
	ran := false
	for time.Now().Before(deadline) && !ran {
		if b, err := os.ReadFile(replyPath); err == nil && len(b) > 0 {
			break
		}
		select {
		case <-done:
			ran = true
		case <-time.After(25 * time.Millisecond):
		}
	}
	cancel()
	if !ran {
		<-done
	}
	raw, _ := os.ReadFile(replyPath)

	if askedTool != "bash" {
		t.Fatalf("the confirmer was asked about %q, want \"bash\" — the drain loop never routed the "+
			"ask line, so a gated worker would sit waiting for a verdict nobody is listening for", askedTool)
	}
	if !strings.Contains(askedPreview, "rm -rf /tmp/x") {
		t.Errorf("preview = %q, want the worker's own preview text", askedPreview)
	}

	// The reply reached the child, in the shape EncodeApprove produced.
	if len(raw) == 0 {
		t.Fatal("the worker never received a reply on stdin — the drain loop recognised the ask " +
			"but nothing was written back, so a gated worker would block forever")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("reply is not the encoded frame: %q", raw)
	}
	if got["type"] != "approve" || got["id"] != "a-1" || got["allow"] != true {
		t.Errorf("reply frame = %v, want the approve for a-1", got)
	}

	// An ask is a QUESTION, not a transcript line: the drain loop `continue`s
	// past IngestEvent so it is never mirrored to the sink. Without this the
	// interception could be deleted and the test above would still pass —
	// handleAsk would run AND the raw ask would be surfaced as agent output.
	for _, line := range sink.lines() {
		if strings.Contains(line, "rm -rf /tmp/x") && !strings.HasPrefix(line, "approval:") {
			t.Errorf("the ask was mirrored to the transcript as %q — it is a question the runner "+
				"answers, not output the operator reads", line)
		}
	}
	// …and the verdict IS reported, so the operator can see what was allowed.
	if !strings.Contains(strings.Join(sink.lines(), "\n"), "approval: allowed bash") {
		t.Errorf("the verdict never reached the transcript: %q", sink.lines())
	}
}
