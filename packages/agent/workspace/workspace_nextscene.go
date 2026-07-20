package workspace

// Start the next scene (SD5 — docs/proposals/session-doctor.md): close a
// played session at a chapter boundary and open a fresh one in the same World,
// carrying the recap and the pinned scene-state card, with the cliffhanger as
// a cold open.
//
// The point is WHERE the context resets. Compaction resets it mid-beat and by
// surprise: it keeps the nouns and drops exactly what the state card pins —
// numeric state, scheduled callbacks. A scene break resets it at a story
// boundary the author chose, and everything worth keeping crosses the gap as
// structure (lore + the pin) rather than as summarized prose.
//
// Two phases, one verb. PROPOSE spends one bounded Dramaturgi call and returns
// a draft title, recap, and cold open; COMMIT creates the scene from those
// fields as the author edited them and spends nothing. Nothing is ever created
// by the call that drafts it.

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

// nextSceneMaxTranscript bounds the scene evidence. Smaller than the doctor's
// sweep on purpose: a recap is written from the shape of the scene and its
// ending, and the durable facts are already structured in the lore this
// prompt also carries.
const nextSceneMaxTranscript = 16000

// SessionsNextScene implements sessions.next_scene.
func (w *Workspace) SessionsNextScene(ctx context.Context, sess string, p ctrlproto.NextSceneParams) (ctrlproto.NextSceneResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.NextSceneResult{}, err
	}
	if s.sess == nil || s.sess.Meta.Experience == "" {
		return ctrlproto.NextSceneResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("scene breaks are for chat and play sessions"))
	}
	if p.Commit {
		return w.commitNextScene(s, p)
	}
	return proposeNextScene(ctx, s, p)
}

// proposeNextScene drafts the boundary: one booked model call over the scene,
// the roster, and the recorded lore.
func proposeNextScene(ctx context.Context, s *wsSession, p ctrlproto.NextSceneParams) (ctrlproto.NextSceneResult, error) {
	ag := s.agent
	if ag == nil || ag.Client == nil {
		return ctrlproto.NextSceneResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not logged in"))
	}
	msgs := ag.Messages()
	if len(msgs) == 0 {
		return ctrlproto.NextSceneResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("this scene has not been played yet"))
	}
	persona, err := build.ResolvePersona(dramaturgPersona)
	if err != nil || strings.TrimSpace(persona.Charter) == "" {
		return ctrlproto.NextSceneResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the %s persona is unavailable", dramaturgPersona))
	}
	cl := ag.Client
	_, model := s.currentModel()
	if strings.TrimSpace(p.Model) != "" {
		oc, om, err := s.ws.overrideClient(s.argsSnapshot(), p.Provider, p.Model)
		if err != nil {
			return ctrlproto.NextSceneResult{}, err
		}
		cl, model = oc, om
	}

	boundName, _ := s.boundCharacter()
	user := renderNextSceneEvidence(s.playerLabel(), boundName, sortedNames(s.castRefs()), s.sess.Meta.WorldLore, msgs)
	req := provider.Request{
		Model:     model,
		System:    persona.Charter + "\n\n" + nextSceneTask,
		MaxTokens: 4000,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: user}},
			Time:    time.Now(),
		}},
	}
	out, usage, err := streamText(ctx, cl, req)
	s.recordSideChannelUsage(usage)
	if err != nil {
		return ctrlproto.NextSceneResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "next scene: %v", err)
	}
	draft, err := parseNextSceneDraft(out)
	if err != nil {
		return ctrlproto.NextSceneResult{}, err
	}
	draft.WorldID, draft.WorldName = s.worldForNextScene(boundName)
	return draft, nil
}

// worldForNextScene reports the World this story is in, or a name to suggest for
// one. A story with a World needs no offer — the next scene joins it. A story
// without one is about to become two sessions with nothing grouping them, which
// is the state this reports so the client can offer to fix it.
//
// The suggestion is the bound character's name (a chat is "their" story) and
// falls back to the session's title. Both are only a prefill.
func (s *wsSession) worldForNextScene(boundName string) (id, name string) {
	if id = strings.TrimSpace(s.sess.Meta.World); id != "" {
		if doc, err := build.NewWorldStore().Get(id); err == nil {
			return id, doc.Name
		}
		// Stamped into a World that is no longer on disk: treat it as unsaved
		// rather than reporting membership of something that cannot be shown.
		id = ""
	}
	if n := strings.TrimSpace(boundName); n != "" && n != "the character" {
		return "", n
	}
	return "", strings.TrimSpace(s.title)
}

