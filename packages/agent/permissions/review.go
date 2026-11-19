package permissions

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The review count (decision 0029 rule 17). A model review runs on the
// forge, and the daemon cannot see what it costs. It can count the requests
// of a chain that no person began. ReviewRule reads one tool call and says
// how many review requests it holds.
//
// A review request has no built-in form. Each forge and each repository asks
// for a model review in its own way, so the person names the commands.
//
// 🔑 The rule finds a command with the reader of the merge rule, so it looks
// in each place where that reader looks: behind a runner, in a shell in a
// shell, in a variable, in what a pipe gives a shell.
//
// 🚨 A count must be exact, and a search is not. The rule counts a command
// that the shell runs one time, as its own command. For a request in any
// other place it gives no number, and the host refuses the call: a loop, a
// function, xargs, a shell in a shell, a variable that holds the command.
// The cost is one clearer command.
//
// 🚨 Like the merge rule, this rule steers a model and it is no boundary. A
// script that asks for a review passes with no count.
type ReviewRule struct {
	// Commands are the person's patterns for a command that asks for a
	// review. Each one is tried against each command of a line, with the
	// quotes removed and with no runner in front, and against the name of a
	// tool of an extension.
	Commands []*regexp.Regexp
}

// reviewUncounted is what the rule says of a call that holds a review
// request where it cannot count it.
const reviewUncounted = "terva cannot count the review requests of this command. " +
	"Ask for each review in a command of its own, with no loop, no shell and no variable around it"

// Count reads one tool call. It returns the number of review requests in
// it. For a call that holds a request where the rule cannot count it, why
// says so, and the number is 0. It is the same for a call with a line that
// the rule does not read for its size.
func (r ReviewRule) Count(tool string, args json.RawMessage) (n int, why string) {
	if len(r.Commands) == 0 {
		return 0, ""
	}
	rv := &reviews{}
	rv.m = MergeRule{Commands: r.Commands, hits: &rv.hits}
	switch {
	case tool == "bash":
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) != nil {
			// The bash tool runs nothing for arguments that it cannot read.
			return 0, ""
		}
		rv.line(a.Command)
	case IsBuiltin(tool):
		// A first-party tool asks for no review. One of them may take a
		// text under a name such as script, and that text is no shell line.
		return 0, ""
	default:
		// A tool whose own name is the request: mcp_forgejo_request_review.
		if rv.matches(tool) {
			rv.n++
		}
		var v any
		switch {
		case len(strings.TrimSpace(string(args))) == 0:
			// A call with no arguments runs nothing.
		case json.Unmarshal(args, &v) != nil:
			// 🚨 The tool's own reader may take what Go's reader refused, so
			// arguments that the rule cannot read get no pass. The merge
			// rule refuses them too.
			rv.lost = true
		default:
			rv.argument("", v)
		}
	}
	switch {
	case rv.lost:
		return 0, reviewUncounted
	case rv.long:
		return 0, tooLong
	}
	return rv.n, ""
}

// reviews is the state of one count.
type reviews struct {
	// m is the merge rule's reader with the patterns of the review rule, and
	// hits holds each text that it matched.
	m    MergeRule
	hits []string
	// n counts the requests that the rule can count. lost is set for a
	// request that it found and cannot count, and long for a line that it
	// did not read for its size.
	n    int
	lost bool
	long bool
}

// matches reports whether a pattern matches a text. It gathers no hit.
func (rv *reviews) matches(text string) bool {
	for _, re := range rv.m.Commands {
		if re != nil && re.MatchString(text) {
			return true
		}
	}
	return false
}

// found runs one read of the merge rule's reader. It returns how many texts
// the patterns matched in it, and whether the read ended in a doubt of the
// reader's own and in no match.
//
// 🚨 The reader stops where it has a doubt: a command inside too many shells,
// a command in a variable that it cannot read. A request behind such a stop
// is one that it did not look for. So a read of a whole line that ends in a
// doubt has no count. The merge rule refuses each such line, and the host
// asks that rule first.
//
// The walk of a line asks about one command at a time, with the variables
// not filled in. A doubt there is no doubt about the line: the reader of the
// whole line knows that t=echo; $t pr-merge prints.
func (rv *reviews) found(read func(l *line)) (hits int, doubt bool) {
	before := len(rv.hits)
	var l line
	read(&l)
	hits = len(rv.hits) - before
	return hits, hits == 0 && l.why != ""
}

// one reports whether a command is one review request that the shell runs
// as it stands: the reader matched one text in it, and that text is the
// command itself behind its runners.
//
// 🚨 Two matches are a request inside a request, as a shell around one is
// for a pattern with no anchor. xargs runs its command one time for each
// line of its input. Neither has a count.
func (rv *reviews) one(words []word, hits int) bool {
	bare, w := unwrap(words)
	if hits != 1 || w.fed {
		return false
	}
	texts := make([]string, len(bare))
	for i, w := range bare {
		texts[i] = w.text
	}
	return rv.matches(strings.Join(texts, " "))
}

