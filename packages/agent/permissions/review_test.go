package permissions

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// reviewRule is the rule of a person who asks for a review with one recipe.
func reviewRule(patterns ...string) ReviewRule {
	if len(patterns) == 0 {
		patterns = []string{`^just pr-review\b`}
	}
	var r ReviewRule
	for _, p := range patterns {
		r.Commands = append(r.Commands, regexp.MustCompile(p))
	}
	return r
}

// A command that the shell runs one time, as its own command, has a count.
func TestAReviewRequestThatStandsAsItsOwnCommandIsCounted(t *testing.T) {
	r := reviewRule()
	for line, want := range map[string]int{
		// No request.
		"":                               0,
		"go test ./...":                  0,
		"just pr-status 12":              0,
		"just pr 12 -t 'pr-review'":      0,
		"grep -rn 'just pr-review' docs": 0,
		`git commit -m "just pr-review 12 passed"`: 0,
		`echo "just pr-review 12"`:                 0,
		"man just pr-review":                       0,
		// The reader of the whole line knows that this prints. The walk
		// reads one command with its variables not filled in, and its
		// doubt about that command is none about the line.
		"t=echo; $t pr-merge 12": 0,
		// One request, as the shell runs it.
		"just pr-review 12":                          1,
		"just pr-review 12 --panel code":             1,
		"  just   pr-review   12  ":                  1,
		`"just" pr-review 12`:                        1,
		`ju''st pr-re"vie"w 12`:                      1,
		"cd packages && just pr-review 12":           1,
		"just pr-review 12 | tee .tmp/review.log":    1,
		"just pr-review 12 > .tmp/review.log 2>&1":   1,
		"just pr-review 12 &":                        1,
		"(just pr-review 12)":                        1,
		"{ just pr-review 12; }":                     1,
		"out=$(just pr-review 12)":                   1,
		`echo "asked: $(just pr-review 12)"`:         1,
		"if just pr-review 12; then echo ok; fi":     1,
		"! just pr-review 12":                        1,
		"n=12; just pr-review $n":                    1,
		`just pr-review "$PR"`:                       1,
		"TERVA_PR_BASE=sothr-main just pr-review 12": 1,
		// Behind a runner that starts one program.
		"timeout 100 just pr-review 12":                1,
		"env TERVA_PR_BASE=x just pr-review 12":        1,
		"nohup just pr-review 12 &":                    1,
		"time just pr-review 12":                       1,
		"command just pr-review 12":                    1,
		"nice -n 5 timeout -k 5 100 just pr-review 12": 1,
		// A substitution in back quotes, and one inside one.
		"echo `just pr-review 12`":                        1,
		"echo `echo \\`just pr-review 12\\``":             1,
		"x=`just pr-review 12`; echo `just pr-review 13`": 2,
		// Behind a loop and a function that hold no request.
		"for n in 1 2; do true; done; just pr-review 12": 1,
		"while false; do :; done\njust pr-review 12":     1,
		"f() { true; }; f; just pr-review 12":            1,
		// A here document that no shell reads.
		"cat <<EOF\n$(just pr-review 12)\nEOF": 1,
		// More than one.
		"just pr-review 12 && just pr-review 13":                  2,
		"just pr-review 12; just pr-review 13\njust pr-review 14": 3,
		"just pr-review 12 || just pr-review 12":                  2,
		"just pr-review $(just pr-review 13)":                     2,
		"just pr-review 12 & just pr-review 13 & wait":            2,
	} {
		n, why := r.Count("bash", bashCall(t, line))
		if n != want || why != "" {
			t.Errorf("%q: got %d and %q, want %d", line, n, why, want)
		}
	}
}

