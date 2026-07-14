package agent

import (
	"bytes"
	"strings"
	"testing"
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
	for _, want := range []string{"uid / euid", "no-new-privs", "core dumps", "sudo -n true"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "skipped (--no-sudo)") {
		t.Errorf("--no-sudo must skip the sudo probe; got:\n%s", out)
	}
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
