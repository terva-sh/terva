package agent

import (
	"regexp"
	"testing"
)

// Binding a session has to reach three things — the agent's own identity, the
// built-in task board, and the extensions that subscribed to session_start —
// and missing one is silent in every direction. An unbound board works
// perfectly in memory and is never written; an unannounced session leaves a
// subscribing extension with one undifferentiated stream; an unadopted identity
// makes terva_status report no session.
//
// The rule was already half-written: RebindTasks' doc says "Call it wherever
// EmitSessionStart fires." A rule in a comment, enforced by nothing — and it had
// come apart in both directions. rpc announced and never rebound. ACP did
// neither. Of the three hosts that did both, two ran them in the opposite order
// to the third.
//
// build.SessionBinding is that list and build.BindSession the order. This guard
// is the other half. It reads sources — see host_census_test.go, whose walk it
// reuses — because these calls sit inside functions that open session files and
// spawn extension subprocesses.

// Matched in CALL position where a receiver is involved, so the declarations in
// core and build are not mistaken for uses.
var sessionBindStepsOwnedByTheEvent = []struct {
	pattern *regexp.Regexp
	why     string
}{
	{
		pattern: regexp.MustCompile(`\.AdoptSessionIdentity\(`),
		why: "the agent's session identity is step 1; alone it tells terva_status which transcript this is " +
			"while the board and the extensions still believe in a different session, or none",
	},
	{
		pattern: regexp.MustCompile(`\bRebindTasks\(`),
		why: "re-keying the board is the ONLY thing that keys it — an unbound store has no file name and " +
			"silently declines to persist, so a host that skips this has a task board that is never written",
	},
	{
		pattern: regexp.MustCompile(`\bEmitSessionStart\(`),
		why: "announcing the session is what cues a subscribing extension to act, so it goes LAST; a host " +
			"that emits it on its own is a host deciding for itself whether the board is loaded first",
	},
}

// bindStepHome is where these steps legitimately live: the event, and the two
// files that define the halves it calls.
var bindStepHome = map[string]string{
	"packages/agent/build/bindsession.go": "build.BindSession itself",
	"packages/agent/build/wiring.go": "defines EmitSessionStart, and WireHeadlessSessionPersist adopts the " +
		"identity as part of wiring persistence — a different question from binding, and idempotent with it",
	"packages/agent/build/lorewiring.go": "defines RebindTasks",
	"packages/core/agent.go":             "defines AdoptSessionIdentity",
}

func TestNoHostPerformsASessionBindStepOutsideTheEvent(t *testing.T) {
	var checked int
	for _, f := range census(t) {
		if _, ok := bindStepHome[f.path]; ok {
			continue
		}
		for _, code := range f.code {
			for _, step := range sessionBindStepsOwnedByTheEvent {
				if step.pattern.MatchString(code) {
					t.Errorf("%s performs a session-binding step outside build.BindSession:\n    %s\n  %s", f.path, code, step.why)
				}
			}
		}
	}
	// Vacuity: the patterns must still match the event's own body, or they have
	// gone stale and this guard is checking nothing.
	for _, f := range census(t) {
		if f.path != "packages/agent/build/bindsession.go" {
			continue
		}
		for _, code := range f.code {
			for _, step := range sessionBindStepsOwnedByTheEvent {
				if step.pattern.MatchString(code) {
					checked++
				}
			}
		}
	}
	if checked < len(sessionBindStepsOwnedByTheEvent) {
		t.Errorf("only %d of %d binding-step spellings matched inside build.BindSession itself — the patterns "+
			"have gone stale", checked, len(sessionBindStepsOwnedByTheEvent))
	}
}

// The ORDER is the substantive rule, and it is the one three hosts disagreed
// about: the board is loaded before the session is announced, because announcing
// is what cues a subscribing extension to act. EmitEvent's FIFO delivery
// guarantees a subscriber sees session_start before that session's first
// tool_call — worth nothing if the host has not finished binding by then.
//
// A source-order check, like the daemon's trust ordering, because the thing it
// protects is a race window: asserting it by running a bind and timing a
// concurrent task call would be exactly the flaky test that teaches people to
// re-run CI.
func TestTheBoardIsKeyedBeforeTheSessionIsAnnounced(t *testing.T) {
	var body []string
	for _, f := range census(t) {
		if f.path == "packages/agent/build/bindsession.go" {
			body = f.code
		}
	}
	if body == nil {
		t.Fatal("build/bindsession.go not found — the event has moved and this guard cannot read it")
	}
	rebind, emit := -1, -1
	for i, code := range body {
		if rebind < 0 && regexp.MustCompile(`\bRebindTasks\(`).MatchString(code) {
			rebind = i
		}
		if emit < 0 && regexp.MustCompile(`\bEmitSessionStart\(`).MatchString(code) {
			emit = i
		}
	}
	switch {
	case rebind < 0:
		t.Error("BindSession no longer keys the task board")
	case emit < 0:
		t.Error("BindSession no longer announces the session")
	case rebind > emit:
		t.Error("BindSession announces the session BEFORE keying the task board: an extension that answers " +
			"session_start by calling a task tool then finds a board still keyed to nothing — which, because " +
			"an unbound store silently declines to persist, means its work is accepted and never written")
	}
}

// The other direction: every host that opens a session must reach the event.
// Only a named list can fail the day one stops calling it — to a census, a host
// that never bound a session and one that quietly stopped look identical.
var sessionBindHosts = map[string]string{
	"packages/agent/cli.go":                         "print and json modes",
	"packages/agent/botcmd.go":                      "bot mode's paired-DM session",
	"packages/agent/swarm_agent.go":                 "a swarm child's own session (no task board: nil Tasks)",
	"packages/agent/rpc.go":                         "rpc --session (split: board at open, announcement after extensions start)",
	"packages/agent/acp_mode.go":                    "acp session/new and session/load",
	"packages/agent/workspace/workspace_session.go": "a daemon session (split, as rpc)",
}

func TestEverySessionHostReachesTheEvent(t *testing.T) {
	seen := map[string]bool{}
	call := regexp.MustCompile(`BindSession\(`)
	for _, f := range census(t) {
		if f.path == "packages/agent/build/bindsession.go" {
			continue
		}
		for _, code := range f.code {
			if call.MatchString(code) {
				seen[f.path] = true
			}
		}
	}
	for path, what := range sessionBindHosts {
		if !seen[path] {
			t.Errorf("%s no longer reaches build.BindSession (%s) — either it stopped opening sessions "+
				"(drop it from sessionBindHosts) or it grew its own sequence, which is how rpc came to "+
				"announce a session whose task board it never keyed", path, what)
		}
	}
	for path := range seen {
		if _, listed := sessionBindHosts[path]; !listed {
			t.Errorf("%s binds a session and is not in sessionBindHosts — add it, so that the day it "+
				"stops, something fails", path)
		}
	}
}
