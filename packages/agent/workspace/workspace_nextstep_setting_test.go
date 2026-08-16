package workspace

// Stage 4 of docs/proposals/idle-suggestions.md: the setting.
//
// Read off the REAL settings view rather than the config struct, because the
// pane is what a user actually meets — a field that defaults to false while its
// row reports something else would be a lie in the only place it matters.

import (
	"strings"
	"testing"
)

// Off by default, and the row exists to say so. This is the guard the whole
// feature leans on: everything else on this branch is inert until the setting
// turns it on, and a default of true would spend a completion per reply for
// every user who never asked for any of it.
func TestTheNextStepSettingIsOffByDefault(t *testing.T) {
	_, s, _ := chatTestWorkspace(t, "s1")
	it := findSetting(s.settingsView(), "next_step")
	if it == nil {
		t.Fatalf("no next_step row in the settings pane: %+v", s.settingsView().Items)
	}
	if it.Type != "bool" {
		t.Fatalf("next_step is a %q; the trigger reads it as a bool", it.Type)
	}
	if it.Value != "false" {
		t.Fatalf("next_step defaults to %q — it must be off until a user asks for it", it.Value)
	}
}

// The description states the cost. The proposal asks for this specifically:
// this is the one row in the pane that spends money on terva's own initiative,
// with the user having sent nothing, so leaving them to infer it from "offer a
// suggestion" would be the wrong kind of quiet.
func TestTheNextStepSettingSaysWhatItCosts(t *testing.T) {
	_, s, _ := chatTestWorkspace(t, "s1")
	it := findSetting(s.settingsView(), "next_step")
	if it == nil {
		t.Fatal("no next_step row in the settings pane")
	}
	d := strings.ToLower(it.Description)
	if !strings.Contains(d, "model call") && !strings.Contains(d, "completion") {
		t.Fatalf("the description does not say it costs a model call: %q", it.Description)
	}
	// And what the user is agreeing to: an offer, not a message.
	if !strings.Contains(d, "nothing is sent") {
		t.Fatalf("the description does not say nothing is sent without the user: %q", it.Description)
	}
}

// The description names every key that accepts, because this row is the only
// place the feature is explained — an offer draws no hint of its own, so a key
// missing here is a key nobody finds.
//
// It drifted once already. Right joined Tab as a second accept key and this
// text still said "Tab accepts it", which the guards above could not catch:
// they pin the cost claim and the nothing-is-sent promise, and both survive a
// description that names the wrong keys.
func TestTheNextStepSettingNamesEveryAcceptKey(t *testing.T) {
	_, s, _ := chatTestWorkspace(t, "s1")
	it := findSetting(s.settingsView(), "next_step")
	if it == nil {
		t.Fatal("no next_step row in the settings pane")
	}
	d := strings.ToLower(it.Description)
	for _, key := range []string{"tab", "right arrow"} {
		if !strings.Contains(d, key) {
			t.Errorf("the description does not name %q as an accept key: %q", key, it.Description)
		}
	}
}
