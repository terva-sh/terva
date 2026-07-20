package workspace

// The reply-suggestion surface — the daemon half of Stage's "Suggest a reply".
//
// A drafting aid alongside a live chat: given the session's transcript, the
// user's bound persona, and the character card, it writes the HUMAN player's
// next message in their voice. The user sketches an intent, refines it in a
// back-and-forth with the model, then copies the result into the composer — the
// transcript is never touched. Tool-less and stateless: the client carries the
// refinement history, and each call re-reads the live session so a draft can
// account for anything the character just said.
//
// Unlike a side chat (which keeps the session's character system prompt so the
// model answers AS the character), this REPLACES the system prompt with a
// player-drafting instruction, and moves the roleplay scene into that prompt as
// context — so the message list itself is purely the out-of-character drafting
// dialogue.

import (
	"context"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

var _ ctrlproto.SuggestController = (*Workspace)(nil)

// suggestMaxTranscript bounds how much of the tail transcript we feed the
// drafter — enough for the immediate scene, most-recent anchored, without
// dragging the whole history (and its cost) into a one-shot draft.
const suggestMaxTranscript = 8000

// suggestMaxTokens caps the drafted message length. A player's line is short;
// this is generous headroom, not a target.
const suggestMaxTokens = 1200

// SuggestReply drafts the player's next message against the live session.
func (w *Workspace) SuggestReply(ctx context.Context, sess string, p ctrlproto.SuggestParams) (ctrlproto.SuggestResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.SuggestResult{}, err
	}
	ag := s.agent
	if ag == nil || ag.Client == nil {
		// No credential resolved for this session yet (a credential-less boot
		// before /login). Nothing to draft against.
		return ctrlproto.SuggestResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "not logged in")
	}
	// Default to the session's live client + model; a per-generation override
	// (Phase 7) resolves a fresh client for the chosen model instead.
	cl := ag.Client
	_, model := s.currentModel()
	if strings.TrimSpace(p.Model) != "" {
		oc, om, err := w.overrideClient(s.argsSnapshot(), p.Provider, p.Model)
		if err != nil {
			return ctrlproto.SuggestResult{}, err
		}
		cl, model = oc, om
	}

	// The character card, if this session has one — richer voice/context than the
	// visible transcript tail alone. A play or plain-chat session may have none;
	// the transcript still carries the scene.
	var c *card.Card
	if ref := s.sess.Meta.Card; ref != "" {
		if sc, err := w.cardStore().Get(ref); err == nil {
			c = &sc.Card
		}
	}
	target := suggestTarget{kind: p.Target, name: p.TargetName, voice: p.TargetVoice}
	// Voicing a library card (Worlds W1): resolve it to the character's full voice,
	// which supersedes a typed walk-on. Name defaults to the card's.
	if p.Target == "actor" {
		if ref := strings.TrimSpace(p.TargetCard); ref != "" {
			if sc, err := w.cardStore().Get(ref); err == nil {
				target.card = &sc.Card
				if strings.TrimSpace(target.name) == "" {
					target.name = strings.TrimSpace(sc.Card.Name)
				}
			}
		}
	}
	// A directed draft grounds on the World lore its speaker is cleared for
	// (L2): the drafted character sees world-shared entries plus their own, the
	// narrator sees everything, and a player draft carries none (the player is
	// not a character). The guidance note joins the keyword scan, so "ask about
	// the debt" fires the debt entry.
	loreBlock := ""
	switch {
	case target.isActor():
		name := strings.TrimSpace(target.name)
		if name == "" {
			name = "walk-on" // an unnamed walk-on holds no targeted lore
		}
		loreBlock = s.worldLoreBlock(p.Note, name)
	case target.isNarrator():
		loreBlock = s.worldLoreBlock(p.Note, "")
	}
	system := renderSuggestSystem(target, s.userPersona(), c, loreBlock, ag.Messages())

	// The drafting back-and-forth: each prior round is (user guidance → assistant
	// draft), then the new guidance. The roleplay transcript lives in the system
	// prompt, so this list is purely the out-of-character drafting dialogue.
	var msgs []provider.Message
	for _, t := range p.History {
		msgs = append(msgs, suggestMessage(provider.RoleUser, suggestGuidance(t.Note)))
		if strings.TrimSpace(t.Draft) != "" {
			msgs = append(msgs, suggestMessage(provider.RoleAssistant, t.Draft))
		}
	}
	msgs = append(msgs, suggestMessage(provider.RoleUser, suggestGuidance(p.Note)))

	req := provider.Request{
		Model:     model,
		System:    system,
		MaxTokens: suggestMaxTokens,
		Messages:  msgs,
		// No tools: drafting is conversational, not agentic.
	}
	text, usage, err := streamText(ctx, cl, req)
	s.recordSideChannelUsage(usage)
	if err != nil {
		return ctrlproto.SuggestResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "suggest a reply: %v", err)
	}
	return ctrlproto.SuggestResult{Draft: strings.TrimSpace(text)}, nil
}

