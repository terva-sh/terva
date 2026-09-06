package build

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

type persistenceFailureClient struct {
	calls  int
	onCall func()
}

func (c *persistenceFailureClient) Name() string { return "persistence-test" }
func (c *persistenceFailureClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	c.calls++
	if c.onCall != nil {
		c.onCall()
	}
	out := make(chan provider.Event, 3)
	out <- provider.EventTextDelta{Delta: "reply"}
	out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 10, OutputTokens: 1}}
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "reply"}}}}
	close(out)
	return out, nil
}

func TestPersistenceFailureReachesHeadlessCaller(t *testing.T) {
	for _, where := range []string{"user", "reply", "compaction", "delegated usage"} {
		t.Run(where, func(t *testing.T) {
			dir := testsupport.TempDir(t)
			sess, err := core.NewSession(dir, dir, "test", "test", "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sess.Close() })
			cl := &persistenceFailureClient{}
			ag := core.NewAgent(cl, "test", "", nil)
			WireHeadlessSessionPersist(ag, sess)
			if err := ag.Prompt(context.Background(), "first saved turn", nil, nil); err != nil {
				t.Fatal(err)
			}
			before := cl.calls
			if where == "user" || where == "delegated usage" {
				if err := sess.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cl.onCall = func() { _ = sess.Close(); cl.onCall = nil }
			}
			switch where {
			case "compaction":
				_, err = ag.Compact(context.Background(), 0, nil)
			case "delegated usage":
				ag.RecordDelegatedUsage(provider.Usage{InputTokens: 5})
				err = ag.Continue(context.Background(), nil)
			default:
				err = ag.Prompt(context.Background(), "unsaved turn", nil, nil)
			}
			if !errors.Is(err, core.ErrPersistence) {
				t.Errorf("operation returned %v; want persistence failure", err)
			}
			if (where == "user" || where == "delegated usage") && cl.calls != before {
				t.Error("provider ran after persistence had already failed")
			}
			calls, count := cl.calls, len(ag.Messages())
			if err := ag.Prompt(context.Background(), "retry must not append", nil, nil); err == nil {
				t.Error("later prompt did not report the unresolved persistence failure")
			}
			if cl.calls != calls || len(ag.Messages()) != count {
				t.Error("failed handle admitted another turn or appended duplicate history")
			}
		})
	}
}
