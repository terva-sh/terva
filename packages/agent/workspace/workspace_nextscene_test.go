package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The scene-break evidence: the scene, who carries over, the lore that crosses
// intact — and the pin and the previous recap in their own blocks, because one
// is the state the next scene opens in and the other is what this draft
// replaces.
func TestRenderNextSceneEvidence(t *testing.T) {
	transcript := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "I bank the fire."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Kobeni bolts the door."}}},
	}
	lore := []core.WorldLoreEntry{
		{Name: "The bell", Content: "Rings at dusk."},
		{Name: core.SceneStateName, Constant: true, Content: "Day 14, midnight. The shop."},
		{Name: core.StorySoFarName, Constant: true, Content: "They met on the north road."},
	}
	out := renderNextSceneEvidence("Kira", "Kobeni", []string{"Elira"}, lore, transcript)
	for _, want := range []string{
		"Kira: I bank the fire.",
		"- Kira (the player)",
		"- Kobeni (the main character)",
		"- Elira (on the roster)",
		"- The bell [shared]: Rings at dusk.",
		"THE PINNED SCENE-STATE CARD",
		"Day 14, midnight. The shop.",
		"THE STORY SO FAR",
		"They met on the north road.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("evidence missing %q:\n%s", want, out)
		}
	}
	// The pin and the recap must not ALSO appear in the carried-lore list: one
	// would be summarized, the other double-counted.
	if strings.Contains(out, "- "+core.SceneStateName) || strings.Contains(out, "- "+core.StorySoFarName) {
		t.Errorf("the pin/recap must have their own blocks, not the lore list:\n%s", out)
	}
	empty := renderNextSceneEvidence("Me", "", nil, nil, nil)
	for _, want := range []string{"(the scene has not started yet)", "(nothing recorded yet)", "(nothing pinned yet)", "(this is the first scene)"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty evidence missing %q:\n%s", want, empty)
		}
	}
}

// All three fields are required in a draft: the author cannot accept what they
// were not shown, and a commit needs every one of them.
func TestParseNextSceneDraft(t *testing.T) {
	ok := "chatter\n```json\n" + `{"note":"a clean ending","title":"The North Road","summary":"They owe Marrow three silver.","opening":"*Dawn finds the shop cold.*"}` + "\n```"
	res, err := parseNextSceneDraft(ok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Title != "The North Road" || res.Summary != "They owe Marrow three silver." || res.Note != "a clean ending" {
		t.Fatalf("draft = %+v", res)
	}
	if res.Opening != "*Dawn finds the shop cold.*" {
		t.Errorf("opening = %q", res.Opening)
	}
	for _, bad := range []string{
		`{"title":"x","summary":"y"}`,               // no opening
		`{"title":"x","opening":"y"}`,               // no summary
		`{"summary":"x","opening":"y"}`,             // no title
		`{"title":" ","summary":"x","opening":"y"}`, // blank counts as missing
		"no json at all",
	} {
		if _, err := parseNextSceneDraft(bad); err == nil {
			t.Errorf("expected a refusal for %q", bad)
		}
	}
}

// Propose spends exactly one booked call and creates nothing; an unplayed
// scene and a coding session are refused before any spend.
func TestSessionsNextSceneProposesAndBooks(t *testing.T) {
	reply := `{"title":"The North Road","summary":"They owe Marrow three silver.","opening":"*Dawn finds the shop cold.*"}`
	cl := &scriptedClient{replies: []string{reply}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	var booked []provider.Usage
	s.agent.AddUsageObserver(func(u, _ provider.Usage) { booked = append(booked, u) })

	// An unplayed scene has nothing to recap — refused before the model runs.
	if _, err := proposeNextScene(context.Background(), s, ctrlproto.NextSceneParams{}); err == nil {
		t.Error("an empty transcript should be refused")
	}
	if len(cl.requests()) != 0 {
		t.Fatalf("the refusal must spend nothing, got %d calls", len(cl.requests()))
	}

	s.agent.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "I bank the fire."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Kobeni bolts the door."}}},
	})
	res, err := proposeNextScene(context.Background(), s, ctrlproto.NextSceneParams{})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if res.Title != "The North Road" || res.Session != nil {
		t.Fatalf("a propose drafts and creates nothing: %+v", res)
	}
	if len(booked) != 1 || booked[0] != scriptedCallUsage {
		t.Fatalf("the draft booked %v, want exactly one %+v — the scene break is never free", booked, scriptedCallUsage)
	}
	reqs := cl.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one model call, got %d", len(reqs))
	}
	if !strings.Contains(reqs[0].System, "scene-break writer") {
		t.Errorf("system prompt is not the scene-break task:\n%.200s", reqs[0].System)
	}
	if !strings.Contains(textOf(reqs[0].Messages[0]), "- Elira (on the roster)") {
		t.Errorf("evidence missing the roster")
	}

}

