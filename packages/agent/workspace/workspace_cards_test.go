package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/egress"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkspaceCardsCRUD drives the cards.* control-group verbs through a real
// Workspace: import → list → get → edit → delete, asserting the inventory
// summary a library grid needs and that an edit round-trips through a reload.
func TestWorkspaceCardsCRUD(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	if r, err := w.CardsList(ctx); err != nil || len(r.Cards) != 0 {
		t.Fatalf("empty library: %v, %d", err, len(r.Cards))
	}

	src := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Iris","first_mes":"hi",` +
		`"alternate_greetings":["a","b"],"post_history_instructions":"stay",` +
		`"character_book":{"entries":[{"keys":["k"],"content":"c"}]}}}`
	view, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "Iris" {
		t.Fatalf("import returned %q", view.Name)
	}
	if view.Greetings != 3 || view.BookEntries != 1 || !view.HasPHI {
		t.Fatalf("inventory wrong: greetings=%d book=%d phi=%v", view.Greetings, view.BookEntries, view.HasPHI)
	}
	if len(view.Raw) == 0 {
		t.Error("CardView must carry the raw card json for round-trip")
	}
	id := view.ID

	if r, _ := w.CardsList(ctx); len(r.Cards) != 1 || r.Cards[0].ID != id {
		t.Fatalf("list after import: %+v", r.Cards)
	}

	got, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: id})
	if err != nil || got.Name != "Iris" {
		t.Fatalf("get: %+v, %v", got, err)
	}

	edited := `{"spec":"chara_card_v2","data":{"name":"Iris","description":"changed"}}`
	e, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{ID: id, Card: json.RawMessage(edited)})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != id {
		t.Errorf("edit changed the id: %q", e.ID)
	}
	got2, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data struct {
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got2.Raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Data.Description != "changed" {
		t.Errorf("edit not persisted: %q", doc.Data.Description)
	}

	if err := w.CardsDelete(ctx, ctrlproto.CardDeleteParams{ID: id}); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.CardsList(ctx); len(r.Cards) != 0 {
		t.Errorf("list after delete: %+v", r.Cards)
	}
}

// TestWorkspaceCardsErrors covers the wire error shapes.
func TestWorkspaceCardFavorite(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	view, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Fav","first_mes":"hi"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	id := view.ID

	// Summaries carry Added and are not favorited by default.
	r, _ := w.CardsList(ctx)
	if len(r.Cards) != 1 || r.Cards[0].Favorite {
		t.Fatalf("default not favorite: %+v", r.Cards)
	}
	if r.Cards[0].Added.IsZero() {
		t.Error("summary must carry Added (the recently-added sort key)")
	}

	// Favorite it → reflected in both list and get.
	if err := w.CardFavorite(ctx, ctrlproto.CardFavoriteParams{ID: id, Favorite: true}); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.CardsList(ctx); !r.Cards[0].Favorite {
		t.Error("CardsList must mark the favorite")
	}
	if got, _ := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: id}); !got.Favorite {
		t.Error("CardsGet must mark the favorite")
	}

	// Clearing it un-marks.
	if err := w.CardFavorite(ctx, ctrlproto.CardFavoriteParams{ID: id, Favorite: false}); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.CardsList(ctx); r.Cards[0].Favorite {
		t.Error("clearing favorite must un-mark")
	}
}

func TestWorkspaceCardsErrors(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	if _, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: "missing-000000000000"}); err == nil {
		t.Error("get on a missing card should error")
	}
	if _, err := w.CardsImport(ctx, ctrlproto.CardImportParams{}); err == nil {
		t.Error("import with no bytes, path, or url should error")
	}
	if _, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{ID: "x-1"}); err == nil {
		t.Error("edit with an empty body should error")
	}
}