func suggestMessage(role provider.Role, text string) provider.Message {
	return provider.Message{
		Role:    role,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Time:    time.Now(),
	}
}

// suggestGuidance normalizes one round's guidance: an empty note becomes an
// explicit "no guidance, suggest something" instruction so a blank sketch still
// reads as a clear request rather than an empty user turn.
func suggestGuidance(note string) string {
	n := strings.TrimSpace(note)
	if n == "" {
		return "(no specific guidance — suggest a natural next line)"
	}
	return n
}

const suggestTask = `You are a writing assistant helping a human player compose THEIR next message in an ongoing roleplay chat. You write as the player — the human — in the first person, in their voice. You are NOT the character the player is talking to: never write the character's dialogue or actions for them.

Output ONLY the text of the player's next message. No preamble ("Here's a draft:"), no surrounding quotes, no narration about what you are doing, no list of options — just the message itself, exactly as the player would type it to send. Match the scene's tone, length, and formatting conventions (for example, *actions* in asterisks if the scene uses them), and write in the tense the scene is already written in — read it off the transcript rather than defaulting to present tense.

The user will give you guidance for what they want to say; draft accordingly. When they follow up with a refinement (shorter, angrier, mention the locked door), revise your previous draft to match. If they give no guidance, suggest a natural, in-character next line that moves the scene forward.`

// suggestActorTask drafts a named character's line — an NPC or walk-on the user
// is bringing into the scene at their reply boundary (Phase 6). {name} is
// substituted in.
const suggestActorTask = `You are a writing assistant composing the next line for {name} — a character in an ongoing roleplay scene — in THEIR voice. Write only what {name} says and does.

Do NOT write the player's words or actions, and do NOT speak for the main character the player is in dialogue with (unless {name} IS that character). {name} is being brought into the scene by the user; give them a voice that fits who they are and what is happening.

Output ONLY {name}'s line — their dialogue and any *action* beats, exactly as it should read in the scene. No preamble, no surrounding quotes, no narration about what you are doing — just the line. Match the scene's tone, length, and formatting conventions, and write in the tense the scene is already written in — read it off the transcript rather than defaulting to present tense.

The user will give you guidance for what {name} should say or do; draft accordingly, revising on follow-up. If they give none, suggest a natural next line for {name} that fits the moment.`

// suggestNarratorTask drafts a narrative beat — the narrator's voice, not any
// one character's (Phase 6).
const suggestNarratorTask = `You are a writing assistant composing the next NARRATIVE beat for an ongoing roleplay scene — the narrator's voice, not any single character's. Describe what happens: the setting, the atmosphere, an event, the passage of time, an entrance, a turn in the scene. Write in the scene's established narrative voice and tense.

Do NOT script the player's choices or put decisive words in the player's mouth. You may bring in incidental or background figures, but keep the focus on moving the scene, not on speaking for the character the player is in dialogue with.

Output ONLY the narration itself. No preamble, no surrounding quotes, no meta-commentary — just the beat, ready to drop into the scene. Match the scene's tone, length, and formatting conventions, and write in the tense the scene is already written in — read it off the transcript rather than defaulting to present tense.

The user will give you guidance; draft accordingly, revising on follow-up. If they give none, write a natural next beat that moves the scene forward.`

// suggestTarget selects whose line renderSuggestSystem drafts: the player
// (default), a named character (an NPC/walk-on), or the narrator. name/voice
// apply only to the actor kind; card, when set (Worlds W1), voices a library
// character from its full card and supersedes voice.
type suggestTarget struct {
	kind  string // "" / "user" / "actor" / "narrator"
	name  string
	voice string
	card  *card.Card
}

func (t suggestTarget) isActor() bool    { return t.kind == "actor" }
func (t suggestTarget) isNarrator() bool { return t.kind == "narrator" }

