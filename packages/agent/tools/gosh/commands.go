//go:build terva_gosh

package gosh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/mark3labs/go-bash/builtins/grep"
	"github.com/mark3labs/go-bash/builtins/ls"
	"github.com/mark3labs/go-bash/builtins/sed"
	"github.com/mark3labs/go-bash/builtins/wc"
	"github.com/mark3labs/go-bash/command"
)

// customCommands replace or add to go-bash's built-ins. go-bash registers
// custom commands first, and a built-in of the same name then stays out.
func customCommands() []command.Command {
	cmds := []command.Command{
		command.Define("grep", runGrep),
		command.Define("sed", runSed),
		command.Define("python", refusePython),
		command.Define("python3", refusePython),
		command.Define("curl", refuseNetwork),
		command.Define("wget", refuseNetwork),
		command.Define("mktemp", runMktemp),
		command.Define("cmp", runCmp),
		command.Define("js", runJS),
		command.Define("ls", runLs),
		command.Define("wc", runWc),
	}
	return append(cmds, normalizedCommands()...)
}

func fail(c *command.Context, name string, code int, format string, args ...any) command.Result {
	fmt.Fprintf(c.Stderr, "%s: %s\n", name, fmt.Sprintf(format, args...))
	return command.Result{ExitCode: code}
}

func resolve(c *command.Context, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(c.Cwd, p)
}

// pythonRefusal is what python and python3 print. It names the two tools
// that cover what agents use python for here: the lake census found 46% of
// inline python imports json and 38% sys, mostly to pick fields out of JSON.
const pythonRefusal = `%s: python is not available in this shell, and nothing can be installed.
Use js instead. It runs JavaScript, reads stdin, and has require('fs') for files:
  cat data.json | js -e 'const d = JSON.parse(stdin); console.log(d.items.length)'
For JSON on a pipe, jq also works: cat data.json | jq '.items | length'
`

// refuseNetwork stands in for curl and wget. go-bash's curl refuses already
// while the runner passes no Fetch and no Network, but the shell's word
// that the network is off should not rest on an option it does not set.
func refuseNetwork(_ context.Context, args []string, c *command.Context) command.Result {
	return fail(c, args[0], 127, "the network is off in this shell. Ask the user, or use a tool that is allowed to fetch.")
}

func refusePython(_ context.Context, args []string, c *command.Context) command.Result {
	fmt.Fprintf(c.Stderr, pythonRefusal, args[0])
	return command.Result{ExitCode: 127}
}

// grepLongValueOpts are GNU grep's long options that take a value. GNU
// accepts `--include '*.go'` as well as `--include='*.go'`.
var grepLongValueOpts = map[string]bool{
	"--regexp": true, "--file": true, "--include": true, "--exclude": true,
	"--exclude-from": true, "--exclude-dir": true, "--context": true,
	"--after-context": true, "--before-context": true, "--max-count": true,
	"--label": true, "--binary-files": true, "--devices": true,
	"--directories": true, "--group-separator": true,
}

// joinLongValues rewrites `--opt VALUE` as `--opt=VALUE` for the long
// options in values, up to a --. Without it the value reads as an operand,
// and the first operand becomes grep's pattern: `grep --exclude-dir x -r
// TODO .` searched for x.
//
// ⚠️ The value of a short option passes through untouched, so the pattern
// in `grep -e --include .` stays a pattern and . stays the operand. The
// args must already be split, so every short value is its own argument.
func joinLongValues(args []string, shortValues string, values map[string]bool) []string {
	out := []string{args[0]}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(out, args[i:]...)
		}
		if len(a) == 2 && a[0] == '-' && strings.IndexByte(shortValues, a[1]) >= 0 && i+1 < len(args) {
			out = append(out, a, args[i+1])
			i++
			continue
		}
		if values[a] && i+1 < len(args) {
			out = append(out, a+"="+args[i+1])
			i++
			continue
		}
		out = append(out, a)
	}
	return out
}

// grepValueOpts are the short options that take a value.
const grepValueOpts = "efABCmdD"

