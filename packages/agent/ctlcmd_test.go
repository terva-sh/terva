package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// fakeCaller stands in for the connected client: it records what was sent and
// answers with a canned reply. The dial is deliberately on the other side of
// ctlCall's seam, so these run with no daemon and no socket.
type fakeCaller struct {
	sess   string
	method ctrlproto.Method
	params any
	reply  string // JSON, or "" for a bare-ok
	err    error
}

func (f *fakeCaller) Call(_ context.Context, sess string, method ctrlproto.Method, params, result any) error {
	f.sess, f.method, f.params = sess, method, params
	if f.err != nil {
		return f.err
	}
	if f.reply == "" {
		return nil
	}
	return json.Unmarshal([]byte(f.reply), result)
}

func runCtlCall(t *testing.T, o ctlOptions, c *fakeCaller) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := ctlCall(context.Background(), c, o, ctrlproto.Method(o.verb), map[string]any{}, &out)
	return out.String(), err
}

func TestCtlPrintsTheReplyAsJSON(t *testing.T) {
	c := &fakeCaller{reply: `{"worlds":[{"id":"a-1","name":"Emma"}]}`}
	out, err := runCtlCall(t, ctlOptions{verb: "worlds.list"}, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name": "Emma"`) {
		t.Errorf("default output should be the full reply, got:\n%s", out)
	}
}

// --shape must not print values even though the caller reached the same verb.
// The seam matters: this asserts the FLAG routes there, where ctlshape_test
// asserts the renderer is safe.
func TestCtlShapeRoutesToTheShapeRenderer(t *testing.T) {
	c := &fakeCaller{reply: `{"worlds":[{"id":"a-1","name":"Emma"}]}`}
	out, err := runCtlCall(t, ctlOptions{verb: "worlds.list", shape: true}, c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Emma") {
		t.Errorf("--shape printed a value:\n%s", out)
	}
	if !strings.Contains(out, "worlds[].name") {
		t.Errorf("--shape did not describe the reply:\n%s", out)
	}
}

func TestCtlSelectNarrowsTheReply(t *testing.T) {
	c := &fakeCaller{reply: `{"worlds":[{"id":"a-1","name":"Emma","lore":[{"content":"PRIVATE"}]}]}`}
	out, err := runCtlCall(t, ctlOptions{verb: "worlds.list", sel: "worlds.id"}, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a-1") {
		t.Errorf("--select should print what was asked for:\n%s", out)
	}
	if strings.Contains(out, "PRIVATE") {
		t.Errorf("--select printed a field nobody selected:\n%s", out)
	}
}

// A bare-ok verb answers with no body. Printing "null" for it reads as a verb
// that returned an empty answer rather than one that has no answer to give.
func TestCtlSaysOkForABareResponse(t *testing.T) {
	c := &fakeCaller{reply: ""}
	out, err := runCtlCall(t, ctlOptions{verb: "worlds.delete"}, c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Errorf("a bare-ok reply should print ok, got %q", out)
	}
}

// The frame session rides where the caller put it — a verb addressed to
// #workspace that silently went to the default session would MINT one on disk.
func TestCtlForwardsTheFrameSession(t *testing.T) {
	c := &fakeCaller{reply: `{}`}
	if _, err := runCtlCall(t, ctlOptions{verb: "files.list", sess: ctrlproto.AddrWorkspace}, c); err != nil {
		t.Fatal(err)
	}
	if c.sess != ctrlproto.AddrWorkspace {
		t.Errorf("frame session = %q, want %q", c.sess, ctrlproto.AddrWorkspace)
	}
}

func TestCtlRefusesAVerbTheBuildDoesNotServe(t *testing.T) {
	if servedMethod("worlds.lst") {
		t.Error("a misspelled verb must not be served")
	}
	if !servedMethod(ctrlproto.MethodWorldsList) {
		t.Error("a real verb must be served — ServedMethods is the authority for --list")
	}
}

// --list has to name verbs that actually answer. The dispatch TABLE is the
// authority rather than the Method constants: a constant with no entry names a
// verb the server refuses, and listing it sends a caller after nothing.
func TestServedMethodsComesFromTheTable(t *testing.T) {
	got := ctrlproto.ServedMethods()
	if len(got) < 50 {
		t.Fatalf("only %d served methods — the table should be large", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("--list must be sorted and unique: %q then %q", got[i-1], got[i])
		}
	}
	// subscribe/unsubscribe are answered by serveState, not the table, so they
	// are not one-shot commands and must not be offered as such.
	for _, m := range got {
		if m == "subscribe" || m == "unsubscribe" {
			t.Errorf("%q is a connection verb, not a callable command", m)
		}
	}
}

func TestParseCtlArgs(t *testing.T) {
	t.Run("verb and params", func(t *testing.T) {
		o, err := parseCtlArgs([]string{"worlds.doctor", `{"id":"x"}`})
		if err != nil {
			t.Fatal(err)
		}
		if o.verb != "worlds.doctor" || o.params != `{"id":"x"}` {
			t.Errorf("got %+v", o)
		}
	})
	t.Run("--flag=value and --flag value both work", func(t *testing.T) {
		a, err := parseCtlArgs([]string{"worlds.list", "--select=worlds.id"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseCtlArgs([]string{"worlds.list", "--select", "worlds.id"})
		if err != nil {
			t.Fatal(err)
		}
		if a.sel != "worlds.id" || b.sel != "worlds.id" {
			t.Errorf("a=%q b=%q", a.sel, b.sel)
		}
	})
	t.Run("--shape and --select are mutually exclusive", func(t *testing.T) {
		if _, err := parseCtlArgs([]string{"worlds.list", "--shape", "--select", "worlds.id"}); err == nil {
			t.Error("two output modes at once must be refused, not silently ranked")
		}
	})
	t.Run("a missing verb is refused", func(t *testing.T) {
		if _, err := parseCtlArgs(nil); err == nil {
			t.Error("no verb must be an error")
		}
	})
	t.Run("an unknown flag is refused rather than read as a verb", func(t *testing.T) {
		if _, err := parseCtlArgs([]string{"--shpae", "worlds.list"}); err == nil {
			t.Error("a typo'd flag must not fall through to the positional list")
		}
	})
	t.Run("--timeout takes a duration", func(t *testing.T) {
		o, err := parseCtlArgs([]string{"worlds.list", "--timeout", "90s"})
		if err != nil {
			t.Fatal(err)
		}
		if o.timeout.Seconds() != 90 {
			t.Errorf("timeout = %s", o.timeout)
		}
		if _, err := parseCtlArgs([]string{"worlds.list", "--timeout", "90"}); err == nil {
			t.Error("a bare number is not a duration and must be refused")
		}
	})
}

func TestCtlParams(t *testing.T) {
	got, err := ctlParams("")
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := got.(map[string]any); !ok || len(m) != 0 {
		t.Errorf("no params should send an empty object, got %#v", got)
	}
	if _, err := ctlParams("{not json"); err == nil {
		t.Error("malformed params must be refused before the dial")
	}
}

// The router owns exactly one word. A command that answered to anything else
// would shadow a real one.
func TestRunCtlCommandOnlyHandlesCtl(t *testing.T) {
	if handled, _ := runCtlCommand([]string{"project", "status"}, "test"); handled {
		t.Error("ctl claimed another subcommand")
	}
	if handled, _ := runCtlCommand(nil, "test"); handled {
		t.Error("ctl claimed an empty argv")
	}
}
