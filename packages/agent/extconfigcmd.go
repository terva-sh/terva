package agent

// terva ext config — read and change an extension's declared settings from a
// script, as the owning UID, without a browser.
//
// The load-bearing decision is that this command is not a new writer. Every
// other route to an extension's settings goes THROUGH the running instance:
// the browser form and the terminal dialog both submit to the process that owns
// config.json, which persists the values and pushes them to the live extension
// in one step. A CLI that wrote the file behind that process would race its next
// rewrite, and would leave the running extension holding the old value even when
// the write survived — a change that looks applied and is not. So the daemon
// path is the real one: dial the endpoint `terva attach` dials, submit the
// action the browser submits. The local-disk path is for when nothing is
// running, and it says which one it took rather than letting the two look alike.
//
// Secrets keep the contract the wire already keeps. A secret's value is never
// read back — not by `get`, not by `show`, not over ctrlproto — and never
// accepted on argv, where it would be readable in `ps` and would land in shell
// history; --from-file and --stdin are the way in. A blank secret in a submitted
// form means "leave it alone", which is what lets `set` of some other key
// round-trip the form without ever holding it, and is why clearing one is its
// own operation rather than a blank submit (see build.ClearExtensionConfigKey).

import (
	"context"
	"encoding/json"
	"errors"
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

// extConfigEndpointEnv names a running daemon without putting it in every
// command line — the deployment that knows its socket path exports this once.
const extConfigEndpointEnv = "TERVA_ATTACH"

// extConfigOpts are the flags, in the order they matter: where to write, which
// extension directory, and where a value comes from.
type extConfigOpts struct {
	endpoint    string
	endpointSet bool
	offline     bool
	dir         string
	asJSON      bool
	fromFile    string
	stdin       bool
	token       string
	tokenFile   string
	// dialTimeout overrides how long an explicitly named endpoint is given to
	// answer. Not a flag — it exists so a test can assert the no-fallback rule
	// without waiting out the human-facing timeout.
	dialTimeout time.Duration
}

// extConfigIO separates the data stream from the chatter: a value goes to out
// and everything a human reads goes to msg, so `terva ext config x get y` in a
// backtick is the value and nothing else. Injected rather than hardcoded to the
// process's own descriptors, which is what makes the verbs testable.
type extConfigIO struct{ out, msg io.Writer }

// extConfigStore is the two transports behind one small surface. Both persist
// and both validate; only one of them reaches a running extension.
type extConfigStore interface {
	// form is the extension's declared fields with the values saved for them.
	form(ctx context.Context) ([]build.ConfigFormField, error)
	// save submits a WHOLE form — a field left blank falls back to its default,
	// a blank secret keeps the stored value.
	save(ctx context.Context, values map[string]string) error
	// clear deletes one saved value, the thing a form cannot say.
	clear(ctx context.Context, key string) error
	// where names what this transport writes: a config file, or a daemon. It
	// goes on every confirmation line because "which home did that touch" is
	// the first question anyone asks of a machine running more than one terva.
	where() string
	// live reports whether a change here reaches the running extension.
	live() bool
	close()
}

// errExtConfigHelp marks "the user asked for the usage text", which prints the
// same words as a usage error and exits 0 rather than 1.
var errExtConfigHelp = errors.New("ext config: help requested")

func extConfigUsage() error {
	return i18n.Errorf(`usage:
  terva ext config <name>                     show the declared settings and their values
  terva ext config <name> get <key>           print one value (never a secret)
  terva ext config <name> set <key>=<value>…  set one or more values
  terva ext config <name> set <key> --stdin   set one value read from stdin (the secret route)
  terva ext config <name> unset <key>…        clear back to the declared default

flags:
  --endpoint <url>   write through a running terva (ws://host:port, unix:/path/to.sock)
  --offline          write config.json directly; refuse to look for a daemon
  --dir <path>       the extension's directory, for a --ext load outside the install roots
  --json             machine-readable output for the show verb
  --from-file <path> read the value from a file instead of the command line
  --token/--token-file  authenticate to a daemon that requires a token`)
}

// extConfig dispatches `terva ext config …`.
func extConfig(argv []string, version string) error {
	pos, o, err := parseExtConfigArgs(argv)
	if errors.Is(err, errExtConfigHelp) {
		fmt.Fprintln(os.Stderr, extConfigUsage())
		return nil
	}
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return extConfigUsage()
	}
	name := pos[0]
	verb := "show"
	if len(pos) > 1 {
		verb = pos[1]
	}
	rest := pos[min(len(pos), 2):]

	ctx := context.Background()
	store, err := openExtConfigStore(ctx, name, o, version)
	if err != nil {
		return err
	}
	defer store.close()
	return runExtConfigVerb(ctx, store, extConfigIO{out: os.Stdout, msg: os.Stderr}, name, verb, rest, o)
}

