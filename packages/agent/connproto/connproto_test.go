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
			// Protocol 2 (stage A): identity fields are additive —
			// id/ts own the message's identity, reply_to is truly
			// in-reply-to, chat_kind/chat_title describe the chat.
			"message v2 identity",
			MessageFromConn{Type: "message", ID: "m10", TS: 1751469000123, ChatID: "c1",
				ChatKind: "group", ChatTitle: "ops", UserID: "u1", Username: "drew",
				ReplyTo: "m9", Text: "hi"},
			`{"type":"message","id":"m10","ts":1751469000123,"chat_id":"c1","chat_kind":"group","chat_title":"ops","user_id":"u1","username":"drew","reply_to":"m9","text":"hi"}`,
		},
		{
			// Stage B: minimum-viable markup; bot_mention drives group
			// mention-gating.
			"message with entities",
			MessageFromConn{Type: "message", ID: "m1", ChatID: "c1", ChatKind: "group",
				UserID: "u1", Text: "hey @tervabot look",
				Entities: []Entity{
					{Kind: "bot_mention", Offset: 4, Length: 9},
					{Kind: "mention", Offset: 20, Length: 5, UserID: "u9"},
				}},
			`{"type":"message","id":"m1","chat_id":"c1","chat_kind":"group","user_id":"u1","text":"hey @tervabot look","entities":[{"kind":"bot_mention","offset":4,"length":9},{"kind":"mention","offset":20,"length":5,"user_id":"u9"}]}`,
		},
		{
			"chat_membership added",
			ChatMembershipFromConn{Type: "chat_membership",
				Chat:   MembershipChat{ID: "c9", Kind: "group", Title: "ops"},
				Change: "added", ByUserID: "u1", ByUsername: "drew"},
			`{"type":"chat_membership","chat":{"id":"c9","kind":"group","title":"ops"},"change":"added","by_user_id":"u1","by_username":"drew"}`,
		},
		{
			"chat_membership removed minimal",
			ChatMembershipFromConn{Type: "chat_membership",
				Chat: MembershipChat{ID: "c9"}, Change: "removed"},
			`{"type":"chat_membership","chat":{"id":"c9"},"change":"removed"}`,
		},
		{
			// Stage D inbound: edits reference the ORIGINAL id.
			"message_edited",
			MessageEditedFromConn{Type: "message_edited", ChatID: "c1", ID: "m10",
				TS: 1751469000123, Text: "fixed typo",
				Entities: []Entity{{Kind: "bot_mention", Offset: 0, Length: 4}}},
			`{"type":"message_edited","chat_id":"c1","id":"m10","ts":1751469000123,"text":"fixed typo","entities":[{"kind":"bot_mention","offset":0,"length":4}]}`,
		},
		{
			"message_deleted",
			MessageDeletedFromConn{Type: "message_deleted", ChatID: "c1", ID: "m10"},
			`{"type":"message_deleted","chat_id":"c1","id":"m10"}`,
		},
		{
			"reaction added",
			ReactionFromConn{Type: "reaction", ChatID: "c1", MessageID: "m-90",
				UserID: "u1", Username: "drew", Key: "👍"},
			`{"type":"reaction","chat_id":"c1","message_id":"m-90","user_id":"u1","username":"drew","key":"👍"}`,
		},
		{
			"reaction removed",
			ReactionFromConn{Type: "reaction", ChatID: "c1", MessageID: "m-90",
				UserID: "u1", Key: "👍", Removed: true},
			`{"type":"reaction","chat_id":"c1","message_id":"m-90","user_id":"u1","key":"👍","removed":true}`,
		},
		{
			// Stage E: attachments grow kinds; the v1 pair stays first.
			"message with attachment kinds",
			MessageFromConn{Type: "message", ID: "m1", ChatID: "c1", UserID: "u1",
				Attachments: []Attachment{{MimeType: "audio/ogg", Path: "/d/v.ogg",
					Kind: "voice", Name: "voice.ogg", Size: 38112, DurationMS: 4200,
					Caption: "listen to this"}}},
			`{"type":"message","id":"m1","chat_id":"c1","user_id":"u1","attachments":[{"mime_type":"audio/ogg","path":"/d/v.ogg","kind":"voice","name":"voice.ogg","size":38112,"duration_ms":4200,"caption":"listen to this"}]}`,
		},
		{
			// Stage D capability: how fast the host may stream edits.
			"hello with min_edit_interval",
			HelloFromConn{Type: "hello", Name: "discord", ProtocolMin: 1, ProtocolMax: 2,
				Capabilities: Capabilities{Features: []string{"edits_out"}, MinEditIntervalMS: 1000}},
			`{"type":"hello","name":"discord","protocol_min":1,"protocol_max":2,"capabilities":{"features":["edits_out"],"min_edit_interval_ms":1000}}`,
		},
		{
			"result ok",
			ResultFromConn{Type: "result", ID: "42"},
			`{"type":"result","id":"42"}`,
		},
		{
			"result v2 message id",
			ResultFromConn{Type: "result", ID: "42", MessageID: "m77"},
			`{"type":"result","id":"42","message_id":"m77"}`,
		},
		{
			// Stage I: thread_start's result carries the new chat.
			"result thread chat id",
			ResultFromConn{Type: "result", ID: "t1", ChatID: "c1.t-99"},
			`{"type":"result","id":"t1","chat_id":"c1.t-99"}`,
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
			// Stage G: answers are not command results — they arrive
			// whenever users interact, keyed to the ask by ask_id.
			"answer attested",
			AnswerFromConn{Type: "answer", AskID: "a1", Key: "approve", UserID: "u1",
				Username: "drew", Attestation: AttestationAttested},
			`{"type":"answer","ask_id":"a1","key":"approve","user_id":"u1","username":"drew","attestation":"attested"}`,
		},
		{
			"answer minimal",
			AnswerFromConn{Type: "answer", AskID: "a1", Key: "deny", UserID: "u2"},
			`{"type":"answer","ask_id":"a1","key":"deny","user_id":"u2"}`,
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
			// Protocol 2: the host advertises what it consumes; hello's
			// capabilities gain the same features list on the way in.
			"hello_ack v2 features",
			HelloAckFromHost{Type: "hello_ack", Protocol: 2, TervaVersion: "1.2.3",
				Capabilities: &Capabilities{Features: []string{"message_ids", "chat_kinds"}}},
			`{"type":"hello_ack","protocol":2,"terva_version":"1.2.3","capabilities":{"features":["message_ids","chat_kinds"]}}`,
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
			// Stage H: an alternate outbound identity for the cast.
			"send with speaker",
			SendFromHost{Type: "send", ID: "s1", ChatID: "c1", Text: "The airlock hisses open.",
				Speaker: &Speaker{Key: "kaiku", Name: "Kaiku", AvatarPath: "/data/av/kaiku.png"}},
			`{"type":"send","id":"s1","chat_id":"c1","text":"The airlock hisses open.","speaker":{"key":"kaiku","name":"Kaiku","avatar_path":"/data/av/kaiku.png"}}`,
		},
		{
			"send with name-only speaker",
			SendFromHost{Type: "send", ID: "s2", ChatID: "c1", Text: "hello",
				Speaker: &Speaker{Key: "aava", Name: "Aava"}},
			`{"type":"send","id":"s2","chat_id":"c1","text":"hello","speaker":{"key":"aava","name":"Aava"}}`,
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
			// Stage G: option keys ride the wire, never widgets — the
			// connector picks buttons / keyboards / reactions / text.
			"ask",
			AskFromHost{Type: "ask", ID: "a1", ChatID: "c1", ReplyTo: "m12",
				Text: "terva wants to run `rm -rf build/` — approve?",
				Options: []AskOption{
					{Key: "approve", Label: "Approve", Style: "affirm", Hint: "👍"},
					{Key: "deny", Label: "Deny", Style: "deny", Hint: "👎"},
				},
				RestrictTo: []string{"u1"}, ExpiresMS: 120000},
			`{"type":"ask","id":"a1","chat_id":"c1","reply_to":"m12","text":"terva wants to run ` + "`rm -rf build/`" + ` — approve?","options":[{"key":"approve","label":"Approve","style":"affirm","hint":"👍"},{"key":"deny","label":"Deny","style":"deny","hint":"👎"}],"restrict_to":["u1"],"expires_ms":120000}`,
		},
		{
			"ask minimal",
			AskFromHost{Type: "ask", ID: "a1", ChatID: "c1", Text: "pick one",
				Options: []AskOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}}},
			`{"type":"ask","id":"a1","chat_id":"c1","text":"pick one","options":[{"key":"a","label":"A"},{"key":"b","label":"B"}]}`,
		},
		{
			"ask_close",
			AskCloseFromHost{Type: "ask_close", ID: "a2", AskID: "a1", Outcome: "Approve — @drew"},
			`{"type":"ask_close","id":"a2","ask_id":"a1","outcome":"Approve — @drew"}`,
		},
		{
			"ask_close bare withdrawal",
			AskCloseFromHost{Type: "ask_close", ID: "a2", AskID: "a1"},
			`{"type":"ask_close","id":"a2","ask_id":"a1"}`,
		},
		{
			"thread_start",
			ThreadStartFromHost{Type: "thread_start", ID: "t1", ChatID: "c1",
				FromMessageID: "m-12", Name: "refactor: extract session core"},
			`{"type":"thread_start","id":"t1","chat_id":"c1","from_message_id":"m-12","name":"refactor: extract session core"}`,
		},
		{
			"thread_start anchorless",
			ThreadStartFromHost{Type: "thread_start", ID: "t2", ChatID: "c1", Name: "sidebar"},
			`{"type":"thread_start","id":"t2","chat_id":"c1","name":"sidebar"}`,
		},
		{
			// Stage D outbound: the streaming-reply primitive.
			"edit",
			EditFromHost{Type: "edit", ID: "e1", ChatID: "c1", MessageID: "m-90", Text: "updated"},
			`{"type":"edit","id":"e1","chat_id":"c1","message_id":"m-90","text":"updated"}`,
		},
		{
			"react",
			ReactFromHost{Type: "react", ID: "r1", ChatID: "c1", MessageID: "m-12", Key: "👀"},
			`{"type":"react","id":"r1","chat_id":"c1","message_id":"m-12","key":"👀"}`,
		},
		{
			"react remove",
			ReactFromHost{Type: "react", ID: "r2", ChatID: "c1", MessageID: "m-12", Key: "👀", Remove: true},
			`{"type":"react","id":"r2","chat_id":"c1","message_id":"m-12","key":"👀","remove":true}`,
		},
		{
			"delete",
			DeleteFromHost{Type: "delete", ID: "d1", ChatID: "c1", MessageID: "m-90"},
			`{"type":"delete","id":"d1","chat_id":"c1","message_id":"m-90"}`,
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
