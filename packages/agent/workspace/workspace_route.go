package workspace

// The meta-narrator — the Worlds W3 coordinator (docs/proposals/stage-worlds.md).
//
// In a chat World with several characters (the bound card plus a kept-on-stage
// roster), a normal user turn no longer assumes the bound character answers:
// the meta-narrator ROUTES — a cheap in-turn call picks who speaks — then that
// speaker GENERATES, in their own voice, on their own model pin. The whole
// decision runs inside the one claimed turn slot, so busy/cancel/error/snapshot
// semantics are exactly a normal turn's:
//
//	prompt → routedTurn (beginTurn) → pickSpeaker
//	    ├─ the bound character → PromptWithPolicy (today's turn: full streaming,
//	    │  the cached charter prefix — the router call is the only added cost)
//	    └─ a roster character / the narrator → voiceLine (a card-grounded side
//	       call, per-actor CastModels pin, committed as an attributed line the
//	       way post.line lands)
//
// Dormant at N=1: an empty roster skips all of this — a 1:1 chat pays nothing
// (the proposal's invisibility rule). Directed authorship (post.line /
// direct.turn) remains the override, and SessionMeta.Coordination the setting:
// "" auto, "off" (bound always answers), "focus:<name>" (that character always
// answers, no routing call). Play sessions keep their director — actor_spawn IS
// their coordinator; this path is chat-only.
//
// Failure posture: the router degrades, never blocks — any routing error or
// unparseable pick falls through to the bound character's normal turn.