const nextSceneTask = `You are being run as a one-shot scene-break writer. The author is ending this scene and starting the next one in the same world. You receive the played scene, who is on stage, and the world lore already recorded.

Respond with ONLY a JSON object (no prose, no code fences) of this exact shape:

{
  "note": "<one short remark, or an empty string>",
  "title": "<a short title for the NEXT scene>",
  "summary": "<the story so far: what a reader must know to pick up here>",
  "opening": "<the cold open: the first beat of the next scene>"
}

Rules:
- "summary" is reference knowledge, not prose: who these people are to each other, what was decided, what is owed, what was promised and is still outstanding. Write it tight and factual, in present tense. It REPLACES any existing "story so far" entry, so carry forward what still matters from it.
- Do not restate what the pinned scene-state card or an existing lore entry already holds — those cross the boundary intact. Summarize the SCENE, not the world.
- "opening" is in-fiction prose: the first beat of the new scene, in the same voice and person as the played scene. Honor the ending — if the scene closed on a cliffhanger, open into its aftermath; if it closed on a promise (a visit at dawn, a verdict due), open at that appointment.
- The opening invites the player back in: end it where their next move is obvious but unwritten. Never write the player's own words or actions.
- Keep the title short and concrete — no volume numbers, no "Part Two".`

// renderNextSceneEvidence assembles the scene-break grounding. A free
// function, like the doctor's evidence renderers, so the prompt is testable
// without a client.
func renderNextSceneEvidence(playerLabel, boundName string, roster []string, lore []core.WorldLoreEntry, transcript []provider.Message) string {
	var b strings.Builder
	b.WriteString("THE SCENE THAT IS ENDING (most recent last)\n")
	charLabel := boundName
	if charLabel == "" {
		charLabel = "Narrator"
	}
	tail := renderSceneTail(transcript, playerLabel, charLabel, nextSceneMaxTranscript)
	if tail == "" {
		tail = "(the scene has not started yet)"
	}
	b.WriteString(tail + "\n")

	b.WriteString("\nON STAGE — they carry into the next scene\n")
	b.WriteString("- " + playerLabel + " (the player)\n")
	if boundName != "" {
		b.WriteString("- " + boundName + " (the main character)\n")
	}
	for _, n := range roster {
		b.WriteString("- " + n + " (on the roster)\n")
	}

	// The lore crosses the boundary intact, so it is here as "already carried",
	// not as material to summarize. The pin and the previous recap get their
	// own blocks: one is the state the new scene opens in, the other is what
	// this draft replaces.
	var pin, prior string
	b.WriteString("\nWORLD LORE — carried into the next scene, do not summarize it\n")
	wrote := false
	for _, e := range lore {
		switch {
		case core.IsSceneState(e.Name):
			pin = e.Content
		case core.IsStorySoFar(e.Name):
			prior = e.Content
		default:
			scope := "shared"
			if len(e.Audience) > 0 {
				scope = "known to " + strings.Join(e.Audience, ", ")
			}
			b.WriteString("- " + e.Name + " [" + scope + "]: " + e.Content + "\n")
			wrote = true
		}
	}
	if !wrote {
		b.WriteString("(nothing recorded yet)\n")
	}

	b.WriteString("\nTHE PINNED SCENE-STATE CARD — the state the next scene opens in\n")
	if pin == "" {
		b.WriteString("(nothing pinned yet)\n")
	} else {
		b.WriteString(pin + "\n")
	}

	b.WriteString("\nTHE STORY SO FAR — your summary REPLACES this; carry forward what still matters\n")
	if prior == "" {
		b.WriteString("(this is the first scene)\n")
	} else {
		b.WriteString(prior + "\n")
	}
	return b.String()
}

// parseNextSceneDraft extracts the drafted fields. All three are required: a
// draft missing one would commit a scene with no recap or no opening, and the
// author cannot accept what they were not shown.
func parseNextSceneDraft(raw string) (ctrlproto.NextSceneResult, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return ctrlproto.NextSceneResult{}, i18n.Errorf("the scene-break writer returned nothing usable")
	}
	var parsed struct {
		Note    string `json:"note"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Opening string `json:"opening"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ctrlproto.NextSceneResult{}, i18n.Errorf("the scene-break writer returned a malformed draft")
	}
	out := ctrlproto.NextSceneResult{
		Note:    strings.TrimSpace(parsed.Note),
		Title:   strings.TrimSpace(parsed.Title),
		Summary: strings.TrimSpace(parsed.Summary),
		Opening: strings.TrimSpace(parsed.Opening),
	}
	if out.Title == "" || out.Summary == "" || out.Opening == "" {
		return ctrlproto.NextSceneResult{}, i18n.Errorf("the scene-break writer left the title, recap, or cold open empty")
	}
	return out, nil
}