// 🚨 A request in a place where the shell runs it any number of times, or
// where the rule cannot see how the shell runs it, has no count. The rule
// says so, and the host refuses the call.
func TestAReviewRequestThatTheRuleCannotCountIsRefused(t *testing.T) {
	r := reviewRule()
	for _, line := range []string{
		// A loop, and a function.
		"for n in 12 13 14; do just pr-review $n; done",
		"for ((i=0;i<50;i++)); do just pr-review 12; done",
		"while true; do just pr-review 12; sleep 60; done",
		"until just pr-review 12; do sleep 1; done",
		"while just pr-review 12; do sleep 1; done",
		"cat prs | while read n; do timeout 100 just pr-review $n; done",
		"select n in 12 13; do just pr-review $n; done",
		"ask() { just pr-review \"$1\"; }; ask 12; ask 13; ask 14",
		"function ask { just pr-review 12; }\nask\nask",
		"for n in 1 2; do ( just pr-review $n ) & done; wait",
		"for n in 1 2; do x=$(just pr-review $n); done",
		// A program that runs its command more than one time.
		"seq 12 14 | xargs -n1 just pr-review",
		"seq 12 14 | xargs -I{} just pr-review {}",
		"watch -n 60 just pr-review 12",
		"find . -name '*.go' -exec just pr-review 12 \\;",
		"parallel just pr-review ::: 12 13 14",
		// A shell in a shell.
		"sh -c 'just pr-review 12'",
		"bash -c 'just pr-review 12; just pr-review 13'",
		"bash -euo pipefail -c 'just pr-review 12'",
		`eval "just pr-review 12"`,
		"echo 'just pr-review 12' | sh",
		"bash <<EOF\njust pr-review 12\njust pr-review 13\nEOF",
		"bash <<< 'just pr-review 12'",
		"ssh host 'just pr-review 12'",
		"trap 'just pr-review 12' EXIT",
		// A variable that holds the command.
		`c="just pr-review"; $c 12`,
		`c="just pr-review 12"; $c; $c; $c`,
		"j=just; $j pr-review 12",
		"alias ask='just pr-review 12'; ask; ask",
		// A program that prints the command for a shell.
		"$(echo just pr-review 12)",
		"echo just pr-review 12",
		// A request in a variable that a program gets, beside one that the
		// rule can count.
		"GIT_PAGER='just pr-review 12' just pr-review 13",
		// One that the rule can count beside one that it cannot.
		"just pr-review 12; c='just pr-review'; $c 13",
		"just pr-review 12; echo 'just pr-review 13' | sh",
		"just pr-review 12; sh -c 'just pr-review 13'",
		"just pr-review 12; for n in 13 14; do just pr-review $n; done",
		// A line that does not parse. The shell may run its front.
		"just pr-review 12; (",
		"just pr-review 12\nfi",
	} {
		n, why := r.Count("bash", bashCall(t, line))
		if n != 0 || why != reviewUncounted {
			t.Errorf("%q: got %d and %q, want no count and the refusal", line, n, why)
		}
	}
	if !strings.Contains(reviewUncounted, "in a command of its own") {
		t.Errorf("the refusal must say how to ask: %q", reviewUncounted)
	}
}

// A person's pattern may stand anywhere in the command. One with no anchor
// also matches a command that only names the request, which is why the
// documentation asks for an anchor.
func TestAPatternWithNoAnchor(t *testing.T) {
	r := reviewRule(`\bpr-review\b`)
	for line, want := range map[string]int{
		"just pr-review 12":                      1,
		"./scripts/pr.sh pr-review 12":           1,
		"timeout 100 just pr-review 12":          1,
		"just pr-review 12 && just pr-review 13": 2,
		"just pr-status 12":                      0,
		// A command that only names the request counts too.
		"grep -rn pr-review docs": 1,
	} {
		n, why := r.Count("bash", bashCall(t, line))
		if n != want || why != "" {
			t.Errorf("%q: got %d and %q, want %d", line, n, why, want)
		}
	}
	// 🚨 Such a pattern matches the shell around a request and the request in
	// it. Two matches in one command are no count.
	for _, line := range []string{
		"bash -c 'just pr-review 12; just pr-review 13'",
		"eval 'just pr-review 12; just pr-review 13'",
		"for n in 12 13; do just pr-review $n; done",
		"seq 3 | xargs -n1 just pr-review",
	} {
		if n, why := r.Count("bash", bashCall(t, line)); n != 0 || why != reviewUncounted {
			t.Errorf("%q: got %d and %q, want no count and the refusal", line, n, why)
		}
	}
}