// runGrep gives grep's basic mode GNU semantics and maps -P onto RE2, then
// hands the rewritten argv to go-bash's grep in extended mode. See
// breToERE for why. The argv is split first, so every option value is its
// own argument and `-m1` works.
func runGrep(ctx context.Context, args []string, c *command.Context) command.Result {
	args = joinLongValues(splitShortOpts(args, grepValueOpts), grepValueOpts, grepLongValueOpts)
	// GNU permutes its arguments, so an option counts wherever it stands:
	// `grep 'a|b' f -E` is extended, and `grep f -e x` reads f. One pass
	// sorts every argument into an option or an operand, and only then
	// does the first operand become the pattern.
	mode, setMode := byte('G'), byte(0)
	// flags holds the options that take no value, apart from the patterns
	// and values in out. Every later question about a flag asks flags, so a
	// pattern spelled -q or -h cannot pose as one.
	var out, ops, flags []string
	var pats []int // indexes into out that hold a pattern
	sawE := false
	maxCount := -1
	context := false
	var skipDirs []string
	addPattern := func(p string) {
		out = append(out, "-e", p)
		pats = append(pats, len(out)-1)
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			ops = append(ops, args[i+1:]...)
			i = len(args)
		case a == "-" || !strings.HasPrefix(a, "-"):
			ops = append(ops, a)
		case a == "-E" || a == "--extended-regexp" || a == "-F" || a == "--fixed-strings" ||
			a == "-G" || a == "--basic-regexp" || a == "-P" || a == "--perl-regexp":
			m := a[1]
			if m == '-' {
				m = map[string]byte{"--extended-regexp": 'E', "--fixed-strings": 'F', "--basic-regexp": 'G', "--perl-regexp": 'P'}[a]
			}
			// GNU grep 3.11 refuses two different matchers rather than
			// taking the last one.
			if setMode != 0 && setMode != m {
				return fail(c, "grep", 2, "conflicting matchers specified")
			}
			setMode, mode = m, m
		case strings.HasPrefix(a, "--regexp="):
			sawE = true
			addPattern(strings.TrimPrefix(a, "--regexp="))
		case strings.HasPrefix(a, "--file="):
			sawE = true
			if r := patternFile(c, strings.TrimPrefix(a, "--file="), addPattern); r != nil {
				return *r
			}
		case strings.HasPrefix(a, "--exclude-dir="):
			skipDirs = append(skipDirs, strings.TrimPrefix(a, "--exclude-dir="))
		case strings.HasPrefix(a, "--context=") || strings.HasPrefix(a, "--after-context=") ||
			strings.HasPrefix(a, "--before-context=") || isNumber(a[1:]):
			// go-bash knows only -A, -B and -C with a separate value, so the
			// long forms and the -NUM shorthand for -C NUM become those.
			context = true
			if isNumber(a[1:]) {
				out = append(out, "-C", a[1:])
				break
			}
			key, val, _ := strings.Cut(a, "=")
			out = append(out, map[string]string{"--context": "-C", "--after-context": "-A", "--before-context": "-B"}[key], val)
		case strings.HasPrefix(a, "--max-count="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--max-count="))
			if err != nil {
				return fail(c, "grep", 2, "invalid max count")
			}
			maxCount = n
		case a == "--regexp" || a == "--file" || len(a) == 2 && strings.IndexByte(grepValueOpts, a[1]) >= 0:
			val := ""
			if i+1 < len(args) {
				i++
				val = args[i]
			}
			switch {
			case a == "-e" || a == "--regexp":
				sawE = true
				addPattern(val)
			case a == "-f" || a == "--file":
				sawE = true
				if r := patternFile(c, val, addPattern); r != nil {
					return *r
				}
			case a == "-m":
				n, err := strconv.Atoi(val)
				if err != nil {
					return fail(c, "grep", 2, "invalid max count")
				}
				maxCount = n
			default:
				if a == "-A" || a == "-B" || a == "-C" {
					context = true
				}
				out = append(out, a, val)
			}
		default:
			out = append(out, a)
			flags = append(flags, a)
		}
	}
	if sawE && len(pats) == 0 {
		// Only empty -f files gave patterns. GNU then exits 1 without
		// reading, even under -c, and under -v it selects every line.
		// go-bash refuses a grep with no pattern, so -v gets one that can
		// never match, in a mode where it is not translated.
		if !slices.Contains(flags, "-v") && !slices.Contains(flags, "--invert-match") {
			return command.Result{ExitCode: 1}
		}
		mode = 'E'
		addPattern(`\b\B`)
	}
	if !sawE {
		if len(ops) == 0 {
			return fail(c, "grep", 2, "no pattern given")
		}
		// Through -e, because go-bash reads a pattern such as --x) as an
		// option, and GNU does not after --.
		addPattern(ops[0])
		ops = ops[1:]
	}

	// The mode flags are not forwarded. The one mode becomes one flag.
	argv := []string{args[0]}
	switch mode {
	case 'G':
		for _, k := range pats {
			out[k] = breToERE(out[k])
		}
		argv = append(argv, "-E")
	case 'E', 'P':
		argv = append(argv, "-E")
	case 'F':
		argv = append(argv, "-F")
	}
	if len(skipDirs) > 0 {
		sub := *c
		sub.FS = &skipDirFS{FileSystem: c.FS, globs: skipDirs}
		c = &sub
	}
	// grep prints file names with -H or -r, or when it reads more than one
	// file, and -h turns them off. grepMaxCount needs to know which.
	names := len(ops) > 1
	quiet, recursive, forced := false, false, false
	var gf grepFlags
	for _, a := range flags {
		switch a {
		case "-c", "--count":
			gf.count = true
		case "-o", "--only-matching":
			gf.only = true
		case "-l", "--files-with-matches", "-L", "--files-without-match":
			gf.names = true
		}
		switch a {
		case "-H", "--with-filename":
			names, forced = true, true
		case "-h", "--no-filename":
			names, forced = false, true
		case "-r", "-R", "--recursive", "--dereference-recursive":
			recursive = true
		case "-q", "--quiet", "--silent":
			quiet = true
		}
	}
	gf.quiet = quiet
	if recursive && !forced && len(ops) <= 1 {
		// GNU names the files under -r unless the one operand is a file.
		names = len(ops) == 0
		if len(ops) == 1 {
			info, err := c.FS.Stat(resolve(c, ops[0]))
			names = err != nil || info.IsDir()
		}
		if !names {
			out = append([]string{"-h"}, out...)
		}
	}
	if len(ops) > 1 {
		// go-bash sorts its file operands, and GNU searches them in the order
		// given, so `grep X b.go a.go` printed a.go first. Run one operand at
		// a time instead, and combine the statuses the way GNU does: 0 if a
		// line was selected, 2 if an error happened (unless -q selected a
		// line), and 1 otherwise.
		base := append(append([]string{}, argv...), out...)
		if names {
			base = append(base, "-H")
		}
		// GNU puts a -- between context groups from different files too.
		// Only when grep prints lines: -l, -L, -c and -q print none.
		var sep *groupSepWriter
		if context && !gf.count && !gf.quiet && !gf.names {
			sep = &groupSepWriter{w: c.Stdout}
			sub := *c
			sub.Stdout = sep
			c = &sub
		}
		code, errSeen := 1, false
		for _, op := range ops {
			if sep != nil {
				sep.next()
			}
			res := grepOne(c, append(append([]string{}, base...), "--", op), []string{op}, maxCount, context, names, gf)
			switch res.ExitCode {
			case 0:
				code = 0
				if quiet {
					return res
				}
			case 1:
			default:
				errSeen = true
			}
		}
		if errSeen {
			code = 2
		}
		return command.Result{ExitCode: code}
	}
	// -- before the operands, so go-bash cannot read a file named
	// --include=x as an option. GNU takes it as a file after --.
	return grepOne(c, append(append(append(argv, out...), "--"), ops...), ops, maxCount, context, names, gf)
}

