package workspace

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

func TestTalkootModelVisibilityDoesNotSeatOrCallAModel(t *testing.T) {
	cwd := talkootHome(t)
	var calls atomic.Int32
	w := openTalkootWorkspaceWith(t, cwd, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rw.WriteHeader(http.StatusBadRequest)
	})
	before := len(w.sessions)
	v, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))})
	if err != nil {
		t.Fatal(err)
	}
	assertModel := func(m ctrlproto.TalkootMember, provider, model, source string) {
		t.Helper()
		if m.ResolvedProvider != provider || m.ResolvedModel != model || m.ModelSource != source {
			t.Fatalf("model = %s/%s (%s), want %s/%s (%s)", m.ResolvedProvider, m.ResolvedModel, m.ModelSource, provider, model, source)
		}
	}
	assertModel(v.Members[0], "openai-compatible", "fake-model", "default")
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(`{"talkoot_enabled":true,"swarm_tiers":{"openai-compatible":{"cheap":{"model":"cheap-test-model"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err = w.UpdateTalkoot(t.Context(), ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "helm", Set: map[string]any{"tier": "cheap", "model": nil}}}})
	if err != nil {
		t.Fatal(err)
	}
	assertModel(v.Members[0], "openai-compatible", "cheap-test-model", "tier")
	v, err = w.UpdateTalkoot(t.Context(), ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "helm", Set: map[string]any{"model": "claude-opus-4-8", "tier": nil}}}})
	if err != nil {
		t.Fatal(err)
	}
	assertModel(v.Members[0], "anthropic", "claude-opus-4-8", "model")
	for range 3 {
		v, err = w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
		if err != nil {
			t.Fatal(err)
		}
		assertModel(v.Members[0], "anthropic", "claude-opus-4-8", "model")
	}
	for _, m := range v.Members {
		if m.Session != "" || m.Status.SpendUSD != 0 || m.Status.Turns != 0 {
			t.Fatalf("read changed member state: %+v", m)
		}
	}
	if calls.Load() != 0 || len(w.sessions) != before {
		t.Fatalf("read made %d calls, sessions %d -> %d", calls.Load(), before, len(w.sessions))
	}
}

func TestTalkootModelVisibilityPrefersLiveSessionOverTier(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	text := strings.Replace(string(crewText(cwd)), "role: coordinator", "role: coordinator\n    tier: cheap", 1)
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: text}); err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	// A live session can have been switched independently of the roster.
	w.mu.Lock()
	w.sessions["live-test"] = &wsSession{provider: "live-provider", model: "live-model"}
	w.mu.Unlock()
	w.talkoot.mu.Lock()
	run.seats["helm"] = "live-test"
	w.talkoot.mu.Unlock()
	t.Cleanup(func() { w.mu.Lock(); delete(w.sessions, "live-test"); w.mu.Unlock() })
	v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	m := v.Members[0]
	if m.ResolvedProvider != "live-provider" || m.ResolvedModel != "live-model" || m.ModelSource != "session" {
		t.Fatalf("live model not reported: %+v", m)
	}
	// The same seat ID without a live session must not materialize one.
	w.mu.Lock()
	delete(w.sessions, "live-test")
	w.mu.Unlock()
	v, err = w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Members[0].ResolvedModel != "fake-model" || v.Members[0].ModelSource != "tier" {
		t.Fatalf("cold seat should resolve the roster: %+v", v.Members[0])
	}
}

func TestTalkootModelVisibilityReportsResolutionFailureAndLeavesWorkersUnknown(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	v := talkootMemberView{Member: talkoot.Member{Driver: talkoot.DriverNative, Model: "does-not-exist"}}
	w.resolveTalkootMemberModel(&v)
	if v.ModelProblem == "" || v.ResolvedModel != "" {
		t.Fatalf("invalid model looks resolved: %+v", v)
	}
	v = talkootMemberView{Member: talkoot.Member{Driver: "claude", Model: "claude-opus-4-8"}}
	w.resolveTalkootMemberModel(&v)
	if v.ResolvedModel != "" || v.ResolvedProvider != "" || v.ModelProblem != "" {
		t.Fatalf("worker's requested model is not a live model: %+v", v)
	}
}
