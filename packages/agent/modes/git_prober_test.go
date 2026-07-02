package modes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// fakeGit builds a gitRunner from canned per-command responses, keyed
// by the git subcommand ("rev-parse", "status", "diff").
func fakeGit(responses map[string]string, errs map[string]error) gitRunner {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		key := ""
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				key = a
				break
			}
		}
		if err, ok := errs[key]; ok {
			return "", err
		}
		return responses[key], nil
	}
}

var exit128 = errors.New("exit status 128")

func TestProbeGitCleanBranch(t *testing.T) {
	run := fakeGit(map[string]string{
		"rev-parse": "true\n",
		"status":    "# branch.oid 1234567890abcdef\n# branch.head sothr-main\n",
		"diff":      "",
	}, nil)
	info, ok, transient := probeGit(context.Background(), run, "/repo")
	if !ok || transient {
		t.Fatalf("ok=%v transient=%v, want ok", ok, transient)
	}
	want := tui.GitInfo{Present: true, Branch: "sothr-main"}
	if info != want {
		t.Fatalf("info = %+v, want %+v", info, want)
	}
}

func TestProbeGitDirtyWithStats(t *testing.T) {
	run := fakeGit(map[string]string{
		"rev-parse": "true\n",
		"status": "# branch.oid 1234567890abcdef\n# branch.head feat/x\n" +
			"1 .M N... 100644 100644 100644 aaa bbb packages/tui/view.go\n" +
			"? packages/tui/new_file.go\n",
		"diff": "480\t100\tpackages/tui/view.go\n19\t9\tpackages/tui/statusbar.go\n-\t-\tassets/logo.png\n",
	}, nil)
	info, ok, _ := probeGit(context.Background(), run, "/repo")
	if !ok {
		t.Fatal("want ok")
	}
	want := tui.GitInfo{Present: true, Branch: "feat/x", Dirty: true, Added: 499, Removed: 109}
	if info != want {
		t.Fatalf("info = %+v, want %+v", info, want)
	}
}

func TestProbeGitDetachedHead(t *testing.T) {
	run := fakeGit(map[string]string{
		"rev-parse": "true\n",
		"status":    "# branch.oid a1b2c3d4e5f60708\n# branch.head (detached)\n",
		"diff":      "",
	}, nil)
	info, ok, _ := probeGit(context.Background(), run, "/repo")
	if !ok || info.Branch != "a1b2c3d" {
		t.Fatalf("detached HEAD should render the short OID, got %+v ok=%v", info, ok)
	}
}

func TestProbeGitUnbornHeadDegradesStats(t *testing.T) {
	run := fakeGit(map[string]string{
		"rev-parse": "true\n",
		"status":    "# branch.oid (initial)\n# branch.head main\n? README.md\n",
	}, map[string]error{
		"diff": exit128, // no HEAD yet
	})
	info, ok, _ := probeGit(context.Background(), run, "/repo")
	if !ok {
		t.Fatal("unborn HEAD should still report the branch")
	}
	want := tui.GitInfo{Present: true, Branch: "main", Dirty: true}
	if info != want {
		t.Fatalf("info = %+v, want %+v", info, want)
	}
}

func TestProbeGitAbsentCases(t *testing.T) {
	cases := map[string]gitRunner{
		"not a repo": fakeGit(nil, map[string]error{"rev-parse": exit128}),
		"bare repo":  fakeGit(map[string]string{"rev-parse": "false\n"}, nil),
		"status fails": fakeGit(map[string]string{"rev-parse": "true\n"},
			map[string]error{"status": exit128}),
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			info, ok, transient := probeGit(context.Background(), run, "/somewhere")
			if ok || transient || info.Present {
				t.Fatalf("want plain absent, got info=%+v ok=%v transient=%v", info, ok, transient)
			}
		})
	}
	// Empty dir: absent without running anything.
	if _, ok, _ := probeGit(context.Background(), nil, ""); ok {
		t.Fatal("empty dir should be absent")
	}
}

// A timeout is transient: the prober must keep its previous snapshot
// rather than flapping the segment off during a slow probe.
func TestProbeGitTimeoutIsTransient(t *testing.T) {
	run := fakeGit(nil, map[string]error{"rev-parse": errGitTimeout})
	_, ok, transient := probeGit(context.Background(), run, "/repo")
	if ok || !transient {
		t.Fatalf("timeout should be transient, got ok=%v transient=%v", ok, transient)
	}

	// And mid-probe too (gate passed, status timed out).
	run = fakeGit(map[string]string{"rev-parse": "true\n"},
		map[string]error{"status": errGitTimeout})
	_, ok, transient = probeGit(context.Background(), run, "/repo")
	if ok || !transient {
		t.Fatalf("status timeout should be transient, got ok=%v transient=%v", ok, transient)
	}
}

func TestSumNumstat(t *testing.T) {
	out := "10\t2\ta.go\n-\t-\tbin.png\n0\t5\tb.go\n\nnot a numstat line\n"
	added, removed := sumNumstat(out)
	if added != 10 || removed != 7 {
		t.Fatalf("sumNumstat = +%d -%d, want +10 -7", added, removed)
	}
}
