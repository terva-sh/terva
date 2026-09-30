package talkoot

import (
	"strings"
	"testing"
)

// The rows apply from the top, and the first match wins.
func TestTeamStateTakesTheFirstRowThatMatches(t *testing.T) {
	idle := Status{}
	working := Status{Working: true}
	waiting := Status{Waiting: true}
	offline := Status{Offline: true}
	byPerson := Status{Pauses: []string{pausePerson}}
	failed := Status{Pauses: []string{pauseSpend, pauseFailed}}
	cases := []struct {
		name string
		st   []Status
		want string
	}{
		{"no member", nil, TeamOffline},
		{"every member offline", []Status{offline, offline}, TeamOffline},
		{"a member waits on a person", []Status{working, waiting}, TeamNeedsYou},
		{"a member stopped on a failure", []Status{working, failed}, TeamNeedsYou},
		{"a member at work", []Status{working, byPerson, idle}, TeamBusy},
		{"every member that listens is paused", []Status{byPerson, offline, byPerson}, TeamPaused},
		{"any other mix", []Status{byPerson, idle, offline}, TeamOnline},
		{"a paused working member is paused, not busy", []Status{{Working: true, Pauses: []string{pausePerson}}}, TeamPaused},
	}
	for _, c := range cases {
		if got := TeamState(c.st); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// SetColor edits the colour alone, keeps the comments and the charter, and
// removes the key to return to the default.
func TestSetColorEditsOnlyTheColour(t *testing.T) {
	out, err := SetColor([]byte(commented), "#12A594")
	if err != nil {
		t.Fatal(err)
	}
	r := mustParse(t, string(out))
	if r.Color != "#12A594" {
		t.Fatalf("colour = %q", r.Color)
	}
	for _, want := range []string{"# The team for the lake schema.", "# leads", "\nWork from tickets.\n\nKeep this line as it is.\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the result lost %q:\n%s", want, out)
		}
	}
	again, err := SetColor(out, "#E5484D")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(again), "color:"); n != 1 {
		t.Errorf("a second colour wrote %d color keys:\n%s", n, again)
	}
	reset, err := SetColor(again, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := mustParse(t, string(reset)); r.Color != "" {
		t.Errorf("a reset left %q", r.Color)
	}
	if strings.Contains(string(reset), "color:") {
		t.Errorf("a reset left the key:\n%s", reset)
	}
	if _, err := SetColor([]byte(commented), "teal"); err == nil || !strings.Contains(err.Error(), "#RRGGBB") {
		t.Errorf("a bad colour = %v", err)
	}
}