// renderSuggestSystem assembles the drafting system prompt: the voice
// instruction for whoever is being drafted (player by default; a named character
// or the narrator for directed authorship), the World lore the speaker is
// cleared for (loreBlock, pre-filtered by the caller — "" for none), who the
// player is, the character they are talking to (if any), and the recent scene.
// Kept a free function (primitives in, string out) so it is unit-testable
// without a live session.
func renderSuggestSystem(t suggestTarget, player userPersona, c *card.Card, loreBlock string, transcript []provider.Message) string {
	var b strings.Builder
	switch {
	case t.isActor():
		name := strings.TrimSpace(t.name)
		if t.card != nil && strings.TrimSpace(t.card.Name) != "" {
			name = strings.TrimSpace(t.card.Name)
		}
		if name == "" {
			name = "the character"
		}
		b.WriteString(strings.ReplaceAll(suggestActorTask, "{name}", name))
		// A picked library card (W1) gives the richer voice — its authored fields —
		// and supersedes a typed walk-on description.
		if t.card != nil {
			b.WriteString("\n\nWHO YOU ARE VOICING\n")
			writeField(&b, "name", t.card.Name)
			writeField(&b, "description", t.card.Description)
			writeField(&b, "personality", t.card.Personality)
			writeField(&b, "scenario", t.card.Scenario)
		} else if voice := strings.TrimSpace(t.voice); voice != "" {
			b.WriteString("\n\nWHO YOU ARE VOICING\n")
			b.WriteString(name + ": " + voice + "\n")
		}
	case t.isNarrator():
		b.WriteString(suggestNarratorTask)
	default:
		b.WriteString(suggestTask)
	}
	if loreBlock != "" {
		b.WriteString("\n\nWHAT THE SPEAKER KNOWS OF THIS WORLD (lore)\n")
		b.WriteString(loreBlock + "\n")
	}

	// Who the player is — the voice to write for a "user" draft, and context to
	// keep out of the mouth of a directed actor/narrator draft.
	if t.isActor() || t.isNarrator() {
		b.WriteString("\n\nTHE PLAYER IN THIS SCENE (context — do not write their words)\n")
	} else {
		b.WriteString("\n\nWHO THE PLAYER IS\n")
	}
	who := player.brief()
	if who == "" {
		who = "(not specified — infer a natural voice from the conversation)"
	}
	b.WriteString(who + "\n")

	charName := "the character"
	if c != nil && strings.TrimSpace(c.Name) != "" {
		charName = strings.TrimSpace(c.Name)
		b.WriteString("\nTHE CHARACTER THE PLAYER IS TALKING TO (context only — do not speak as them)\n")
		writeField(&b, "name", c.Name)
		writeField(&b, "description", c.Description)
		writeField(&b, "personality", c.Personality)
		writeField(&b, "scenario", c.Scenario)
	}

	b.WriteString("\nRECENT CONVERSATION (most recent last)\n")
	playerLabel := strings.TrimSpace(player.Name)
	if playerLabel == "" {
		playerLabel = "Me"
	}
	tail := renderTranscriptTail(transcript, playerLabel, charName, suggestMaxTranscript)
	if tail == "" {
		tail = "(the scene has not started yet)"
	}
	b.WriteString(tail + "\n")
	return b.String()
}

func writeField(b *strings.Builder, label, value string) {
	if v := strings.TrimSpace(value); v != "" {
		b.WriteString(label + ": " + v + "\n")
	}
}

// renderTranscriptTail renders the most recent messages as "Name: text" lines,
// keeping the tail within budget (most-recent anchored — the immediate scene is
// what a draft needs). Non-text blocks (tool calls, reasoning) are dropped; a
// message with no prose is skipped.
func renderTranscriptTail(msgs []provider.Message, playerLabel, charLabel string, budget int) string {
	var lines []string
	for _, m := range msgs {
		text := messageProse(m)
		if text == "" {
			continue
		}
		label := charLabel
		if m.Role == provider.RoleUser {
			label = playerLabel
		}
		lines = append(lines, label+": "+text)
	}
	// Keep the tail within budget: walk from the end, accumulating until the next
	// line would overflow, then take that suffix.
	start := 0
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		total += len(lines[i]) + 1
		if total > budget {
			start = i + 1
			break
		}
	}
	if start >= len(lines) {
		// Even the single most-recent line exceeds the budget; keep it anyway so
		// the drafter is not blind to what it is replying to.
		if len(lines) == 0 {
			return ""
		}
		start = len(lines) - 1
	}
	return strings.Join(lines[start:], "\n")
}

// messageProse joins a message's prose blocks, ignoring tool/reasoning content.
func messageProse(m provider.Message) string {
	var parts []string
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if s := strings.TrimSpace(tb.Text); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}
