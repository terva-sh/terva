package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The ask channel lives on the ask_user_question tool INSTANCE, not on a
// long-lived object the way the confirmer lives on the gate. rebuildTools
// re-resolves the registry from scratch, minting a fresh tool with a nil
// Asker — so every rebuild is a chance to silently drop the channel and leave
// the tool answering "No interactive channel is available" for the rest of the
// session, with the TUI sitting right there, question dialog wired and waiting.
//
// Not a corner case: a rebuild fires before the first turn whenever an
// extension asserts its tool policy, and again on entering plan mode — the one
// mode that deliberately KEEPS ask_user_question so the agent can ask when
// requirements are unclear.

func newAskSession(t *testing.T, id string) *wsSession {
	t.Helper()
	tmp := testsupport.TempDir(t)
	sess, err := core.NewSession(tmp, tmp, "p", "claude-sonnet-4-5", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	s := &wsSession{
		id:      id,
		ws:      &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:     newWSHub(),
		sess:    sess,
		pendAsk: map[string]chan core.UserAnswer{},
		askReq:  map[string]ctrlproto.AskRequest{},
		args:    build.Args{Model: "claude-sonnet-4-5"},
	}

	// Build the registry the way buildSession does: asker bound.
	r, err := build.Resolve(s.args, true)
	if err != nil {
		t.Skipf("resolve unavailable here: %v", err)
	}
	r.SetAsker(&webAsker{s: s})
	s.agent = core.NewAgent(nil, "claude-sonnet-4-5", "", r.ToolRegistry)
	return s
}

// askerOf reaches the live tool the agent would actually call.
func askerOf(t *testing.T, s *wsSession) core.Asker {
	t.Helper()
	tl, ok := s.agent.LookupTool("ask_user_question")
	if !ok {
		t.Fatal("ask_user_question is not in the agent's registry")
	}
	at, ok := tl.(*tools.AskUserTool)
	if !ok {
		t.Fatalf("ask_user_question is %T, want *tools.AskUserTool", tl)
	}
	return at.Asker
}

func TestRebuildToolsKeepsTheAskChannel(t *testing.T) {
	s := newAskSession(t, "ask-rebuild")

	if askerOf(t, s) == nil {
		t.Fatal("precondition: no asker bound at session build")
	}

	// Every reason rebuildTools is fired with in real operation. The startup
	// one lands before the user has typed anything.
	for _, reason := range []string{
		"extension-context", "tool-withdrawal", "extension-reload", "approval-mode",
	} {
		s.rebuildTools(reason)
		if askerOf(t, s) == nil {
			t.Fatalf("rebuildTools(%q) dropped the ask channel — ask_user_question "+
				"would report \"no interactive channel\" for the rest of the session", reason)
		}
	}
}

// The end the user actually sees: after a rebuild, CALLING the tool must reach
// the front end and come back with the answer, not the "no channel" refusal.
func TestAskUserQuestionReachesTheFrontEndAfterRebuild(t *testing.T) {
	s := newAskSession(t, "ask-call")

	s.rebuildTools("approval-mode") // e.g. the user switched to plan mode

	tl, ok := s.agent.LookupTool("ask_user_question")
	if !ok {
		t.Fatal("ask_user_question missing after rebuild")
	}

	// Stand in for the TUI: answer the ask as soon as it is parked.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			s.mu.Lock()
			var id string
			for k := range s.pendAsk {
				id = k
				break
			}
			s.mu.Unlock()
			if id != "" {
				s.answer(id, core.UserAnswer{Answer: "stow"})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	res, err := tl.Execute(ctx, []byte(`{"question":"which one?","options":["stow","chezmoi"]}`), func(string) {})
	if err != nil {
		t.Fatalf("ask_user_question: %v", err)
	}

	got := toolText(res)
	if strings.Contains(got, "No interactive channel") {
		t.Fatalf("the tool still refuses to ask after a rebuild: %s", got)
	}
	if !strings.Contains(got, "stow") {
		t.Fatalf("the answer never came back through the tool: %s", got)
	}
	det, _ := res.Details.(map[string]any)
	if asked, _ := det["asked"].(bool); !asked {
		t.Errorf("Details[asked] = %v, want the tool to record that it asked", det["asked"])
	}
}

func toolText(r core.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}
