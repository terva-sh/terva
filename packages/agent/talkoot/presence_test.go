package talkoot

import "testing"

// The first match wins among offline, paused, waiting, and working, and idle
// is the rest. A worker stopped for idleness is idle.
func TestPresenceTakesTheFirstStateThatMatches(t *testing.T) {
	failed := []string{pauseFailed}
	for _, c := range []struct {
		name string
		s    Status
		want string
	}{
		{"nothing", Status{}, PresenceIdle},
		{"a worker stopped for idleness", Status{Idle: true}, PresenceIdle},
		{"a turn", Status{Working: true}, PresenceWorking},
		{"a question in a turn", Status{Working: true, Waiting: true}, PresenceWaiting},
		{"a pause over a question", Status{Waiting: true, Pauses: failed}, PresencePaused},
		{"no listener over everything", Status{Offline: true, Working: true, Waiting: true, Pauses: failed}, PresenceOffline},
	} {
		if got := c.s.Presence(); got != c.want {
			t.Errorf("%s: presence %s, want %s", c.name, got, c.want)
		}
	}
}