// grepFlags are the flags the -m path needs, read from the options alone.
// names is -l or -L, which print file names instead of lines.
type grepFlags struct{ count, quiet, only, names bool }

// grepOne runs go-bash's grep over at most one file operand.
func grepOne(c *command.Context, argv, ops []string, maxCount int, context, names bool, gf grepFlags) command.Result {
	// GNU keeps the operand as written, so `grep -r X .` prints ./path.
	// go-bash cleans the ./ away. Put it back for a single such operand.
	if names && len(ops) == 1 && (ops[0] == "." || strings.HasPrefix(ops[0], "./")) {
		pw := &dotPrefixWriter{w: c.Stdout}
		sub := *c
		sub.Stdout = pw
		c = &sub
		defer pw.flush()
	}
	if maxCount < 0 {
		return grep.Run(c, argv, grep.ModeBasic)
	}
	if context {
		return fail(c, "grep", 2, "-m with -A, -B or -C is not supported in this shell")
	}
	return grepMaxCount(c, argv, maxCount, names, gf)
}

// patternFile reads grep's -f FILE and adds one pattern per line, so a
// basic-mode file gets the same translation as -e. It returns a result
// only when the file cannot be read.
func patternFile(c *command.Context, name string, add func(string)) *command.Result {
	var data []byte
	var err error
	if name == "-" {
		data, err = io.ReadAll(c.Stdin)
	} else {
		data, err = c.FS.ReadFile(resolve(c, name))
	}
	if err != nil {
		r := fail(c, "grep", 2, "%s: %v", name, err)
		return &r
	}
	// An empty file adds no pattern. runGrep decides what that means once
	// every option is read.
	if len(data) == 0 {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		add(line)
	}
	return nil
}

