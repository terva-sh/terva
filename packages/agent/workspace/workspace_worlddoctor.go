package workspace

// The world doctor (SD6 — docs/plans/worlds-doctor.md): Dramaturgi reads a
// World as an ENSEMBLE — every roster character's card, together, alongside the
// World's lorebook — and proposes character edits beside world edits, plus the
// character the cast is missing.
//
// The other two doctors each read one thing. Seppä reads a card against its
// lint; Toimittaja reads a card against the scene it was played in. Neither can
// answer "who is missing from this cast", because neither is ever shown more
// than one card. That is the whole reason this exists, and it is why the
// evidence budget below is the load-bearing part: five cards and a lorebook is a
// materially bigger prompt than either of them was priced for.
//
// It runs on a SAVED WORLD by id, with no session anywhere. That was not always
// true: it first shipped session-scoped because a saved World had no sessionless
// write path, so a run from the shelf could only have produced proposals nothing
// could apply. The worlds.* content verbs closed that, and the scope moved to
// where it belonged — the World studio is a Library screen, and a doctor that
// demanded an open scene could not be reached from it.
//
// Nothing here applies anything. The accepts write through the worlds.* verbs,
// and a card edit goes through worlds.edit_character, which FORKS rather than
// rewriting the shared library card — so an accepted proposal cannot change a
// character another World is still playing.
//
// The scenes it reads are CHOSEN, not implied by a frame. A World's evidence is
// its whole history of play, and which nights matter is a judgement: a run aimed
// at "what has this World established that the lorebook never recorded" wants
// the nights that established it, not whichever chat happened to be open.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

const (
	// worldDoctorMaxRoster bounds the roster block — every card's prose,
	// together. The budget is shared across the cast and divided by
	// budgetShares, so a World of two gets generous room per character and a
	// World of eight still fits.
	worldDoctorMaxRoster = 20000

	// worldDoctorMaxScenes bounds how many scenes one run will read at all.
	// The picker defaults to a few and the pool above bounds the bytes, but an
	// author who ticks thirty nights would otherwise get thirty transcripts
	// read, parsed, and divided into slivers too thin to say anything — paying
	// in latency for evidence too fragmentary to use.
	worldDoctorMaxScenes = 6

	// worldDoctorMaxLore bounds the lorebook. A World accumulates entries
	// without bound — realize alone lands ~25 — and an unbudgeted lorebook is
	// the one input here that can grow past the cards it is supposed to
	// contextualize.
	worldDoctorMaxLore = 8000

	// The two families are capped SEPARATELY, not against one shared total.
	// They are reviewed in separate sections, so the failure to prevent is one
	// family crowding out the other: a shared cap of twelve lets a doctor that
	// found twelve card edits return no world edits at all, and the author never
	// learns there were any.
	worldDoctorMaxCardProposals  = 10
	worldDoctorMaxWorldProposals = 10
)

// worldDoctorMaxScene bounds the played scenes TOGETHER — a pool shared by every
// scene the author picked, divided by the same water-filling as the roster.
// Pooled rather than per-scene so one long night cannot crowd out three short
// ones, and so the cost of a run does not scale with how many boxes were ticked.
//
// Sized against a live run rather than reasoned from the armchair, because the
// armchair was wrong twice. It began at 6000 on the theory that scenes are
// CONTEXT rather than the warrant — the World is the subject, the scenes only
// say how it has been playing. Then 6000 turned out never to have been reached:
// the sizing pass asked renderSceneTail for a budget of 0 meaning "unbudgeted",
// which that function reads as zero bytes, so every scene rendered as its last
// message and the pool never bound. Raising it 8x moved the prompt by zero
// bytes, which is how the inert budget was found at all.
//
// 48000 is twice what the session doctor spends reading ONE scene
// (dramaturgMaxTranscript, 24000), pooled: two scenes get a session-doctor-grade
// look each, or six get a third of one. Still a tail (renderSceneTail keeps the
// END of a scene), still bounded, and a World assembled but never played still
// renders none of it.
//
// A var rather than a const so TestWorldDoctorSceneEvidenceGrowsWithTheBudget
// can move it. The budget has to be tested THROUGH the caller: the sizing pass,
// the water-filling, and the render agreed with each other for four slices while
// all three were wrong together, and a helper taking a budget parameter would
// have passed every time while its one caller kept handing it 0.
var worldDoctorMaxScene = 48000

