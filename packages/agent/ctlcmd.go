package agent

// `terva ctl` — send one ctrlproto verb to a running terva and print the reply.
//
// Every surface terva has is a ctrlproto client: the TUI, `terva attach`, the
// web PWA, `terva ext config`. There was no way to send ONE verb, which meant
// anything not yet given a button could only be reached by clicking something
// adjacent and hoping, or by writing a throwaway websocket script. Calibrating
// the world doctor's evidence budget needed exactly this and got the throwaway
// script, which is the argument for the command.
//
// It is a debugging and automation tool, not a user-facing surface: it speaks in
// verb names and JSON, and it will happily send a destructive verb because the
// caller typed one. What it will NOT do is print a reply nobody asked to read —
// see --shape and ctlshape.go, which exist because a `worlds.list` for one id
// once dumped a private lorebook to a terminal.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

const ctlDefaultTimeout = 30 * time.Second

type ctlOptions struct {
	verb     string
	params   string
	sess     string
	endpoint string
	token    string
	tokenTxt string
	shape    bool
	sel      string
	timeout  time.Duration
	list     bool
}

// runCtlCommand routes `terva ctl ...`, mirroring the other subcommand routers.
func runCtlCommand(rawArgs []string, version string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "ctl" {
		return false, nil
	}
	o, err := parseCtlArgs(rawArgs[1:])
	if err != nil {
		printCtlHelp()
		return true, err
	}
	if o == nil { // help was asked for and printed
		return true, nil
	}
	if o.list {
		for _, m := range ctrlproto.ServedMethods() {
			fmt.Println(m)
		}
		return true, nil
	}
	return true, runCtl(context.Background(), *o, version, os.Stdout)
}

func parseCtlArgs(args []string) (*ctlOptions, error) {
	o := &ctlOptions{timeout: ctlDefaultTimeout}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		// A flag that needs a value takes the NEXT argument; --flag=value is
		// accepted too because muscle memory supplies it either way.
		val := func(name string) (string, error) {
			if eq := strings.Index(a, "="); eq >= 0 {
				return a[eq+1:], nil
			}
			if i+1 >= len(args) {
				return "", i18n.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "-h" || a == "--help" || a == "help":
			printCtlHelp()
			return nil, nil
		case a == "--list":
			o.list = true
		case a == "--shape":
			o.shape = true
		case strings.HasPrefix(a, "--sess"):
			v, err := val("--sess")
			if err != nil {
				return nil, err
			}
			o.sess = v
		case strings.HasPrefix(a, "--select"):
			v, err := val("--select")
			if err != nil {
				return nil, err
			}
			o.sel = v
		case strings.HasPrefix(a, "--endpoint"):
			v, err := val("--endpoint")
			if err != nil {
				return nil, err
			}
			o.endpoint = v
		case strings.HasPrefix(a, "--token-file"):
			v, err := val("--token-file")
			if err != nil {
				return nil, err
			}
			o.tokenTxt = v
		case strings.HasPrefix(a, "--token"):
			v, err := val("--token")
			if err != nil {
				return nil, err
			}
			o.token = v
		case strings.HasPrefix(a, "--timeout"):
			v, err := val("--timeout")
			if err != nil {
				return nil, err
			}
			d, perr := time.ParseDuration(v)
			if perr != nil {
				return nil, i18n.Errorf("--timeout %q is not a duration (try 30s, 2m)", v)
			}
			o.timeout = d
		case strings.HasPrefix(a, "-"):
			return nil, i18n.Errorf("unknown flag %q", a)
		default:
			positional = append(positional, a)
		}
	}
	if o.list {
		return o, nil
	}
	if len(positional) == 0 {
		return nil, i18n.Errorf("name a verb (try: terva ctl --list)")
	}
	if len(positional) > 2 {
		return nil, i18n.Errorf("too many arguments — a verb and at most one JSON params blob")
	}
	o.verb = positional[0]
	if len(positional) == 2 {
		o.params = positional[1]
	}
	if o.shape && o.sel != "" {
		return nil, i18n.Errorf("--shape and --select ask for different things — pick one")
	}
	return o, nil
}

// ctlCaller is the slice of the client this command needs — one call. Narrow so
// a test can stand in for it without a daemon or a socket.
type ctlCaller interface {
	Call(ctx context.Context, sess string, method ctrlproto.Method, params, result any) error
}

