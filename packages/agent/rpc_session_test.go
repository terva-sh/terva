package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// rpcEchoClient is a provider.Client that answers each turn by echoing the last
// user message — enough to drive a real agent turn through runPrompt with no
// model or network, so the persistence path can be tested deterministically.
type rpcEchoClient struct{}

func (rpcEchoClient) Name() string { return "echo" }

func (rpcEchoClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	last := ""
	if n := len(req.Messages); n > 0 {
		for _, b := range req.Messages[n-1].Content {
			if tb, ok := b.(provider.TextBlock); ok {
				last += tb.Text
			}
		}
	}
	reply := "echo: " + last
	msg := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: reply}},
	}
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "echo", Model: req.Model}
		out <- provider.EventTextDelta{Delta: reply}
		// The committed assistant message comes from EventDone.Message, not the
		// streamed deltas — the deltas are for display only.
		out <- provider.EventDone{Stop: provider.StopEnd, Message: msg}
	}()
	return out, nil
}

// rpcSyncWriter is a mutex-guarded discard sink for the frames runPrompt emits;
// this test cares about the session file, not the wire.
type rpcSyncWriter struct{ mu sync.Mutex }

func (w *rpcSyncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(p), nil
}

func transcriptText(msgs []provider.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		for _, b := range m.Content {
			if tb, ok := b.(provider.TextBlock); ok {
				sb.WriteString(string(m.Role))
				sb.WriteString(":")
				sb.WriteString(tb.Text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

// TestRPCSessionPersistsAndResumes proves the rpc carrier's resume: a run given a
// --session path persists every turn to that file (via WireHeadlessSessionPersist,
// the exact wiring runRPCMode installs), and a fresh process pointed at the same
// file reopens the transcript and continues on top of it rather than blank. This
// is what lets a terva-backed swarm worker be revived — the model-free proof of
// the whole loop, driving the real runPrompt through an echo client.
func TestRPCSessionPersistsAndResumes(t *testing.T) {
	dir := testsupport.TempDir(t)
	sessPath := filepath.Join(dir, "worker.json")

	// --- first process: fresh session, one turn, then it "dies" (Close). ---
	sess, err := core.NewSessionAtPath(sessPath, dir, "echo", "fake-model", "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	ag := core.NewAgent(rpcEchoClient{}, "fake-model", "sys", core.Registry{})
	build.WireHeadlessSessionPersist(ag, sess) // what runRPCMode wires when --session is set

	s := &rpcServer{ctx: context.Background(), agent: ag, out: &rpcSyncWriter{}}
	s.runPrompt("1", "first task", nil)
	sess.Close()

	// The turn reached disk: reopening the file restores the user prompt and the
	// assistant reply, in order.
	reopened, msgs, err := core.OpenSession(sessPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := transcriptText(msgs)
	if !strings.Contains(got, "user:first task") || !strings.Contains(got, "assistant:echo: first task") {
		t.Fatalf("first turn did not persist; transcript:\n%s", got)
	}

	// --- second process (the revival): restore the transcript, run another turn.
	// A revived worker must build ON the prior conversation, not restart it. ---
	ag2 := core.NewAgent(rpcEchoClient{}, "fake-model", "sys", core.Registry{})
	ag2.SetMessages(msgs) // what openOrCreateSession does on resume
	if n := len(ag2.Messages()); n != 2 {
		t.Fatalf("revived agent restored %d messages, want the 2 from the prior turn", n)
	}
	build.WireHeadlessSessionPersist(ag2, reopened)

	s2 := &rpcServer{ctx: context.Background(), agent: ag2, out: &rpcSyncWriter{}}
	s2.runPrompt("2", "second task", nil)
	reopened.Close()

	// Both turns are now in the durable transcript — the second stacked on the
	// first, proving continuity across the revival.
	_, all, err := core.OpenSession(sessPath)
	if err != nil {
		t.Fatalf("final reopen: %v", err)
	}
	final := transcriptText(all)
	for _, want := range []string{"user:first task", "assistant:echo: first task", "user:second task", "assistant:echo: second task"} {
		if !strings.Contains(final, want) {
			t.Errorf("resumed transcript missing %q; full:\n%s", want, final)
		}
	}
}

// TestRPCNoSessionPersistsNothing pins the default contract: without a session
// wired, running turns writes no transcript file — rpc stays stateless unless a
// --session path opts in. (The inverse guard for the resume feature: it must not
// start persisting for every rpc run.)
func TestRPCNoSessionPersistsNothing(t *testing.T) {
	ag := core.NewAgent(rpcEchoClient{}, "fake-model", "sys", core.Registry{})
	// No WireHeadlessSessionPersist — exactly the runRPCMode path when args.Session
	// is empty.
	s := &rpcServer{ctx: context.Background(), agent: ag, out: &rpcSyncWriter{}}
	s.runPrompt("1", "ephemeral task", nil)

	if _, path := ag.SessionIdentity(); path != "" {
		t.Errorf("a session-less rpc run adopted a transcript file %q; it must stay live-only", path)
	}
	if n := len(ag.Messages()); n != 2 {
		t.Errorf("turn should still run in memory; got %d messages, want 2", n)
	}
}
