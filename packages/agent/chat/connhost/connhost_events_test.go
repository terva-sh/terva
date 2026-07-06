package connhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/testsupport"
)

// startEventSession is startAskSession plus stage-D event consumers.
func startEventSession(t *testing.T, features []string, dataDir string) (*Session, *scriptConn, *eventRec) {
	t.Helper()
	rec := &eventRec{
		edited:   make(chan chat.MessageEdited, 4),
		deleted:  make(chan chat.MessageDeleted, 4),
		reaction: make(chan chat.Reaction, 4),
		messages: make(chan chat.Message, 4),
	}
	sc := newScriptConn()
	s := New(Config{
		Name: "stub", DataDir: dataDir,
		Conn:    sc,
		Deliver: func(m chat.Message) { rec.messages <- m },
		Events: chat.ChatEventHandlers{
			Edited:   func(ev chat.MessageEdited) { rec.edited <- ev },
			Deleted:  func(ev chat.MessageDeleted) { rec.deleted <- ev },
			Reaction: func(ev chat.Reaction) { rec.reaction <- ev },
		},
		Warn: func(string) {}, Log: func(string) {},
		SendTimeout: 2 * time.Second,
	})
	sc.push(t, connproto.HelloFromConn{
		Type: "hello", Name: "stub", ProtocolMin: 1, ProtocolMax: 2,
		Capabilities: connproto.Capabilities{Features: features, MinEditIntervalMS: 1000},
	})
	if err := s.Start(time.Second); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sc.next(t, "hello_ack")
	t.Cleanup(func() { close(sc.inbound) })
	return s, sc, rec
}

type eventRec struct {
	edited   chan chat.MessageEdited
	deleted  chan chat.MessageDeleted
	reaction chan chat.Reaction
	messages chan chat.Message
}

// TestSessionInboundEvents routes the stage-D trio, tagging reactions
// on the bot's own messages via the send bookkeeping.
func TestSessionInboundEvents(t *testing.T) {
	s, sc, rec := startEventSession(t,
		[]string{"edits_in", "deletes_in", "reactions_in", "edits_out", "reactions_out", "deletes_out"},
		testsupport.TempDir(t))
	caps := s.Capabilities()
	if !caps.EditsOut || !caps.ReactionsOut || !caps.DeletesOut || caps.MinEditInterval != time.Second {
		t.Fatalf("caps = %+v", caps)
	}

	// A send whose result carries message_id marks m-90 as ours.
	done := make(chan error, 1)
	go func() { done <- s.Send(context.Background(), chat.Outgoing{ChatID: "c1", Text: "mine"}) }()
	raw := sc.next(t, "send")
	var sf connproto.SendFromHost
	_ = json.Unmarshal(raw, &sf)
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: sf.ID, MessageID: "m-90"})
	if err := <-done; err != nil {
		t.Fatalf("Send: %v", err)
	}

	sc.push(t, connproto.MessageEditedFromConn{Type: "message_edited", ChatID: "c1", ID: "m10",
		TS: 5, Text: "fixed", Entities: []connproto.Entity{{Kind: "code", Offset: 0, Length: 5}}})
	sc.push(t, connproto.MessageDeletedFromConn{Type: "message_deleted", ChatID: "c1", ID: "m11"})
	sc.push(t, connproto.ReactionFromConn{Type: "reaction", ChatID: "c1", MessageID: "m-90",
		UserID: "u1", Username: "drew", Key: "👍"})
	sc.push(t, connproto.ReactionFromConn{Type: "reaction", ChatID: "c1", MessageID: "m-other",
		UserID: "u1", Key: "👍", Removed: true})

	ed := <-rec.edited
	if ed.ChatID != "c1" || ed.ID != "m10" || ed.Text != "fixed" || len(ed.Entities) != 1 {
		t.Errorf("edited = %+v", ed)
	}
	del := <-rec.deleted
	if del.ID != "m11" {
		t.Errorf("deleted = %+v", del)
	}
	r1 := <-rec.reaction
	if !r1.OwnMessage || r1.Key != "👍" || r1.Username != "drew" {
		t.Errorf("own-message reaction = %+v", r1)
	}
	r2 := <-rec.reaction
	if r2.OwnMessage || !r2.Removed {
		t.Errorf("foreign reaction = %+v", r2)
	}
}