// line counts the review requests of one command line.
func (rv *reviews) line(text string) {
	if pastBounds(text) {
		// 🚨 The parser must not get such a line, and the rule does not read
		// it word by word either. That read takes the size of the line for
		// each pattern, and the line of a tool of an extension has no bound
		// of its own. So a line past the bounds has no count, with or
		// without a request in it. The merge rule refuses each such line
		// too, and the bash tool refuses it at each host.
		rv.long = true
		return
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
	if err != nil {
		// 🚨 The shell may still run the front of a line that does not
		// parse, and the rule cannot say how much of it. The reader tries
		// each pattern at each word of such a line.
		hits, doubt := rv.found(func(l *line) { rv.m.unparsed(text, l, 0) })
		rv.lost = rv.lost || hits > 0 || doubt || rv.matches(text)
		return
	}
	from := len(rv.hits)
	var counted []*syntax.CallExpr
	// repeats counts the loops and the functions around the command that
	// the walk stands on. A command there runs any number of times.
	repeats := 0
	var open []bool
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			if open[len(open)-1] {
				repeats--
			}
			open = open[:len(open)-1]
			return true
		}
		again := false
		switch node.(type) {
		case *syntax.WhileClause, *syntax.ForClause, *syntax.FuncDecl:
			again = true
			repeats++
		}
		open = append(open, again)
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		words := wordsOf(call)
		hits, _ := rv.found(func(l *line) { rv.m.call(words, false, l, 0) })
		if repeats == 0 && rv.one(words, hits) {
			counted = append(counted, call)
		}
		return true
	})
	// 🚨 The walk reads each command as it stands in the line. A request can
	// also come from a variable, from a here document that a shell reads, or
	// from the words that a pipe gives a shell. So the whole reader reads
	// the line again with each counted command taken out, and a request
	// that it still finds is one with no count. A command that the walk
	// matched and did not count is still in the line for that read.
	//
	// 🔑 The command is taken out of the parsed line and not out of the
	// text. The place of a command in the text is not exact inside a
	// substitution in back quotes.
	for _, call := range counted {
		at := call.Args[0].Pos()
		end := syntax.NewPos(at.Offset()+1, at.Line(), at.Col()+1)
		call.Args = []*syntax.Word{{Parts: []syntax.WordPart{&syntax.Lit{ValuePos: at, ValueEnd: end, Value: ":"}}}}
	}
	rv.n += len(counted)
	if hits, doubt := rv.found(func(l *line) { rv.m.read(file, l, 0) }); hits > 0 || doubt {
		rv.lost = true
	}
	// 🚨 A pattern that matches the line as a whole and no command of it is
	// a pattern across two commands. Such a match has no count, and it must
	// not pass as a line with no request.
	for _, re := range rv.m.Commands {
		if re != nil && re.MatchString(text) && !slices.ContainsFunc(rv.hits[from:], re.MatchString) {
			rv.lost = true
		}
	}
}

// listed counts the review requests of a command that a tool takes as a
// list of words.
//
// 🚨 A list under a name such as commands can also be a list of lines. So
// a list that is not one plain request is read again with each word as a
// line. A list with a request that neither read can count has no count.
func (rv *reviews) listed(list []string) {
	words := make([]word, len(list))
	for i, text := range list {
		words[i] = word{text: text, ok: true}
	}
	hits, doubt := rv.found(func(l *line) {
		rv.m.call(words, false, l, 0)
		for _, text := range list {
			if strings.ContainsAny(text, " \t\n") {
				rv.m.nested(text, l, 0)
			}
		}
	})
	switch {
	case doubt:
		rv.lost = true
	case hits == 0:
	case rv.one(words, hits):
		rv.n++
	default:
		before := rv.n
		for _, text := range list {
			rv.line(text)
		}
		rv.lost = rv.lost || rv.n == before
	}
}

// argument counts the review requests in one value of the arguments of a
// tool of an extension or an MCP server. It reads the values that the merge
// rule reads as commands: a text or a list under a name such as command,
// and a program under one name with its words under another.
func (rv *reviews) argument(key string, v any) {
	switch v := v.(type) {
	case string:
		if commandKeys[keyName(key)] {
			rv.line(v)
		}
	case []any:
		if k := keyName(key); commandKeys[k] || k == "argv" {
			rv.listed(texts(v))
			return
		}
		for _, e := range v {
			rv.argument(key, e)
		}
	case map[string]any:
		// programs holds each command of the object as one text, under its
		// key, and words holds the words that the object gives its command.
		programs := map[string]string{}
		var words []string
		for k, e := range v {
			switch name := keyName(k); {
			case commandKeys[name]:
				switch e := e.(type) {
				case string:
					programs[k] = e
				case []any:
					programs[k] = strings.Join(texts(e), " ")
				}
			case argumentKeys[name]:
				switch e := e.(type) {
				case []any:
					words = append(words, texts(e)...)
				case string:
					words = append(words, strings.Fields(e)...)
				}
			}
		}
		if len(programs) > maxPrograms && len(words) > 0 {
			// 🚨 An object can spell one key in many ways, and each spelling
			// is one more read of every word. The merge rule refuses such a
			// call for its size.
			rv.lost = true
			return
		}
		for k, e := range v {
			prog, command := programs[k]
			if !command || len(words) == 0 {
				rv.argument(k, e)
				continue
			}
			// 🚨 A command beside words is read two times: with the words,
			// as a tool that starts a program gets it, and by itself, as a
			// tool that runs a line gets it. The rule cannot say which tool
			// this is. The two reads see one command, so they are one count
			// and not a sum. A read by itself that finds another number is
			// a line with more requests than its front shows, and such a
			// call has no count.
			before := rv.n
			rv.listed(append(strings.Fields(prog), words...))
			with := rv.n - before
			rv.n = before
			rv.argument(k, e)
			if alone := rv.n - before; alone != 0 && alone != with {
				rv.lost = true
			}
			rv.n = before + with
		}
	}
}
