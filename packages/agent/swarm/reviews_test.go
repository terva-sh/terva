package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// reviewInbox returns an inbox path in a directory of its own. The path is
// short, because a unix socket's path has a limit.
func reviewInbox(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets not supported")
	}
	return filepath.Join(testsupport.TempDir(t), "in.sock")
}

// 🚨 TKT-01M7R5QJ. The child asks, the supervisor counts, and the answer
// decides. A refusal reaches the child in the supervisor's own words.
func TestTheSupervisorCountsAChildsReviewRequests(t *testing.T) {
	inbox := reviewInbox(t)
	var mu sync.Mutex
	var asked []int
	srv, err := serveReviews(ReviewSocketPath(inbox), func(n int) string {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, n)
		if n > 2 {
			return "the routine has no review request left today"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if st, err := os.Stat(ReviewSocketPath(inbox)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the socket's mode is %v (%v), want 0600: the mode is the guard", st.Mode().Perm(), err)
	}

	if deny, err := AskReviews(t.Context(), inbox, 2); deny != "" || err != nil {
		t.Fatalf("a count inside the limit: deny %q, err %v", deny, err)
	}
	deny, err := AskReviews(t.Context(), inbox, 3)
	if err != nil || deny != "the routine has no review request left today" {
		t.Fatalf("a count past the limit: deny %q, err %v", deny, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []int{2, 3}) {
		t.Errorf("the supervisor counted %v, want [2 3]", asked)
	}
}

// 🚨 Each failure refuses. The caller runs the request only when deny and err
// are both empty, so each case here must leave one of them set.
func TestAReviewQuestionWithNoGoodAnswerIsNoPermission(t *testing.T) {
	// answers serves the review socket of a new inbox with a peer that
	// writes raw to each connection.
	answers := func(t *testing.T, raw string) string {
		t.Helper()
		inbox := reviewInbox(t)
		ln, err := net.Listen("unix", ReviewSocketPath(inbox))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				buf := make([]byte, 256)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte(raw))
				_ = c.Close()
			}
		}()
		return inbox
	}
	for _, c := range []struct {
		name, raw string
	}{
		{"no answer", ""},
		{"an answer with no key", "{}\n"},
		{"an answer that says no and gives no reason", `{"ok":false}` + "\n"},
		{"an answer that does not parse", "yes\n"},
		{"an answer that was cut", `{"ok":tr`},
		{"an answer above the frame limit", `{"ok":true,"deny":"` + strings.Repeat("x", reviewFrameMax) + `"}` + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			deny, err := AskReviews(t.Context(), answers(t, c.raw), 1)
			if deny == "" && err == nil {
				t.Fatal("the request would run")
			}
		})
	}
	t.Run("no socket", func(t *testing.T) {
		deny, err := AskReviews(t.Context(), reviewInbox(t), 1)
		if deny != "" || err == nil {
			t.Fatalf("deny %q, err %v: want an error that names the supervisor", deny, err)
		}
	})
	t.Run("a turn that ended", func(t *testing.T) {
		inbox := reviewInbox(t)
		release := make(chan struct{})
		srv, err := serveReviews(ReviewSocketPath(inbox), func(int) string { <-release; return "" })
		if err != nil {
			t.Fatal(err)
		}
		defer srv.Close()
		defer close(release)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		done := make(chan error, 1)
		go func() { _, err := AskReviews(ctx, inbox, 1); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the request would run")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the question outlived its turn")
		}
	})
	// The positive control: the same peer with a good answer allows.
	t.Run("a good answer", func(t *testing.T) {
		if deny, err := AskReviews(t.Context(), answers(t, `{"ok":true}`+"\n"), 1); deny != "" || err != nil {
			t.Fatalf("deny %q, err %v", deny, err)
		}
	})
}

