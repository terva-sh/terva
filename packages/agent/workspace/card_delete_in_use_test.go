package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// importTestCard puts a minimal JSON character card in the library and returns
// its id.
func importTestCard(t *testing.T, w *Workspace, name string) string {
	t.Helper()
	doc := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"` + name +
		`","description":"a character under test","first_mes":"Hello."}}`
	v, err := w.CardsImport(context.Background(), ctrlproto.CardImportParams{Bytes: []byte(doc)})
	if err != nil {
		t.Fatalf("CardsImport: %v", err)
	}
	return v.ID
}

// TestACardWithChatsOnItCannotBeDeleted is the defect.
//
// cards.delete was an os.RemoveAll with no in-use check, and a session
// re-resolves SessionMeta.Card on every materialize — so deleting a card with
// chats on it did not degrade them, it stopped them opening for good, and the
// bytes were gone so nothing brought them back.
func TestACardWithChatsOnItCannotBeDeleted(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	id := importTestCard(t, w, "Kobeni")
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{
		Experience: "chat", Card: id,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seedTurn(t, w, info.ID)

	err = w.CardsDelete(context.Background(), ctrlproto.CardDeleteParams{ID: id})
	if err == nil {
		t.Fatal("the card was deleted while a chat was still bound to it; that chat can no longer open")
	}
	if !strings.Contains(err.Error(), "1 chat") {
		t.Errorf("the refusal does not say how many chats hold the card: %v", err)
	}
	// Refusing is only useful if the card is actually still there.
	if _, err := w.CardsGet(context.Background(), ctrlproto.CardGetParams{ID: id}); err != nil {
		t.Errorf("the card is gone despite the refusal: %v", err)
	}
}

// TestACardWithNoChatsIsStillDeletable: the guard must not turn the library
// read-only. A card nobody has talked to deletes as it always did.
func TestACardWithNoChatsIsStillDeletable(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	id := importTestCard(t, w, "Nobody")
	if err := w.CardsDelete(context.Background(), ctrlproto.CardDeleteParams{ID: id}); err != nil {
		t.Fatalf("an unused card must still be deletable: %v", err)
	}
	if _, err := w.CardsGet(context.Background(), ctrlproto.CardGetParams{ID: id}); err == nil {
		t.Error("the card survived a delete that reported success")
	}
}

// TestDeletingTheChatReleasesTheCard is the way out of the refusal, and it has
// to work or the guard has made the card permanently undeletable.
func TestDeletingTheChatReleasesTheCard(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	id := importTestCard(t, w, "Kobeni")
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat", Card: id})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seedTurn(t, w, info.ID)
	if err := w.CardsDelete(context.Background(), ctrlproto.CardDeleteParams{ID: id}); err == nil {
		t.Fatal("expected the delete to be refused while the chat exists")
	}

	if err := w.DeleteSession(context.Background(), info.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if err := w.CardsDelete(context.Background(), ctrlproto.CardDeleteParams{ID: id}); err != nil {
		t.Fatalf("the card is still refused after its only chat was deleted — it can never be removed: %v", err)
	}
}

// TestArchivingTheChatAlsoReleasesTheCard pins the other exit, and the reason
// archives are excluded from the scan: DeleteSession removes the .jsonl and
// never the .jsonl.gz, so a card counted against an archive could not be deleted
// by any sequence of actions.
func TestArchivingTheChatAlsoReleasesTheCard(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	id := importTestCard(t, w, "Kobeni")
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat", Card: id})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seedTurn(t, w, info.ID)
	if _, err := w.ArchiveSession(context.Background(), info.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if err := w.CardsDelete(context.Background(), ctrlproto.CardDeleteParams{ID: id}); err != nil {
		t.Fatalf("archiving a chat must release its card, or a card with one is undeletable forever: %v", err)
	}
}
