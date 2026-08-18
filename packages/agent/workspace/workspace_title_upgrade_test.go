package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// A Stage session opens on the bound character's name — instant and free, which
// is right for the moment a chat opens. But every chat with that character gets
// the same one, and the Library already draws the character name beside the
// title, so three chats with Kobeni all read "Kobeni / Kobeni". Dogfooding bore
// that out: the user hit ✨ regenerate twice and settled on a scene summary.
//
// So the character name is PROVISIONAL and upgrades once the scene has enough in
// it to summarize. Everything deciding whether tokens get spent is in
// titleUpgradeDue; these pin it without a model call.
func titleTestSession(t *testing.T, cardName string, playerLines int) *wsSession {
	t.Helper()
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{
		Bytes: []byte(`{"name":"` + cardName + `","first_mes":"Hi there."}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("session is not live")
	}
	msgs := s.agent.Messages()
	for i := 0; i < playerLines; i++ {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "player line"}}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "reply"}}})
	}
	s.agent.SetMessages(msgs)
	return s
}

func TestTitleUpgradeWaitsForAScene(t *testing.T) {
	// One exchange in: a title drawn from here says "User greets the shopkeeper".
	s := titleTestSession(t, "Kobeni", 1)
	s.setTitle("Kobeni", true)
	if _, due := s.titleUpgradeDue(); due {
		t.Error("upgraded after a single exchange — that spends tokens on a chat that may be abandoned")
	}

	// By the third player turn the scene has a subject.
	s2 := titleTestSession(t, "Kobeni", immersiveTitleUpgradeTurns)
	s2.setTitle("Kobeni", true)
	prev, due := s2.titleUpgradeDue()
	if !due {
		t.Error("a scene with three player turns should earn a real title")
	}
	if prev != "Kobeni" {
		t.Errorf("prev title = %q, want the provisional character name", prev)
	}
}

// A manual rename outranks the machine, always. This is the guard that decides
// whether a title the user typed can be silently replaced.
func TestTitleUpgradeNeverTouchesAManualRename(t *testing.T) {
	s := titleTestSession(t, "Kobeni", 5)
	s.setTitle("My favourite scene", false) // false = not machine-generated
	if _, due := s.titleUpgradeDue(); due {
		t.Error("would overwrite a title the user typed")
	}

	// The case that actually isolates the provenance guard: the user renames the
	// session to the character's name ON PURPOSE. It is then byte-identical to the
	// provisional title, so only the machine-generated flag distinguishes "we chose
	// this" from "they chose this" — and choosing it themselves must still stick.
	s2 := titleTestSession(t, "Kobeni", 5)
	s2.setTitle("Kobeni", false)
	if _, due := s2.titleUpgradeDue(); due {
		t.Error("would overwrite a title the user typed, because it happened to match the one we would have picked")
	}
}

// Once upgraded, it stays upgraded: the guard keys on the title still BEING the
// character name, so this is a cheap no-op on every later turn rather than a
// title call per turn forever.
func TestTitleUpgradeFiresOnlyOnce(t *testing.T) {
	s := titleTestSession(t, "Kobeni", 5)
	s.setTitle("Kobeni's First Day at the Shop", true)
	if _, due := s.titleUpgradeDue(); due {
		t.Error("would re-generate a title it had already generated, once per turn")
	}
}

// An untitled session is settleTitle's job, not this one's.
func TestTitleUpgradeSkipsUntitled(t *testing.T) {
	s := titleTestSession(t, "Kobeni", 5)
	s.setTitle("", true)
	if _, due := s.titleUpgradeDue(); due {
		t.Error("an untitled session should be settled, not upgraded")
	}
}

// Machine-authored user messages are not the player speaking, so they must not
// push a scene over the threshold.
//
// This used to name only the harness-injected nudge, because that was the one
// this function's author had in mind — and the count went on treating a
// compaction summary and a tool-image mirror as the player, bringing an
// immersive retitle forward by however many of them the scene had accumulated.
// core.IsUserTurn is the shared answer; the cases below are what it covers.
func TestPlayerTurnsIgnoresMachineAuthoredMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "real"}}},
		{Role: provider.RoleUser, Meta: map[string]string{core.MetaSynthetic: "true"},
			Content: []provider.Content{provider.TextBlock{Text: "injected"}}},
		{Role: provider.RoleUser, Meta: map[string]string{core.MetaCompaction: "true"},
			Content: []provider.Content{provider.TextBlock{Text: "Conversation so far: …"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: core.ToolImageMirrorPrefix}}},
		{Role: provider.RoleUser, Meta: map[string]string{core.MetaClear: "true"}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "reply"}}},
	}
	if got := playerTurns(msgs); got != 1 {
		t.Errorf("playerTurns = %d, want 1 — only the first message is the player speaking", got)
	}
}

// A coding session is settleTitle's existing cascade; this pass must not touch it.
func TestTitleUpgradeIsImmersiveOnly(t *testing.T) {
	s := titleTestSession(t, "Kobeni", 5)
	s.setTitle("Kobeni", true)
	s.sess.Meta.Experience = ""
	before := s.title
	s.upgradeImmersiveTitle(context.Background())
	if s.title != before {
		t.Errorf("touched a non-immersive session's title: %q -> %q", before, s.title)
	}
}