// commitNextScene creates the scene. No model runs here — the fields are the
// author's, whether or not they edited the draft.
func (w *Workspace) commitNextScene(s *wsSession, p ctrlproto.NextSceneParams) (ctrlproto.NextSceneResult, error) {
	title := strings.TrimSpace(p.Title)
	summary := strings.TrimSpace(p.Summary)
	opening := strings.TrimSpace(p.Opening)
	if title == "" || summary == "" || opening == "" {
		return ctrlproto.NextSceneResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a scene needs a title, a recap, and a cold open"))
	}
	// A parent mid-turn is still writing the scene this recap describes — the
	// same settle-first discipline fork applies to a moving branch point.
	if s.busyNow() {
		return ctrlproto.NextSceneResult{}, ctrlproto.ErrBusy
	}

	meta := s.sess.Meta
	// The recap replaces any previous one, so scene five carries a single
	// cumulative entry rather than four stacked ones. It is always-on: a recap
	// that fires only on keywords is a recap the model reads only by luck.
	lore := make([]core.WorldLoreEntry, 0, len(meta.WorldLore)+1)
	placed := false
	for _, e := range meta.WorldLore {
		if core.IsStorySoFar(e.Name) {
			if placed {
				continue
			}
			lore = append(lore, core.WorldLoreEntry{Name: core.StorySoFarName, Constant: true, Content: summary})
			placed = true
			continue
		}
		// The pin crosses the boundary intact, but its DATE does not: it describes
		// the state the next scene opens in, so it is current as of that scene's
		// message zero. Carrying the parent's count forward would open every scene
		// with a pin already reported as N turns behind (SD6).
		if core.IsSceneState(e.Name) {
			e.PinnedAt = 0
		}
		lore = append(lore, e)
	}
	if !placed {
		lore = append(lore, core.WorldLoreEntry{Name: core.StorySoFarName, Constant: true, Content: summary})
	}

	// The cold open is attributed to the main character when there is one, so
	// it renders as their line rather than an unattributed beat; a World with
	// no bound card opens in the narrator's voice.
	boundName, _ := s.boundCharacter()

	// Group the story before it splits. A scene boundary is the moment a chat
	// stops being one session and becomes a story told across several, and until
	// now nothing marked that: the successor copied `world`, which was empty
	// whenever nobody had run worlds.save by hand, so both halves came out
	// grouped by nothing. Promoting here stamps the ENDING scene too, which is
	// the half that would otherwise be left behind.
	//
	// Only when the author named one, and only when there is no World already —
	// a story that has one just joins it. A failure here is fatal on purpose:
	// they asked for the grouping, and silently opening an ungrouped scene would
	// be the very outcome they were trying to avoid.
	worldID := meta.World
	if worldID == "" {
		if name := strings.TrimSpace(p.World); name != "" {
			saved, err := s.saveWorld(name, "")
			if err != nil {
				return ctrlproto.NextSceneResult{}, err
			}
			worldID = saved.ID
			meta = s.sess.Meta // saveWorld stamped membership; re-read before seeding
		}
	}
	opts := ctrlproto.CreateOpts{
		Title:      title,
		Provider:   meta.Provider,
		Model:      meta.Model,
		Persona:    meta.Persona,
		Experience: meta.Experience,
		Card:       meta.Card,
		Cast:       meta.Cast,
		Greeting:   meta.Greeting,
		Background: meta.Background,
	}
	seed := &sceneSeed{
		lore:            lore,
		coordination:    meta.Coordination,
		world:           worldID,
		parent:          s.id,
		castModels:      meta.CastModels,
		note:            meta.Note,
		userName:        meta.UserName,
		userDescription: meta.UserDescription,
		userGender:      meta.UserGender,
		userPronouns:    meta.UserPronouns,
		opening:         opening,
		openingActor:    boundName,
	}

	w.mu.Lock()
	next, err := w.createSeededLocked(opts, seed)
	w.mu.Unlock()
	if err != nil {
		return ctrlproto.NextSceneResult{}, err
	}
	// The ending scene's own membership may have just changed, so its watchers
	// need the new snapshot too — the sessions list alone would not show it.
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	info := next.info()
	out := ctrlproto.NextSceneResult{Title: title, Summary: summary, Opening: opening, Session: &info}
	// Echo the grouping back rather than making the client infer it from what it
	// asked for: a commit that joined an existing World reports that one, not the
	// name the author never got to type.
	if worldID != "" {
		out.WorldID = worldID
		if doc, err := build.NewWorldStore().Get(worldID); err == nil {
			out.WorldName = doc.Name
		}
	}
	return out, nil
}
