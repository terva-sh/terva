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
func DecomposeBashCommand(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	var (
		printer = syntax.NewPrinter(syntax.SingleLine(true))
		scopes  []string
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
	if len(scopes) < 2 {
		return nil
	}
	return scopes
}
