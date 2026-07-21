package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// A converged Kestrelbridge realize, as Kartoittaja would return it: a World, a
// protagonist held out of the roster, two NPCs (one carrying the cold open), and
// two lore entries (one keyed, one bare).
const realizeReply = `{"note":"","world":{"name":"Kestrelbridge","description":"A charter town on the only stone crossing."},"protagonist":{"name":"Nao","description":"a shut-in from another world","personality":"avoids work","notes":"cannot read the local script"},"roster":[{"name":"Edrin","role":"apothecary","description":"quick with a lie","first_mes":"Name. Quickly."},{"name":"Rook","role":"inspector","description":"severe and observant"}],"lore":[{"name":"The Law","keys":["law","apprentice"],"content":"an apprentice belongs to a master's roof and seal"},{"name":"The Bell","content":"black metal rings a breach bell on any crossing"}],"cold_open":"Boots struck the cobbles.","cold_open_actor":"Edrin","coordination":""}`

// propose runs on the session's OWN model (like the doctors) and returns the
// parsed structure. Reverting the resolution to the workspace default makes the
// model assertion fail.
func TestProposeRealizeRunsOnSessionModel(t *testing.T) {
	cl := &scriptedClient{replies: []string{realizeReply}}
	s := worldTestSession(t, cl, map[string]string{})
	s.provider, s.model = "test", "session-model"

	res, err := proposeRealize(context.Background(), s, ctrlproto.RealizeParams{})
	if err != nil {
		t.Fatalf("proposeRealize: %v", err)
	}
	if res.Proposal == nil {
		t.Fatal("nil proposal")
	}
	if res.Proposal.World.Name != "Kestrelbridge" || res.Proposal.Protagonist.Name != "Nao" {
		t.Errorf("proposal = %+v", res.Proposal)
	}
	if len(res.Proposal.Roster) != 2 || res.Proposal.ColdOpenActor != "Edrin" {
		t.Errorf("roster/cold-open actor wrong: %+v", res.Proposal)
	}
	reqs := cl.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one model call, got %d", len(reqs))
	}
	if reqs[0].Model != "session-model" {
		t.Errorf("propose ran on %q, want the session's model — realize is ignoring the session agent", reqs[0].Model)
	}
	if !strings.Contains(reqs[0].System, "realize") {
		t.Errorf("system prompt is not the realize task:\n%.160s", reqs[0].System)
	}
}

// commit imports the roster as cards and seeds a PLAY session: the cast is the
// NPCs, the protagonist is the bound user persona (not a cast member), and the
// cold open stands in for the greeting.
func TestCommitRealizeSeedsPlaySession(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	// A creator chat: card-less, bound to the cartographer.
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Persona: "kartoittaja"})
	if err != nil {
		t.Fatal(err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("creator session is not live")
	}

	var prop ctrlproto.RealizeProposal
	if err := json.Unmarshal([]byte(realizeReply), &prop); err != nil {
		t.Fatal(err)
	}
	res, err := w.commitRealize(s, ctrlproto.RealizeParams{Commit: true, Proposal: &prop})
	if err != nil {
		t.Fatalf("commitRealize: %v", err)
	}
	if res.Session == nil {
		t.Fatal("commit returned no session")
	}

	play := w.live(res.Session.ID)
	if play == nil {
		if play, err = w.resolve(res.Session.ID); err != nil {
			t.Fatalf("resolve realized session: %v", err)
		}
	}
	meta := play.sess.Meta
	if meta.Experience != build.ExperiencePlay {
		t.Errorf("experience = %q, want a play session", meta.Experience)
	}
	if len(meta.Cast) != 2 {
		t.Errorf("cast = %v, want Edrin + Rook", meta.Cast)
	}
	// The protagonist is the bound USER persona (DR-2), never a cast member.
	if meta.UserName != "Nao" {
		t.Errorf("protagonist not bound as the user persona: UserName=%q", meta.UserName)
	}
	if _, isCast := meta.Cast["Nao"]; isCast {
		t.Error("the protagonist must not be in the roster")
	}
	// Each roster NPC became a real card in the library.
	for name, ref := range meta.Cast {
		if _, err := w.cardStore().Get(ref); err != nil {
			t.Errorf("card for %q was not imported: %v", name, err)
		}
	}
	// The cold open seeded the transcript, attributed to Edrin.
	msgs := play.agent.Messages()
	if len(msgs) == 0 || !strings.Contains(reviseTexts(msgs)[0], "Boots struck the cobbles") {
		t.Errorf("cold open not seeded as the opening: %v", reviseTexts(msgs))
	}

	// A commit with no proposal, or a hollow one, is refused before anything is
	// created.
	if _, err := w.commitRealize(s, ctrlproto.RealizeParams{Commit: true}); err == nil {
		t.Error("a commit with no proposal should be refused")
	}
	if _, err := w.commitRealize(s, ctrlproto.RealizeParams{Commit: true, Proposal: &ctrlproto.RealizeProposal{}}); err == nil {
		t.Error("a commit missing world/protagonist/cold-open should be refused")
	}
}

func TestParseRealizeProposal(t *testing.T) {
	p, err := parseRealizeProposal(realizeReply)
	if err != nil || p.World.Name != "Kestrelbridge" {
		t.Fatalf("valid parse: %v, %+v", err, p)
	}
	// Tolerates a code fence, like the doctor parser.
	if p, err := parseRealizeProposal("```json\n" + realizeReply + "\n```"); err != nil || p.Protagonist.Name != "Nao" {
		t.Fatalf("fenced parse: %v", err)
	}
	// An unconverged conversation returns a note + empty fields, not an error.
	un, err := parseRealizeProposal(`{"note":"we have not settled on a direction yet","world":{"name":""}}`)
	if err != nil {
		t.Fatalf("unconverged should parse: %v", err)
	}
	if un.World.Name != "" || un.Note == "" {
		t.Errorf("unconverged proposal = %+v", un)
	}
	if _, err := parseRealizeProposal("not json at all"); err == nil {
		t.Error("malformed output should error")
	}
}

func TestRealizeLoreForcesConstant(t *testing.T) {
	out := realizeLore([]ctrlproto.RealizeLore{
		{Name: "Keyed", Keys: []string{"x"}, Content: "c1"},
		{Name: "Bare", Content: "c2"}, // no keys → forced constant
		{Name: "Marked", AlwaysOn: true, Keys: []string{"y"}, Content: "c3"},
		{Name: "", Content: "dropped"},       // no name → dropped
		{Name: "Empty", Keys: []string{"z"}}, // no content → dropped
	})
	if len(out) != 3 {
		t.Fatalf("kept %d entries, want 3 (Keyed, Bare, Marked)", len(out))
	}
	by := map[string]core.WorldLoreEntry{}
	for _, e := range out {
		by[e.Name] = e
	}
	if by["Keyed"].Constant {
		t.Error("a keyed entry should not be forced constant")
	}
	if !by["Bare"].Constant {
		t.Error("a keyless entry must be forced constant, else it could never fire")
	}
	if !by["Marked"].Constant {
		t.Error("an always_on entry must be constant")
	}
}
