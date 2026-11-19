package permissions

import (
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"terva.sh/terva/packages/agent/tools"
)

// The merge rule (decision 0029 rules 14 and 15). A chain that no person
// began may push its own branch and open a pull request, and it never
// merges. MergeRule reads one tool call and says whether it is a merge
// action. The host asks it only for a turn with no person at its root.
//
// A merge action is one of these:
//
//   - a forge's CLI or a script that merges a pull request: the words pr,
//     pull or mr, and then merge (gh pr merge, tea pulls merge, pr.sh merge)
//   - a recipe whose name says so: just pr-merge
//   - a forge's merge endpoint, anywhere in the command's text
//   - a git push that moves a protected branch, or one whose destination
//     the rule cannot read
//   - a tool of an extension or an MCP server whose name says merge and
//     pull request
//   - a command that a pattern of the user configuration matches
//
// ⚠️ The rule reads a command's text, so it steers a model and it is no
// boundary. A script that the member wrote, an alias or a variable that a
// file sets, a program in front that the rule does not know, or another
// interpreter gets past it. So does a program that runs a command from its
// own configuration, as a pager does, and a recipe's name that the shell
// makes from a pattern. So does a route that is no merge by name: a
// forge's API can write a file to a branch, or move a ref. The entity's
// forge account has no right to merge to trunk, and branch protection there
// is the guard that holds.
//
// 🔑 A doubt about a push reads as a merge. The rule can ask for a clearer
// command at no cost, and it cannot take a push back.
type MergeRule struct {
	// Commands are the person's own patterns for a command that merges. Each
	// one is tried against the whole line and against each command in it.
	Commands []*regexp.Regexp
	// Protected are branch patterns in the form of path.Match, beside the
	// built-in names and each remote's default branch.
	Protected []string
	// hits gathers each text that a pattern of Commands matched. ReviewRule
	// sets it, to find a command of its own patterns at each place that
	// this reader looks (review.go). It is nil for the merge rule.
	hits *[]string
}

// RepoFacts is what the rule needs from the repository of a call, to read a
// push that names no branch.
type RepoFacts struct {
	// Branch is the branch at work. It is empty for a detached HEAD, and
	// the rule then cannot read a push that names no branch.
	Branch string
	// Defaults holds the default branch of each remote.
	Defaults []string
	// PushDefault is git's push.default, empty when it is not set.
	PushDefault string
	// Upstream is the branch on the remote that Branch follows, with no
	// refs/heads/ in front.
	Upstream string
	// PushRefspecs is set when a remote has a push refspec of its own, or
	// is a mirror. A push to such a remote moves more than its words say.
	PushRefspecs bool
	// Unknown is set when the host could not read the repository. A push
	// that names no branch is then a push that the rule cannot read.
	Unknown bool
	// Root is the full path of the repository's top level. A line that
	// changes to that directory stays in the repository that the host read.
	Root string
	// AutoSetupMerge is git's branch.autoSetupMerge, empty when it is not
	// set. With always or inherit a new branch follows the branch that it
	// starts at, and the rule then does not know what it follows.
	AutoSetupMerge string
}

// builtinProtected are the branch names that are protected in every
// repository.
var builtinProtected = []string{"main", "master", "trunk"}

// howToPush ends the text of a push that the rule cannot read.
const howToPush = "Name the remote, and name the branch on both sides of a colon. Put no command that changes the directory, the branch or git's configuration on the same line. An example: git push origin HEAD:refs/heads/my-branch"

// mergeEndpoint matches the path of a merge in a forge's API: Forgejo, Gitea
// and GitHub (pulls), GitLab (merge_requests), and Bitbucket (pullrequests
// and pull-requests). The GraphQL mutation has a name of its own.
//
// GitHub also merges one branch into another with no pull request, at
// /repos/OWNER/REPO/merges.
var mergeEndpoint = regexp.MustCompile(`(?i)/(?:pulls|merge_requests|pullrequests|pull-requests)/[^/\s'"]+/merge(?:[^\w-]|$)|\brepos/[^/\s'"]+/[^/\s'"]+/merges(?:[^\w-]|$)|\bmutation\b[\s\S]*?\b(?:mergePullRequest|enablePullRequestAutoMerge|enqueuePullRequest|mergeBranch)\b`)

// A path as curl and a forge read it, after they clean it.
var (
	escapedByte   = regexp.MustCompile(`%[0-9a-fA-F]{2}`)
	emptySegments = regexp.MustCompile(`/(?:\.?/)+`)
	parentSegment = regexp.MustCompile(`/[^/\s'"]+/\.\./`)
)

// maxParents bounds the passes that remove a segment and the .. behind it.
// One pass removes every pair that stands alone, and a path needs one more
// pass for each level of pairs inside pairs.
const maxParents = 4

// namesEndpoint reports whether text holds the path of a merge in a forge's
// API.
//
// 🚨 curl and the forge clean a path before they read it. Each of
// /pulls/12/./merge, /pulls/12//merge, /pulls/12/x/../merge and
// /pulls/12/%6derge reaches the endpoint. So the rule reads the cleaned text
// too.
func namesEndpoint(text string) bool {
	if mergeEndpoint.MatchString(text) {
		return true
	}
	clean := escapedByte.ReplaceAllStringFunc(text, func(s string) string {
		b, _ := hex.DecodeString(s[1:])
		return string(b)
	})
	clean = emptySegments.ReplaceAllString(clean, "/")
	for range maxParents {
		next := parentSegment.ReplaceAllString(clean, "/")
		if next == clean {
			break
		}
		clean = next
	}
	return clean != text && mergeEndpoint.MatchString(clean)
}

// recipeWord matches a recipe or a program whose name says that it merges a
// pull request, such as pr-merge.
var recipeWord = regexp.MustCompile(`^(?:(?:pr|mr|pull|pulls|pull[-_]?request)[-_:.]?merge|merge[-_:.]?(?:pr|mr|pull[-_]?request)|auto[-_]?merge)$`)

// looseMerge and loosePush read a line that the parser refused.
var (
	looseMerge = regexp.MustCompile(`(?i)\b(?:pr|prs|pull|pulls|mr|mrs)\b[^|;&\n]{0,80}?\b(?:merge|accept)\b|\b(?:pr|mr)[-_]merge\b`)
	loosePush  = regexp.MustCompile(`(?i)\bgit\b[^|;&\n]*\bpush\b`)
)

var (
	prWords = map[string]bool{
		"pr": true, "prs": true, "pull": true, "pulls": true, "mr": true, "mrs": true,
		"pull-request": true, "pull-requests": true, "pullrequest": true, "pull_request": true,
		"merge-request": true, "merge_request": true,
	}
	// taskRunners take a recipe's name as an argument.
	taskRunners = map[string]bool{
		"just": true, "make": true, "gmake": true, "task": true, "mise": true, "rake": true,
		"npm": true, "pnpm": true, "yarn": true, "bun": true, "npx": true, "invoke": true,
	}
	// runners start the program that follows them and their own options.
	runners = map[string]bool{
		"sudo": true, "doas": true, "env": true, "command": true, "builtin": true, "exec": true,
		"nohup": true, "nice": true, "ionice": true, "time": true, "xargs": true, "setsid": true,
		"stdbuf": true, "timeout": true, "chrt": true, "taskset": true, "busybox": true,
	}
	// runnerValueOptions are the options of a runner that take the next
	// word. Without them the value of an option reads as the program.
	runnerValueOptions = map[string][]string{
		"sudo":    {"-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U", "-r", "-t", "--user", "--group", "--host", "--prompt", "--chdir", "--chroot", "--close-from", "--other-user", "--role", "--type", "--command-timeout"},
		"doas":    {"-u", "-C"},
		"env":     {"-u", "-C", "--unset", "--chdir"},
		"nice":    {"-n", "--adjustment"},
		"ionice":  {"-c", "-n", "-p", "--class", "--classdata", "--pid"},
		"timeout": {"-s", "-k", "--signal", "--kill-after"},
		"xargs":   {"-I", "-n", "-P", "-d", "-E", "-L", "-s", "-a", "--replace", "--max-args", "--max-procs", "--delimiter", "--eof", "--max-lines", "--max-chars", "--arg-file"},
		"stdbuf":  {"-i", "-o", "-e", "--input", "--output", "--error"},
		"exec":    {"-a"},
		"time":    {"-o", "-f", "--output", "--format"},
	}
	shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true}
	// forgeCLIs are the programs that the rule knows by name. With the
	// shells, git and the task runners they are where a command can start
	// behind a runner whose options the rule read wrongly.
	forgeCLIs = map[string]bool{"gh": true, "tea": true, "glab": true, "fj": true, "hub": true}
	// commandKeys are the argument names under which a tool of an extension
	// takes a command line.
	commandKeys = map[string]bool{
		"command": true, "commands": true, "cmd": true, "script": true, "shell": true, "bash": true, "sh": true,
		"program": true, "executable": true, "exe": true, "binary": true,
	}
	// gitValueOptions are git's own options that take the next word.
	gitValueOptions = map[string]bool{
		"--git-dir": true, "--work-tree": true, "--namespace": true,
		"--config-env": true, "--super-prefix": true,
	}
	// gitPlainOptions are git's own options that leave a push where it was.
	// Every other option in front of the subcommand is a doubt: git takes
	// -cpush.default=upstream and -C../other as one word each.
	gitPlainOptions = map[string]bool{
		"--no-pager": true, "-p": true, "--paginate": true, "-P": true, "--no-replace-objects": true,
		"--no-optional-locks": true, "--literal-pathspecs": true, "--glob-pathspecs": true,
		"--noglob-pathspecs": true, "--icase-pathspecs": true, "--no-lazy-fetch": true,
	}
	// toolNamePull holds the words of a tool's name that say what a merge
	// in that name is a merge of.
	toolNamePull = map[string]bool{
		"pull": true, "pulls": true, "pr": true, "prs": true, "mr": true, "mrs": true,
		"request": true, "requests": true, "pullrequest": true, "branch": true, "branches": true,
		// A merge with no object, in a tool of a forge: mcp_forgejo_merge. And
		// the two ways to merge later: auto-merge, and a merge queue.
		"github": true, "gitlab": true, "forgejo": true, "gitea": true, "bitbucket": true,
		"codeberg": true, "gerrit": true, "auto": true, "queue": true,
	}
)

// Match reads one tool call. It returns what makes the call a merge action,
// as a clause that follows "this call is refused because", or an empty text.
// facts is called at most one time, and only for a push.
func (r MergeRule) Match(tool string, args json.RawMessage, facts func() RepoFacts) string {
	// 🚨 Arguments that the rule cannot read get no pass. Go refuses JSON
	// that is nested 10,000 deep, and the tool's own reader may take it.
	// A call with no arguments runs nothing.
	unread := "terva cannot read the arguments of this call"
	if len(strings.TrimSpace(string(args))) == 0 {
		unread = ""
	}
	// 🔑 A tool of an extension can hold many commands, and each push asks
	// for the facts. The host reads them with git, so one call of Match
	// asks one time.
	if ask := facts; ask != nil {
		var f RepoFacts
		asked := false
		facts = func() RepoFacts {
			if !asked {
				f, asked = ask(), true
			}
			return f
		}
	}
	if tool == "bash" {
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) != nil {
			return unread
		}
		return r.command(a.Command, facts)
	}
	// A first-party tool merges nothing, and write or edit may hold a text
	// about a merge.
	if IsBuiltin(tool) {
		return ""
	}
	names := toolWords(tool)
	if mergeToolName(names) {
		return "the name of the tool " + tool + " says that it merges"
	}
	var v any
	if json.Unmarshal(args, &v) != nil {
		return unread
	}
	// 🔑 Such a tool runs in a directory that the host does not know. So the
	// rule reads no repository for it, and a push that names no branch is a
	// push that the rule cannot read. The default branch of the session's
	// own remote is still the best word on what is protected.
	elsewhere := func() RepoFacts {
		f := RepoFacts{Unknown: true}
		if facts != nil {
			f.Defaults = facts().Defaults
		}
		return f
	}
	if why := r.argument("", v, elsewhere); why != "" {
		return why
	}
	if pushToolName(names, v) {
		return r.pushTool(names, v, elsewhere())
	}
	return ""
}

// maxPrograms bounds the commands that the rule reads in one object of the
// arguments of a tool.
const maxPrograms = 8

// argument reads one value of the arguments of a tool of an extension or an
// MCP server. Every text is read for a merge endpoint, at any depth. A text
// under a name such as command is read as a command line.
//
// The depth has no bound here: Go's own reader refuses arguments that are
// nested 10,000 deep, and Match refuses what that reader refused.
func (r MergeRule) argument(key string, v any, facts func() RepoFacts) string {
	switch v := v.(type) {
	case string:
		if namesEndpoint(v) {
			return "it names a forge's merge endpoint"
		}
		if commandKeys[keyName(key)] {
			return r.command(v, facts)
		}
	case []any:
		// A command as a list of words: ["gh", "pr", "merge", "12"].
		if k := keyName(key); commandKeys[k] || k == "argv" {
			if why := r.listed(texts(v), facts); why != "" {
				return why
			}
		}
		for _, e := range v {
			if why := r.argument(key, e, facts); why != "" {
				return why
			}
		}
	case map[string]any:
		// A program under one name and its words under another:
		// {"command": "gh", "args": ["pr", "merge", "12"]}.
		var programs, words []string
		for k, e := range v {
			switch k := keyName(k); {
			case commandKeys[k]:
				switch e := e.(type) {
				case string:
					programs = append(programs, e)
				case []any:
					// {"command": ["gh"], "args": ["pr", "merge", "12"]}
					programs = append(programs, strings.Join(texts(e), " "))
				}
			case argumentKeys[k]:
				switch e := e.(type) {
				case []any:
					words = append(words, texts(e)...)
				case string:
					words = append(words, strings.Fields(e)...)
				}
			}
		}
		// 🚨 An object can spell one key in many ways, and each spelling
		// is one more read of every word.
		if len(programs) > maxPrograms && len(words) > 0 {
			return "terva cannot read a call that names this many commands"
		}
		for _, prog := range programs {
			if len(words) == 0 {
				break
			}
			if why := r.listed(append(strings.Fields(prog), words...), facts); why != "" {
				return why
			}
		}
		for k, e := range v {
			if why := r.argument(k, e, facts); why != "" {
				return why
			}
		}
	}
	return ""
}

// keyName is the name of an argument in lower case and with no _ or -, so
// that targetBranch, target_branch and target-branch are one name.
func keyName(key string) string {
	return keyMarks.Replace(strings.ToLower(key))
}

var keyMarks = strings.NewReplacer("_", "", "-", "")

