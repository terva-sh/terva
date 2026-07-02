package agent

import "testing"

// The child re-parses its argv, so --substrate (emitted by the swarm boot spec)
// must be accepted and stored, alongside --play/--card.
func TestParseArgs_SubstrateAndImmersiveFlags(t *testing.T) {
	a, err := ParseArgs([]string{"--play", "--card", "/c/innkeeper.png", "--substrate", "world:local:tavern"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Experience != ExperiencePlay {
		t.Errorf("Experience = %q, want play", a.Experience)
	}
	if a.Card != "/c/innkeeper.png" {
		t.Errorf("Card = %q", a.Card)
	}
	if a.Substrate != "world:local:tavern" {
		t.Errorf("Substrate = %q", a.Substrate)
	}
}
