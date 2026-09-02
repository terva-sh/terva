package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/secrets"
)

// runDoctorCommand dispatches `terva doctor`: a read-only report of this
// process's effective privilege and deployment posture (uid/euid, no_new_privs,
// core-dump limit, passwordless sudo, config-root permissions). Returns
// (handled=true, err) when rawArgs
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
(no_new_privs), whether core dumps are disabled, whether passwordless sudo is
available (a 'sudo -n true' probe), and whether the config/credential root is
owner-only (with a chmod repair when it is group/other-accessible). It also
reports how many skills this directory loads and which ones lost their name to
a same-named skill in a higher-priority directory. It reads only process,
directory, and SKILL.md metadata and never prints a secret value; the sudo
probe runs a no-op command and discards its output. Linux-only facts report
"unknown" on other platforms.`))
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
	reportConfigPerms(w)
	reportSecretsPosture(w)
	reportSkillCollisions(w)
	reportAlwaysOnSkills(w)
	return nil
}

// reportSkillCollisions reports how many skills this directory would load and,
// crucially, which ones lost their bare name to a higher tier. A collision is
// otherwise invisible: the loser simply never appears in the model's manifest,
// and the only symptom is a skill quietly behaving like a different one. That
// makes it exactly the class of fact `doctor` exists to surface.
//
// Read-only, and it resolves against the SAME trust verdict a session here
// would: reporting skills an untrusted workspace never loads would be a lie in
// the reassuring direction.
func reportSkillCollisions(w io.Writer) {
	cwd, err := os.Getwd()
	if err != nil {
		doctorLine(w, "skills", "unknown — "+err.Error())
		return
	}
	userHome, _ := os.UserHomeDir()
	trusted := permissions.ResolveTrustState(cwd, false).IsTrusted()
	found, errs := skills.Discover(config.TervaHome(), cwd, userHome, true, true,
		skills.Gate{TrustProject: trusted, Disabled: config.ResolveConfig(cwd, trusted).Config.DisableExtensions})

	summary := fmt.Sprintf("%d active", len(found))
	if !trusted {
		summary += " (workspace untrusted — project skills not loaded)"
	}
	if len(errs) > 0 {
		summary += fmt.Sprintf(" · %d unreadable", len(errs))
	}
	collisions := skills.Collisions(found)
	if len(collisions) == 0 {
		doctorLine(w, "skills", summary+" · no name collisions")
		return
	}
	doctorLine(w, "skills", fmt.Sprintf("%s · %d name collision(s)", summary, len(collisions)))
	for _, winner := range collisions {
		for _, loser := range winner.Shadowed {
			doctorLine(w, "", fmt.Sprintf("%q resolves to %s; %s is shadowed (load it as %s)",
				winner.Name, winner.Qualified(), loser.Source, loser.Qualified()))
		}
	}
}

// reportAlwaysOnSkills reports which skill BODIES this directory pins into the
// system prompt.
//
// Every other symptom of getting this wrong is silence. A pinned skill leaves
// the manifest, so a mistyped name does not fall back to on-demand loading with
// a warning: the skill simply stops being present, and the only evidence is
// prose that slowly drifts. A refused name is worse, because the operator asked
// for something terva declined on trust grounds and nothing said so.
//
// It resolves against the same trust verdict a session here would, and it
// reports the config-level answer. The two per-run flags are not visible from
// another process.
func reportAlwaysOnSkills(w io.Writer) {
	cwd, err := os.Getwd()
	if err != nil {
		doctorLine(w, "always-on", "unknown — "+err.Error())
		return
	}
	userHome, _ := os.UserHomeDir()
	trusted := permissions.ResolveTrustState(cwd, false).IsTrusted()
	eff := config.ResolveConfig(cwd, trusted)

	source := "shipped default"
	if eff.Config.AlwaysOnSkills != nil {
		source = "always_on_skills in config.json"
	}
	names := skills.AlwaysOnNames(eff.Config.AlwaysOnSkills, nil, false)
	if len(names) == 0 {
		doctorLine(w, "always-on", "none pinned ("+source+")")
		return
	}

	found, _ := skills.Discover(config.TervaHome(), cwd, userHome, true, true,
		skills.Gate{TrustProject: trusted, Disabled: eff.Config.DisableExtensions})
	set := skills.ResolveAlwaysOn(found, names)

	var chars int
	for _, s := range set.Skills {
		chars += len(s.Body)
	}
	doctorLine(w, "always-on", fmt.Sprintf("%d pinned (%s) · %d characters in every request",
		len(set.Skills), source, chars))
	for _, s := range set.Skills {
		doctorLine(w, "", fmt.Sprintf("%q pinned from %s", s.Name, s.Source))
	}
	for _, n := range set.Refused {
		doctorLine(w, "", fmt.Sprintf("%q refused: it resolves only to a project skill, and a project cannot pin a body", n))
	}
	for _, n := range set.Missing {
		doctorLine(w, "", fmt.Sprintf("%q pins nothing: no skill of that name loads here", n))
	}
}

// reportSecretsPosture reports the at-rest encryption posture: whether a
// secrets key is configured (and its file mode), and whether auth.json is
// encrypted or still plaintext. Shape-only like everything else here — the
// sniff reads leading bytes, never a value. `terva secret status` gives the
// fuller report; this line exists so the posture shows up where operators
// already look.
func reportSecretsPosture(w io.Writer) {
	keyPath := config.SecretsKeyPath()
	_, err := config.SecretsIdentity()
	switch {
	case err == nil:
		doctorLine(w, "secrets key", keyPath+"  "+keyFileMode(keyPath))
	case errors.Is(err, secrets.ErrNoKey):
		doctorLine(w, "secrets key", "absent — secrets at rest are plaintext (see `terva secret init`)")
	default:
		doctorLine(w, "secrets key", "UNUSABLE: "+err.Error())
	}
	doctorLine(w, "auth.json", fileEncryptionState(config.AuthPath()))
	if cwd, err := os.Getwd(); err == nil {
		if ok, why := build.ConfigReadableByAgent(cwd); ok {
			doctorLine(w, "config.json", "no plaintext secrets — readable by the agent")
		} else {
			doctorLine(w, "config.json", "denied to the agent — "+why)
		}
	}
}

// reportConfigPerms diagnoses the permission posture of the directories that
// hold private state — the config root (config.json carries plaintext extension
// secrets; auth.json and the bot-token files live here too) and, when project
// scoping has split them, the credential home that auth + trust resolve against.
// It is read-only: it stats the directory mode and, when group/other-accessible,
// prints a safe explicit repair command. It never opens a file, so no secret
// value can reach the output. A missing directory reports "absent" — a fresh
// install creates it owner-only.
func reportConfigPerms(w io.Writer) {
	seen := map[string]bool{}
	report := func(label, dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		d := privfs.InspectDir(dir)
		switch {
		case !d.Exists:
			doctorLine(w, label, dir+"  absent (created owner-only on first write)")
		case d.TooOpen:
			doctorLine(w, label, fmt.Sprintf("%s  %#o  group/other-accessible — repair: %s", dir, d.Mode, d.RepairCommand()))
		default:
			doctorLine(w, label, fmt.Sprintf("%s  %#o  owner-only", dir, d.Mode))
		}
	}
	report("config root", config.TervaHome())
	report("credential dir", config.CredentialHome())
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
