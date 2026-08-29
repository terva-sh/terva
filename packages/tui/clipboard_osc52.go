package tui

// OSC 52: asking the TERMINAL to set the clipboard.
//
// WriteClipboardText shells out to pbcopy / wl-copy / xclip, which puts
// the text on the clipboard of the machine terva is running on. Over ssh
// that is the wrong machine — the user is looking at a terminal on their
// laptop and the copy lands on a server they will never paste from.
//
// OSC 52 goes the other way round: terva writes an escape sequence to
// its own stdout, and whatever terminal is rendering that stream puts the
// payload on ITS clipboard. It travels the ssh connection for free
// because it is just bytes in the tty.
//
// The catch is that it is unverifiable. Terminals may refuse it (iTerm2
// and others gate it behind a preference precisely because a remote host
// being able to write your clipboard is a real thing to be careful
// about), and there is no reply to read. So a caller can report that it
// asked, never that it worked.

import (
	"encoding/base64"
	"os"
	"strings"

	"terva.sh/terva/packages/envcompat"
)

// maxOSC52Bytes bounds the payload terva will try to push through OSC 52.
// Terminals cap the sequence length and differ on where; a copy that is
// silently truncated at the far end is worse than one that declines here
// and says so. 64 KiB is far above any plausible "copy that message" and
// below the tightest cap worth caring about.
const maxOSC52Bytes = 64 * 1024

// OSC52Copy returns the escape sequence asking the terminal to put s on
// the clipboard of the machine the terminal itself runs on. ok is false
// when s is too large to send, in which case the caller must not claim a
// copy happened.
func OSC52Copy(s string) (seq string, ok bool) {
	if s == "" || len(s) > maxOSC52Bytes {
		return "", false
	}
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x1b\\", true
}

// RemoteSession reports whether terva is running on the far side of an
// ssh connection, i.e. whether the local clipboard tools address a
// different machine than the one the user is sitting at.
//
// Sniffed from the environment, which is what there is: sshd sets
// SSH_CONNECTION and SSH_TTY for interactive sessions. A `sudo -i` or a
// container step can lose them, so this can answer "no" on a session
// that really is remote — hence TERVA_CLIPBOARD, which lets someone say
// so outright.
func RemoteSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CLIENT") != ""
}

// ClipboardRoute names how a copy should be attempted.
type ClipboardRoute int

const (
	// ClipboardLocal runs the platform clipboard tool (pbcopy and
	// friends), then falls back to OSC 52 if it fails or the platform
	// has no tool.
	ClipboardLocal ClipboardRoute = iota
	// ClipboardTerminal writes OSC 52 and does not touch a local tool:
	// the local tool would address the wrong machine.
	ClipboardTerminal
)

// DetectClipboardRoute picks the route for this session.
//
// TERVA_CLIPBOARD=local|terminal overrides the sniff. "terminal" is the
// escape hatch for a remote session ssh forgot to mark, and "local" for a
// terminal that refuses OSC 52 on a host where the tools do work.
func DetectClipboardRoute() ClipboardRoute {
	switch strings.ToLower(strings.TrimSpace(envcompat.Get("CLIPBOARD"))) {
	case "local", "tool", "native":
		return ClipboardLocal
	case "terminal", "osc52", "osc":
		return ClipboardTerminal
	}
	if RemoteSession() {
		return ClipboardTerminal
	}
	return ClipboardLocal
}