// TestWorkspaceCardsLint drives cards.lint through a real Workspace: a stored
// card with a malformed macro yields the deterministic findings (a warn for the
// macro that would leak into the prompt), and a missing card is a clean error.
func TestWorkspaceCardsLint(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	src := `{"spec":"chara_card_v2","data":{"name":"Kobeni","description":"{{char}} greets {{user)} warmly.","first_mes":"hi"}}`
	view, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.CardsLint(ctx, ctrlproto.CardLintParams{ID: view.ID})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	var warnMalformed bool
	for _, f := range res.Findings {
		if f.Rule == "malformed-macro" && f.Severity == "warn" && strings.Contains(f.Detail, "{{user)") {
			warnMalformed = true
		}
	}
	if !warnMalformed {
		t.Errorf("expected a malformed-macro warn for {{user)}, got %+v", res.Findings)
	}

	if _, err := w.CardsLint(ctx, ctrlproto.CardLintParams{ID: "missing-000000000000"}); err == nil {
		t.Error("lint on a missing card should error")
	}
}

// TestSpawnFromLibraryCard closes the phase-2 spawn gap: CreateOpts.Card can be
// a library id (not just a path), so a controller starts a chat straight from a
// stored card and the session takes on that card's identity.
func TestSpawnFromLibraryCard(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Nova","first_mes":"Hi there."}`)})
	if err != nil {
		t.Fatal(err)
	}

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatalf("spawn from library card id: %v", err)
	}
	live := w.live(info.ID)
	if live == nil {
		t.Fatal("spawned session is not live")
	}
	if live.persona != "Nova" {
		t.Errorf("session identity = %q, want Nova (the card's)", live.persona)
	}
	// The character greets the user: the card's first_mes is seeded as message 0.
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || got[0] != "Hi there." {
		t.Errorf("greeting not seeded: %v, want [\"Hi there.\"]", got)
	}

	if _, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: "no-such-card-000000000000"}); err == nil {
		t.Error("spawning from a nonexistent card id should error")
	}
}

// TestSpawnCardSeedsGreetingSwipes: a card with alternate_greetings seeds the
// whole opening set as message-0 swipe variants on a fresh session — the selected
// greeting active, the others switchable.
func TestSpawnCardSeedsGreetingSwipes(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Nova","first_mes":"Hi there.","alternate_greetings":["A cold open.","A warm open."]}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)

	// The active opening is first_mes (greeting 0).
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || got[0] != "Hi there." {
		t.Errorf("active greeting = %v, want [Hi there.]", got)
	}
	// The tail carries all three openings as swipe variants.
	snap := live.snapshot()
	if snap.Tail == nil || snap.Tail.SpanStart != 0 || snap.Tail.Variants != 3 || snap.Tail.Active != 0 {
		t.Fatalf("tail = %+v, want {span_start:0 variants:3 active:0}", snap.Tail)
	}
	// Swipe to the second alternate (index 2) — the opening changes in place.
	if err := w.SwipeTurn(ctx, info.ID, live.agent.TranscriptEpoch(), 2); err != nil {
		t.Fatalf("swipe: %v", err)
	}
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || got[0] != "A warm open." {
		t.Errorf("after swipe = %v, want [A warm open.]", got)
	}

	// A single-greeting card seeds one opening with nothing to swipe.
	plain, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Solo","first_mes":"Just me."}`)})
	if err != nil {
		t.Fatal(err)
	}
	pInfo, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: plain.ID})
	if err != nil {
		t.Fatal(err)
	}
	if snap := w.live(pInfo.ID).snapshot(); snap.Tail != nil {
		t.Errorf("single-greeting card should carry no swipe tail, got %+v", snap.Tail)
	}
}