// TestSessionOutboundEvents drives edit/react/delete round trips and
// the local refusal without the features.
func TestSessionOutboundEvents(t *testing.T) {
	s, sc, _ := startEventSession(t, []string{"edits_out", "reactions_out", "deletes_out"}, testsupport.TempDir(t))

	check := func(call func() error, wantType string, verify func(raw []byte)) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- call() }()
		raw := sc.next(t, wantType)
		verify(raw)
		var f connproto.Frame
		_ = json.Unmarshal(raw, &f)
		sc.push(t, connproto.ResultFromConn{Type: "result", ID: f.ID})
		if err := <-done; err != nil {
			t.Fatalf("%s: %v", wantType, err)
		}
	}
	check(func() error { return s.EditMessage(context.Background(), "c1", "m-90", "v2") }, "edit", func(raw []byte) {
		var f connproto.EditFromHost
		_ = json.Unmarshal(raw, &f)
		if f.ChatID != "c1" || f.MessageID != "m-90" || f.Text != "v2" {
			t.Errorf("edit = %+v", f)
		}
	})
	check(func() error { return s.React(context.Background(), "c1", "m-12", "👀", false) }, "react", func(raw []byte) {
		var f connproto.ReactFromHost
		_ = json.Unmarshal(raw, &f)
		if f.Key != "👀" || f.Remove {
			t.Errorf("react = %+v", f)
		}
	})
	check(func() error { return s.DeleteMessage(context.Background(), "c1", "m-90") }, "delete", func(raw []byte) {
		var f connproto.DeleteFromHost
		_ = json.Unmarshal(raw, &f)
		if f.MessageID != "m-90" {
			t.Errorf("delete = %+v", f)
		}
	})

	// Without the features: refused locally, nothing written.
	s2, sc2, _ := startEventSession(t, nil, testsupport.TempDir(t))
	for _, call := range []func() error{
		func() error { return s2.EditMessage(context.Background(), "c1", "m", "x") },
		func() error { return s2.React(context.Background(), "c1", "m", "👍", false) },
		func() error { return s2.DeleteMessage(context.Background(), "c1", "m") },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Errorf("ungated call = %v, want refusal", err)
		}
	}
	select {
	case b := <-sc2.writes:
		t.Errorf("unexpected frame written: %s", b)
	default:
	}
}

// TestSessionAttachmentKinds: images stay in-memory; other kinds MOVE
// into a contained per-message dir; captions join the text; escapes
// are refused.
func TestSessionAttachmentKinds(t *testing.T) {
	dataDir := testsupport.TempDir(t)
	_, sc, rec := startEventSession(t, []string{"attachment_kinds"}, dataDir)

	img := filepath.Join(dataDir, "in.png")
	if err := os.WriteFile(img, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	voice := filepath.Join(dataDir, "v.ogg")
	if err := os.WriteFile(voice, []byte("OGG"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(testsupport.TempDir(t), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	sc.push(t, connproto.MessageFromConn{Type: "message", ID: "m1", ChatID: "c1", UserID: "u1",
		Text: "listen",
		Attachments: []connproto.Attachment{
			{MimeType: "image/png", Path: img},
			{MimeType: "audio/ogg", Path: voice, Kind: "voice", Name: "note.ogg",
				Size: 3, DurationMS: 4200, Caption: "a voice note"},
			{MimeType: "text/plain", Path: outside, Kind: "document"},
		}})

	m := <-rec.messages
	if len(m.Images) != 1 || string(m.Images[0].Data) != "PNG" {
		t.Errorf("images = %+v", m.Images)
	}
	if len(m.Files) != 1 {
		t.Fatalf("files = %+v (the escape must be refused)", m.Files)
	}
	f := m.Files[0]
	if f.Kind != "voice" || f.Name != "note.ogg" || f.Duration != 4200*time.Millisecond {
		t.Errorf("file = %+v", f)
	}
	if !strings.HasPrefix(f.Path, filepath.Join(dataDir, "incoming")+string(filepath.Separator)) {
		t.Errorf("file escaped containment: %s", f.Path)
	}
	if b, err := os.ReadFile(f.Path); err != nil || string(b) != "OGG" {
		t.Errorf("moved bytes = %q err=%v", b, err)
	}
	if _, err := os.Stat(voice); !os.IsNotExist(err) {
		t.Error("original voice file should have been moved away")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("outside file must be untouched")
	}
	if !strings.Contains(m.Text, "a voice note") {
		t.Errorf("caption did not join text: %q", m.Text)
	}
}

// TestOwnMessageEventEchoesDropped: edits/deletes of ids this session
// minted (ask_close outcome renders, future streaming edits) reflect
// off the service as message_edited/message_deleted — they must never
// reach the host consumers as "the user edited/deleted a message".
// (Only a message's author can edit it, so an own-id edit is always
// the session's own write-back.)
func TestOwnMessageEventEchoesDropped(t *testing.T) {
	s, sc, rec := startEventSession(t, []string{"edits_in", "deletes_in"}, testsupport.TempDir(t))

	// A send whose result carries message_id marks m-77 as ours.
	done := make(chan error, 1)
	go func() { done <- s.Send(context.Background(), chat.Outgoing{ChatID: "c1", Text: "mine"}) }()
	raw := sc.next(t, "send")
	var sf connproto.SendFromHost
	_ = json.Unmarshal(raw, &sf)
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: sf.ID, MessageID: "m-77"})
	if err := <-done; err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Own edit + own delete must be dropped; the foreign edit pushed
	// right after must still arrive (frames route in order, so its
	// arrival proves the earlier ones were dropped, not delayed).
	sc.push(t, connproto.MessageEditedFromConn{Type: "message_edited", ChatID: "c1", ID: "m-77", Text: "outcome render"})
	sc.push(t, connproto.MessageDeletedFromConn{Type: "message_deleted", ChatID: "c1", ID: "m-77"})
	sc.push(t, connproto.MessageEditedFromConn{Type: "message_edited", ChatID: "c1", ID: "m-user", Text: "real edit"})

	select {
	case ev := <-rec.edited:
		if ev.ID != "m-user" {
			t.Fatalf("own-message edit leaked through: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreign edit never arrived")
	}
	select {
	case ev := <-rec.deleted:
		t.Fatalf("own-message delete leaked through: %+v", ev)
	default:
	}
}
