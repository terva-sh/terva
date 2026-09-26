package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

const importBase = `# header
packages/core packages/provider wire The engine imports the wire.
packages/core packages/core/i18n pure The translator seam.
packages/core packages/agent/config io Reads the config file (TKT-X).
`

func TestImportGrowth(t *testing.T) {
	for _, tc := range []struct {
		name string
		cur  string
		want []string
	}{
		{"unchanged", importBase, nil},
		{"a removal", strings.Replace(importBase, "packages/core packages/agent/config io Reads the config file (TKT-X).\n", "", 1), nil},
		{"an edited reason", strings.Replace(importBase, "Reads the config file (TKT-X).", "Reads config; TKT-Y removes it.", 1), nil},
		{"a new pure leaf", importBase + "packages/core packages/core/compactprose pure Text only.\n", nil},
		{"a pure package that moves", strings.Replace(importBase, "packages/core/i18n pure", "packages/provider/i18n pure", 1), nil},
		{"an io package that moves", strings.Replace(importBase, "packages/agent/config io", "packages/session/config io", 1),
			[]string{"packages/core packages/session/config io was added"}},

		{"a new io import", importBase + "packages/provider packages/privfs io Writes a file.\n",
			[]string{"packages/provider packages/privfs io was added"}},
		{"a pure entry that becomes io", strings.Replace(importBase, "packages/core/i18n pure", "packages/core/i18n io", 1),
			[]string{"packages/core packages/core/i18n changed kind from pure to io"}},
		{"an io swap for another package", strings.Replace(importBase, "packages/agent/config io", "packages/agent/auth io", 1),
			[]string{"packages/core packages/agent/auth io was added"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := importGrowth(importBase, tc.cur)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("growth = %q, want %q", got, tc.want)
			}
		})
	}
}

const ioBase = "# header\npackages/core/session.go\tos.ReadFile\t2\n"

// A pure entry that becomes io is growth, even when the same change drops an io
// entry for a package of the same name. A rule that paired the two as a move
// would excuse it.
func TestAKindChangeIsNeverAMove(t *testing.T) {
	base := importBase + "packages/core packages/core/config pure Text.\n"
	cur := strings.Replace(strings.Replace(base,
		"packages/core packages/agent/config io Reads the config file (TKT-X).\n", "", 1),
		"packages/core/config pure", "packages/core/config io", 1)
	got, err := importGrowth(base, cur)
	if err != nil {
		t.Fatal(err)
	}
	want := "packages/core packages/core/config changed kind from pure to io"
	if strings.Join(got, "\n") != want {
		t.Fatalf("growth = %q, want %q", got, want)
	}
}

