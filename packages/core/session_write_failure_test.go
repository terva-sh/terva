package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

type failingSessionWriter struct {
	dst  io.Writer
	fail bool
}

func TestPersistenceFailureStopsBeforeToolExecution(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		return calledATool(10), nil
	}}
	tool := &recordingTool{}
	ag := NewAgent(client, "test", "", Registry{"echo": tool})
	ag.MaxSteps = 1
	ag.AddMessageObserver(func(m provider.Message) {
		if m.Role == provider.RoleAssistant {
			ag.RecordPersistenceError(errSessionStorage)
		}
	})
	err := ag.Prompt(context.Background(), "run the tool", nil, nil)
	if !errors.Is(err, ErrPersistence) || !errors.Is(err, errSessionStorage) {
		t.Fatalf("Prompt returned %v; want the persistence failure", err)
	}
	if tool.lastArgs != nil {
		t.Fatal("tool ran after its call could not be persisted")
	}
}

var errSessionStorage = errors.New("synthetic storage failure")

func (w *failingSessionWriter) Write(p []byte) (int, error) {
	if w.fail {
		n, _ := w.dst.Write(p[:len(p)/2])
		return n, errSessionStorage
	}
	return w.dst.Write(p)
}

func TestSessionWriteFailureCannotBeRetriedThroughSameHandle(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "test", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := &failingSessionWriter{dst: s.writer}
	s.buf = bufio.NewWriter(w)
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "saved first"}}}
	if err := s.AppendMessage(msg); err != nil {
		t.Fatal(err)
	}
	w.fail = true
	if err := s.AppendMessage(msg); !errors.Is(err, errSessionStorage) {
		t.Fatalf("failed append returned %v", err)
	}
	partial, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Restoring the writer cannot establish whether the failed row reached disk.
	// The session must keep its own failure state, independent of bufio's latch.
	w.fail = false
	s.buf.Reset(w)
	if err := s.AppendMessage(msg); !errors.Is(err, errSessionStorage) {
		t.Errorf("retry after partial write returned %v; want original failure", err)
	}
	if err := s.AppendCompaction(nil, CompactResult{}); !errors.Is(err, errSessionStorage) {
		t.Errorf("checkpoint after partial write returned %v; want original failure", err)
	}
	after, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(partial) {
		t.Error("failed session wrote more bytes after an ambiguous partial append")
	}
	_ = s.Close()
	reopened, msgs, err := OpenSession(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(msgs) != 1 || len(reopened.LoadWarnings) == 0 {
		t.Fatal("recovery did not preserve the saved prefix and report the partial row")
	}
	if err := reopened.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "recovered"}}}); err != nil {
		t.Fatal(err)
	}
	replayed, err := ReadSessionMessages(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 {
		t.Errorf("recovered append was lost behind the partial row: got %d messages, want 2", len(replayed))
	}
}

func TestSessionFailedFirstWriteKeepsFile(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "test", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	s.buf = bufio.NewWriter(&failingSessionWriter{dst: s.writer, fail: true})
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "partly saved"}}}); err == nil {
		t.Fatal("fixture did not fail the write")
	}
	if err := s.Close(); !errors.Is(err, errSessionStorage) {
		t.Fatalf("Close returned %v", err)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("failed session was removed: %v", err)
	}
}
