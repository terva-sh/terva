package connproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGoldenFrames pins the exact bytes of every frame type. A
// failure here means the wire format changed: either revert the
// change or bump ProtocolVersion and teach the proxy the new frame —
// external connectors built against the old schema are deployed
// independently of zot and cannot be recompiled in lockstep.
func TestGoldenFrames(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{
			"hello",
			HelloFromConn{Type: "hello", Name: "discord", Version: "1.0.0", ProtocolMin: 1, ProtocolMax: 1,
				Capabilities: Capabilities{MaxTextLen: 2000, TypingRefreshMS: 8000, SendsImages: true, SendsFiles: true}},
			`{"type":"hello","name":"discord","version":"1.0.0","protocol_min":1,"protocol_max":1,"capabilities":{"max_text_len":2000,"typing_refresh_ms":8000,"sends_images":true,"sends_files":true}}`,
		},
		{
			"hello minimal capabilities",
			HelloFromConn{Type: "hello", Name: "irc", ProtocolMin: 1, ProtocolMax: 1},
			`{"type":"hello","name":"irc","protocol_min":1,"protocol_max":1,"capabilities":{}}`,
		},
		{
			"connected",
			ConnectedFromConn{Type: "connected", ID: "1234", Username: "zotbot"},
			`{"type":"connected","id":"1234","username":"zotbot"}`,
		},
		{
			"connect_error",
			ConnectErrorFromConn{Type: "connect_error", Error: "bad token"},
			`{"type":"connect_error","error":"bad token"}`,
		},
		{
			"message",
			MessageFromConn{Type: "message", ChatID: "c1", UserID: "u1", Username: "drew", ReplyTo: "m9",
				Text: "hi", Attachments: []Attachment{{MimeType: "image/png", Path: "/data/in/abc.png"}}},
			`{"type":"message","chat_id":"c1","user_id":"u1","username":"drew","reply_to":"m9","text":"hi","attachments":[{"mime_type":"image/png","path":"/data/in/abc.png"}]}`,
		},
		{
			"message bare",
			MessageFromConn{Type: "message", ChatID: "c1", UserID: "u1"},
			`{"type":"message","chat_id":"c1","user_id":"u1"}`,
		},
		{
			"result ok",
			ResultFromConn{Type: "result", ID: "42"},
			`{"type":"result","id":"42"}`,
		},
		{
			"result error",
			ResultFromConn{Type: "result", ID: "42", Error: "rate limited"},
			`{"type":"result","id":"42","error":"rate limited"}`,
		},
		{
			"warn",
			WarnFromConn{Type: "warn", Message: "gateway reconnecting"},
			`{"type":"warn","message":"gateway reconnecting"}`,
		},
		{
			"hello_ack",
			HelloAckFromHost{Type: "hello_ack", Protocol: 1, ZotVersion: "1.2.3", DataDir: "/home/u/.zot/connectors/discord/data"},
			`{"type":"hello_ack","protocol":1,"zot_version":"1.2.3","data_dir":"/home/u/.zot/connectors/discord/data"}`,
		},
		{
			// The rename bridge: terva_version is additive; zot_version
			// stays so connectors built against the old SDK keep working.
			"hello_ack both naming eras",
			HelloAckFromHost{Type: "hello_ack", Protocol: 1, ZotVersion: "1.2.3", TervaVersion: "1.2.3"},
			`{"type":"hello_ack","protocol":1,"zot_version":"1.2.3","terva_version":"1.2.3"}`,
		},
		{
			"connect",
			ConnectFromHost{Type: "connect"},
			`{"type":"connect"}`,
		},
		{
			"send",
			SendFromHost{Type: "send", ID: "42", ChatID: "c1", ReplyTo: "m9", Text: "hello"},
			`{"type":"send","id":"42","chat_id":"c1","reply_to":"m9","text":"hello"}`,
		},
		{
			"send empty text",
			SendFromHost{Type: "send", ID: "42", ChatID: "c1"},
			`{"type":"send","id":"42","chat_id":"c1","text":""}`,
		},
		{
			"send_image",
			SendImageFromHost{Type: "send_image", ID: "43", ChatID: "c1", Path: "/tmp/shot.png", Caption: "the bug"},
			`{"type":"send_image","id":"43","chat_id":"c1","path":"/tmp/shot.png","caption":"the bug"}`,
		},
		{
			"send_file",
			SendFileFromHost{Type: "send_file", ID: "44", ChatID: "c1", Path: "/tmp/report.pdf"},
			`{"type":"send_file","id":"44","chat_id":"c1","path":"/tmp/report.pdf"}`,
		},
		{
			"typing",
			TypingFromHost{Type: "typing", ChatID: "c1"},
			`{"type":"typing","chat_id":"c1"}`,
		},
		{
			"shutdown",
			ShutdownFromHost{Type: "shutdown"},
			`{"type":"shutdown"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.v)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !strings.HasSuffix(string(got), "\n") {
				t.Fatalf("frame not LF-terminated: %q", got)
			}
			if s := strings.TrimSuffix(string(got), "\n"); s != tc.want {
				t.Errorf("frame bytes drifted\n got: %s\nwant: %s", s, tc.want)
			}
		})
	}
}

// TestGoldenDecode pins the read direction: frames written by an
// old connector must keep decoding into the same struct values.
func TestGoldenDecode(t *testing.T) {
	var hello HelloFromConn
	line := `{"type":"hello","name":"discord","version":"1.0.0","protocol_min":1,"protocol_max":2,"capabilities":{"max_text_len":2000,"typing_refresh_ms":8000,"sends_images":true}}`
	if err := json.Unmarshal([]byte(line), &hello); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	if hello.Name != "discord" || hello.ProtocolMin != 1 || hello.ProtocolMax != 2 {
		t.Errorf("hello fields: %+v", hello)
	}
	if hello.Capabilities.MaxTextLen != 2000 || !hello.Capabilities.SendsImages || hello.Capabilities.SendsFiles {
		t.Errorf("hello capabilities: %+v", hello.Capabilities)
	}

	var msg MessageFromConn
	line = `{"type":"message","chat_id":"c1","user_id":"u1","attachments":[{"mime_type":"image/png","path":"/d/a.png"}]}`
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.ChatID != "c1" || len(msg.Attachments) != 1 || msg.Attachments[0].Path != "/d/a.png" {
		t.Errorf("message fields: %+v", msg)
	}

	// Unknown fields must not break decoding: a newer connector may
	// add fields an older host ignores.
	var res ResultFromConn
	line = `{"type":"result","id":"42","error":"","future_field":true}`
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		t.Fatalf("decode result with unknown field: %v", err)
	}
	if res.ID != "42" || res.Error != "" {
		t.Errorf("result fields: %+v", res)
	}
}
