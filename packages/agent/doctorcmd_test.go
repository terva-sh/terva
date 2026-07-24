package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestDoctorCommandDispatch(t *testing.T) {
	if handled, _ := runDoctorCommand(nil); handled {
		t.Error("empty args must not be handled by doctor")
	}
	if handled, _ := runDoctorCommand([]string{"migrate"}); handled {
		t.Error("a non-doctor verb must not be handled by doctor")
	}
	if handled, err := runDoctorCommand([]string{"doctor", "--bogus"}); !handled || err == nil {
		t.Errorf("unknown flag: handled=%v err=%v, want handled + error", handled, err)
	}
	if handled, err := runDoctorCommand([]string{"doctor", "--help"}); !handled || err != nil {
		t.Errorf("--help: handled=%v err=%v, want handled + nil", handled, err)
	}
}

// TestDoctorReportShape checks the report renders every posture line and that
// --no-sudo skips the probe. skipSudo keeps the test from actually invoking
// sudo (which could prompt, log, or hang under CI).
func TestDoctorReportShape(t *testing.T) {
	var buf bytes.Buffer
	if err := runDoctor(&buf, doctorOptions{skipSudo: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"uid / euid", "no-new-privs", "core dumps", "sudo -n true", "config root"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "skipped (--no-sudo)") {
		t.Errorf("--no-sudo must skip the sudo probe; got:\n%s", out)
	}
}

// TestDoctorReportsConfigPerms covers the config-root permission diagnosis:
// a group/other-accessible root is reported with an explicit chmod repair, an
// owner-only root reads "owner-only", a missing root reads "absent", and — the
// safety property — a secret sitting in the root is never echoed to the output
// (doctor stats the directory, it never opens a file).
func TestDoctorReportsConfigPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-mode diagnosis does not apply on Windows")
	}
	const secret = "sk-DO-NOT-LEAK-abc123"

	runIn := func(t *testing.T, home string) string {
		t.Helper()
		t.Setenv("TERVA_HOME", home)
		var buf bytes.Buffer
		if err := runDoctor(&buf, doctorOptions{skipSudo: true}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "config root") {
			t.Errorf("output missing the config-root line; got:\n%s", out)
		}
		return out
	}

	// Group/other-accessible root: reported as such, with an explicit repair
	// naming the path, and the seeded secret must not appear.
	t.Run("too open", func(t *testing.T) {
		home := filepath.Join(testsupport.TempDir(t), "open")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"token":"`+secret+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(home, 0o775); err != nil { // pin past the test umask
			t.Fatal(err)
		}
		out := runIn(t, home)
		if !strings.Contains(out, "group/other-accessible") {
			t.Errorf("0775 root not flagged; got:\n%s", out)
		}
		if !strings.Contains(out, "chmod 700 "+home) {
			t.Errorf("output missing explicit repair %q; got:\n%s", "chmod 700 "+home, out)
		}
		if strings.Contains(out, secret) {
			t.Errorf("doctor leaked a secret value into its output; got:\n%s", out)
		}
	})

	// Owner-only root: reported private, no repair offered.
	t.Run("owner only", func(t *testing.T) {
		home := filepath.Join(testsupport.TempDir(t), "priv")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(home, 0o700); err != nil {
			t.Fatal(err)
		}
		out := runIn(t, home)
		if !strings.Contains(out, "owner-only") {
			t.Errorf("0700 root not reported owner-only; got:\n%s", out)
		}
		if strings.Contains(out, "chmod 700") {
			t.Errorf("private root must not offer a repair; got:\n%s", out)
		}
	})

	// Absent root: nothing to repair; a fresh install creates it private.
	t.Run("absent", func(t *testing.T) {
		home := filepath.Join(testsupport.TempDir(t), "nope")
		out := runIn(t, home)
		if !strings.Contains(out, "absent") {
			t.Errorf("missing root not reported absent; got:\n%s", out)
		}
	})
}

// TestDoctorIdentityLine: on a POSIX host the identity is numeric uid/euid.
func TestDoctorIdentityLine(t *testing.T) {
	got := identityLine()
	if strings.HasPrefix(got, "n/a") {
		return // non-POSIX (Windows) — the honest fallback
	}
	if !strings.Contains(got, "/") {
		t.Errorf("identityLine = %q, want a uid / euid pair", got)
	}
}