// worldDoctorFields is the card surface this doctor reads and may edit.
//
// Narrower than the card doctor's allow-list on purpose. Seppä sees nine fields
// because it reads ONE card and card craft is its whole subject; showing nine
// fields for eight characters would spend the budget on system prompts and
// creator notes, which are not what an ensemble read is for. A proposal naming
// anything outside this set is dropped — including fields Seppä would accept,
// because a doctor that cannot see a field has no business rewriting it.
var worldDoctorFields = []string{"description", "personality", "scenario", "first_mes", "mes_example"}

// worldDoctorCard is one roster character as the doctor reads them.
type worldDoctorCard struct {
	Name     string
	Ref      string
	Card     card.Card
	Findings []card.Finding
}

// worldDoctorScene is one played scene as the doctor reads it.
type worldDoctorScene struct {
	Title  string
	Player string
	Msgs   []provider.Message
}

// WorldsDoctor implements worlds.doctor: sessionless, over a saved World.
//
// Gates and credential resolution live here; the spend-and-parse path lives in
// worldsDoctor below. Split for the reason sessionsDoctor is: the half that
// costs money runs under test with a scripted client, without resolving a real
// credential — and every refusal above the split is therefore provably
// PRE-SPEND, in the posture SD2/SD3 established.
func (w *Workspace) WorldsDoctor(ctx context.Context, p ctrlproto.WorldDoctorParams) (ctrlproto.WorldDoctorResult, error) {
	doc, err := build.NewWorldStore().Get(strings.TrimSpace(p.ID))
	if err != nil {
		return ctrlproto.WorldDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	if len(w.worldRosterCards(doc)) == 0 {
		return ctrlproto.WorldDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("this World has no characters to read"))
	}
	// No session to inherit a client from, so the ladder answers: the author's
	// explicit pick first, else the World's own default, else the workspace's.
	// Reading the World rung here is the point of having one — a doctor that
	// consulted the global default while standing inside a World the author had
	// aimed at a particular model would be quietly ignoring the aim.
	prov, mdl := strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model)
	if mdl == "" {
		if dp, dm, src := w.effectiveDefaultModel("", doc.ID); src == ctrlproto.DefaultSourceWorld {
			prov, mdl = dp, dm
		}
	}
	cl, model, err := w.overrideClient(w.args, prov, mdl)
	if err != nil {
		return ctrlproto.WorldDoctorResult{}, err
	}
	return w.worldsDoctor(ctx, cl, model, doc, p)
}

// worldsDoctor is the verb after the gates: evidence, one model call, booking,
// parse.
func (w *Workspace) worldsDoctor(ctx context.Context, cl provider.Client, model string, doc build.WorldDoc, p ctrlproto.WorldDoctorParams) (ctrlproto.WorldDoctorResult, error) {
	roster := w.worldRosterCards(doc)
	if len(roster) == 0 {
		return ctrlproto.WorldDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("this World has no characters to read"))
	}
	pers, err := persona.Resolve(dramaturgPersona)
	if err != nil || strings.TrimSpace(pers.Charter) == "" {
		return ctrlproto.WorldDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the %s persona is unavailable", dramaturgPersona))
	}

	user := renderWorldDoctorEvidence(doc.Name, doc.Description, roster, doc.Lore, w.worldDoctorScenes(ctx, doc.ID, p.Sessions), p.Steer)
	user += renderSessionDoctorDecisions(p.Decisions)

	req := provider.Request{
		Model:     model,
		System:    pers.Charter + "\n\n" + worldDoctorTask,
		MaxTokens: 8000,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: user}},
			Time:    time.Now(),
		}},
	}
	out, usage, err := streamText(ctx, cl, req)
	// Booked BEFORE the error check: a call that streamed and then failed still
	// cost money, and a ledger that only records successes reports less than was
	// spent. There is no session file to hold this row — that is what the
	// workspace ledger is for.
	w.bookSessionlessUsage(ctrlproto.MethodWorldsDoctor, doc.ID, model, usage)
	if err != nil {
		return ctrlproto.WorldDoctorResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "world doctor: %v", err)
	}
	res, err := parseWorldDoctorResult(out, roster, doc.Lore)
	if err != nil {
		return res, err
	}
	res.Note = persona.MachineNotice(dramaturgPersona, pers, res.Note)
	return res, nil
}