func texts(list []any) []string {
	var out []string
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// listed reads a command that a tool takes as a list of words. The tool
// starts the program with those words, and no shell reads them.
//
// 🚨 The words are not joined into a line. A word such as A=' would open a
// quote there, and the words behind it would be read as a text.
func (r MergeRule) listed(list []string, facts func() RepoFacts) string {
	if len(list) == 0 {
		return ""
	}
	var l line
	words := make([]word, len(list))
	for i, text := range list {
		words[i] = word{text: text, ok: true}
		if namesEndpoint(text) {
			l.find("it names a forge's merge endpoint")
		}
	}
	r.call(words, false, &l, 0)
	// One of the words may be a line: ["bash", "-c", "gh pr merge 12"].
	for _, text := range list {
		if strings.ContainsAny(text, " \t\n") {
			r.nested(text, &l, 0)
		}
	}
	return r.verdict(&l, facts)
}

// argumentKeys are the names under which a tool takes the words of its
// command, beside the program.
var argumentKeys = map[string]bool{"args": true, "arguments": true, "argv": true, "params": true}

// toolWords splits the name of a tool into its words, in lower case:
// mcp_github_merge_pull_request, mergePullRequest, PRMerge.
func toolWords(tool string) []string {
	tool = camelWords.ReplaceAllString(tool, "${1}_${2}")
	tool = capitalWords.ReplaceAllString(tool, "${1}_${2}")
	var words []string
	for _, w := range strings.FieldsFunc(tool, func(c rune) bool {
		return (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9')
	}) {
		words = append(words, strings.ToLower(w))
	}
	return words
}

// camelWords and capitalWords find the place where a word starts inside
// mergePullRequest and inside PRMerge.
var (
	camelWords   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	capitalWords = regexp.MustCompile(`([A-Z])([A-Z][a-z])`)
)

// mergeToolName reports whether the words of a tool's name say that it
// merges a pull request or a branch.
//
// 🔑 GitLab's name for a pull request is merge request. So those two words
// together are the thing and no action: create_merge_request opens one, and
// a chain may open one. merge_merge_request and accept_merge_request merge
// it. A name that says get, list or search reads, whatever else it says.
func mergeToolName(words []string) bool {
	// thing is set for a word that names a pull request. place is set for
	// that, and also for a forge, a branch, a queue.
	merge, ends, thing, place, submits, change := false, false, false, false, false, false
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case toolReads[w]:
			return false
		case w == "merge" && i+1 < len(words) && (words[i+1] == "request" || words[i+1] == "requests"):
			thing, place = true, true
			i++
		case w == "merge" || w == "automerge":
			merge = true
		case w == "accept" || w == "complete" || w == "enqueue":
			// Azure DevOps completes a pull request, GitLab accepts a merge
			// request, and GitHub puts one in the merge queue.
			// accept_repository_invitation merges nothing.
			ends = true
		case w == "submit":
			// Gerrit submits a change. submit_review merges nothing.
			submits = true
		case w == "change" || w == "changes" || w == "gerrit":
			change = true
			place = place || w == "gerrit"
		case toolNameRequest[w]:
			thing, place = true, true
		case toolNamePull[w]:
			place = true
		}
	}
	return merge && place || ends && thing || submits && change
}

// toolNameRequest are the words of a tool's name that name a pull request.
var toolNameRequest = map[string]bool{
	"pull": true, "pulls": true, "pr": true, "prs": true, "mr": true, "mrs": true,
	"request": true, "requests": true, "pullrequest": true,
}

// toolReads are the words of a tool's name that say that it changes
// nothing.
var toolReads = map[string]bool{
	"get": true, "list": true, "search": true, "read": true, "view": true, "show": true, "describe": true,
}

// pushToolName reports whether a tool pushes to a repository: mcp_git_push,
// or GitHub's push_files. Its name says push, and it names a repository or
// its arguments name a branch.
func pushToolName(words []string, args any) bool {
	push, repo := false, false
	for _, w := range words {
		push = push || w == "push"
		repo = repo || pushToolRepo[w]
	}
	if push && !repo {
		if object, ok := args.(map[string]any); ok {
			for k := range object {
				repo = repo || pushBranchKeys[keyName(k)]
			}
		}
	}
	return push && repo
}

var pushToolRepo = map[string]bool{
	"git": true, "github": true, "gitlab": true, "forgejo": true, "gitea": true, "bitbucket": true,
	"codeberg": true, "gerrit": true, "branch": true, "commit": true, "commits": true, "files": true,
	"repo": true, "repository": true, "vcs": true, "scm": true,
}

// pushBranchKeys are the names under which a tool that pushes takes its
// branch or its refspec.
var pushBranchKeys = map[string]bool{
	"branch": true, "branchname": true, "ref": true, "refspec": true, "refspecs": true,
	"target": true, "targetbranch": true, "targetref": true, "destination": true, "destinationbranch": true,
	"dest": true, "destbranch": true, "dst": true, "to": true, "remotebranch": true, "remoteref": true,
	"upstream": true,
}

// pushEveryKeys are the names under which a tool that pushes takes a wish
// for more than one branch.
var pushEveryKeys = map[string]bool{"all": true, "mirror": true, "branches": true}

// pushTool reads the arguments of a tool that pushes. Such a tool pushes
// where the host cannot look. So it must name its branch, and the branch
// must be none that is protected.
//
// 🚨 A tool of a forge writes to the branch that it names. A tool that runs
// git gives git a refspec, and git may send a name with one side to the
// upstream of that branch. So such a tool must name both sides.
func (r MergeRule) pushTool(names []string, v any, f RepoFacts) string {
	unread := "terva cannot read where the push of this tool goes. Push with git in the bash tool: git push origin HEAD:refs/heads/my-branch"
	var branches []string
	every := false
	var find func(key string, v any)
	find = func(key string, v any) {
		switch v := v.(type) {
		case bool:
			every = every || (v && pushEveryKeys[keyName(key)])
		case string:
			if pushBranchKeys[keyName(key)] {
				branches = append(branches, strings.TrimSpace(v))
			}
		case []any:
			for _, e := range v {
				find(key, e)
			}
		case map[string]any:
			for k, e := range v {
				find(k, e)
			}
		}
	}
	find("", v)
	if every || len(branches) == 0 {
		return unread
	}
	local := slices.Contains(names, "git") || slices.Contains(names, "vcs") || slices.Contains(names, "scm")
	for _, b := range branches {
		p := readPush([]word{{text: "origin", ok: true}, {text: b, ok: true}})
		if p.every || p.head || p.unread || (local && p.bare()) {
			return unread
		}
		for _, d := range p.dests {
			if r.protected(d, f) {
				return "its push moves the protected branch " + d
			}
		}
	}
	return ""
}

// word is one word of a command. ok is false for a word that holds a
// variable or a substitution, which the rule cannot read.
//
// 🚨 A word that is not read has a mark in its text for each such part. One
// with no mark at all comes from a variable that the line itself fills where
// the rule does not look, as read does.
type word struct {
	text string
	ok   bool
	// from names the variable that gave this word, where the variable has
	// no quotes around it. The shell drops such a word when the variable
	// holds nothing.
	from string
}

// line is what the rule gathered from one command line.
type line struct {
	why string
	// Two of the three ways in which a line leaves the repository that the
	// host read. elsewhere: it changes the directory. reconfigured: it
	// changes a setting that places a push. A push that names no branch
	// then goes where the rule cannot know. The third way is a change of
	// the branch at work, and log holds each one.
	elsewhere, reconfigured bool
	// dirs holds each directory with a full path that the line changes to.
	// One that is the repository's own top level changes nothing.
	dirs []string
	// gitEnv is set when the line exports a variable of git's own, such as
	// GIT_DIR. Every push after it reads another repository or another
	// configuration.
	gitEnv bool
	// log holds each change of the branch at work, in the order of the
	// line. A push is read against the changes in front of it.
	log []branchEvent
	// again counts the bytes of the commands that the rule read a second
	// time, with a variable left out. See maxAgain.
	again *int
	// chain is the chain of && of the command that the rule reads now. It
	// is 0 where the rule knows no chain. piped is set while that command
	// is a stage of a pipe: the pipe hides whether it ended well.
	chain int
	piped bool
	// inLoop is set while that command stands in a loop.
	inLoop bool
	// events counts the commands that the rule read, so that it knows what
	// came first: a change of the branch or a push.
	events int
	// autocorrect is set when the line sets git's help.autocorrect.
	autocorrect bool
	// functions is set for a line that declares a function. Its commands
	// run where the function is called, so their order says nothing.
	functions bool
	// tags holds each tag that the line makes, with the chain of && of the
	// command that made it. The chain is 0 where the rule knows none.
	tags   map[string]int
	pushes []push
}

// find records the first thing that makes the line a merge action.
func (l *line) find(why string) {
	if l.why == "" {
		l.why = why
	}
}

// branchEvent is one change of the branch at work on a line.
type branchEvent struct {
	at int
	// name is the branch that a checkout names, or that the line makes.
	// It is empty for a change that the rule cannot name: git checkout -,
	// a new upstream, a write into .git. create is set for a branch that
	// the line makes, and fromStart when the new branch starts at a branch
	// that it may then follow.
	name              string
	create, fromStart bool
	// sure is set for a command that the rule read as the shell runs it,
	// and chain is its chain of &&. A push in the same chain runs only when
	// the command ended well.
	sure  bool
	chain int
	// loop is set for a change inside a loop.
	loop bool
}

// moveBranch records a change of the branch at work, or of its upstream,
// that the rule cannot name.
func (l *line) moveBranch() {
	l.events++
	l.log = append(l.log, branchEvent{at: l.events, loop: l.inLoop})
}

// cd records a change of the directory. Only a full path can be the top
// level of the repository that the host read.
//
// rootMark is the top level of the repository of the directory at work,
// which is the one that the host read. A path through .. can leave a link
// by another way than it came, so it is no full path here.
func (l *line) cd(dir word) {
	switch {
	case dir.ok && dir.text == rootMark:
	case dir.ok && path.IsAbs(dir.text) && !strings.Contains(dir.text, ".."):
		l.dirs = append(l.dirs, dir.text)
	default:
		l.elsewhere = true
	}
}

// checkOut records a change to a branch that the line names. It is a
// change of the branch only when the name is not the branch that the host
// read, and places knows that. direct is set for a command that the rule
// read as the shell runs it.
func (l *line) checkOut(branch string, direct bool) {
	l.events++
	if branch != branchMark {
		l.log = append(l.log, branchEvent{at: l.events, name: branch, sure: direct && !l.piped, chain: l.chain, loop: l.inLoop})
	}
}

// add records a push, and its place among the commands of the line.
func (l *line) add(p push) {
	l.events++
	p.at, p.chain, p.loop = l.events, l.chain, l.inLoop
	l.pushes = append(l.pushes, p)
}

// place is a branch on which a push may run.
type place struct {
	branch string
	// created is set for a branch that the line made, and fromStart says
	// that it may follow its start.
	created, fromStart bool
	// switched is set when a change of the branch stands in front of the
	// push, and the rule does not know where it led.
	switched bool
}

// places returns each branch on which a push may run. The push must pass
// on every one.
//
// 🔑 git checkout -b fix/y && git push -u origin HEAD pushes fix/y, and the
// rule knows that. It holds when the new branch is the last change of the
// branch in front of the push.
//
// 🚨 Only a chain of && makes that sure. In git checkout -b fix/y; git push
// the first command fails when fix/y exists, and the push runs on the
// branch that the line began on. A probe with a real remote moved trunk
// that way. So a push that is not in the chain of the last change is read
// on the branch before it too.
//
// 🚨 A change behind the push is no change for that push. And a push is
// read against the change in front of it, not against the last one of the
// line: git checkout -B main && git push origin HEAD && git checkout -b
// other pushes main.
func (l *line) places(p push, f RepoFacts) []place {
	var before []branchEvent
	for _, e := range l.log {
		// 🚨 The commands of a function run where the line calls it. So
		// with a function on the line every change counts, also one that
		// stands behind the push: p() { git push origin HEAD; }; git
		// checkout main; p. A loop runs its commands again, so a change in
		// a loop stands in front of each push in a loop.
		if e.at < p.at || l.functions || (e.loop && p.loop) {
			before = append(before, e)
		}
	}
	first := place{branch: f.Branch, switched: leaves(before, f.Branch)}
	if len(before) == 0 || l.functions {
		return []place{first}
	}
	last, rest := before[len(before)-1], before[:len(before)-1]
	chained := last.sure && p.chain != 0 && p.chain == last.chain
	switch {
	case last.create && chained:
		return []place{{branch: last.name, created: true, fromStart: last.fromStart}}
	case last.create:
		return []place{
			{branch: last.name, created: true, fromStart: last.fromStart},
			{branch: f.Branch, switched: leaves(rest, f.Branch)},
		}
	case chained:
		// git checkout main && git pull && git checkout feat/x && git push:
		// the push runs only on the branch of the last checkout.
		//
		// 🚨 A change with no name in front of it can be a new upstream of
		// that branch: git branch -u origin/main && git checkout feat/x.
		// A new branch of the same name can be one too: git checkout -B
		// feat/x origin/main. A new branch of another name is none.
		unnamed := false
		for _, e := range rest {
			unnamed = unnamed || e.name == "" || (e.create && e.name == f.Branch)
		}
		return []place{{branch: f.Branch, switched: last.name != f.Branch || unnamed}}
	}
	return []place{first}
}

// leaves reports whether one of the changes may leave the branch that the
// host read. git checkout feat/x on feat/x changes nothing. A change with
// no name leaves it, and so does a new branch of the same name: git
// checkout -B feat/x origin/main gives feat/x another upstream.
func leaves(events []branchEvent, branch string) bool {
	for _, e := range events {
		if e.create || e.name != branch {
			return true
		}
	}
	return false
}

// absorb takes what a subshell or a substitution gathered.
//
// 🔑 A cd in a subshell ends with it: (cd client && npm run lint) && git
// push pushes from the directory of the line. So a change of the directory
// or of a variable counts outside the group only when the group itself
// pushes.
//
// 🚨 The branch at work and the configuration are on the disk, and a change
// of them outlasts the group: (git checkout main) && git push pushes main.
func (l *line) absorb(in *line) {
	l.find(in.why)
	// The commands of the group stand behind the ones that the line read
	// so far.
	created := false
	for _, e := range in.log {
		e.at += l.events
		created = created || e.create
		l.log = append(l.log, e)
	}
	for i := range in.pushes {
		in.pushes[i].at += l.events
	}
	l.events += in.events
	if created {
		// The new branch outlasts the group. The rule reads no push behind
		// the group on it.
		l.moveBranch()
	}
	l.reconfigured = l.reconfigured || in.reconfigured
	l.functions = l.functions || in.functions
	for name, chain := range in.tags {
		l.tag(name, chain)
	}
	if len(in.pushes) == 0 {
		return
	}
	l.pushes = append(l.pushes, in.pushes...)
	l.dirs = append(l.dirs, in.dirs...)
	l.elsewhere = l.elsewhere || in.elsewhere
	l.gitEnv = l.gitEnv || in.gitEnv
}

func (l *line) tag(name string, chain int) {
	if l.tags == nil {
		l.tags = map[string]int{}
	}
	l.tags[name] = chain
}

// push is one git push on a line.
type push struct {
	dests []string
	// head is set when a refspec names HEAD, so the destination is the
	// branch at work. implicit is set when the push names no refspec.
	head, implicit bool
	// bares holds each branch of a refspec that names a source and no
	// destination, and bareHead is set for HEAD in that form. A push refspec
	// of the remote, or the upstream mode, can send such a refspec to
	// another branch.
	bares    []string
	bareHead bool
	// named is set for a refspec with no destination that is the branch at
	// work by its name: git push origin "$(git branch --show-current)".
	named bool
	// merges is set for a push option that asks the forge to merge.
	merges bool
	// inert is set for a push that sends nothing: --dry-run, --help.
	inert bool
	// early is set for a refspec that names the branch at work through a
	// variable. The name is the branch at the place of the variable.
	early bool
	// tags is set for --tags with no refspec. Such a push sends the tags and
	// no branch, so push.default does not place it.
	tags bool
	// every is set for a push of more branches than it names. unread is set
	// when a word of the push cannot be read, or when an option of git or a
	// variable moves the repository that the push reads.
	every, unread bool
	// at is the place of the push among the commands of its line, and
	// chain is its chain of &&. loop is set for a push inside a loop.
	at, chain int
	loop      bool
}