// lsAllEach lists file operands and then each directory under -a, as GNU
// does: a header per directory, and . and .. at the top of each, which
// go-bash leaves out. A blank line comes before a header unless nothing has
// been printed yet.
func lsAllEach(ctx context.Context, c *command.Context, name string, opts, files, dirs []string) command.Result {
	w := &countWriter{w: c.Stdout}
	sub := *c
	sub.Stdout = w
	worst := 0
	run := func(args ...string) {
		res := ls.New().Execute(ctx, append(append([]string{name}, opts...), args...), &sub)
		worst = max(worst, res.ExitCode)
	}
	if len(files) > 0 {
		run(files...)
	}
	for _, d := range dirs {
		if w.n > 0 {
			io.WriteString(w, "\n")
		}
		fmt.Fprintf(w, "%s:\n.\n..\n", d)
		run(d)
	}
	if worst == 1 {
		worst = 2 // GNU exits 2 when it cannot reach an operand.
	}
	return command.Result{ExitCode: worst}
}

// countWriter counts the bytes that pass through it.
type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// groupSepWriter writes a -- line before the first output of a file when
// an earlier file already wrote output.
type groupSepWriter struct {
	w       io.Writer
	wrote   bool // some file wrote output
	started bool // the current file wrote output
}

func (g *groupSepWriter) next() { g.started = false }

func (g *groupSepWriter) Write(p []byte) (int, error) {
	if len(p) > 0 && !g.started {
		if g.wrote {
			if _, err := io.WriteString(g.w, "--\n"); err != nil {
				return 0, err
			}
		}
		g.started, g.wrote = true, true
	}
	return g.w.Write(p)
}

// dotPrefixWriter puts ./ in front of each output line that lacks it,
// except the -- that separates context groups.
type dotPrefixWriter struct {
	w       io.Writer
	partial []byte
}

