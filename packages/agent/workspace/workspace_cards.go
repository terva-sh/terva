package workspace

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/egress"
	"terva.sh/terva/packages/i18n"
)

// The card library on the wire. The Workspace is just the access point — the
// store is global ($TERVA_HOME/cards), shared by every workspace — so these are
// thin adapters over build.CardStore. Cards are ungated data (editing a card
// grants it nothing), so unlike personas.* there is no trust gate here.
var _ ctrlproto.CardsController = (*Workspace)(nil)

func (w *Workspace) cardStore() *build.CardStore { return build.NewCardStore() }

// cardName resolves a stored card's display name from its library ref, or "" if
// the ref is empty or the card can't be loaded. Used to title immersive sessions
// by their character (the design's "default title = character name").
func (w *Workspace) cardName(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return ""
	}
	sc, err := w.cardStore().Get(ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sc.Card.Name)
}

// CardsList summarizes every stored card, each tagged with whether it is a
// favorite (a stale favorite for a since-deleted card simply never matches).
func (w *Workspace) CardsList(_ context.Context) (ctrlproto.CardsListResult, error) {
	store := w.cardStore()
	stored, err := store.List()
	if err != nil {
		return ctrlproto.CardsListResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "list cards: %v", err)
	}
	favs, _ := store.Favorites() // best-effort: a read error just means nothing is highlighted
	out := make([]ctrlproto.CardSummary, 0, len(stored))
	for _, sc := range stored {
		s := cardSummary(sc)
		s.Favorite = favs[sc.ID]
		out = append(out, s)
	}
	return ctrlproto.CardsListResult{Cards: out}, nil
}

// CardsGet returns one card in full.
func (w *Workspace) CardsGet(_ context.Context, p ctrlproto.CardGetParams) (ctrlproto.CardView, error) {
	store := w.cardStore()
	sc, err := store.Get(p.ID)
	if err != nil {
		return ctrlproto.CardView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	v := cardView(sc)
	if favs, err := store.Favorites(); err == nil {
		v.Favorite = favs[sc.ID]
	}
	return v, nil
}

// CardFavorite sets or clears a card's favorite flag and broadcasts the library
// change so every open surface re-highlights. No existence check: favoriting is
// idempotent metadata, and a stale entry is filtered on read (CardsList).
func (w *Workspace) CardFavorite(_ context.Context, p ctrlproto.CardFavoriteParams) error {
	if err := w.cardStore().SetFavorite(p.ID, p.Favorite); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "favorite card: %v", err)
	}
	w.broadcastLibraryChanged()
	return nil
}

// CardsLint runs the deterministic card lint over a stored card — the card
// doctor's model-free orientation pass, also rendered as badges on the Stage
// character sheet. Read-only.
func (w *Workspace) CardsLint(_ context.Context, p ctrlproto.CardLintParams) (ctrlproto.CardLintResult, error) {
	sc, err := w.cardStore().Get(p.ID)
	if err != nil {
		return ctrlproto.CardLintResult{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	findings := card.Lint(sc.Card)
	res := ctrlproto.CardLintResult{Findings: make([]ctrlproto.CardLintFinding, len(findings))}
	for i, f := range findings {
		res.Findings[i] = ctrlproto.CardLintFinding{Rule: f.Rule, Severity: f.Severity, Field: f.Field, Message: f.Message, Detail: f.Detail}
	}
	return res, nil
}

// CardsImport ingests a PNG or JSON card. Bytes (an upload) wins over Path (a
// server-local file), which wins over URL (a remote card); at least one must be
// present.
func (w *Workspace) CardsImport(ctx context.Context, p ctrlproto.CardImportParams) (ctrlproto.CardView, error) {
	var (
		sc  build.StoredCard
		err error
	)
	switch {
	case len(p.Bytes) > 0:
		sc, err = w.cardStore().ImportBytes(p.Bytes)
	case p.Path != "":
		sc, err = w.cardStore().ImportPath(p.Path)
	case strings.TrimSpace(p.URL) != "":
		var data []byte
		// The default guard blocks the daemon's own loopback/private network,
		// so a pasted URL cannot be turned into an SSRF probe.
		data, err = fetchCardBytes(ctx, egress.New(), p.URL, maxCardFetchBytes)
		if err == nil {
			sc, err = w.cardStore().ImportBytes(data)
		}
	default:
		return ctrlproto.CardView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("cards.import needs bytes, a path, or a url"))
	}
	if err != nil {
		return ctrlproto.CardView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "import card: %v", err)
	}
	w.broadcastLibraryChanged()
	return cardView(sc), nil
}

// maxCardFetchBytes caps a URL-imported card: a character card (PNG-embedded or
// bare JSON) is small, so a body larger than this is not a card and the fetch is
// aborted rather than buffered.
const maxCardFetchBytes = 16 << 20 // 16 MiB

