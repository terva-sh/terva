package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// runSessionCommand dispatches `terva session`: the release valve for the
// cross-process session lock.
//
// It exists because the lock can outlive the reason for it. A terva that is
// stopped releases its lock through the kernel and needs nothing here, but a
// deliberate claim has no process behind it and ends only when somebody says
// so, or when it lapses. Without a command to list and drop one, a claim made
// by mistake would need a user to know the on-disk layout.
//
// Returns (handled=true, err) when rawArgs starts with "session"; otherwise
// (false, nil) so the router falls through to the flag parser. Mirrors
// runDoctorCommand's dispatch shape.
func runSessionCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "session" {
		return false, nil
	}
	args := rawArgs[1:]
	if len(args) == 0 {
		printSessionHelp()
		return true, nil
	}
	switch args[0] {
	case "-h", "--help", "help":
		printSessionHelp()
		return true, nil
	case "locks":
		return true, runSessionLocks(args[1:])
	case "lock":
		return true, runSessionLock(args[1:])
	case "unlock":
		return true, runSessionUnlock(args[1:])
	default:
		printSessionHelp()
		return true, i18n.Errorf("unknown subcommand for `session`: %s", args[0])
	}
}

func printSessionHelp() {
	fmt.Fprintln(helpOut, i18n.H("help.session", `terva session — inspect and release the locks on this directory's sessions

A session is locked while a terva writes to it. That lock is released when
that terva stops, and you do not need this command for it. A claim you make
yourself outlives the process that made it, and this is how you end one.

usage:
  terva session locks                     list this directory's sessions and who holds them
  terva session lock ID --reason "..."    claim a session so no terva opens it
  terva session unlock ID                 drop a claim

flags for lock:
  --reason TEXT   why you are holding it. Required, and shown to whoever is refused.
  --for DURATION  how long, such as 2h or 30m. The default is 12h and the cap is 168h.

A claim always expires. A machine that never comes back must not hold a
session for ever.`))
}

// sessionLockDir is this run's bucket. The lock is per transcript, and a
// transcript belongs to a working directory, so the command works where terva
// works rather than across every project.
func sessionLockDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return core.SessionsDir(config.TervaHome(), cwd), nil
}

// resolveLockTarget turns a session id into a transcript path in this
// directory's bucket, refusing a separator so an id cannot escape the bucket.
func resolveLockTarget(id string) (string, error) {
	if id == "" {
		return "", i18n.Errorf("name a session; `terva session locks` lists them")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", i18n.Errorf("%q is not a session id", id)
	}
	dir, err := sessionLockDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, strings.TrimSuffix(id, ".jsonl")+".jsonl")
	if _, err := os.Stat(p); err != nil {
		return "", i18n.Errorf("no session %q in this directory", id)
	}
	return p, nil
}

func runSessionLocks(args []string) error {
	if len(args) > 0 {
		printSessionHelp()
		return i18n.Errorf("`session locks` takes no arguments")
	}
	dir, err := sessionLockDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stdout, i18n.T("no sessions in this directory"))
			return nil
		}
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") && !strings.Contains(e.Name(), ".errors.") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(os.Stdout, i18n.T("no sessions in this directory"))
		return nil
	}
	shown := 0
	for _, n := range names {
		p := filepath.Join(dir, n)
		st, ok := core.DescribeSessionLock(p)
		if !ok && !core.SessionIsLocked(p) {
			continue // nobody holds it and nobody claimed it
		}
		shown++
		id := strings.TrimSuffix(n, ".jsonl")
		switch {
		case st.Held:
			fmt.Fprintf(os.Stdout, "%s  %s  %s\n", id, i18n.T("held"), st.Claim.Describe())
		case st.Stale:
			// Reported and not reclaimed. A listing that tidied state would
			// destroy the evidence somebody is about to read.
			fmt.Fprintf(os.Stdout, "%s  %s  %s (%s)\n", id, i18n.T("stale"), st.Claim.Describe(), st.StaleReason)
		default:
			fmt.Fprintf(os.Stdout, "%s  %s  %s\n", id, i18n.T("claimed"), st.Claim.Describe())
		}
	}
	if shown == 0 {
		fmt.Fprintln(os.Stdout, i18n.T("no session in this directory is locked"))
	}
	return nil
}

func runSessionLock(args []string) error {
	var id, reason, dur string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--reason" && i+1 < len(args):
			i++
			reason = args[i]
		case strings.HasPrefix(a, "--reason="):
			reason = strings.TrimPrefix(a, "--reason=")
		case a == "--for" && i+1 < len(args):
			i++
			dur = args[i]
		case strings.HasPrefix(a, "--for="):
			dur = strings.TrimPrefix(a, "--for=")
		case strings.HasPrefix(a, "-"):
			printSessionHelp()
			return i18n.Errorf("unknown flag for `session lock`: %s", a)
		default:
			id = a
		}
	}
	if strings.TrimSpace(reason) == "" {
		// Required, because the reason is the whole point of a claim record
		// over a bare lock: it is what the next person reads when refused.
		return i18n.Errorf("say why with --reason; whoever is refused reads it")
	}
	path, err := resolveLockTarget(id)
	if err != nil {
		return err
	}
	ttl := 0
	if dur != "" {
		d, err := time.ParseDuration(dur)
		if err != nil || d <= 0 {
			return i18n.Errorf("--for wants a duration such as 2h or 30m, not %q", dur)
		}
		ttl = int(d / time.Second)
	}
	holder := sessionClaimHolder()
	if err := core.ClaimSession(path, reason, holder, ttl); err != nil {
		return sessionLockError(err)
	}
	claim, _ := core.SessionLockClaim(path)
	fmt.Fprintf(os.Stdout, "%s\n", i18n.T("claimed %s", filepath.Base(path)))
	fmt.Fprintf(os.Stdout, "%s\n", claim.Describe())
	return nil
}

func runSessionUnlock(args []string) error {
	var id string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			printSessionHelp()
			return i18n.Errorf("unknown flag for `session unlock`: %s", a)
		}
		id = a
	}
	path, err := resolveLockTarget(id)
	if err != nil {
		return err
	}
	if err := core.UnlockSession(path); err != nil {
		return sessionLockError(err)
	}
	fmt.Fprintf(os.Stdout, "%s\n", i18n.T("released the claim on %s", filepath.Base(path)))
	return nil
}

// sessionLockError renders a refusal with the one instruction that resolves it.
// A lock refusal from `unlock` means a process still has the session, and the
// remedy is to stop that terva rather than to try harder here.
func sessionLockError(err error) error {
	if errors.Is(err, core.ErrSessionLocked) {
		return i18n.Errorf("%v\nStop that terva first, then try again.", err)
	}
	return err
}

// sessionClaimHolder names whoever made a claim, for the refusal another person
// reads. The user name is a courtesy label and never an identity: nothing
// authenticates it, and nothing checks it on release.
func sessionClaimHolder() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return "someone at pid " + strconv.Itoa(os.Getpid())
}
