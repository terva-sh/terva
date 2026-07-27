package worker

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// AllowSpawn is the single gate every foreign spawn consults — the swarm_spawn
// tool (through workspace's allowWorkerBackend), the board's tasks-surface
// spawn, and the TUI's /swarm. Both of its conditions matter and they fail for
// different reasons: external workers off is a POLICY answer, an unregistered
// name is a FACT answer, and a caller that conflates them tells the user to
// flip a knob that would not have helped.
//
// This lives here rather than beside one initiator because the gate's whole
// point is that the policy cannot drift between them. It also replaces the
// coverage that used to ride TestRunSwarmNewForeignBackendGated: that test
// asserted the TUI's own copy of the check, which existed only on the direct
// driver's local spawn path and went away with it. The gate did not move — the
// TUI now issues a tasks-surface action and the daemon gates it — but the only
// test exercising it did, and a gate nothing asserts is one nobody notices
// losing.
func TestAllowSpawnRefusesWhenExternalWorkersAreOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	err := AllowSpawn("claude-code")
	if err == nil {
		t.Fatal("a foreign spawn must be refused while external workers are off")
	}
	if !strings.Contains(err.Error(), "external_workers") {
		t.Errorf("the refusal must name the knob that would change the answer; got %q", err)
	}
}

func TestAllowSpawnRefusesAnUnregisteredBackend(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	err := AllowSpawn("nonesuch-backend")
	if err == nil {
		t.Fatal("an unregistered backend must be refused")
	}
	// With external workers off, the policy answer comes first and is the
	// honest one: enabling the knob still would not make this name work, but
	// the caller has not yet earned the more specific reason.
	if !strings.Contains(err.Error(), "external_workers") && !strings.Contains(err.Error(), "nonesuch-backend") {
		t.Errorf("the refusal must name either the knob or the backend; got %q", err)
	}
}