// 🚨 A pattern that matches a line as a whole and no command of it is a
// pattern across two commands. It has no count, and it must not pass as a
// line with no request.
func TestAPatternAcrossTwoCommandsHasNoCount(t *testing.T) {
	r := reviewRule(`cd repo && just ask`)
	if n, why := r.Count("bash", bashCall(t, "cd repo && just ask 12")); n != 0 || why != reviewUncounted {
		t.Errorf("got %d and %q, want no count and the refusal", n, why)
	}
	if n, why := r.Count("bash", bashCall(t, "cd repo && just test")); n != 0 || why != "" {
		t.Errorf("a line that the pattern does not match has no request, got %d and %q", n, why)
	}
	// 🚨 A request that another pattern counts in the same line does not
	// cover it.
	r = reviewRule(`^just pr-review\b`, `cd repo && just ask`)
	if n, why := r.Count("bash", bashCall(t, "just pr-review 1; cd repo && just ask 2")); n != 0 || why != reviewUncounted {
		t.Errorf("a pattern across two commands beside a request: got %d and %q, want the refusal", n, why)
	}
	if n, why := r.Count("bash", bashCall(t, "just pr-review 1; cd repo && just test")); n != 1 || why != "" {
		t.Errorf("got %d and %q, want 1", n, why)
	}
	// Nor does a request in an earlier line of a list of lines.
	lines := json.RawMessage(`{"commands":["just pr-review 1","cd repo && just ask 2"]}`)
	if n, why := r.Count("mcp_exec", lines); n != 0 || why != reviewUncounted {
		t.Errorf("a pattern across two commands in a list of lines: got %d and %q, want the refusal", n, why)
	}
}

// A rule with no pattern counts nothing, and a first-party tool asks for no
// review.
func TestWhatAsksForNoReview(t *testing.T) {
	if n, why := (ReviewRule{}).Count("bash", bashCall(t, "just pr-review 12")); n != 0 || why != "" {
		t.Errorf("a rule with no pattern must count nothing, got %d and %q", n, why)
	}
	if n, why := (ReviewRule{Commands: []*regexp.Regexp{nil}}).Count("bash", bashCall(t, "just pr-review 12")); n != 0 || why != "" {
		t.Errorf("a nil pattern matches nothing, got %d and %q", n, why)
	}
	// A nil pattern is no pattern for the name of a tool either.
	if n, why := (ReviewRule{Commands: []*regexp.Regexp{nil}}).Count("mcp_shell_run", json.RawMessage(`{"command":"just pr-review 12"}`)); n != 0 || why != "" {
		t.Errorf("a nil pattern matches no tool, got %d and %q", n, why)
	}
	r := reviewRule()
	for tool, args := range map[string]string{
		"write": `{"path":"notes.md","content":"just pr-review 12"}`,
		"edit":  `{"path":"notes.md","old":"x","new":"just pr-review 12"}`,
		"grep":  `{"pattern":"just pr-review"}`,
		// A first-party tool is not read, whatever the names of its
		// arguments are.
		"read": `{"path":"notes.md","command":"just pr-review 12"}`,
		"bash": `{"command":`,
	} {
		if n, why := r.Count(tool, json.RawMessage(args)); n != 0 || why != "" {
			t.Errorf("%s %s: got %d and %q, want no count", tool, args, n, why)
		}
	}
}

