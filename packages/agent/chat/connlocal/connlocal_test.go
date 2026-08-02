package connlocal

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/testsupport"
)

// stubTransport is a scriptable connsdk.Transport — the same contract
// the compiled-in Discord transport implements.
type stubTransport struct {
	mu         sync.Mutex
	connectErr error
	receiveErr error
	receiveDie chan struct{}
	deliver    chan connsdk.Message
	sends      []connsdk.Outgoing
	session    connsdk.Session
	ctxDone    chan struct{}
	ctxOnce    sync.Once
}

func newStubTransport() *stubTransport {
	return &stubTransport{
		receiveDie: make(chan struct{}),
		deliver:    make(chan connsdk.Message, 8),
		ctxDone:    make(chan struct{}),
	}
}

func (s *stubTransport) factory(sess connsdk.Session) (connsdk.Transport, error) {
	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()
	return s, nil
}

func (s *stubTransport) Connect(ctx context.Context) (connsdk.Identity, error) {
	if s.connectErr != nil {
		return connsdk.Identity{}, s.connectErr
	}
	return connsdk.Identity{ID: "bot-1", Username: "stub"}, nil
}

func (s *stubTransport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	defer s.ctxOnce.Do(func() { close(s.ctxDone) })
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.receiveDie:
			return s.receiveErr
		case m := <-s.deliver:
			deliver(m)
		}
	}
}

func (s *stubTransport) Send(ctx context.Context, out connsdk.Outgoing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, out)
	return nil
}

func (s *stubTransport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return errors.New("no images")
}

func (s *stubTransport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return errors.New("no files")
}

func (s *stubTransport) Typing(ctx context.Context, chatID string) error { return nil }

func testConn(t *testing.T, st *stubTransport) *Conn {
	t.Helper()
	return New("localstub", connsdk.Config{
		Name:         "localstub",
		Version:      "0.0.0-test",
		Capabilities: connsdk.Capabilities{MaxTextLen: 321, TypingRefresh: 2 * time.Second},
		NewTransport: st.factory,
	}, testsupport.TempDir(t), func(string) {})
}

// drainReceive runs Receive for the test's lifetime and JOINS it at
// cleanup. The engine's log file only closes when Receive unwinds
// (stopEngine), and Windows cannot delete a TempDir that still holds
// an open file — a test that Connects without Receiving, or cancels
// without waiting, fails its cleanup there. Registered after the
// test's TempDir, so LIFO ordering runs this join first.
func drainReceive(t *testing.T, conn *Conn, handle func(chat.Message)) {
	t.Helper()
	if handle == nil {
		handle = func(chat.Message) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = conn.Receive(ctx, handle) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Receive never returned after cancel")
		}
	})
}

