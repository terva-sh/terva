package workspace

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/testsupport"
)

// TestNewRunnerDispatch pins the swarm's Config.NewRunner routing: the opaque
// Backend label alone decides which runner an agent gets, and an unknown label
// fails loudly rather than silently falling back to a native child (which would
// run plausible work under the wrong identity).
func TestNewRunnerDispatch(t *testing.T) {
	repo := testsupport.TempDir(t)
	w := &Workspace{args: build.Args{CWD: repo}}

	nativeType := reflect.TypeOf(swarm.NewExecRunner(&swarm.Agent{}))

	t.Run("no backend -> native exec runner", func(t *testing.T) {
		r := w.newRunner(&swarm.Agent{ID: "n-1"})
		if reflect.TypeOf(r) != nativeType {
			t.Fatalf("empty backend should get the native runner, got %T", r)
		}
	})

	t.Run("known backend -> worker runner", func(t *testing.T) {
		r := w.newRunner(&swarm.Agent{ID: "w-1", Backend: worker.BackendClaude, Dir: repo})
		if _, ok := r.(*worker.Runner); !ok {
			t.Fatalf("a registered backend should get a worker.Runner, got %T", r)
		}
	})

	t.Run("unknown backend -> loud failure, never native", func(t *testing.T) {
		r := w.newRunner(&swarm.Agent{ID: "b-1", Backend: "no-such-backend"})
		if reflect.TypeOf(r) == nativeType {
			t.Fatal("an unknown backend must NOT fall back to a native child")
		}
		if _, ok := r.(*worker.Runner); ok {
			t.Fatal("an unknown backend must not produce a worker.Runner either")
		}
		// It carries the failure into Run, where the swarm turns it into a
		// StatusFailed agent with a diagnosable message.
		err := r.Run(context.Background(), stubSink{})
		if err == nil || !strings.Contains(err.Error(), "unknown backend") {
			t.Fatalf("failure should name the unknown backend, got %v", err)
		}
	})
}

type stubSink struct{}

func (stubSink) Activity(string)   {}
func (stubSink) Transcript(string) {}
func (stubSink) Result(string)     {}
func (stubSink) GuardNudge()       {}
