package workspace

import (
	"context"
	"os"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// TestCardsDoctorLive runs the REAL doctor pass against a real model — gated
// behind TERVA_DOCTOR_LIVE=1 so it never runs in CI. It imports the card at
// DOCTOR_CARD (default ~/Downloads/kobeni.png) into the library and calls the
// workspace's CardsDoctor with the configured provider/model, printing the
// structured proposals. This is a manual smoke to confirm the model returns the
// expected JSON shape and that Seppä's suggestions are useful.
//
//	TERVA_DOCTOR_LIVE=1 DOCTOR_PROVIDER=anthropic go test ./packages/agent/workspace/ \
//	  -run TestCardsDoctorLive -v -count=1 -timeout 120s
func TestCardsDoctorLive(t *testing.T) {
	if os.Getenv("TERVA_DOCTOR_LIVE") != "1" {
		t.Skip("set TERVA_DOCTOR_LIVE=1 to run the live card-doctor pass")
	}
	cardPath := os.Getenv("DOCTOR_CARD")
	if cardPath == "" {
		cardPath = os.ExpandEnv("$HOME/Downloads/kobeni.png")
	}

	// Import the card into the real library (idempotent — a content-hash id).
	sc, err := build.NewCardStore().ImportPath(cardPath)
	if err != nil {
		t.Fatalf("import %s: %v", cardPath, err)
	}
	t.Logf("card %q id=%s — %d lint findings", sc.Card.Name, sc.ID, len(card.Lint(sc.Card)))
	for _, f := range card.Lint(sc.Card) {
		t.Logf("  lint [%s] %s: %s %s", f.Severity, f.Field, f.Message, f.Detail)
	}

	prov := os.Getenv("DOCTOR_PROVIDER")
	if prov == "" {
		prov = "anthropic"
	}
	model := os.Getenv("DOCTOR_MODEL")
	if model == "" {
		model = build.DefaultModelForProvider(prov)
	}
	w, err := NewWorkspace(build.Args{Provider: prov, Model: model, CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Logf("doctor model: %s/%s", prov, model)

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	res, err := w.CardsDoctor(ctx, ctrlproto.DoctorParams{ID: sc.ID})
	if err != nil {
		t.Fatalf("CardsDoctor: %v", err)
	}

	t.Logf("NOTE: %s", res.Note)
	t.Logf("%d proposals:", len(res.Proposals))
	for _, p := range res.Proposals {
		t.Logf("\n── [%s] %s (id=%s)\n  why: %s\n  BEFORE: %s\n  AFTER:  %s", p.Severity, p.Field, p.ID, p.Rationale, truncate(p.Before, 200), truncate(p.After, 400))
	}
	if len(res.Proposals) == 0 && res.Note == "" {
		t.Error("doctor returned neither proposals nor a note — the parse may be wrong")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