// bookSessionlessUsage records what a verb with no session in the frame spent.
// Best-effort: losing the row is bad, and failing the author's doctor run
// because the ledger could not be appended to is worse.
func (w *Workspace) bookSessionlessUsage(verb ctrlproto.Method, subject, model string, u provider.Usage) {
	_ = build.NewUsageLedger().Book(build.UsageRecord{
		At:       time.Now(),
		Verb:     string(verb),
		Subject:  subject,
		Provider: w.args.Provider,
		Model:    model,
		Usage:    u,
	})
}

// worldDoctorScenes loads the scenes the author picked, in the order they picked
// them, skipping any that do not belong to this World.
//
// Membership comes from the workspace's own session listing rather than from the
// file's first meta row. A session's World is a LAST-WINS row appended when it
// is stamped (SetWorld), so a reader of the opening meta reports "" for every
// session that joined a World after creation, and the doctor would have silently
// read no scenes at all. The listing folds those rows, and is the same source the
// Library's own member count comes from. The cheap read is now
// core.ReadSessionCreation, which does not offer a World to be wrong about.
//
// A stray id is IGNORED rather than refused: the picker offers only members, so
// one arriving here is a stale client rather than a request to honour, and
// failing the whole run over it would cost the author their other scenes.
// Reading the transcript is sessionless — core.ReadSessionMessages replays it
// from the file with nothing bound.
func (w *Workspace) worldDoctorScenes(ctx context.Context, worldID string, ids []string) []worldDoctorScene {
	if len(ids) == 0 {
		return nil
	}
	members := map[string]ctrlproto.SessionInfo{}
	if list, err := w.Sessions(ctx); err == nil {
		for _, si := range list {
			if si.World == worldID {
				members[si.ID] = si
			}
		}
	}
	out := make([]worldDoctorScene, 0, len(ids))
	for _, id := range ids {
		if len(out) >= worldDoctorMaxScenes {
			break
		}
		si, ok := members[strings.TrimSpace(id)]
		if !ok {
			continue
		}
		msgs, err := core.ReadSessionMessages(w.sessionPath(si.ID))
		if err != nil || len(msgs) == 0 {
			continue
		}
		title := strings.TrimSpace(si.Title)
		if title == "" {
			title = si.ID
		}
		player := strings.TrimSpace(si.UserName)
		if player == "" {
			player = framePlayerFallbackLabel()
		}
		out = append(out, worldDoctorScene{Title: title, Player: player, Msgs: msgs})
	}
	return out
}

// worldRosterCards resolves the SAVED World's roster to their stored cards, with
// each card's deterministic lint.
//
// The roster comes from the saved doc, not from a session's working copy. That
// is the whole point of the sessionless scope: the doctor audits the cast the
// World actually has, which is the cast every new scene will start with — not
// whichever actors one night happened to warm.
//
// A ref that no longer resolves is skipped rather than failing the run: a
// dangling roster entry is a real state (the card was deleted) and it must not
// stop the doctor reading the characters that do exist.
func (w *Workspace) worldRosterCards(doc build.WorldDoc) []worldDoctorCard {
	seen := map[string]bool{}
	out := make([]worldDoctorCard, 0, len(doc.Characters))
	for _, name := range sortedNames(doc.Characters) {
		ref := doc.Characters[name]
		if ref == "" || seen[ref] {
			continue
		}
		sc, err := w.cardStore().Get(ref)
		if err != nil {
			continue
		}
		seen[ref] = true
		out = append(out, worldDoctorCard{Name: name, Ref: ref, Card: sc.Card, Findings: card.Lint(sc.Card)})
	}
	return out
}