import (
	"context"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

const (
	// CoordinationOff pins every turn to the bound character (today's
	// behavior); coordinationFocus prefixes a roster name that always answers.
	CoordinationOff   = "off"
	coordinationFocus = "focus:"

	// routeMaxTokens bounds the router's answer — it is one name.
	routeMaxTokens = 24
	// routeMaxTranscript bounds the scene tail the router reads; routing needs
	// the immediate moment, not the history.
	routeMaxTranscript = 6000
	// voiceMaxTokens/voiceMaxTranscript mirror the suggest surface: a spoken
	// line is short, and the immediate scene is what a voice needs.
	voiceMaxTokens     = 1200
	voiceMaxTranscript = 8000
	// routeLoreScanDepth is how many recent messages feed the world-lore
	// keyword scan for a voiced line, mirroring lore.DefaultScanDepth's intent
	// at this seam (the new user text always joins the scan).
	routeLoreScanDepth = 4
)

// speakerPick is where a routed prompt goes: back to the bound character (the
// normal turn path), or to a named roster character / the narrator ("" name).
type speakerPick struct {
	bound bool
	name  string // roster character; "" with !bound = a narrator beat
	ref   string // the roster card ref ("" for the narrator)
}

// worldRoutes reports whether this World's coordination is live: a chat World
// whose roster holds someone OTHER than the bound character (N≥2 counting the
// bound character), with coordination not "off". The meta-narrator is viable
// exactly here — with one character it has nothing to decide, so every path
// stays today's.
func (s *wsSession) worldRoutes() bool {
	if s.sess == nil || s.sess.Meta.Experience != "chat" {
		return false
	}
	if s.sess.Meta.Coordination == CoordinationOff {
		return false
	}
	return len(s.routableRoster()) > 0
}

// routableRoster is the roster the meta-narrator routes over: the session cast
// minus the bound character, however they got in (cast.add once accepted the
// bound card, and old sessions may still carry it). Filtering at read time —
// not just at cast.add — keeps those sessions from routing a one-character
// scene, and keeps the router prompt from listing the bound character twice.
func (s *wsSession) routableRoster() map[string]string {
	roster := s.castRefs()
	boundRef := strings.TrimSpace(s.sess.Meta.Card)
	boundName, _ := s.boundCharacter()
	for name, ref := range roster {
		if (boundRef != "" && ref == boundRef) || strings.EqualFold(name, boundName) {
			delete(roster, name)
		}
	}
	return roster
}

// shouldRoute reports whether a prompt enters the meta-narrator path. Images
// ride the bound charter — the voicing side call is text-only, and the bound
// character is the session's vision-capable seat.
func (s *wsSession) shouldRoute(text string, images []ctrlproto.Image) bool {
	if len(images) > 0 || strings.TrimSpace(text) == "" {
		return false
	}
	return s.worldRoutes()
}

// routedTurn runs one meta-narrator turn: claim the slot, decide the speaker,
// then either fall through to the bound character's ordinary generation or
// voice the pick. launchTurn supplies the shared turn lifecycle (error + done
// events, snapshot-on-done, titling, queue restart).
func (s *wsSession) routedTurn(text string) error {
	turnCtx, err := s.beginTurn()
	if err != nil {
		return err
	}
	s.clearTail()
	s.launchTurn(turnCtx, func(ctx context.Context) error {
		pick := s.pickSpeaker(ctx, text)
		if pick.bound {
			return s.agent.PromptWithPolicy(ctx, text, nil, nil)
		}
		return s.voiceLine(ctx, pick, text)
	}, nil)
	return nil
}

// pickSpeaker decides who answers this prompt — or, with empty text, who the
// scene's next beat belongs to (a routed ▶ Advance: no new player message,
// the transcript as it stands is the whole question). focus pins without a
// model call; auto asks the router and falls back to the bound character on
// any failure — routing degrades, it never blocks a turn.
func (s *wsSession) pickSpeaker(ctx context.Context, text string) speakerPick {
	roster := s.routableRoster()
	if target, ok := strings.CutPrefix(s.sess.Meta.Coordination, coordinationFocus); ok {
		target = strings.TrimSpace(target)
		if ref, in := roster[target]; in {
			return speakerPick{name: target, ref: ref}
		}
		return speakerPick{bound: true} // a stale focus target — degrade
	}
	ag := s.agent
	if ag == nil || ag.Client == nil {
		return speakerPick{bound: true}
	}
	boundName, _ := s.boundCharacter()
	system := renderRouteSystem(boundName, roster, s.rosterHints(roster))
	tail := renderTranscriptTail(ag.Messages(), s.playerLabel(), boundName, routeMaxTranscript)
	if tail == "" {
		tail = frameSceneNotStarted()
	}
	prompt := frameRecentConversation() + "\n" + tail
	if strings.TrimSpace(text) != "" {
		prompt += "\n\n" + i18n.P("stage.route.new_message", "THE PLAYER'S NEW MESSAGE") + "\n" + s.playerLabel() + ": " + text
	} else {
		prompt += "\n\n" + i18n.P("stage.route.wait", "THE PLAYER WAITS — the scene advances on its own; whoever the moment belongs to speaks or acts next.")
	}
	prompt += "\n\n" + i18n.P("stage.route.pick", "Who speaks next? Reply with exactly one name.")
	_, model := s.currentModel()
	out, usage, err := streamText(ctx, ag.Client, provider.Request{
		Model:     model,
		System:    system,
		MaxTokens: routeMaxTokens,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: prompt}}, Time: time.Now()}},
	})
	// Booked before the error check: a router pick that failed still spent the
	// tokens it sent, and falling back to the bound character does not refund them.
	s.recordSideChannelUsage(usage)
	if err != nil {
		return speakerPick{bound: true}
	}
	return parseSpeaker(out, boundName, roster)
}