// TestConnLocalEndToEnd drives the whole chat.Connector surface through
// the in-process wire: Connect (identity + caps from the inner hello),
// Receive (inbound delivery), Send, and an orderly ctx-cancel shutdown
// that unwinds the engine and the transport.
func TestConnLocalEndToEnd(t *testing.T) {
	st := newStubTransport()
	conn := testConn(t, st)

	id, err := conn.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if id.ID != "bot-1" || id.Username != "stub" {
		t.Errorf("identity = %+v", id)
	}
	if caps := conn.Capabilities(); caps.MaxTextLen != 321 || caps.TypingRefresh != 2*time.Second {
		t.Errorf("caps = %+v (inner hello should carry the declared capabilities)", caps)
	}
	st.mu.Lock()
	dataDir := st.session.DataDir
	st.mu.Unlock()
	if !strings.Contains(filepath.ToSlash(dataDir), "connectors/localstub/data") {
		t.Errorf("transport DataDir = %q, want the connectors convention", dataDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan chat.Message, 1)
	recvErr := make(chan error, 1)
	go func() { recvErr <- conn.Receive(ctx, func(m chat.Message) { got <- m }) }()

	st.deliver <- connsdk.Message{ChatID: "c1", UserID: "u1", Username: "drew", Text: "hi"}
	select {
	case m := <-got:
		if m.ChatID != "c1" || m.Text != "hi" {
			t.Errorf("message = %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inbound message never arrived")
	}

	if err := conn.Send(context.Background(), chat.Outgoing{ChatID: "c1", Text: "pong"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	st.mu.Lock()
	sends := len(st.sends)
	st.mu.Unlock()
	if sends != 1 {
		t.Errorf("transport got %d sends, want 1", sends)
	}

	cancel()
	select {
	case err := <-recvErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Receive = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Receive never returned after cancel")
	}
	// The engine unwound and the transport's ctx ended.
	select {
	case <-st.ctxDone:
	case <-time.After(3 * time.Second):
		t.Fatal("transport ctx never cancelled on shutdown")
	}
}

// TestConnLocalConnectError: a failed dial surfaces from Connect with
// the transport's reason, and the engine is torn down.
func TestConnLocalConnectError(t *testing.T) {
	st := newStubTransport()
	st.connectErr = errors.New("bad token")
	conn := testConn(t, st)
	_, err := conn.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Errorf("Connect = %v, want the transport's error", err)
	}
}

// TestConnLocalReceiveDeath: a fatal transport failure ends Receive
// permanently with the transport's reason (no restart budget in this
// carrier — the transport's internal retries are the resilience layer).
func TestConnLocalReceiveDeath(t *testing.T) {
	st := newStubTransport()
	conn := testConn(t, st)
	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	st.receiveErr = errors.New("gateway lost")
	close(st.receiveDie)
	err := conn.Receive(context.Background(), func(chat.Message) {})
	if err == nil || !strings.Contains(err.Error(), "gateway lost") {
		t.Errorf("Receive = %v, want the transport's fatal error", err)
	}
}

// askStubTransport adds the connsdk.Asker upgrade to the stub: Ask
// answers itself (the scripted human) after recording the question.
type askStubTransport struct {
	*stubTransport
	asks   chan connsdk.Ask
	answer connsdk.Answer
	closes chan [2]string
}

func (s *askStubTransport) Ask(ctx context.Context, a connsdk.Ask, deliver func(connsdk.Answer)) (string, error) {
	s.asks <- a
	go deliver(s.answer)
	return "m-ask", nil
}

func (s *askStubTransport) CloseAsk(ctx context.Context, askID, outcome string) error {
	s.closes <- [2]string{askID, outcome}
	return nil
}

// TestConnLocalAsk proves the whole stage-G stack in one process:
// chat.Asker on the carrier → connhost ask/answer/ask_close frames
// over the pipes → connsdk.Serve → the transport's widget. This is
// the dogfood path every compiled-in connector rides.
func TestConnLocalAsk(t *testing.T) {
	st := &askStubTransport{
		stubTransport: newStubTransport(),
		asks:          make(chan connsdk.Ask, 1),
		answer: connsdk.Answer{Key: "approve", UserID: "u1", Username: "drew",
			Attestation: connsdk.AttestationAttested},
		closes: make(chan [2]string, 1),
	}
	conn := New("localstub", connsdk.Config{
		Name:    "localstub",
		Version: "0.0.0-test",
		Capabilities: connsdk.Capabilities{
			Features: []string{"message_ids", "asks"},
		},
		NewTransport: func(connsdk.Session) (connsdk.Transport, error) { return st, nil },
	}, testsupport.TempDir(t), func(string) {})

	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	drainReceive(t, conn, nil)
	if !conn.Capabilities().Asks {
		t.Fatal("Capabilities().Asks should be true when the transport declares the feature")
	}

	var asker chat.Asker = conn // the carrier is the chat.Asker
	ans, err := asker.Ask(context.Background(), chat.Ask{
		ChatID: "c1", Text: "approve?",
		Options: []chat.AskOption{
			{Key: "approve", Label: "Approve", Style: "affirm"},
			{Key: "deny", Label: "Deny", Style: "deny"},
		},
		RestrictTo: []string{"u1"},
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// DeepEqual, not ==: chat.Answer carries a Keys slice since multi-select
	// landed, so the struct is no longer comparable.
	want := chat.Answer{Key: "approve", UserID: "u1", Username: "drew", Attestation: chat.AttestationAttested}
	if !reflect.DeepEqual(ans, want) {
		t.Errorf("answer = %+v, want %+v", ans, want)
	}

	select {
	case a := <-st.asks:
		if a.ChatID != "c1" || len(a.Options) != 2 || a.Options[0].Key != "approve" {
			t.Errorf("transport saw ask %+v", a)
		}
	default:
		t.Error("transport never saw the ask")
	}
	select {
	case cl := <-st.closes:
		if cl[1] != "Approve — @drew" {
			t.Errorf("close outcome = %q", cl[1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CloseAsk never called")
	}
}

// speakerStubTransport adds the connsdk.SpeakerSender upgrade.
type speakerStubTransport struct {
	*stubTransport
	spoken chan connsdk.Outgoing
}

func (s *speakerStubTransport) SendAsSpeaker(ctx context.Context, out connsdk.Outgoing) (string, error) {
	s.spoken <- out
	return "m-cast", nil
}

// TestConnLocalSpeaker proves stage H through the whole in-process
// stack: chat.Outgoing.Speaker → connhost send frame → connsdk.Serve
// → the transport's speaker surface. The prefix fallback for ungraded
// connectors is pinned at the connhost layer; this pins the graded
// path end to end.
func TestConnLocalSpeaker(t *testing.T) {
	st := &speakerStubTransport{
		stubTransport: newStubTransport(),
		spoken:        make(chan connsdk.Outgoing, 1),
	}
	conn := New("localstub", connsdk.Config{
		Name:    "localstub",
		Version: "0.0.0-test",
		Capabilities: connsdk.Capabilities{
			Features: []string{"message_ids", "speaker:name_only"},
		},
		NewTransport: func(connsdk.Session) (connsdk.Transport, error) { return st, nil },
	}, testsupport.TempDir(t), func(string) {})

	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	drainReceive(t, conn, nil)
	if got := conn.Capabilities().Speaker; got != chat.SpeakerNameOnly {
		t.Fatalf("Speaker grade = %q, want name_only", got)
	}

	if err := conn.Send(context.Background(), chat.Outgoing{
		ChatID: "c1", Text: "The airlock hisses open.",
		Speaker: &chat.Speaker{Key: "kaiku", Name: "Kaiku"},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case out := <-st.spoken:
		if out.Speaker == nil || out.Speaker.Name != "Kaiku" || out.Text != "The airlock hisses open." {
			t.Errorf("speaker send = %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendAsSpeaker never called")
	}
	st.mu.Lock()
	plain := len(st.sends)
	st.mu.Unlock()
	if plain != 0 {
		t.Errorf("plain sends = %d, want none", plain)
	}
}

// threadStubTransport adds the connsdk.Threader upgrade.
type threadStubTransport struct {
	*stubTransport
	threads chan [3]string
}

func (s *threadStubTransport) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	s.threads <- [3]string{chatID, fromMessageID, name}
	return "t-99", nil
}

// TestConnLocalStartThread proves stage I through the in-process
// stack: chat.Threader on the carrier → thread_start over the pipes →
// the transport, with the new chat id riding the result back.
func TestConnLocalStartThread(t *testing.T) {
	st := &threadStubTransport{
		stubTransport: newStubTransport(),
		threads:       make(chan [3]string, 1),
	}
	conn := New("localstub", connsdk.Config{
		Name:    "localstub",
		Version: "0.0.0-test",
		Capabilities: connsdk.Capabilities{
			Features: []string{"threads_out"},
		},
		NewTransport: func(connsdk.Session) (connsdk.Transport, error) { return st, nil },
	}, testsupport.TempDir(t), func(string) {})

	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	drainReceive(t, conn, nil)
	if !conn.Capabilities().ThreadsOut {
		t.Fatal("ThreadsOut should be true when the transport declares the feature")
	}

	var threader chat.Threader = conn
	id, err := threader.StartThread(context.Background(), "c1", "m-12", "sidebar")
	if err != nil || id != "t-99" {
		t.Fatalf("StartThread = %q, %v", id, err)
	}
	select {
	case got := <-st.threads:
		if got != [3]string{"c1", "m-12", "sidebar"} {
			t.Errorf("StartThread args = %v", got)
		}
	default:
		t.Error("transport never saw the thread_start")
	}
}

// membershipStubTransport emits entities on its message and one
// admission event (connsdk.MembershipSource).
type membershipStubTransport struct {
	*stubTransport
	events chan connsdk.Membership
}

func (s *membershipStubTransport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	deliver(connsdk.Message{
		ID: "m1", ChatID: "c9", ChatKind: "group", UserID: "u2", Text: "hey @tervabot",
		Entities: []connsdk.Entity{{Kind: "bot_mention", Offset: 4, Length: 9}},
	})
	<-ctx.Done()
	return nil
}

func (s *membershipStubTransport) ReceiveMembership(ctx context.Context, deliver func(connsdk.Membership)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case mb := <-s.events:
			deliver(mb)
		}
	}
}

// TestConnLocalEntitiesAndMembership proves the stage-B inbound
// surface end to end: entities ride the message frame onto
// chat.Message, and admission events reach the handler installed via
// chat.MembershipHandlerSetter.
func TestConnLocalEntitiesAndMembership(t *testing.T) {
	st := &membershipStubTransport{
		stubTransport: newStubTransport(),
		events:        make(chan connsdk.Membership, 1),
	}
	conn := New("localstub", connsdk.Config{
		Name:    "localstub",
		Version: "0.0.0-test",
		Capabilities: connsdk.Capabilities{
			Features: []string{"entities", "chat_membership"},
		},
		NewTransport: func(connsdk.Session) (connsdk.Transport, error) { return st, nil },
	}, testsupport.TempDir(t), func(string) {})

	gotMembership := make(chan chat.Membership, 1)
	var setter chat.MembershipHandlerSetter = conn
	setter.SetMembershipHandler(func(mb chat.Membership) { gotMembership <- mb })

	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	gotMsg := make(chan chat.Message, 1)
	drainReceive(t, conn, func(m chat.Message) { gotMsg <- m })

	select {
	case m := <-gotMsg:
		if !m.MentionsBot() || len(m.Entities) != 1 || m.Entities[0].Offset != 4 {
			t.Errorf("entities = %+v", m.Entities)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("message never arrived")
	}

	st.events <- connsdk.Membership{ChatID: "c9", ChatKind: "group", ChatTitle: "ops",
		Change: "added", ByUserID: "u1", ByUsername: "drew"}
	select {
	case mb := <-gotMembership:
		want := chat.Membership{ChatID: "c9", ChatKind: "group", ChatTitle: "ops",
			Change: "added", ByUserID: "u1", ByUsername: "drew"}
		if mb != want {
			t.Errorf("membership = %+v, want %+v", mb, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("membership event never arrived")
	}
}