// budgetShares divides total across sizes so that nothing is trimmed until it
// has to be, and the LARGEST is trimmed first.
//
// Equal division alone is wrong here: it clips a rich character down to the same
// allowance as a one-line walk-on while the walk-on's unused room goes to waste.
// So this fills like water — everyone gets an equal share, whoever needs less
// than their share takes only what they need, and the remainder is redivided
// among the rest until nothing more can be given away. What is left over at the
// end is spread among the entries still over their allowance, which are by
// definition the biggest ones.
func budgetShares(sizes []int, total int) []int {
	out := make([]int, len(sizes))
	if len(sizes) == 0 || total <= 0 {
		return out
	}
	remaining, open := total, len(sizes)
	done := make([]bool, len(sizes))
	for open > 0 {
		share := remaining / open
		if share <= 0 {
			break
		}
		settled := false
		for i, n := range sizes {
			if done[i] || n > share {
				continue
			}
			out[i], done[i] = n, true
			remaining -= n
			open--
			settled = true
		}
		if !settled {
			// Everyone left wants more than an equal share: give them one each.
			// Integer division can leave a few characters unassigned; they are
			// not worth a second pass.
			for i := range sizes {
				if !done[i] {
					out[i] = share
				}
			}
			break
		}
	}
	return out
}

// renderWorldDoctorEvidence assembles the ensemble read. A free function, like
// renderDramaturgEvidence and renderEditorEvidence, so the prompt is testable
// without a session.
func renderWorldDoctorEvidence(worldName, worldDesc string, roster []worldDoctorCard, lore []core.WorldLoreEntry, scenes []worldDoctorScene, steer string) string {
	var b strings.Builder
	b.WriteString("THE WORLD — " + worldName + "\n")
	if strings.TrimSpace(worldDesc) != "" {
		b.WriteString(strings.TrimSpace(worldDesc) + "\n")
	}

	// The steer sits FIRST, before the evidence it is meant to direct the
	// reading of. The card doctor learned this the expensive way (the
	// model-facing text eval: a prohibition after the detail it governs is
	// followed far less often than one before it), and here the steer is not a
	// refinement — it is frequently the entire question.
	if s := strings.TrimSpace(steer); s != "" {
		b.WriteString("\nTHE AUTHOR'S REQUEST — this outranks everything the census and lint suggest\n" + s + "\n")
	}

	b.WriteString("\nTHE CAST — every character on this World's roster, as their cards stand\n")
	sizes := make([]int, len(roster))
	for i, c := range roster {
		sizes[i] = len(renderWorldDoctorCard(c, 0))
	}
	shares := budgetShares(sizes, worldDoctorMaxRoster)
	for i, c := range roster {
		b.WriteString(renderWorldDoctorCard(c, shares[i]))
	}

	b.WriteString("\nWORLD LORE ALREADY RECORDED — do not re-record any of this; retire (lore_retire) anything it has outgrown\n")
	b.WriteString(renderWorldDoctorLore(lore, worldDoctorMaxLore))

	// The scenes are optional and explicitly demoted. A World assembled but
	// never played is the case this doctor was asked for, and no scenes must
	// read as a normal state rather than as missing evidence.
	//
	// The budget is a POOL divided by the same water-filling as the roster, so a
	// long night and three short ones each keep what they need instead of the
	// long one taking everything. Each scene is NAMED, because "this came up in
	// two different scenes" is a materially stronger warrant than "this came up"
	// — and the doctor cannot tell them apart in one undifferentiated wall.
	// The sizing pass asks each scene for as much as the WHOLE pool could hold,
	// so sceneSizes measures demand and budgetShares divides against it. It used
	// to pass 0 here, meaning "unbudgeted" — renderSceneTail has no such
	// convention. A budget of zero is exceeded by the first line it considers,
	// so every scene measured, and rendered, as its LAST MESSAGE. The pool was
	// then never binding (two single messages always fit), which is why raising
	// it from 6000 to 48000 changed the prompt by zero bytes and why the first
	// live run read ~1% of ~1% of the play it was given.
	tails := make([]string, len(scenes))
	sceneSizes := make([]int, len(scenes))
	for i, sc := range scenes {
		tails[i] = renderSceneTail(sc.Msgs, sc.Player, "Narrator", worldDoctorMaxScene)
		sceneSizes[i] = len(tails[i])
	}
	sceneShares := budgetShares(sceneSizes, worldDoctorMaxScene)
	var played strings.Builder
	for i, sc := range scenes {
		tail := tails[i]
		if strings.TrimSpace(tail) == "" {
			continue
		}
		// Trimming to the awarded share RE-RENDERS rather than slicing bytes off
		// the front. Slicing was wrong twice over: it kept the OLDEST end of what
		// is deliberately a tail, and a byte offset lands mid-rune on any scene
		// with a non-ASCII character in it — which, for prose, is most of them.
		if sceneShares[i] > 0 && len(tail) > sceneShares[i] {
			tail = renderSceneTail(sc.Msgs, sc.Player, "Narrator", sceneShares[i]) +
				"\n…(this scene is longer than the room available; this is its most recent stretch)\n"
		}
		played.WriteString("\n--- SCENE: " + sc.Title + "\n" + tail + "\n")
	}
	if strings.TrimSpace(played.String()) != "" {
		b.WriteString("\nHOW IT HAS BEEN PLAYING (context, not your warrant — the World is)\n" + played.String())
	} else {
		b.WriteString("\nHOW IT HAS BEEN PLAYING\n(this World has not been played yet — judge it as written)\n")
	}
	return b.String()
}

