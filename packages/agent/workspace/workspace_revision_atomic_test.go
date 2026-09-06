package workspace

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestConcurrentRevisionUsesEpochOnce(t *testing.T) {
	for round := range 30 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			dir := testsupport.TempDir(t)
			sess, err := core.NewSession(dir, dir, "test", "test", "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sess.Close() })
			s := &wsSession{sess: sess, agent: core.NewAgent(nil, "test", "", nil), hub: newWSHub()}
			var msgs []provider.Message
			for i := range 32 {
				m := swipeMsg(provider.RoleUser, fmt.Sprint(i))
				msgs = append(msgs, m)
				if err := sess.AppendMessage(m); err != nil {
					t.Fatal(err)
				}
			}
			s.agent.SetMessages(msgs)
			epoch := s.agent.TranscriptEpoch()
			start := make(chan struct{})
			errs := make(chan error, 16)
			var wg sync.WaitGroup
			for range 16 {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; errs <- s.deleteMessage(epoch, 0) }()
			}
			close(start)
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				}
			}
			if success != 1 || len(s.agent.Messages()) != 31 {
				t.Fatalf("same-epoch deletes accepted %d times; transcript has %d messages", success, len(s.agent.Messages()))
			}
			reopened, replay, err := core.OpenSession(sess.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if !reflect.DeepEqual(reviseTexts(replay), reviseTexts(s.agent.Messages())) {
				t.Fatal("live transcript differs from replay after concurrent deletes")
			}
		})
	}
}

func TestCompactionExcludesRevisionsAndTurnAdmission(t *testing.T) {
	for _, op := range []string{"edit", "delete", "clear", "turn"} {
		t.Run(op, func(t *testing.T) {
			cl := &blockingCompactClient{inFlight: make(chan struct{}), release: make(chan struct{})}
			s := compactQueueSession(t, cl)
			done := make(chan error, 1)
			go func() { done <- s.compact(context.Background()) }()
			select {
			case <-cl.inFlight:
			case <-time.After(5 * time.Second):
				t.Fatal("compaction did not start")
			}
			before := s.agent.Messages()
			epoch := s.agent.TranscriptEpoch()
			var err error
			switch op {
			case "edit":
				err = s.editMessage(epoch, 0, "raced")
			case "delete":
				err = s.deleteMessage(epoch, 0)
			case "clear":
				err = s.clear()
			case "turn":
				_, err = s.beginTurn()
			}
			if err != ctrlproto.ErrBusy {
				t.Errorf("%s during compaction returned %v; want ErrBusy", op, err)
			}
			if !reflect.DeepEqual(s.agent.Messages(), before) {
				t.Error("operation changed the transcript during compaction")
			}
			close(cl.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