func (d *dotPrefixWriter) Write(p []byte) (int, error) {
	d.partial = append(d.partial, p...)
	for {
		i := bytes.IndexByte(d.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		d.line(d.partial[:i+1])
		d.partial = d.partial[i+1:]
	}
}

func (d *dotPrefixWriter) line(l []byte) {
	if !bytes.HasPrefix(l, []byte("./")) && !bytes.Equal(bytes.TrimRight(l, "\n"), []byte("--")) {
		io.WriteString(d.w, "./")
	}
	d.w.Write(l)
}

func (d *dotPrefixWriter) flush() {
	if len(d.partial) > 0 {
		d.line(d.partial)
		d.partial = nil
	}
}

// runLs prints one name per line unless the script asks for columns. GNU
// does that whenever stdout is not a terminal, and here it never is. go-bash
// printed columns, so `ls | wc -l` counted 1.
func runLs(ctx context.Context, args []string, c *command.Context) command.Result {
	args = splitShortOpts(args, valueOpts["ls"])
	argv := []string{args[0]}
	columns := false
	for _, a := range args[1:] {
		// Columns are go-bash's default, and it does not know -C.
		if a == "-C" || a == "--format=vertical" {
			columns = true
			continue
		}
		argv = append(argv, a)
	}
	if !columns {
		argv = append([]string{argv[0], "-1"}, argv[1:]...)
	}
	// go-bash leaves . and .. out of -a. Add them for the plain one-column
	// listing of one directory, the shape agents use.
	all, plain, byName, reverse := false, !columns, true, false
	var ops, opts []string
	for k := 1; k < len(argv); k++ {
		a := argv[k]
		switch {
		case len(a) == 2 && strings.IndexByte(valueOpts["ls"], a[1]) >= 0:
			// -I PATTERN and the like: the value stays with its option.
			opts = append(opts, a)
			if k+1 < len(argv) {
				k++
				opts = append(opts, argv[k])
			}
			plain = false
			continue
		case a == "-a" || a == "--all":
			all = true
		case a == "-A" || a == "--almost-all":
			all = false
		case a == "-1":
		case a == "-t" || a == "-S" || a == "-U" || a == "-f" || a == "-v" || a == "-X" || strings.HasPrefix(a, "--sort"):
			byName, plain = false, false
		case a == "-r" || a == "--reverse":
			reverse, plain = true, false
		case strings.HasPrefix(a, "-") && a != "-":
			plain = false
		default:
			ops = append(ops, a)
			continue
		}
		opts = append(opts, a)
	}
	var files, dirs []string
	if len(ops) > 1 && byName {
		// GNU lists the file operands first and then each directory, both
		// sorted by name. go-bash keeps the order given.
		for _, op := range ops {
			if info, err := c.FS.Stat(resolve(c, op)); err == nil && info.IsDir() && !slices.Contains(opts, "-d") {
				dirs = append(dirs, op)
			} else {
				files = append(files, op)
			}
		}
		for _, g := range [][]string{files, dirs} {
			slices.Sort(g)
			if reverse {
				slices.Reverse(g)
			}
		}
		argv = append(append(append([]string{argv[0]}, opts...), files...), dirs...)
	}
	if all && plain && len(dirs) > 0 {
		return lsAllEach(ctx, c, argv[0], opts, files, dirs)
	}
	if all && plain && len(ops) <= 1 {
		dir := "."
		if len(ops) == 1 {
			dir = ops[0]
		}
		if info, err := c.FS.Stat(resolve(c, dir)); err == nil && info.IsDir() {
			io.WriteString(c.Stdout, ".\n..\n")
		}
	}
	res := ls.New().Execute(ctx, argv, c)
	// GNU exits 2 when it cannot reach an operand, and go-bash exits 1.
	if res.ExitCode == 1 {
		res.ExitCode = 2
	}
	return res
}

// grepMaxCount adds -m, which go-bash's grep lacks. It runs grep into a
// buffer and keeps the first n output lines of each file, or caps each count
// under -c. When grep printed file names, the file is the text before the
// first colon, so a file name with a colon in it can be grouped wrongly.
func grepMaxCount(c *command.Context, argv []string, n int, names bool, gf grepFlags) command.Result {
	if n == 0 {
		// GNU reads nothing under -m 0 and reports no match.
		return command.Result{ExitCode: 1}
	}
	switch {
	case gf.quiet:
		// -q prints nothing for the filter to count, and one match already
		// decides the status, so the limit changes nothing.
		return grep.Run(c, argv, grep.ModeBasic)
	case gf.only:
		// GNU counts selected input lines, and -o prints one output line per
		// match, so the filter would cut a line's later matches.
		return fail(c, "grep", 2, "-m with -o is not supported in this shell")
	}
	// The limit applies to the lines as they stream past, and adds no buffer
	// of its own. 🚨 go-bash's grep still reads each input whole and holds its
	// output until the file ends, so -m cannot stop it early.
	w := &maxCountWriter{w: c.Stdout, n: n, names: names, countOnly: gf.count, kept: map[string]int{}}
	sub := *c
	sub.Stdout = w
	res := grep.Run(&sub, argv, grep.ModeBasic)
	w.flush()
	if res.ExitCode > 1 {
		return res
	}
	if !w.any {
		return command.Result{ExitCode: 1}
	}
	return command.Result{}
}

// maxCountWriter passes on at most n selected lines per file, and caps each
// -c count at n.
type maxCountWriter struct {
	w                io.Writer
	n                int
	names, countOnly bool
	kept             map[string]int
	partial          []byte
	any              bool
}

func (m *maxCountWriter) Write(p []byte) (int, error) {
	m.partial = append(m.partial, p...)
	for {
		k := bytes.IndexByte(m.partial, '\n')
		if k < 0 {
			return len(p), nil
		}
		line := string(m.partial[:k+1])
		m.partial = m.partial[k+1:]
		if err := m.line(line); err != nil {
			return len(p), err
		}
	}
}

func (m *maxCountWriter) flush() {
	if len(m.partial) > 0 {
		m.line(string(m.partial))
		m.partial = nil
	}
}

func (m *maxCountWriter) line(line string) error {
	if m.countOnly {
		body := strings.TrimSuffix(line, "\n")
		k := strings.LastIndexByte(body, ':')
		if v, err := strconv.Atoi(body[k+1:]); err == nil && v > m.n {
			body = body[:k+1] + strconv.Itoa(m.n)
		}
		m.any = m.any || !strings.HasSuffix(body, ":0") && body != "0"
		_, err := fmt.Fprintln(m.w, body)
		return err
	}
	file := ""
	if m.names {
		file, _, _ = strings.Cut(line, ":")
	}
	if m.kept[file] >= m.n {
		return nil
	}
	m.kept[file]++
	m.any = true
	_, err := io.WriteString(m.w, line)
	return err
}

// runSed adds -i to go-bash's sed, which refuses it outright. Each file is
// read through the jailed filesystem, run through go-bash's sed, and written
// back. A failed run leaves the file as it was.
func runSed(ctx context.Context, args []string, c *command.Context) command.Result {
	inPlace := false
	suffix := ""
	var pass []string
	var scripts []string
	var files []string
	// GNU sed permutes, so `sed 's/a/b/' -i f` edits in place. Operands are
	// collected as they come, and options count wherever they stand.
	var operands []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			name, val, hasVal := strings.Cut(a, "=")
			switch name {
			case "--in-place":
				inPlace, suffix = true, val
			case "--expression":
				if hasVal {
					scripts = append(scripts, val)
				} else if i+1 < len(args) {
					i++
					scripts = append(scripts, args[i])
				}
			default:
				pass = append(pass, a)
			}
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			operands = append(operands, a)
			continue
		}
		var cl strings.Builder
		cl.WriteByte('-')
		for j := 1; j < len(a); j++ {
			ch := a[j]
			if ch == 'i' {
				inPlace, suffix = true, a[j+1:]
				break
			}
			if ch == 'e' || ch == 'f' {
				val := a[j+1:]
				if val == "" && i+1 < len(args) {
					i++
					val = args[i]
				}
				if ch == 'e' {
					scripts = append(scripts, val)
				} else {
					pass = append(pass, "-f", val)
				}
				break
			}
			cl.WriteByte(ch)
		}
		if cl.Len() > 1 {
			pass = append(pass, cl.String())
		}
	}
	rest := operands
	hasF := false
	for _, p := range pass {
		if p == "-f" || strings.HasPrefix(p, "--file") {
			hasF = true
		}
	}
	if len(scripts) == 0 && !hasF && len(rest) > 0 {
		scripts, rest = append(scripts, rest[0]), rest[1:]
	}
	files = rest

	argv := []string{args[0]}
	argv = append(argv, pass...)
	for _, s := range scripts {
		argv = append(argv, "-e", s)
	}
	if !inPlace {
		return sed.New().Execute(ctx, append(argv, files...), c)
	}
	if len(files) == 0 {
		return fail(c, "sed", 1, "no input files")
	}
	for _, f := range files {
		p := resolve(c, f)
		info, err := c.FS.Stat(p)
		if err != nil {
			return fail(c, "sed", 2, "can't read %s: %v", f, err)
		}
		data, err := c.FS.ReadFile(p)
		if err != nil {
			return fail(c, "sed", 2, "can't read %s: %v", f, err)
		}
		var buf bytes.Buffer
		sub := *c
		sub.Stdin = bytes.NewReader(data)
		sub.Stdout = &buf
		if res := sed.New().Execute(ctx, argv, &sub); res.ExitCode != 0 {
			return res
		}
		if suffix != "" {
			backup := p + suffix
			if strings.Contains(suffix, "*") {
				backup = path.Join(path.Dir(p), strings.ReplaceAll(suffix, "*", path.Base(p)))
			}
			if err := c.FS.WriteFile(backup, data, info.Mode().Perm()); err != nil {
				return fail(c, "sed", 4, "couldn't write %s: %v", backup, err)
			}
		}
		if err := c.FS.WriteFile(p, buf.Bytes(), info.Mode().Perm()); err != nil {
			return fail(c, "sed", 4, "couldn't write %s: %v", f, err)
		}
	}
	return command.Result{}
}