// The commit: a fresh session carrying this one's live World state, with the
// recap REPLACED (not stacked) and the cold open standing in for the greeting.
func TestSessionsNextSceneCommitCarriesWorld(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","first_mes":"*She looks up.* \"Oh — hello.\""}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{
		Experience: "chat",
		Card:       imported.ID,
		Cast:       map[string]string{"Elira": "elira-ref"},
		Title:      "Scene one",
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := w.live(info.ID)
	if err := parent.sess.SetWorldLore([]core.WorldLoreEntry{
		{Name: "The bell", Keys: []string{"bell"}, Content: "Rings at dusk."},
		{Name: core.SceneStateName, Constant: true, Content: "Day 14, midnight."},
		{Name: core.StorySoFarName, Constant: true, Content: "Scene zero: they met."},
	}); err != nil {
		t.Fatal(err)
	}
	if err := parent.sess.SetCoordination("off"); err != nil {
		t.Fatal(err)
	}
	if err := parent.sess.SetUserPersona("Kira", "A weary courier.", "woman", "she/her"); err != nil {
		t.Fatal(err)
	}
	if err := parent.sess.SetNote("Keep it cold."); err != nil {
		t.Fatal(err)
	}

	// A commit with a missing field is refused — the fields ARE the review.
	if _, err := w.SessionsNextScene(ctx, info.ID, ctrlproto.NextSceneParams{Commit: true, Title: "x", Summary: "y"}); err == nil {
		t.Error("a commit without a cold open should be refused")
	}
	// A coding session has no scene to break (the verb's gate, exercised
	// through the real resolve path rather than a bare struct).
	coding, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.SessionsNextScene(ctx, coding.ID, ctrlproto.NextSceneParams{}); err == nil {
		t.Error("a coding session should be refused")
	}

	// The parent opened on its card greeting; a scene break must not disturb it.
	parentBefore := len(parent.agent.Messages())

	res, err := w.SessionsNextScene(ctx, info.ID, ctrlproto.NextSceneParams{
		Commit:  true,
		Title:   "The North Road",
		Summary: "They owe Marrow three silver; the search leaves at first light.",
		Opening: "*Dawn finds the shop cold.*",
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.Session == nil || res.Session.ID == info.ID {
		t.Fatalf("commit should create a NEW session, got %+v", res.Session)
	}
	next := w.live(res.Session.ID)
	meta := next.sess.Meta

	// The World state carried.
	if meta.Card != imported.ID || meta.Experience != "chat" || meta.Coordination != "off" {
		t.Errorf("card/experience/coordination did not carry: %+v", meta)
	}
	if meta.Cast["Elira"] != "elira-ref" {
		t.Errorf("roster did not carry: %+v", meta.Cast)
	}
	if meta.UserName != "Kira" || meta.UserPronouns != "she/her" || meta.UserDescription != "A weary courier." {
		t.Errorf("the player's persona did not carry: %+v", meta)
	}
	if meta.Note != "Keep it cold." {
		t.Errorf("author's note did not carry: %q", meta.Note)
	}
	if meta.Title != "The North Road" {
		t.Errorf("title = %q", meta.Title)
	}

	// The lore carried; the recap REPLACED the old one rather than stacking.
	var pin, recap string
	recaps := 0
	for _, e := range meta.WorldLore {
		switch {
		case core.IsSceneState(e.Name):
			pin = e.Content
		case core.IsStorySoFar(e.Name):
			recap = e.Content
			recaps++
			if !e.Constant {
				t.Error("the recap must be always-on — one that fires on keywords is read by luck")
			}
		}
	}
	if pin != "Day 14, midnight." {
		t.Errorf("the pinned card did not carry: %q", pin)
	}
	if recaps != 1 || !strings.Contains(recap, "three silver") {
		t.Errorf("want exactly one replaced recap, got %d: %q", recaps, recap)
	}
	if len(meta.WorldLore) != 3 {
		t.Errorf("ordinary lore should carry alongside: %+v", meta.WorldLore)
	}

	// The cold open is the transcript's first message, attributed to the main
	// character — and it stands IN PLACE of the card greeting.
	msgs := next.agent.Messages()
	if len(msgs) != 1 {
		t.Fatalf("the new scene opens on exactly the cold open, got %d messages: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != provider.RoleAssistant || messageProse(msgs[0]) != "*Dawn finds the shop cold.*" {
		t.Fatalf("cold open = %+v", msgs[0])
	}
	if msgs[0].Meta[core.MetaActor] != "Kobeni" || msgs[0].Meta[core.MetaSource] != core.MetaDirected {
		t.Errorf("cold open should be attributed to the main character: %+v", msgs[0].Meta)
	}
	if strings.Contains(messageProse(msgs[0]), "Oh — hello") {
		t.Error("the card greeting must not also seed the scene")
	}
	// The parent is untouched — this scene stays as it was played.
	if len(parent.agent.Messages()) != parentBefore || parent.sess.Meta.Title != "Scene one" {
		t.Errorf("the parent scene must be left alone: %d messages (was %d), title %q",
			len(parent.agent.Messages()), parentBefore, parent.sess.Meta.Title)
	}
}

// A scene boundary is the moment a chat stops being one session and becomes a
// story told across several. Until this landed, nothing marked that: the
// successor copied the parent's `world`, which is empty for every story nobody
// had explicitly promoted with worlds.save — so both halves came out grouped by
// nothing, and the sheet's own "opens in the same world" copy was a promise the
// data model did not keep.
func TestSessionsNextScenePromotesAWorldForAnUngroupedStory(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Title: "Scene one"})
	if err != nil {
		t.Fatal(err)
	}
	parent := w.live(info.ID)
	if err := parent.sess.SetWorldLore([]core.WorldLoreEntry{
		{Name: "The bell", Keys: []string{"bell"}, Content: "Rings at dusk."},
	}); err != nil {
		t.Fatal(err)
	}
	if parent.sess.Meta.World != "" {
		t.Fatal("fixture is not the ungrouped case this test is about")
	}

	res, err := w.SessionsNextScene(ctx, info.ID, ctrlproto.NextSceneParams{
		Commit:  true,
		Title:   "The North Road",
		Summary: "They owe Marrow three silver.",
		Opening: "*Dawn finds the shop cold.*",
		World:   "The Marrow Debt",
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	next := w.live(res.Session.ID)
	if next.sess.Meta.World == "" {
		t.Fatal("the new scene joined no World — the promotion did not reach it")
	}
	// The half that would otherwise be left behind: the scene being ENDED must
	// join too, or the story is grouped from its second chapter onward.
	if parent.sess.Meta.World != next.sess.Meta.World {
		t.Errorf("the ending scene was left out of the World: parent %q, next %q",
			parent.sess.Meta.World, next.sess.Meta.World)
	}
	// Named, saved, and carrying what the story had accumulated.
	doc, err := build.NewWorldStore().Get(next.sess.Meta.World)
	if err != nil {
		t.Fatalf("the World was stamped but not saved: %v", err)
	}
	if doc.Name != "The Marrow Debt" {
		t.Errorf("World name = %q", doc.Name)
	}
	if len(doc.Lore) == 0 {
		t.Error("the promoted World carried none of the story's lore")
	}
	// And the result says where it landed, rather than leaving the client to
	// assume its own request succeeded.
	if res.WorldID != next.sess.Meta.World || res.WorldName != "The Marrow Debt" {
		t.Errorf("result did not echo the grouping: %+v", res)
	}
}

// Declining the offer still produces a scene — and still records where it came
// from. Lineage is not conditional on grouping: it is the only durable record
// that scene two followed scene one, and a World does not order its scenes.
func TestSessionsNextSceneRecordsLineageEvenUngrouped(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Title: "Scene one"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.SessionsNextScene(ctx, info.ID, ctrlproto.NextSceneParams{
		Commit:  true,
		Title:   "The North Road",
		Summary: "They owe Marrow three silver.",
		Opening: "*Dawn finds the shop cold.*",
		// No World: the author unticked the offer.
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	next := w.live(res.Session.ID)
	if next.sess.Meta.World != "" {
		t.Errorf("a declined offer still created a World: %q", next.sess.Meta.World)
	}
	if next.sess.Meta.Parent != info.ID {
		t.Errorf("lineage not recorded: parent = %q, want %q", next.sess.Meta.Parent, info.ID)
	}
	// A successor is not a branch: it shares no transcript prefix, so the fork
	// point must stay empty or resume/branch UI would read it as one.
	if next.sess.Meta.ForkPoint != 0 {
		t.Errorf("a next scene must not look like a fork: ForkPoint = %d", next.sess.Meta.ForkPoint)
	}
}

// A story already in a World joins it; the offer is not an opportunity to make
// a second one beside it.
func TestSessionsNextSceneJoinsAnExistingWorld(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Title: "Scene one"})
	if err != nil {
		t.Fatal(err)
	}
	parent := w.live(info.ID)
	saved, err := parent.saveWorld("The Marrow Debt", "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := w.SessionsNextScene(ctx, info.ID, ctrlproto.NextSceneParams{
		Commit:  true,
		Title:   "The North Road",
		Summary: "They owe Marrow three silver.",
		Opening: "*Dawn finds the shop cold.*",
		World:   "A Different Name Entirely",
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	next := w.live(res.Session.ID)
	if next.sess.Meta.World != saved.ID {
		t.Errorf("the scene did not join the story's World: got %q, want %q", next.sess.Meta.World, saved.ID)
	}
	if res.WorldName != "The Marrow Debt" {
		t.Errorf("result should report the World it JOINED, not the ignored name: %q", res.WorldName)
	}
}
