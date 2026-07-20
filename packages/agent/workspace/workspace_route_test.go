package workspace

import (
	"context"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// scriptedClient answers each Stream call with the next scripted reply,
// recording every request — enough to play the router and the voice call in
// one routed turn. Replies ride BOTH a text delta (streamText accumulates
// deltas) and the done message (the agent path reads the final message).
type scriptedClient struct {
	mu      sync.Mutex
	replies []string
	reqs    []provider.Request
}

func (c *scriptedClient) Name() string { return "scripted-fake" }

func (c *scriptedClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	reply := "ok"
	if len(c.replies) > 0 {
		reply, c.replies = c.replies[0], c.replies[1:]
	}
	c.mu.Unlock()
	out := make(chan provider.Event, 4)
	out <- provider.EventTextDelta{Delta: reply}
	// Every real provider reports what the request cost. The fake does too, so a
	// test can tell whether a side-channel call was booked or dropped.
	out <- provider.EventUsage{Usage: scriptedCallUsage}
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: reply}},
	}}
	close(out)
	return out, nil
}

// What one scripted model call "costs", for the accounting assertions.
var scriptedCallUsage = provider.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 0.01}

func (c *scriptedClient) requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.reqs...)
}

// worldTestSession is newTurnTestSession dressed as a chat World: a chat
// experience with a kept-on-stage roster, the meta-narrator's precondition.
func worldTestSession(t *testing.T, cl provider.Client, roster map[string]string) *wsSession {
	t.Helper()
	s := newTurnTestSession(t, cl)
	s.sess.Meta.Experience = "chat"
	s.args.Cast = roster
	return s
}