func runExtConfigVerb(ctx context.Context, store extConfigStore, w extConfigIO, name, verb string, rest []string, o extConfigOpts) error {
	switch verb {
	case "show", "list":
		if len(rest) > 0 {
			return extConfigUsage()
		}
		return extConfigShow(ctx, store, w, name, o)
	case "get":
		if len(rest) != 1 {
			return i18n.Errorf("usage: terva ext config %s get <key>", name)
		}
		return extConfigGet(ctx, store, w, name, rest[0])
	case "set":
		return extConfigSet(ctx, store, w, name, rest, o)
	case "unset", "clear":
		if len(rest) == 0 {
			return i18n.Errorf("usage: terva ext config %s unset <key>…", name)
		}
		return extConfigUnset(ctx, store, w, name, rest)
	default:
		return extConfigUsage()
	}
}

// parseExtConfigArgs splits flags from positionals, accepting both --flag value
// and --flag=value. Hand-rolled rather than flag.FlagSet because the verbs come
// before the flags in every natural spelling of these commands, and the stdlib
// parser stops at the first positional.
func parseExtConfigArgs(argv []string) ([]string, extConfigOpts, error) {
	var (
		pos []string
		o   extConfigOpts
	)
	valueFor := func(i *int, name, inline string, present bool) (string, error) {
		if present {
			return inline, nil
		}
		if *i+1 >= len(argv) {
			return "", i18n.Errorf("%s needs a value", name)
		}
		*i++
		return argv[*i], nil
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		// Both spellings, because the short aliases below are useless if only
		// "--" is recognised as introducing a flag.
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flag, inline, hasInline := strings.Cut(a, "=")
		var err error
		switch flag {
		case "--endpoint", "-e":
			o.endpoint, err = valueFor(&i, flag, inline, hasInline)
			o.endpointSet = true
		case "--dir":
			o.dir, err = valueFor(&i, flag, inline, hasInline)
		case "--from-file":
			o.fromFile, err = valueFor(&i, flag, inline, hasInline)
		case "--token":
			o.token, err = valueFor(&i, flag, inline, hasInline)
		case "--token-file":
			o.tokenFile, err = valueFor(&i, flag, inline, hasInline)
		case "--offline":
			o.offline = true
		case "--json":
			o.asJSON = true
		case "--stdin":
			o.stdin = true
		case "--help", "-h":
			// Asking for help is not a failure — the same usage text, but the
			// caller prints it and exits 0 rather than 1.
			return nil, o, errExtConfigHelp
		default:
			return nil, o, i18n.Errorf("unknown flag %q", flag)
		}
		if err != nil {
			return nil, o, err
		}
	}
	return pos, o, nil
}

