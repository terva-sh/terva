package tools

import (
	"encoding/json"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

var (
	_ core.LedgerArgsRenderer = (*BashTool)(nil)
	_ core.LedgerFailureNoter = (*BashTool)(nil)
)

// LedgerArgs implements core.LedgerArgsRenderer. It renders a call's arguments
// for the executed-actions ledger and elides the shell preamble, which
// identifies nothing.
//
// A model that believes a shell's working directory carries over between calls
// re-anchors every command with `cd <cwd>`. It does not carry over — Execute
// sets cmd.Dir on every call — so that `cd` is a no-op, and so is a leading
// `set +e` (the tool runs `sh -c`, which starts with errexit already off).
// Measured on a dogfooded session: 1,090 of 1,112 `cd`s pointed at the agent's
// own cwd, and the preamble averaged 92 of the 160 characters the ledger allots
// to a call, so more than half of its identification budget was spent on bytes
// that say nothing about WHICH command ran. The clip then fell inside the
// boilerplate and the entry named no command at all.
//
// Only a `cd` to the directory the command runs in is elided; `cd /tmp/build &&
// make` changes WHERE the work happened and survives whole. Anything
// unparseable, unrecognized, or merely prefix-similar (`cd /srv/app2` against
// /srv/app) is left exactly as sent — under-eliding costs characters,
// over-eliding would misreport the action.
func (t *BashTool) LedgerArgs(args json.RawMessage) string {
	return bashLedgerArgs(args, t.effectiveCWD())
}

// LedgerFailed implements core.LedgerFailureNoter.
//
// A SHELL COMMAND IS NOT ONE ACTION. Its result is the exit status of the last
// stage, and a command that failed can have changed the workspace several times
// before it got there. Measured on a dogfooded session: 12 bash calls were
// marked failed in compaction ledgers and ALL TWELVE were composite — six of
// them `gofmt -w <file> && go test <pkg>`, where the rewrite certainly happened
// and only the test that followed failed. A note that said the failed call's
// effect did not exist told the resuming agent the same wrong thing the ledger
// exists to prevent, pointed the other way, so this one says what the tool
// actually knows: a non-zero exit, and an unknown amount of work already done.
func (t *BashTool) LedgerFailed() string {
	return i18n.T("FAILED (non-zero exit; earlier stages of the command may still have taken effect)")
}

// bashLedgerArgs is LedgerArgs against an explicit directory. An empty dir
// elides nothing, since no `cd` can be verified as a no-op against it.
func bashLedgerArgs(args json.RawMessage, dir string) string {
	raw := strings.TrimSpace(string(args))
	if dir == "" {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return raw
	}
	cmd, ok := m["command"].(string)
	if !ok {
		return raw
	}
	stripped := stripShellPreamble(cmd, dir)
	if stripped == cmd {
		return raw
	}
	m["command"] = stripped
	// Without SetEscapeHTML(false) the encoder writes `&&` as `&&`,
	// spending 12 of the 160 characters on a two-character operator. Shell
	// commands are chained with `&&`, so a handful per entry pushed the clip back
	// past the preamble elision it is paired with here. The ledger is prose the
	// model reads, never JSON anything parses, so the plain byte is strictly
	// better. A trailing newline is the encoder's, not ours.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return raw
	}
	return strings.TrimRight(buf.String(), "\n")
}

// stripShellPreamble removes every leading no-op statement from cmd. It loops
// because the two forms appear in either order and in either combination.
func stripShellPreamble(cmd, dir string) string {
	for {
		rest, cut := cutShellNoop(cmd, dir)
		if !cut {
			return cmd
		}
		cmd = rest
	}
}

// cutShellNoop strips ONE leading no-op statement together with the separator
// that ends it, reporting whether it found one.
//
// The separator is required. Without it the match was not a whole statement —
// `cd /srv/app2` shares a prefix with /srv/app and `set +export` with `set +e` —
// and stripping either would rewrite the command into something that never ran.
func cutShellNoop(cmd, dir string) (string, bool) {
	s := strings.TrimLeft(cmd, " \t\r\n")
	n := noopStatementLen(s, dir)
	if n == 0 {
		return cmd, false
	}
	rest := strings.TrimLeft(s[n:], " \t")
	switch {
	case strings.HasPrefix(rest, "&&"):
		rest = rest[2:]
	case strings.HasPrefix(rest, ";"), strings.HasPrefix(rest, "\n"):
		rest = rest[1:]
	default:
		return cmd, false
	}
	return strings.TrimLeft(rest, " \t\r\n"), true
}

// noopStatementLen returns the byte length of a leading no-op statement in s, or
// 0. A `cd` counts only when its argument is exactly dir, bare or quoted; a
// trailing slash does not match, which under-elides rather than guessing.
func noopStatementLen(s, dir string) int {
	if strings.HasPrefix(s, "set +e") {
		return len("set +e")
	}
	const cd = "cd "
	if !strings.HasPrefix(s, cd) {
		return 0
	}
	arg := strings.TrimLeft(s[len(cd):], " \t")
	off := len(s) - len(arg)
	for _, cand := range []string{dir, `"` + dir + `"`, `'` + dir + `'`} {
		if strings.HasPrefix(arg, cand) {
			return off + len(cand)
		}
	}
	return 0
}
