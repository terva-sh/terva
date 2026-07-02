package swarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestSwarmAgentArgsBootSpec pins the immersive boot-spec passthrough: an
// Experience/Substrate/Card on the opts become --chat/--play, --substrate, and
// --card flags BEFORE the positional task; empty ones are omitted (a plain
// coding child, the historical behaviour).
func TestSwarmAgentArgsBootSpec(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
		Experience: "play", Substrate: "world:local:tavern", Card: "/cards/innkeeper.png",
		Task: "greet the travelers",
	})
	if args[len(args)-1] != "greet the travelers" {
		t.Fatalf("task should be the last positional; got %v", args)
	}
	// --play, before the task; never both experiences.
	if pi := indexOf(args, "--play"); pi < 0 || pi >= len(args)-1 {
		t.Fatalf("--play missing or after the task: %v", args)
	}
	if indexOf(args, "--chat") >= 0 {
		t.Fatalf("play must not also emit --chat: %v", args)
	}
	// --substrate <ref>, before the task.
	if si := indexOf(args, "--substrate"); si < 0 || safeAt(args, si+1) != "world:local:tavern" || si+1 >= len(args)-1 {
		t.Fatalf("--substrate world:local:tavern missing/after task: %v", args)
	}
	// --card <path>, before the task.
	if ci := indexOf(args, "--card"); ci < 0 || safeAt(args, ci+1) != "/cards/innkeeper.png" || ci+1 >= len(args)-1 {
		t.Fatalf("--card missing/after task: %v", args)
	}

	// chat experience → --chat only.
	chat := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
		Experience: "chat", Task: "t",
	})
	if indexOf(chat, "--chat") < 0 || indexOf(chat, "--play") >= 0 {
		t.Fatalf("chat experience should emit --chat only: %v", chat)
	}

	// Empty boot spec → none of the flags.
	none := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock", Task: "t",
	})
	for _, f := range []string{"--chat", "--play", "--substrate", "--card"} {
		if indexOf(none, f) >= 0 {
			t.Fatalf("empty boot spec should omit %s: %v", f, none)
		}
	}
}

// TestDefaultChildArgsResumeKeepsBootSpec closes the hop the round-trip test
// below leaves unpinned: a RESUMED agent (Resuming==true, positional task
// omitted) must still emit the boot-spec flags in its child argv — the
// passthrough's whole point is surviving a restart. A regression zeroing the
// fields inside the Resuming branch would escape both TestSwarmAgentArgsBootSpec
// (task present) and TestDefaultChildArgsResumeOmitsTask (no boot spec).
func TestDefaultChildArgsResumeKeepsBootSpec(t *testing.T) {
	a := &Agent{
		Dir: "/wt", Task: "greet the travelers", Resuming: true,
		Experience: "play", Substrate: "world:local:tavern", Card: "/cards/innkeeper.png",
	}
	args := defaultChildArgs("/terva", a, "/s.json", "/in.sock")
	if indexOf(args, "greet the travelers") >= 0 {
		t.Fatalf("resume argv must omit the positional task: %v", args)
	}
	if indexOf(args, "--play") < 0 {
		t.Fatalf("--play lost on resume: %v", args)
	}
	if si := indexOf(args, "--substrate"); si < 0 || safeAt(args, si+1) != "world:local:tavern" {
		t.Fatalf("--substrate lost on resume: %v", args)
	}
	if ci := indexOf(args, "--card"); ci < 0 || safeAt(args, ci+1) != "/cards/innkeeper.png" {
		t.Fatalf("--card lost on resume: %v", args)
	}
}

// TestSpawnReqBootSpecRoundTrip verifies the boot-spec fields flow the same
// passthrough persona/model already use: SpawnRequest → Agent → meta.json →
// Reload (a simulated restart rebuilding the detached agent).
func TestSpawnReqBootSpecRoundTrip(t *testing.T) {
	root := testsupport.TempDir(t)
	newFleet := func() *Swarm {
		return New(Config{
			Root:     root,
			RepoRoot: root,
			NewRunner: func(a *Agent) Runner {
				return RunnerFunc(func(ctx context.Context, _ Sink) error {
					<-ctx.Done()
					return ctx.Err()
				})
			},
		})
	}
	f := newFleet()
	defer f.StopAll()

	a, err := f.SpawnReq(context.Background(), SpawnRequest{
		Task:       "greet the travelers",
		Experience: "play",
		Substrate:  "world:local:tavern",
		Card:       "/cards/innkeeper.png",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if a.Experience != "play" || a.Substrate != "world:local:tavern" || a.Card != "/cards/innkeeper.png" {
		t.Fatalf("boot spec not set on the spawned agent: %+v", a)
	}

	// Persisted to meta.json on disk.
	metaBytes, err := os.ReadFile(filepath.Join(root, "agents", a.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var m agentMeta
	if err := json.Unmarshal(metaBytes, &m); err != nil {
		t.Fatal(err)
	}
	if m.Experience != "play" || m.Substrate != "world:local:tavern" || m.Card != "/cards/innkeeper.png" {
		t.Fatalf("boot spec not persisted to meta.json: %+v", m)
	}

	// Survives a Reload (restart) on a fresh fleet at the same root.
	second := newFleet()
	defer second.StopAll()
	loaded, errs := second.Reload()
	if len(errs) > 0 {
		t.Fatalf("reload errors: %v", errs)
	}
	if loaded < 1 {
		t.Fatalf("expected at least one reloaded agent, got %d", loaded)
	}
	found := second.Get(a.ID)
	if found == nil {
		t.Fatalf("reloaded agents missing %s", a.ID)
	}
	if found.Experience != "play" || found.Substrate != "world:local:tavern" || found.Card != "/cards/innkeeper.png" {
		t.Fatalf("boot spec lost across reload: %+v", found)
	}
}