func TestIOGrowth(t *testing.T) {
	for _, tc := range []struct {
		name string
		cur  string
		want []string
	}{
		{"unchanged", ioBase, nil},
		{"emptied", "# header\n", nil},
		{"lowered", "packages/core/session.go\tos.ReadFile\t1\n", nil},
		{"a file that moves", "packages/session/session.go\tos.ReadFile\t2\n",
			[]string{"packages/session/session.go os.ReadFile rose from 0 to 2 references"}},
		{"a call that moves to another package's file of the same name", "packages/provider/session.go\tos.ReadFile\t2\n",
			[]string{"packages/provider/session.go os.ReadFile rose from 0 to 2 references"}},
		{"raised", "packages/core/session.go\tos.ReadFile\t3\n",
			[]string{"packages/core/session.go os.ReadFile rose from 2 to 3 references"}},
		{"a new identifier", ioBase + "packages/provider/auth.go\tos.Getenv\t1\n",
			[]string{"packages/provider/auth.go os.Getenv rose from 0 to 1 references"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ioGrowth(ioBase, tc.cur)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("growth = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMalformedBaselineIsAnError(t *testing.T) {
	if _, err := importGrowth(importBase, "packages/core packages/provider\n"); err == nil {
		t.Error("an import line without a kind was accepted")
	}
	if _, err := importGrowth(importBase, "packages/core packages/provider wire\n"); err == nil {
		t.Error("an import line without a reason was accepted")
	}
	if _, err := ioGrowth(ioBase, "packages/core/x.go os.Getenv 1\n"); err == nil {
		t.Error("an I/O line without tabs was accepted")
	}
	if _, err := ioGrowth(ioBase, "packages/core/x.go\t\t1\n"); err == nil {
		t.Error("an I/O line with an empty identifier was accepted")
	}
	if _, err := ioGrowth(ioBase, "packages/core/x.go\tos.Getenv\t-1\n"); err == nil {
		t.Error("a negative count was accepted")
	}
	// A duplicate could carry a negative or zero line that hides a raised one.
	if _, err := ioGrowth(ioBase, ioBase+"packages/core/session.go\tos.ReadFile\t0\n"); err == nil {
		t.Error("a duplicate I/O line was accepted")
	}
	if _, err := importGrowth(importBase, importBase+"packages/core packages/privfs IO Writes.\n"); err == nil {
		t.Error("an unknown kind was accepted")
	}
	// A later pure line must not hide an io line for the same import.
	if _, err := importGrowth(importBase, importBase+"packages/core packages/agent/config pure Hidden.\n"); err == nil {
		t.Error("a duplicate import line was accepted")
	}
}

// isolateGit keeps the scratch repositories' git away from the caller's. Run
// from a hook, git exports GIT_DIR and GIT_INDEX_FILE, and those would point
// every command here, and the guard's own, at the outer repository. A global
// config could add hooks or signing. The variables come back at cleanup.
func isolateGit(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// The guard end to end, in a scratch repository with a trunk and a branch: the
// cases the ticket's acceptance criteria name, and the trailer that allows
// growth on purpose.
func TestRunAgainstAMergeBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	isolateGit(t)
	importPath := filepath.FromSlash(baselines[0].path)
	ioPath := filepath.FromSlash(baselines[1].path)

	for _, tc := range []struct {
		name    string
		imports string // the import baseline on the branch
		io      string // the I/O baseline on the branch
		baseIO  string // the I/O baseline at the base; ioBase when empty
		message string
		ok      bool
		output  string
	}{
		{name: "nothing changed", imports: importBase, io: ioBase, ok: true,
			output: "no engine boundary baseline grew"},
		{name: "an io entry added", imports: importBase + "packages/core packages/privfs io Writes.\n", io: ioBase,
			output: "import_boundary_baseline.txt: packages/core packages/privfs io was added"},
		{name: "an I/O count raised", imports: importBase, io: "packages/core/session.go\tos.ReadFile\t3\n",
			output: "io_boundary_baseline.txt: packages/core/session.go os.ReadFile rose from 2 to 3"},
		{name: "entries removed and a reason edited", ok: true, io: "# header\n",
			imports: strings.Replace(strings.Replace(importBase, "packages/core packages/agent/config io Reads the config file (TKT-X).\n", "", 1), "The translator seam.", "The seam.", 1)},
		{name: "a pure package moved", ok: true, io: ioBase,
			imports: strings.Replace(importBase, "packages/core/i18n pure", "packages/provider/i18n pure", 1)},
		{name: "the I/O baseline deleted", imports: importBase, io: "\x00delete",
			output: "io_boundary_baseline.txt: the file was deleted or moved"},
		{name: "an empty I/O baseline deleted", imports: importBase, io: "\x00delete", baseIO: "\x00empty",
			output: "io_boundary_baseline.txt: the file was deleted or moved"},
		{name: "the I/O baseline retired on purpose", imports: importBase, io: "\x00delete",
			message: "retire it\n\nBoundary-Grows: TestIOBoundary moved to a lint", ok: true},
		{name: "growth with a trailer", imports: importBase + "packages/core packages/privfs io Writes.\n", io: ioBase,
			message: "grow on purpose\n\nBoundary-Grows: the audit log needs it until TKT-Z", ok: true,
			output: "the audit log needs it until TKT-Z"},
	} {
		switch tc.baseIO {
		case "":
			tc.baseIO = ioBase
		case "\x00empty":
			tc.baseIO = ""
		}
		t.Run(tc.name, func(t *testing.T) {
			dir := testsupport.TempDir(t)
			git := func(args ...string) {
				t.Helper()
				full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
				cmd := exec.Command("git", full...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			write := func(rel, text string) {
				t.Helper()
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q", "-b", "trunk")
			write(importPath, importBase)
			write(ioPath, tc.baseIO)
			git("add", ".")
			git("commit", "-q", "-m", "base")
			git("switch", "-q", "-c", "topic")
			write(importPath, tc.imports)
			if tc.io == "\x00delete" {
				if err := os.Remove(filepath.Join(dir, ioPath)); err != nil {
					t.Fatal(err)
				}
			} else {
				write(ioPath, tc.io)
			}
			msg := tc.message
			if msg == "" {
				msg = "change"
			}
			git("commit", "-q", "--allow-empty", "-a", "-m", msg)
			// Trunk drops its io entry after the branch point. A guard that
			// compared with trunk's tip would read the branch's unchanged
			// line as an io entry the branch added. The merge base does not.
			git("switch", "-q", "trunk")
			write(importPath, strings.Replace(importBase, "packages/core packages/agent/config io Reads the config file (TKT-X).\n", "", 1))
			git("add", ".")
			git("commit", "-q", "-m", "trunk moves")
			git("switch", "-q", "topic")

			var out strings.Builder
			ok, err := run(dir, "trunk", &out)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v\n%s", ok, tc.ok, out.String())
			}
			if tc.output != "" && !strings.Contains(out.String(), tc.output) {
				t.Fatalf("output lacks %q:\n%s", tc.output, out.String())
			}
			if !tc.ok && !strings.Contains(out.String(), "Boundary-Grows: <reason>") {
				t.Fatalf("a refusal does not say how to allow the growth:\n%s", out.String())
			}
		})
	}
}

func TestRunFailsWithoutAMergeBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	isolateGit(t)
	dir := testsupport.TempDir(t)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	var out strings.Builder
	if _, err := run(dir, "no-such-ref", &out); err == nil {
		t.Fatal("a missing base ref was reported as a result, not an error")
	}
}
