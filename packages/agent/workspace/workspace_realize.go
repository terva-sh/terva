package workspace

// realize (creator C3 — docs/plans/creator-realize.md): the exit from a
// cartographer conversation. PROPOSE re-reads the converged planning chat with
// one bounded Kartoittaja call and returns the finished structure — a World, the
// protagonist, the NPC roster, the lore, and a cold open — creating nothing.
// COMMIT imports the roster as cards and seeds a play session through the shared
// createSeededLocked primitive (the second caller after next_scene, proposal
// D5), with the protagonist as the bound user persona and the cold open standing
// in for the greeting — spending nothing. Nothing is created by the call that
// drafts it.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// realizePersona is the creator/cartographer persona whose charter authors the
// realize proposal — the same persona a C1 creator chat runs as, so it is
// re-reading its own conversation and knows which draft the author kept.
const realizePersona = "kartoittaja"

// realizeMaxTranscript bounds the conversation fed to the propose call. Larger
// than the scene-break window: realize reads the WHOLE planning conversation,
// where next_scene reads only the ending of a played scene.
const realizeMaxTranscript = 32000

// SessionsRealize implements sessions.realize.
func (w *Workspace) SessionsRealize(ctx context.Context, sess string, p ctrlproto.RealizeParams) (ctrlproto.RealizeResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.RealizeResult{}, err
	}
	if s.sess == nil || s.sess.Meta.Experience == "" {
		return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("realize is for a chat or play session"))
	}
	if p.Commit {
		return w.commitRealize(s, p)
	}
	return proposeRealize(ctx, s, p)
}

// proposeRealize extracts the structure: one booked Kartoittaja call over the
// whole conversation.
func proposeRealize(ctx context.Context, s *wsSession, p ctrlproto.RealizeParams) (ctrlproto.RealizeResult, error) {
	ag := s.agent
	if ag == nil || ag.Client == nil {
		return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not logged in"))
	}
	msgs := ag.Messages()
	persona, err := build.ResolvePersona(realizePersona)
	if err != nil || strings.TrimSpace(persona.Charter) == "" {
		return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the %s persona is unavailable", realizePersona))
	}
	cl := ag.Client
	_, model := s.currentModel()
	if strings.TrimSpace(p.Model) != "" {
		oc, om, err := s.ws.overrideClient(s.argsSnapshot(), p.Provider, p.Model)
		if err != nil {
			return ctrlproto.RealizeResult{}, err
		}
		cl, model = oc, om
	}

	user := renderRealizeEvidence(s.playerLabel(), msgs)
	req := provider.Request{
		Model:     model,
		System:    persona.Charter + "\n\n" + realizeTask,
		MaxTokens: 12000,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: user}},
			Time:    time.Now(),
		}},
	}
	out, usage, err := streamText(ctx, cl, req)
	s.recordSideChannelUsage(usage)
	if err != nil {
		return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "realize: %v", err)
	}
	proposal, err := parseRealizeProposal(out)
	if err != nil {
		return ctrlproto.RealizeResult{}, err
	}
	// Announce a shadowed cartographer (see MachinePersonaNotice).
	proposal.Note = build.MachinePersonaNotice(realizePersona, persona, proposal.Note)
	return ctrlproto.RealizeResult{Proposal: proposal}, nil
}

const realizeTask = `You are being run as a one-shot "realize" pass. The author has finished planning with you and has CONVERGED on a direction — a protagonist, a world, and an accepted opening. Your job is to extract the finished, PLAYABLE structure from the conversation so a session can be seeded from it. Invent nothing the conversation did not establish.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short remark, or an empty string; use it to say so if the conversation has NOT converged on anything to realize>",
  "world": { "name": "<short world name>", "description": "<1-3 sentence shelf blurb>" },
  "protagonist": { "name": "<the character the AUTHOR plays>", "description": "<who they are>", "personality": "<traits>", "notes": "<anything that constrains play, e.g. a language or ability quirk>" },
  "roster": [ { "name": "<an NPC the model voices>", "role": "<one line: their function>", "description": "<card description>", "personality": "<card personality>", "first_mes": "<a greeting ONLY for the NPC who opens the scene, else empty>" } ],
  "lore": [ { "name": "<entry title>", "keys": ["<trigger keyword>", "..."], "always_on": false, "content": "<the fact, self-contained>" } ],
  "cold_open": "<the opening scene the play session starts on, verbatim from the accepted opening>",
  "cold_open_actor": "<the roster name who delivers the cold open, or empty for narration>",
  "coordination": "<'' (auto: a meta-narrator picks who replies), 'off' (one character always replies), or 'focus:<name>'>",
  "given_by_author": ["<what the author supplied>"],
  "invented_by_you": ["<what you supplied>"]
}

Rules:
- The PROTAGONIST is who the author plays; the ROSTER is the NPCs the model voices. Never put the protagonist in the roster.
- LORE is the setting facts (laws, magic, geography, factions, deadlines) as discrete keyed entries, each self-contained so it injects when its keys appear. Prefer several focused entries over one blob; mark truly always-relevant facts always_on:true.
- COLD_OPEN is the scene the story starts on, taken verbatim from the opening the author accepted. cold_open_actor must be one of the roster names, or empty.
- Invent nothing the conversation did not establish. If it has not converged, set "note" to say so and leave the rest empty.`