// A routed turn in auto mode: the router picks a roster character, the voice
// call generates their line, and it lands attributed (MetaRouted + MetaActor)
// after the user's message — with the turn lifecycle (busy → done) intact.
func TestRoutedTurnVoicesRosterPick(t *testing.T) {
	cl := &scriptedClient{replies: []string{"Elira", "*She turns.* \"You came back.\""}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	sub := s.hub.add(nil, true)

	if err := s.prompt("I open the door.", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	reqs := cl.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected router + voice = 2 model calls, got %d", len(reqs))
	}
	if !strings.Contains(reqs[0].System, "meta-narrator") || !strings.Contains(reqs[0].System, "- Elira") {
		t.Errorf("router system prompt missing the job/roster: %q", reqs[0].System[:120])
	}
	if !strings.Contains(reqs[1].System, "You are Elira") {
		t.Errorf("voice system prompt not in Elira's voice: %q", reqs[1].System[:120])
	}

	msgs := s.agent.Messages()
	if len(msgs) != 2 {
		t.Fatalf("transcript should be user + routed line, got %d messages", len(msgs))
	}
	if msgs[0].Role != provider.RoleUser {
		t.Errorf("first message should be the user's, got %s", msgs[0].Role)
	}
	last := msgs[1]
	if last.Meta[core.MetaSource] != core.MetaRouted || last.Meta[core.MetaActor] != "Elira" {
		t.Errorf("routed line not attributed: meta=%v", last.Meta)
	}
	// The attribution crosses the wire typed, like a directed line.
	w := core.MessageToWire(last)
	if !w.Routed || w.Actor != "Elira" || w.Directed {
		t.Errorf("wire form = %+v, want Routed+Actor", w)
	}
	// The session file replays the same attribution (live == replayed).
	if _, replayed, err := core.OpenSession(s.sess.Path); err != nil {
		t.Fatalf("replay: %v", err)
	} else if got := replayed[len(replayed)-1].Meta[core.MetaActor]; got != "Elira" {
		t.Errorf("replayed actor = %q, want Elira", got)
	}
}

// An unparseable router answer degrades to the bound character's ordinary
// turn — the fallback posture: routing never blocks a turn.
func TestRoutedTurnFallsBackToBound(t *testing.T) {
	cl := &scriptedClient{replies: []string{"honestly it could be anyone", "\"Just me here.\""}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	sub := s.hub.add(nil, true)

	if err := s.prompt("hello?", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	msgs := s.agent.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || last.Meta[core.MetaSource] != "" {
		t.Errorf("fallback should be an ordinary (unattributed) turn, got meta=%v", last.Meta)
	}
}

// focus:<name> pins the speaker with NO routing call — one model call total.
func TestFocusSkipsRouter(t *testing.T) {
	cl := &scriptedClient{replies: []string{"\"As you wish.\""}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	s.sess.Meta.Coordination = "focus:Elira"
	sub := s.hub.add(nil, true)

	if err := s.prompt("Elira, the plan?", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	if reqs := cl.requests(); len(reqs) != 1 {
		t.Fatalf("focus should skip the router: got %d calls", len(reqs))
	}
	msgs := s.agent.Messages()
	if got := msgs[len(msgs)-1].Meta[core.MetaActor]; got != "Elira" {
		t.Errorf("focused line actor = %q, want Elira", got)
	}
}

// The dormancy rules: routing only for a chat World with a roster, text-only,
// coordination not off. Everything else is today's turn, no added calls.
func TestShouldRouteGates(t *testing.T) {
	roster := map[string]string{"Elira": "ref"}
	cases := []struct {
		name       string
		experience string
		roster     map[string]string
		coord      string
		text       string
		images     []ctrlproto.Image
		want       bool
	}{
		{"chat with roster routes", "chat", roster, "", "hi", nil, true},
		{"empty roster is dormant (N=1)", "chat", nil, "", "hi", nil, false},
		{"play keeps its director", "play", roster, "", "hi", nil, false},
		{"coding session never routes", "", roster, "", "hi", nil, false},
		{"off pins the bound character", "chat", roster, CoordinationOff, "hi", nil, false},
		{"focus still routes (to the focus)", "chat", roster, "focus:Elira", "hi", nil, true},
		{"images ride the bound charter", "chat", roster, "", "hi", []ctrlproto.Image{{}}, false},
		{"blank text is not a routable turn", "chat", roster, "", "   ", nil, false},
	}
	for _, tc := range cases {
		s := worldTestSession(t, &scriptedClient{}, tc.roster)
		s.sess.Meta.Experience = tc.experience
		s.sess.Meta.Coordination = tc.coord
		if got := s.shouldRoute(tc.text, tc.images); got != tc.want {
			t.Errorf("%s: shouldRoute = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The kobeni dogfood review's gate defect: cast.add once accepted the bound
// card, so a roster holding ONLY the bound character routed a one-character
// scene. The roster read now filters the bound character (by ref or name), so
// such sessions stay dormant — and with a real second character on stage the
// router's roster lists the bound character exactly once (via the fixed
// renderRouteSystem input), not twice.
func TestRoutableRosterExcludesBound(t *testing.T) {
	s := worldTestSession(t, &scriptedClient{}, map[string]string{"Kobeni": "kobeni-ref"})
	s.sess.Meta.Card = "kobeni-ref"
	if s.shouldRoute("hi", nil) {
		t.Fatal("a cast holding only the bound card must stay dormant (N=1)")
	}
	s.args.Cast = map[string]string{"Kobeni": "kobeni-ref", "Elira": "elira-ref"}
	if !s.shouldRoute("hi", nil) {
		t.Fatal("a real second character on stage should route")
	}
	roster := s.routableRoster()
	if _, in := roster["Kobeni"]; in || len(roster) != 1 {
		t.Fatalf("routableRoster = %v, want just Elira", roster)
	}
}

// A routed ▶ Advance (W3b): with N≥2 the meta-narrator decides who the next
// beat belongs to — and unlike a prompt, NOTHING is appended but the picked
// speaker's line. Per the design call: advance is where the router shines once
// there is more than one persistent character to choose from.
func TestAdvanceRoutes(t *testing.T) {
	cl := &scriptedClient{replies: []string{"Elira", "*Elira breaks the silence.*"}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	seed := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "The tavern falls quiet."}}}
	if err := s.sess.AppendMessage(seed); err != nil {
		t.Fatal(err)
	}
	s.agent.SetMessages([]provider.Message{seed})
	sub := s.hub.add(nil, true)

	if err := s.advance(); err != nil {
		t.Fatalf("advance: %v", err)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	reqs := cl.requests()
	if len(reqs) != 2 {
		t.Fatalf("routed advance = router + voice, got %d calls", len(reqs))
	}
	// The router is asked about the scene's own momentum, not a new message.
	routerPrompt := blockTextOf(t, reqs[0].Messages)
	if !strings.Contains(routerPrompt, "THE PLAYER WAITS") || strings.Contains(routerPrompt, "NEW MESSAGE") {
		t.Errorf("advance router prompt should frame a waiting player, got %q", routerPrompt)
	}
	// Nothing appended but the picked speaker's line.
	msgs := s.agent.Messages()
	if len(msgs) != 2 {
		t.Fatalf("advance must append ONLY the line: %d messages", len(msgs))
	}
	if got := msgs[1].Meta[core.MetaActor]; got != "Elira" || msgs[1].Meta[core.MetaSource] != core.MetaRouted {
		t.Errorf("routed advance line not attributed: %v", msgs[1].Meta)
	}
}

// A World of one (or coordination off) leaves advance bit-for-bit today's:
// one agent turn, no router call, no attribution.
func TestAdvanceDormantAtNOne(t *testing.T) {
	cl := &scriptedClient{replies: []string{"the scene continues"}}
	s := worldTestSession(t, cl, nil) // empty roster — N=1
	seed := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "..."}}}
	if err := s.sess.AppendMessage(seed); err != nil {
		t.Fatal(err)
	}
	s.agent.SetMessages([]provider.Message{seed})
	sub := s.hub.add(nil, true)

	if err := s.advance(); err != nil {
		t.Fatalf("advance: %v", err)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	if reqs := cl.requests(); len(reqs) != 1 {
		t.Fatalf("N=1 advance must not route: %d calls", len(reqs))
	}
	msgs := s.agent.Messages()
	if got := msgs[len(msgs)-1].Meta[core.MetaSource]; got != "" {
		t.Errorf("N=1 advance should be an ordinary turn, got meta source %q", got)
	}
}

// The voice call's lore block is audience-scoped (L2): the picked character
// sees world lore + their own secrets, never another's; the narrator sees all.
func TestVoiceLoreScoped(t *testing.T) {
	s := worldTestSession(t, &scriptedClient{}, map[string]string{"Elira": "r1", "Rook": "r2"})
	s.sess.Meta.WorldLore = []core.WorldLoreEntry{
		{Name: "world", Constant: true, Content: "Magic is outlawed."},
		{Name: "hers", Constant: true, Content: "Elira owes a life.", Audience: []string{"Elira"}},
		{Name: "his", Constant: true, Content: "Rook is the informant.", Audience: []string{"Rook"}},
	}
	elira := s.worldLoreBlock("", "Elira")
	if !strings.Contains(elira, "Magic is outlawed.") || !strings.Contains(elira, "owes a life") {
		t.Errorf("Elira should see world + her secret: %q", elira)
	}
	if strings.Contains(elira, "informant") {
		t.Errorf("Elira must NOT see Rook's secret: %q", elira)
	}
	narrator := s.worldLoreBlock("", "")
	if !strings.Contains(narrator, "informant") || !strings.Contains(narrator, "owes a life") {
		t.Errorf("the narrator sees everything: %q", narrator)
	}
	// The suggest surface shares the seam: a drafted speaker's lore rides the
	// drafting prompt under its own heading.
	sys := renderSuggestSystem(suggestTarget{kind: "actor", name: "Elira"}, userPersona{}, nil, elira, nil)
	if !strings.Contains(sys, "WHAT THE SPEAKER KNOWS OF THIS WORLD") || !strings.Contains(sys, "owes a life") {
		t.Errorf("suggest drafts should carry the speaker's lore: %q", sys)
	}
}

// blockTextOf flattens a request's message text for assertions.
func blockTextOf(t *testing.T, msgs []provider.Message) string {
	t.Helper()
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(messageProse(m) + "\n")
	}
	return b.String()
}

func TestParseSpeaker(t *testing.T) {
	roster := map[string]string{"Elira": "r1", "Elira the Red": "r2"}
	cases := []struct {
		out       string
		wantBound bool
		wantName  string
	}{
		{"Elira", false, "Elira"},
		{"  elira.\n(second line ignored)", false, "Elira"},
		{"\"Kertoja\"", true, ""},                                // the bound name → normal turn
		{"Narrator", false, ""},                                  // a narrator beat
		{"the narrator", false, ""},                              //
		{"I think Elira the Red speaks", false, "Elira the Red"}, // longest containment
		{"no idea, sorry", true, ""},                             // garbage → bound
		{"", true, ""},                                           // empty → bound
	}
	for _, tc := range cases {
		got := parseSpeaker(tc.out, "Kertoja", roster)
		if got.bound != tc.wantBound || got.name != tc.wantName {
			t.Errorf("parseSpeaker(%q) = %+v, want bound=%v name=%q", tc.out, got, tc.wantBound, tc.wantName)
		}
	}
}

// renderVoiceSystem grounds the speaker in their card, shares the World's
// lore, and frames the narrator distinctly.
func TestRenderVoiceSystem(t *testing.T) {
	c := &card.Card{Name: "Elira", Description: "A sharp-eyed fence.", Personality: "wry"}
	bound := &card.Card{Name: "Kertoja", Description: "The narrator-in-residence."}
	sys := renderVoiceSystem("Elira", c, userPersona{Name: "Kira", Description: "a wary courier"}, "Kertoja", bound, "<lore>\nMagic is outlawed.\n</lore>", nil, "Kira")
	for _, want := range []string{
		"You are Elira",
		"A sharp-eyed fence.",
		"WHAT EVERYONE ON STAGE KNOWS",
		"Magic is outlawed.",
		"Kira: a wary courier",
		"THE MAIN CHARACTER IN THE SCENE",
		"the scene has not started yet",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("voice system missing %q", want)
		}
	}
	nar := renderVoiceSystem("", nil, userPersona{}, "Kertoja", nil, "", nil, "Me")
	if !strings.Contains(nar, "narrator of an ongoing roleplay scene") {
		t.Errorf("narrator frame missing: %q", nar[:80])
	}
	if strings.Contains(nar, "WHO YOU ARE\n") {
		t.Error("a narrator beat has no card grounding")
	}
}

// world.set validates the mode and persists it; a focus target must exist.
func TestWorldSetCoordination(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	rook, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Rook","first_mes":"Trouble?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.CastAdd(ctx, info.ID, ctrlproto.CastMemberParams{Name: "Rook", Ref: rook.ID}); err != nil {
		t.Fatal(err)
	}

	set := func(mode string) error { return w.WorldSet(ctx, info.ID, ctrlproto.WorldSetParams{Coordination: mode}) }
	for _, ok := range []string{"focus:Rook", "off", ""} {
		if err := set(ok); err != nil {
			t.Errorf("coordination %q should be accepted: %v", ok, err)
		}
	}
	live := w.live(info.ID)
	if got := live.sess.Meta.Coordination; got != "" {
		t.Errorf("last set should win, got %q", got)
	}
	if got := live.info().Coordination; got != "" {
		t.Errorf("SessionInfo should surface coordination, got %q", got)
	}
	for _, bad := range []string{"focus:Ghost", "banana"} {
		if err := set(bad); err == nil {
			t.Errorf("coordination %q should be rejected", bad)
		}
	}
}