// openExtConfigStore picks the transport: a daemon when one is NAMED, this
// process on the file otherwise.
//
// A named endpoint that does not answer is an error, never a quiet fallback to
// the file — naming a daemon asserts that one is running, and writing behind it
// is the outcome this command exists to avoid.
//
// Nothing is PROBED unasked, and that distinction is the interesting half. An
// earlier draft dialled terva web's default listener when no endpoint was given,
// so the command would "just work" against a local daemon. It found one
// immediately — an unrelated instance serving a DIFFERENT $TERVA_HOME — and
// cheerfully offered to write that home's config.json for a command whose
// environment named another. A config file belongs to a home and the handshake
// does not say which home a daemon holds, so there was nothing to check the
// guess against.
//
// Reading the daemon record has no such gap, because the record lives INSIDE the
// home under discussion: a daemon named there is serving this home by
// construction. That is why discovery is safe here and a port probe was not.
func openExtConfigStore(ctx context.Context, name string, o extConfigOpts, version string) (extConfigStore, error) {
	if o.offline {
		if o.endpointSet {
			return nil, i18n.Errorf("--offline and --endpoint ask for opposite things")
		}
		return newLocalExtConfigStore(name, o)
	}
	endpoint, needsToken := o.endpoint, false
	if !o.endpointSet {
		endpoint = strings.TrimSpace(os.Getenv(extConfigEndpointEnv))
	}
	discovered := false
	if endpoint == "" {
		if rec, ok := config.ReadListenRecord(); ok && rec.Endpoint != "" {
			endpoint, discovered, needsToken = rec.Endpoint, true, rec.Auth
		}
	}
	if endpoint == "" {
		return newLocalExtConfigStore(name, o)
	}
	// Resolved once, here, rather than inside the dial: reading the environment
	// SCRUBS the token (build.webTokenFromEnv, so the agent's own shell cannot
	// read it back out), which makes "do we have one" a question worth asking
	// exactly once and then carrying the answer.
	token, err := build.ResolveAttachToken(build.Args{Token: o.token, WebTokenFile: o.tokenFile})
	if err != nil {
		return nil, err
	}
	// A discovered daemon that wants a token is worth naming BEFORE the dial:
	// the handshake failure it would otherwise produce says nothing about which
	// of the two possible problems it is.
	if discovered && needsToken && token == "" {
		return nil, i18n.Errorf("the terva at %s requires a token — set %s, pass --token-file, or use --offline to write the file directly", endpoint, build.WebTokenEnv)
	}
	timeout := attachConnectTimeout
	if o.dialTimeout > 0 {
		timeout = o.dialTimeout
	}
	store, err := dialExtConfig(ctx, name, endpoint, token, version, timeout)
	if err != nil {
		return nil, err
	}
	if discovered {
		fmt.Fprintf(os.Stderr, "found a running terva at %s\n", endpoint)
	}
	return store, nil
}

// --- the local transport ---

type localExtConfigStore struct {
	name string
	dir  string
}

func newLocalExtConfigStore(name string, o extConfigOpts) (extConfigStore, error) {
	dir := o.dir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		found, err := config.FindExtensionDirIn(cwd, name)
		if err != nil {
			return nil, i18n.Errorf("no installed extension named %q: %v\n"+
				"a --ext load lives outside the install roots — name its directory with --dir <path>, "+
				"or reach the terva that loaded it with --endpoint", name, err)
		}
		dir = found
	}
	return &localExtConfigStore{name: name, dir: dir}, nil
}

func (l *localExtConfigStore) form(context.Context) ([]build.ConfigFormField, error) {
	return build.ExtensionConfigFormIn(l.dir, l.name), nil
}

func (l *localExtConfigStore) save(_ context.Context, values map[string]string) error {
	return build.SetExtensionConfigFormIn(l.dir, l.name, values)
}

func (l *localExtConfigStore) clear(_ context.Context, key string) error {
	return build.ClearExtensionConfigKey(l.name, key)
}

func (l *localExtConfigStore) where() string { return config.ConfigPath() }

func (l *localExtConfigStore) live() bool { return false }

func (l *localExtConfigStore) close() {}

// --- the daemon transport ---

// extConfigWire is the slice of the control protocol this command needs. Narrow
// on purpose: it is what a test can stand in for.
type extConfigWire interface {
	Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error)
	Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error)
	SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error
}

type daemonExtConfigStore struct {
	svc     extConfigWire
	sess    string
	name    string
	addr    string
	closeFn func()
}

