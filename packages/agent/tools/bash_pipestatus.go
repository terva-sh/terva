package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// transparentFilters are the commands whose own exit status is never the
// question anyone meant to ask. Each one reads its input, writes it back in
// another shape, and succeeds. A non-zero status from one of them means the
// process could not run at all. It never means that the work before the pipe
// failed.
//
// This set is what keeps checkPipeStatusRead narrow by construction. It is the
// same move matchExitCommands makes for the post-hoc hint, in the other
// direction. A pipeline that ends in a genuine test is absent on purpose. In
// `... | grep -q foo` the status of grep IS the subject, and a refusal there
// would be wrong.
var transparentFilters = map[string]bool{
	"tail":   true,
	"head":   true,
	"sed":    true,
	"cat":    true,
	"tee":    true,
	"wc":     true,
	"sort":   true,
	"uniq":   true,
	"tr":     true,
	"jq":     true,
	"column": true,
}

// shellStatement is one statement of a command, split into its pipeline
// stages. A statement with more than one stage is a pipeline, and the shell
// reports the status of its final stage.
type shellStatement struct {
	stages []string
}

// splitShellStatements breaks cmd into statements and each statement into its
// pipeline stages. It is quote-aware, so a `;` or a `|` inside an argument does
// not split anything.
//
// The separators are `;`, a newline, `&&`, `||`, and a background `&`. A `>&`
// or `<&` redirection keeps its `&`, because that ampersand joins two file
// descriptors and starts nothing.
//
// Both the pipe guard and lastPipelineCommand read the result. One scanner
// serves both, so the guard cannot come to a different reading of a command
// than the hint that explains its exit code.
func splitShellStatements(cmd string) []shellStatement {
	var (
		stmts  []shellStatement
		stages []string
		seg    strings.Builder
		quote  rune
		escape bool
	)
	endStage := func() {
		if s := strings.TrimSpace(seg.String()); s != "" {
			stages = append(stages, s)
		}
		seg.Reset()
	}
	endStatement := func() {
		endStage()
		if len(stages) > 0 {
			stmts = append(stmts, shellStatement{stages: stages})
		}
		stages = nil
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case escape:
			escape = false
			seg.WriteRune(r)
		case r == '\\' && quote != '\'':
			escape = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
			seg.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			seg.WriteRune(r)
		case r == ';' || r == '\n':
			endStatement()
		case r == '&':
			if i+1 < len(rs) && rs[i+1] == '&' {
				i++
				endStatement()
				continue
			}
			if endsInRedirection(seg.String()) {
				seg.WriteRune(r)
				continue
			}
			endStatement()
		case r == '|':
			if i+1 < len(rs) && rs[i+1] == '|' {
				i++
				endStatement()
				continue
			}
			endStage()
		default:
			seg.WriteRune(r)
		}
	}
	endStatement()
	return stmts
}

// endsInRedirection reports whether seg stops at the `>` or `<` of a `2>&1`
// style redirection. The scanner then keeps the `&` that follows.
func endsInRedirection(seg string) bool {
	t := strings.TrimRight(seg, "0123456789")
	return strings.HasSuffix(t, ">") || strings.HasSuffix(t, "<")
}

// commandWord returns the command word of one pipeline stage. It skips a
// leading environment assignment, so `FOO=bar grep x` gives grep, and it
// reduces a path to its base, so `/usr/bin/grep` also gives grep.
func commandWord(stage string) string {
	for _, f := range strings.Fields(stage) {
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "=") {
			continue
		}
		return filepath.Base(strings.Trim(f, `"'`))
	}
	return ""
}

// readsExitStatus reports whether any stage of st reads `$?`.
func readsExitStatus(st shellStatement) bool {
	for _, s := range st.stages {
		if strings.Contains(s, "$?") {
			return true
		}
	}
	return false
}

// checkPipeStatusRead refuses a command that reads `$?` straight after a
// pipeline whose last stage only reformats text. The shell answers with the
// status of that last stage, so the command reports success while the work
// before the pipe failed.
//
// The trap survives review because a formatting pipe does not look like control
// flow. `git ticket status $id done | tail -3; echo "exit=$?"` printed exit=0
// while the command had failed on a missing field. A cosmetic `| sed` defeated
// an `if` the same way, and reported a branch as deleted that still existed. A
// check whose failure path cannot run is worse than no check, because it
// manufactures the confidence that somebody ran it for.
//
// The rule is narrow on three counts, so a refusal is always right:
//
//   - the statement must be a real pipeline, and its final stage must be a
//     transparent filter. Those commands succeed whatever the input, so their
//     status carries no information anybody wanted.
//   - the `$?` must sit in the very next statement. A later read has other
//     statements between it and the pipe, so the author means one of those.
//   - `PIPESTATUS` or `pipefail` anywhere in the command exempts it. Both make
//     the read correct, and neither has another use.
//
// A prose rule in AGENTS.md did not hold. The trap fired three times in one
// session on the day somebody documented it there, so this refuses instead.
func checkPipeStatusRead(cmd string) error {
	if strings.Contains(cmd, "PIPESTATUS") || strings.Contains(cmd, "pipefail") {
		return nil
	}
	stmts := splitShellStatements(cmd)
	for i, st := range stmts {
		if len(st.stages) < 2 || i+1 >= len(stmts) {
			continue
		}
		name := commandWord(st.stages[len(st.stages)-1])
		if !transparentFilters[name] {
			continue
		}
		if !readsExitStatus(stmts[i+1]) {
			continue
		}
		return fmt.Errorf("refused: this command reads $? straight after a pipeline that ends with %s. "+
			"The shell reports the status of %s there. It does not report the status of the command before the pipe. "+
			"%s succeeds whatever you give it, so a real failure reports as success. "+
			"Read ${PIPESTATUS[0]} for the status of the first stage. "+
			"Or put `set -o pipefail` at the front of the command. "+
			"To keep the pipe for readability only, drop the $? check instead", name, name, name)
	}
	return nil
}