// renderWorldDoctorCard renders one character. budget 0 means unbounded, which
// is how the caller measures a card's natural size before dividing the room.
func renderWorldDoctorCard(c worldDoctorCard, budget int) string {
	fields := doctorFields(c.Card)
	var body strings.Builder
	for _, f := range worldDoctorFields {
		v := strings.TrimSpace(fields[f])
		if v == "" {
			// An empty field is stated rather than skipped: "this character has
			// no personality written" is one of the most useful things an
			// ensemble read can notice, and a silent omission hides it.
			body.WriteString("  " + f + ": (empty)\n")
			continue
		}
		body.WriteString("  " + f + ": " + v + "\n")
	}
	for _, f := range c.Findings {
		body.WriteString("  LINT [" + f.Severity + "] " + f.Rule + " on " + f.Field + ": " + f.Detail + "\n")
	}
	out := body.String()
	if budget > 0 && len(out) > budget {
		out = out[:budget] + "\n  …(this character's card is longer than the room available)\n"
	}
	return "\n### " + c.Name + "  [card " + c.Ref + "]\n" + out
}

// renderWorldDoctorLore renders the lorebook within a budget, dropping the
// LONGEST entries first and saying how many went — so the doctor knows its view
// is partial rather than concluding the World records less than it does.
func renderWorldDoctorLore(lore []core.WorldLoreEntry, budget int) string {
	if len(lore) == 0 {
		return "(nothing recorded yet)\n"
	}
	lines := make([]string, 0, len(lore))
	for _, e := range lore {
		scope := "shared"
		if len(e.Audience) > 0 {
			scope = "known to " + strings.Join(e.Audience, ", ")
		}
		lines = append(lines, "- "+e.Name+" ["+scope+"]: "+strings.TrimSpace(e.Content)+"\n")
	}
	order := make([]int, len(lines))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(lines[order[a]]) < len(lines[order[b]]) })
	keep := make([]bool, len(lines))
	used, kept := 0, 0
	for _, i := range order {
		if used+len(lines[i]) > budget {
			continue
		}
		used += len(lines[i])
		keep[i] = true
		kept++
	}
	var b strings.Builder
	for i, l := range lines {
		if keep[i] {
			b.WriteString(l)
		}
	}
	if kept < len(lines) {
		b.WriteString(fmt.Sprintf("(%d further entries were too long to include — the lorebook is larger than shown)\n", len(lines)-kept))
	}
	return b.String()
}

