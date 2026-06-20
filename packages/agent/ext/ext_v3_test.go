package ext

import (
	"encoding/json"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

func TestRefreshContextEmitsFrame(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	go h.ext.RefreshContext("project notes")

	f := h.drainUntil(t, "refresh_context")
	var rc extproto.RefreshContextFromExt
	if err := json.Unmarshal(f.raw, &rc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rc.Text != "project notes" {
		t.Errorf("refresh_context text = %q, want %q", rc.Text, "project notes")
	}
}

func TestHostToolCallRoundTrip(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	type result struct {
		r   ToolResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := h.ext.HostToolCall("read", json.RawMessage(`{"path":"x"}`))
		done <- result{r, err}
	}()

	f := h.drainUntil(t, "host_tool_call")
	var htc extproto.HostToolCallFromExt
	if err := json.Unmarshal(f.raw, &htc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if htc.Name != "read" {
		t.Errorf("name = %q, want read", htc.Name)
	}
	if !htc.Silent {
		t.Error("HostToolCall should default to silent")
	}
	if htc.ID == "" {
		t.Error("host_tool_call needs a correlation id")
	}

	h.sendToExt(t, extproto.HostToolResultFromHost{
		Type:    "host_tool_result",
		ID:      htc.ID,
		Content: []extproto.ContentBlock{{Type: "text", Text: "file contents"}},
	})

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("HostToolCall error: %v", got.err)
		}
		if len(got.r.Content) != 1 || got.r.Content[0].Text != "file contents" {
			t.Errorf("result = %+v, want one text block", got.r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HostToolCall never returned after the reply")
	}
}

func TestHostToolCallVisibleAndError(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	done := make(chan ToolResult, 1)
	go func() {
		r, _ := h.ext.HostToolCall("bash", json.RawMessage(`{}`), Silent(false))
		done <- r
	}()

	f := h.drainUntil(t, "host_tool_call")
	var htc extproto.HostToolCallFromExt
	json.Unmarshal(f.raw, &htc)
	if htc.Silent {
		t.Error("Silent(false) should clear the silent flag")
	}
	h.sendToExt(t, extproto.HostToolResultFromHost{
		Type: "host_tool_result", ID: htc.ID, IsError: true,
		Content: []extproto.ContentBlock{{Type: "text", Text: "boom"}},
	})
	got := <-done
	if !got.IsError {
		t.Error("is_error should propagate to the ToolResult")
	}
}

func TestHostToolCallTimeout(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	done := make(chan error, 1)
	go func() {
		_, err := h.ext.HostToolCall("read", json.RawMessage(`{}`), WithCallTimeout(100*time.Millisecond))
		done <- err
	}()

	h.drainUntil(t, "host_tool_call") // read the request, never reply

	select {
	case err := <-done:
		if err == nil {
			t.Error("HostToolCall with no reply should return a timeout error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HostToolCall did not time out")
	}
}

func TestListSessions(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	done := make(chan []SessionInfo, 1)
	errc := make(chan error, 1)
	go func() {
		s, err := h.ext.ListSessions()
		if err != nil {
			errc <- err
			return
		}
		done <- s
	}()

	f := h.drainUntil(t, "list_sessions")
	var ls extproto.ListSessionsFromExt
	json.Unmarshal(f.raw, &ls)
	h.sendToExt(t, extproto.SessionListFromHost{
		Type: "session_list", ID: ls.ID,
		Sessions: []extproto.SessionInfo{{SessionID: "s1", Title: "t", Messages: 3, ModTime: 42}},
	})

	select {
	case s := <-done:
		if len(s) != 1 || s[0].ID != "s1" || s[0].Messages != 3 || s[0].ModTime != 42 {
			t.Errorf("ListSessions = %+v", s)
		}
	case err := <-errc:
		t.Fatalf("ListSessions error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("ListSessions never returned")
	}
}

func TestReadSessionFoundAndNotFound(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	// Found.
	done := make(chan []SessionMessage, 1)
	errc := make(chan error, 1)
	go func() {
		m, err := h.ext.ReadSession("s1")
		if err != nil {
			errc <- err
			return
		}
		done <- m
	}()
	f := h.drainUntil(t, "read_session")
	var rs extproto.ReadSessionFromExt
	json.Unmarshal(f.raw, &rs)
	if rs.SessionID != "s1" {
		t.Errorf("read_session session_id = %q", rs.SessionID)
	}
	h.sendToExt(t, extproto.SessionDataFromHost{
		Type: "session_data", ID: rs.ID,
		Messages: []extproto.SessionMessage{{Role: "user", Text: "hi"}},
	})
	select {
	case m := <-done:
		if len(m) != 1 || m[0].Role != "user" || m[0].Text != "hi" {
			t.Errorf("ReadSession = %+v", m)
		}
	case err := <-errc:
		t.Fatalf("ReadSession error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("ReadSession never returned")
	}

	// Not found → error.
	errc2 := make(chan error, 1)
	go func() {
		_, err := h.ext.ReadSession("nope")
		errc2 <- err
	}()
	f2 := h.drainUntil(t, "read_session")
	var rs2 extproto.ReadSessionFromExt
	json.Unmarshal(f2.raw, &rs2)
	h.sendToExt(t, extproto.SessionDataFromHost{Type: "session_data", ID: rs2.ID, NotFound: true})
	select {
	case err := <-errc2:
		if err == nil {
			t.Error("ReadSession of a not-found session should error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadSession (not found) never returned")
	}
}