// voiceLine generates the picked speaker's line and commits it attributed —
// the model-driven sibling of postDirected. With text, the user's message
// lands first (exactly as a normal turn: a failed generation leaves the
// user's message in the transcript with no reply); a routed ▶ Advance passes
// "" and appends nothing but the line itself. Then the voice call runs on the
// character's pinned route and the line is appended with MetaRouted/MetaActor.
func (s *wsSession) voiceLine(ctx context.Context, pick speakerPick, text string) error {
	if text != "" {
		um := provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: text}},
			Time:    time.Now(),
		}
		if err := s.sess.AppendMessage(um); err != nil {
			return err
		}
		s.agent.SetMessages(append(s.agent.Messages(), um))
		// Show the player's bubble now, not after generation.
		s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	}
	if pick.name != "" {
		s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("🎭 %s is replying…", pick.name)))
	}

	var c *card.Card
	if pick.ref != "" {
		if sc, err := s.ws.cardStore().Get(pick.ref); err == nil {
			c = &sc.Card
		}
	}
	// The character's model pin (Phase 7) routes their generation; a pin that
	// fails to resolve degrades to the session route rather than failing the
	// line.
	cl := s.agent.Client
	_, model := s.currentModel()
	if route, ok := s.castModels()[pick.name]; ok && strings.TrimSpace(route.Model) != "" {
		if oc, om, err := s.ws.overrideClient(s.argsSnapshot(), route.Provider, route.Model); err == nil {
			cl, model = oc, om
		}
	}

	boundName, boundCard := s.boundCharacter()
	system := renderVoiceSystem(pick.name, c, s.userPersona(),
		boundName, boundCard, s.worldLoreBlock(text, pick.name), s.agent.Messages(), s.playerLabel())
	line, usage, err := streamText(ctx, cl, provider.Request{
		Model:     model,
		System:    system,
		MaxTokens: voiceMaxTokens,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: voiceInstruction(pick.name)}},
			Time:    time.Now(),
		}},
	})
	s.recordSideChannelUsage(usage)
	if err != nil {
		return err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the routed speaker produced no line"))
	}

	meta := map[string]string{core.MetaSource: core.MetaRouted}
	if pick.name != "" {
		meta[core.MetaActor] = pick.name
	}
	am := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: line}},
		Time:    time.Now(),
		Meta:    meta,
	}
	if err := s.sess.AppendMessage(am); err != nil {
		return err
	}
	s.agent.SetMessages(append(s.agent.Messages(), am))
	// A normal turn's "done" is emitted by the agent; a voiced line never runs
	// the agent, so emit it here or every subscriber's busy state sticks. (On
	// the error path launchTurn emits error → done itself.)
	s.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "done"}))
	return nil // launchTurn then broadcasts the sealed snapshot
}

// boundCharacter resolves the session's own character — the card it was
// created from (name + card), or the persona name for a card-less chat.
func (s *wsSession) boundCharacter() (string, *card.Card) {
	if ref := s.sess.Meta.Card; ref != "" {
		if sc, err := s.ws.cardStore().Get(ref); err == nil {
			if name := strings.TrimSpace(sc.Card.Name); name != "" {
				return name, &sc.Card
			}
			return "the character", &sc.Card
		}
	}
	if p := s.personaName(); p != "" {
		return p, nil
	}
	return "the character", nil
}

// playerLabel is the transcript label for the human's lines — the bound user
// persona's name, or "Me".
func (s *wsSession) playerLabel() string {
	if n := strings.TrimSpace(s.sess.Meta.UserName); n != "" {
		return n
	}
	return framePlayerFallbackLabel()
}

// rosterHints resolves each roster character to a one-line hint (the card
// description's first sentence-ish) for the router's context. Best-effort: a
// missing card just yields no hint.
func (s *wsSession) rosterHints(roster map[string]string) map[string]string {
	hints := make(map[string]string, len(roster))
	for name, ref := range roster {
		if sc, err := s.ws.cardStore().Get(ref); err == nil {
			hints[name] = hintOf(sc.Card.Description)
		}
	}
	return hints
}

// hintOf compresses a card description to a short router hint.
func hintOf(desc string) string {
	d := strings.Join(strings.Fields(desc), " ")
	const max = 140
	if len(d) > max {
		d = d[:max] + "…"
	}
	return d
}

// worldLoreBlock renders the World lore this speaker is cleared for (L2:
// audience-filtered — a named character sees world-shared entries plus their
// own; the narrator, "" — the scene authority — sees everything) that is
// relevant to this moment. The scan is the new user text plus the most recent
// transcript prose, so keyed entries fire the same way the session tail's do;
// constants always ride. Empty when nothing applies.
func (s *wsSession) worldLoreBlock(text, speaker string) string {
	entries := build.WorldLoreEntries(worldLoreFor(s.sess.Meta.WorldLore, speaker))
	if len(entries) == 0 {
		return ""
	}
	// An empty scan (a routed advance on a prose-less transcript) still
	// renders constants — Select fires them regardless of the haystack.
	var scan []string
	if strings.TrimSpace(text) != "" {
		scan = append(scan, text)
	}
	msgs := s.agent.Messages()
	for i := len(msgs) - 1; i >= 0 && len(scan) < routeLoreScanDepth; i-- {
		if p := messageProse(msgs[i]); p != "" {
			scan = append(scan, p)
		}
	}
	res := lore.Select(entries, lore.Config{ScanDepth: len(scan)}, scan, lore.ApproxTokens)
	return lore.Render(res.All())
}