func runCtl(ctx context.Context, o ctlOptions, version string, out io.Writer) error {
	verb := ctrlproto.Method(strings.TrimSpace(o.verb))
	if !servedMethod(verb) {
		return i18n.Errorf("unknown verb %q — `terva ctl --list` names every verb this build serves", o.verb)
	}
	params, err := ctlParams(o.params)
	if err != nil {
		return err
	}
	endpoint, needsToken, err := ctlEndpoint(o.endpoint)
	if err != nil {
		return err
	}
	// Resolved here rather than inside the dial: reading the environment SCRUBS
	// the token, so "do we have one" is a question worth asking exactly once.
	token, err := build.ResolveAttachToken(build.Args{Token: o.token, WebTokenFile: o.tokenTxt})
	if err != nil {
		return err
	}
	if needsToken && token == "" {
		return i18n.Errorf("the terva at %s requires a token — set %s or pass --token-file", endpoint, build.WebTokenEnv)
	}
	conn, err := dialCtrl(ctx, endpoint, token, "terva-ctl", version, o.timeout)
	if err != nil {
		return err
	}
	defer conn.Stop()

	callCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	return ctlCall(callCtx, conn.Client, o, verb, params, out)
}

// ctlCall is the half that has an answer to render — split from the dial so a
// test drives it with a fake caller instead of a daemon.
func ctlCall(ctx context.Context, c ctlCaller, o ctlOptions, verb ctrlproto.Method, params any, out io.Writer) error {
	var result any
	if err := c.Call(ctx, o.sess, verb, params, &result); err != nil {
		return err
	}
	// A verb with no result answers bare-ok. Say so rather than printing "null",
	// which reads like a verb that returned an empty answer.
	if result == nil {
		fmt.Fprintln(out, "ok")
		return nil
	}
	switch {
	case o.shape:
		for _, line := range shapeLines(result) {
			fmt.Fprintln(out, line)
		}
		return nil
	case o.sel != "":
		picked, err := selectPaths(result, strings.Split(o.sel, ","))
		if err != nil {
			return err
		}
		s, err := renderJSON(picked)
		if err != nil {
			return err
		}
		fmt.Fprint(out, s)
		return nil
	default:
		s, err := renderJSON(result)
		if err != nil {
			return err
		}
		fmt.Fprint(out, s)
		return nil
	}
}

func servedMethod(m ctrlproto.Method) bool {
	for _, s := range ctrlproto.ServedMethods() {
		if s == m {
			return true
		}
	}
	return false
}

// ctlParams decodes the params blob. "-" reads stdin, because a card body or a
// lorebook entry is not something to paste into an argv slot.
func ctlParams(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	if raw == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		raw = strings.TrimSpace(string(b))
		if raw == "" {
			return map[string]any{}, nil
		}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, i18n.Errorf("params must be JSON: %v", err)
	}
	return v, nil
}

// ctlEndpoint resolves which terva to talk to, and reports whether the discovery
// record says it wants a token — worth knowing BEFORE the dial, because the
// handshake failure otherwise says nothing about which of the two problems it is.
func ctlEndpoint(flag string) (endpoint string, needsToken bool, err error) {
	if e := strings.TrimSpace(flag); e != "" {
		return e, false, nil
	}
	rec, ok := config.ReadListenRecord()
	if !ok || rec.Endpoint == "" {
		return "", false, i18n.Errorf("no running terva found — start one with `terva web`, or pass --endpoint")
	}
	return rec.Endpoint, rec.Auth, nil
}

func printCtlHelp() {
	fmt.Fprint(os.Stderr, i18n.H("help.ctl", `terva ctl — send one control-plane verb to a running terva

  terva ctl <verb> [PARAMS_JSON]     call a verb; PARAMS_JSON may be "-" to read stdin
  terva ctl --list                   every verb this build serves

  --shape            print the reply's STRUCTURE and sizes, never its values
  --select a,b.c     print only these dotted paths (arrays are mapped over)
  --sess ID          address a session (#workspace for the workspace itself)
  --endpoint URL     which terva (default: the running one, auto-discovered)
  --token / --token-file PATH
  --timeout 30s

Look before you read. A list verb answers with everything it has, which for a
World is its whole lorebook — --shape answers "what is in here and how big"
without putting any of it on your screen:

  terva ctl worlds.list --shape
  terva ctl worlds.list --select worlds.id,worlds.name    # one row per World
  terva ctl worlds.doctor '{"id":"kobeni-3f8a02e6","sessions":["..."]}'

This is a debugging tool. It sends whatever verb you name, including the ones
that delete things.
`))
}
