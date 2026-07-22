package build

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/testsupport"
)

// mkCharaPNG builds a minimal PNG carrying a card in a base64 `chara` tEXt chunk
// — the SillyTavern convention. ReadPNG ignores CRC content, so a placeholder
// CRC is fine; the pixels here are just the signature + chunks, which is exactly
// the "original bytes" the store must retain as the avatar.
func mkCharaPNG(cardJSON string) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	b.Write(mkPNGChunk("tEXt", []byte("chara\x00"+base64.StdEncoding.EncodeToString([]byte(cardJSON)))))
	b.Write(mkPNGChunk("IEND", nil))
	return b.Bytes()
}

func mkPNGChunk(ctype string, data []byte) []byte {
	var b bytes.Buffer
	var lenb [4]byte
	binary.BigEndian.PutUint32(lenb[:], uint32(len(data)))
	b.Write(lenb[:])
	b.WriteString(ctype)
	b.Write(data)
	b.Write([]byte{0, 0, 0, 0}) // CRC placeholder (ReadPNG ignores it)
	return b.Bytes()
}

func TestCardStoreImportJSONListGet(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	if cards, err := st.List(); err != nil || len(cards) != 0 {
		t.Fatalf("a fresh store must be empty: %v, %d", err, len(cards))
	}

	src := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Mara","first_mes":"hi"}}`
	sc, err := st.ImportBytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if sc.Card.Name != "Mara" {
		t.Fatalf("imported wrong card: %+v", sc.Card)
	}
	if sc.HasAvatar() {
		t.Error("a JSON import has no avatar")
	}
	if !strings.HasPrefix(sc.ID, "mara-") {
		t.Errorf("id should be slug-prefixed: %q", sc.ID)
	}

	got, err := st.Get(sc.ID)
	if err != nil || got.Card.Name != "Mara" {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if cards, _ := st.List(); len(cards) != 1 || cards[0].ID != sc.ID {
		t.Fatalf("list after import: %+v", cards)
	}
}

func TestCardStoreFavoritesAndAdded(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	a, err := st.ImportBytes([]byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Aa","first_mes":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.ImportBytes([]byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Bb","first_mes":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}

	// Get stamps Added from the card directory mtime.
	if got, _ := st.Get(a.ID); got.Added.IsZero() {
		t.Error("Get must stamp Added from the card directory mtime")
	}
	// A fresh store has no favorites.
	if favs, err := st.Favorites(); err != nil || len(favs) != 0 {
		t.Fatalf("fresh favorites: %v, %d", err, len(favs))
	}

	// Favorite both; both stick, order-independent.
	if err := st.SetFavorite(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFavorite(b.ID, true); err != nil {
		t.Fatal(err)
	}
	if favs, _ := st.Favorites(); !favs[a.ID] || !favs[b.ID] {
		t.Fatalf("both should be favorited: %v", favs)
	}
	// Un-favorite removes only the one, and is idempotent.
	if err := st.SetFavorite(a.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFavorite(a.ID, false); err != nil {
		t.Fatalf("un-favorite must be idempotent: %v", err)
	}
	if favs, _ := st.Favorites(); favs[a.ID] || !favs[b.ID] {
		t.Fatalf("only b should remain: %v", favs)
	}
	// SetFavorite does not chase deletions — a stale id stays in the set until
	// re-toggled (the controller filters it on read, like a group's stale member).
	if err := st.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if favs, _ := st.Favorites(); !favs[b.ID] {
		t.Error("a stale favorite id should survive the card's deletion")
	}
	// A malformed id is refused.
	if err := st.SetFavorite("../escape", true); err == nil {
		t.Error("SetFavorite must reject an invalid id")
	}
}

func TestCardStoreImportPNGKeepsOriginalAvatar(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	png := mkCharaPNG(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Seraphina"}}`)
	sc, err := st.ImportBytes(png)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.HasAvatar() || sc.AvatarExt != "png" {
		t.Fatalf("a PNG import must retain the avatar: %+v", sc)
	}
	ap := st.AvatarPath(sc.ID)
	if ap == "" {
		t.Fatal("AvatarPath empty for a card that has an avatar")
	}
	stored, err := os.ReadFile(ap)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, png) {
		t.Error("the stored avatar must be the ORIGINAL png bytes, byte-for-byte")
	}
}

func TestCardStoreImportIdempotent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	src := []byte(`{"name":"Dup"}`) // a flat V1 card
	a, err := st.ImportBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.ImportBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Errorf("re-importing identical content must be idempotent: %q != %q", a.ID, b.ID)
	}
	if cards, _ := st.List(); len(cards) != 1 {
		t.Errorf("idempotent import must not duplicate: got %d", len(cards))
	}
}

func TestCardStoreEditKeepsIDAndAvatar(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	png := mkCharaPNG(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Nyx","description":"old"}}`)
	sc, err := st.ImportBytes(png)
	if err != nil {
		t.Fatal(err)
	}

	edited := `{"spec":"chara_card_v2","data":{"name":"Nyx","description":"NEW bio","extensions":{"vendor":1}}}`
	sc2, err := st.Edit(sc.ID, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if sc2.ID != sc.ID {
		t.Errorf("edit must keep the id: %q != %q", sc2.ID, sc.ID)
	}
	if sc2.Card.Description != "NEW bio" {
		t.Errorf("edit did not apply: %q", sc2.Card.Description)
	}
	if !sc2.HasAvatar() {
		t.Error("edit must preserve the avatar")
	}

	got, err := st.Get(sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Card.Description != "NEW bio" || len(got.Card.Extensions) == 0 {
		t.Errorf("reload after edit lost data: %+v", got.Card)
	}
}

func TestCardStoreEditRejectsMissingAndNameless(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	if _, err := st.Edit("nope-000000000000", []byte(`{"name":"x"}`)); err == nil {
		t.Error("editing a missing card should error")
	}
	sc, err := st.ImportBytes([]byte(`{"name":"Keep"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(sc.ID, []byte(`{"description":"no name"}`)); err == nil {
		t.Error("editing with a nameless card body should error")
	}
}

func TestCardStoreDelete(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes([]byte(`{"name":"Gone"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(sc.ID); err == nil {
		t.Error("get after delete should fail")
	}
	if err := st.Delete(sc.ID); err == nil {
		t.Error("double delete should error")
	}
}

func TestCardStoreRejectsTraversalID(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()
	for _, bad := range []string{"", "../etc", "a/b", `a\b`, ".."} {
		if _, err := st.Get(bad); err == nil {
			t.Errorf("Get(%q) should be rejected", bad)
		}
		if err := st.Delete(bad); err == nil {
			t.Errorf("Delete(%q) should be rejected", bad)
		}
		if p := st.AvatarPath(bad); p != "" {
			t.Errorf("AvatarPath(%q) should be empty, got %q", bad, p)
		}
	}
}

func TestCardStoreImportPath(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	p := filepath.Join(testsupport.TempDir(t), "c.json")
	if err := os.WriteFile(p, []byte(`{"name":"FromPath"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := st.ImportPath(p)
	if err != nil || sc.Card.Name != "FromPath" {
		t.Fatalf("ImportPath: %+v, %v", sc, err)
	}
}

func TestResolveCardRef(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()
	sc, err := st.ImportBytes([]byte(`{"name":"Ref"}`))
	if err != nil {
		t.Fatal(err)
	}

	// A library id resolves to its stored, loadable card.json.
	got, err := ResolveCardRef(sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join(sc.ID, cardJSONName)) {
		t.Errorf("library id resolved to %q", got)
	}
	if c, err := card.Load(got); err != nil || c.Name != "Ref" {
		t.Fatalf("resolved path is not a loadable card: %v", err)
	}

	// A real file path passes through unchanged (the classic --card flow).
	fp := filepath.Join(testsupport.TempDir(t), "x.json")
	if err := os.WriteFile(fp, []byte(`{"name":"Path"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := ResolveCardRef(fp); err != nil || p != fp {
		t.Errorf("path passthrough: %q, %v", p, err)
	}

	// Empty stays empty; unknown id, traversal, and a directory all error.
	if p, _ := ResolveCardRef(""); p != "" {
		t.Errorf("empty ref should resolve empty, got %q", p)
	}
	for _, bad := range []string{"nope-000000000000", "../escape", ".."} {
		if _, err := ResolveCardRef(bad); err == nil {
			t.Errorf("ResolveCardRef(%q) should error", bad)
		}
	}
}