// fetchCardBytes GETs a card from a remote URL through the SSRF-guarded egress
// client. The guard is a parameter (not built here) so a test can inject an
// allow-loopback guard against an httptest server while production passes the
// default deny-non-public guard. The body is bounded by maxBytes.
func fetchCardBytes(ctx context.Context, guard *egress.Guard, rawURL string, maxBytes int64) ([]byte, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := guard.CheckURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	req.Header.Set("User-Agent", "terva-card-import")
	req.Header.Set("Accept", "image/png, application/json;q=0.9, */*;q=0.8")
	resp, err := guard.Client(30*time.Second, 0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	// Read one byte past the cap so an oversize body is detected, not truncated
	// into a corrupt card.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, i18n.Errorf("card too large (over %d bytes)", maxBytes)
	}
	return data, nil
}

// CardsEdit replaces a card's data with an edited document.
func (w *Workspace) CardsEdit(_ context.Context, p ctrlproto.CardEditParams) (ctrlproto.CardView, error) {
	if len(p.Card) == 0 {
		return ctrlproto.CardView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("cards.edit needs a card body"))
	}
	sc, err := w.cardStore().Edit(p.ID, p.Card)
	if err != nil {
		return ctrlproto.CardView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "edit card: %v", err)
	}
	w.broadcastLibraryChanged()
	return cardView(sc), nil
}

// CardsExport serializes a stored card for download: a CCv2 PNG (the current
// card JSON embedded in the retained avatar, so an edited card exports its
// current data) when the card has an avatar, else the CCv2 JSON. Format "png" /
// "json" forces one; "" auto-picks by whether an avatar exists.
func (w *Workspace) CardsExport(_ context.Context, p ctrlproto.CardExportParams) (ctrlproto.CardExport, error) {
	sc, err := w.cardStore().Get(p.ID)
	if err != nil {
		return ctrlproto.CardExport{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	return w.exportCard(sc, p.Format)
}

// exportCard serializes a stored card for download in the given format ("" =
// auto: PNG when it retains an avatar, else JSON) — the core of cards.export,
// shared with worlds.export, which embeds every roster card in this form.
func (w *Workspace) exportCard(sc build.StoredCard, format string) (ctrlproto.CardExport, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		if sc.HasAvatar() {
			format = "png"
		} else {
			format = "json"
		}
	}
	name := exportFilename(sc.Card.Name)
	switch format {
	case "json":
		return ctrlproto.CardExport{Filename: name + ".json", MimeType: "application/json", Bytes: sc.Raw}, nil
	case "png":
		path := w.cardStore().AvatarPath(sc.ID)
		if path == "" {
			return ctrlproto.CardExport{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("card %q has no avatar to embed in a PNG — export it as json", sc.ID))
		}
		avatar, err := os.ReadFile(path)
		if err != nil {
			return ctrlproto.CardExport{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "read avatar: %v", err)
		}
		out, err := card.WritePNG(avatar, sc.Raw)
		if err != nil {
			return ctrlproto.CardExport{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "embed card in png: %v", err)
		}
		return ctrlproto.CardExport{Filename: name + ".png", MimeType: "image/png", Bytes: out}, nil
	default:
		return ctrlproto.CardExport{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown export format %q (png|json)", format))
	}
}

// exportFilename turns a card name into a safe download filename stem.
func exportFilename(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "card"
	}
	return s
}

// CardsDelete removes a card from the library.
func (w *Workspace) CardsDelete(_ context.Context, p ctrlproto.CardDeleteParams) error {
	if err := w.cardStore().Delete(p.ID); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	w.broadcastLibraryChanged()
	return nil
}

// cardSummary projects a stored card into its wire summary + inventory.
func cardSummary(sc build.StoredCard) ctrlproto.CardSummary {
	c := sc.Card
	s := ctrlproto.CardSummary{
		ID:               sc.ID,
		Name:             c.Name,
		Creator:          c.Creator,
		CharacterVersion: c.CharacterVersion,
		SpecVersion:      c.SpecVersion,
		Tags:             c.Tags,
		Greetings:        1 + len(c.AlternateGreetings),
		HasPHI:           strings.TrimSpace(c.PostHistoryInstructions) != "",
		Added:            sc.Added,
	}
	if c.CharacterBook != nil {
		s.BookEntries = len(c.CharacterBook.Entries)
	}
	if sc.HasAvatar() {
		s.AvatarURL = cardAvatarURL(sc.ID)
	}
	return s
}

func cardView(sc build.StoredCard) ctrlproto.CardView {
	return ctrlproto.CardView{CardSummary: cardSummary(sc), Raw: sc.Raw, Warnings: sc.Warnings}
}

// cardAvatarURL is the media route a client fetches a card's portrait from
// (served by the /media handler, phase 2b). Kept next to the summary so the URL
// scheme has one owner.
func cardAvatarURL(id string) string { return "/media/cards/" + id }