// parseSpeaker maps the router's answer onto a pick: the bound character, the
// narrator, or a roster member — tolerant of case, quotes, and stray prose
// (exact name match first, then containment). Anything unrecognized degrades
// to the bound character.
func parseSpeaker(out, boundName string, roster map[string]string) speakerPick {
	ans := strings.ToLower(strings.TrimSpace(out))
	if i := strings.IndexByte(ans, '\n'); i >= 0 {
		ans = ans[:i]
	}
	ans = strings.Trim(ans, " \t\"'.,:;!*")
	if ans == "" {
		return speakerPick{bound: true}
	}
	if ans == "narrator" || ans == "the narrator" {
		return speakerPick{}
	}
	// The roster advertises the narrator under its translated name; accept that
	// form too (the English match above stays, so a locale change mid-session
	// cannot strand a reply either way).
	if n := strings.ToLower(strings.TrimSpace(narratorName())); n != "" && ans == n {
		return speakerPick{}
	}
	if strings.EqualFold(ans, boundName) {
		return speakerPick{bound: true}
	}
	for name, ref := range roster {
		if strings.EqualFold(ans, name) {
			return speakerPick{name: name, ref: ref}
		}
	}
	// A chatty router ("Elira should answer this one"): containment, longest
	// name first so "Elira the Red" beats "Elira".
	best := speakerPick{bound: true}
	bestLen := 0
	if boundName != "" && strings.Contains(ans, strings.ToLower(boundName)) {
		bestLen = len(boundName)
	}
	for name, ref := range roster {
		if len(name) > bestLen && strings.Contains(ans, strings.ToLower(name)) {
			best = speakerPick{name: name, ref: ref}
			bestLen = len(name)
		}
	}
	if bestLen == 0 && strings.Contains(ans, "narrator") {
		return speakerPick{}
	}
	return best
}

// renderRouteSystem writes the router's instruction: the speakers on stage and
// the job — pick one. Kept a free function (primitives in, string out) so it
// is unit-testable without a live session.
func renderRouteSystem(boundName string, roster map[string]string, hints map[string]string) string {
	var b strings.Builder
	b.WriteString(i18n.P("stage.route.task", routeTask))
	b.WriteString("\n\n" + i18n.P("stage.route.on_stage", "WHO IS ON STAGE") + "\n")
	b.WriteString("- " + i18n.P("stage.route.main_hint", "%s — the main character the player is talking to", boundName) + "\n")
	for _, name := range sortedNames(roster) {
		b.WriteString("- " + name)
		if h := hints[name]; h != "" {
			b.WriteString(" — " + h)
		}
		b.WriteString("\n")
	}
	b.WriteString("- " + narratorName() + " — " + i18n.P("stage.route.narrator_hint", "a scene/narration beat, when no single character should answer") + "\n")
	return b.String()
}

func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	// Deterministic order for the prompt (and tests).
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// routeTask is the meta-narrator's routing instruction. It picks, it does not
// write — generation belongs to the picked speaker.
const routeTask = `You are the meta-narrator of an ongoing roleplay scene: you decide WHO responds to the player's newest message. You never write the response yourself.

Pick the character whose reply the moment calls for — who was addressed, who the scene's momentum points at, whose reaction matters now. Prefer the character the player is directly engaging; bring another voice in when the moment clearly belongs to them; pick Narrator only when a scene beat (time passing, an arrival, atmosphere) serves better than any character's line.

Reply with EXACTLY one name from the list — nothing else. No punctuation, no explanation.`

// voiceActorTask generates a named character's line directly (unlike
// suggestActorTask, which drafts under user guidance). {name} is substituted.
const voiceActorTask = `You are {name} in an ongoing roleplay scene. The meta-narrator has decided {name} responds to the player's newest message.

Write {name}'s next line — their dialogue and any *action* beats, in their established voice. Respond to what the player just said; keep the scene moving. Do NOT write the player's words or actions, and do NOT speak for any other character.

Output ONLY {name}'s line, exactly as it should read in the scene. No preamble, no surrounding quotes, no meta-commentary. Match the scene's tone, length, and formatting conventions, and write in the tense the scene is already written in — read it off the transcript rather than defaulting to present tense.`