const worldDoctorTask = `You are being run as a one-shot WORLD doctor. You receive a World, every character on its roster as their cards stand, the World lore already recorded, sometimes the scene it has been played in, sometimes an instruction from the author, and — on a follow-up round — their decisions on your previous proposals.

You read the cast TOGETHER. That is what this pass is for and what no other pass can do: whether these characters have anything to play against each other, where the World is thin, and who is missing.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short overall remark, or an empty string>",
  "card_proposals": [
    {
      "id": "<short stable id, e.g. c1>",
      "card": "<the [card <ref>] id of the character this edits>",
      "field": "<description | personality | scenario | first_mes | mes_example>",
      "severity": "<warn | info | suggestion>",
      "rationale": "<what about the ENSEMBLE earned this — name the other character or the lore it plays against>",
      "after": "<the full replacement text for that field>",
      "remove": false
    }
  ],
  "world_proposals": [
    {
      "id": "<short stable id, e.g. w1>",
      "kind": "<lore_entry | open_thread | lore_retire | character_new>",
      "rationale": "<what in the World earned this>",

      "name": "<lore_entry/open_thread: a short title; lore_retire: the EXACT name of the recorded entry>",
      "content": "<lore_entry/open_thread: the knowledge, one tight paragraph>",
      "keys": ["<trigger keywords; omit for always-on>"],
      "audience": ["<who knows this, by name; omit for world-shared>"],

      "character": "<character_new: the new character's name>",
      "description": "<character_new: who they are, and how they stand to the rest of the cast>",
      "personality": "<character_new: how they act>",
      "first_mes": "<character_new: an opening line in their voice>"
    }
  ]
}

Rules:
- The author's request, when present, outranks every other signal here. If they asked for characters, propose characters.
- A card edit must earn its place from the ENSEMBLE. "This description is vague" is the card doctor's job, not yours; "these two have the same backstory and nothing to disagree about" is yours. Never propose an edit whose rationale would read the same if the character were alone.
- Do not rewrite a character into someone else. Edits sharpen who they already are, or give them a relationship to someone they now share a World with.
- "after" is the WHOLE new value of that field, not a diff and not an addition to paste on the end.
- character_new is for a hole in the cast you can name — a foil, an authority, a rival, someone who wants what the protagonist wants. Say in the rationale what they are FOR and who they play against. Never propose someone already on the roster, and never a duplicate of an existing character under a new name.
- Existing lore outranks you: propose only the deltas, never an entry a recorded one already covers.
- Respect scope: knowledge only some characters hold gets an audience; never widen a secret into world-shared lore.
- lore_retire drops a RECORDED entry the World has outgrown — "name" must match one exactly, and nothing else about it is read.
- If the author declined a previous proposal with a reason, honor it: withdraw or revise, don't re-argue.
- A World that is coherent and a cast with no obvious hole is a real answer: return empty arrays and say so in "note". Do not manufacture proposals to fill the shape.`