func (p push) bare() bool { return p.bareHead || p.named || len(p.bares) > 0 }

func (r MergeRule) command(command string, facts func() RepoFacts) string {
	if namesEndpoint(command) {
		return "it names a forge's merge endpoint"
	}
	if r.configured(command) {
		return "it matches a merge command of the user configuration"
	}
	var l line
	if !r.parse(command, &l, 0) {
		r.unparsed(command, &l, 0)
	}
	return r.verdict(&l, facts)
}

// verdict says what makes a line that was read a merge action.
func (r MergeRule) verdict(l *line, facts func() RepoFacts) string {
	if l.why != "" {
		return l.why
	}
	if len(l.pushes) == 0 {
		return ""
	}
	f := RepoFacts{Unknown: true}
	if facts != nil {
		f = facts()
	}
	for _, dir := range l.dirs {
		if f.Root == "" || path.Clean(dir) != path.Clean(f.Root) {
			l.elsewhere = true
		}
	}
	for _, p := range l.pushes {
		p.unread = p.unread || l.gitEnv
		if why := r.pushed(p, l, f); why != "" {
			return why
		}
	}
	return ""
}

// unparsed reads a line that the parser refused. The shell may still run
// the front of such a line. So each line of it is read by itself, and then
// all of it word by word: a word of a merge or of a push is enough.
func (r MergeRule) unparsed(text string, l *line, depth int) {
	// A backslash at the end of a line joins it to the next one.
	text = strings.ReplaceAll(text, "\\\n", "")
	if depth < maxNested {
		for _, one := range strings.Split(text, "\n") {
			if strings.TrimSpace(one) != "" {
				r.parse(one, l, depth+1)
			}
		}
	}
	if why := loose(text); why != "" {
		l.find(why)
	}
	// 🚨 ((just land 12) ) is two groups for bash, and the parser refuses it.
	// A person's pattern with an anchor matches no such line as a whole.
	if r.anywhere(text) {
		l.find("it matches a merge command of the user configuration")
	}
	// 🚨 gi''t pu''sh origin HEAD:main; cat <<EOF: bash warns of the open
	// here document and runs the push. The quotes hide the words from a
	// read word by word.
	if why := loose(quoteMarks.Replace(text)); why != "" {
		l.find(why)
	}
}

var quoteMarks = strings.NewReplacer("'", "", `"`, "", `\`, "")

// loose reads a text word by word.
func loose(text string) string {
	switch {
	case looseMerge.MatchString(text):
		return "it merges a pull request"
	case loosePush.MatchString(text):
		return "terva cannot read where its push goes. " + howToPush
	}
	return ""
}

// tooDeep is what the rule says of a line inside more shells than it reads.
const tooDeep = "terva cannot read a command inside this many shells. Run the command with no shell around it"

func (r MergeRule) configured(text string) bool {
	for _, re := range r.Commands {
		if re != nil && re.MatchString(text) {
			if r.hits != nil {
				*r.hits = append(*r.hits, text)
			}
			return true
		}
	}
	return false
}

// maxPatternText bounds the text that anywhere gives a pattern at one word.
const maxPatternText = 1 << 10

// anywhere reports whether a person's pattern matches at the front of any
// word of a text, with the quotes removed. It is the read of a line that the
// rule does not parse, where a word of a command is enough.
//
// 🔑 The bound keeps that read near the size of the line. Without it a
// pattern with no fixed front reads the rest of the line one time for each
// word, which is the square of the line.
func (r MergeRule) anywhere(text string) bool {
	text = quoteMarks.Replace(text)
	front := true
	for i := 0; i < len(text); i++ {
		if strings.IndexByte(" \t\n\r;|&(){}`$<>=!", text[i]) >= 0 {
			front = true
			continue
		}
		if front && r.configured(text[i:min(len(text), i+maxPatternText)]) {
			return true
		}
		front = false
	}
	return false
}

// maxNested bounds the lines inside lines that the rule reads: sh -c and
// eval.
const maxNested = 4

// maxLine bounds the line that the rule gives to the parser, and pastBounds
// says whether a line is past it or too deep. The bash tool has the same
// bounds, and tools.ShellPastBounds holds the reason for each one.
const maxLine = tools.MaxShellLine

func pastBounds(text string) bool { return tools.ShellPastBounds(text) }

// tooLong is what the rule says of a line past those bounds.
const tooLong = tools.ShellTooLong

// maxWords bounds the words that the variables of a line can add to its
// commands, and maxGroupDepth the subshells and substitutions around one
// command.
const (
	maxWords      = 1 << 16
	maxGroupDepth = 64
)

// reading is the state of one parse of a line.
type reading struct {
	r     MergeRule
	depth int
	// seen holds each pipe that an outer pipe read, and read each command
	// whose printed text an outer command read. done holds each
	// substitution that the rule read ahead of the command that holds it.
	seen map[*syntax.BinaryCmd]bool
	read map[*syntax.CallExpr]bool
	done map[syntax.Node]bool
	// loopEnd is the end of the last loop that the rule entered. A command
	// in front of it is in a loop.
	loopEnd uint
	// epoch counts the evals that the rule could not read, and setAt holds
	// the count at the time when the line last set a variable.
	epoch int
	setAt map[string]int
	// vars holds the values of the variables that the line set. A variable
	// with no value there is one that the rule cannot read.
	vars map[string][]string
	// printedBy holds, for a variable, the commands whose output it holds:
	// x=$(echo 'gh pr merge 12'). printedRead counts the ones that the rule
	// read already.
	printedBy   map[string][]syntax.Node
	printedRead map[string]int
	// looped holds each loop that the read of an outer loop covered, and
	// groups counts the groups around the command that the rule reads now.
	looped map[syntax.Node]bool
	groups int
	// blind is set when the line can change a variable where the rule does
	// not see it: eval, source, a function. No value is then trusted.
	blind bool
	// hidden holds each variable that a command of the line fills where the
	// rule does not see the value: read c, printf -v c.
	hidden map[string]bool
	// added counts the words that variables added to the commands of the
	// line.
	added int
	// long is set when a variable adds more than maxWords words to one
	// command.
	long bool
	// chains gives each statement the chain of && that it stands in, and
	// inPipe holds each statement that is a stage of a pipe.
	chains map[*syntax.Stmt]int
	inPipe map[*syntax.Stmt]bool
	chain  int
}

// parse reads every command of a line into l. It reports false for a line
// that does not parse.
func (r MergeRule) parse(command string, l *line, depth int) bool {
	if pastBounds(command) {
		l.find(tooLong)
		return true
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	r.read(file, l, depth)
	return true
}

// read reads every command of a parsed line into l.
func (r MergeRule) read(file *syntax.File, l *line, depth int) {
	rd := &reading{
		r: r, depth: depth,
		seen: map[*syntax.BinaryCmd]bool{}, read: map[*syntax.CallExpr]bool{},
		done: map[syntax.Node]bool{}, setAt: map[string]int{},
		vars: map[string][]string{}, printedBy: map[string][]syntax.Node{},
		printedRead: map[string]int{}, looped: map[syntax.Node]bool{},
		chains: map[*syntax.Stmt]int{}, inPipe: map[*syntax.Stmt]bool{}, hidden: map[string]bool{},
	}
	rd.walk(file, l)
}

// walk reads the commands under one node.
func (rd *reading) walk(top syntax.Node, l *line) {
	r, depth := rd.r, rd.depth
	syntax.Walk(top, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Subshell:
			rd.group(n.Stmts, l)
			return false
		case *syntax.CmdSubst:
			if !rd.done[n] {
				rd.group(n.Stmts, l)
			}
			return false
		case *syntax.ProcSubst:
			if !rd.done[n] {
				rd.group(n.Stmts, l)
			}
			return false
		case *syntax.FuncDecl:
			// The commands of a function run where it is called.
			l.functions, rd.blind = true, true
		case *syntax.WhileClause:
			rd.loopEnd = max(rd.loopEnd, n.End().Offset())
			rd.loop(n)
		case *syntax.ForClause:
			rd.loopEnd = max(rd.loopEnd, n.End().Offset())
			rd.loop(n)
			// for c in "git push origin HEAD:main"; do $c; done
			if loop, ok := n.Loop.(*syntax.WordIter); ok && loop.Name != nil {
				var values []string
				for _, item := range loop.Items {
					text, ok := wordText(item)
					if !ok {
						values = nil
						break
					}
					values = append(values, text)
				}
				rd.set(loop.Name.Value, values)
			}
		case *syntax.Stmt:
			rd.chained(n, l)
			// 🚨 fed and printed read the outer command first, and they
			// mark what is inside it as read. The read ahead goes to the
			// inner command first, so it stands behind them.
			rd.fed(n, l)
			for _, to := range n.Redirs {
				rd.ahead(l, to.Word, to.Hdoc)
			}
			// A write into .git changes the configuration or the branch
			// with no git command: printf ... >> .git/config.
			for _, to := range n.Redirs {
				if text, _ := wordText(to.Word); insideGit(text) {
					l.reconfigured = true
					l.moveBranch()
				}
			}
		case *syntax.BinaryCmd:
			// 🚨 A pipe is read one time, at its outermost node. The parser
			// nests a pipe of n stages n deep. A read at each of those
			// nodes took 20 seconds for a line of 2.5 KiB.
			if isPipe(n) && !rd.seen[n] {
				rd.piped(pipeline(n, rd.seen), l)
			}
		case *syntax.ParamExp:
			// ${b:=HEAD:main} gives b a value where it has none.
			if n.Param != nil && n.Exp != nil && (n.Exp.Op == syntax.AssignUnset || n.Exp.Op == syntax.AssignUnsetOrNull) {
				rd.set(n.Param.Value, nil)
			}
		case *syntax.DeclClause:
			// export GIT_DIR=... changes the repository of every git
			// command after it.
			for _, a := range n.Args {
				if rd.assigned(a) {
					l.gitEnv = true
				}
				rd.given(a, l)
				// declare -n makes a second name for a variable, -l changes
				// its letters, and -i reads its value as a sum. Only the
				// options that leave a value as it is are read.
				if text, _ := wordText(a.Value); a.Name == nil && strings.HasPrefix(text, "-") && strings.Trim(text[1:], "xrgaA") != "" {
					rd.blind = true
				}
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				// HOME=/tmp/other; git push. The shell exports HOME, so the
				// new value reaches every command after it.
				for _, a := range n.Assigns {
					if rd.assigned(a) {
						l.gitEnv = true
					}
				}
				break
			}
			// b=feat/y true sets b for that one command, and bash keeps
			// what b was. A plain sh keeps the new value behind a command
			// such as :, so the rule then knows no value.
			gitEnv := false
			for _, a := range n.Assigns {
				gitEnv = gitEnv || (a.Name != nil && gitVariable(a.Name.Value))
				rd.given(a, l)
				if a.Name != nil {
					rd.forget(a.Name.Value)
				}
			}
			rd.printed(n, l)
			// 🚨 The shell runs the substitutions of a command before the
			// command: X=$(git checkout -q -B main) git push origin HEAD
			// pushes main.
			//
			// A substitution among the words of a push makes the push one
			// that the rule cannot read, so only the values in front of
			// the command are read ahead.
			for _, a := range n.Assigns {
				rd.ahead(l, a.Value)
			}
			// 🚨 A command that is a variable, where the line itself fills
			// the variable and the rule does not see with what: read c <<<
			// 'gh pr merge 12'; $c.
			//
			// A variable that the line never names comes from outside the
			// line, as $EDITOR does, and it passes.
			if name, _, ok := variable(n.Args[0]); ok && (rd.hidden[name] || shellSets[name] || rd.stale(name)) {
				l.find("terva cannot read the command in the variable " + name + ". Write the command itself")
			}
			variants := rd.words(n)
			for _, words := range variants {
				r.call(words, gitEnv, l, depth)
				// 🚨 A word from a variable with no quotes is no word at
				// all when the variable holds nothing, and bash sets none
				// in (x=view) or behind false &&. So the command is read
				// without those words too: (x=view); gh pr $x merge 12.
				// Only a merge and a push count from that read.
				//
				// 🚨 One variable can hold nothing while the next one holds
				// its value: (o=-n); r=origin; git push $o $r HEAD:main. So
				// the rule reads each combination.
				bares, many := withoutOptional(words)
				for _, bare := range bares {
					spent := l.budget()
					for _, w := range bare {
						*spent += len(w.text) + 1
					}
					if *spent > maxAgain {
						l.find(tooLong)
						break
					}
					t := line{chain: l.chain, piped: l.piped, inLoop: l.inLoop, again: spent}
					r.call(bare, gitEnv, &t, depth)
					l.find(t.why)
					for _, p := range t.pushes {
						l.add(p)
					}
				}
				if many {
					// More variables than the rule reads in turn: the
					// words are read as those of a line that does not
					// parse.
					texts := make([]string, len(words))
					for i, w := range words {
						texts[i] = w.text
					}
					l.find(loose(strings.Join(texts, " ")))
				}
			}
			for _, words := range variants {
				// u=unset; $u b
				rd.changes(words)
			}
			if rd.long {
				l.find(tooLong)
			}
		}
		return true
	})
}

// given reads the value of a variable that a program gets, as a line. A
// program may run it: GIT_PAGER='git push origin HEAD:main' git log.
//
// A variable that the line keeps for itself is no such value. Its text is
// read where a command runs it: msg="just pr-merge refuses a draft" is the
// message of a commit.
func (rd *reading) given(a *syntax.Assign, l *line) {
	if a.Value == nil {
		return
	}
	if text, _ := wordText(a.Value); startsACommand(text) {
		rd.r.nested(text, l, rd.depth)
	}
}

// ahead reads the substitutions of a command's own words in front of the
// command, as the shell runs them.
func (rd *reading) ahead(l *line, words ...*syntax.Word) {
	for _, w := range words {
		if w == nil {
			continue
		}
		syntax.Walk(w, func(node syntax.Node) bool {
			switch n := node.(type) {
			case *syntax.CmdSubst:
				rd.done[n] = true
				rd.group(n.Stmts, l)
				return false
			case *syntax.ProcSubst:
				rd.done[n] = true
				rd.group(n.Stmts, l)
				return false
			}
			return true
		})
	}
}

// maxOptional bounds the variables with no quotes that the rule reads one
// command with and without, in each combination. maxAgain bounds the bytes
// of those reads in one line.
//
// 🚨 Each such read of a shell reads the line inside it again, and that line
// has reads of its own. Four levels of 15 reads are 50,625 reads of the last
// line, so the bytes have a bound and the reads have none.
//
// The bound is 2 MiB. The 15 reads of one command with four such variables
// fit in it at the longest line that the rule reads.
const (
	maxOptional = 4
	maxAgain    = 16 * maxLine
)

// withoutOptional returns the command as the shell runs it when some of its
// variables with no quotes hold nothing: one list of words for each set of
// such variables. The words of one variable stay or go together.
//
// many is set for a command with more than maxOptional of them. The rule
// then gets the command with none of their words, and nothing between.
func withoutOptional(words []word) (bare [][]word, many bool) {
	var names []string
	for _, w := range words {
		if w.from != "" && !slices.Contains(names, w.from) {
			if names = append(names, w.from); len(names) > maxOptional {
				many = true
				break
			}
		}
	}
	if many {
		var none []word
		for _, w := range words {
			if w.from == "" {
				none = append(none, w)
			}
		}
		return [][]word{none}, true
	}
	for gone := 1; gone < 1<<len(names); gone++ {
		var some []word
		for _, w := range words {
			if i := slices.Index(names, w.from); i < 0 || gone&(1<<i) == 0 {
				some = append(some, w)
			}
		}
		bare = append(bare, some)
	}
	return bare, false
}