// renderRealizeEvidence lays the whole planning conversation out for the propose
// call, speaker-attributed. A free function, like the doctors' evidence
// renderers, so the prompt is testable without a client.
func renderRealizeEvidence(playerLabel string, transcript []provider.Message) string {
	var b strings.Builder
	b.WriteString("THE PLANNING CONVERSATION (most recent last)\n\n")
	author := playerLabel
	if author == "" {
		author = "AUTHOR"
	}
	tail := renderSceneTail(transcript, author, "CARTOGRAPHER", realizeMaxTranscript)
	if tail == "" {
		tail = "(nothing said yet)"
	}
	b.WriteString(tail + "\n")
	return b.String()
}

// parseRealizeProposal extracts the structure. Lenient on propose: an
// unconverged conversation returns a Note and empty fields, which the client
// renders as "nothing to realize yet" rather than an error — the same shape the
// session doctor uses for a scene with nothing worth keeping.
func parseRealizeProposal(raw string) (*ctrlproto.RealizeProposal, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return nil, i18n.Errorf("the cartographer returned nothing usable")
	}
	var proposal ctrlproto.RealizeProposal
	if err := json.Unmarshal([]byte(body), &proposal); err != nil {
		return nil, i18n.Errorf("the cartographer returned a malformed proposal")
	}
	// Trim the strings the client renders as headings so whitespace-only fields
	// read as empty.
	proposal.World.Name = strings.TrimSpace(proposal.World.Name)
	proposal.Protagonist.Name = strings.TrimSpace(proposal.Protagonist.Name)
	proposal.ColdOpen = strings.TrimSpace(proposal.ColdOpen)
	proposal.ColdOpenActor = strings.TrimSpace(proposal.ColdOpenActor)
	proposal.Note = strings.TrimSpace(proposal.Note)
	return &proposal, nil
}

// commitRealize creates the play session. No model runs — the structure is the
// author's, whether or not they edited the proposal.
func (w *Workspace) commitRealize(s *wsSession, p ctrlproto.RealizeParams) (ctrlproto.RealizeResult, error) {
	prop := p.Proposal
	if prop == nil {
		return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a commit needs the proposal to realize"))
	}
	world := strings.TrimSpace(prop.World.Name)
	protagonist := strings.TrimSpace(prop.Protagonist.Name)
	coldOpen := strings.TrimSpace(prop.ColdOpen)
	if world == "" || protagonist == "" || coldOpen == "" {
		return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("realize needs a world, a protagonist, and a cold open"))
	}
	if s.busyNow() {
		return ctrlproto.RealizeResult{}, ctrlproto.ErrBusy
	}

	// Import each roster NPC as a card and build the cast (name → ref). A card
	// with no name cannot be addressed by the router, so it is dropped.
	cast := make(map[string]string, len(prop.Roster))
	for _, ch := range prop.Roster {
		name := strings.TrimSpace(ch.Name)
		if name == "" {
			continue
		}
		bytes, err := json.Marshal(map[string]string{
			"name":        name,
			"description": strings.TrimSpace(ch.Description),
			"personality": strings.TrimSpace(ch.Personality),
			"first_mes":   strings.TrimSpace(ch.FirstMes),
		})
		if err != nil {
			return ctrlproto.RealizeResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "realize: encode card %q: %v", name, err)
		}
		card, err := w.CardsImport(s.ws.ctx, ctrlproto.CardImportParams{Bytes: bytes})
		if err != nil {
			return ctrlproto.RealizeResult{}, err
		}
		cast[name] = card.ID
	}

	// The cold open is attributed to the NPC who delivers it when that actor is
	// on the roster; an unknown or empty actor opens in the narrator's voice.
	openingActor := strings.TrimSpace(prop.ColdOpenActor)
	if openingActor != "" {
		if _, ok := cast[openingActor]; !ok {
			openingActor = ""
		}
	}

	meta := s.sess.Meta
	opts := ctrlproto.CreateOpts{
		Title:      world,
		Provider:   meta.Provider,
		Model:      meta.Model,
		Experience: build.ExperiencePlay,
		Cast:       cast,
	}
	seed := &sceneSeed{
		lore:            realizeLore(prop.Lore),
		coordination:    strings.TrimSpace(prop.Coordination),
		parent:          s.id,
		userName:        protagonist,
		userDescription: protagonistDescription(prop.Protagonist),
		opening:         coldOpen,
		openingActor:    openingActor,
	}

	w.mu.Lock()
	next, err := w.createSeededLocked(opts, seed)
	w.mu.Unlock()
	if err != nil {
		return ctrlproto.RealizeResult{}, err
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	info := next.info()
	return ctrlproto.RealizeResult{Session: &info}, nil
}

// realizeLore converts the proposed entries to the session's lore shape. An
// entry with no keys and not marked always-on could never fire, so it is forced
// constant rather than written dead — a realized setting fact is meant to be
// available, and the author saw and kept it.
func realizeLore(in []ctrlproto.RealizeLore) []core.WorldLoreEntry {
	out := make([]core.WorldLoreEntry, 0, len(in))
	for _, e := range in {
		name := strings.TrimSpace(e.Name)
		content := strings.TrimSpace(e.Content)
		if name == "" || content == "" {
			continue
		}
		keys := make([]string, 0, len(e.Keys))
		for _, k := range e.Keys {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
		out = append(out, core.WorldLoreEntry{
			Name:     name,
			Keys:     keys,
			Constant: e.AlwaysOn || len(keys) == 0,
			Content:  content,
		})
	}
	return out
}

// protagonistDescription folds the protagonist's fields into the one
// description slot the bound user persona carries — "who the user is in the
// story" — keeping the personality and the play-constraint notes with it.
func protagonistDescription(p ctrlproto.RealizeCharacter) string {
	parts := make([]string, 0, 3)
	for _, s := range []string{p.Description, p.Personality, p.Notes} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}