// TestCardsExport: a JSON-imported card exports as CCv2 JSON; a PNG-imported
// card exports as a CCv2 PNG whose embedded data re-imports to the same card
// (round-trip). A missing card and a forced-png-without-avatar are clean errors.
func TestCardsExport(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	// A JSON card (no avatar) exports as JSON that parses back.
	jv, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Flat Iris","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	je, err := w.CardsExport(ctx, ctrlproto.CardExportParams{ID: jv.ID})
	if err != nil {
		t.Fatalf("export json: %v", err)
	}
	if je.MimeType != "application/json" || !strings.HasSuffix(je.Filename, ".json") {
		t.Fatalf("json export = filename %q mime %q", je.Filename, je.MimeType)
	}
	if c, err := card.ParseJSON(je.Bytes); err != nil || c.Name != "Flat Iris" {
		t.Errorf("json export does not parse: %v / %q", err, c.Name)
	}
	// Forcing png on an avatarless card is a bad request.
	if _, err := w.CardsExport(ctx, ctrlproto.CardExportParams{ID: jv.ID, Format: "png"}); err == nil {
		t.Error("png export of an avatarless card should error")
	}

	// A PNG card (with avatar) exports as a CCv2 PNG; re-importing it yields the
	// SAME card id (the export embeds the current, content-identical JSON).
	pngBytes, err := os.ReadFile("../../../examples/cards/aava-v2.png")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: pngBytes})
	if err != nil {
		t.Fatal(err)
	}
	pe, err := w.CardsExport(ctx, ctrlproto.CardExportParams{ID: pv.ID})
	if err != nil {
		t.Fatalf("export png: %v", err)
	}
	if pe.MimeType != "image/png" || !strings.HasSuffix(pe.Filename, ".png") || !card.IsPNG(pe.Bytes) {
		t.Fatalf("png export = filename %q mime %q isPNG %v", pe.Filename, pe.MimeType, card.IsPNG(pe.Bytes))
	}
	reimport, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: pe.Bytes})
	if err != nil {
		t.Fatalf("re-import export: %v", err)
	}
	if reimport.ID != pv.ID {
		t.Errorf("export round-trip id = %q, want %q", reimport.ID, pv.ID)
	}

	if _, err := w.CardsExport(ctx, ctrlproto.CardExportParams{ID: "nope-000000000000"}); err == nil {
		t.Error("exporting a missing card should error")
	}
}

// TestFetchCardBytes drives the URL-import fetch helper: a happy fetch, the size
// cap, and a non-2xx status. The httptest server binds to loopback, which the
// production default guard blocks — so the test injects an allow-loopback guard,
// exactly the seam that lets CardsImport keep the strict deny-non-public default.
func TestFetchCardBytes(t *testing.T) {
	body := []byte(`{"name":"Remote","first_mes":"hi"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/card.json":
			_, _ = w.Write(body)
		case "/huge":
			_, _ = w.Write(make([]byte, 64))
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	guard := egress.New(egress.AllowCIDR("127.0.0.0/8"), egress.AllowCIDR("::1/128"))
	ctx := context.Background()

	got, err := fetchCardBytes(ctx, guard, srv.URL+"/card.json", 1<<20)
	if err != nil {
		t.Fatalf("happy fetch: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("fetched %q, want %q", got, body)
	}
	if _, err := fetchCardBytes(ctx, guard, srv.URL+"/huge", 16); err == nil {
		t.Error("a body over the cap should error, not truncate into a corrupt card")
	}
	if _, err := fetchCardBytes(ctx, guard, srv.URL+"/missing", 1<<20); err == nil {
		t.Error("a non-2xx response should error")
	}
}

// TestCardsImportURLBlocksSSRF: the default guard behind cards.import refuses a
// URL aimed at the daemon's own loopback/private network or the cloud-metadata
// endpoint, and rejects non-http schemes — all before any connection is made.
func TestCardsImportURLBlocksSSRF(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	for _, bad := range []string{
		"http://127.0.0.1/card.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.1.2.3/card.png",
		"http://[::1]/card.png",
		"file:///etc/passwd",
		"ftp://example.com/card.png",
	} {
		if _, err := w.CardsImport(ctx, ctrlproto.CardImportParams{URL: bad}); err == nil {
			t.Errorf("cards.import should refuse %q", bad)
		}
	}
}

// TestCardSummaryAvatarURL pins the media-URL scheme (a pure function of whether
// an avatar was retained) without needing a PNG at this layer.
func TestCardSummaryAvatarURL(t *testing.T) {
	withAvatar := cardSummary(build.StoredCard{ID: "x-1", Card: card.Card{Name: "n"}, AvatarExt: "png"})
	if withAvatar.AvatarURL != "/media/cards/x-1" {
		t.Errorf("avatar url = %q, want /media/cards/x-1", withAvatar.AvatarURL)
	}
	noAvatar := cardSummary(build.StoredCard{ID: "y-2", Card: card.Card{Name: "n"}})
	if noAvatar.AvatarURL != "" {
		t.Errorf("no-avatar url should be empty, got %q", noAvatar.AvatarURL)
	}
}