// budget returns the count of the bytes that the line read a second time.
// Each line inside the line shares it.
func (l *line) budget() *int {
	if l.again == nil {
		l.again = new(int)
	}
	return l.again
}

// stale reports whether an eval that the rule could not read stands behind
// the last value that the line gave a variable.
func (rd *reading) stale(name string) bool {
	at, known := rd.setAt[name]
	return known && at < rd.epoch
}

// chained gives a statement its chain. Each command that && joins is in one
// chain: the second one runs only when the first one ended well.
//
// 🚨 git checkout -b fix/y; git push is no chain. When fix/y exists, the
// first command fails, and the push runs on the branch that the line began
// on. A probe with a real remote moved trunk that way.
func (rd *reading) chained(stmt *syntax.Stmt, l *line) {
	id, ok := rd.chains[stmt]
	if !ok {
		rd.chain++
		id = rd.chain
	}
	l.chain, l.piped = 0, rd.inPipe[stmt]
	// A line inside a line knows only its own loops, so it keeps what the
	// line around it said.
	inLoop := stmt.Pos().Offset() < rd.loopEnd
	l.inLoop = inLoop || (rd.depth > 0 && l.inLoop)
	// 🚨 A line inside a line counts its chains from 1 again, so it has no
	// place in the chains of the line around it.
	if rd.depth > 0 {
		return
	}
	l.chain = id
	both, ok := stmt.Cmd.(*syntax.BinaryCmd)
	if !ok || both.X == nil || both.Y == nil {
		return
	}
	switch {
	case both.Op == syntax.AndStmt && !both.X.Negated && !both.Y.Negated:
		// 🚨 A ! in front of either side turns its end around: true && !
		// git checkout -b fix..z && git push runs the push when the
		// checkout failed.
		rd.chains[both.X], rd.chains[both.Y] = id, id
	case isPipe(both):
		// git push -u origin HEAD 2>&1 | tail -3 runs when the commands in
		// front of it ended well, so the first stage keeps the chain. The
		// pipe ends as its last stage does, so no stage of it makes a
		// later command sure.
		rd.chains[both.X] = id
		rd.inPipe[both.X], rd.inPipe[both.Y] = true, true
	}
}

// insideGit reports whether a path is one of the files in which git keeps
// its configuration or the branch at work.
func insideGit(text string) bool {
	return strings.Contains(text, ".git/config") || strings.Contains(text, ".git/HEAD") || strings.HasSuffix(text, ".gitconfig")
}

// group reads the commands of a subshell or of a substitution into a line
// of their own.
//
// 🚨 Each group hands what it gathered to the group around it. So the cost
// of a group is its depth, and the rule reads none past maxGroupDepth.
func (rd *reading) group(stmts []*syntax.Stmt, l *line) {
	rd.groups++
	defer func() { rd.groups-- }()
	if rd.groups > maxGroupDepth {
		l.find(tooLong)
		return
	}
	in := line{inLoop: l.inLoop, again: l.budget()}
	for _, stmt := range stmts {
		rd.walk(stmt, &in)
	}
	l.absorb(&in)
}

// loop reads a loop before its commands. An assignment in the loop reaches
// each command of the loop, also the ones in front of it, so its variable
// is one that the rule cannot read.
//
// 🚨 The same holds for a command of the loop that sets a variable, as read
// b does behind a push of "$b".
func (rd *reading) loop(n syntax.Node) {
	// 🚨 The read of a loop covers each loop inside it. A read at each one
	// took 8 seconds for 128 KiB.
	if rd.looped[n] {
		return
	}
	syntax.Walk(n, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.WhileClause, *syntax.ForClause:
			rd.looped[n] = true
		case *syntax.Assign:
			if n.Name != nil {
				rd.forget(n.Name.Value)
			}
		case *syntax.CallExpr:
			if len(n.Args) > 0 {
				rd.changes(wordsOf(n))
			}
		}
		return true
	})
}

// set records the values of a variable. No value means one that the rule
// cannot read, and the variable then stays unread to the end of the line.
//
// 🚨 A second value does not take the place of the first. The line may set
// the variable under a condition, or in a group, and the rule does not know
// which value a later command gets: b=main; test -n "$x" && b=feat/x.
func (rd *reading) set(name string, values []string) {
	if name == "IFS" {
		// IFS changes where the shell cuts a value into words.
		rd.blind = true
	}
	rd.setAt[name] = rd.epoch
	have, known := rd.vars[name]
	if len(values) == 0 || (known && have == nil) || shellSets[name] {
		rd.vars[name] = nil
		return
	}
	for _, value := range values {
		// 🚨 The branch at work in a variable is the branch at that place of
		// the line. b=$(git branch --show-current); git checkout -b fix/q &&
		// git push origin "$b" pushes the first branch by its name.
		value = strings.ReplaceAll(value, branchMark, startMark)
		if !slices.Contains(have, value) {
			have = append(have, value)
		}
	}
	if len(have) > 8 {
		have = nil
	}
	rd.vars[name] = have
}

// forget makes a variable one that the rule cannot read, as a word and as
// every word of an array.
func (rd *reading) forget(name string) {
	rd.set(name, nil)
	rd.set(name+everyWord, nil)
}

// everyWord ends the name under which the rule keeps "${c[@]}". "$c" is
// the first word of an array, and it has the plain name.
const everyWord = "[@]"

// shellSets are the variables that the shell fills by itself. The rule
// knows no value of one: $_ is the last word of the command before it.
var shellSets = map[string]bool{
	"_": true, "REPLY": true, "MAPFILE": true, "OPTARG": true, "OPTIND": true, "RANDOM": true,
	"SECONDS": true, "LINENO": true, "PIPESTATUS": true, "BASH_COMMAND": true, "BASH_REMATCH": true,
	"COPROC": true, "PWD": true, "OLDPWD": true,
}

// assigned reads one assignment, and it reports whether the variable is one
// that moves git.
//
// 🔑 The value is kept when the rule can read it. A later command of the
// line that is the variable, or that takes it as a word, is then read with
// the value in its place: cmd="gh pr merge 12"; $cmd. A value that no
// command runs stays a text, such as the message of a commit.
func (rd *reading) assigned(a *syntax.Assign) bool {
	name, value, ok := "", "", true
	if a.Name != nil {
		name = a.Name.Value
	}
	if a.Naked && a.Name != nil {
		// export b gives b no value. It keeps the one that it has, which
		// may come from the environment.
		return gitVariable(name)
	}
	var texts []string
	switch {
	case a.Array != nil:
		for _, e := range a.Array.Elems {
			if e.Value != nil {
				text, readable := wordText(e.Value)
				texts, ok = append(texts, text), ok && readable
			}
			// b=([1]=feat/x:feat/x [0]=HEAD:main): the first word is not
			// the first one in the text.
			ok = ok && e.Index == nil
		}
		value = strings.Join(texts, " ")
	case a.Value != nil:
		value, ok = wordText(a.Value)
	}
	if a.Name == nil {
		// export 'GIT_DIR=../other/.git': the name is inside the word. A
		// word that the rule cannot read may hold any name.
		name, value, _ = strings.Cut(value, "=")
		rd.blind = rd.blind || !ok
	}
	switch {
	case !ok || a.Append || a.Index != nil || name == "":
		rd.forget(name)
		// x=$(echo 'gh pr merge 12'); $x runs what the command printed.
		if a.Value != nil && name != "" {
			rd.printedBy[name] = append(rd.printedBy[name], substitutions(a.Value)...)
		}
	case a.Array != nil:
		// 🚨 "$c" is the first word of an array, and "${c[@]}" is each one.
		rd.set(name+everyWord, []string{value})
		if len(texts) > 0 {
			rd.set(name, texts[:1])
		} else {
			rd.set(name, nil)
		}
	default:
		rd.set(name, []string{value})
		rd.set(name+everyWord, []string{value})
	}
	return gitVariable(name)
}

// changes reads a command that sets a variable where the rule does not see
// the value: read r, unset r, printf -v r, eval, source.
func (rd *reading) changes(words []word) {
	words, _ = unwrap(words)
	if len(words) == 0 {
		return
	}
	switch program(words[0]) {
	case "eval":
		rd.blind = true
		rd.learn(words[1:])
	case "source", ".":
		rd.blind = true
	case "trap":
		// trap 'b=HEAD:main' DEBUG runs its line in front of each command.
		// trap 'rm -f /tmp/x' EXIT sets nothing.
		rd.blind = rd.blind || sets(words[1:])
	case "read", "unset", "mapfile", "readarray", "getopts", "printf", "let":
		if program(words[0]) == "printf" && !hasWord(words[1:], "-v") {
			// printf with no -v prints, and it sets nothing.
			return
		}
		for _, w := range words[1:] {
			// printf -v "$(echo b)" HEAD:main sets a variable of any name.
			rd.blind = rd.blind || !w.ok
			if name, _, _ := strings.Cut(w.text, "="); w.ok && variableName.MatchString(name) {
				rd.forget(name)
				rd.hidden[name], rd.hidden[name+everyWord] = true, true
			}
		}
	}
}

// learn reads the assignments of the line that eval runs. The shell of the
// line keeps them: eval 'c="gh pr merge 12"'; $c.
func (rd *reading) learn(words []word) {
	texts := make([]string, len(words))
	for i, w := range words {
		if !w.ok {
			// 🚨 Such an eval may give any variable of the line a new
			// value: c="git status"; eval "$(echo c=)'gh pr merge 12'"; $c.
			rd.epoch++
			return
		}
		texts[i] = w.text
	}
	text := strings.Join(texts, " ")
	if pastBounds(text) {
		return
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
	if err != nil {
		return
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		if a, ok := node.(*syntax.Assign); ok {
			rd.assigned(a)
		}
		return true
	})
}

