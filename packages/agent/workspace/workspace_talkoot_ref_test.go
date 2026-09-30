package workspace

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The view opens a path: reference from the roster's home checkout, the
// daemon's own directory, and refuses one that leaves it.
func TestAPathReferenceOpensFromTheHomeCheckout(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "coding", ID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{Template: "coding", ID: "team", Digest: pv.Digest}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "PLAN.md"), []byte("ship it"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := w.OpenTalkootRef(ctx, ctrlproto.TalkootOpenRefParams{ID: "team", Ref: "path:PLAN.md"})
	if err != nil || got.Text != "ship it" || got.Ref != "path:PLAN.md" {
		t.Fatalf("path:PLAN.md = %+v, %v", got, err)
	}
	if _, err := w.OpenTalkootRef(ctx, ctrlproto.TalkootOpenRefParams{ID: "team", Ref: "path:../outside"}); err == nil {
		t.Error("a path out of the checkout was opened")
	}
	if _, err := w.OpenTalkootRef(ctx, ctrlproto.TalkootOpenRefParams{ID: "nope", Ref: "path:PLAN.md"}); err == nil {
		t.Error("a reference in a talkoot that does not exist was opened")
	}
}

// A tool call in a member's session reaches the room's watchers as a status
// event, so the sidebar names it without a fresh read.
func TestAToolCallSendsTheMembersStatus(t *testing.T) {
	cwd := talkootHome(t)
	release := make(chan struct{})
	w := openTalkootWorkspaceWith(t, cwd, func(rw http.ResponseWriter, r *http.Request) {
		// The coordinator's turn waits here, so it stays working.
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.Error(rw, "released", http.StatusServiceUnavailable)
	})
	// Registered after the workspace, so it runs before the workspace closes.
	t.Cleanup(func() { close(release) })
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var tools []string
	stop := w.talkootWatch("crew", func(ev talkootEvent) {
		if ev.Kind != "status" {
			return
		}
		for _, s := range ev.Status {
			if s.Member == "helm" {
				mu.Lock()
				tools = append(tools, s.Tool)
				mu.Unlock()
			}
		}
	})
	defer stop()
	if _, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm to work in its session", func() bool {
		v := memberView(t, w, "crew", "helm")
		return v.Session != "" && v.Status.Working
	})
	session := memberView(t, w, "crew", "helm").Session
	w.talkootActivity(session, "c1", "grep")
	w.talkootActivity(session, "", "")
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(tools, "grep") || tools[len(tools)-1] != "" {
		t.Errorf("status events for helm carried tools %q, want grep and then none", tools)
	}
}