// A question that the supervisor cannot read gets a refusal and no count.
func TestTheSupervisorRefusesAQuestionItCannotRead(t *testing.T) {
	for _, raw := range []string{"{}\n", `{"reviews":0}` + "\n", `{"reviews":-1}` + "\n", "review 2\n", strings.Repeat("x", reviewFrameMax+1) + "\n"} {
		inbox := reviewInbox(t)
		srv, err := serveReviews(ReviewSocketPath(inbox), func(int) string {
			t.Errorf("the supervisor counted for %q", raw)
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("unix", ReviewSocketPath(inbox))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte(raw))
		var reply reviewReply
		if err := json.NewDecoder(conn).Decode(&reply); err != nil {
			t.Fatalf("%q: no answer: %v", raw, err)
		}
		if reply.OK || reply.Deny == "" {
			t.Errorf("%q: answer %+v, want a refusal", raw, reply)
		}
		_ = conn.Close()
		_ = srv.Close()
	}
}

// The mark is in meta.json, so a daemon that starts again and a resume both
// find it. An agent with no mark gets none.
func TestTheMarkOfUnattendedWorkSurvivesAReloadAndAResume(t *testing.T) {
	mark := &Unattended{Team: "crew", Member: "helm", Chain: "run-1"}
	for _, c := range []struct {
		name string
		mark *Unattended
	}{{"marked", mark}, {"plain", nil}} {
		t.Run(c.name, func(t *testing.T) {
			root := testsupport.TempDir(t)
			idle := func(a *Agent) Runner {
				return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
			}
			f := New(Config{Root: root, RepoRoot: root, NewRunner: idle})
			req := SpawnRequest{Task: "x", Unattended: c.mark}
			a, err := f.SpawnReq(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			// The agent holds a copy. A caller that changes its own mark
			// afterward does not move the agent to another chain.
			if c.mark != nil {
				c.mark.Chain = "run-2"
				defer func() { c.mark.Chain = "run-1" }()
			}
			same := func(when string, got *Unattended) {
				t.Helper()
				if c.mark == nil {
					if got != nil {
						t.Errorf("%s: an agent with no mark got %+v", when, got)
					}
					return
				}
				if got == nil || *got != (Unattended{Team: "crew", Member: "helm", Chain: "run-1"}) {
					t.Errorf("%s: the mark is %+v", when, got)
				}
			}
			same("after the spawn", a.Unattended)
			if err := f.Stop(a.ID); err != nil {
				t.Fatal(err)
			}
			a.Wait()
			g := New(Config{Root: root, RepoRoot: root, NewRunner: idle})
			if loaded, errs := g.Reload(); loaded != 1 || len(errs) > 0 {
				t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
			}
			same("after a reload", g.Get(a.ID).Unattended)
			r, err := g.Resume(context.Background(), a.ID)
			if err != nil {
				t.Fatal(err)
			}
			same("after a resume", r.Unattended)
			_ = g.Stop(r.ID)
			r.Wait()
			h := New(Config{Root: root, RepoRoot: root, NewRunner: idle})
			if _, errs := h.Reload(); len(errs) > 0 {
				t.Fatal(errs)
			}
			same("after a resume and a second reload", h.Get(a.ID).Unattended)
		})
	}
}

// 🚨 The whole path, with a real child process. The runner tells a marked
// child of its mark and serves its socket, and the child's question reaches
// the host's count. A child with no mark is told nothing.
func TestAMarkedChildReachesTheHostsCount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets not supported")
	}
	if testing.Short() {
		t.Skip("skip end-to-end runner test in -short mode")
	}
	// The runner hands the test's environment to the child. A mark there
	// would reach the child that must have none.
	t.Setenv("TERVA_"+UnattendedEnv, "")
	exe := buildStubChild(t)
	root, repo := testsupport.TempDir(t), testsupport.TempDir(t)
	var mu sync.Mutex
	counted := map[string]int{}
	f := New(Config{
		Root: root, RepoRoot: repo,
		NewRunner: func(a *Agent) Runner {
			r := NewExecRunner(a, WithReviews(func(n int) string {
				mu.Lock()
				defer mu.Unlock()
				if counted[a.ID]+n > 2 {
					return "the limit is 2 a day"
				}
				counted[a.ID] += n
				return ""
			})).(*execRunner)
			r.Command = swarmAgentArgs(swarmAgentArgsOpts{Exe: exe, Dir: a.Dir, SessionPath: a.SessionPath, InboxPath: a.InboxPath, Task: a.Task})
			return r
		},
	})
	defer f.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	said := func(a *Agent, want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			evs, _ := ReadEventLog(a.EventLogPath)
			for _, ev := range evs {
				if ev.Type == "assistant_message" && strings.Contains(eventText(ev), want) {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		evs, _ := ReadEventLog(a.EventLogPath)
		t.Fatalf("the child never said %q:\n%s", want, dumpEventsVerbose(evs))
	}
	marked, err := f.SpawnReq(ctx, SpawnRequest{Task: "start", Unattended: &Unattended{Team: "crew", Member: "helm", Chain: "run-1"}})
	if err != nil {
		t.Fatal(err)
	}
	said(marked, "echo: start")
	if err := retrySend(f, marked.ID, "user review 2", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	said(marked, "echo: counted 2")
	if err := retrySend(f, marked.ID, "user review 1", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	said(marked, "echo: denied: the limit is 2 a day")

	plain, err := f.SpawnReq(ctx, SpawnRequest{Task: "start"})
	if err != nil {
		t.Fatal(err)
	}
	said(plain, "echo: start")
	if err := retrySend(f, plain.ID, "user review 1", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	said(plain, "echo: no mark")
	mu.Lock()
	defer mu.Unlock()
	if counted[marked.ID] != 2 || counted[plain.ID] != 0 {
		t.Errorf("the host counted %v, want 2 for the marked child and none for the other", counted)
	}
}

// 🚨 The mode is the guard of the socket. A socket whose mode terva could not
// set serves nothing, and leaves no file.
func TestAReviewSocketWithNoModeServesNothing(t *testing.T) {
	inbox := reviewInbox(t)
	was := chmodSocket
	t.Cleanup(func() { chmodSocket = was })
	chmodSocket = func(string, os.FileMode) error { return os.ErrPermission }
	srv, err := serveReviews(ReviewSocketPath(inbox), func(int) string { return "" })
	if err == nil {
		_ = srv.Close()
		t.Fatal("a socket with a mode that terva could not set must not serve")
	}
	if _, statErr := os.Stat(ReviewSocketPath(inbox)); statErr == nil {
		t.Fatal("the refused socket left its file")
	}
	if deny, askErr := AskReviews(t.Context(), inbox, 1); deny == "" && askErr == nil {
		t.Fatal("the request would run")
	}
}

// 🚨 The path is the agent's, and a later runner of the same agent listens
// at it again. A runner that winds down after that removes its own socket
// only, so the new child still gets its answers.
func TestAnOldReviewServerLeavesTheNewSocket(t *testing.T) {
	inbox := reviewInbox(t)
	old, err := serveReviews(ReviewSocketPath(inbox), func(int) string { return "the old runner answered" })
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := serveReviews(ReviewSocketPath(inbox), func(int) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if deny, err := AskReviews(t.Context(), inbox, 1); deny != "" || err != nil {
		t.Fatalf("after the old server closed: deny %q, err %v, want the new server's answer", deny, err)
	}
	// The new server still removes what it made.
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(ReviewSocketPath(inbox)); statErr == nil {
		t.Fatal("the last server left its socket")
	}
}

// 🚨 Each way to start an agent ends at SpawnReq. A process that is itself a
// child of unattended work marks each agent that it starts, whatever asked,
// and no marked agent starts on a worker backend.
func TestAnUnattendedProcessMarksWhatItStartsAndStartsNoWorker(t *testing.T) {
	newSwarm := func(t *testing.T) (*Swarm, *int) {
		root := testsupport.TempDir(t)
		started := new(int)
		f := New(Config{Root: root, RepoRoot: root, NewRunner: func(*Agent) Runner {
			*started++
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		}})
		t.Cleanup(f.StopAll)
		return f, started
	}
	chain := &Unattended{Team: "crew", Member: "helm", Chain: "run-1"}
	for _, c := range []struct {
		name    string
		env     string
		req     SpawnRequest
		refused bool
		marked  bool
	}{
		{"a plain process starts a plain agent", "", SpawnRequest{Task: "x"}, false, false},
		{"a plain process starts a worker", "", SpawnRequest{Task: "x", Backend: "claude"}, false, false},
		{"a marked request starts no worker", "", SpawnRequest{Task: "x", Backend: "claude", Unattended: chain}, true, false},
		{"an unattended process marks its agent", "1", SpawnRequest{Task: "x"}, false, true},
		{"an unattended process starts no worker", "1", SpawnRequest{Task: "x", Backend: "claude"}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TERVA_"+UnattendedEnv, c.env)
			f, started := newSwarm(t)
			a, err := f.SpawnReq(context.Background(), c.req)
			if c.refused {
				if !errors.Is(err, ErrUnattendedBackend) || *started != 0 {
					t.Fatalf("err %v, started %d: want the refusal and no agent", err, *started)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (a.Unattended != nil) != c.marked {
				t.Fatalf("the agent's mark is %+v, want marked=%v", a.Unattended, c.marked)
			}
		})
	}
}
