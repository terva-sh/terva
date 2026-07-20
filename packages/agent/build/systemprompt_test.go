package build

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptStatusToolHint(t *testing.T) {
	with := BuildSystemPrompt(SystemPromptOpts{CWD: "/x", StatusTool: true})
	if !strings.Contains(with, "terva_status") {
		t.Errorf("expected terva_status hint when StatusTool=true; got:\n%s", with)
	}

	without := BuildSystemPrompt(SystemPromptOpts{CWD: "/x", StatusTool: false})
	if strings.Contains(without, "terva_status") {
		t.Errorf("did not expect terva_status hint when StatusTool=false; got:\n%s", without)
	}
}

// The immersive craft guards ride every chat/play prompt — each line traces to
// a measured long-session failure (kobeni review round 3): template lock,
// motif fatigue, list recitation, question barrages, flat crisis register, and
// no self-driven scene movement. Pin that they ship in both immersive
// experiences, survive a card's intro override (terva-brackets-the-card), and
// stay out of coding prompts.
func TestImmersiveCraftGuardsShipWithChatAndPlay(t *testing.T) {
	anchors := []string{
		"Never settle into a fixed template",
		"last couple of replies",
		"Do not recite lists back",
		"at most one question",
		"shift your rhythm",
		"advance time",
	}
	for _, exp := range []string{ExperienceChat, ExperiencePlay} {
		got := BuildSystemPrompt(SystemPromptOpts{Experience: exp})
		for _, a := range anchors {
			if !strings.Contains(got, a) {
				t.Errorf("%s prompt missing craft guard %q", exp, a)
			}
		}
	}
	// A card session overrides the intro but keeps terva's conventions — the
	// guards must survive that bracketing.
	card := BuildSystemPrompt(SystemPromptOpts{Experience: ExperienceChat, IntroOverride: "You are Kobeni.", IntroSource: "card:system_prompt"})
	if !strings.Contains(card, "at most one question") {
		t.Error("card-intro chat prompt lost the craft guards")
	}
	coding := BuildSystemPrompt(SystemPromptOpts{})
	if strings.Contains(coding, "at most one question") || strings.Contains(coding, "advance time") {
		t.Error("craft guards leaked into the coding prompt")
	}
}
