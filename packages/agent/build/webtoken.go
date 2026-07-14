package build

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// WebTokenEnv carries the web bearer token — the daemon's --web-token and the
// attaching client's --token are the same secret.
const WebTokenEnv = "TERVA_WEB_TOKEN"

// webTokenFromEnv reads WebTokenEnv once and scrubs it from the environment.
//
// The scrub is the point. terva hands the agent's own shell tool the process
// environment (tools/bash.go: cmd.Env = os.Environ()), so a bearer token left
// sitting there is one `env` call away from the model — and from anything the
// model is talked into running. Unsetting it closes that, and self-restart still
// works: packages/relaunch captures the exec environment in init(), before main
// runs, and re-execs from that copy, so the token survives an exec it can no
// longer be read out of. relaunch.capture scrubs TERVA_RELAUNCH_* from the live
// environment for exactly this reason.
//
// The scrub closes the os.Environ() copy, not /proc/<pid>/environ: the kernel
// wrote the token's KEY=VALUE bytes to the initial process stack at execve, and
// os.Unsetenv rewrites glibc's environ[] array without touching that region, so
// a same-UID process (the agent's own shell) can still read the value out of
// /proc. Only a source that never puts the token in the environment closes that
// route — hence ResolveWebToken warns on the env path, and --web-token-file
// exists to keep the token out of process memory entirely.
//
// Reading through a sync.OnceValue keeps that safe under a second call, which
// would otherwise find the variable already gone and report no token.
var webTokenFromEnv = sync.OnceValue(func() string {
	v := strings.TrimSpace(os.Getenv(WebTokenEnv))
	if v != "" {
		os.Unsetenv(WebTokenEnv)
	}
	return v
})

// ResolveWebToken settles the bearer token for `terva web`, from — in order —
// the explicit flag, a file, or the environment.
//
// A token passed on the command line leaks. /proc/<pid>/cmdline is
// world-readable, so `ps` hands it to every other user on the box; under systemd
// it also sits in the unit file and in `systemctl show` output. The flag stays
// supported, because breaking it would be worse, but it now says so, and the two
// routes that do not leak are first-class:
//
//	--web-token-file PATH   the token never enters the environment at all —
//	                        what systemd's LoadCredential= is for
//	TERVA_WEB_TOKEN         the EnvironmentFile= route, scrubbed from os.Environ()
//	                        once read — but see webTokenFromEnv, it lingers in /proc
//
// A --web-token-file that cannot be read, or that holds nothing, is a hard error
// rather than an empty token. Returning "" there would drop the daemon into
// no-auth mode — the one outcome an operator reaching for a token file is trying
// to avoid, and one they would only discover from the absence of a log line.
//
// --web-token-require-file (off by default) is the hardening opt-in: it accepts
// the token only from --web-token-file and turns the other two sources into hard
// errors, so an operator who has committed to the file route cannot silently
// regress to the argv or /proc-visible environment paths.
func ResolveWebToken(args Args) (string, error) {
	if args.WebToken != "" {
		if args.WebTokenRequireFile {
			return "", fmt.Errorf("--web-token-require-file: refusing --web-token, whose value is in this process's command line (readable via ps and /proc/<pid>/cmdline); pass the token with --web-token-file")
		}
		fmt.Fprintf(os.Stderr, "terva web: note: --web-token puts the token in this process's command line, where any local user can read it (ps, /proc/<pid>/cmdline). Prefer %s or --web-token-file.\n", WebTokenEnv)
		return args.WebToken, nil
	}
	if path := args.WebTokenFile; path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("--web-token-file: %w", err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("--web-token-file %s: holds no token (refusing to fall back to no auth)", path)
		}
		return tok, nil
	}
	tok := webTokenFromEnv()
	if tok != "" {
		if args.WebTokenRequireFile {
			return "", fmt.Errorf("--web-token-require-file: refusing %s, whose value remains in /proc/<pid>/environ after the scrub; pass the token with --web-token-file", WebTokenEnv)
		}
		if webTokenInProcEnviron() {
			fmt.Fprintf(os.Stderr, "terva web: note: %s was scrubbed from the environment, but its value remains in /proc/<pid>/environ, where any same-UID process (including the agent's own shell) can read it. Use --web-token-file to keep the token out of process memory.\n", WebTokenEnv)
		}
	}
	return tok, nil
}

// ResolveAttachToken settles the bearer token an attaching client presents. Same
// secret, same leak, same environment fallback — `terva attach --token` is as
// readable from `ps` as the daemon's flag is.
func ResolveAttachToken(args Args) string {
	if args.Token != "" {
		return args.Token
	}
	return webTokenFromEnv()
}
