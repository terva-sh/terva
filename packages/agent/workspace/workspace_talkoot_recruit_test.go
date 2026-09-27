package workspace

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// recruitCrew makes the crew talkoot, and a recruiter session for it. calls
// counts the requests that reach the provider.
func recruitCrew(t *testing.T) (*Workspace, ctrlproto.SessionInfo, *atomic.Int64) {
	t.Helper()
	cwd := talkootHome(t)
	var calls atomic.Int64
	w := openTalkootWorkspaceWith(t, cwd, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		okProvider(rw, r)
	})
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	info, err := w.RecruitTalkoot(ctx, ctrlproto.TalkootRecruitParams{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	return w, info, &calls
}

const rookDraft = `---
name: Rook
summary: Reviews Go diffs and reports defects, without editing them.
avoid_for:
  - editing the code it reviews (jev writes the fix)
  - planning new work (atlas plans)
---

I read Go diffs and say what is wrong with them. I name the file and the line.
`

var addRook = []talkoot.Op{{Op: talkoot.OpAdd, Member: "rook", Set: map[string]any{
	"role": "specialist", "persona": "Rook", "posture": "plan", "tools": []any{"read", "grep", "glob"},
}}}

// A recruiter session runs Hautoja, opens on a static greeting with no model
// call, keeps its binding in meta, and holds the two recruiter tools and no
// other Talkoot tool.
func TestARecruiterSessionOpensOnAStaticGreeting(t *testing.T) {
	w, info, calls := recruitCrew(t)
	if !strings.EqualFold(info.Persona, "hautoja") || info.Recruit != "crew" {
		t.Fatalf("info: persona %q, recruit %q", info.Persona, info.Recruit)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("the recruiter session is not live")
	}
	msgs := s.agent.Messages()
	if len(msgs) != 1 || msgs[0].Meta[core.MetaSource] != recruitGreetingSource || !strings.Contains(textOf(msgs[0]), "the talkoot crew") {
		t.Fatalf("transcript: %+v", msgs)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the provider saw %d requests before the person wrote", n)
	}
	raw, err := os.ReadFile(s.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"recruit":"crew"`)) {
		t.Error("the session file does not keep the recruit binding, so a restart would lose it")
	}
	if _, ok := s.agent.LookupTool("talkoot_propose"); !ok {
		t.Error("a recruiter session has no talkoot_propose")
	} else if tl, _ := s.agent.LookupTool("talkoot_propose"); tl == nil {
		t.Error("nil tool")
	} else if _, ok := tl.(*tools.TalkootRecruitProposeTool); !ok {
		t.Errorf("talkoot_propose is %T, want the recruiter's", tl)
	}
	for _, name := range []string{"talkoot_send", "talkoot_handoff", "talkoot_note_write", "talkoot_note_read"} {
		if _, ok := s.agent.LookupTool(name); ok {
			t.Errorf("a recruiter session holds %s", name)
		}
	}

	ctx := t.Context()
	if _, err := w.RecruitTalkoot(ctx, ctrlproto.TalkootRecruitParams{ID: " "}); talkootCode(err) != ctrlproto.CodeBadRequest {
		t.Errorf("a recruiter with no talkoot: err = %v, want bad request", err)
	}
	if _, err := w.RecruitTalkoot(ctx, ctrlproto.TalkootRecruitParams{ID: "nope"}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("a recruiter for a talkoot this daemon does not run: err = %v, want not found", err)
	}
	if _, err := w.RecruitTalkoot(ctx, ctrlproto.TalkootRecruitParams{ID: "crew", Persona: "kertoja"}); talkootCode(err) != ctrlproto.CodeBadRequest || !strings.Contains(err.Error(), "immersive persona") {
		t.Errorf("a recruiter running Kertoja: err = %v, want the immersive refusal", err)
	}
	// A persona given as a path resolves as the session build reads it.
	scene := filepath.Join(testsupport.TempDir(t), "scene.md")
	if err := os.WriteFile(scene, []byte("---\nname: Scene\nimmersive: true\n---\nYou narrate a scene.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RecruitTalkoot(ctx, ctrlproto.TalkootRecruitParams{ID: "crew", Persona: scene}); talkootCode(err) != ctrlproto.CodeBadRequest || !strings.Contains(err.Error(), "immersive persona") {
		t.Errorf("a recruiter running an immersive persona by path: err = %v, want the immersive refusal", err)
	}
	for name, opts := range map[string]ctrlproto.CreateOpts{
		"an experience": {Recruit: "crew", Experience: "chat"},
		"a background":  {Recruit: "crew", Background: "dusk"},
		"a greeting":    {Recruit: "crew", Greeting: 2},
	} {
		if _, err := w.CreateSession(ctx, opts); err == nil || !strings.Contains(err.Error(), "immersive") {
			t.Errorf("a recruiter with %s: err = %v, want the immersive refusal", name, err)
		}
	}
}

// The recruiter reads each member's job, not its charter, the installed
// drivers with what each can narrow, and the library.
func TestTheRecruiterReadsTheRoster(t *testing.T) {
	w, info, _ := recruitCrew(t)
	v, err := talkootRecruit{w: w, session: info.ID, talkoot: "crew"}.Roster()
	if err != nil {
		t.Fatal(err)
	}
	if v.Talkoot != "crew" || len(v.Members) != 2 || v.Members[0].Member.ID != "helm" {
		t.Fatalf("view: %+v", v)
	}
	if len(v.Drivers) == 0 || v.Drivers[0].Name != talkoot.DriverNative {
		t.Fatalf("drivers: %+v", v.Drivers)
	}
	for _, d := range v.Drivers {
		if d.Name == "terva" && !strings.Contains(d.Tools, "no tools list") {
			t.Errorf("terva narrows no tools, and the view says %q", d.Tools)
		}
		if d.Name == "claude" && d.Tools != "only read, write, edit, bash, grep, glob" {
			t.Errorf("claude narrows the core tools only, and the view says %q", d.Tools)
		}
	}
	tool := &tools.TalkootRecruitRosterTool{Recruit: talkootRecruit{w: w, session: info.ID, talkoot: "crew"}}
	res, err := tool.Execute(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(provider.TextBlock).Text
	for _, want := range []string{"Talkoot crew.", "- helm; role coordinator; driver native", "Installed drivers:\n- native:", "none of these is a member", "- hautoja: "} {
		if !strings.Contains(text, want) {
			t.Errorf("the roster text lacks %q:\n%s", want, text)
		}
	}
}

// A recruiter's proposal carries a new persona. The card shows it whole, and
// only the approval writes it, byte for byte, before the roster names it.
func TestARecruiterProposesAMemberWithANewPersona(t *testing.T) {
	w, info, _ := recruitCrew(t)
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	rec := talkootRecruit{w: w, session: info.ID, talkoot: "crew"}
	p, err := rec.Propose(addRook, rookDraft, "Nobody reviews jev's diffs.")
	if err != nil {
		t.Fatal(err)
	}
	if p.Proposer != talkoot.RecruiterPrefix+info.ID || p.Persona == nil || p.Persona.Name != "Rook" || p.Persona.Text != rookDraft {
		t.Fatalf("proposal: %+v", p)
	}
	if _, ok := persona.Lookup("rook"); ok {
		t.Fatal("the persona exists before a person approved it")
	}
	cards, err := w.talkootInbox(t.Context(), "crew")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Proposal == nil || cards[0].Proposal.Persona == nil || cards[0].Proposal.Persona.Text != rookDraft ||
		!strings.Contains(cards[0].Proposal.Title, "with the new persona Rook") {
		t.Fatalf("cards: %+v", cards)
	}
	if _, err := w.talkootDecide(t.Context(), "crew", "sothr", p.ID, ctrlproto.TalkootDecisionApprove, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(persona.Dir(), "rook.md")); err != nil || string(got) != rookDraft {
		t.Fatalf("the persona file: %q, %v", got, err)
	}
	if _, ok := persona.Lookup("Rook"); !ok {
		t.Error("the written persona does not resolve")
	}
	r := w.talkoot.runs["crew"].roster.Load()
	if m, ok := memberOf(*r, "rook"); !ok || m.Persona != "Rook" {
		t.Errorf("roster: %+v", r.Members)
	}
}

// What a recruiter may not propose is refused before any card exists.
func TestARecruiterProposalIsBounded(t *testing.T) {
	w, info, _ := recruitCrew(t)
	rec := talkootRecruit{w: w, session: info.ID, talkoot: "crew"}
	with := func(set map[string]any) []talkoot.Op {
		s := map[string]any{"role": "specialist", "persona": "Rook"}
		for k, v := range set {
			s[k] = v
		}
		return []talkoot.Op{{Op: talkoot.OpAdd, Member: "rook", Set: s}}
	}
	cases := []struct {
		name  string
		ops   []talkoot.Op
		draft string
		want  string
	}{
		{"yolo", with(map[string]any{"posture": "YOLO"}), rookDraft, "never proposes the posture yolo"},
		{"remove", []talkoot.Op{{Op: talkoot.OpRemove, Member: "jev"}}, "", "a person removes a member"},
		{"a taken name", with(map[string]any{"persona": "Kirjuri"}), strings.Replace(rookDraft, "name: Rook", "name: Kirjuri", 1), "is taken by"},
		{"no avoid_for", with(nil), strings.Replace(rookDraft, "avoid_for:\n  - editing the code it reviews (jev writes the fix)\n  - planning new work (atlas plans)\n", "", 1), "needs avoid_for"},
		{"no summary", with(nil), strings.Replace(rookDraft, "summary: Reviews Go diffs and reports defects, without editing them.\n", "", 1), "needs a summary"},
		{"unused", with(map[string]any{"persona": "mieli"}), rookDraft, "no operation sets persona"},
		{"too large", with(nil), rookDraft + strings.Repeat("x", talkoot.MaxPersonaDraftBytes), "above the"},
		{"immersive", with(nil), strings.Replace(rookDraft, "name: Rook\n", "name: Rook\nimmersive: true\n", 1), "cannot be immersive"},
	}
	for _, c := range cases {
		if _, err := rec.Propose(c.ops, c.draft, "why"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// Only a recruiter carries a persona.
	for _, from := range []string{"helm", "human:sothr"} {
		if _, err := w.talkootPropose("crew", from, addRook, "", "why", rookDraft, nil); err == nil || !strings.Contains(err.Error(), "only a recruiter") {
			t.Errorf("%s with a persona: err = %v", from, err)
		}
	}
	// The recruiter's tool has no undo, and the workspace refuses one too.
	if _, err := w.talkootPropose("crew", talkoot.RecruiterPrefix+info.ID, nil, "01M3AAAAAAAAAAAAAAAAAAAAAA", "why", "", nil); err == nil || !strings.Contains(err.Error(), "a person undoes a change") {
		t.Errorf("a recruiter undo: err = %v", err)
	}
	if cards, _ := w.talkootInbox(t.Context(), "crew"); len(cards) != 0 {
		t.Errorf("a refused proposal made cards: %+v", cards)
	}
}

// An approval writes the persona only when the operations that apply still
// name it, and only into a free name in a trusted workspace.
func TestAnApprovalWritesThePersonaOnlyWhenItStillApplies(t *testing.T) {
	w, info, _ := recruitCrew(t)
	rec := talkootRecruit{w: w, session: info.ID, talkoot: "crew"}
	ctx := t.Context()
	path := filepath.Join(persona.Dir(), "rook.md")

	// Untrusted: the card keeps the reason, and nothing is written.
	p, err := rec.Propose(addRook, rookDraft, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootDecide(ctx, "crew", "sothr", p.ID, ctrlproto.TalkootDecisionApprove, nil, ""); err == nil || !strings.Contains(err.Error(), "trusted workspace") {
		t.Fatalf("untrusted approval: err = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an untrusted approval wrote the persona: %v", err)
	}
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}

	// A persona that took the name since the proposal fails the approval, and
	// its file stays as it was.
	if err := os.MkdirAll(persona.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := strings.Replace(rookDraft, "Reviews Go diffs", "A person's own Rook", 1)
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootDecide(ctx, "crew", "sothr", p.ID, ctrlproto.TalkootDecisionApprove, nil, ""); err == nil || !strings.Contains(err.Error(), "now taken") {
		t.Fatalf("approval over a taken name: err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != mine {
		t.Fatal("the approval replaced a persona a person wrote")
	}

	// A file that does not parse holds the path, and Lookup cannot see it, so
	// only the write finds it. The card says why, and the roster is unchanged.
	const junk = "not a persona"
	if err := os.WriteFile(path, []byte(junk), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := persona.Lookup("rook"); ok {
		t.Fatal("the probe is void: Lookup sees the unparseable file")
	}
	if _, err := w.talkootDecide(ctx, "crew", "sothr", p.ID, ctrlproto.TalkootDecisionApprove, nil, ""); err == nil || !strings.Contains(err.Error(), "write the persona") {
		t.Fatalf("approval over an unreadable file: err = %v", err)
	}
	if got := inbox(t, w); len(got) != 1 || got[0].Proposal == nil || !strings.Contains(got[0].Proposal.Problem, "write the persona") {
		t.Fatalf("the card does not say why the approval failed: %+v", got)
	}
	if got, _ := os.ReadFile(path); string(got) != junk {
		t.Fatal("the approval replaced a file it did not write")
	}
	if _, ok := memberOf(*w.talkoot.runs["crew"].roster.Load(), "rook"); ok {
		t.Fatal("the roster changed although the persona was not written")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// A person's edit that no longer names the draft writes no persona.
	edited := []talkoot.Op{{Op: talkoot.OpAdd, Member: "rook", Set: map[string]any{"role": "specialist", "persona": "kirjuri", "posture": "plan"}}}
	if _, err := w.talkootDecide(ctx, "crew", "sothr", p.ID, ctrlproto.TalkootDecisionApprove, edited, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an edit that dropped the draft still wrote it: %v", err)
	}
}

// The draft answers to the references Lookup will resolve to it once it is
// written: its name and its file stem, in any case. A draft named with a space
// files under a hyphenated stem.
func TestADraftAnswersAsTheLibraryWill(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	d := &talkoot.PersonaDraft{Name: "Rook Two", Text: strings.Replace(rookDraft, "name: Rook", "name: Rook Two", 1)}
	env := envWithDraft(d)
	for ref, want := range map[string]bool{"Rook Two": true, "rook two": true, "rook-two": true, "rook": false, "other:rook-two": false} {
		ops := []talkoot.Op{{Op: talkoot.OpAdd, Member: "r", Set: map[string]any{"persona": ref}}}
		if got := opsUseDraft(ops, d); got != want {
			t.Errorf("opsUseDraft(%q) = %v, want %v", ref, got, want)
		}
		if got := env.PersonaExists(ref); got != want {
			t.Errorf("PersonaExists(%q) = %v, want %v", ref, got, want)
		}
	}
	dest, err := writePersonaDraft(d)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dest) != "rook-two.md" {
		t.Fatalf("the draft was written to %s, want rook-two.md", dest)
	}
	for _, ref := range []string{"Rook Two", "rook-two"} {
		if _, ok := persona.Lookup(ref); !ok {
			t.Errorf("after the write, Lookup(%q) finds nothing", ref)
		}
	}
}

// The proposal record is a file, so an approval checks the stored draft again
// before it writes it. A record edited after propose is refused with the
// reason on its card, and nothing is written.
func TestAnApprovalChecksTheStoredDraftAgain(t *testing.T) {
	w, info, _ := recruitCrew(t)
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	rec := talkootRecruit{w: w, session: info.ID, talkoot: "crew"}
	ctx := t.Context()
	dir := w.talkoot.runs["crew"].dir
	path := filepath.Join(persona.Dir(), "rook.md")
	noAvoid := strings.Replace(rookDraft, "avoid_for:\n  - editing the code it reviews (jev writes the fix)\n  - planning new work (atlas plans)\n", "", 1)
	for name, edit := range map[string]func(*talkoot.Proposal){
		"a draft without avoid_for": func(p *talkoot.Proposal) { p.Persona.Text = noAvoid },
		"a member as the proposer":  func(p *talkoot.Proposal) { p.Proposer = "helm" },
		"a renamed draft":           func(p *talkoot.Proposal) { p.Persona.Name = "Rook Two" },
	} {
		p, err := rec.Propose(addRook, rookDraft, "review")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := talkoot.LoadProposal(dir, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		edit(&stored)
		if err := talkoot.SaveProposal(dir, stored); err != nil {
			t.Fatal(err)
		}
		if _, err := w.talkootDecide(ctx, "crew", "sothr", p.ID, ctrlproto.TalkootDecisionApprove, nil, ""); err == nil {
			t.Fatalf("%s: the approval went through", name)
		}
		after, err := talkoot.LoadProposal(dir, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != talkoot.ProposalPending || after.Problem == "" {
			t.Errorf("%s: the record is %s with problem %q, want pending with a reason", name, after.Status, after.Problem)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: the approval wrote the persona: %v", name, err)
		}
		if _, ok := memberOf(*w.talkoot.runs["crew"].roster.Load(), "rook"); ok {
			t.Fatalf("%s: the roster changed", name)
		}
		if _, err := w.talkootDecide(ctx, "crew", "sothr", p.ID, ctrlproto.TalkootDecisionDecline, nil, ""); err != nil {
			t.Fatalf("%s: decline: %v", name, err)
		}
	}
}
