//go:build terva_gosh

package gosh

import (
	"context"
	"strings"

	"github.com/mark3labs/go-bash/builtins/cut"
	"github.com/mark3labs/go-bash/builtins/date"
	"github.com/mark3labs/go-bash/builtins/diff"
	"github.com/mark3labs/go-bash/builtins/du"
	"github.com/mark3labs/go-bash/builtins/fold"
	"github.com/mark3labs/go-bash/builtins/head"
	"github.com/mark3labs/go-bash/builtins/nl"
	"github.com/mark3labs/go-bash/builtins/od"
	"github.com/mark3labs/go-bash/builtins/paste"
	"github.com/mark3labs/go-bash/builtins/seq"
	"github.com/mark3labs/go-bash/builtins/sort"
	"github.com/mark3labs/go-bash/builtins/split"
	"github.com/mark3labs/go-bash/builtins/tail"
	"github.com/mark3labs/go-bash/builtins/uniq"
	"github.com/mark3labs/go-bash/builtins/xargs"
	"github.com/mark3labs/go-bash/command"
)

// valueOpts lists, per command, the GNU short options that take a value.
//
// ⚠️ go-bash parses options per command, and many of its commands accept
// only `-n 1`, never `-n1` or `-nk2`. Agents write the attached forms all
// the time: `head -n1`, `xargs -I{}`, `sort -nk2`, `paste -sd,`. Each one
// failed with a usage message. Splitting the value off is the same argv to
// any getopt parser, so this rewrite cannot change a meaning.
var valueOpts = map[string]string{
	"head":  "nc",
	"tail":  "nc",
	"sort":  "ktoST",
	"xargs": "nILPdasE",
	"nl":    "bnwsvi",
	"paste": "d",
	"uniq":  "fsw",
	"du":    "dB",
	"diff":  "UCLI",
	"fold":  "w",
	"split": "lbna",
	"cut":   "dfcb",
	"ls":    "IwT",
	"date":  "dfr",
	"seq":   "sf",
	"od":    "AtjNw",
	"grep":  grepValueOpts,
}

// splitShortOpts rewrites argv so that every short option stands alone and
// every option value is its own argument. A bare number such as head's -5
// passes through, as do -- and everything after it, and long options.
func splitShortOpts(args []string, values string) []string {
	out := []string{args[0]}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(out, args[i:]...)
		}
		if len(a) < 2 || a[0] != '-' || a[1] == '-' || isNumber(a[1:]) {
			out = append(out, a)
			continue
		}
		for j := 1; j < len(a); j++ {
			ch := a[j]
			if strings.IndexByte(values, ch) < 0 {
				out = append(out, "-"+string(ch))
				continue
			}
			out = append(out, "-"+string(ch))
			if rest := a[j+1:]; rest != "" {
				out = append(out, rest)
			} else if i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			break
		}
	}
	return out
}

// splitLeadingOpts splits only the options before the first operand. xargs
// stops reading options at the command it runs, and that command's options
// belong to it: `xargs grep -nA2 x` must not split -nA2 with xargs's table,
// where -n takes a value.
func splitLeadingOpts(args []string, values string) []string {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" || len(a) < 2 || a[0] != '-' {
			return append(splitShortOpts(args[:i], values), args[i:]...)
		}
		// A value option given alone takes the next argument as its value.
		if len(a) == 2 && strings.IndexByte(values, a[1]) >= 0 {
			i++
		}
	}
	return splitShortOpts(args, values)
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normalized wraps a go-bash built-in so its argv is split first.
func normalized(inner command.Command) command.Command {
	name := string(inner.Name())
	values := valueOpts[name]
	return command.Define(name, func(ctx context.Context, args []string, c *command.Context) command.Result {
		split := splitShortOpts
		if name == "xargs" {
			split = splitLeadingOpts
		}
		res := inner.Execute(ctx, split(args, values), c)
		if name == "xargs" {
			res.ExitCode = xargsStatus(res.ExitCode)
		}
		return res
	})
}

// xargsStatus maps the status of the first failed invocation, which go-bash
// returns as it is, onto GNU's: 123 for a status from 1 to 125, and 124 for
// 255. 126 and 127 pass through.
//
// ⚠️ go-bash's own usage error is 2 and becomes 123 here, where GNU says 1.
// After splitShortOpts a usage error is rare, and a failed grep is not.
func xargsStatus(code int) int {
	switch {
	case code >= 1 && code <= 125:
		return 123
	case code == 255:
		return 124
	}
	return code
}

func normalizedCommands() []command.Command {
	var out []command.Command
	for _, mk := range []func() command.Command{
		head.New, tail.New, sort.New, xargs.New, nl.New, paste.New, uniq.New,
		du.New, diff.New, fold.New, split.New, cut.New, date.New, seq.New, od.New,
	} {
		out = append(out, normalized(mk()))
	}
	return out
}