// sets reports whether a line in the words of a command may give a variable
// a value. A line that the rule cannot read may.
func sets(words []word) bool {
	for _, w := range words {
		if !w.ok || pastBounds(w.text) {
			return true
		}
		file, err := syntax.NewParser().Parse(strings.NewReader(w.text), "")
		if err != nil {
			return true
		}
		found := false
		syntax.Walk(file, func(node syntax.Node) bool {
			switch n := node.(type) {
			case *syntax.Assign, *syntax.DeclClause, *syntax.FuncDecl, *syntax.ParamExp, *syntax.ForClause:
				found = true
			case *syntax.CallExpr:
				if words, _ := unwrap(wordsOf(n)); len(words) > 0 {
					switch program(words[0]) {
					case "eval", "source", ".", "trap", "read", "unset", "mapfile", "readarray", "getopts", "printf", "let":
						found = true
					}
					found = found || !words[0].ok
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// substitutions returns the commands of each substitution in a word.
func substitutions(w *syntax.Word) []syntax.Node {
	var inside []syntax.Node
	syntax.Walk(w, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CmdSubst:
			for _, stmt := range n.Stmts {
				inside = append(inside, stmt)
			}
			return false
		case *syntax.ProcSubst:
			for _, stmt := range n.Stmts {
				inside = append(inside, stmt)
			}
			return false
		}
		return true
	})
	return inside
}

// simple returns the name of a variable that is written with no change of
// its value: $cmd or ${cmd}. all is set for ${cmd[@]}, which is as many
// words as the array has.
func simple(p *syntax.ParamExp) (name string, all, ok bool) {
	if p == nil || p.Param == nil || p.Excl || p.Length || p.Width || p.Slice != nil || p.Repl != nil || p.Exp != nil || p.Names != 0 {
		return "", false, false
	}
	if p.Index != nil {
		index, isWord := p.Index.(*syntax.Word)
		if !isWord {
			return "", false, false
		}
		if lit, _ := bareWord(index); lit != "@" && lit != "*" {
			return "", false, false
		}
		return p.Param.Value + everyWord, true, true
	}
	return p.Param.Value, false, true
}

// variable returns the name of a word that is one variable and nothing
// else: $cmd, "$cmd", ${cmd}, or "${cmd[@]}". quoted says that the shell
// keeps its value as one word.
func variable(w *syntax.Word) (name string, quoted, ok bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", false, false
	}
	part := w.Parts[0]
	if q, isQuoted := part.(*syntax.DblQuoted); isQuoted {
		if len(q.Parts) != 1 {
			return "", false, false
		}
		part, quoted = q.Parts[0], true
	}
	p, isParam := part.(*syntax.ParamExp)
	if !isParam {
		return "", false, false
	}
	name, all, ok := simple(p)
	return name, quoted && !all, ok
}

// lookup gives the one value of a variable that stands inside a word, as
// in "$b:$b". A value with a space in it is none: the shell may cut it
// into words.
func (rd *reading) lookup(name string) (string, bool) {
	values := rd.vars[name]
	if rd.blind || len(values) != 1 || strings.ContainsAny(values[0], " \t\n") {
		return "", false
	}
	return values[0], true
}

// words reads the words of a command, with the value of each variable that
// the line set in its place. A variable of a loop has more than one value,
// and a command that is such a variable is read one time for each.
func (rd *reading) words(call *syntax.CallExpr) [][]word {
	var heads [][]word
	// 🔑 A command that is a variable is read with each value that the line
	// gave it, also behind an eval or a source. A value of the line that
	// merges is then seen, and a value that the rule lost is a script that
	// it could not read anyway.
	if name, _, ok := variable(call.Args[0]); ok {
		for _, value := range rd.vars[name] {
			var head []word
			for _, f := range strings.Fields(value) {
				head = append(head, word{text: f, ok: true})
			}
			rd.added += len(head)
			switch {
			case len(head) >= maxWords || rd.added > maxWords:
				rd.long = true
			case len(head) > 0:
				heads = append(heads, head)
			}
		}
	}
	if len(heads) == 0 {
		text, ok := readWord(call.Args[0], false, rd.lookup)
		heads = [][]word{{{text: text, ok: ok}}}
	}
	var rest []word
	for _, w := range call.Args[1:] {
		rest = append(rest, rd.argument(w, len(rest))...)
	}
	out := make([][]word, len(heads))
	for i, head := range heads {
		out[i] = append(head[:len(head):len(head)], rest...)
	}
	return out
}

// argument reads one word of a command that is no program.
func (rd *reading) argument(w *syntax.Word, have int) []word {
	name, quoted, ok := variable(w)
	values, known := rd.vars[name]
	switch {
	case ok && (rd.hidden[name] || shellSets[name]):
		// 🚨 A word with no mark: the line fills this variable itself.
		return []word{{}}
	case !ok || !known || rd.blind || len(values) == 0:
		text, readable := readWord(w, false, rd.lookup)
		return []word{{text: text, ok: readable}}
	case len(values) > 1:
		// 🚨 The rule does not know which value the command gets. The
		// word keeps them all, so that a merge among them is seen:
		// act=view; test -n "$ok" && act=merge; gh pr $act 12.
		return []word{{text: strings.Join(values, " ") + unreadMark}}
	case quoted:
		return []word{{text: values[0], ok: true}}
	}
	fields := strings.Fields(values[0])
	// A variable of many words, used many times, is as many words as the
	// product of the two, in one command or in many.
	rd.added += len(fields)
	if have+len(fields) > maxWords || rd.added > maxWords {
		rd.long = true
		return []word{{text: unreadMark}}
	}
	out := make([]word, len(fields))
	for i, f := range fields {
		out[i] = word{text: f, ok: true, from: name}
	}
	return out
}

// printed reads what the commands inside a word print, for a command that
// runs such a text: eval "$(echo 'gh pr merge 12')", bash <(echo ...), a
// command that is itself a substitution, or a variable that holds one.
func (rd *reading) printed(call *syntax.CallExpr, l *line) {
	// 🚨 An outer command that read this one also read what is inside it.
	// A read at each level parses each quoted line inside again: 32 levels
	// of eval $( around four quoted lines took 28 seconds for 1.4 KiB.
	if rd.read[call] {
		return
	}
	inside := substitutions(call.Args[0])
	if name, _, ok := variable(call.Args[0]); ok {
		inside = append(inside, rd.printedOnce(name)...)
	}
	// $(echo gh pr) merge 12: the words that one command prints, and then
	// the words of the command itself.
	if rest := wordsOf(call)[1:]; len(inside) == 1 && len(rest) > 0 {
		if stmt, ok := inside[0].(*syntax.Stmt); ok {
			if in, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(in.Args) > 1 {
				var said []string
				for _, w := range append(wordsOf(in)[1:], rest...) {
					said = append(said, w.text)
				}
				rd.r.nested(strings.Join(said, " "), l, rd.depth)
			}
		}
	}
	if words, _ := unwrap(wordsOf(call)); len(words) > 0 {
		if prog := program(words[0]); shells[prog] || prog == "eval" || prog == "source" || prog == "." {
			for _, w := range call.Args[1:] {
				inside = append(inside, substitutions(w)...)
				if name, _, ok := variable(w); ok {
					inside = append(inside, rd.printedOnce(name)...)
				}
			}
		}
	}
	if len(inside) > 0 {
		rd.lines(inside, l)
	}
}

// printedOnce returns the commands whose output a variable holds, each one
// time for the line. A read at each use of the variable took 77 seconds for
// a line of 48 KiB.
func (rd *reading) printedOnce(name string) []syntax.Node {
	nodes := rd.printedBy[name][rd.printedRead[name]:]
	rd.printedRead[name] = len(rd.printedBy[name])
	return nodes
}

// startsACommand reports whether a text of more than one word starts with a
// program that the rule reads, or with a runner of one.
func startsACommand(text string) bool {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return false
	}
	prog := program(word{text: fields[0], ok: true})
	return known(prog) || runners[prog]
}

// gitVariable reports whether a variable changes the repository or the
// configuration that git reads: one of git's own, or the directory that
// holds the person's configuration. A variable of git's own that the rule
// does not know is a doubt.
func gitVariable(name string) bool {
	switch {
	case name == "HOME" || name == "XDG_CONFIG_HOME":
		return true
	case !strings.HasPrefix(name, "GIT_"), gitPlainVariables[name]:
		return false
	}
	for _, prefix := range []string{"GIT_TRACE", "GIT_AUTHOR_", "GIT_COMMITTER_"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

// gitPlainVariables are variables of git's own that leave a push where it
// was. GIT_SSH_COMMAND and GIT_ASKPASS are none: they change the account
// that pushes.
var gitPlainVariables = map[string]bool{
	"GIT_TERMINAL_PROMPT": true, "GIT_PAGER": true, "GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true,
	"GIT_MERGE_AUTOEDIT": true, "GIT_CURL_VERBOSE": true, "GIT_OPTIONAL_LOCKS": true, "GIT_ADVICE": true,
}

func isPipe(b *syntax.BinaryCmd) bool { return b.Op == syntax.Pipe || b.Op == syntax.PipeAll }

// pipeline returns the stages of a pipe, first to last, whichever way the
// parser nested them. It marks each node of the pipe in seen.
func pipeline(top *syntax.BinaryCmd, seen map[*syntax.BinaryCmd]bool) []*syntax.Stmt {
	var stages []*syntax.Stmt
	var walk func(s *syntax.Stmt)
	walk = func(s *syntax.Stmt) {
		if s == nil {
			return
		}
		if b, ok := s.Cmd.(*syntax.BinaryCmd); ok && isPipe(b) {
			seen[b] = true
			walk(b.X)
			walk(b.Y)
			return
		}
		stages = append(stages, s)
	}
	seen[top] = true
	walk(top.X)
	walk(top.Y)
	return stages
}

// piped reads a pipe.
//
// 🚨 A shell behind a pipe runs what the pipe gives it: echo 'gh pr merge
// 12' | sh. The rule cannot know that text in general. So it reads each
// word of the pipe, and the words of each command together, as a line that
// the shell may get. It then reads all of it word by word, as it reads a
// line that does not parse.
func (rd *reading) piped(stages []*syntax.Stmt, l *line) {
	shell := false
	for _, stage := range stages[1:] {
		shell = shell || rd.runsItsInput(stage)
	}
	if !shell {
		return
	}
	nodes := make([]syntax.Node, len(stages))
	for i, stage := range stages {
		nodes[i] = stage
	}
	rd.lines(nodes, l)
}

// lines reads the words of every command under the nodes as lines.
//
// 🚨 Each pipe and each command under the nodes is marked as read. A pipe
// in a group in a pipe was read one time for each level, and a line of 1.2
// KiB took 13 seconds.
func (rd *reading) lines(nodes []syntax.Node, l *line) {
	if rd.depth >= maxNested {
		l.find(tooDeep)
		return
	}
	var texts []string
	for _, node := range nodes {
		syntax.Walk(node, func(node syntax.Node) bool {
			switch n := node.(type) {
			case *syntax.BinaryCmd:
				if isPipe(n) {
					rd.seen[n] = true
				}
			case *syntax.Stmt:
				texts = append(texts, rd.heredocs(n)...)
			case *syntax.CallExpr:
				rd.read[n] = true
				if len(n.Args) == 0 {
					break
				}
				// A variable of the line stands for its value here too:
				// cmd="git push origin HEAD:main"; echo "$cmd" | bash.
				for _, words := range rd.words(n) {
					var said []string
					for _, w := range words {
						texts = append(texts, w.text)
						said = append(said, w.text)
					}
					if len(said) > 2 {
						// echo gh pr merge 12: the words after the
						// program are a line.
						texts = append(texts, strings.Join(said[1:], " "))
					}
				}
			}
			return true
		})
	}
	if rd.long {
		l.find(tooLong)
		return
	}
	for _, text := range texts {
		if strings.ContainsAny(text, " \t\n") {
			rd.r.nested(text, l, rd.depth)
		}
	}
	if why := loose(strings.Join(texts, "\n")); why != "" {
		l.find(why)
	}
}

// runsItsInput reports whether a stage of a pipe holds a command that runs
// its standard input: a shell with no line of its own and no script, in a
// group or not, behind xargs or another program, or an eval.
//
// A stage with no such command has no pipe with one either, so each pipe
// inside it is marked as read.
func (rd *reading) runsItsInput(stage *syntax.Stmt) bool {
	found := false
	var pipes []*syntax.BinaryCmd
	syntax.Walk(stage, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BinaryCmd:
			if isPipe(n) {
				pipes = append(pipes, n)
			}
		case *syntax.CallExpr:
			found = found || readsALine(wordsOf(n), 0)
		}
		return !found
	})
	if !found {
		for _, pipe := range pipes {
			rd.seen[pipe] = true
		}
	}
	return found
}

// readsALine reports whether a command runs its standard input. bash -c
// 'sh' does: the shell inside it gets the same input. So does read -r l;
// eval "$l", and script -qc sh /dev/null, where the shell is a later word.
func readsALine(words []word, depth int) bool {
	words, w := unwrap(words)
	if len(words) == 0 {
		return false
	}
	first := program(words[0])
	if first == "eval" {
		return true
	}
	// source /dev/stdin runs its input, and so does a script that the rule
	// cannot read: bash <(cat).
	if (first == "source" || first == ".") && len(words) > 1 && stdinScript(words[1:]) {
		return true
	}
	for i := range words {
		if i > 0 && lineRunners[first] && strings.ContainsAny(words[i].text, " \t\n") {
			// script -qc 'sh -s' /dev/null
			if lineReadsALine(words[i].text, depth) {
				return true
			}
			continue
		}
		if !shells[program(words[i])] {
			continue
		}
		sh := shellLine(words[i+1:])
		switch {
		case sh.stdin, w.fed, stdinScript(sh.script):
			// xargs gives the shell its line: xargs -I{} sh -c '{}'.
			return true
		case sh.line == nil && i > 0:
			// 🚨 The words behind a shell that is no first word can belong to
			// the program in front: script -qc sh /dev/null. The file is the
			// program's, and the shell runs its input.
			return true
		case sh.line == nil || i > 0:
			continue
		}
		if lineReadsALine(sh.line.text, depth) {
			return true
		}
	}
	return false
}

// stdinScript reports whether the script of a shell is its standard input,
// or a file that the rule cannot read.
func stdinScript(script []word) bool {
	if len(script) == 0 {
		return false
	}
	switch script[0].text {
	case "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0":
		return true
	}
	return !script[0].ok
}

// lineReadsALine reports whether a line in one word holds a command that
// runs its standard input.
func lineReadsALine(text string, depth int) bool {
	if depth >= maxNested {
		return false
	}
	if pastBounds(text) {
		return true
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
	if err != nil {
		return false
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok {
			found = found || readsALine(wordsOf(call), depth+1)
		}
		return !found
	})
	return found
}

// wordsOf reads the words of a command.
func wordsOf(call *syntax.CallExpr) []word {
	words := make([]word, len(call.Args))
	for i, w := range call.Args {
		words[i].text, words[i].ok = wordText(w)
	}
	return words
}

// shellLine reads the words after a shell, as the shell does. Its options
// come first. With -c among them the first word after the options is the
// line, and line points at it. With no -c that word starts the script and
// its arguments.
//
// 🚨 An option can take the next word, and a shell lets options stand in one
// word: bash -euo pipefail -c 'a line'. Each o in such a word takes one
// word. Without that count the value reads as a script, and the line after
// it is never read.
//
// A lone - ends the options as -- does. With -s the shell runs its standard
// input, and the words after the options are arguments and no script.
func shellLine(words []word) shellWords {
	c, s := false, false
	i := 0
options:
	for ; i < len(words); i++ {
		w := words[i]
		if !w.ok {
			// A word that the rule cannot read ends the options.
			break
		}
		name, _, attached := strings.Cut(w.text, "=")
		switch {
		case w.text == "--" || w.text == "-":
			i++
			break options
		case strings.HasPrefix(w.text, "--"):
			if (name == "--rcfile" || name == "--init-file") && !attached {
				i++
			}
		case (strings.HasPrefix(w.text, "-") || strings.HasPrefix(w.text, "+")) && len(w.text) > 1:
			if w.text[0] == '-' {
				c = c || strings.Contains(w.text, "c")
				s = s || strings.Contains(w.text, "s")
			}
			i += strings.Count(strings.ToLower(w.text), "o")
		default:
			break options
		}
	}
	var rest []word
	if i < len(words) {
		rest = words[i:]
	}
	switch {
	case c && len(rest) > 0:
		return shellWords{line: &rest[0]}
	case c:
		return shellWords{}
	case s || len(rest) == 0:
		return shellWords{stdin: true}
	}
	return shellWords{script: rest}
}

// shellWords is what the words after a shell say: the line of -c, or a
// script and its arguments, or that the shell runs its standard input.
type shellWords struct {
	line   *word
	script []word
	stdin  bool
}

// fed reads the line that a shell gets on its standard input: a here
// document, a here string, or what a command prints.
func (rd *reading) fed(stmt *syntax.Stmt, l *line) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(stmt.Redirs) == 0 {
		return
	}
	// 🚨 A shell inside a substitution that feeds a shell was read with
	// that one.
	if rd.read[call] {
		return
	}
	words, _ := unwrap(wordsOf(call))
	if len(words) == 0 {
		return
	}
	// source /dev/stdin <<< 'gh pr merge 12' runs its input as a shell does.
	if prog := program(words[0]); !shells[prog] && prog != "source" && prog != "." {
		return
	}
	for _, text := range rd.heredocs(stmt) {
		rd.r.nested(text, l, rd.depth)
	}
	// sh < <(echo 'gh pr merge 12'), and a substitution in a here document.
	var inside []syntax.Node
	for _, from := range stmt.Redirs {
		if from.Word != nil && from.Hdoc == nil {
			inside = append(inside, substitutions(from.Word)...)
		}
		if from.Hdoc != nil {
			inside = append(inside, substitutions(from.Hdoc)...)
		}
	}
	if len(inside) > 0 {
		rd.lines(inside, l)
	}
}

// heredocs returns the text of each here document and here string of a
// statement. A variable of the line stands for its value: bash <<< "$cmd".
// Another one leaves a mark in the text.
func (rd *reading) heredocs(stmt *syntax.Stmt) []string {
	var texts []string
	for _, from := range stmt.Redirs {
		body := from.Hdoc
		if from.Op == syntax.WordHdoc {
			body = from.Word
		}
		if body == nil {
			continue
		}
		if name, _, ok := variable(body); ok && !rd.blind && len(rd.vars[name]) > 0 {
			texts = append(texts, rd.vars[name]...)
			continue
		}
		text, _ := readWord(body, from.Op != syntax.WordHdoc, nil)
		texts = append(texts, text)
	}
	return texts
}

// call reads one command: the command itself, behind the runners in front
// of it.
func (r MergeRule) call(words []word, gitEnv bool, l *line, depth int) {
	words, w := unwrap(words)
	if len(words) == 0 {
		return
	}
	// 🚨 A runner in front may run nothing: env --version git checkout -b
	// fix/z prints. So only a command with no runner is direct.
	c := called{gitEnv: gitEnv || w.gitEnv, fed: w.fed, direct: !w.any}
	if w.replace != "" {
		// 🚨 xargs -I{} gh pr {} 12: the pipe gives the word.
		words = slices.Clone(words)
		for i := range words {
			if strings.Contains(words[i].text, w.replace) {
				words[i] = word{text: strings.ReplaceAll(words[i].text, w.replace, "")}
			}
		}
	}
	if words[0].ok && words[0].text != "[" && strings.ContainsAny(words[0].text, "*?[") {
		// 🚨 /usr/bin/gi[t] is /usr/bin/git: the shell reads the word as a
		// pattern for the names of files. So the rule cannot read it.
		words = append([]word{{text: words[0].text}}, words[1:]...)
	}
	first := program(words[0])
	for _, word := range words {
		// tee -a .git/config, cp other.config .git/config
		if first != "git" && insideGit(word.text) {
			l.reconfigured = true
			l.moveBranch()
			break
		}
	}
	for _, dir := range w.dirs {
		// env -C ../other git push
		l.cd(dir)
	}
	if !words[0].ok {
		// A program that the rule cannot read: "$(command -v git)" push
		// origin HEAD:main. A push or a recipe behind it is a doubt.
		for _, w := range words[1:] {
			switch {
			case w.ok && fits(w.text, "push"):
				l.add(push{unread: true})
			case w.ok && recipeWord.MatchString(strings.ToLower(w.text)):
				l.find("the recipe " + w.text + " merges a pull request")
			}
		}
	}
	r.reads(words, c, l, depth)
	// 🚨 A program in front that the rule does not know can run the command
	// behind it: flock, ssh-agent, busybox, find -exec. A runner's option
	// that the rule does not know leaves its value as the program. So a
	// later word that names a program the rule knows starts a command too.
	//
	// man git push and grep -rn git push docs look a text up, and they run
	// nothing.
	c.direct = false
	inside := 0
	//
	// 🚨 A function or an alias of the line can give such a name to another
	// program: grep() { "$@"; }.
	looks := looksUp[program(words[0])] && !l.functions
	for i := 1; i < len(words) && l.why == "" && !looks; i++ {
		if !known(program(words[i])) {
			continue
		}
		// Each such word is a read of the words after it.
		if inside++; inside > maxInside {
			l.find(tooMany)
			return
		}
		r.reads(words[i:], c, l, depth)
	}
	// A line in one word.
	switch prog := program(words[0]); {
	case lineRunners[prog]:
		for _, w := range words[1:] {
			if strings.ContainsAny(w.text, " \t\n") {
				r.nested(w.text, l, depth)
			}
		}
	case w.any && startsACommand(words[0].text):
		// env -S 'git push origin HEAD:main'
		r.nested(words[0].text, l, depth)
	}
}

// fits reports whether a word is a name, or a pattern of the shell for the
// names of files that the name fits: pus[h] is push beside a file of that
// name.
func fits(w, name string) bool {
	if w == name {
		return true
	}
	if !strings.ContainsAny(w, "*?[") {
		return false
	}
	ok, err := path.Match(w, name)
	return ok || err != nil
}

// looksUp are programs that look a text up among their words.
var looksUp = map[string]bool{
	"man": true, "which": true, "type": true, "whereis": true, "whatis": true, "apropos": true, "info": true,
	"help": true, "grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
}

// maxInside bounds the commands that the rule reads inside one command.
const maxInside = 16

// tooMany is what the rule says of a command with more than maxInside names
// of programs among its words.
const tooMany = "terva cannot read a command that names this many programs. Run each command by itself"

// lineRunners are programs that take a command line in one word: trap
// 'a line' EXIT, ssh host 'a line', script -qc 'a line', man -P 'a line'.
var lineRunners = map[string]bool{
	"trap": true, "ssh": true, "su": true, "runuser": true, "script": true, "watch": true,
	"parallel": true, "flock": true, "tmux": true, "screen": true, "man": true,
}

// known reports whether prog is a program that the rule reads the
// arguments of. A word such as pr-merge is none: behind grep it is a text
// that a command looks for.
func known(prog string) bool {
	return prog == "git" || prog == "eval" || shells[prog] || taskRunners[prog] || forgeCLIs[prog]
}

// called is what the runners in front of a command say about it.
type called struct {
	// gitEnv is set when an assignment gives git a variable of its own.
	gitEnv bool
	// fed is set when xargs adds words that the rule cannot see.
	fed bool
	// direct is set for the command as the shell runs it. It is not set for
	// a command that the rule found among the words of another program.
	direct bool
}

// reads takes a command whose first word is its program.
func (r MergeRule) reads(words []word, c called, l *line, depth int) {
	texts := make([]string, len(words))
	for i, w := range words {
		texts[i] = w.text
	}
	said := strings.Join(texts, " ")
	switch {
	case r.configured(said):
		l.find("it matches a merge command of the user configuration")
	case namesEndpoint(said):
		// The words with their quotes removed: .../pulls/12/'merge'.
		l.find("it names a forge's merge endpoint")
	}
	prog := program(words[0])
	rest := words[1:]

	switch {
	case shells[prog]:
		// sh -c 'a line', or a script that is the command. A variable of
		// git's own in front of the shell reaches each git inside it.
		l.gitEnv = l.gitEnv || c.gitEnv
		sh := shellLine(rest)
		switch {
		case sh.line != nil:
			r.nested(sh.line.text, l, depth)
		case len(sh.script) == 0:
		case depth >= maxNested:
			l.find(tooDeep)
		default:
			if !sh.script[0].ok {
				// bash $OPTS -c 'a line': a later word may be the line.
				for _, w := range sh.script[1:] {
					r.nested(w.text, l, depth)
				}
			}
			r.call(sh.script, c.gitEnv, l, depth+1)
		}
	case prog == "eval":
		l.gitEnv = l.gitEnv || c.gitEnv
		r.nested(strings.Join(texts[1:], " "), l, depth)
	case prog == "alias":
		// alias p='git push origin HEAD:main': the value is a line. The
		// alias is a name that the line gives, as a function is.
		l.functions = true
		for _, w := range rest {
			if _, value, ok := strings.Cut(w.text, "="); ok {
				r.nested(value, l, depth)
			}
		}
	case prog == "export" || prog == "declare" || prog == "typeset" || prog == "readonly":
		// builtin export GIT_DIR=...: the parser reads this one as a
		// command, and export GIT_DIR=... as a declaration.
		for _, w := range rest {
			if name, _, _ := strings.Cut(w.text, "="); gitVariable(name) {
				l.gitEnv = true
			}
		}
	case prog == "cd" || prog == "pushd" || prog == "popd":
		// cd with no word goes home, and popd goes back. cd -P DIR follows
		// links by another way. cd - is no full path.
		if prog == "popd" || len(rest) != 1 {
			l.elsewhere = true
			break
		}
		l.cd(rest[0])
	case prog == "git" || prog == "hub":
		// hub takes each command of git.
		r.git(rest, c, l, depth)
	}
	if l.why != "" {
		return
	}

	if recipeWord.MatchString(prog) {
		l.find("the command " + prog + " merges a pull request")
		return
	}
	if taskRunners[prog] && !printsARecipe(prog, rest) {
		for _, w := range rest {
			// A variable with two values keeps them both in its text.
			for _, name := range strings.Fields(strings.ReplaceAll(w.text, unreadMark, " ")) {
				if (w.ok || strings.Contains(w.text, " ")) && recipeWord.MatchString(strings.ToLower(name)) {
					l.find("the recipe " + name + " merges a pull request")
					return
				}
			}
		}
	}
	// A forge's CLI: pr, pull or mr, and then merge, near the front of the
	// command. git has no such command, and git pull takes a remote of any
	// name.
	if prog == "git" {
		return
	}
	// 🔑 The search runs for every other program, and not for a list of
	// CLIs. A script such as scripts/pr.sh has any name. A program that
	// prints is read too, because $(echo gh pr merge 12) runs what echo
	// printed. The cost is a refusal of echo pr merge.
	//
	// 🚨 An option can stand between the two words, and so can its value:
	// gh pr --repo owner/repo merge 12. The rule does not know which
	// options of which CLI take a value. So a word behind an option may be
	// a value, and it is skipped unless it is the word merge itself. A word
	// behind no option is a subcommand, and it ends the search: gh pr list
	// --search merge lists.
	//
	// A word that the rule cannot read, in the place of a subcommand, is
	// merge when its text says so: gh pr {merge,--squash} 12. A variable
	// there is most often a number, as in tea pr $PR.
	const unreadWord = "\x00"
	plain, value := []string{prog}, []bool{false}
	behind := false
	for _, w := range rest {
		switch {
		case !w.ok && (strings.Contains(strings.ToLower(w.text), "merge") || (forgeCLIs[prog] && !strings.Contains(w.text, unreadMark))):
			// The second case is a word with no variable that the rule
			// still cannot read: gh pr $'m\x65rge' 12.
			plain, value = append(plain, "merge"), append(value, behind)
			behind = false
		case !w.ok:
			plain, value = append(plain, unreadWord), append(value, behind)
			behind = false
		case strings.HasPrefix(w.text, "-"):
			behind = !strings.Contains(w.text, "=")
		default:
			text := strings.ToLower(w.text)
			if name := program(w); prWords[name] {
				// flock /tmp/l ./scripts/pr.sh merge 12
				text = name
			}
			plain, value = append(plain, text), append(value, behind)
			behind = false
		}
	}
	if forgeCLIs[prog] && len(plain) > 2 {
		switch {
		case plain[1] == "search":
			// gh search prs merge rule looks for the words.
			return
		case plain[1] == "alias" && plain[2] == "set" && len(rest) > 3:
			// gh alias set land 'pr merge': the alias is a command of gh.
			value := strings.TrimSpace(rest[len(rest)-1].text)
			if line, ok := strings.CutPrefix(value, "!"); ok {
				r.nested(line, l, depth)
			} else {
				r.nested(prog+" "+value, l, depth)
			}
			return
		}
	}
	if c.fed && forgeCLIs[prog] && (len(plain) == 1 || prWords[plain[len(plain)-1]]) {
		// echo "pr merge 12" | xargs gh: the pipe holds the subcommand.
		l.find("xargs gives " + prog + " its command, and it may be merge")
		return
	}
	for i := 0; i+1 < len(plain) && i < 6; i++ {
		if !prWord(plain[i]) {
			continue
		}
		for j := i + 1; j < len(plain); j++ {
			if mergeWord(prog, plain[j]) {
				l.find("it merges a pull request (" + strings.ReplaceAll(strings.Join(plain[:j+1], " "), unreadWord, "...") + ")")
				return
			}
			if forgeCLIs[prog] && (plain[j] == "checkout" || plain[j] == "co") {
				// gh pr checkout 12 stands on the branch of another's
				// pull request.
				l.moveBranch()
			}
			if !value[j] {
				break
			}
		}
	}
}

// prWord reports whether a word names a pull request, as it stands or as a
// pattern of the shell: p? is pr beside a file of that name.
func prWord(w string) bool {
	if prWords[w] {
		return true
	}
	if !strings.ContainsAny(w, "*?[") {
		return false
	}
	for name := range prWords {
		if fits(w, name) {
			return true
		}
	}
	return false
}

// printsARecipe reports whether just or make shows a recipe and does not
// run it: just --show pr-merge, make -n pr-merge.
//
// 🚨 just takes its own options in front of the recipe. A word behind the
// recipe is an argument of the recipe: just pr-merge 12 -n runs it.
//
// 🚨 The option counts only as the first word. Behind another option it may
// be that option's value: make -o -n pr-merge runs the recipe, and so does
// just --shell-arg -n pr-merge for a recipe with an interpreter of its own.
func printsARecipe(prog string, words []word) bool {
	if len(words) == 0 || !words[0].ok {
		return false
	}
	switch w := words[0].text; prog {
	case "just":
		return w == "--show" || w == "-s" || w == "--dry-run" || w == "-n" || w == "--list" || w == "-l"
	case "make", "gmake":
		return w == "-n" || w == "--dry-run" || w == "--just-print"
	}
	return false
}

// mergeWord reports whether w is a CLI's word for a merge. tea and glab each
// have a second one.
//
// A word with a pattern in it is each name that the pattern fits: merg? is
// merge beside a file of that name.
func mergeWord(prog, w string) bool {
	names := []string{"merge"}
	switch prog {
	case "tea":
		names = append(names, "m")
	case "glab":
		names = append(names, "accept")
	}
	if slices.Contains(names, w) {
		return true
	}
	if !strings.ContainsAny(w, "*?[") {
		return false
	}
	for _, name := range names {
		if fits, err := path.Match(w, name); fits || err != nil {
			return true
		}
	}
	return false
}

// nested reads a line inside a line: the argument of sh -c or of eval, and
// what a shell reads from a here document.
//
// 🚨 A line that the rule cannot read gets no pass here. One that does not
// parse is read as the top line is, word by word. One that is too deep is
// refused, because a shell around a command costs a model nothing.
func (r MergeRule) nested(text string, l *line, depth int) {
	switch {
	case strings.TrimSpace(text) == "":
	case depth >= maxNested:
		l.find(tooDeep)
	case namesEndpoint(text):
		l.find("it names a forge's merge endpoint")
	case r.configured(text):
		l.find("it matches a merge command of the user configuration")
	case !r.parse(text, l, depth+1):
		r.unparsed(text, l, depth+1)
	}
}

// wrapped is what unwrap found in front of a command.
type wrapped struct {
	// any is set when a runner stood in front. gitEnv is set when env gave
	// git a variable of its own. fed is set for xargs, which adds words
	// from its input.
	any, gitEnv, fed bool
	// dirs holds each directory that a runner changes to: env -C DIR.
	dirs []word
	// replace is the text that xargs puts its input in the place of.
	replace string
}

// unwrap removes the runners in front of a command: sudo, env, timeout and
// the rest, with their options, the values of those options, their numbers,
// and env's assignments. A runner can stand behind a runner.
func unwrap(words []word) ([]word, wrapped) {
	var w wrapped
	for len(words) > 0 && words[0].ok && runners[program(words[0])] {
		runner := program(words[0])
		w.any = true
		w.fed = w.fed || runner == "xargs"
		words = words[1:]
	options:
		for len(words) > 0 && words[0].ok && words[0].text != "" {
			t := words[0].text
			name, _, attached := strings.Cut(t, "=")
			switch {
			case t == "--":
				words = words[1:]
				break options
			case strings.HasPrefix(t, "-"):
				if runner == "xargs" {
					switch {
					case t == "-I" && len(words) > 1:
						w.replace = words[1].text
					case name == "--replace" && attached:
						w.replace = t[len(name)+1:]
					case t == "-i" || t == "--replace":
						w.replace = "{}"
					case strings.HasPrefix(t, "-I") || strings.HasPrefix(t, "-i"):
						w.replace = t[2:]
					}
				}
				chdir := (runner == "env" && (name == "-C" || name == "--chdir")) || (runner == "sudo" && (name == "-D" || name == "--chdir"))
				if chdir && attached {
					w.dirs = append(w.dirs, word{text: t[len(name)+1:], ok: true})
				}
				if !attached && slices.Contains(runnerValueOptions[runner], t) && len(words) > 1 {
					if chdir {
						w.dirs = append(w.dirs, words[1])
					}
					words = words[1:]
				}
			case t[0] >= '0' && t[0] <= '9':
			case attached && name != "" && !strings.ContainsAny(name, "/-"):
				w.gitEnv = w.gitEnv || gitVariable(name)
			default:
				break options
			}
			words = words[1:]
		}
	}
	return words, w
}

// program names the program of a command: the last part of its path, in
// lower case, with no extension of a script. A word that cannot be read
// names no program.
func program(w word) string {
	if !w.ok {
		return ""
	}
	p := strings.ToLower(path.Base(strings.ReplaceAll(w.text, `\`, "/")))
	for _, ext := range []string{".sh", ".bash", ".zsh", ".py", ".rb", ".js", ".mjs", ".ts", ".exe", ".cmd", ".bat", ".ps1"} {
		if strings.HasSuffix(p, ext) {
			return strings.TrimSuffix(p, ext)
		}
	}
	return p
}

// git reads the words after git.
func (r MergeRule) git(words []word, c called, l *line, depth int) {
	// An option of git itself can point the command at another repository
	// or change its configuration.
	redirected := c.gitEnv
	// 🚨 lost is set for an option that the rule does not know. Such an
	// option may take the next word, as --attr-source HEAD does. The rule
	// then does not know which word is the subcommand.
	lost := false
	i := 0
	for ; i < len(words); i++ {
		w := words[i]
		if !w.ok || !strings.HasPrefix(w.text, "-") {
			break
		}
		name, value, attached := strings.Cut(w.text, "=")
		switch {
		case gitPlainOptions[w.text]:
		case name == "-C" && !attached:
			// git -C DIR is cd DIR.
			if i++; i < len(words) {
				l.cd(words[i])
			}
		case strings.HasPrefix(w.text, "-C") && !strings.HasPrefix(w.text, "--"):
			l.cd(word{text: w.text[2:], ok: true})
		case name == "-c" && !attached:
			value = ""
			if i++; i < len(words) {
				value = words[i].text
				redirected = redirected || !words[i].ok
			}
			redirected = r.setting(value, l, depth) || redirected
		case strings.HasPrefix(w.text, "-c") && !strings.HasPrefix(w.text, "--"):
			// -cKEY=VALUE in one word.
			redirected = r.setting(w.text[2:], l, depth) || redirected
		case gitValueOptions[name]:
			redirected = true
			if !attached {
				i++
			}
		default:
			redirected, lost = true, true
		}
	}
	// 🚨 An option at the end that takes a word leaves i past the words:
	// git --foo -c.
	if i > len(words) {
		i = len(words)
	}
	if lost {
		// Every push on the line is in doubt, and so is the branch at work.
		l.reconfigured = true
		l.moveBranch()
		if hasWord(words[i:], "push") || hasWord(words[i:], "send-pack") {
			l.add(push{unread: true})
			return
		}
	}
	if i >= len(words) || !words[i].ok || strings.ContainsAny(words[i].text, "*?[") {
		// xargs git gets its subcommand from the pipe, and git "$sub" from a
		// variable. Each may be a push, and so may git pus[h] beside a file
		// with the name push.
		if c.fed || i < len(words) {
			l.add(push{unread: true})
		}
		return
	}
	sub, rest := strings.ToLower(words[i].text), words[i+1:]
	switch sub {
	case "push":
		p := readPush(rest)
		// xargs adds words to the push that the rule cannot see, and one
		// of them may be --no-dry-run.
		p.unread = p.unread || redirected || c.fed
		p.inert = p.inert && !c.fed
		l.add(p)
	case "send-pack":
		l.add(push{unread: true})
	case "subtree":
		// git subtree push sends a branch that it makes itself.
		if hasWord(rest, "push") {
			l.add(push{unread: true})
		}
	case "checkout", "switch":
		name, start, made := createsABranch(sub, rest)
		switch {
		case made:
			// git checkout -b fix/y: the branch at work is fix/y.
			l.events++
			l.log = append(l.log, branchEvent{
				at: l.events, name: name, create: true, fromStart: start,
				sure: c.direct && !redirected && !l.piped, chain: l.chain, loop: l.inLoop,
			})
		case sub == "checkout" && pathsFollow(rest):
			// git checkout -- a-file changes no branch, and neither does
			// git checkout main -- a-file. git checkout main -- changes it.
		default:
			if branch, ok := checksOut(rest); ok {
				l.checkOut(branch, c.direct && !redirected)
			} else {
				l.moveBranch()
			}
		}
	case "rebase":
		// git rebase feat/x main checks main out. The value of an option
		// such as --onto is no branch to check out.
		//
		// 🚨 With --root the first word is that branch: git rebase --root
		// main checks main out.
		var plain []word
		at := 1
		for j := 0; j < len(rest); j++ {
			w := rest[j]
			switch {
			case w.ok && rebaseValueOptions[w.text]:
				j++
			case w.ok && abbreviates(w.text, "--root"):
				at = 0
			case !w.ok || !strings.HasPrefix(w.text, "-"):
				plain = append(plain, w)
			}
		}
		switch {
		case len(plain) == at+1 && plain[at].ok:
			l.checkOut(plain[at].text, c.direct && !redirected)
		case len(plain) > at:
			l.moveBranch()
		}
	case "stash":
		// git stash branch NAME makes NAME and checks it out.
		if hasWord(rest, "branch") {
			l.moveBranch()
		}
	case "bisect":
		// git bisect reset main checks main out.
		l.moveBranch()
	case "pull", "fetch":
		for _, w := range rest {
			if name, _, _ := strings.Cut(w.text, "="); abbreviates(name, "--set-upstream") {
				l.moveBranch()
			}
		}
	case "branch":
		for _, w := range rest {
			name, _, _ := strings.Cut(w.text, "=")
			switch {
			case abbreviates(name, "--move"), abbreviates(name, "--set-upstream-to"), abbreviates(name, "--unset-upstream"):
				l.moveBranch()
			case strings.HasPrefix(w.text, "-") && !strings.HasPrefix(w.text, "--") && strings.ContainsAny(w.text, "mMu"):
				// Options in one word, and a value in the same word:
				// -Mf, -uorigin/main.
				l.moveBranch()
			}
		}
	case "tag":
		// git tag v1 && git push origin v1 pushes a tag. git tag -l feat/x
		// lists, and it makes no tag.
		//
		// 🚨 The first word that is no option and no value of one is the
		// tag. In git tag -m other v1 the word other is a message. A push of
		// the name other would then pass as a push of a tag.
		//
		// 🚨 An option that makes no tag can stand behind the name: git
		// tag other -l lists.
		chain := 0
		if c.direct && !redirected && !l.piped {
			chain = l.chain
		}
		name, makes := "", true
		for j := 0; j < len(rest); j++ {
			w := rest[j]
			switch {
			case !w.ok:
				makes = false
			case tagValue(w.text):
				j++
			case tagMake(w.text):
			case strings.HasPrefix(w.text, "-"):
				makes = false
			case name == "":
				name = w.text
			}
		}
		if makes && name != "" {
			l.tag(name, chain)
		}
	case "config":
		l.reconfigured = l.reconfigured || placesAPush(rest)
		// git config alias.p 'push origin HEAD:main' && git p
		for j, w := range rest {
			l.autocorrect = l.autocorrect || strings.EqualFold(w.text, "help.autocorrect")
			if strings.HasPrefix(strings.ToLower(w.text), "alias.") && j+1 < len(rest) {
				r.alias(rest[j+1].text, l, depth)
			}
		}
	case "remote":
		for _, w := range rest {
			switch w.text {
			case "add", "set-url", "rename", "remove", "rm", "set-head", "set-branches":
				l.reconfigured = true
			}
		}
	case "symbolic-ref":
		n := 0
		for _, w := range rest {
			if !strings.HasPrefix(w.text, "-") {
				n++
			}
		}
		if n >= 2 {
			l.moveBranch()
		}
	default:
		// 🚨 With help.autocorrect git runs the command that a wrong word
		// is nearest to: git psuh is git push.
		if l.autocorrect {
			l.add(push{unread: true})
		}
	}
	if gitRunsALine[sub] {
		// git rebase -x 'git push origin HEAD:main' HEAD~1
		for _, w := range rest {
			if strings.ContainsAny(w.text, " \t\n") {
				r.nested(w.text, l, depth)
			}
		}
	}
}

// tagMake reports whether a word is an option of git tag that makes a tag
// and takes no word, alone or with others in one word: -a, -af.
func tagMake(w string) bool {
	switch w {
	case "--annotate", "--sign", "--force":
		return true
	}
	return len(w) > 1 && w[0] == '-' && strings.Trim(w[1:], "asf") == ""
}

// tagValue reports whether a word is such an option that takes the next
// word: -m, and -am for -a -m.
func tagValue(w string) bool {
	switch w {
	case "--message", "--file", "--local-user":
		return true
	}
	if len(w) < 2 || w[0] != '-' || w[1] == '-' {
		return false
	}
	last := w[len(w)-1]
	return strings.Trim(w[1:len(w)-1], "asf") == "" && (last == 'm' || last == 'F' || last == 'u')
}

// checksOut reads git checkout NAME and git switch NAME: one word beside
// -q and -f. Another option is read as a name, and it is the name of no
// branch, so the line then changes the branch.
func checksOut(words []word) (string, bool) {
	name := ""
	for _, w := range words {
		switch {
		case !w.ok || name != "":
			return "", false
		case w.text == "-q" || w.text == "--quiet" || w.text == "-f" || w.text == "--force":
		default:
			name = w.text
		}
	}
	return name, name != ""
}

// gitRunsALine holds the commands of git that run a command line of the
// person: rebase --exec, submodule foreach, bisect run.
var gitRunsALine = map[string]bool{"rebase": true, "submodule": true, "bisect": true, "filter-branch": true}

// rebaseValueOptions are the options of git rebase that take the next word.
var rebaseValueOptions = map[string]bool{
	"--onto": true, "-x": true, "--exec": true, "-s": true, "--strategy": true, "-X": true,
	"--strategy-option": true, "-C": true, "--whitespace": true,
}

// setting reads the value of git -c, which is a key and its value. It
// reports whether the setting can place a push.
//
// An alias there is a command that the same git runs. A key such as
// color.ui places nothing, so git -c color.ui=never push is a plain push.
func (r MergeRule) setting(text string, l *line, depth int) bool {
	key, value, ok := strings.Cut(text, "=")
	if !ok || strings.Contains(text, unreadMark) {
		return true
	}
	section, _, _ := strings.Cut(strings.ToLower(key), ".")
	if section == "alias" {
		r.alias(value, l, depth)
		return false
	}
	l.autocorrect = l.autocorrect || strings.EqualFold(key, "help.autocorrect")
	if strings.ContainsAny(value, " \t\n") {
		// git -c core.pager='git push origin HEAD:main' log runs the value.
		r.nested(value, l, depth)
	}
	return configPlacesAPush[section]
}

// createsABranch reads git checkout -b NAME and git switch -c NAME. It
// returns the new branch, and whether it starts at a branch that it may
// then follow.
func createsABranch(sub string, words []word) (name string, start, ok bool) {
	at := -1
	for i, w := range words {
		if !w.ok {
			return "", false, false
		}
		switch {
		case w.text == "-B" || w.text == "-C" || w.text == "--force-create":
			// 🚨 git checkout -B feat/x takes a branch that exists, and
			// that branch keeps what it follows. A probe moved main with
			// git checkout -B feat/x && git push in the upstream mode.
			at, start = i, true
		case sub == "checkout" && (w.text == "-b" || w.text == "--orphan"),
			sub == "switch" && (w.text == "-c" || w.text == "--create" || w.text == "--orphan"):
			at = i
		case w.text == "-t" || strings.HasPrefix(w.text, "--track"):
			start = true
		}
	}
	if at < 0 || at+1 >= len(words) || strings.HasPrefix(words[at+1].text, "-") {
		return "", false, false
	}
	for _, w := range words[at+2:] {
		start = start || !strings.HasPrefix(w.text, "-")
	}
	return words[at+1].text, start, true
}

// alias reads the value of an alias of git as the command that it runs. The
// rule does not follow the alias to its use. So an alias that merges is
// refused where the line makes it.
func (r MergeRule) alias(value string, l *line, depth int) {
	if line, ok := strings.CutPrefix(strings.TrimSpace(value), "!"); ok {
		r.nested(line, l, depth)
		return
	}
	r.nested("git "+value, l, depth)
	// An alias for push takes its refspec where the alias is used:
	// git -c alias.p=push p origin HEAD:main.
	if fields := strings.Fields(value); len(fields) > 0 && (fields[0] == "push" || fields[0] == "send-pack") {
		l.add(push{unread: true})
	}
}

// pathsFollow reports whether a word stands behind -- in the words of git
// checkout. The words there are files, and the branch at work stays.
func pathsFollow(words []word) bool {
	for i, w := range words {
		if w.ok && w.text == "--" {
			return i+1 < len(words)
		}
	}
	return false
}

// abbreviates reports whether name is a long option of git, or the front of
// one. git takes --al for --all and --mirr for --mirror. A front that fits
// two options is an error in git, so the rule may read it as either.
func abbreviates(name, option string) bool {
	return len(name) >= 3 && strings.HasPrefix(option, name)
}

// placesAPush reports whether the words after git config change a setting
// that places a push: push.default, a remote, or a branch's upstream. A read
// changes nothing, and neither does a key such as user.name. A key that the
// rule cannot read may be any key.
//
// 🚨 The rule does not find the key by its place. An option of git config can
// take the next word, as --file .git/config does, and that word then stands
// where the key was looked for. So every word is a key that it may be. A
// value that reads as such a key costs one refusal.
func placesAPush(words []word) bool {
	if len(words) == 0 {
		return false
	}
	// git config get and git config list take the subcommand as their first
	// word. In another place the word is a value or a file's name.
	if words[0].ok && (words[0].text == "get" || words[0].text == "list") {
		return false
	}
	// plain counts the words that are no option. keyed is set behind a key
	// that the rule read and that places no push: the next word is its
	// value. doubt is set behind an option that may take a word, and writes
	// for an option that makes the command a change.
	names, edits, doubt, writes, keyed := false, false, false, false, false
	plain := 0
	for i, w := range words {
		if !w.ok {
			// git config user.email "$EMAIL" sets a value. git config "$key"
			// upstream may set any key.
			plain++
			names, doubt = names || !keyed, true
			continue
		}
		// The word behind --file or --blob is a name, and no option.
		file := i > 0 && words[i-1].ok && configFileOptions[words[i-1].text]
		if strings.HasPrefix(w.text, "-") && !file {
			switch {
			case configReads[w.text]:
				// 🚨 --comment --get writes a comment with the text --get.
				// A read counts only behind options that take no word.
				if !doubt {
					return false
				}
			case w.text == "-e" || w.text == "--edit":
				edits = true
			case configPlainOptions[w.text] || configFileOptions[w.text] || strings.Contains(w.text, "="):
			case configWrites[w.text]:
				writes = true
			default:
				doubt, writes = true, true
			}
			continue
		}
		if file {
			continue
		}
		plain++
		section, _, dotted := strings.Cut(strings.ToLower(w.text), ".")
		switch {
		case configPlacesAPush[section]:
			names = true
		case dotted && !doubt:
			// 🚨 Behind an option that may take a word, a word with a dot is
			// no key: --comment a.b "$key" upstream.
			keyed = true
		}
	}
	if edits {
		return true
	}
	// git config remote.origin.url prints the value: one word is a read.
	return names && (plain > 1 || writes)
}

// configFileOptions are the options of git config that take a file's name.
var configFileOptions = map[string]bool{"-f": true, "--file": true, "--blob": true}

// configReads are the options of git config that make it a read.
var configReads = map[string]bool{
	"--get": true, "--get-all": true, "--get-regexp": true, "--get-urlmatch": true,
	"--get-color": true, "--get-colorbool": true, "--list": true, "-l": true,
}

// configWrites are the options of git config that make it a change and
// that take no word.
var configWrites = map[string]bool{
	"--add": true, "--replace-all": true, "--unset": true, "--unset-all": true,
	"--remove-section": true, "--rename-section": true,
}

// configPlainOptions are the options of git config that take no word. Every
// other option may take the next word as its value.
var configPlainOptions = map[string]bool{
	"--global": true, "--local": true, "--system": true, "--worktree": true, "--show-origin": true,
	"--show-scope": true, "-z": true, "--null": true, "--name-only": true, "--includes": true,
	"--no-includes": true, "--all": true, "--fixed-value": true, "--bool": true, "--int": true,
	"--bool-or-int": true, "--path": true, "--expiry-date": true,
}

// configPlacesAPush holds the sections of git's configuration that place a
// push. An include brings in a file that can hold each of the others, and a
// url section sends a remote's name to another repository.
var configPlacesAPush = map[string]bool{
	"push": true, "remote": true, "branch": true, "include": true, "includeif": true, "url": true,
}

func hasWord(words []word, text string) bool {
	for _, w := range words {
		if w.ok && w.text == text {
			return true
		}
	}
	return false
}

// readPush reads the words after git push.
func readPush(words []word) push {
	var p push
	var plain []string
	named, tags, deletes, inert := false, false, false, false
	for i := 0; i < len(words); i++ {
		w := words[i]
		if strings.Contains(w.text, startMark) {
			p.early = true
			w.text = strings.ReplaceAll(w.text, startMark, branchMark)
		}
		if !w.ok {
			p.unread = true
			continue
		}
		if !strings.HasPrefix(w.text, "-") {
			// 🚨 The shell reads ? and [ as a pattern for the names of files:
			// HEAD:mai? is HEAD:main beside a file of that name. git takes
			// neither in the name of a branch.
			p.unread = p.unread || strings.ContainsAny(w.text, "?[")
			plain = append(plain, w.text)
			continue
		}
		name, value, attached := strings.Cut(w.text, "=")
		switch {
		case w.text == "--help" || w.text == "-h" || w.text == "--dry-run" || w.text == "-n":
			// git push --help prints, and git push --dry-run sends nothing.
			//
			// 🚨 The word counts only where git reads it as an option. In
			// git push -o -n origin HEAD:main it is the value of -o, and a
			// probe with a real remote moved main.
			inert = true
		case abbreviates(name, "--all"), abbreviates(name, "--mirror"), abbreviates(name, "--branches"):
			p.every = true
		case abbreviates(name, "--tags"):
			tags = true
		case abbreviates(name, "--delete"):
			deletes = true
		case strings.HasPrefix(w.text, "-o") && len(w.text) > 2 && !strings.HasPrefix(w.text, "--"):
			// -o and its value in one word.
			p.merges = p.merges || pushOptionMerges.MatchString(w.text[2:])
		case pushValueOption(name):
			if !attached {
				value = ""
				if i+1 < len(words) {
					value = words[i+1].text
					i++
				}
			}
			named = named || abbreviates(name, "--repo")
			if name == "-o" || abbreviates(name, "--push-option") {
				p.merges = p.merges || pushOptionMerges.MatchString(value)
			}
		case attached || pushPlainOption(name):
		case !strings.HasPrefix(name, "--") && strings.Trim(name[1:], "fundqv46") == "":
			// Short options in one word: -fu.
			deletes = deletes || strings.Contains(name, "d")
		default:
			// 🚨 An option that the rule does not know may take the next
			// word: git push --recurse-submodules no origin. The remote
			// and the refspecs are then in other places.
			p.unread = true
		}
	}
	// 🚨 A word that the rule cannot read may take it back, or may take it
	// as its value: git push -n --no-dry-run origin HEAD:main sends, and so
	// does git push $o --dry-run origin HEAD:main when $o is -o.
	if inert && !p.unread {
		return push{inert: true}
	}
	// With no --repo the first plain word is the remote.
	specs := plain
	if !named && len(specs) > 0 {
		specs = specs[1:]
	}
	if len(specs) == 0 {
		// --tags is a refspec of its own, so git adds no branch to it.
		// --follow-tags is none, and the branch of push.default goes too.
		p.tags, p.implicit = tags, !tags
		return p
	}
	// 🚨 --repo names the remote only for a push with no other word. With
	// one, git reads that word as the remote, and --repo counts for nothing:
	// git push --repo=origin origin is git push origin. The rule does not
	// choose. It reads the word as a refspec, and the push as one with no
	// refspec too.
	p.implicit = named && len(plain) == 1
	for i := 0; i < len(specs); i++ {
		// tag v1 names a tag. +tag is a branch with the name tag.
		//
		// 🚨 With --repo the first plain word is the remote all the same,
		// and a remote can have the name tag.
		if specs[i] == "tag" && (i > 0 || !named) {
			i++
			continue
		}
		spec := strings.TrimPrefix(specs[i], "+")
		// Gerrit merges a change that is pushed with %submit, alone or
		// among other options: refs/for/main%topic=x,submit.
		if _, options, ok := strings.Cut(spec, "%"); ok {
			p.merges = p.merges || slices.Contains(strings.Split(options, ","), "submit")
		}
		src, dst, colon := strings.Cut(spec, ":")
		if !colon {
			dst = src
		}
		// git push --delete NAME removes NAME, whatever it follows.
		colon = colon || deletes
		switch {
		case dst == "" || strings.Contains(dst, "*"):
			// A lone colon pushes each branch that the remote has too.
			p.every = true
		case dst == "HEAD" || dst == "@":
			p.head = true
			p.bareHead = p.bareHead || !colon
		default:
			if branch, ok := strings.CutPrefix(dst, "refs/heads/"); ok {
				dst = branch
			} else if strings.HasPrefix(dst, "refs/") {
				// A tag, a note or a review ref moves no branch.
				continue
			}
			dst = strings.TrimPrefix(dst, "heads/")
			switch {
			case dst == branchMark:
				// The branch at work, by its name.
				p.head = true
				p.named = p.named || !colon
				continue
			case strings.Contains(dst, branchMark):
				p.unread = true
				continue
			}
			p.dests = append(p.dests, dst)
			if !colon {
				p.bares = append(p.bares, dst)
			}
		}
	}
	return p
}

// pushValueOption reports whether an option of git push takes the next
// word.
func pushValueOption(name string) bool {
	return name == "-o" || abbreviates(name, "--push-option") || abbreviates(name, "--repo") ||
		abbreviates(name, "--receive-pack") || abbreviates(name, "--exec")
}

// pushPlainOption reports whether name is a long option of git push that
// takes no word, or the front of one.
func pushPlainOption(name string) bool {
	for option := range pushPlainOptions {
		if abbreviates(name, option) {
			return true
		}
	}
	return false
}

// pushPlainOptions are the long options of git push that take no word.
// Every other one is a doubt.
var pushPlainOptions = map[string]bool{
	"--force": true, "--force-with-lease": true, "--force-if-includes": true, "--no-force-if-includes": true,
	"--no-force-with-lease": true, "--set-upstream": true, "--dry-run": true, "--follow-tags": true,
	"--no-follow-tags": true, "--atomic": true, "--no-atomic": true, "--verify": true, "--no-verify": true,
	"--porcelain": true, "--quiet": true, "--verbose": true, "--progress": true, "--no-progress": true,
	"--prune": true, "--thin": true, "--no-thin": true, "--signed": true, "--no-signed": true,
	"--ipv4": true, "--ipv6": true, "--no-recurse-submodules": true, "--no-tags": true,
}

// pushOptionMerges matches a push option that asks GitLab to merge the
// merge request of the push.
var pushOptionMerges = regexp.MustCompile(`(?i)merge_request\.(?:merge_when_pipeline_succeeds|auto_merge)|^submit$`)

// pushed says why a push is a merge action, or returns an empty text.
//
// 🚨 A push behind git checkout -b NAME is read on the new branch. When no
// && joins the two, the create may have failed, and the push is then read
// a second time on the branch that the line began on. Both must pass.
func (r MergeRule) pushed(p push, l *line, f RepoFacts) string {
	if p.inert {
		return ""
	}
	for _, at := range l.places(p, f) {
		if why := r.pushedOn(p, l, f, at); why != "" {
			return why
		}
	}
	return ""
}

func (r MergeRule) pushedOn(p push, l *line, f RepoFacts, at place) string {
	unread := "terva cannot read where its push goes. " + howToPush
	if p.merges {
		return "its push asks the forge to merge when the checks pass"
	}
	if p.every {
		return "its push moves more branches than it names. " + howToPush
	}
	if p.unread {
		return unread
	}
	if p.early {
		// Any change of the branch on the line may stand between the
		// variable and the push.
		if leaves(l.log, f.Branch) {
			return unread
		}
	}
	// A branch that the push names is read first: it gets the plain answer.
	for _, d := range p.dests {
		if r.protected(d, f) {
			return "its push moves the protected branch " + d
		}
	}
	// The branch at work, and the branch that it follows. A branch that the
	// line made follows nothing, or a start that the rule did not read.
	branch, follows, followsKnown := f.Branch, f.Upstream, true
	moved := l.elsewhere || l.reconfigured || at.switched
	if at.created {
		// A new branch follows its start when the configuration says so
		// for every start, and the default says so for a remote branch.
		plain := f.AutoSetupMerge == "" || f.AutoSetupMerge == "true" || f.AutoSetupMerge == "false"
		branch, follows, followsKnown = at.branch, "", !at.fromStart && plain
	}
	if (p.head || p.implicit) && (moved || f.Unknown) {
		return unread
	}
	// 🚨 With no branch at work the host read a rebase or a bisect that has
	// not ended. git rebase --continue && git push ends on the branch of
	// that rebase, and the host did not read it.
	if (p.head || p.implicit) && branch == "" {
		return unread
	}
	if p.implicit && f.PushRefspecs {
		return unread
	}
	// 🚨 A refspec with no destination goes where a push refspec of the
	// remote sends it. git push origin feat/x moves trunk when the remote
	// maps feat/x to trunk. So such a refspec needs a remote that the host
	// read, and one with no mapping of its own.
	//
	// 🚨 A push of the tags alone moves no branch at a plain remote. At a
	// mirror remote it also deletes each branch that the remote has and this
	// repository has not. So it needs the same remote.
	if (p.bare() || p.tags) && (f.PushRefspecs || f.Unknown || l.elsewhere || l.reconfigured) {
		return unread
	}
	// 🚨 In the upstream mode git sends a refspec with no destination to the
	// upstream of its branch. git push origin feat/x then moves trunk when
	// feat/x follows trunk, as a branch of just scratch does. The host read
	// the upstream of the branch at work, and of no other branch.
	//
	// git does that for the name of a branch. HEAD goes to the branch of the
	// same name, and a tag that the line made is a tag.
	upstream := f.PushDefault == "upstream" || f.PushDefault == "tracking"
	mapped := false
	if upstream && p.named {
		if moved || !followsKnown {
			return unread
		}
		mapped = true
	}
	if upstream {
		for _, b := range p.bares {
			switch {
			case l.tags[b] != 0 && l.tags[b] == p.chain && b != branch:
				// 🚨 The tag exists only when && joins the push to the
				// command that made it. git tag other no-such-commit fails,
				// and a push behind a ; then sends the branch other.
			case moved || b != branch || !followsKnown:
				return unread
			default:
				mapped = true
			}
		}
	}
	var dests []string
	if p.head && branch != "" {
		dests = append(dests, branch)
	}
	if mapped && follows != "" {
		dests = append(dests, follows)
	}
	if p.implicit {
		switch f.PushDefault {
		case "", "simple", "current":
			// A push in the simple mode goes to a branch of the same name,
			// or it fails.
			if branch != "" {
				dests = append(dests, branch)
			}
		case "upstream", "tracking":
			if !followsKnown {
				return unread
			}
			if follows != "" {
				dests = append(dests, follows)
			}
		case "nothing":
		case "matching":
			return "its push moves more branches than it names. " + howToPush
		default:
			return unread
		}
	}
	for _, d := range dests {
		if r.protected(d, f) {
			return "its push moves the protected branch " + d
		}
	}
	return ""
}

func (r MergeRule) protected(branch string, f RepoFacts) bool {
	for _, n := range builtinProtected {
		if branch == n {
			return true
		}
	}
	for _, n := range f.Defaults {
		if n != "" && branch == n {
			return true
		}
	}
	for _, pat := range r.Protected {
		if ok, err := path.Match(pat, branch); err == nil && ok {
			return true
		}
	}
	return false
}

// wordText returns a word as the shell hands it to the program, for a word
// made of plain and quoted text. A word with a variable or a substitution is
// not read, with one exception: a substitution that prints the branch at
// work reads as that branch, because a model writes it often.
//
// 🚨 A word that is not read still has a text: its plain parts, with a mark
// in the place of each variable and substitution. sh -c "git push origin
// HEAD:main; echo $?" is a line that the rule must read, and one variable in
// it does not hide the push. The line inside is parsed again, and a word
// with the mark cannot be read there, whatever quotes stand around it.
func wordText(w *syntax.Word) (string, bool) { return readWord(w, false, nil) }

// readWord reads a word. quoted is set for the body of a here document,
// whose plain text follows the rules of double quotes. lookup gives the
// value of a variable that the line set, and it can be nil.
func readWord(w *syntax.Word, quoted bool, lookup func(string) (string, bool)) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	ok := true
	// shape is the word with an x for each part that is no plain text. The
	// shell makes more than one word of HEAD:{main,"x"} too.
	var shape strings.Builder
	unread := func(n syntax.Node) {
		// b=feat/x; git push origin "$b:$b"
		if p, isParam := n.(*syntax.ParamExp); isParam && lookup != nil {
			if name, all, simple := simple(p); simple && !all {
				if value, known := lookup(name); known {
					b.WriteString(value)
					return
				}
			}
		}
		ok = false
		b.WriteString(unreadMark)
	}
	for _, part := range w.Parts {
		if lit, plain := part.(*syntax.Lit); plain && !quoted {
			shape.WriteString(lit.Value)
		} else {
			shape.WriteString("x")
		}
		switch p := part.(type) {
		case *syntax.Lit:
			if quoted {
				if strings.Contains(p.Value, unreadMark) {
					ok = false
				}
				b.WriteString(unescapeQuoted(p.Value))
				break
			}
			if strings.Contains(p.Value, unreadMark) {
				ok = false
			}
			b.WriteString(unescape(p.Value))
		case *syntax.SglQuoted:
			// $'ma\x69n' is main.
			if p.Dollar && strings.Contains(p.Value, `\`) || strings.Contains(p.Value, unreadMark) {
				ok = false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				switch q := q.(type) {
				case *syntax.Lit:
					if strings.Contains(q.Value, unreadMark) {
						ok = false
					}
					b.WriteString(unescapeQuoted(q.Value))
				case *syntax.CmdSubst:
					if mark := knownOutput(q); mark != "" {
						b.WriteString(mark)
					} else {
						unread(q)
					}
				default:
					unread(q)
				}
			}
		case *syntax.CmdSubst:
			if mark := knownOutput(p); mark != "" {
				b.WriteString(mark)
			} else {
				unread(p)
			}
		default:
			unread(p)
		}
	}
	// The shell makes more than one word of HEAD:{main,feat/x}.
	if braces.MatchString(shape.String()) {
		ok = false
	}
	return b.String(), ok
}

// unreadMark stands in a word's text for a part that the rule cannot read.
const unreadMark = "\x01"

// branchMark stands in a word's text for a substitution that prints the
// branch at work.
//
// 🚨 It is not HEAD. In the upstream mode git sends the name of a branch to
// the upstream of that branch, and it sends HEAD to the branch of the same
// name.
const branchMark = "\x02"

// braces matches a brace expansion: {a,b} or {1..3}. HEAD@{u} is none.
var braces = regexp.MustCompile(`\{[^{}]*(?:,|\.\.)[^{}]*\}`)

// rootMark stands in a word's text for a substitution that prints the top
// level of the repository.
const rootMark = "\x03"

// startMark stands in the value of a variable for the branch at work at the
// place where the line set the variable.
const startMark = "\x04"

// knownOutput returns the mark of a substitution whose output the rule
// knows: one of the commands that print the branch at work, or the one that
// prints the top level of the repository.
func knownOutput(c *syntax.CmdSubst) string {
	if len(c.Stmts) != 1 {
		return ""
	}
	call, ok := c.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 {
		return ""
	}
	var texts []string
	for _, w := range call.Args {
		lit, ok := bareWord(w)
		if !ok {
			return ""
		}
		texts = append(texts, lit)
	}
	switch strings.Join(texts, " ") {
	case "git branch --show-current", "git rev-parse --abbrev-ref HEAD", "git symbolic-ref --short HEAD":
		return branchMark
	case "git rev-parse --show-toplevel":
		return rootMark
	}
	return ""
}

func bareWord(w *syntax.Word) (string, bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", false
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok {
		return "", false
	}
	return lit.Value, true
}

// unescapeQuoted removes the backslashes that the shell removes inside
// double quotes and in a here document: those in front of $, `, ", \ and a
// new line. Every other one stays. "$'ma\x69n'" keeps its escape, and the
// shell inside reads it as main.
func unescapeQuoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescape removes the backslashes of an unquoted word: g\it is git.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
