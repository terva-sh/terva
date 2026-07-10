package agent

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A brand-new session created through the --session ENOENT branch
// (NewSessionAtPath) is as fresh as the no-flag path, so the card greeting
// must seed there too — both into the agent and into the persisted session.
// This is also the path a dispatched actor child takes (the runner passes a
// fixed --session), where the greeting is deliberately part of the actor's
// own context.
func TestOpenOrCreateSession_SeedsGreetingAtExplicitSessionPath(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "sessions", "actor.jsonl") // does not exist yet

	ag := &core.Agent{}
	s, err := openOrCreateSession(build.Args{Session: path, CWD: dir}, build.Resolved{CardGreeting: "*The door creaks.* You made it."}, ag, "test")
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected a session created at the explicit path")
	}

	assertGreeting := func(label string, msgs []provider.Message) {
		t.Helper()
		if len(msgs) != 1 || msgs[0].Role != provider.RoleAssistant {
			t.Fatalf("%s: want exactly the seeded assistant greeting, got %+v", label, msgs)
		}
	}
	assertGreeting("agent", ag.Messages())

	// Persisted too: reopening the same path sees the greeting (and, being a
	// reopen, must NOT seed a second one).
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ag2 := &core.Agent{}
	s2, err := openOrCreateSession(build.Args{Session: path, CWD: dir}, build.Resolved{CardGreeting: "*The door creaks.* You made it."}, ag2, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	assertGreeting("reopen", ag2.Messages())
}
