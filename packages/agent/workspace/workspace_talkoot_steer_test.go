package workspace

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// chanConn is one end of an in-memory frame pipe.
type chanConn struct {
	recv <-chan ctrlproto.Frame
	send chan<- ctrlproto.Frame
}

func (c chanConn) ReadFrame(ctx context.Context) (ctrlproto.Frame, error) {
	select {
	case <-ctx.Done():
		return ctrlproto.Frame{}, ctx.Err()
	case f, ok := <-c.recv:
		if !ok {
			return ctrlproto.Frame{}, io.EOF
		}
		return f, nil
	}
}

func (c chanConn) WriteFrame(ctx context.Context, f ctrlproto.Frame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.send <- f:
		return nil
	}
}

func (chanConn) Close() error { return nil }

// serveRestricted serves w over an in-memory pipe with mask as the caller's
// authority, and returns a call that sends one command and reads its reply.
func serveRestricted(t *testing.T, w *Workspace, mask ctrlproto.Capability) func(sess string, m ctrlproto.Method, params any) *ctrlproto.Error {
	t.Helper()
	up, down := make(chan ctrlproto.Frame, 16), make(chan ctrlproto.Frame, 16)
	client, server := chanConn{recv: down, send: up}, chanConn{recv: up, send: down}
	go func() {
		_, _ = ctrlproto.ServeConn(t.Context(), server, w, ctrlproto.ServerHello("terva-test", "0"), ctrlproto.WithAuthority(mask))
	}()
	read := func() ctrlproto.Frame {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		f, err := client.ReadFrame(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	hello := ctrlproto.Hello{Role: ctrlproto.RoleClient, Protocol: ctrlproto.Protocol, Groups: []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupSession}}
	if err := client.WriteFrame(t.Context(), ctrlproto.HelloFrame(hello)); err != nil {
		t.Fatal(err)
	}
	read()
	var id uint64
	return func(sess string, m ctrlproto.Method, params any) *ctrlproto.Error {
		t.Helper()
		id++
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.WriteFrame(t.Context(), ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: id, Sess: sess, Method: m, Params: raw}); err != nil {
			t.Fatal(err)
		}
		for {
			if f := read(); f.Kind != ctrlproto.KindEvent && f.ID == id {
				return f.Error
			}
		}
	}
}