// runMktemp makes a file or directory from a template of trailing Xs. The
// default directory is $TMPDIR, which is the private in-memory /tmp.
func runMktemp(_ context.Context, args []string, c *command.Context) command.Result {
	dir, mkDir, dry, quiet := "", false, false, false
	template := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-d" || a == "--directory":
			mkDir = true
		case a == "-u" || a == "--dry-run":
			dry = true
		case a == "-q" || a == "--quiet":
			quiet = true
		case a == "-t":
			if dir == "" {
				dir = "$TMPDIR"
			}
		case a == "-p" || a == "--tmpdir":
			if i+1 < len(args) {
				i++
				dir = args[i]
			} else {
				dir = "$TMPDIR"
			}
		case strings.HasPrefix(a, "--tmpdir="):
			dir = strings.TrimPrefix(a, "--tmpdir=")
		case strings.HasPrefix(a, "-p"):
			dir = a[2:]
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, ch := range a[1:] {
				switch ch {
				case 'd':
					mkDir = true
				case 'u':
					dry = true
				case 'q':
					quiet = true
				case 't':
					if dir == "" {
						dir = "$TMPDIR"
					}
				default:
					return fail(c, "mktemp", 1, "invalid option -- '%c'", ch)
				}
			}
		default:
			template = a
		}
	}
	tmp := c.Env["TMPDIR"]
	if tmp == "" {
		tmp = "/tmp"
	}
	if dir == "$TMPDIR" {
		dir = tmp
	}
	if template == "" {
		template = "tmp.XXXXXXXXXX"
		if dir == "" {
			dir = tmp
		}
	}
	x := len(template) - len(strings.TrimRight(template, "X"))
	if x < 3 {
		return fail(c, "mktemp", 1, "too few X's in template '%s'", template)
	}
	for attempt := 0; attempt < 100; attempt++ {
		b := make([]byte, x)
		_, _ = rand.Read(b)
		name := template[:len(template)-x] + hex.EncodeToString(b)[:x]
		p := name
		if dir != "" {
			p = path.Join(dir, name)
		}
		p = resolve(c, p)
		if _, err := c.FS.Stat(p); err == nil {
			continue
		}
		if !dry {
			var err error
			if mkDir {
				err = c.FS.Mkdir(p, 0o700)
			} else {
				err = c.FS.WriteFile(p, nil, 0o600)
			}
			if err != nil {
				if quiet {
					return command.Result{ExitCode: 1}
				}
				return fail(c, "mktemp", 1, "failed to create %s: %v", p, err)
			}
		}
		fmt.Fprintln(c.Stdout, p)
		return command.Result{}
	}
	return fail(c, "mktemp", 1, "could not find a free name for '%s'", template)
}

