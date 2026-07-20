package workspace

// Shared framing atoms of the Stage side-channel prompts (the Suggest drafter,
// the meta-narrator router/voicer, the card doctor/editor). Each atom is one
// translatable template resolved at render time; section layout — the "\n\n"
// glue, list bullets, trailing newlines — stays in the render code per the
// whitespace doctrine (docs/localization.md). Centralized here so a string
// shared by several renderers has exactly one catalog entry.

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

func frameRecentConversation() string {
	return i18n.P("stage.frame.recent_conversation", "RECENT CONVERSATION (most recent last)")
}

func frameSceneNotStarted() string {
	return i18n.P("stage.frame.scene_not_started", "(the scene has not started yet)")
}

func framePlayerContext() string {
	return i18n.P("stage.frame.player_context", "THE PLAYER IN THIS SCENE (context — do not write their words)")
}

// framePlayerFallbackLabel labels the player's transcript lines when no persona
// name is bound.
func framePlayerFallbackLabel() string {
	return i18n.P("stage.frame.player_label", "Me")
}

// frameFallbackCharacter stands in for a character nobody named.
func frameFallbackCharacter() string {
	return i18n.P("stage.frame.the_character", "the character")
}

// speakerLabel names who said a message, for any surface that has to attribute
// one — the doctors' scene tails and the markdown export.
//
// It exists because that ladder was written out inline and is now needed twice,
// and because a THIRD copy already lives in the Stage client's ChatRow which
// keys off the derived directed/routed booleans rather than MetaSource. The two
// agree today only because messageToWire maps exactly those two source values;
// the moment a third is added they diverge silently. One function here at least
// keeps the Go side honest.
//
// The ladder, in precedence order: the player speaks for every user message; an
// explicit actor names itself; a directed or routed line without an actor is the
// narrator; anything else is the bound character. A card greeting falls all the
// way through, which is correct — it is the bound character's opening line.
func speakerLabel(m provider.Message, playerLabel, charLabel string) string {
	switch {
	case m.Role == provider.RoleUser:
		return playerLabel
	case m.Meta[core.MetaActor] != "":
		return m.Meta[core.MetaActor]
	case m.Meta[core.MetaSource] == core.MetaDirected || m.Meta[core.MetaSource] == core.MetaRouted:
		return narratorName()
	}
	return charLabel
}

// narratorName is both written into the router's roster and matched against
// the router's reply (parseSpeaker) — the one prompt token that must
// round-trip through the model. parseSpeaker accepts the English form too, so
// a mid-session locale change can't strand a reply.
func narratorName() string {
	return i18n.P("stage.route.narrator_name", "Narrator")
}