// A caller without steer may watch a talkoot's sessions and may not write to
// them: a seated member's session and a recruiter's session are the team's.
// Its own session stays open to it.
func TestACallerWithoutSteerCannotWriteToATeamSession(t *testing.T) {
	w, recruiter, _ := recruitCrew(t)
	seatOf(t, w, "jev")
	member := memberView(t, w, "crew", "jev").Session
	mine, err := w.CreateSession(t.Context(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	call := serveRestricted(t, w, ctrlproto.CapRead|ctrlproto.CapWrite|ctrlproto.CapSpend)
	for name, sess := range map[string]string{"member": member, "recruiter": recruiter.ID} {
		if e := call(sess, ctrlproto.MethodSessionRename, ctrlproto.RenameParams{Title: "mine now"}); e == nil || e.Code != ctrlproto.CodeForbidden {
			t.Errorf("a rename of the %s's session without steer: %+v", name, e)
		}
		if e := call(sess, ctrlproto.MethodSessionState, nil); e != nil {
			t.Errorf("a read of the %s's session without steer: %+v", name, e)
		}
	}
	if e := call(mine.ID, ctrlproto.MethodSessionRename, ctrlproto.RenameParams{Title: "mine"}); e != nil {
		t.Errorf("a rename of the caller's own session: %+v", e)
	}
	full := serveRestricted(t, w, ctrlproto.CapRead|ctrlproto.CapWrite|ctrlproto.CapSpend|ctrlproto.CapSteer)
	if e := full(member, ctrlproto.MethodSessionRename, ctrlproto.RenameParams{Title: "helm's jev"}); e != nil {
		t.Errorf("a rename of the member's session with steer: %+v", e)
	}
	// A prompt, an answer, and a transcript edit, each on the seated member:
	// refused without steer, and carried out with it.
	s := w.existing(member)
	if s == nil {
		t.Fatal("the member's session is not live")
	}
	waitTalkoot(t, "the member's first turn to end", func() bool { return !s.busyNow() && len(s.agent.Messages()) > 0 })
	edit := ctrlproto.MessageEditParams{Epoch: s.agent.TranscriptEpoch(), Index: 0, Text: "Edited by a person."}
	for _, c := range []struct {
		m      ctrlproto.Method
		params any
	}{
		{ctrlproto.MethodMessageEdit, edit},
		{ctrlproto.MethodAnswer, ctrlproto.AnswerParams{AskID: "none"}},
		{ctrlproto.MethodPrompt, ctrlproto.PromptParams{Text: "Drop the login fix."}},
	} {
		if e := call(member, c.m, c.params); e == nil || e.Code != ctrlproto.CodeForbidden {
			t.Errorf("%s on the member's session without steer: %+v", c.m, e)
		}
		if c.m == ctrlproto.MethodMessageEdit && textOf(s.agent.Messages()[0]) == edit.Text {
			t.Fatal("a refused edit changed the member's transcript")
		}
		if e := full(member, c.m, c.params); e != nil {
			t.Errorf("%s on the member's session with steer: %+v", c.m, e)
		}
		if c.m == ctrlproto.MethodMessageEdit && textOf(s.agent.Messages()[0]) != edit.Text {
			t.Errorf("the edit with steer left the transcript as %q", textOf(s.agent.Messages()[0]))
		}
	}
}

// The answer holds for a session this workspace has not loaded: a member of a
// talkoot another process runs, and a recruiter at rest on disk. An empty id
// asks about the session it resolves to.
func TestSteersTalkootReadsSessionsAtRest(t *testing.T) {
	w, recruiter, _ := recruitCrew(t)
	seatOf(t, w, "jev")
	member := memberView(t, w, "crew", "jev").Session
	if !w.SteersTalkoot("") {
		t.Error("the empty id resolves to the recruiter, and the answer is false")
	}
	cold := openTalkootWorkspace(t, w.cwd)
	if cold.existing(recruiter.ID) != nil || cold.existing(member) != nil {
		t.Fatal("the second workspace already holds the sessions live")
	}
	if !cold.SteersTalkoot(recruiter.ID) {
		t.Error("a recruiter at rest does not count as bound")
	}
	if !cold.SteersTalkoot(member) {
		t.Error("a member session that another workspace seats does not count as bound")
	}
	if cold.SteersTalkoot("../escape") || cold.SteersTalkoot("20260101-120000-00000000") {
		t.Error("an id that names no session counts as bound")
	}
	// A session file that exists and does not read counts as bound.
	unread := "20260101-120000-dddddddd"
	if err := os.Mkdir(cold.sessionPath(unread), 0o700); err != nil {
		t.Fatal(err)
	}
	if !cold.SteersTalkoot(unread) {
		t.Error("a session file that does not read counts as unbound")
	}
	// A member the other workspace seats after this one's first fold.
	seatOf(t, w, "helm")
	if helm := memberView(t, w, "crew", "helm").Session; !cold.SteersTalkoot(helm) {
		t.Error("a member seated after the first fold does not count as bound")
	}
	mine, err := w.CreateSession(t.Context(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if w.SteersTalkoot(mine.ID) {
		t.Error("a person's session counts as bound")
	}
	if w.SteersTalkoot("") {
		t.Error("the empty id resolves to the person's new session, and the answer is true")
	}
}

// A miss folds again, so a room that does not read would report on every
// write from a caller without steer. It reports once. The room belongs to a
// talkoot that no workspace runs, written straight to disk, so breaking its
// key races no router.
func TestARoomThatDoesNotReadReportsOnce(t *testing.T) {
	cwd := talkootHome(t)
	dir := filepath.Join(talkoot.Dir(), "ghost")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	roster := "---\nname: ghost\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n---\n"
	for name, text := range map[string]string{
		"talkoot.md":     roster,
		talkoot.RoomFile: "{}\n",
		talkoot.KeyFile:  "not a key\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := openTalkootWorkspace(t, cwd)
	var mu sync.Mutex
	var said []string
	w.SetDiag(func(s string) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(s, "could not read the room") {
			said = append(said, s)
		}
	})
	for range 3 {
		w.SteersTalkoot("20260101-120000-eeeeeeee")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(said) != 1 {
		t.Fatalf("the room that does not read was reported %d times: %q", len(said), said)
	}
}