// runCmp compares two files byte by byte. A file named - is stdin.
func runCmp(_ context.Context, args []string, c *command.Context) command.Result {
	silent := false
	var names []string
	for _, a := range args[1:] {
		switch a {
		case "-s", "--silent", "--quiet":
			silent = true
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				return fail(c, "cmp", 2, "invalid option '%s'", a)
			}
			names = append(names, a)
		}
	}
	if len(names) != 2 {
		return fail(c, "cmp", 2, "usage: cmp [-s] FILE1 FILE2")
	}
	var data [2][]byte
	for k, n := range names {
		var err error
		if n == "-" {
			var buf bytes.Buffer
			_, err = buf.ReadFrom(c.Stdin)
			data[k] = buf.Bytes()
		} else {
			data[k], err = c.FS.ReadFile(resolve(c, n))
		}
		if err != nil {
			if os.IsNotExist(err) {
				return fail(c, "cmp", 2, "%s: No such file or directory", n)
			}
			return fail(c, "cmp", 2, "%s: %v", n, err)
		}
	}
	a, b := data[0], data[1]
	line := 1
	for k := 0; k < len(a) && k < len(b); k++ {
		if a[k] != b[k] {
			if !silent {
				fmt.Fprintf(c.Stdout, "%s %s differ: byte %d, line %d\n", names[0], names[1], k+1, line)
			}
			return command.Result{ExitCode: 1}
		}
		if a[k] == '\n' {
			line++
		}
	}
	if len(a) != len(b) {
		if !silent {
			short := names[0]
			if len(b) < len(a) {
				short = names[1]
			}
			fmt.Fprintf(c.Stderr, "cmp: EOF on %s\n", short)
		}
		return command.Result{ExitCode: 1}
	}
	return command.Result{}
}

