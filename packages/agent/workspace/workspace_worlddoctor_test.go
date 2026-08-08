package workspace

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The world doctor reads the cast TOGETHER — the thing neither other doctor can
// do, because neither is ever shown more than one card. These cover the three
// places that go wrong: the evidence (does the ensemble actually reach the
// model, within a budget that holds), the parse (does a proposal apply to the
// card it claims), and the gate (does a run that could not apply anything cost
// nothing).

// worldDoctorFixture is a workspace plus a saved World — the doctor's whole
// world now that it reads the LIBRARY rather than a session's working copy.
func worldDoctorFixture(t *testing.T, roster map[string]string) (*Workspace, build.WorldDoc) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	doc, err := build.NewWorldStore().Save(build.WorldDoc{Name: "Tokyo Division", Characters: roster})
	if err != nil {
		t.Fatal(err)
	}
	return w, doc
}

// storeCard puts a real card in the library so the doctor can read it back the
// way it will in production — through cardStore, not through a fixture struct.
func storeCard(t *testing.T, name, description, personality string) string {
	t.Helper()
	sc, err := build.NewCardStore().ImportBytes([]byte(`{"name":"` + name + `","description":"` + description +
		`","personality":"` + personality + `","first_mes":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	return sc.ID
}

func TestRenderWorldDoctorEvidence(t *testing.T) {
	roster := []worldDoctorCard{
		{Name: "Kobeni", Ref: "kobeni-1", Card: card.Card{
			Name: "Kobeni", Description: "a nervous devil hunter", Personality: "anxious", FirstMes: "...hello?",
		}},
		{Name: "Aki", Ref: "aki-2", Card: card.Card{Name: "Aki", Description: "her senior"}, Findings: []card.Finding{
			{Rule: "empty-personality", Severity: "warn", Field: "personality", Detail: "no personality written"},
		}},
	}
	lore := []core.WorldLoreEntry{{Name: "The Bureau", Content: "Public Safety runs the hunts.", Constant: true}}

	got := renderWorldDoctorEvidence("Tokyo Division", "devil hunters on salary", roster, lore, nil, "give Kobeni a rival")

	// Both cards, addressed by the ref a card proposal has to name.
	for _, want := range []string{"### Kobeni  [card kobeni-1]", "### Aki  [card aki-2]", "a nervous devil hunter"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence missing %q:\n%s", want, got)
		}
	}
	// An empty field is STATED. "This character has no personality" is one of
	// the most useful things an ensemble read can notice, and a silent omission
	// would hide it behind an absence that reads as nothing to say.
	if !strings.Contains(got, "personality: (empty)") {
		t.Error("an empty field must be stated, not skipped")
	}
	if !strings.Contains(got, "LINT [warn] empty-personality") {
		t.Error("evidence missing the deterministic lint")
	}
	if !strings.Contains(got, "The Bureau") {
		t.Error("evidence missing the lorebook")
	}
	// The steer leads. Position is not cosmetic here: the model-facing text eval
	// found a directive placed after the detail it governs is followed far less
	// often than one placed before it.
	steerAt, castAt := strings.Index(got, "give Kobeni a rival"), strings.Index(got, "THE CAST")
	if steerAt < 0 || steerAt > castAt {
		t.Errorf("the steer must precede the evidence it directs (steer@%d, cast@%d)", steerAt, castAt)
	}
	// A World assembled but never played is the case this doctor was ASKED for,
	// so an empty scene must read as a normal state rather than missing evidence.
	if !strings.Contains(got, "has not been played yet") {
		t.Error("an unplayed World must say so rather than render an empty scene block")
	}
}

// budgetShares is the load-bearing piece of the evidence: five cards and a
// lorebook is a bigger prompt than either other doctor was priced for.
func TestBudgetShares(t *testing.T) {
	t.Run("everything fits when there is room", func(t *testing.T) {
		got := budgetShares([]int{100, 200, 300}, 1000)
		for i, want := range []int{100, 200, 300} {
			if got[i] != want {
				t.Errorf("share[%d] = %d, want the whole %d", i, got[i], want)
			}
		}
	})

	// The point of water-filling: a small card keeps ALL of its text and donates
	// the rest of its equal share to the big one, rather than both being clipped
	// to total/N with the small card's room wasted.
	t.Run("the largest is trimmed first and the smallest is untouched", func(t *testing.T) {
		got := budgetShares([]int{10, 990}, 500)
		if got[0] != 10 {
			t.Errorf("the small entry should keep all 10, got %d", got[0])
		}
		if got[1] != 490 {
			t.Errorf("the large entry should absorb the remainder (490), got %d", got[1])
		}
	})

	t.Run("shares out evenly when everyone is over", func(t *testing.T) {
		got := budgetShares([]int{1000, 1000, 1000}, 300)
		for i, n := range got {
			if n != 100 {
				t.Errorf("share[%d] = %d, want 100", i, n)
			}
		}
	})

	t.Run("never hands out more than the budget", func(t *testing.T) {
		for _, sizes := range [][]int{{1, 2, 3}, {500, 500}, {0, 0, 900}, {}} {
			sum := 0
			for _, n := range budgetShares(sizes, 100) {
				sum += n
			}
			if sum > 100 {
				t.Errorf("sizes %v were given %d of a 100 budget", sizes, sum)
			}
		}
	})
}

// The lorebook is the one input that grows without bound — realize alone lands
// ~25 entries — so it is budgeted, and a partial view must SAY it is partial.
func TestRenderWorldDoctorLoreBudget(t *testing.T) {
	lore := []core.WorldLoreEntry{
		{Name: "Short", Content: "brief"},
		{Name: "Long", Content: strings.Repeat("x", 500)},
	}
	got := renderWorldDoctorLore(lore, 100)
	if !strings.Contains(got, "Short") {
		t.Error("the short entry fits and must be kept")
	}
	if strings.Contains(got, strings.Repeat("x", 500)) {
		t.Error("the over-budget entry must be dropped")
	}
	if !strings.Contains(got, "1 further entries were too long") {
		t.Errorf("a partial lorebook must say it is partial, else the doctor concludes the World records less than it does:\n%s", got)
	}
	if full := renderWorldDoctorLore(lore, 10000); strings.Contains(full, "further entries") {
		t.Error("a lorebook that fits must not claim anything was dropped")
	}
}

func TestParseWorldDoctorResult(t *testing.T) {
	roster := []worldDoctorCard{
		{Name: "Kobeni", Ref: "kobeni-1", Card: card.Card{Name: "Kobeni", Description: "a nervous devil hunter", Personality: "anxious"}},
		{Name: "Aki", Ref: "aki-2", Card: card.Card{Name: "Aki", Description: "her senior"}},
	}
	lore := []core.WorldLoreEntry{{Name: "The Bureau", Content: "Public Safety runs the hunts."}}

	reply := `{"note":"the cast has no authority figure",
	 "card_proposals":[
	   {"id":"c1","card":"kobeni-1","field":"personality","severity":"suggestion","rationale":"nothing to play against Aki","after":"anxious, but sharper when cornered"},
	   {"id":"c2","card":"kobeni-1","field":"description","rationale":"echoing the model","after":"a nervous devil hunter"},
	   {"id":"c3","card":"nobody-9","field":"personality","rationale":"unknown card","after":"x"},
	   {"id":"c4","card":"aki-2","field":"system_prompt","rationale":"outside the surface","after":"x"},
	   {"id":"c5","card":"aki-2","field":"personality","rationale":"nothing offered","after":"   "},
	   {"id":"c6","card":"aki-2","field":"scenario","rationale":"clearing an empty field","remove":true}],
	 "world_proposals":[
	   {"id":"w1","kind":"character_new","rationale":"the cast needs an authority","character":"Makima","description":"the one who gives the orders"},
	   {"id":"w2","kind":"character_new","rationale":"already here","character":"Aki","description":"x"},
	   {"id":"w3","kind":"lore_entry","rationale":"delta","name":"The Contract","content":"Hunters sign in blood."},
	   {"id":"w4","kind":"lore_entry","rationale":"already recorded","name":"The Bureau","content":"dup"},
	   {"id":"w5","kind":"lore_retire","rationale":"outgrown","name":"The Bureau"},
	   {"id":"w6","kind":"lore_retire","rationale":"never recorded","name":"Invented"},
	   {"id":"w7","kind":"scene_break","rationale":"not this doctor's kind","name":"Chapter 2"}]}`

	res, err := parseWorldDoctorResult(reply, roster, lore)
	if err != nil {
		t.Fatal(err)
	}

	// --- card proposals ---
	if len(res.CardProposals) != 1 {
		t.Fatalf("card proposals = %d, want 1 (c1 only): %+v", len(res.CardProposals), res.CardProposals)
	}
	c := res.CardProposals[0]
	if c.ID != "c1" || c.Field != "personality" {
		t.Errorf("kept the wrong card proposal: %+v", c)
	}
	// Before comes from the card, never the model's echo. With five cards in a
	// prompt a misattributed "before" would render a diff that is a fiction, and
	// the author would accept it believing they had read it.
	if c.Before != "anxious" {
		t.Errorf("before = %q, want the card's real value", c.Before)
	}
	// Addressed for a review sheet that groups by character without resolving refs.
	if c.Card != "kobeni-1" || c.Character != "Kobeni" {
		t.Errorf("proposal not addressed to its character: card=%q character=%q", c.Card, c.Character)
	}

	// --- world proposals ---
	kinds := map[string]string{}
	for _, p := range res.WorldProposals {
		kinds[p.ID] = p.Kind
	}
	if len(res.WorldProposals) != 3 {
		t.Fatalf("world proposals = %d, want 3 (w1, w3, w5): %+v", len(res.WorldProposals), kinds)
	}
	if kinds["w1"] != ctrlproto.SessionProposalCharacterNew || kinds["w3"] != ctrlproto.SessionProposalLore || kinds["w5"] != ctrlproto.SessionProposalRetire {
		t.Errorf("wrong survivors: %+v", kinds)
	}
	// A retirement answers with the RECORDED entry, not the doctor's paraphrase:
	// the author is being asked to delete something and must see what is on file.
	for _, p := range res.WorldProposals {
		if p.ID == "w5" && p.Content != "Public Safety runs the hunts." {
			t.Errorf("retire proposal did not carry the recorded content: %q", p.Content)
		}
	}
	if res.Note != "the cast has no authority figure" {
		t.Errorf("note = %q", res.Note)
	}
}

// The reserved pins have their own lifecycles — the scene-state card belongs to
// sessions.doctor and the recap to a scene break. Neither is a World's to write.
func TestParseWorldDoctorResultRefusesTheReservedPins(t *testing.T) {
	reply := `{"world_proposals":[
	  {"id":"w1","kind":"lore_entry","rationale":"x","name":"` + core.SceneStateName + `","content":"Day 2"},
	  {"id":"w2","kind":"lore_entry","rationale":"x","name":"` + core.StorySoFarName + `","content":"Previously"}]}`
	res, err := parseWorldDoctorResult(reply, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.WorldProposals) != 0 {
		t.Errorf("the reserved pins must be refused, got %+v", res.WorldProposals)
	}
}

func TestParseWorldDoctorResultGarbage(t *testing.T) {
	if _, err := parseWorldDoctorResult("I'm afraid I can't do that.", nil, nil); err == nil {
		t.Error("prose with no JSON object should be an error")
	}
	if _, err := parseWorldDoctorResult(`{"card_proposals": "not a list"}`, nil, nil); err == nil {
		t.Error("malformed JSON should be an error")
	}
}

// The whole vertical: gate, evidence, one booked call, parse.
func TestWorldsDoctorRunsAndBooks(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	kobeni := storeCard(t, "Kobeni", "a nervous devil hunter", "anxious")
	w, doc := worldDoctorFixture(t, map[string]string{"Kobeni": kobeni})

	reply := `{"note":"ok",
	 "card_proposals":[{"id":"c1","card":"` + kobeni + `","field":"personality","rationale":"nothing to play against","after":"anxious, sharper when cornered"}],
	 "world_proposals":[{"id":"w1","kind":"character_new","rationale":"no authority figure","character":"Makima","description":"the one who gives the orders"}]}`
	cl := &scriptedClient{replies: []string{reply}}

	res, err := w.worldsDoctor(context.Background(), cl, "gpt-5", doc, ctrlproto.WorldDoctorParams{ID: doc.ID, Steer: "give her a rival"})
	if err != nil {
		t.Fatalf("worldsDoctor: %v", err)
	}
	if len(res.CardProposals) != 1 || len(res.WorldProposals) != 1 {
		t.Fatalf("proposals = %d card / %d world", len(res.CardProposals), len(res.WorldProposals))
	}
	if res.CardProposals[0].Before != "anxious" {
		t.Errorf("before not sourced from the stored card: %q", res.CardProposals[0].Before)
	}

	// The priciest side-channel call in the daemon must not spend unrecorded —
	// the #270 lesson. There is no session file to hold the row now, so the
	// workspace ledger is where it has to land; without it a sessionless run
	// would be free as far as any later accounting could tell.
	rows, err := build.NewUsageLedger().Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("booked %d ledger rows, want exactly 1", len(rows))
	}
	if rows[0].Usage != scriptedCallUsage {
		t.Errorf("booked %+v, want %+v", rows[0].Usage, scriptedCallUsage)
	}
	// The row has to say what it was for, or a ledger of anonymous costs cannot
	// answer the only question it exists to answer.
	if rows[0].Verb != string(ctrlproto.MethodWorldsDoctor) || rows[0].Subject != doc.ID {
		t.Errorf("row is not attributed: %+v", rows[0])
	}

	reqs := cl.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one model call, got %d", len(reqs))
	}
	if !strings.Contains(reqs[0].System, "WORLD doctor") {
		t.Errorf("system prompt is not the world doctor's:\n%.200s", reqs[0].System)
	}
	body := textOf(reqs[0].Messages[0])
	if !strings.Contains(body, "[card "+kobeni+"]") {
		t.Errorf("evidence missing the roster card:\n%s", body)
	}
	if !strings.Contains(body, "give her a rival") {
		t.Error("evidence missing the steer")
	}
}

// Every refusal is PRE-SPEND: a run that could not produce an applicable
// proposal must cost nothing. The gates sit ABOVE credential resolution in
// WorldsDoctor, so a refused run does not even build a client.
func TestWorldsDoctorRefusalsSpendNothing(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	storeCard(t, "Kobeni", "a nervous devil hunter", "anxious")

	t.Run("a World that is not in the library", func(t *testing.T) {
		w, _ := worldDoctorFixture(t, nil)
		if _, err := w.WorldsDoctor(context.Background(), ctrlproto.WorldDoctorParams{ID: "no-such-world"}); err == nil {
			t.Fatal("an unknown World must be refused")
		}
		assertNothingBooked(t)
	})

	t.Run("a World whose roster resolves to nothing", func(t *testing.T) {
		// A dangling ref: the card was deleted out from under the roster.
		w, doc := worldDoctorFixture(t, map[string]string{"Ghost": "gone-000000000000"})
		if _, err := w.WorldsDoctor(context.Background(), ctrlproto.WorldDoctorParams{ID: doc.ID}); err == nil {
			t.Fatal("a World with no readable characters must be refused")
		}
		assertNothingBooked(t)
	})

	t.Run("an empty World", func(t *testing.T) {
		w, doc := worldDoctorFixture(t, nil)
		if _, err := w.WorldsDoctor(context.Background(), ctrlproto.WorldDoctorParams{ID: doc.ID}); err == nil {
			t.Fatal("a World with no characters must be refused")
		}
		assertNothingBooked(t)
	})
}

// assertNothingBooked is how a pre-spend refusal is PROVEN now. The old test
// counted a scripted client's requests, which a sessionless run has no way to
// reach; the ledger is the equivalent evidence and a stronger one — it fails if
// the call happened at all, not merely if one particular client saw it.
func assertNothingBooked(t *testing.T) {
	t.Helper()
	rows, err := build.NewUsageLedger().Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a refusal booked %d usage row(s): %+v", len(rows), rows)
	}
}

// A World-of-one is the case the feature was asked for: one character, and a
// request for the cast around them. The roster is read from the SAVED World, so
// a World with a single saved character is a run the doctor accepts.
func TestWorldRosterCardsReadsTheSavedRoster(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	kobeni := storeCard(t, "Kobeni", "a nervous devil hunter", "anxious")
	w, doc := worldDoctorFixture(t, map[string]string{"Kobeni": kobeni})

	roster := w.worldRosterCards(doc)
	if len(roster) != 1 || roster[0].Name != "Kobeni" {
		t.Fatalf("a World of one must read as a roster of one, got %+v", roster)
	}
	if roster[0].Findings == nil {
		t.Error("each card must carry its deterministic lint")
	}

	// Two names pointing at ONE card is one character, not two: the same actor
	// cast twice would otherwise read as an ensemble that has someone to play
	// against, which is exactly the judgement this doctor exists to make.
	doc.Characters["Understudy"] = kobeni
	if roster := w.worldRosterCards(doc); len(roster) != 1 {
		t.Errorf("one card under two names must read once, got %d", len(roster))
	}
}

// The scenes are CHOSEN, and only this World's scenes are readable — a stray id
// is ignored rather than refused, so one stale checkbox cannot cost the author
// the other five scenes they picked.
func TestWorldDoctorScenesReadsOnlyItsOwnMembers(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	kobeni := storeCard(t, "Kobeni", "a nervous devil hunter", "anxious")
	w, doc := worldDoctorFixture(t, map[string]string{"Kobeni": kobeni})

	other, err := build.NewWorldStore().Save(build.WorldDoc{Name: "Lowtown", Characters: map[string]string{"Kobeni": kobeni}})
	if err != nil {
		t.Fatal(err)
	}
	mine := seedWorldScene(t, w, doc.ID, "The first night", "the bells rang at dusk")
	theirs := seedWorldScene(t, w, other.ID, "Elsewhere", "nothing to do with it")

	got := w.worldDoctorScenes(context.Background(), doc.ID, []string{mine, theirs, "no-such-session"})
	if len(got) != 1 {
		t.Fatalf("read %d scenes, want only this World's one: %+v", len(got), got)
	}
	if got[0].Title != "The first night" {
		t.Errorf("scene title is %q", got[0].Title)
	}
}

// The scene pool is shared and each scene is NAMED: "this came up in two
// different scenes" is a materially stronger warrant than "this came up", and
// the doctor cannot tell them apart in one undifferentiated wall.
func TestRenderWorldDoctorEvidenceNamesEachScene(t *testing.T) {
	roster := []worldDoctorCard{{Name: "Kobeni", Ref: "kobeni-1", Card: card.Card{Name: "Kobeni", Description: "a nervous devil hunter"}}}
	scenes := []worldDoctorScene{
		{Title: "The first night", Player: "You", Msgs: []provider.Message{sceneLine("the bells rang at dusk")}},
		{Title: "The harbour job", Player: "You", Msgs: []provider.Message{sceneLine("tar and rope and no names")}},
	}
	got := renderWorldDoctorEvidence("Tokyo Division", "", roster, nil, scenes, "")

	for _, want := range []string{"SCENE: The first night", "SCENE: The harbour job", "the bells rang at dusk", "tar and rope and no names"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "has not been played yet") {
		t.Error("a World with scenes must not claim it has never been played")
	}
}

// sceneLine is one played line. Named rather than reusing this package's
// userMessage, which builds a message from a live session.
func sceneLine(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: text}},
	}
}

// seedWorldScene creates a real session stamped into a World, through the
// workspace's own create path — so the doctor reads it back exactly as it will
// in production, from a file, with nothing bound.
func seedWorldScene(t *testing.T, w *Workspace, worldID, title, line string) string {
	t.Helper()
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat", World: worldID})
	if err != nil {
		t.Fatal(err)
	}
	s, err := w.resolve(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A scene with no messages is skipped as unread, so the fixture needs one.
	if err := s.sess.AppendMessage(sceneLine(line)); err != nil {
		t.Fatal(err)
	}
	// Renamed AFTER the first message: an untitled session takes its title from
	// what was said in it, which would otherwise overwrite this one.
	if err := w.RenameSession(context.Background(), info.ID, title); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// The doctor stands INSIDE a World, so the World's own default model is the
// rung it should fall to — a run that consulted the global default while the
// author had aimed this World at a particular model would be quietly ignoring
// the aim.
//
// Proven through the credential gate rather than a live call: the World is
// pointed at a catalog model this workspace holds no key for, so the refusal
// NAMES the model that was resolved. A doctor that skipped the world rung would
// have resolved openai/gpt-5 — which does have a key here — and gone on to
// spend. Both halves therefore also assert nothing was booked.
func TestWorldsDoctorResolvesTheWorldsModel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("ANTHROPIC_API_KEY", "")
	kobeni := storeCard(t, "Kobeni", "a nervous devil hunter", "anxious")
	w, doc := worldDoctorFixture(t, map[string]string{"Kobeni": kobeni})

	if _, err := w.WorldSetModel(context.Background(), ctrlproto.WorldSetModelParams{Provider: "anthropic", Model: "claude-opus-4-8", ID: doc.ID}); err != nil {
		t.Fatal(err)
	}
	_, err := w.WorldsDoctor(context.Background(), ctrlproto.WorldDoctorParams{ID: doc.ID})
	if err == nil || !strings.Contains(err.Error(), "claude-opus-4-8") {
		t.Fatalf("the doctor should have resolved the World's model, got err %v", err)
	}
	assertNothingBooked(t)

	// ...and an explicit pick still outranks the World, which is the other half
	// of the order: a refusal naming the World's model here would mean the
	// author's in-the-moment choice had been overridden by a stored setting.
	_, err = w.WorldsDoctor(context.Background(), ctrlproto.WorldDoctorParams{ID: doc.ID, Provider: "anthropic", Model: "claude-sonnet-4-5"})
	if err == nil || !strings.Contains(err.Error(), "claude-sonnet-4-5") {
		t.Fatalf("an explicit model must outrank the World's, got err %v", err)
	}
	assertNothingBooked(t)
}

// The scene pool has to BIND. This is the test the first live calibration run
// should not have had to be: worldDoctorMaxScene was raised 8x and the rendered
// prompt did not change by one byte, because the sizing pass asked for a budget
// of 0 and renderSceneTail read that as zero bytes rather than "no limit" — so
// every scene measured, and rendered, as its last message alone.
//
// Asserting on the evidence rather than on a helper: the sizing pass, the
// water-filling, and the render are three steps that agreed with each other
// while all three were wrong together.
func TestWorldDoctorSceneEvidenceGrowsWithTheBudget(t *testing.T) {
	// A scene far longer than any plausible pool, so the budget is what decides
	// how much of it lands rather than the transcript running out.
	// Sized off the constant, not off a guess: the scene must exceed the pool or
	// nothing is trimmed and the test measures nothing. (It measured nothing on
	// the first attempt at 400 lines — 22KB against a 48KB pool.)
	filler := strings.Repeat("the harbour bell rang and nobody moved. ", 6)
	lines := worldDoctorMaxScene / len(filler) * 4
	longScene := func(tag string) []provider.Message {
		out := make([]provider.Message, 0, lines)
		for i := range lines {
			out = append(out, sceneLine(fmt.Sprintf("%s %04d: %s", tag, i, filler)))
		}
		return out
	}
	lastLine := fmt.Sprintf("night %04d", lines-1)
	// TWO scenes, because the pool is a pool: with one, its share IS the whole
	// budget and nothing is ever trimmed. The short one is what the water-filling
	// is for — it must keep everything it needs while the long one takes the rest.
	// TWO long scenes, because the pool is a pool: the sizing pass already clamps
	// each scene at the pool, so a single long one is never trimmed — its share
	// IS the whole budget. Only competition makes the water-filling visible. The
	// short third is what water-filling is FOR: it must keep everything it needs
	// while the two long ones split the rest.
	short := []provider.Message{sceneLine("a quiet evening"), sceneLine("nothing happened")}
	scenes := []worldDoctorScene{
		{Title: "The long night", Player: "Drew", Msgs: longScene("night")},
		{Title: "The other long night", Player: "Drew", Msgs: longScene("other")},
		{Title: "A quiet one", Player: "Drew", Msgs: short},
	}

	render := func() string {
		return renderWorldDoctorEvidence("Bellhaven", "", nil, nil, scenes, "")
	}
	small := render()

	// The constant is what the run is budgeted by, so the test moves the
	// constant — a helper taking a budget parameter would have passed happily
	// while the caller kept handing it 0.
	restore := worldDoctorMaxScene
	t.Cleanup(func() { worldDoctorMaxScene = restore })
	worldDoctorMaxScene = restore * 4
	big := render()

	if len(big) <= len(small) {
		t.Fatalf("quadrupling the pool changed the evidence from %d to %d bytes — the budget is inert", len(small), len(big))
	}
	// And the small render must be a real tail, not the degenerate last line.
	if strings.Count(small, "night ") < 2 {
		t.Errorf("the budgeted scene rendered %d lines — a pool that only ever yields the last message is the bug this test exists for",
			strings.Count(small, "night "))
	}
	// The tail is the RECENT end: the last message must be present at any budget.
	if !strings.Contains(small, lastLine) || !strings.Contains(big, lastLine) {
		t.Error("the scene tail must keep the most recent messages")
	}
	// A trimmed scene says so, so the model does not read a mid-scene cut as the
	// end of the story.
	if !strings.Contains(small, "most recent stretch") {
		t.Error("a scene trimmed to its share must be marked as trimmed")
	}
	// The short scene keeps everything — water-filling, not an equal split.
	if !strings.Contains(small, "a quiet evening") {
		t.Error("the short scene lost content to the long one's share")
	}
}
