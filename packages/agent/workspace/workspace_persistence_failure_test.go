package workspace

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestRevisionWriteFailurePreservesLiveState(t *testing.T) {
	for _, op := range []string{"edit older", "edit tail", "delete", "clear", "swipe", "swipe message", "prune", "drop", "post"} {
		t.Run(op, func(t *testing.T) {
			dir := testsupport.TempDir(t)
			sess, err := core.NewSession(dir, dir, "test", "test", "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sess.Close() })
			s := &wsSession{sess: sess, agent: core.NewAgent(nil, "test", "", nil), hub: newWSHub(), ws: &Workspace{ctx: context.Background()}}
			msgs := []provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0"), swipeMsg(provider.RoleUser, "u1"), swipeMsg(provider.RoleAssistant, "a1")}
			for _, m := range msgs {
				if err := sess.AppendMessage(m); err != nil {
					t.Fatal(err)
				}
			}
			s.agent.SetMessages(msgs)
			if err := s.editMessage(s.agent.TranscriptEpoch(), 1, "older alternative"); err != nil {
				t.Fatal(err)
			}
			if err := s.editMessage(s.agent.TranscriptEpoch(), 3, "tail alternative"); err != nil {
				t.Fatal(err)
			}
			before := s.snapshot()
			bytes, err := os.ReadFile(sess.Path)
			if err != nil {
				t.Fatal(err)
			}
			epoch := s.agent.TranscriptEpoch()
			// Fail after admission, so this exercises each write error path rather
			// than only the preflight check for an already-failed handle.
			err = s.revise(&epoch, func() error {
				if err := sess.Close(); err != nil {
					return err
				}
				switch op {
				case "edit older":
					return s.editMessageHeld(1, "unsaved")
				case "edit tail":
					return s.editMessageHeld(3, "unsaved")
				case "delete":
					return s.deleteMessageHeld(1)
				case "clear":
					return s.clearHeld()
				case "swipe":
					return s.swipeHeld(0)
				case "swipe message":
					return s.swipeMessageHeld(1, 0)
				case "prune":
					return s.pruneVariantsHeld(1)
				case "drop":
					return s.dropVariantHeld(1, 0)
				default:
					return s.postDirectedHeld("", "unsaved")
				}
			})
			if err == nil || s.agent.PersistenceError() == nil {
				t.Fatal("revision did not report and retain the storage failure")
			}
			after := s.snapshot()
			if !reflect.DeepEqual(before.Messages, after.Messages) || !reflect.DeepEqual(before.VariantMarks, after.VariantMarks) || before.Epoch != after.Epoch {
				t.Error("failed write changed live messages, variants, or epoch")
			}
			if _, err := s.beginTurn(); err == nil {
				t.Error("failed session admitted a new turn")
			}
			if err := s.retry(ctrlproto.TurnRetryParams{Epoch: epoch}); err == nil {
				t.Error("failed session admitted a retry")
			}
			if err := s.deleteMessage(epoch, 0); err == nil {
				t.Error("failed session admitted another revision")
			}
			stored, err := os.ReadFile(sess.Path)
			if err != nil {
				t.Fatal(err)
			}
			if string(stored) != string(bytes) {
				t.Error("failed operation or retry appended more rows")
			}
		})
	}
}

func TestPersistenceFailureKeepsQueuedInput(t *testing.T) {
	s := compactQueueSession(t, nil)
	ctx, err := s.beginTurn()
	if err != nil {
		t.Fatal(err)
	}
	s.agent.QueueMessage("first unsent message")
	s.agent.QueueMessage("second unsent message")
	s.agent.RecordPersistenceError(errors.New("synthetic storage failure"))
	if _, restart := s.endTurn(ctx, s.agent.PersistenceError()); restart {
		t.Fatal("failed persistence restarted the queued turn")
	}
	if s.agent.QueuedMessageCount() != 2 {
		t.Fatal("persistence failure discarded unsent input")
	}
}