// runWc reformats go-bash's wc, which pads every count to seven columns.
// GNU pads only as wide as it needs: one count of one input prints bare
// (`printf 'a\nb\n' | wc -l` is 2), several counts from stdin take seven
// columns, and counts of files take the width of their total size.
func runWc(ctx context.Context, args []string, c *command.Context) command.Result {
	var buf bytes.Buffer
	sub := *c
	sub.Stdout = &buf
	res := wc.New().Execute(ctx, args, &sub)

	var files []string
	for _, a := range args[1:] {
		if a == "-" || !strings.HasPrefix(a, "-") {
			files = append(files, a)
		}
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if buf.Len() == 0 {
		return res
	}
	counts := len(strings.Fields(lines[0]))
	if len(files) > 0 {
		counts--
	}
	width := 1
	switch {
	case len(files) == 0:
		if counts > 1 {
			width = 7
		}
	case counts > 1 || len(files) > 1:
		var total int64
		for _, f := range files {
			info, err := c.FS.Stat(resolve(c, f))
			if f == "-" || err != nil || !info.Mode().IsRegular() {
				total = -1
				break
			}
			total += info.Size()
		}
		if total < 0 {
			width = 7
		} else {
			width = len(strconv.FormatInt(total, 10))
		}
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		var b strings.Builder
		n := 0
		for n < len(fields) {
			if _, err := strconv.Atoi(fields[n]); err != nil {
				break
			}
			if n > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%*s", width, fields[n])
			n++
		}
		if n < len(fields) {
			// The name keeps its own spacing: take it from the original line.
			rest := line
			for k := 0; k < n; k++ {
				rest = strings.TrimLeft(rest, " \t")
				rest = rest[len(fields[k]):]
			}
			b.WriteString(" " + strings.TrimLeft(rest, " \t"))
		}
		fmt.Fprintln(c.Stdout, b.String())
	}
	return res
}
