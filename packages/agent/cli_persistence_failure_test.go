package agent

import (
	"errors"
	"os"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestWriteNewTranscriptReportsFailure(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "test", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "saved"}}}
	if err := sess.AppendMessage(msg); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	ag := core.NewAgent(nil, "test", "", nil)
	ag.SetMessages([]provider.Message{msg, msg})
	before, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := WriteNewTranscript(ag, sess, 1); !errors.Is(err, core.ErrPersistence) {
			t.Fatalf("catch-up persistence returned %v; want persistence failure", err)
		}
	}
	after, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("retry changed stored history")
	}
}
