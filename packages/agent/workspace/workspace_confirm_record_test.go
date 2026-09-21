package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A decision a person takes is written to the transcript as a permission row,
// and an answered question as an ask row, so a replay can show both. A
// cancelled prompt writes nothing.
func TestWebConfirmerAndAskerRecordExchanges(t *testing.T) {
	s := newTestSession()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	s.sess = sess
	// A transcript with no message is discarded on close, so give it one.
	if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}); err != nil {
		t.Fatal(err)
	}

	c := &webConfirmer{s: s}
	decided := make(chan core.ConfirmDecision, 1)
	go func() {
		decided <- c.ConfirmWithRequest(context.Background(), core.ConfirmRequest{Tool: "bash", Preview: "go test", CallID: "call-1"})
	}()
	waitFor(t, "a parked permission", func() bool { return s.permPark.Len() == 1 })
	s.approve("call-1", core.ConfirmDecision{Allow: false, Reason: "not now"})
	if d := <-decided; d.Allow {
		t.Fatal("decision lost")
	}

	a := &webAsker{s: s}
	answered := make(chan []core.UserAnswer, 1)
	go func() {
		ans, _ := a.Ask(context.Background(), []core.UserQuestion{{Question: "which?", Options: []string{"a", "b"}}})
		answered <- ans
	}()
	waitFor(t, "a parked ask", func() bool { return s.askPark.Len() == 1 })
	s.answer("ask_1", []core.UserAnswer{{Answer: "b"}})
	if ans := <-answered; len(ans) != 1 || ans[0].Answer != "b" {
		t.Fatalf("answers: %+v", ans)
	}

	// A cancelled prompt is not an exchange.
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan struct{})
	go func() {
		c.ConfirmWithRequest(ctx, core.ConfirmRequest{Tool: "bash", CallID: "call-2"})
		close(cancelled)
	}()
	waitFor(t, "a parked permission", func() bool { return s.permPark.Len() == 1 })
	cancel()
	<-cancelled

	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	rows, _, err := core.ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1].Kind != core.ReplayRowPermission || rows[2].Kind != core.ReplayRowAsk {
		t.Fatalf("rows: %+v", rows)
	}
	p := rows[1].Permission
	if p.CallID != "call-1" || p.Tool != "bash" || p.Preview != "go test" || p.Allow || p.Reason != "not now" || p.Asked.IsZero() || p.Waited <= 0 {
		t.Errorf("permission row: %+v", p)
	}
	q := rows[2].Ask
	if q.AskID != "ask_1" || len(q.Questions) != 1 || q.Questions[0].Question != "which?" || len(q.Answers) != 1 || q.Answers[0].Answer != "b" || q.Waited <= 0 {
		t.Errorf("ask row: %+v", q)
	}
}
