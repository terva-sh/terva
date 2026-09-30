package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

// talkoot.worker reports a worker member's swarm agent, which the tasks
// surface cannot show the view: that list is scoped to the session asking,
// and the worker belongs to the talkoot's address.
func TestTalkootWorkerReportsTheSeatedAgent(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	code := func(err error) string {
		var ce *ctrlproto.Error
		if errors.As(err, &ce) {
			return ce.Code
		}
		return ""
	}
	if _, err := w.TalkootWorker(ctx, ctrlproto.TalkootWorkerParams{ID: "crew", Member: "jev"}); code(err) != ctrlproto.CodeNotFound || !strings.Contains(err.Error(), "first delivery") {
		t.Errorf("before its first delivery: %v, want not found and the reason", err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Review the schema.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	got, err := w.TalkootWorker(ctx, ctrlproto.TalkootWorkerParams{ID: "crew", Member: "jev"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "agent-1" || got.Status != "running" {
		t.Errorf("worker = %+v, want agent-1 running", got)
	}
	if _, err := w.TalkootWorker(ctx, ctrlproto.TalkootWorkerParams{ID: "crew", Member: "helm"}); code(err) != ctrlproto.CodeBadRequest {
		t.Errorf("a native member: %v, want bad request", err)
	}
	if _, err := w.TalkootWorker(ctx, ctrlproto.TalkootWorkerParams{ID: "crew", Member: "nobody"}); code(err) != ctrlproto.CodeNotFound {
		t.Errorf("an unknown member: %v, want not found", err)
	}
	fw.mu.Lock()
	delete(fw.live, "agent-1")
	fw.mu.Unlock()
	if _, err := w.TalkootWorker(ctx, ctrlproto.TalkootWorkerParams{ID: "crew", Member: "jev"}); code(err) != ctrlproto.CodeNotFound || !strings.Contains(err.Error(), "not in the swarm") {
		t.Errorf("a worker the swarm dropped: %v, want not found", err)
	}
}

// A real swarm hides a talkoot worker from the tasks list of a session,
// because the worker spawns under the talkoot's address, and the worker host
// still reports it. That scope is why talkoot.worker exists.
func TestARealSwarmShowsATalkootWorkerOnlyThroughTheHost(t *testing.T) {
	root := testsupport.TempDir(t)
	f := swarm.New(swarm.Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, _ swarm.Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	m := talkoot.Member{ID: "jev", Driver: "claude"}
	req, err := (&Workspace{}).workerRequest(&talkootRun{id: "crew"}, m, "Start.")
	if err != nil {
		t.Fatal(err)
	}
	id, err := swarmWorkers{f: f}.spawn(context.Background(), req, workerEvents{turnEnd: func(string, float64, string, int, string, bool) {}, exit: func(string, float64) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Stop(id); f.Get(id).Wait() })
	for _, s := range f.SnapshotFor("some-session") {
		if s.ID == id {
			t.Fatal("a focused session's tasks list shows the talkoot's worker; the verb would not be needed")
		}
	}
	snap, ok := swarmWorkers{f: f}.agentSnapshot(id)
	if !ok || snap.ID != id {
		t.Fatalf("the host reports %+v, %v for worker %s", snap, ok, id)
	}
	if got := taskInfo(snap); got.ID != id || got.Status == "" {
		t.Errorf("task info %+v", got)
	}
}
