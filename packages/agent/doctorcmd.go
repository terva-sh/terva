package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/i18n"
)

// runDoctorCommand dispatches `terva doctor`: a read-only report of this
// process's effective privilege and deployment posture (uid/euid, no_new_privs,
// core-dump limit, passwordless sudo). Returns (handled=true, err) when rawArgs
// starts with "doctor"; otherwise (false, nil) so the router falls through to
// the flag parser. Mirrors runMigrateCommand's dispatch shape.
//
// It is deliberately privilege-diagnostic, not privilege-using: it reads only
// process metadata and runs a single `sudo -n true` capability probe whose
// output it discards. Nothing is mutated and no secret value is printed.
func runDoctorCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "doctor" {
		return false, nil
	}
	var opts doctorOptions
	for _, a := range rawArgs[1:] {
		switch a {
		case "-h", "--help", "help":
			printDoctorHelp()
			return true, nil
		case "--no-sudo":
			opts.skipSudo = true
		default:
			printDoctorHelp()
			return true, i18n.Errorf("unknown flag for `doctor`: %s", a)
		}
	}
	return true, runDoctor(os.Stdout, opts)
}

type doctorOptions struct {
	skipSudo bool // skip the `sudo -n true` capability probe
}

func printDoctorHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.doctor", `terva doctor — report this process's effective privilege and deployment posture

usage:
  terva doctor            print a read-only diagnosis and exit
  terva doctor --no-sudo  skip the sudo capability probe

Reports process identity (uid/euid), whether new privileges are blocked
(no_new_privs), whether core dumps are disabled, and whether passwordless
sudo is available (a 'sudo -n true' probe). It reads only process metadata
and never prints a secret value; the sudo probe runs a no-op command and
discards its output. Linux-only facts report "unknown" on other platforms.`))
}

// runDoctor gathers the posture facts and prints them. It never mutates
// anything and never emits a secret value. The platform-specific probes
// (noNewPrivs, coreDumpStatus) live in doctor_probe_{linux,other}.go.
func runDoctor(w io.Writer, opts doctorOptions) error {
	fmt.Fprintln(w, "terva doctor — effective privilege and deployment posture")
	fmt.Fprintln(w)
	doctorLine(w, "terva", buildinfo.Get().String())
	doctorLine(w, "os / arch", runtime.GOOS+" / "+runtime.GOARCH)
	doctorLine(w, "uid / euid", identityLine())
	doctorLine(w, "no-new-privs", noNewPrivs())
	doctorLine(w, "core dumps", coreDumpStatus())
	if opts.skipSudo {
		doctorLine(w, "sudo -n true", "skipped (--no-sudo)")
	} else {
		doctorLine(w, "sudo -n true", sudoProbe())
	}
	return nil
}

func doctorLine(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %-14s %s\n", label, value)
}

// identityLine renders real/effective uid. os.Getuid/os.Geteuid return -1 off
// POSIX (Windows), where the concept does not apply.
func identityLine() string {
	uid, euid := os.Getuid(), os.Geteuid()
	if uid < 0 || euid < 0 {
		return "n/a (not a POSIX platform)"
	}
	if uid != euid {
		return fmt.Sprintf("%d / %d  (running setuid)", uid, euid)
	}
	return fmt.Sprintf("%d / %d", uid, euid)
}

// sudoProbe runs `sudo -n true` and reports only whether it succeeded — never
// its output. The -n flag makes sudo fail immediately instead of prompting, so
// this never blocks and never reads stdin. A read-only capability check
// (AGENTS.md treats `sudo -n true` as a probe that needs no audit entry).
func sudoProbe() string {
	if _, err := exec.LookPath("sudo"); err != nil {
		return "sudo not found on PATH"
	}
	cmd := exec.Command("sudo", "-n", "true")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil // discard all streams; never surface sudo output
	if err := cmd.Run(); err != nil {
		return "unavailable (needs a password, or denied)"
	}
	return "available (passwordless)"
}