// parseWorldDoctorResult validates the reply against the World it was run on.
// Every rule here is server-side so that every client sees the same contract.
func parseWorldDoctorResult(raw string, roster []worldDoctorCard, lore []core.WorldLoreEntry) (ctrlproto.WorldDoctorResult, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return ctrlproto.WorldDoctorResult{}, i18n.Errorf("the world doctor returned no usable proposals")
	}
	var parsed struct {
		Note           string                        `json:"note"`
		CardProposals  []ctrlproto.WorldCardProposal `json:"card_proposals"`
		WorldProposals []ctrlproto.SessionProposal   `json:"world_proposals"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ctrlproto.WorldDoctorResult{}, i18n.Errorf("the world doctor returned malformed proposals")
	}

	byRef := map[string]worldDoctorCard{}
	onStage := map[string]bool{}
	for _, c := range roster {
		byRef[c.Ref] = c
		onStage[strings.ToLower(strings.TrimSpace(c.Name))] = true
	}
	allowed := map[string]bool{}
	for _, f := range worldDoctorFields {
		allowed[f] = true
	}

	out := ctrlproto.WorldDoctorResult{Note: strings.TrimSpace(parsed.Note)}
	for _, p := range parsed.CardProposals {
		if len(out.CardProposals) >= worldDoctorMaxCardProposals {
			break
		}
		c, known := byRef[strings.TrimSpace(p.Card)]
		if !known || !allowed[p.Field] {
			continue
		}
		// Before comes from the CARD, never from the model's echo of it. The
		// card doctor established this and the reason is sharper with five
		// cards in the prompt: a doctor that misattributes one character's
		// current text to another would otherwise render a diff that is a
		// fiction, and the author would accept it believing they had read it.
		before := doctorFields(c.Card)[p.Field]
		after := strings.TrimSpace(p.After)
		if !p.Remove && after == "" {
			continue // nothing offered — indistinguishable from a model with nothing to say
		}
		if p.Remove {
			after = ""
			if strings.TrimSpace(before) == "" {
				continue // clearing an empty field changes nothing
			}
		}
		if after == before {
			continue
		}
		p.Before, p.After = before, after
		p.Character = c.Name
		p.Rationale = strings.TrimSpace(p.Rationale)
		if p.Severity == "" {
			p.Severity = "suggestion"
		}
		out.CardProposals = append(out.CardProposals, p)
	}

	loreNames := map[string]bool{}
	recorded := map[string]core.WorldLoreEntry{}
	for _, e := range lore {
		loreNames[strings.ToLower(e.Name)] = true
		recorded[strings.ToLower(strings.TrimSpace(e.Name))] = e
	}
	retiring := map[string]bool{}
	for _, p := range parsed.WorldProposals {
		if len(out.WorldProposals) >= worldDoctorMaxWorldProposals {
			break
		}
		p.Rationale = strings.TrimSpace(p.Rationale)
		switch p.Kind {
		case ctrlproto.SessionProposalLore, ctrlproto.SessionProposalThread:
			p.Name = strings.TrimSpace(p.Name)
			p.Content = strings.TrimSpace(p.Content)
			// The reserved pins belong to a scene's lifecycle, not a World's:
			// the scene-state card is replaced by sessions.doctor and the recap
			// by a scene break. Neither is this doctor's to write.
			if p.Name == "" || p.Content == "" || loreNames[strings.ToLower(p.Name)] ||
				core.IsSceneState(p.Name) || core.IsStorySoFar(p.Name) {
				continue
			}
			loreNames[strings.ToLower(p.Name)] = true
			p.Keys = trimList(p.Keys)
			p.Audience = dedupeNames(p.Audience)
			p.Character, p.Description, p.Personality, p.FirstMes = "", "", "", ""
		case ctrlproto.SessionProposalCharacterNew:
			p.Character = strings.TrimSpace(p.Character)
			if p.Character == "" || onStage[strings.ToLower(p.Character)] || strings.TrimSpace(p.Description) == "" {
				continue
			}
			onStage[strings.ToLower(p.Character)] = true
			p.Name, p.Content, p.Keys, p.Audience = "", "", nil, nil
		case ctrlproto.SessionProposalRetire:
			key := strings.ToLower(strings.TrimSpace(p.Name))
			e, exists := recorded[key]
			if !exists || retiring[key] || core.IsSceneState(e.Name) || core.IsStorySoFar(e.Name) {
				continue
			}
			retiring[key] = true
			// Answer with the RECORDED entry, not the doctor's paraphrase: the
			// author is being asked to delete something and must see what is
			// actually on file.
			p.Name, p.Content, p.Keys, p.Audience = e.Name, strings.TrimSpace(e.Content), e.Keys, e.Audience
			p.Character, p.Description, p.Personality, p.FirstMes = "", "", "", ""
		default:
			continue // outside the vocabulary, including the scene-only kinds
		}
		out.WorldProposals = append(out.WorldProposals, p)
	}
	return out, nil
}
