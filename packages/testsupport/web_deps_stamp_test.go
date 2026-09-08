package testsupport

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const webDeps = "../../scripts/web-deps.sh"

// The web client's node_modules has two inputs, and for a long time the gates
// watched only one of them. `ci-web-client` reinstalled when the lock file was
// newer than the tree, which covers a dependency change and nothing else. The
// machine that performed the install was invisible to it, so an install stayed
// "fresh" across a Node major upgrade for as long as nobody touched a
// dependency.
//
// Node 25 is what that costs. It changed which Web Storage globals exist at
// startup, the client's vitest suite failed 29 times, and a stale node_modules
// was ruled out early because the tree looked current. It was current, by the
// only rule anything here knew how to apply.
//
// So this asks the script the question the old rule could not: the tree is
// present, the lock file is old, and the toolchain moved. It must say stale.
func TestWebDepsStampCatchesAToolchainChange(t *testing.T) {
	requireShellAndNode(t)

	cases := []struct {
		name  string
		stamp string
		// The word the reason has to name, so a passing test means the script
		// found the field that moved rather than tripping over the fixture.
		wants string
	}{
		{
			name:  "node upgraded under an unchanged tree",
			stamp: "node v22.11.0\nnpm " + npmVersion(t) + "\nplatform " + unameS(t) + "\narch " + unameM(t) + "\n",
			wants: "node changed",
		},
		{
			name:  "moved to another architecture",
			stamp: "node " + nodeVersion(t) + "\nnpm " + npmVersion(t) + "\nplatform " + unameS(t) + "\narch mips\n",
			wants: "arch changed",
		},
		{
			name:  "installed by a bare npm ci, which leaves no stamp",
			stamp: "",
			wants: "no toolchain stamp",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := clientFixture(t, tc.stamp)

			out, err := webDepsStatus(dir)
			if err == nil {
				t.Fatalf("the script called this tree fresh: %s\n"+
					"A node_modules installed by a different toolchain is not usable, and "+
					"nothing else in the repository is watching for it. Its output was: %s",
					tc.name, out)
			}
			if !strings.Contains(out, tc.wants) {
				t.Errorf("the reason does not name what moved.\nwant it to contain: %q\ngot: %s", tc.wants, out)
			}
		})
	}
}

// The companion case. A guard that calls everything stale reinstalls on every
// `just ci`, which is the failure that gets a guard deleted rather than fixed.
func TestWebDepsLeavesAMatchingTreeAlone(t *testing.T) {
	requireShellAndNode(t)

	dir := clientFixture(t, "node "+nodeVersion(t)+"\nnpm "+npmVersion(t)+
		"\nplatform "+unameS(t)+"\narch "+unameM(t)+"\n")

	out, err := webDepsStatus(dir)
	if err != nil {
		t.Fatalf("the script wants to reinstall a tree its own stamp matches: %s\n"+
			"Every `just ci` on an untouched checkout would pay for an install.", out)
	}
	if !strings.Contains(out, "fresh") {
		t.Errorf("expected the script to report the tree fresh, got: %s", out)
	}
}

// And the wiring. The check above proves the script is right, not that any gate
// asks it. `ci-web-client` is the recipe that reuses an existing node_modules,
// so it is the one that has to route through the shared rule; every other web
// recipe reinstalls unconditionally and cannot be fooled.
//
// recipeBlock drops the comments, so the prose above the recipe explaining what
// web-deps.sh is for does not satisfy this.
func TestCIWebClientAsksTheSharedFreshnessCheck(t *testing.T) {
	justfile, err := os.ReadFile("../../justfile")
	if err != nil {
		t.Fatal(err)
	}

	body := recipeBlock(t, string(justfile), "ci-web-client")
	if strings.Contains(body, "web-deps.sh ensure") {
		return
	}
	t.Errorf("`just ci-web-client` no longer runs 'web-deps.sh ensure'.\n"+
		"It reuses whatever node_modules it finds, so something has to decide whether that "+
		"tree is still usable. An inline test on the lock file is the rule this replaced: it "+
		"cannot see a Node upgrade, and it cost a day of chasing a phantom vitest failure.\n"+
		"The recipe body was:\n%s", body)
}

// clientFixture builds a plausible client directory: a lock file, a
// node_modules newer than it, and the stamp the caller asked for. An empty
// stamp means none is written.
func clientFixture(t *testing.T, stamp string) string {
	t.Helper()

	dir := TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	if stamp != "" {
		if err := os.WriteFile(filepath.Join(modules, ".terva-deps-stamp"), []byte(stamp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// webDepsStatus runs the read-only mode. It installs nothing, so this suite
// never reaches the network. A non-nil error means the script called the tree
// stale, which it reports as exit 3.
//
// The fixture arrives as an absolute path, so the script's own default client
// directory never applies and the working directory does not matter.
func webDepsStatus(dir string) (string, error) {
	out, err := exec.Command("sh", webDeps, "status", "--dir", dir).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// requireShellAndNode skips where the script cannot run at all.
//
// This does mean the check is a developer-machine one. The Go jobs run in a
// golang-alpine container with no Node, and the web-client job that has Node
// runs no Go tests. That is acceptable here, because the thing being guarded is
// a developer's long-lived node_modules. A CI container installs a fresh one
// every run and has nothing to go stale.
func requireShellAndNode(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("web-deps.sh is a POSIX shell script")
	}
	for _, bin := range []string{"sh", "node", "npm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not on PATH, so the freshness check cannot run here", bin)
		}
	}
}

func nodeVersion(t *testing.T) string { return commandOutput(t, "node", "--version") }
func npmVersion(t *testing.T) string  { return commandOutput(t, "npm", "--version") }
func unameS(t *testing.T) string      { return commandOutput(t, "uname", "-s") }
func unameM(t *testing.T) string      { return commandOutput(t, "uname", "-m") }

func commandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()

	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return strings.TrimSpace(string(out))
}
