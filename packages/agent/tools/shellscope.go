package tools

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// DecomposeBashCommand splits a shell command into the sequence of
// simple commands it would run, each rendered back to source text. A
// compound line like `git diff && rm -rf /` yields ["git diff", "rm -rf
// /"]; commands nested in a pipeline, subshell, or command substitution
// (`echo $(rm x)`) are captured too, so nothing that actually executes
// escapes the list. That completeness is what the permission policy and
// the sandbox jail rely on: each command is judged on its own, so an
// allow rule scoped to one program (`git *`) cannot silently clear a
// second command on the same line, and a banned command cannot hide
// behind a harmless leading one.
//
// It returns nil — meaning "treat the whole line as one unit" — when the
// command parses to fewer than two distinct commands, or when it cannot
// be parsed at all. Both are the historical single-unit behavior, so a
// parser that chokes on exotic syntax never makes the call more
// permissive than before; callers fall back to the raw command string.
// The parser is pure Go (no CGO), keeping the CGO_ENABLED=0 build clean.
//
// 🚨 It also returns nil for a line that terva refuses to split
// (ShellUnreadable). bash runs such a line, so one unit is no safe answer
// for it. The bash tool refuses to run the line whatever a caller decided,
// and every gate refuses the call first (build.BuildToolGate). A new caller
// that judges a command asks ShellUnreadable too.
func DecomposeBashCommand(command string) []string {
	scopes, _ := splitBashCommand(command)
	if len(scopes) < 2 {
		return nil
	}
	return scopes
}

// maxScopeBytes bounds the text of the commands that one line splits into.
//
// 🚨 A command inside a substitution is a command of its own, and the command
// around it holds its text too. So a line of n commands, each one inside the
// last, splits into text of n times its length. 128 KiB of echo $(echo $(...
// gave 16,384 commands and 1 GiB of text in 29 seconds (2026-11-19, go 1.27,
// amd64). Eight lengths of the longest line let a daily line pass with its
// substitutions, and a line past them is not split.
const maxScopeBytes = 8 * MaxShellLine

// ShellUnreadable reports whether terva refuses to split a command line into
// its commands: the line is past the bounds of the parser (ShellPastBounds),
// or its commands hold each other so deep that their text is past
// maxScopeBytes.
//
// 🚨 DecomposeBashCommand gives no commands for such a line, and a caller
// reads that as one unit. bash runs the line, so one unit would hide a
// command from a rule. A caller that judges a command asks this first, and it
// refuses the line.
func ShellUnreadable(command string) bool {
	_, unreadable := splitBashCommand(command)
	return unreadable
}

// splitBashCommand returns each command of a line, and whether the line is
// one that terva refuses to split. A line that does not parse is not such a
// line: bash does not run it.
func splitBashCommand(command string) (scopes []string, unreadable bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, false
	}
	// 🚨 The parser ends the process on a line past the bounds, so such a
	// line is not parsed.
	if ShellPastBounds(command) {
		return nil, true
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, false
	}
	// The length of each command in the source, before any text is made.
	total := 0
	syntax.Walk(file, func(node syntax.Node) bool {
		if total > maxScopeBytes {
			return false
		}
		if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			total += int(call.End().Offset() - call.Pos().Offset())
		}
		return true
	})
	if total > maxScopeBytes {
		return nil, true
	}
	var (
		printer = syntax.NewPrinter(syntax.SingleLine(true))
		buf     strings.Builder
	)
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		// A CallExpr with no Args is a bare assignment prefix (FOO=bar)
		// that runs no command — it contributes no scope.
		if len(call.Args) == 0 {
			return true
		}
		buf.Reset()
		if err := printer.Print(&buf, call); err != nil {
			return true
		}
		if s := strings.TrimSpace(buf.String()); s != "" {
			scopes = append(scopes, s)
		}
		return true
	})
	return scopes, false
}