func dialExtConfig(ctx context.Context, name, raw, token, version string, timeout time.Duration) (extConfigStore, error) {
	// The dial, the run loop, and the wait-for-handshake live in ctrldial.go —
	// `terva ctl` needed the same forty lines, and the failure text (which of
	// "cannot reach" and "no handshake" it was) is the part nobody re-derives
	// correctly the second time.
	conn, err := dialCtrl(ctx, raw, token, "terva-ext-config", version, timeout)
	if err != nil {
		return nil, i18n.Errorf("ext config: %v", err)
	}
	svc := conn.Client.Service()
	sess, err := extConfigSession(ctx, svc)
	if err != nil {
		conn.Stop()
		return nil, i18n.Errorf("ext config: sessions.list: %v", err)
	}
	return &daemonExtConfigStore{svc: svc, sess: sess, name: name, addr: conn.Addr, closeFn: conn.Stop}, nil
}

// extConfigSession picks the session to address. The extensions surface is
// session-scoped, and the empty address MINTS one when the daemon has none —
// which a config read has no business doing, so an existing session is preferred
// and the empty address is the last resort rather than the default.
func extConfigSession(ctx context.Context, svc extConfigWire) (string, error) {
	infos, err := svc.Sessions(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range infos {
		if s.Current {
			return s.ID, nil
		}
	}
	if len(infos) > 0 {
		return infos[0].ID, nil
	}
	return "", nil
}

func (d *daemonExtConfigStore) form(ctx context.Context) ([]build.ConfigFormField, error) {
	s, err := d.svc.Surface(ctx, d.sess, "extensions")
	if err != nil {
		return nil, i18n.Errorf("ext config: surface.get extensions: %v", err)
	}
	if s.Extensions == nil {
		return nil, i18n.Errorf("the terva at %s serves no extensions pane", d.addr)
	}
	for _, e := range s.Extensions.Extensions {
		if e.Name != d.name {
			continue
		}
		return wireConfigForm(e.Config), nil
	}
	return nil, i18n.Errorf("the terva at %s has loaded no extension named %q", d.addr, d.name)
}

// wireConfigForm maps the transport's field onto the assembler's. The two carry
// the same information by construction — the wire type is the same form with
// json tags — and this is the seam where that stays true.
func wireConfigForm(in []ctrlproto.ExtensionConfigField) []build.ConfigFormField {
	out := make([]build.ConfigFormField, 0, len(in))
	for _, f := range in {
		out = append(out, build.ConfigFormField{
			Key:         f.Key,
			Label:       f.Label,
			Type:        f.Type,
			Description: f.Description,
			Required:    f.Required,
			Secret:      f.Secret,
			Options:     f.Options,
			Saved:       f.Saved,
			Default:     f.Default,
			HasSaved:    f.HasSaved,
		})
	}
	return out
}

func (d *daemonExtConfigStore) save(ctx context.Context, values map[string]string) error {
	blob, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return d.svc.SurfaceAction(ctx, d.sess, "extensions", "set_config", map[string]string{
		"name":   d.name,
		"values": string(blob),
	})
}

func (d *daemonExtConfigStore) clear(ctx context.Context, key string) error {
	return d.svc.SurfaceAction(ctx, d.sess, "extensions", "clear_config_key", map[string]string{
		"name": d.name,
		"key":  key,
	})
}

func (d *daemonExtConfigStore) where() string { return d.addr }

func (d *daemonExtConfigStore) live() bool { return true }

func (d *daemonExtConfigStore) close() {
	if d.closeFn != nil {
		d.closeFn()
	}
}

// --- the verbs ---

func extConfigShow(ctx context.Context, store extConfigStore, w extConfigIO, name string, o extConfigOpts) error {
	fields, err := store.form(ctx)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		if o.asJSON {
			fmt.Fprintln(w.out, "[]")
			return nil
		}
		fmt.Fprintf(w.msg, "%s declares no configurable settings\n", name)
		return nil
	}
	if o.asJSON {
		return extConfigPrintJSON(w.out, fields)
	}
	fmt.Fprintf(w.msg, "%s — %d setting(s)  [%s]\n", name, len(fields), store.where())
	fmt.Fprintf(w.out, "%-24s  %-8s  %-28s  %s\n", "key", "type", "value", "default")
	for _, f := range fields {
		fmt.Fprintf(w.out, "%-24s  %-8s  %-28s  %s\n",
			f.Key, dashIfEmpty(f.Type), extConfigDisplay(f), dashIfEmpty(f.Default))
	}
	return nil
}