// voiceNarratorTask generates a narration beat directly.
const voiceNarratorTask = `You are the narrator of an ongoing roleplay scene. The meta-narrator has decided a NARRATIVE beat responds to the player's newest message — the scene speaks, not any single character.

Describe what happens next: the setting shifting, an event, an arrival, the passage of time. Do NOT script the player's choices or answer in any character's voice.

Output ONLY the narration, ready to drop into the scene. No preamble, no meta-commentary. Match the scene's established narrative voice and tense — read the tense off the transcript rather than defaulting to present tense.`

// userPersona is who the human in the scene is, as a side-channel prompt needs
// to know them. A struct rather than four more positional parameters: these
// renderers already take eight and nine arguments, and the two fields being
// added are exactly the two that were silently dropped when each of them
// hand-rolled its own "Name: description" line.
type userPersona struct {
	Name        string
	Description string
	Gender      string
	Pronouns    string
}

// brief renders the persona for a side-channel prompt, identity included.
func (p userPersona) brief() string {
	return build.UserPersonaBrief(p.Name, p.Description, p.Gender, p.Pronouns)
}

// userPersona reads the session's bound persona whole, so a caller cannot pick
// up the name and description and forget the gender and pronouns — which is how
// both side-channel prompts came to omit them.
func (s *wsSession) userPersona() userPersona {
	if s == nil || s.sess == nil {
		return userPersona{}
	}
	m := s.sess.Meta
	return userPersona{Name: m.UserName, Description: m.UserDescription, Gender: m.UserGender, Pronouns: m.UserPronouns}
}

// renderVoiceSystem assembles the routed speaker's system prompt: the voice
// task, who they are (card grounding), the world's shared lore, the player,
// the bound character (context), and the recent scene. A free function for
// testability, mirroring renderSuggestSystem.
func renderVoiceSystem(name string, c *card.Card, player userPersona, boundName string, bound *card.Card, loreBlock string, transcript []provider.Message, playerLabel string) string {
	var b strings.Builder
	if name == "" {
		b.WriteString(i18n.P("stage.voice.narrator", voiceNarratorTask))
	} else {
		b.WriteString(strings.ReplaceAll(i18n.P("stage.voice.actor", voiceActorTask), "{name}", name))
		if c != nil {
			b.WriteString("\n\n" + i18n.P("stage.voice.who_you_are", "WHO YOU ARE") + "\n")
			writeField(&b, "name", c.Name)
			writeField(&b, "description", c.Description)
			writeField(&b, "personality", c.Personality)
			writeField(&b, "scenario", c.Scenario)
		}
	}
	if loreBlock != "" {
		b.WriteString("\n\n" + i18n.P("stage.voice.world_lore", "WHAT EVERYONE ON STAGE KNOWS (world lore)") + "\n")
		b.WriteString(loreBlock + "\n")
	}
	b.WriteString("\n\n" + framePlayerContext() + "\n")
	who := player.brief()
	if who == "" {
		who = i18n.P("stage.voice.player_unspecified", "(not specified — infer from the conversation)")
	}
	b.WriteString(who + "\n")
	if bound != nil && name != strings.TrimSpace(bound.Name) {
		b.WriteString("\n" + i18n.P("stage.voice.main_character", "THE MAIN CHARACTER IN THE SCENE (context only — do not speak as them)") + "\n")
		writeField(&b, "name", bound.Name)
		writeField(&b, "description", bound.Description)
		writeField(&b, "personality", bound.Personality)
	}
	b.WriteString("\n" + frameRecentConversation() + "\n")
	tail := renderTranscriptTail(transcript, playerLabel, boundName, voiceMaxTranscript)
	if tail == "" {
		tail = frameSceneNotStarted()
	}
	b.WriteString(tail + "\n")
	return b.String()
}

// voiceInstruction is the single user message of a voice call — the scene
// lives in the system prompt, so this just pulls the trigger.
func voiceInstruction(name string) string {
	if name == "" {
		return i18n.P("stage.voice.cue.narrator", "(write the next narrative beat now)")
	}
	return i18n.P("stage.voice.cue.actor", "(write %s's next line now)", name)
}