// A tool of an extension or an MCP server runs commands too, and one may
// ask for a review under its own name.
func TestAToolOfAnExtensionThatAsksForAReview(t *testing.T) {
	r := reviewRule(`^just pr-review\b`, `^mcp_forgejo_request_review$`)
	for _, tc := range []struct {
		tool, args string
		want       int
	}{
		{"mcp_shell_run", `{"command":"just pr-review 12"}`, 1},
		{"mcp_shell_run", `{"command":"just pr-review 12 && just pr-review 13"}`, 2},
		{"mcp_shell_run", `{"cmd":"go test ./..."}`, 0},
		{"mcp_shell_run", `{"note":"just pr-review 12"}`, 0},
		{"mcp_exec", `{"command":["just","pr-review","12"]}`, 1},
		{"mcp_exec", `{"argv":["timeout","100","just","pr-review","12"]}`, 1},
		{"mcp_exec", `{"command":"just","args":["pr-review","12"]}`, 1},
		{"mcp_exec", `{"command":["just"],"args":["pr-review","12"]}`, 1},
		{"mcp_exec", `{"command":"just","args":"pr-review 12"}`, 1},
		{"mcp_exec", `{"commands":["just pr-review 12","just pr-review 13"]}`, 2},
		{"mcp_exec", `{"commands":["go test ./...","just pr-review 12"]}`, 1},
		{"mcp_exec", `{"command":["just","pr-review","12 --panel code"]}`, 1},
		{"mcp_exec", `{"command":"just pr-review 12","args":["--panel","code"]}`, 1},
		// 🚨 A command beside its words is one request, as a text and as a
		// list. The rule reads it with the words and by itself, and the two
		// reads are one count.
		{"mcp_exec", `{"command":["just","pr-review","12"],"args":["--panel","code"]}`, 1},
		{"mcp_exec", `{"command":["just","pr-review"],"args":["12"]}`, 1},
		{"mcp_exec", `{"command":["just pr-review"],"args":["12"]}`, 1},
		{"mcp_exec", `{"command":"just pr-review","args":["12"]}`, 1},
		{"mcp_exec", `{"command":["sh -c"],"args":["just pr-review 12"]}`, 1},
		{"mcp_exec", `{"command":["go","test"],"args":["./..."]}`, 0},
		{"mcp_exec", `{"steps":[{"command":["just","pr-review","12"],"args":["--panel"]},{"command":["just","pr-review","13"],"args":["--panel"]}]}`, 2},
		{"mcp_exec", `{"command":[]}`, 0},
		{"mcp_exec", `{"command":["bash","-c","just pr-review 12"]}`, 1},
		{"mcp_exec", `{"steps":[{"command":"just pr-review 12"},{"command":"just pr-review 13"}]}`, 2},
		{"mcp_exec", `{"command":"just","args":["test"]}`, 0},
		{"mcp_forgejo_request_review", `{"index":12}`, 1},
		{"mcp_forgejo_request_reviews", `{"index":12}`, 0},
	} {
		n, why := r.Count(tc.tool, json.RawMessage(tc.args))
		if n != tc.want || why != "" {
			t.Errorf("%s %s: got %d and %q, want %d", tc.tool, tc.args, n, why, tc.want)
		}
	}
	for _, tc := range []struct{ tool, args string }{
		{"mcp_shell_run", `{"command":"for n in 12 13; do just pr-review $n; done"}`},
		{"mcp_exec", `{"command":["xargs","-n1","just","pr-review"]}`},
		{"mcp_exec", `{"command":["bash","-c","for n in 12 13; do just pr-review $n; done"]}`},
		{"mcp_exec", `{"command":"xargs","args":["-n1","just","pr-review"]}`},
		{"mcp_exec", `{"command":["watch","just","pr-review","12"]}`},
		// 🚨 A command beside words can be a line, or a list of lines, that
		// holds more requests than the read with the words finds. The two
		// reads differ, so the call has no count.
		{"mcp_exec", `{"commands":["just pr-review 12","just pr-review 13"],"args":["x"]}`},
		{"mcp_exec", `{"command":"just pr-review 12; just pr-review 13","args":["x"]}`},
		{"mcp_exec", `{"command":"for n in 12 13; do just pr-review $n; done","args":["x"]}`},
		// More shells than the reader reads: its doubt is no pass.
		{"mcp_exec", `{"command":["eval","eval","eval","eval","eval","just","pr-review","12"]}`},
		// 🚨 Arguments that Go's reader refuses. The tool's own reader may
		// take them.
		{"mcp_shell_run", `{"command":"just pr-review 12",}`},
		{"mcp_shell_run", `{"command":"go test",}`},
	} {
		if n, why := r.Count(tc.tool, json.RawMessage(tc.args)); n != 0 || why != reviewUncounted {
			t.Errorf("%s %s: got %d and %q, want no count and the refusal", tc.tool, tc.args, n, why)
		}
	}
	// A call with no arguments runs nothing.
	for _, args := range []string{"", "  "} {
		if n, why := r.Count("mcp_shell_run", json.RawMessage(args)); n != 0 || why != "" {
			t.Errorf("no arguments: got %d and %q, want no count", n, why)
		}
	}
	if n, why := r.Count("mcp_forgejo_request_review", nil); n != 1 || why != "" {
		t.Errorf("a tool whose name is the request counts with no arguments, got %d and %q", n, why)
	}
	// 🚨 An object can spell one key in many ways, and each spelling is one
	// more read of every word. The merge rule refuses such a call, and the
	// count does not read it.
	many := `{"command":"a","cmd":"b","script":"c","shell":"d","bash":"e","sh":"f","program":"g","executable":"h","exe":"i","args":["test"]}`
	if n, why := r.Count("mcp_exec", json.RawMessage(many)); n != 0 || why != reviewUncounted {
		t.Errorf("an object of many commands: got %d and %q, want no count and the refusal", n, why)
	}
	// Control: the same programs with no words are read each by itself.
	if n, why := r.Count("mcp_exec", json.RawMessage(strings.Replace(many, `,"args":["test"]`, "", 1))); n != 0 || why != "" {
		t.Errorf("many commands with no words: got %d and %q, want no count", n, why)
	}
}