// extConfigJSONField is the machine-readable row. It has no Value for a secret
// — the same rule the wire follows, for the same reason: whoever is reading this
// asked what is configured, not what the password is.
type extConfigJSONField struct {
	Key         string   `json:"key"`
	Type        string   `json:"type,omitempty"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Options     []string `json:"options,omitempty"`
	Value       string   `json:"value,omitempty"`
	Default     string   `json:"default,omitempty"`
	Set         bool     `json:"set"`
}

func extConfigPrintJSON(w io.Writer, fields []build.ConfigFormField) error {
	rows := make([]extConfigJSONField, 0, len(fields))
	for _, f := range fields {
		row := extConfigJSONField{
			Key: f.Key, Type: f.Type, Label: f.Label, Description: f.Description,
			Required: f.Required, Secret: f.Secret, Options: f.Options,
			Default: f.Default, Set: extConfigIsSet(f),
		}
		if !f.Secret {
			row.Value = f.Saved
		}
		rows = append(rows, row)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func extConfigGet(ctx context.Context, store extConfigStore, w extConfigIO, name, key string) error {
	fields, err := store.form(ctx)
	if err != nil {
		return err
	}
	f, ok := findExtConfigField(fields, key)
	if !ok {
		return unknownExtConfigKey(name, key, fields)
	}
	if f.Secret {
		return i18n.Errorf("%s.%s is a secret and is never printed — `show` reports whether one is set", name, key)
	}
	// The EFFECTIVE value: what the extension will see, which is the saved value
	// when there is one and the declared default when there is not. Bare on
	// stdout, so `$(terva ext config x get y)` is the value and nothing else.
	if f.Saved != "" {
		fmt.Fprintln(w.out, f.Saved)
		return nil
	}
	fmt.Fprintln(w.out, f.Default)
	return nil
}

func extConfigSet(ctx context.Context, store extConfigStore, w extConfigIO, name string, args []string, o extConfigOpts) error {
	fields, err := store.form(ctx)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return i18n.Errorf("%s declares no configurable settings", name)
	}
	values := submitExtConfigForm(fields)
	var changed []string

	switch {
	case o.fromFile != "" || o.stdin:
		if o.fromFile != "" && o.stdin {
			return i18n.Errorf("--from-file and --stdin name two different sources for one value")
		}
		if len(args) != 1 || strings.Contains(args[0], "=") {
			return i18n.Errorf("usage: terva ext config %s set <key> --stdin   (one key, value on stdin)", name)
		}
		f, ok := findExtConfigField(fields, args[0])
		if !ok {
			return unknownExtConfigKey(name, args[0], fields)
		}
		raw, err := readExtConfigValue(o)
		if err != nil {
			return err
		}
		v, err := build.NormalizeFormValue(f, raw)
		if err != nil {
			return err
		}
		values[f.Key] = v
		changed = append(changed, f.Key)
	default:
		if len(args) == 0 {
			return i18n.Errorf("usage: terva ext config %s set <key>=<value>…", name)
		}
		for _, a := range args {
			key, val, ok := strings.Cut(a, "=")
			if !ok {
				return i18n.Errorf("%q is not key=value (a secret takes its value from --from-file or --stdin instead)", a)
			}
			f, ok := findExtConfigField(fields, key)
			if !ok {
				return unknownExtConfigKey(name, key, fields)
			}
			if f.Secret {
				return i18n.Errorf("%s is a secret: a value on the command line is readable in `ps` and lands in your shell history — pass it with --from-file <path> or --stdin", key)
			}
			v, err := build.NormalizeFormValue(f, val)
			if err != nil {
				return err
			}
			values[f.Key] = v
			changed = append(changed, f.Key)
		}
	}

	if err := store.save(ctx, values); err != nil {
		return err
	}
	for _, k := range changed {
		f, _ := findExtConfigField(fields, k)
		shown := values[k]
		if f.Secret {
			shown = "(set)"
		}
		fmt.Fprintf(w.msg, "set %s.%s = %s  [%s]\n", name, k, shown, store.where())
	}
	reportExtConfigReach(store, w)
	return nil
}

func extConfigUnset(ctx context.Context, store extConfigStore, w extConfigIO, name string, keys []string) error {
	fields, err := store.form(ctx)
	if err != nil {
		return err
	}
	// Every key checked before anything is cleared: a typo in the third of three
	// should not leave the first two applied.
	for _, k := range keys {
		if _, ok := findExtConfigField(fields, k); !ok {
			return unknownExtConfigKey(name, k, fields)
		}
	}
	for _, k := range keys {
		if err := store.clear(ctx, k); err != nil {
			return err
		}
		f, _ := findExtConfigField(fields, k)
		fmt.Fprintf(w.msg, "unset %s.%s  [%s]\n", name, k, store.where())
		if f.Required && f.Default == "" {
			fmt.Fprintf(w.msg, "warning: %s is required and declares no default — the extension may refuse to start\n", k)
		}
	}
	reportExtConfigReach(store, w)
	return nil
}

// reportExtConfigReach says what the write did NOT do, when it did not do it. A
// local write persists and stops there; the running extension keeps the old
// value until something reloads it, and that is exactly the failure this
// command was asked to end, so it is stated rather than implied.
func reportExtConfigReach(store extConfigStore, w extConfigIO) {
	if store.live() {
		return
	}
	fmt.Fprintf(w.msg, "note: nothing serving this home was contacted — a terva already running keeps the old value until it reloads.\n"+
		"      `terva web` publishes its endpoint here automatically; for anything else pass --endpoint <url> or set %s.\n", extConfigEndpointEnv)
}

// --- shared helpers ---

// submitExtConfigForm renders the current form as the map a submit expects:
// every field present, in display form, secrets blank — which the host reads as
// "keep what is stored", and is why a client that cannot see a secret can still
// edit the field next to it.
func submitExtConfigForm(fields []build.ConfigFormField) map[string]string {
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		if f.Secret {
			out[f.Key] = ""
			continue
		}
		out[f.Key] = f.Saved
	}
	return out
}

func findExtConfigField(fields []build.ConfigFormField, key string) (build.ConfigFormField, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f, true
		}
	}
	return build.ConfigFormField{}, false
}

func unknownExtConfigKey(name, key string, fields []build.ConfigFormField) error {
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		keys = append(keys, f.Key)
	}
	if len(keys) == 0 {
		return i18n.Errorf("%s declares no configurable settings", name)
	}
	return i18n.Errorf("%s declares no setting %q — it has: %s", name, key, strings.Join(keys, ", "))
}

// extConfigIsSet reports whether the user has stored a value, which for a secret
// is all a client is told.
func extConfigIsSet(f build.ConfigFormField) bool {
	if f.Secret {
		return f.HasSaved
	}
	return f.Saved != ""
}

// extConfigDisplay renders one value for the table. A secret shows whether it
// exists and nothing more.
func extConfigDisplay(f build.ConfigFormField) string {
	if f.Secret {
		if f.HasSaved {
			return "(set)"
		}
		return "(unset)"
	}
	return dashIfEmpty(f.Saved)
}

// readExtConfigValue reads a value from a file or stdin, dropping ONE trailing
// newline — the one `echo` or an editor adds. Anything more is left alone: a
// secret's trailing whitespace is the secret's business.
func readExtConfigValue(o extConfigOpts) (string, error) {
	var (
		raw []byte
		err error
	)
	if o.fromFile != "" {
		raw, err = os.ReadFile(o.fromFile)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return "", err
	}
	return trimOneNewline(string(raw)), nil
}

func trimOneNewline(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}
