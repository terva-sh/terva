package tools

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"terva.sh/terva/packages/core"
)

// maxGrantScopes caps how many distinct commands a single "always allow"
// may cover. A line compound enough to exceed it gets no scoped option —
// a dialog offering to save five rules at once is a wall of text nobody
// audits, and the blanket option is still there for whoever wants it.
const maxGrantScopes = 4

// BashGrantScopes derives the narrow "always allow" options for a bash
// command: one entry per unique command on the line, anchored on its
// normalized first token — two tokens when the second argument is a bare
// word, so subcommand CLIs scope naturally (`git status`, not all of git).
// The Pattern anchors with `(?:\s|$)` rather than `\b` so `^git status`
// cannot also match `git status.foo`.
//
// The derivation is deliberately all-or-nothing and refuses anything it
// cannot anchor honestly, returning nil (= offer no scoped option; the
// blanket and once options remain):
//
//   - a command substitution or process substitution anywhere on the line
//     — `echo $(rm x)` must not become an "always allow echo" that reads
//     as covering the line while the inner command keeps prompting;
//   - an assignment prefix (`FOO=1 git status`) — the policy matches the
//     printed scope text, which includes the prefix, and a pattern built
//     to include arbitrary env prefixes is a quoting bug farm;
//   - a command word that is not a plain literal (quoted, expanded);
//   - more than maxGrantScopes distinct commands.
//
// Function bodies are skipped: `f() { rm x; }` executes nothing at define
// time, and granting `^rm` because a definition scrolled past would be a
// grant the user never saw run.
//
// Wrappers are anchored conservatively as themselves (`sudo rm` derives
// `^sudo rm`, not `^rm`) — the wrapper is what the user read.
func BashGrantScopes(command string) []core.GrantScope {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}

	var (
		scopes []core.GrantScope
		seen   = map[string]bool{}
		ok     = true
	)
	syntax.Walk(file, func(node syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			ok = false
			return false
		case *syntax.FuncDecl:
			return false
		case *syntax.CallExpr:
			// No Args = a bare assignment statement; runs no command.
			if len(n.Args) == 0 {
				return true
			}
			if len(n.Assigns) > 0 {
				ok = false
				return false
			}
			tokens, derivable := grantTokens(n)
			if !derivable {
				ok = false
				return false
			}
			display := strings.Join(tokens, " ")
			if !seen[display] {
				seen[display] = true
				scopes = append(scopes, core.GrantScope{
					Display: display,
					Pattern: "^" + regexp.QuoteMeta(display) + `(?:\s|$)`,
				})
			}
			// Keep walking the args: a substitution hiding in them must
			// still poison the derivation.
			return true
		}
		return true
	})
	if !ok || len(scopes) == 0 || len(scopes) > maxGrantScopes {
		return nil
	}
	return scopes
}

// grantTokens returns the anchor tokens for one call: the command word,
// plus the first argument when it is a bare literal that does not look
// like a flag (`git status` → ["git","status"], `ls -la` → ["ls"]).
func grantTokens(call *syntax.CallExpr) ([]string, bool) {
	cmd, isLit := bareLit(call.Args[0])
	if !isLit || cmd == "" {
		return nil, false
	}
	tokens := []string{cmd}
	if len(call.Args) > 1 {
		if sub, isLit := bareLit(call.Args[1]); isLit && sub != "" && !strings.HasPrefix(sub, "-") {
			tokens = append(tokens, sub)
		}
	}
	return tokens, true
}

// bareLit unwraps a word that is exactly one unquoted literal part.
func bareLit(w *syntax.Word) (string, bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", false
	}
	lit, isLit := w.Parts[0].(*syntax.Lit)
	if !isLit {
		return "", false
	}
	return lit.Value, true
}
