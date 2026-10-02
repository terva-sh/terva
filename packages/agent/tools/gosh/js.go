//go:build terva_gosh

package gosh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mark3labs/go-bash/command"

	"terva.sh/terva/packages/agent/jsengine"
)

const jsUsage = "usage: js -e CODE [ARG...] | js FILE [ARG...]"

// jsPrelude gives a script the names a model reaches for from Node:
// console.log, require('fs') and process. Output goes straight to the
// command's stdout through __out, so it streams and has no 32 KiB cap.
// Objects print as JSON, which is close to Node and exact for data.
const jsPrelude = `
var __fmt = function (v) {
  if (typeof v === 'string') return v;
  if (v === undefined) return 'undefined';
  if (typeof v === 'object' && v !== null) { try { return JSON.stringify(v, null, 2); } catch (e) { return String(v); } }
  return String(v);
};
var __line = function (args) { return Array.prototype.map.call(args, __fmt).join(' ') + '\n'; };
console.log = function () { __out(__line(arguments)); };
console.info = console.log;
console.error = function () { __err(__line(arguments)); };
console.warn = console.error;
print = console.log;
var process = {
  argv: ['js', __script].concat(args),
  env: __env,
  stdout: { write: function (s) { __out(String(s)); return true; } },
  stderr: { write: function (s) { __err(String(s)); return true; } },
  exit: function (code) { __exit(String(code === undefined ? 0 : (code | 0))); throw new Error('process.exit'); }
};
var require = function (m) {
  if (m === 'fs' || m === 'node:fs') {
    return {
      readFileSync: function (p) { return (p === 0 || p === '/dev/stdin') ? stdin : __read(String(p)); },
      writeFileSync: function (p, d) { __write(String(p), String(d)); },
      appendFileSync: function (p, d) { __append(String(p), String(d)); },
      existsSync: function (p) { return __exists(String(p)) === '1'; },
      readdirSync: function (p) { return JSON.parse(__readdir(String(p))); }
    };
  }
  throw new Error("require('" + m + "') is not available in js. Only 'fs' is (readFileSync, writeFileSync, appendFileSync, existsSync, readdirSync).");
};
`

// runJS runs JavaScript on terva's sobek engine. The script sees its
// standard input as the global stdin, its arguments as args, and files
// only through the jailed filesystem.
func runJS(ctx context.Context, args []string, c *command.Context) command.Result {
	var src, name string
	var rest []string
	switch {
	case len(args) >= 3 && (args[1] == "-e" || args[1] == "--eval"):
		src, name, rest = args[2], "[eval]", args[3:]
	case len(args) >= 2 && !strings.HasPrefix(args[1], "-"):
		data, err := c.FS.ReadFile(resolve(c, args[1]))
		if err != nil {
			return fail(c, "js", 1, "%s: %v", args[1], err)
		}
		src, name, rest = string(data), args[1], args[2:]
	default:
		fmt.Fprintln(c.Stderr, jsUsage)
		return command.Result{ExitCode: 2}
	}
	var stdin string
	if c.Stdin != nil {
		b, err := io.ReadAll(c.Stdin)
		if err != nil {
			return fail(c, "js", 1, "reading stdin: %v", err)
		}
		stdin = string(b)
	}
	env := map[string]any{}
	for k, v := range c.Env {
		env[k] = v
	}
	argv := make([]any, len(rest))
	for i, a := range rest {
		argv[i] = a
	}
	str := func(f func(args []string) (string, error)) jsengine.Binding {
		return func(_ context.Context, args []string) (string, error) { return f(args) }
	}
	arg := func(args []string, i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	// process.exit records its code here before it throws to unwind the
	// script, so no text a script throws can pose as an exit.
	exited, exitStatus := false, 0
	opts := jsengine.Options{
		Globals: map[string]any{"stdin": stdin, "args": argv, "__script": name, "__env": env},
		Bindings: map[string]jsengine.Binding{
			"__out": str(func(a []string) (string, error) { _, err := io.WriteString(c.Stdout, arg(a, 0)); return "", err }),
			"__err": str(func(a []string) (string, error) { _, err := io.WriteString(c.Stderr, arg(a, 0)); return "", err }),
			"__read": str(func(a []string) (string, error) {
				b, err := c.FS.ReadFile(resolve(c, arg(a, 0)))
				return string(b), err
			}),
			"__write": str(func(a []string) (string, error) {
				return "", c.FS.WriteFile(resolve(c, arg(a, 0)), []byte(arg(a, 1)), 0o644)
			}),
			"__append": str(func(a []string) (string, error) {
				return "", c.FS.AppendFile(resolve(c, arg(a, 0)), []byte(arg(a, 1)), 0o644)
			}),
			"__exists": str(func(a []string) (string, error) {
				if _, err := c.FS.Stat(resolve(c, arg(a, 0))); err != nil {
					return "0", nil
				}
				return "1", nil
			}),
			"__exit": str(func(a []string) (string, error) {
				exited = true
				exitStatus, _ = strconv.Atoi(arg(a, 0))
				return "", nil
			}),
			"__readdir": str(func(a []string) (string, error) {
				es, err := c.FS.ReadDir(resolve(c, arg(a, 0)))
				if err != nil {
					return "", err
				}
				names := make([]string, len(es))
				for i, e := range es {
					names[i] = e.Name()
				}
				// encoding/json, because %q writes escapes such as \x1b
				// that JSON.parse refuses.
				b, err := json.Marshal(names)
				return string(b), err
			}),
		},
		// A script that logs in a loop makes one host call per line, so the
		// default cap of 50 would end it early. The context deadline is the
		// real bound.
		Limits: jsengine.Limits{MaxHostCalls: 1 << 30, MaxBindingBytes: 64 << 20},
	}
	// The prelude goes on the script's first line, so an error names the
	// line the model wrote.
	prelude := strings.ReplaceAll(jsPrelude, "\n", " ")
	_, err := jsengine.Run(ctx, name, prelude+src, opts)
	if exited {
		return command.Result{ExitCode: exitStatus}
	}
	if err != nil {
		return fail(c, "js", 1, "%v", err)
	}
	return command.Result{}
}
