package connhost

import (
	"testing"

	"terva.sh/terva/packages/agent/chat"
)

func TestSessionParentContext(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		s := New(Config{Deliver: func(chat.Message) {}, Log: func(string) {}})
		s.protocol = 2
		s.chatParents = negotiated
		var delivered []chat.Message
		s.cfg.Deliver = func(m chat.Message) { delivered = append(delivered, m) }
		valid := `{"type":"message","id":"m","chat_id":"thread","chat_kind":"thread","parent_chat_id":"parent","parent_chat_kind":"dm","user_id":"owner"}`
		s.handleFrame([]byte(valid))
		if negotiated {
			if len(delivered) != 1 || delivered[0].ParentChatID != "parent" || delivered[0].ParentChatKind != "dm" || delivered[0].ChatID != "thread" {
				t.Fatalf("parent round trip = %+v", delivered)
			}
		} else if len(delivered) != 0 {
			t.Fatal("unsolicited metadata delivered")
		}
		before := len(delivered)
		for _, invalid := range []string{
			`{"type":"message","chat_id":"thread","chat_kind":"thread","parent_chat_id":"parent"}`,
			`{"type":"message","chat_id":"thread","chat_kind":"thread","parent_chat_id":"","parent_chat_kind":""}`,
			`{"type":"message","chat_id":"thread","chat_kind":"thread","parent_chat_id":null,"parent_chat_kind":null}`,
			`{"type":"message","chat_id":"thread","chat_kind":"thread","parent_chat_id":"thread","parent_chat_kind":"group"}`,
			`{"type":"message","chat_id":"thread","chat_kind":"dm","parent_chat_id":"parent","parent_chat_kind":"dm"}`,
			`{"type":"message","chat_id":"thread","chat_kind":"thread","parent_chat_id":"parent","parent_chat_kind":"thread"}`,
		} {
			s.handleFrame([]byte(invalid))
		}
		if len(delivered) != before {
			t.Fatal("invalid metadata fell back to legacy routing")
		}
		s.handleFrame([]byte(`{"type":"message","chat_id":"legacy","user_id":"owner","text":"control"}`))
		if len(delivered) != before+1 || delivered[len(delivered)-1].ChatID != "legacy" {
			t.Fatal("legacy control failed")
		}
	}
}

func TestSessionAdvertisesParentContext(t *testing.T) {
	s, _, _ := startEventSession(t, []string{"chat_parents"}, "")
	if !s.chatParents {
		t.Fatal("handshake did not enable chat_parents")
	}
	if !contains(hostFeatures, "chat_parents") {
		t.Fatal("host did not advertise chat_parents")
	}
}
