package modes

import "testing"

func TestSlashSuggesterHidesUnjailUntilJailed(t *testing.T) {
	s := newSlashSuggester()

	if got := commandNames(s.matches("/unj")); contains(got, "/unjail") {
		t.Fatalf("/unjail should be hidden while not jailed, got %v", got)
	}
	if got := commandNames(s.matches("/ja")); !contains(got, "/jail") {
		t.Fatalf("/jail should be visible while not jailed, got %v", got)
	}

	s.SetJailed(true)
	if got := commandNames(s.matches("/unj")); !contains(got, "/unjail") {
		t.Fatalf("/unjail should be visible while jailed, got %v", got)
	}
	if got := commandNames(s.matches("/ja")); contains(got, "/jail") {
		t.Fatalf("/jail should be hidden while jailed, got %v", got)
	}
}

func TestSlashSuggesterHasSwarm(t *testing.T) {
	s := newSlashSuggester()
	if got := commandNames(s.matches("/sw")); !contains(got, "/swarm") {
		t.Fatalf("/swarm missing from suggestions, got %v", got)
	}
}

func TestSlashNewIsRegisteredAndDestructive(t *testing.T) {
	s := newSlashSuggester()
	if got := commandNames(s.matches("/ne")); !contains(got, "/new") {
		t.Fatalf("/new missing from suggestions, got %v", got)
	}
	if !isKnownSlashCommand("/new") {
		t.Fatal("/new not recognized as a known slash command")
	}
	// /new resets the transcript, so it must cancel an in-flight turn.
	if !slashCancelsTurn("/new") {
		t.Fatal("/new should cancel an in-flight turn before running")
	}
}

func commandNames(cmds []slashCommand) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if !c.Header {
			out = append(out, c.Name)
		}
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