// 🚨 The count does not give the parser a line past the bounds of the shell
// tools, and a line at the bounds is read in time. A request in a long line
// is counted or refused, and it never passes as no request.
func TestTheReviewCountOfALongLine(t *testing.T) {
	r := reviewRule()
	for name, tc := range map[string]struct {
		line string
		want int
	}{
		"past the bounds, a request in front":   {"just pr-review 12; " + strings.Repeat("(", maxLine), 0},
		"past the bounds, a request behind":     {strings.Repeat("( ", maxLine) + "just pr-review 12", 0},
		"past the bounds, in loops":             {strings.Repeat("while true; do ", 9000) + "just pr-review 1" + strings.Repeat("; done", 9000), 0},
		"groups that the parser does not read":  {strings.Repeat("(", 16000) + "just pr-review 12" + strings.Repeat(")", 16000), 0},
		"shells that the reader does not read":  {strings.Repeat("eval ", 20000) + "just pr-review 1", 0},
		"many requests":                         {strings.Repeat("just pr-review 12\n", 7000), 7000},
		"many commands":                         {strings.Repeat("true;", 25000) + "just pr-review 12", 1},
		"commands deeper than the reader reads": {strings.Repeat("echo $(", 16000) + "just pr-review 12" + strings.Repeat(")", 16000), 0},
		"commands in commands":                  {strings.Repeat("echo $(", 60) + "just pr-review 12" + strings.Repeat(")", 60), 1},
		"requests in requests":                  {strings.Repeat("just pr-review $(", 6000) + "true" + strings.Repeat(")", 6000), 6000},
		"many shells":                           {strings.Repeat("sh -c 'true'\n", 9000) + "just pr-review 12", 1},
		"names of programs":                     {strings.Repeat("echo just git sh bash eval gh;", 4000) + "just pr-review 12", 1},
		"runners":                               {strings.Repeat("timeout 1 ", 12000) + "just pr-review 1", 1},
		"many variables":                        {strings.Repeat("a=1 b=$a c=$b ", 2000) + "just pr-review $a", 1},
	} {
		start := time.Now()
		n, why := r.Count("bash", bashCall(t, tc.line))
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: the count took %v", name, took)
		}
		want := ""
		switch {
		case strings.HasPrefix(name, "past the bounds"):
			want = tooLong
		case tc.want == 0:
			want = reviewUncounted
		}
		if n != tc.want || why != want {
			t.Errorf("%s: got %d and %q, want %d and %q", name, n, why, tc.want, want)
		}
	}
	// 🚨 A line past the bounds has no count with no request in it either:
	// the rule did not read it. A tool of an extension has no bound on its
	// line, so the rule reads no such line word by word.
	long := strings.Repeat("( ", maxLine) + "just pr-status 12"
	if n, why := r.Count("bash", bashCall(t, long)); n != 0 || why != tooLong {
		t.Errorf("a long line with no request: got %d and %q, want the refusal for its size", n, why)
	}
	args := mustJSON(t, map[string]any{"steps": []any{map[string]string{"command": "just pr-review 12"}, map[string]string{"command": long}}})
	if n, why := r.Count("mcp_shell_run", args); n != 0 || why != tooLong {
		t.Errorf("a long line of a tool of an extension: got %d and %q, want the refusal for its size", n, why)
	}
	// Control: the same call with a short line counts its one request.
	if n, why := r.Count("mcp_shell_run", mustJSON(t, map[string]any{"steps": []any{map[string]string{"command": "just pr-review 12"}, map[string]string{"command": "just pr-status 12"}}})); n != 1 || why != "" {
		t.Errorf("the control call: got %d and %q, want 1", n, why)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 🚨 A line that the rule does not parse is read word by word, and a pattern
// with an anchor must still find the request behind another word. The
// reader's own doubt about a line is no pass either.
func TestALineThatTheRuleCannotReadHasNoCount(t *testing.T) {
	r := reviewRule()
	for _, line := range []string{
		"((just pr-review 12) )",
		"true; just pr-review 12; (",
		"true && ju''st pr-review 12 && fi",
		"if true; then\n\tjust pr-review 12",
		// A line of it that parses, inside more shells than the reader reads
		// and with no plain word of the request.
		"eval eval eval eval eval $'\\x6a'ust pr-review 12\nfi",
		// A line of it that parses, with the request in a variable.
		"j=just; $j pr-review 12\nfi",
		// More shells than the reader reads.
		"eval eval eval eval eval just pr-review 12",
		"sh -c \"sh -c 'sh -c \\\"sh -c \\\\\\\"sh -c true\\\\\\\"\\\"'\"",
	} {
		if n, why := r.Count("bash", bashCall(t, line)); n != 0 || why != reviewUncounted {
			t.Errorf("%q: got %d and %q, want no count and the refusal", line, n, why)
		}
	}
	// Control: a line that does not parse and holds no request.
	// A word that only ends in the request is none.
	for _, line := range []string{"true; (", "echo adjust pr-review; ("} {
		if n, why := r.Count("bash", bashCall(t, line)); n != 0 || why != "" {
			t.Errorf("%q: got %d and %q, want no word", line, n, why)
		}
	}
	// A pattern that holds a quote matches the line as it stands.
	quoted := reviewRule(`just ask 'now'`)
	if n, why := quoted.Count("bash", bashCall(t, "true; just ask 'now'; (")); n != 0 || why != reviewUncounted {
		t.Errorf("a pattern with a quote: got %d and %q, want the refusal", n, why)
	}
	// 🚨 The read word by word stays near the size of the line. A pattern
	// with no fixed front reads its whole text for each word, and without
	// the bound on that text a line of 60,000 words took more than a minute.
	slow := reviewRule(`(?i)\bpr-review\b`)
	wordByWord(t, func(command string) {
		if n, why := slow.Count("bash", bashCall(t, command)); n != 0 || why != "" {
			t.Errorf("a long line with no request: got %d and %q", n, why)
		}
	})
}
