package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
)

// webTokenBytes is the entropy in a generated bearer token, matching the
// `openssl rand -hex 24` the docs prescribed before terva could mint one
// itself — 192 bits, far out of reach of the online guessing the login
// throttle already bounds.
const webTokenBytes = 24

// runSecretWebToken dispatches `terva secret web-token`: mint or replace the
// bearer token that gates `terva web`.
//
// It lives under `secret` rather than under `web` because it is credential
// management, not server configuration — the same place an operator goes to
// set up encryption at rest is where they go to issue this.
func runSecretWebToken(rest []string, w io.Writer) error {
	if len(rest) == 0 {
		printWebTokenHelp()
		return nil
	}
	switch rest[0] {
	case "-h", "--help", "help":
		printWebTokenHelp()
		return nil
	case "init":
		return runWebTokenInit(w)
	case "rotate":
		return runWebTokenRotate(w)
	case "path":
		fmt.Fprintln(w, config.WebTokenPath())
		return nil
	default:
		printWebTokenHelp()
		return i18n.Errorf("unknown subcommand for `secret web-token`: %s", rest[0])
	}
}

func printWebTokenHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.secret-web-token", `terva secret web-token — issue the bearer token that gates 'terva web'

usage:
  terva secret web-token init     mint a token (refuses if one already exists)
  terva secret web-token rotate   replace the existing token
  terva secret web-token path     print where the token file lives

The token is 24 random bytes as hex, written owner-only to the credential
home. 'terva web' and 'terva attach' fall back to that file when no
--web-token-file / --token-file flag and no TERVA_WEB_TOKEN are given, so
minting one is enough to turn on authentication for the next start. An
explicit source always wins; the file is on the agent's read deny-list.

The value is printed ONCE, when it is created, and only to a terminal — pipe
the command anywhere else and you get the path instead, so a token cannot
land in a log or a transcript by accident. Read the file directly if you need
it again; rotate it if you lose it.

Rotating does not disturb a RUNNING daemon, which holds the old token in
memory until it restarts; every browser session signed in with the old token
is signed out on its next request once it does.`))
}

// runWebTokenInit mints a token, refusing to overwrite one.
func runWebTokenInit(w io.Writer) error {
	path := config.WebTokenPath()
	if _, err := os.Stat(path); err == nil {
		return i18n.Errorf("a web token already exists at %s — use `terva secret web-token rotate` to replace it", path)
	}
	return writeWebToken(w, path, "minted")
}

// runWebTokenRotate replaces the token, and says what that does and does not
// affect — the daemon keeps serving on the old one until it restarts.
func runWebTokenRotate(w io.Writer) error {
	path := config.WebTokenPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return i18n.Errorf("no web token at %s — `terva secret web-token init` mints one", path)
	}
	if err := writeWebToken(w, path, "rotated"); err != nil {
		return err
	}
	fmt.Fprintln(w, "A running daemon keeps accepting the OLD token until it restarts.")
	fmt.Fprintln(w, "Restart it to switch over; every signed-in browser session is then signed out.")
	return nil
}

// writeWebToken generates a token, stores it owner-only, and shows it only
// when a human is looking.
func writeWebToken(w io.Writer, path, verb string) error {
	b := make([]byte, webTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate web token: %w", err)
	}
	token := hex.EncodeToString(b)
	if err := privfs.MkdirAll(config.CredentialHome()); err != nil {
		return err
	}
	if err := privfs.WriteFile(path, []byte(token+"\n")); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: %s (owner-only)\n", verb, path)
	if stdoutIsTTY() {
		fmt.Fprintf(w, "token: %s\n", token)
	} else {
		// Not a terminal: this is a script, a pipe, or an agent's tool call,
		// and printing here would put a live credential somewhere it is kept.
		fmt.Fprintln(w, "(not a terminal — the value was not printed; read the file directly if you need it)")
	}
	return nil
}

// stdoutIsTTY reports whether stdout is an interactive terminal. The twin of
// stdinIsTTY, for deciding whether it is safe to show a secret.
func stdoutIsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
