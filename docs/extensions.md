# terva extensions

terva can be extended with custom slash commands by running an external
program as a subprocess and exchanging newline-delimited JSON over
its stdin/stdout. Extensions can be written in **any language** that
can read and write JSON lines from stdio — Go, TypeScript, Python,
Rust, shell with `jq`, anything.

Four phases shipped so far:

- **Phase 1**: slash commands + chat notifications.
- **Phase 2**: tools the LLM can call.
- **Phase 3**: lifecycle event subscriptions + tool-call interception
  for guardrail extensions.
- **Phase 4**: interactive extension-owned panels rendered inside terva.
- **Theme-only extensions**: ship `theme.json` without launching a
  subprocess. See [themes.md](themes.md).

## Quick start

The simplest extension is a script that prints a hello frame, reads
commands, and prints responses. Here's the whole thing in **Python**,
no SDK required:

```python
#!/usr/bin/env python3
# $TERVA_HOME/extensions/hello-py/hello.py
import json, sys, threading

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

emit({"type":"hello","name":"hello-py","version":"1.0.0","capabilities":["commands"]})
emit({"type":"register_command","name":"hellopy","description":"say hi (python)"})

for line in sys.stdin:
    msg = json.loads(line)
    if msg["type"] == "command_invoked":
        emit({"type":"command_response","id":msg["id"],"action":"prompt",
              "prompt": "Greet me very briefly. Add one emoji."})
    elif msg["type"] == "shutdown":
        emit({"type":"shutdown_ack"})
        break
```

Drop it in a directory with this `extension.json`:

```json
{
  "name": "hello-py",
  "version": "1.0.0",
  "exec": "./hello.py",
  "language": "python",
  "enabled": true
}
```

`exec` is required for protocol extensions. If an extension only ships
`theme.json` or `themes/theme.json`, no `exec` is required and terva does
not spawn a subprocess.

`chmod +x hello.py`, install:

```bash
terva ext install ./hello-py
```

Restart `terva`, type `/hellopy`, the agent greets you. Done.

## Built-in extensions

**terva ships with no extensions installed by default.** A fresh `terva install` (or `go install`) gives you a clean agent. Extensions are entirely opt-in: you install (or `--ext` for one run) only the ones you want.

The `examples/extensions/` directory in the repo is reference code, not a default install set. To use any of those:

```bash
# go-based examples need a build first
cd path/to/terva/examples/extensions/hello && go build -o hello .

# install (copies to $TERVA_HOME/extensions/hello/)
terva ext install path/to/terva/examples/extensions/hello

# or load straight from the repo for one terva session
terva --ext path/to/terva/examples/extensions/hello
```

Nothing is auto-installed and nothing reaches out to the network without your explicit action.

Per-run scoping: `--extensions calendar,index` loads ONLY the named
installed extensions for that run (restrict-only — explicit `--ext`
paths bypass it, and a config `disable_extensions` entry still
subtracts). This is the least-privilege composition flag for exposed
agents: a Discord bot admitted to a group room can run with
`--extensions calendar` and your mail extension simply never spawns.
`--no-ext` remains the all-off form.

## Extension packs

A **pack** is a hosted manifest naming a set of extensions, so you can
install a useful starting set in one step instead of N `ext install`
calls:

```bash
terva ext pack install              # the built-in "core" pack (default)
terva ext pack install core         # same, explicitly
terva ext pack install https://example.com/team-pack.json
terva ext pack install ./pack.json  # a local manifest
```

A pack is just a **list of sources** — terva clones each one and spawns
it exactly as `ext install` does. It carries no binaries or checksums:
each extension owns its own bring-up (the recommended
[self-bootstrapping launcher](#recommended-a-self-bootstrapping-launcher)
compiles on first run, or downloads a verified release binary when no
compiler is present), so binary integrity is the extension's
responsibility, not terva's. An already-installed entry is skipped, so
re-running a pack is safe. Lifecycle afterwards is the normal per-extension tools
(`terva ext list` / `enable` / `disable` / `remove`) — a pack is a
starting point, not a managed set.

> **A pack is not a deployment lock.** It pins no versions, carries no digests,
> and is consulted only at install time — nothing re-checks it afterwards, and
> nothing stops the installed set from drifting via `ext install`, `remove`, or
> an edit on disk. It answers "give me a reasonable starting set", not "this
> agent runs exactly this code". If you need the latter — attesting which
> immutable code a given account runs — a pack is the wrong tool. That contract
> is the subject of the internal managed-extension-catalog proposal.

Installing from a non-built-in pack (a URL or file) prints the entries
and asks for confirmation first; `--yes` skips the prompt. The built-in
core pack ships with terva and installs without prompting.

**Migrating earlier manual installs.** If you installed an extension by
hand before adopting a pack, your copy may live under a different
directory name than the pack's canonical one (e.g. `zot-web/` vs the
pack's `web/`) — terva would then see two near-identical extensions. Pack
install detects these look-alikes (by git origin, manifest name, and
directory name) and offers to **rename** each to the canonical name,
which preserves its enabled-state, config, and any local edits. A
confident match (the git origin matches the pack source) auto-confirms
under `--yes`; an uncertain, name-only match always asks and is skipped
under `--yes`. Use `--no-migrate` to disable it, `--dry-run` to preview:

```bash
terva ext pack install core --dry-run    # show what would install + migrate
terva ext migrate                        # reconcile look-alikes without (re)installing
terva ext migrate --dry-run              # preview migrations only
```

A pack manifest is JSON:

```json
{
  "schema": "terva-extension-pack/v1",
  "name": "core",
  "description": "The terva core extension set.",
  "extensions": [
    { "name": "index", "source": "https://github.com/terva-sh/terva-ext-index.git", "ref": "v0.2.0" }
  ]
}
```

Each entry needs a `source` (git URL or local path). `ref` is an
optional branch or tag (absent → the repo's default branch); `name`
defaults to the source basename. See
`docs/plans/extension-packs.md` for the full schema.

### First-run offer

The very first time you start an interactive session with **no
extensions installed**, terva offers to install the core pack. It asks
at most once, only on an interactive terminal (never when input is
piped or in CI), and going through `install.sh` never triggers it. Say
no and it won't ask again; install later with
`terva ext pack install core`.

Suppress the offer entirely (e.g. for fleet provisioning) with user
config:

```json
{ "disable_core_pack_offer": true }
```

## Layout & discovery

terva scans two directories on startup, in this order:

1. **Project-local**: `./.terva/extensions/<name>/extension.json`
2. **Global**: `$TERVA_HOME/extensions/<name>/extension.json`

A project-local extension whose **directory name** matches a global one wins.
Note that precedence is by directory basename, not by the `name` field in the
manifest — two different rules apply in sequence:

1. **Discovery** deduplicates by directory basename, so the project copy of
   `foo/` shadows the global `foo/`.
2. **Loading** then claims the *manifest* name, first claim wins. If two
   surviving directories declare the same manifest name, one is loaded and the
   other is **dropped silently** — no error, no log line. Loads run in
   parallel, so which copy wins is not deterministic.

Keep each extension's directory name equal to its manifest name and the two
rules agree. When they differ, a name collision becomes an invisible coin
toss — so avoid it.
On macOS `$TERVA_HOME` defaults to `~/Library/Application Support/terva/`;
on Linux it's `$XDG_STATE_HOME/terva` or `~/.local/state/terva`.

> **Project-scoped agents.** To run a directory as a *self-contained* agent —
> only its own extensions, none of your global/system ones, and all data kept in
> the project — see [Project-scoped agents](#project-scoped-agents) below.

**Write state to `data_dir`, not to the install directory.** `hello_ack`
passes back two *different* paths, and mixing them up is the usual cause of a
read-only-filesystem failure on a managed install:

- `extension_dir` — where the extension's code was installed. Treat it as
  read-only. A root-managed or otherwise externally administered deployment may
  make it literally read-only.
- `data_dir` — the extension's own writable state directory,
  `$TERVA_HOME/ext-data/<manifest name>`. Keyed by manifest name, so it is
  stable even if the install location moves. This is where `todos.json`,
  `settings.json`, or an auth/cache file belongs.

The host falls back to using the install directory as `data_dir` only when
there is no terva home or that directory cannot be created — a compatibility
path for older colocated layouts, not a location to design against. Do not
assume the two paths are equal.

Each extension owns its own subdirectory. The `extension.json`
manifest tells terva how to launch it:

```json
{
  "name": "weather",
  "version": "1.0.0",
  "exec": "./weather",
  "args": ["--mode", "daemon"],
  "language": "go",
  "description": "current weather for any city",
  "enabled": true
}
```

| field | meaning |
|---|---|
| `name` | required. how terva identifies the extension; must match what's sent in the `hello` frame. |
| `version` | optional. shown in `terva ext list`. |
| `exec` | required. path to the executable (relative to the manifest). |
| `args` | optional. extra argv passed to `exec`. |
| `language` | optional. informational only (`go`, `python`, `typescript`, ...). |
| `description` | optional. shown in `terva ext list`. |
| `enabled` | optional, defaults to `true`. set to `false` to disable without removing. |
| `permissions` | optional **bundle contribution**: suggested permission rules (see below). |
| `config` | optional. a schema of settings the user fills in via `/extensions` (see below). |
| `connector` | optional, experimental. declares the extension is ALSO a chat connector (see [connector role](#connector-role-experimental)); without it the host refuses the role at the wire. Global installs only. |
| `data_secrets` | optional **tri-state**. declares whether your `ext-data/<name>/` directory may hold secret material, and so whether the agent's own tools may read in there. `false` — it holds none (say this; it is what keeps your data dir the debugging surface it was meant to be). `true` — it does, and the directory is denied. **Absent — undeclared, and unknown is not clean.** See [Declaring your data directory](#declaring-your-data-directory). |

## Project-scoped agents

Sometimes you want a directory to be a **self-contained agent** — its own
persona and extensions, none of your global/system ones, with all of its data
kept inside the project. Useful for a project built around a specific persona +
extension set, for sharing a ready-to-run agent, or just to keep something out
of your global setup. Turn it on per directory:

```jsonc
// ./.terva/config.json
{ "project_scoped": true }
```

or per run with `--project` (and `--no-project` to force it off):

```bash
terva --project          # this run is project-scoped
terva                     # auto-scoped if .terva/config.json says so (once trusted)
```

The `terva project` commands set this up and keep it legible:

```bash
terva project init [--persona NAME]   # scaffold a scoped project (config + dirs + gitignored data home)
terva project status                  # what will run here: scope, trust, extensions, model
terva project trust / untrust         # trust this project (required to run scoped) / revoke
terva project model <id>              # pin this project's model (it doesn't inherit your global one)
terva project ext adopt/drop/disable/enable <name>
```

When project-scoped, terva:

- **Stores all data in the project** — sessions, `ext-data/`, logs, and
  `config.json` — under `./.terva/home/` (created for you, with a `.gitignore`
  so the runtime state is never committed). Mechanically, `$TERVA_HOME` is
  pointed at that directory for the run.
- **Loads only the project's own extensions** (`./.terva/extensions/`, still
  trust-gated) — your global `$TERVA_HOME/extensions` are not loaded.
- **Inherits your login and trust, globally.** Credentials (`auth.json`) and the
  trust store (`trusted.json`) stay in your real global home, so you don't
  re-authenticate per project, no secrets are ever written into the project, and
  a cloned repo still can't trust itself.

Authored **project personas** live in `./.terva/personas/` (committed, not
gitignored) and load **by path** — `terva --persona ./.terva/personas/NAME.md`,
which is what `terva project init --persona` scaffolds and prints for you.

Because a scoped project runs **its own** config, extensions, hooks, and system
prompt as the agent — that is the whole point — terva requires you to **trust the
directory first**. Running a `project_scoped` directory you haven't trusted
stops with an error pointing you at `terva trust` (one-time; `terva project
trust` is the alias, and `--trust` grants a single run). It's the same
[Workspace Trust](#security) boundary that gates project extensions, applied to
the entire scoped setup — so a cloned repo can't turn itself into a
self-configured agent on a plain `terva`. Once trusted, the project is safe to
commit and share: credentials stay global, so it carries its
extensions/persona/config, never your secrets.

This also works for a [chat bot](connectors.md): `terva bot run --project`
gives you a bot backed entirely by one project's persona + extensions + data.

### Adopting global extensions

A scoped project starts with upstream extensions off, but you often want a few
of your global ones back without copying binaries around. Adopt them by name:

```bash
terva project ext list             # what this project adopts + what's available globally
terva project ext adopt weather    # re-admit your global "weather" here
terva project ext drop weather
```

This records the name in `.terva/config.json`:

```jsonc
{ "project_scoped": true, "adopt_extensions": ["weather"] }
```

An adopted extension loads **from your global install** — no copy, shared
binary, with its data kept project-local — and only when the project is scoped
**and trusted** (same gate as the project's own extensions). It can only select
from extensions you already installed globally, so a scoped project is always a
*subset* of your upstream, never wider. The list travels with the project: on a
machine that doesn't have that global extension, it's simply absent rather than
an error.

## Inspecting & toggling (`/extensions`)

Run `/extensions` (alias `/ext`) in an interactive session to see every
installed extension and its state. Per-row keys:

- `g` — enable/disable globally (the manifest `enabled` flag).
- `p` — disable/enable for this project (`.terva/config.json`
  `disable_extensions`, restrict-only).
- `c` — open the [config form](#configuration) (when the extension
  declares a `config` schema).
- `l` — open a scrollable view of the extension's log
  (`$TERVA_HOME/logs/ext-<name>.log`) without leaving the TUI.

When an extension is enabled but **not running** (it crashed on spawn),
the row shows `off (not running)` and a one-line reason pulled from the
log — press `l` for the full output (or `terva ext logs <name>` from the
shell).

## Configuration

An extension can declare a `config` schema in its manifest. terva renders
it as a form in the `/extensions` dialog (highlight the row and press
`c`), stores the user's values, and delivers the resolved values to the
extension — so an extension that needs an API key or a path gets guided
setup instead of asking the user to hand-edit JSON.

```json
{
  "name": "weather",
  "exec": "./weather",
  "config": [
    { "key": "api_key", "label": "API key", "type": "secret", "required": true,
      "description": "API key for the weather provider." },
    { "key": "units", "label": "Units", "type": "select",
      "options": ["celsius", "fahrenheit"], "default": "celsius" }
  ]
}
```

Each field: `key` (required, the map key), `label`, `type` (`string`
default, `bool`, `int`, `select`, `secret`), `default`, `required`,
`description`, and `options` (for `select`). `secret` fields are masked in
the dialog and never logged.

**Where values live.** In the user config at `$TERVA_HOME/config.json`
under `extensions.<name>`, **user layer only** — a project's
`.terva/config.json` may *disable* an extension but never set its values
(a value is an escalation, not a restriction). The UI masks secret values
and the host never logs them.

### Secrets your extension acquires at runtime (protocol 6)

Config secrets are the ones the *user* types into your settings form. A secret
your extension obtains while running — an OAuth token it negotiated, a key
someone pasted into its own UI — is a different class, and before protocol 6
the only place to put it was your `ext-data/` directory, in the clear, where a
model reading files finds it.

The host brokers those instead. They live in terva's own store, sealed with
terva's key:

```go
if err := e.SetSecret("oauth_token", tok); err != nil { … }
tok, ok, err := e.Secret("oauth_token")   // ok=false means never stored
names, err := e.SecretKeys()              // names only, never values
err = e.DeleteSecret("oauth_token")
```

Declare `RequireProtocol(6)` if your extension needs them — these block on a
reply, so against an older host they would hang rather than degrade.

### Declaring your data directory

`$TERVA_HOME/ext-data/<name>/` has always been readable by the agent, on
purpose: your extension's own state is a legitimate thing for it to debug with.
That is only safe if the directory holds nothing secret — and terva cannot
prove that by looking. Scanning finds what *is* sealed; it can never show that
nothing else should have been. An `access_token` left in the clear by an old
version is indistinguishable from a `homeserver_url` that is public by design.

So you declare it, in the manifest:

```json
{ "name": "my-ext", "exec": "./my-ext", "data_secrets": false }
```

`false` is the honest answer for a new extension, because you have somewhere
better to put a secret — the broker above, where a rotation can reach it even
while your extension is stopped. `true` says the directory does hold secret
material and denies the agent access to it; treat that as a bug to fix by
moving them, not as a setting.

**Leaving it out is not neutral.** An undeclared data directory is unknown, and
unknown is not clean. It stays readable in this release and `terva secret
status` names it, so you can see the change coming:

```
  reads        ext:my-ext — will be denied to the agent in a future release:
               its manifest does not declare "data_secrets"; add
               "data_secrets": false if its data dir holds no secret material
```

Scoping is **host-enforced**: the frames carry no scope, and the host
substitutes your manifest name. One extension cannot read another's secrets,
however it spells the request.

Your extension gets no key of its own, deliberately. It never runs when terva
does not, so a key would be one more thing to generate, store, back up and
rotate for a process that is never awake alone — and brokered storage means a
key rotation reaches your secrets even while your extension is stopped.

`e.Secret` is **not** a way to read config values. Those already arrive opened,
in the register phase and on every config change; a second path to one value is
how the two drift.

Secret values are stored **plaintext** unless at-rest encryption is set up.
After `terva secret init` a `secret`-typed field is stored as an
`enc:age:v2:…` string and decrypted only on its way to the extension, so
`config.json` stops being a credential store — see
[cli.md](cli.md#secrets-at-rest-terva-secret). Nothing changes for the
extension: it receives the same cleartext value it always did.

**Delivery.** The resolved values (manifest defaults overlaid with the
user's) arrive in the `hello_ack` handshake, and again on every change as
a `config_update` event — so a live edit takes effect without a restart.
With the Go SDK:

```go
e := ext.New("weather", "1.0.0")
e.OnConfig(func(c ext.Config) {       // fired on every change (optional)
    e.Logf("config updated: api_key %v", c.Has("api_key")) // never log the value
})
e.Tool("weather", "...", schema, func(args json.RawMessage) ext.ToolResult {
    cfg := e.Config()                  // current values, always fresh
    if cfg.String("api_key") == "" {
        return ext.TextErrorResult("not configured — set an API key in /extensions (press c)")
    }
    // cfg.String(key) / cfg.Bool(key) / cfg.Int(key) / cfg.Has(key)
    ...
})
```

Backward compatible both ways: an extension with no `config` schema gets
no config form; an older host simply never sends config (the extension
keeps whatever it read at the handshake). See
`examples/extensions/weather` for a complete example.

## Recommended: a self-bootstrapping launcher

For a **compiled** extension (Go, Rust, …) the strongly recommended
pattern is to point `exec` at a small launcher script — not at a binary
you commit to the repo — and let that script own the entire
build/download story. A fresh `terva ext install <git-url>` or an
[extension pack](#extension-packs) clones source with no binary in it;
the launcher is what turns that clone into something runnable.

This keeps bring-up **inside the extension**, which is exactly where
terva wants it. terva treats an extension as an opaque subprocess: it
clones the directory and spawns `exec`, and deliberately knows nothing
about toolchains, target platforms, or release URLs. The launcher is the
one place with the context to do build-or-download well — so that
responsibility lives there, not in the host.

> **The build must fit in the hello timeout.** terva waits **10 seconds** for
> an extension's first `hello` frame; past that it kills the process group and
> skips the extension for that run, recording the failure in the extension's
> log rather than stopping startup. That budget covers the launcher *and* the
> build it decides to run. A cold Go build of a small extension measured ~5.5s,
> which fits — but a heavier toolchain on a cold cache will not, and the warm
> path (step 1 below) is far inside it, so this mostly bites on a first-ever
> launch, after `terva update`, or on a machine that has never built the
> extension.
>
> **Where that wait is paid.** In the long-lived hosts — the interactive TUI,
> `terva web`, `terva rpc` — extensions start in the *background*: the session
> materializes immediately and the wait is taken by the first turn instead, so
> a slow extension costs nothing unless you type faster than it boots. That is
> what makes a 10-second budget affordable; it used to be 3, sized to what a
> user would tolerate staring at an empty screen. The single-shot modes (`-p`,
> `--json`) and ACP still pay it up front, because their first turn begins
> right away.
>
> **Better than raising the deadline: say you're working.** A launcher can
> print a [`bootstrap` frame](#bootstrap-optional-before-hello) before it
> starts building and again as it goes; each one restarts the deadline, so the
> host is measuring silence instead of elapsed time, and terva shows your
> message rather than nothing. That is the actual split — "is this process
> alive?" stays a question with a short answer, and "how long may the build
> take?" stops being the same question. A build that reports progress can run
> for minutes; one that goes quiet is still killed on schedule.
>
> **If the default is genuinely too tight** and the launcher can't report
> progress, raise it with `extension_hello_timeout` (seconds) in
> `~/.terva/config.json`, capped at 10 minutes. User-level only: it describes
> how fast your machine builds, not a property of a repository, and a project
> must not be able to make terva sit longer on its own extensions.
>
> **For anything deployed rather than developed, prebuild or prewarm.** Ship a
> release binary, or build once out-of-band and let the launcher find it —
> never let first contact with a service be a compile; that is the real fix,
> and both the `bootstrap` frame and the timeout knob are for where it isn't
> available. If an extension seems to "not load" on a fresh install but works
> on the second try, this is why — and `terva ext doctor` now names it as
> `failed to start` with the reason, rather than only reporting it absent.
> Making the deployed catalog immutable and prebuilt is the motivation for the
> internal managed-extension-catalog proposal.

The launcher should try, in order:

1. **Use the binary** if it's present and newer than the sources — just
   `exec` it. The fast path; no rebuild on every launch.
2. **Build** from source if a compiler is available (binary missing or
   stale). This is also what makes `terva update` work: it pulls new
   source, and the next launch rebuilds because the sources are now
   newer than the binary.
3. **Download** a prebuilt release binary for the host OS/arch when there
   is no compiler — and **verify its checksum** before trusting it.
   Binary integrity is the extension's job; terva does not verify it.
4. **Fail clearly**: if none of the above worked, print how to build it
   by hand, **disable itself** in the manifest so terva stops re-spawning
   it every session, and exit non-zero. The user builds it and runs
   `terva ext enable <name>`.

Pair it with a manifest whose `exec` is the launcher. Ship `enabled`
explicitly so step 4 has a field to flip:

```json
{ "name": "index", "exec": "./run.sh", "language": "go", "enabled": true }
```

A reference `run.sh` (POSIX sh — works on Linux and macOS; Windows is a
second-class target, so ship a `run.cmd` alongside or document a manual
build):

```sh
#!/usr/bin/env sh
set -eu
# Run from the extension's own directory so relative paths resolve no
# matter what cwd terva spawned us from.
cd "$(dirname "$0")"

NAME=index          # must match extension.json "name"
BIN=./index         # the built binary

# Print build instructions, disable in the manifest, and give up.
fail() {
  echo "$NAME: $1" >&2
  echo "Build it yourself:  (cd '$(pwd)' && go build -o '$BIN' .)" >&2
  echo "Then re-enable:     terva ext enable $NAME" >&2
  # Flip "enabled" to false so terva does not re-spawn this launcher on
  # every session until the binary exists.
  if command -v jq >/dev/null 2>&1; then
    tmp=$(mktemp) && jq '.enabled=false' extension.json >"$tmp" && mv "$tmp" extension.json
  else
    sed -i.bak 's/"enabled"[[:space:]]*:[[:space:]]*true/"enabled": false/' extension.json && rm -f extension.json.bak
  fi
  exit 1
}

# Download + checksum-verify a release binary for this host. Returns
# non-zero (so the caller falls through to fail) if anything is missing.
download_release() {
  command -v curl >/dev/null 2>&1 || return 1
  os=$(uname -s | tr '[:upper:]' '[:lower:]')          # linux | darwin
  arch=$(uname -m)
  case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) return 1 ;; esac
  base="https://github.com/OWNER/REPO/releases/latest/download"   # per-extension
  asset="${NAME}_${os}_${arch}"
  curl -fsSL "$base/$asset"        -o "$BIN"        || return 1
  curl -fsSL "$base/$asset.sha256" -o "$BIN.sha256" || return 1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$BIN.sha256" >&2 || return 1
  else
    shasum -a 256 -c "$BIN.sha256" >&2 || return 1
  fi
  rm -f "$BIN.sha256"; chmod +x "$BIN"
}

# 1. Fast path: a fresh binary already exists -> hand off.
if [ -x "$BIN" ] && [ -z "$(find . -name '*.go' -newer "$BIN" -print 2>/dev/null | head -n1)" ]; then
  exec "$BIN" "$@"
fi

# 2. Build from source when a compiler is present.
if command -v go >/dev/null 2>&1; then
  if go build -o "$BIN" . >&2; then
    exec "$BIN" "$@"
  fi
  fail "build failed — see the errors above"
fi

# 3. No compiler: download a verified release binary.
if download_release; then
  exec "$BIN" "$@"
fi

# 4. Nothing worked.
fail "no Go compiler and no verified release download available"
```

`exec` matters: it **replaces** the shell with your binary, so terva's
stdin/stdout pipes connect straight to it — the launcher must never sit
between terva and the extension on the wire. Everything the launcher
prints goes to **stderr**, which terva routes to
`$TERVA_HOME/logs/ext-<name>.log`; stdout is reserved for the JSON
protocol, so build chatter there would corrupt the wire (note the `>&2`
on `go build` and the verify step).

## Bundle contributions

An installed extension directory is also a declarative bundle — it can
contribute data alongside its executable:

- **Skills**: a `skills/` directory beside `extension.json` joins
  skill discovery (`skills/<name>/SKILL.md`, same format as
  [skills.md](skills.md)). Bundle skills rank after the user's own
  skill directories, so they can never shadow a deliberately-authored
  skill, and a disabled extension contributes nothing.
- **Personas**: a `personas/` directory beside `extension.json` joins
  persona discovery (`personas/<name>.md`, same format as
  [personas.md](personas.md)). Each persona is namespaced by the
  extension name (`<ext>:<name>`) and ranks after the user's own
  personas, so it can't shadow a hand-authored one — and the user can
  override it by mirroring the namespace
  (`$TERVA_HOME/personas/<ext>/<name>.md`). A disabled extension —
  whether by manifest `enabled` or the user's `disable_extensions` —
  contributes no personas. A persona shipped with `good_for` tags
  becomes dispatchable to swarm sub-agents, so an extension can pair a
  capability (its tools) with the identity for using it.
- **Lore**: a `lore/` directory beside `extension.json` joins keyed-context
  discovery — the third tier after `$TERVA_HOME/lore/` and a trusted project's
  `.terva/lore/` (see [personas.md](personas.md)). Like skills, it is scanned
  from the global extensions root always and from a project root only when the
  workspace is trusted, and a manifest with `enabled: false` contributes
  nothing.
- **Suggested permission rules**: a `permissions` array in the
  manifest (same shape as [permissions.md](permissions.md) rules).
  Like project rules, the extension layer may only *restrict*: `deny`
  and `ask` are honored, `allow` is dropped with a warning — installing
  a bundle can tighten the posture but never grant tool access the
  user didn't. Evaluated after project rules, before user rules.

Hooks and MCP server declarations are deliberately **not**
bundle-contributable: both mean running additional programs, and that
stays an explicit user-config decision (see [hooks.md](hooks.md),
[mcp.md](mcp.md)).

> **`--extensions` does not narrow bundle contributions.** The per-run
> allowlist is applied by the extension *manager*, which decides which
> discovered extensions are **loaded and spawned**. The bundle scanners for
> skills, personas, and lore walk the extension roots themselves and honor only
> the manifest `enabled` flag and the user's `disable_extensions` — not the
> allowlist. So `--extensions calendar` stops the `mail` extension's *process*
> from starting while its skills, personas, and lore still reach the prompt. To
> exclude a bundle's contributions entirely, disable the extension
> (`disable_extensions`, or `enabled: false`) rather than relying on the
> allowlist. Consolidating this into one resolution result is Phase 1 of the internal
> managed-extension-catalog proposal.

## Lifecycle

1. **Discovery**: terva reads every `extension.json` in the search dirs.
2. **Spawn**: enabled extensions are launched as subprocesses. stderr
   redirects to `$TERVA_HOME/logs/ext-<name>.log` (one file per
   extension, append-mode). The child environment is the host's minus
   loader/interpreter injection vars (`LD_*`, `DYLD_*`, `PYTHONPATH`,
   `NODE_OPTIONS`, `JAVA_TOOL_OPTIONS`, `BASH_ENV`, …) — an extension
   that needs one of those must set it itself for its own children.
   `PATH`, `HOME`, API keys, and everything else pass through.
3. **Hello handshake**: the extension sends a `hello` frame; terva
   replies with `hello_ack` containing the protocol version, the
   active provider/model/cwd, and the extension's own data directory
   so it can persist files beside its manifest. A launcher that has to
   build first may send `bootstrap` frames beforehand to report
   progress and restart the deadline. An extension that never gets
   this far is killed and skipped, and `terva ext doctor` reports it as
   `failed to start` with the reason.
4. **Registration**: the extension sends `register_command` frames.
   First-come-first-served: a name already taken by a built-in or by
   a previously-loaded extension is silently shadowed (logged in the
   extension's own log file).
5. **Runtime**: terva dispatches `command_invoked` frames when the
   user runs a registered command; the extension responds with
   `command_response`. Extensions can also push `notify` frames at
   any time. Panel-capable extensions may open an interactive panel,
   receive key events, and push redraws while the panel is focused.
6. **Shutdown**: when terva exits, it sends `shutdown` and waits up to
   2s for the extension to send `shutdown_ack`. Holdouts are
   SIGTERM'd, then SIGKILL'd.

In the long-lived hosts (interactive TUI, `terva web`, `terva rpc`) steps
1–4 run on a background goroutine while the session comes up, and the
first turn waits for them to finish — so extension tools are never
missing from a turn the model actually runs, and startup no longer
scales with the slowest extension's boot time. The single-shot modes run
them before the first turn begins, which for them amounts to the same
ordering.

A crashing extension does not bring down terva. The slash command it
owned simply stops working until the extension is fixed and terva is
restarted.

## Context contributions

An extension can contribute to what the **model** sees, under host
control (see `docs/plans/archive/extension-context-cards.md`): static
guidance folded into the system prompt (`register_context`), live
per-turn cards (`context_card`), and a status-line segment
(`status_segment`). Run `/context` and switch to the **Extensions** tab
to see exactly what's injected; the **Overview** tab shows a size
breakdown of the whole context window (system prompt, tools, extension
context, and the transcript per message) so you can trace a bloated
context to its source.

The static block is normally set once during registration. An extension
that needs to **swap it mid-session** — a memory store loading this
project's notes on `session_start`, say — sends `refresh_context`
(protocol 3, declare `RequireProtocol(3)`): the host replaces the block
and rebuilds the cached system prompt so it takes effect on the next
turn. It stays a *snapshot* — it changes only when the extension sends
the frame, not every turn — so the prompt cache survives. The per-block
budget is a few KB; the host trims anything larger.

Installing an extension is consent to run it, but you can opt one out of
injecting into the model's context — per user **or** per project — with
`disable_context_extensions` in `config.json`:

```json
{"disable_context_extensions": ["noisy-ext"]}
```

A project's `.terva/config.json` may add to this list but never remove
from it (restrict-only union with the user layer), so a directory can
run terva with a stricter context posture. The disabled extension's
tools, commands, and panels keep working — only model-context injection
is suppressed.

## Responsible use: context & tools

Two of the things an extension can change — its **static context block**
(`register_context` / `refresh_context`) and the **set of tools it exposes**
(`register_tool` / `set_withdrawn_tools`) — live in the model request's
**cached prompt prefix**. Providers cache that prefix and bill/process only
the delta on the next turn; changing it invalidates the cache for everything
after it. In a long session that is potentially hundreds of thousands of
tokens re-sent and re-billed. So the prefix is a **snapshot you replace at a
boundary**, not a per-turn signal. (For genuinely per-turn information, use a
context **card** — the cache-free tail — not the static block.)

**The rule:** decide once per session and assert it from `OnSession`, never
per turn and never from a tool or event handler. The host helps two ways so a
slip is not catastrophic:

- **Per-turn pinning.** The host freezes the system prompt and tool set for the
  whole of a turn. A change that lands mid-turn — even mid agentic loop — can
  neither evict that turn's cache nor pull a tool the model is mid-way through
  using; it takes effect on the *next* turn. So a mistimed change is safe, just
  wasteful.
- **No-op on unchanged.** Re-asserting the *same* context or withdrawn set
  costs nothing — the host diffs and does nothing. Re-declaring your decision on
  every `session_start` (including the re-fire a `/cd` produces) is therefore
  free; you don't need to remember whether you already said it.

**Use the boundary-scoped `Session` handle.** The value passed to your
`OnSession` / `OnSessionEnd` handler carries `RefreshContext`, `WithdrawTools`,
`WithdrawAllTools`, `RestoreTools`, `RestoreAllTools`, and `ProtocolVersion()`.
Calling them there is cache-safe *by construction* — the call site is a
boundary — so they never warn. The identically-named `*Extension` methods are
an advanced escape hatch: they work from anywhere but log an advisory note to
the ext log when used off a boundary (and a `Session` you stash and reuse later
trips the same warning, since its boundary has closed).

```go
e.OnSession(func(s ext.Session) {
    // Decide ONCE per session, from the boundary. Re-asserting the same
    // result on the next session_start / a /cd is a free no-op.
    if usable(s.CWD) {
        s.RefreshContext(standingNotes)        // swap the context block
        if s.ProtocolVersion() >= 4 {
            s.RestoreAllTools()                 // tools are meaningful here
        }
    } else {
        s.RefreshContext("")                    // clear the block
        if s.ProtocolVersion() >= 4 {
            s.WithdrawAllTools()                // hide tools that can't work here
        }
    }
})
```

**Withdraw tools that can't do their job here.** If your tools are useless in
the current workspace — a git extension outside a repo, a cloud tool with no
credentials, a DB tool with no connection — withdraw them with
`WithdrawAllTools` (or `WithdrawTools(names…)` for a subset) so they stop
spending tokens in the model's tool schema and stop tempting the model into
calls that can only refuse. Restore them when they become useful again. You can
only hide your **own** registered tools; names that aren't yours are ignored,
so you can never hide a built-in or another extension's tool. This needs host
protocol 4 — feature-detect with `s.ProtocolVersion() >= 4` (or
`Host().ProtocolVersion`); an older host ignores the frame and your tools simply
stay visible (the pre-4 behavior), so there's no `RequireProtocol(4)` and old
hosts keep loading you.

## Being a good extension citizen

Beyond the cached prefix, an extension shares the model's context window, the
user's trust, and the host's event loop. A few habits keep it a good neighbor:

- **Declare your effect honestly.** Set `read_only` / `authority` truthfully on
  `register_tool` — the host uses them to decide what auto-allows vs. prompts vs.
  is refused in `plan` mode. A side-effecting or network tool mislabeled
  `read_only` bypasses your own user's policy; **lying here only cheats your
  user**. Mark network tools `network-read`, not `read_only`. See the
  [authority classification](standard-tools.md#authority-classification) for the
  taxonomy and `ext.ReadOnly()` / `ext.WithAuthority(...)` in the Go SDK.
- **Keep your model-facing footprint small.** Every registered tool's schema and
  description, and every context block, occupies the cached prefix and adds noise
  to the model's choices. Register only the tools the model actually needs, write
  tight schemas and one-line descriptions, and keep context blocks to a few KB
  (the host trims larger). Trace a bloated window with `/context` — the
  **Extensions** tab shows what you inject, the **Overview** tab attributes size
  across system prompt, tools, and transcript. Under [lazy tool
  visibility](standard-tools.md#lazy-tool-visibility-lazy_tools) your tools defer
  behind `activate_tools` and cost nothing until the model asks for them — so if
  your static guidance names a tool the model must use *before* others ("search
  the index before reading"), mark just that one `ext.Essential()` (the
  `"essential"` field on `register_tool`) to keep it advertised; leave the rest
  deferred. The host caps essential tools per extension (3), so this stays a
  scalpel, not a way to opt out of lazy mode.
- **Feature-detect, and degrade gracefully.** Gate a protocol-N feature on
  `Host().ProtocolVersion`. Declare `RequireProtocol(n)` *only* when your
  extension genuinely can't function without it — it makes an older host refuse
  to load you. Additive frames degrade silently on old hosts; design so the
  extension still does something useful there.
- **Log to stderr, never stdout.** stdout is the JSON wire — a stray `print` /
  `fmt.Println` corrupts the protocol and can desync the host. Use the SDK's
  `Logf` (captured to `$TERVA_HOME/logs/ext-<name>.log`) or write to stderr
  directly.
- **Don't wedge the event loop.** Event delivery is best-effort and buffered: a
  handler that blocks backs the queue up until the host **drops your events**
  (with a log) rather than freezing terva. Keep tool/command/event handlers
  responsive; push slow or long-running work onto your own goroutine.
- **Remember you run with the user's full permissions.** Extensions have the
  user's filesystem and network access (see [Security](#security)); the host's
  permission gate is a backstop, not a license. Be conservative with side
  effects and surface what you're doing.

### Tool-call ordering (`ext.Sequential()`)

A model can emit several tool calls in a single turn, and the SDK runs each
`tool_call` on its **own goroutine** — so two calls that arrive together race.
For independent tools that is exactly what you want (they finish as fast as the
slowest, not the sum). But for **stateful tools with ordering constraints** — a
"raise shields" that must complete before a "fire", a "begin transaction" before
the writes inside it — a race can apply them out of order or interleave them.

Mark such tools `Sequential()`:

```go
e.Tool("raise_shields", "...", schema, raiseHandler, ext.Sequential())
e.Tool("fire",          "...", schema, fireHandler,  ext.Sequential())
```

Every tool marked `Sequential()` shares **one first-in-first-out lane**, so order
is preserved *across* them: the calls execute one at a time, in the order they
arrived. Tools that aren't marked stay fully concurrent and never wait on the
lane.

This does **not** reorder anything — the model is still responsible for issuing
the calls in a sensible order. `Sequential()` only guarantees the SDK *preserves*
that order for the tools where it matters, instead of dropping it on the floor by
racing goroutines. Mark the handful of order-sensitive effectors; leave
read-only and independent tools concurrent. A guarding mutex protects your state
either way — `Sequential()` is about *logical* ordering, not data races.

## Wire format

All frames are one JSON object per line. Top-level `type` is the
discriminator. Optional `id` correlates request frames with their
responses. The schema is pinned by golden tests in
`packages/agent/extproto`; breaking it means bumping
`extproto.ProtocolVersion`, never a rename sweep — third-party
extensions are deployed independently of terva and cannot be recompiled
in lockstep (see [fork.md](fork.md)).

**The golden corpus is published** at
`packages/agent/extproto/testdata/golden.jsonl` — one
`{"name":…,"dir":…,"frame":…}` object per line, where `frame` is the
exact bytes terva emits, carried as a JSON string so it survives the
envelope intact. Point your conformance suite at that file rather than
copying frames by hand: a copy drifts silently, and a fetch cannot. It
covers every frame type in the protocol, and a test refuses to let it
fall behind — a frame added to `extproto.go` without a corpus entry
fails on the commit that adds it.

`dir` is `ext_to_host`, `host_to_ext`, or `both`. An extension only ever
*encodes* `ext_to_host` frames, so the useful split is to assert those
byte-exact against the corpus and the rest on decode.

Two things to know before byte-comparing:

- terva encodes with Go's `encoding/json`, which escapes `<`, `>` and
  `&` where most encoders emit them literally. Both are valid JSON and
  every reader here accepts either, so this never matters on the live
  wire — real frames carry those characters constantly — but the corpus
  is deliberately kept free of them so byte-exact comparison stays
  meaningful across languages.
- Map-valued fields (`config` on `hello_ack` and on a `config_update`
  event) are emitted with their **keys sorted**, because Go marshals maps
  that way. A language whose map preserves insertion order has to sort
  to reproduce these bytes.

The corpus also pins the shapes that are easy to guess wrong. Several
array fields carry no `omitempty`, and Go marshals a nil slice as
`null`, not `[]` — `secret_keys` for an extension holding no secrets is
`{"keys":null}`, and a not-found `session_data` is `{"messages":null}`.
Model those as nullable.

### Frame size limits

There is a per-frame maximum of **4 MiB** (`extproto.MaxFrameBytes`) in
both directions. Oversized frames are handled gracefully, never fatally:

- A frame larger than the cap on the read side (either direction) is
  **skipped and logged**, and reading continues — one oversized frame
  never takes the extension or the host's reader down.
- The host caps the args it puts in a single `tool_call` frame at
  **1 MiB** (`extproto.MaxToolCallBytes`, comfortably below the read
  cap). If the model produces a larger tool argument, the call comes
  back to the model as a normal `is_error` tool result ("arguments are
  N bytes; the limit is …") instead of being sent — so an oversized
  argument can't kill an extension. Keep individual tool results and
  context contributions well under these limits.

### Extension → host

#### `hello` (required, first frame)

```json
{"type":"hello","name":"weather","version":"1.0.0",
 "capabilities":["commands","tools","panels"]}
```

The optional `"min_protocol": 3` field is the lowest host `protocol_version`
this extension can run against — the wire behind `RequireProtocol(n)`. Zero
(the default, and what every pre-negotiation extension sends) means "no
minimum", so old extensions and old hosts interoperate unchanged. When set, a
host below it refuses to load the extension with a clear message instead of
letting it misbehave against a wire it doesn't fully speak. Declare it only
when your extension genuinely can't function without that protocol level;
otherwise feature-detect on `Host().ProtocolVersion` and degrade.

#### `bootstrap` (optional, before `hello`)

```json
{"type":"bootstrap","message":"compiling extension"}
```

Sent by a **launcher**, zero or more times, before `hello`, to say "still
working". Each frame restarts the hello deadline, so the host measures
*silence* rather than total elapsed time, and shows your message so a long
build reads as progress instead of a hang. An absolute ceiling (10 minutes)
still applies.

This is not an SDK feature and cannot be: the SDK isn't running yet, which is
exactly the difficulty. A launcher that builds before it can `exec` the real
extension emits it with one `printf`:

```sh
#!/bin/sh
if [ ! -x ./weather ] || [ weather.go -nt ./weather ]; then
  printf '%s\n' '{"type":"bootstrap","message":"building weather"}'
  go build -o weather . || exit 1
fi
exec ./weather
```

Emit one before the build starts, and another every so often if the build is
long enough that the host could time out between reports. Purely additive: a
host that doesn't know the frame sees a malformed `hello` and skips the
extension — which is what it did with a slow build anyway.

#### `register_command`

```json
{"type":"register_command","name":"weather",
 "description":"current weather for a city"}
```

#### `register_tool`

Registers a tool the LLM can call. `schema` is a JSON Schema object
describing the tool's args (the same shape Anthropic and OpenAI accept).

```json
{"type":"register_tool","name":"weather",
 "description":"Get the current weather for a city.",
 "schema":{
   "type":"object",
   "properties":{"city":{"type":"string"}},
   "required":["city"]
 },
 "authority":"network-read"}
```

The optional `"read_only": true` field declares the tool side-effect
free (the MCP `readOnlyHint` analog). Annotated tools are admitted in
the `plan` approval mode and auto-allowed in `auto-edit` (see
[permissions.md](permissions.md)); unannotated tools are treated as
mutating. Lying here only cheats your own user's policy. Old hosts
ignore the field; old extensions never send it — fully additive.

The optional `"authority"` field is its **richer successor**: the tool's effect
class — `local-read`, `local-data`, `workspace-mutation`, `process-execution`,
`network-read`, `external-mutation` (see the
[authority classification](standard-tools.md#authority-classification)). It says
what `read_only` cannot: a `network-read` tool reads nothing locally yet must
not be auto-allowed as read-only. Also additive/optional — empty means the host
falls back to the `read_only` bool, and an unknown value is treated as
side-effecting (the safe default). From the Go SDK: `ext.WithAuthority(...)`,
the counterpart to `ext.ReadOnly()`.

The optional `"essential": true` field marks a **load-bearing** tool that must
stay advertised to the model every turn, even when the host runs with [lazy tool
visibility](standard-tools.md#lazy-tool-visibility-lazy_tools) and would otherwise
defer your tools behind an `activate_tools` call. Use it for the tool your static guidance
([`register_context`](#register_context-protocol-2)) tells the model to reach for
before others — "search the index before reading a file" only works if the search
tool is in the tool list when the model first wants to read, not sitting deferred
while the prompt that names it is already in context. Only the tools you mark stay
eager; the rest of your tools still lazy-load, so a big extension keeps the context
savings for the tools the model rarely needs. The host **caps** how many tools one
extension may mark essential (currently 3) — excess ones load deferred and the drop
is logged — so an extension can't quietly pin its whole surface always-visible and
defeat lazy mode. Additive/optional: an old host ignores it (the tool lazy-loads as
before) and a host with lazy mode off advertises everything anyway, so it is a
no-op there. `essential` is visibility only — the tool is still permission-gated
exactly as before when actually called. From the Go SDK: `ext.Essential()`.

#### `set_withdrawn_tools` (protocol 4)

Hides (and later restores) tools **this extension registered** from the
model for the session — the tool analog of `refresh_context`. It is a
wholesale snapshot of the names to hide, so the same frame doubles as the
restore path and re-sending an unchanged set is a free host-side no-op.

```json
{"type":"set_withdrawn_tools","all":true}              // hide all my tools
{"type":"set_withdrawn_tools","tools":["gitlog"]}      // hide just these
{"type":"set_withdrawn_tools","tools":[],"all":false}  // restore everything
```

`all:true` hides every tool the extension registered (`tools` ignored);
otherwise the listed tools are hidden and any name that isn't one of this
extension's own is ignored — you can't hide a built-in or another
extension's tool. A withdrawn tool leaves both the callable registry and the
system-prompt tool list but stays registered, so restoring needs no
re-registration (and the `/extensions` dialog still shows it in the count).
Send this only at a session boundary (see
[Responsible use](#responsible-use-context--tools)): the host pins the tool
set per turn, so a mid-turn change applies on the next turn. Additive — a
protocol-3 host ignores the frame and the tools stay visible — so
feature-detect with `Host().ProtocolVersion >= 4` rather than declaring
`RequireProtocol(4)`.

#### `register_context` (protocol 2)

Declares the extension's **static** context block: guidance the host wraps,
bounds, attributes, and folds into the cached system-prompt addendum. Sent
during the register phase, like `register_command` / `register_tool`. See
[Context contributions](#context-contributions).

```json
{"type":"register_context","text":"House style: prefer table-driven tests."}
```

#### `refresh_context` (protocol 3)

`register_context` you can send **mid-session**: the host swaps the block,
rebuilds the cached system prompt, and the change lands on the next turn. The
block stays a *snapshot* — it changes only when you send this frame, never per
turn — so re-snapshot at a boundary (`session_start`, `transcript_compacted`)
rather than churning the prompt cache. Empty `text` clears the block. Same host
wrapping, attribution, and byte bound as `register_context`. Declare
`RequireProtocol(3)`.

```json
{"type":"refresh_context","text":"Project notes for acme-api: …"}
```

#### `context_card` / `context_card_clear` (protocol 2)

A **dynamic** block the host injects at the cache-free tail each turn and never
persists — the channel for live state (a task list, a build status) that would
otherwise invalidate the cached prefix. Set or replace by `id`; `label` is a
short header for the host's wrapper, `priority` orders multiple cards (lower
first), and `blocking` marks open work, which makes the host append a soft
"review before closing" nudge to the card's injection.

```json
{"type":"context_card","id":"todos","label":"Open work",
 "text":"□ ship panel api\n✓ persist state","priority":10,"blocking":true}
{"type":"context_card_clear","id":"todos"}
```

#### `status_segment` (protocol 2)

Sets (or replaces, by `id`) a short segment in the host's status line. Host-
rendered and **not** model-facing — the adjacent channel to the context ones,
for the things a user should see but the model shouldn't pay for. An empty
`text` clears the segment.

```json
{"type":"status_segment","id":"weather","text":"Berlin 16°C"}
```

#### `host_tool_call` (protocol 3)

The reverse of `tool_call`: an extension asks the host to run one of the
**host's own** tools (read, grep, bash, an MCP tool…) and sends back a
`host_tool_result` correlated by the extension's `id`. It exists so an
extension can orchestrate host tools without a model round-trip — e.g. a
code-execution extension whose sandboxed script calls `read`/`grep`/`bash`
as functions, collapsing a multi-step pipeline into one turn.

```json
{"type":"host_tool_call","id":"c1","name":"read","args":{"path":"README.md"},"silent":true}
// → {"type":"host_tool_result","id":"c1","content":[{"type":"text","text":"…"}]}
```

The host runs the tool under the **same permission gate** a model call
uses — an extension gains reach, never authority — and refuses
extension-owned tools, so a `host_tool_call` cannot recurse back into an
extension (only built-in and MCP tools are reachable). `silent` is a hint
not to surface the call in the UI. Declare `RequireProtocol(3)`; a host
that doesn't support it answers with an error result.

#### `list_sessions` / `read_session` (protocol 3)

Read-only, project-scoped access to past session transcripts, so an
extension can index prior conversations. `list_sessions` returns the
active project's sessions; `read_session` returns one transcript
flattened to role+text.

> **Cross-session search now ships in core as the `session_search` tool,
> and a search extension built on this bridge is superseded by it.**
>
> The flattening is why. Role+text drops tool calls, their arguments, and
> their results — on a measured coding session that is **2% of the
> searchable bytes**, and the 98% dropped is where file paths, commands,
> and command output live. Searching one real project for a filename
> found **24 matches at full fidelity against 1** through this bridge,
> and that one was incidental. The bridge also cannot see swarm
> sub-agents at all: their transcripts live under the swarm state root,
> which `list_sessions` never enumerates.
>
> The bridge is unchanged and still supported — it remains the right
> surface for an extension that wants conversation *text* (topic
> clustering, summarisation, export). It is the wrong surface for
> recall, which is why that moved in-tree.
>
> The `session-search` extension is retired accordingly — it is in
> `supersededExtensions`, so an installed copy is **skipped at load**
> with a pointer to `terva ext remove session-search`, the same way
> `git-worktree` and `memory` were retired. Nothing is lost by not
> loading it: its state was an FTS index *derived* from the
> transcripts, and core keeps no index, so there is no adoption step —
> the transcripts were always the source of truth.
>
> Superseding keys on the EXTENSION name, not the tool name. A
> third-party extension under a different name may still register a
> tool called `session_search`; built-ins win that collision, so core's
> stays live.

```json
{"type":"list_sessions","id":"l1"}
// → {"type":"session_list","id":"l1","sessions":[{"session_id":"…","title":"…","messages":12,"mtime":…}]}
{"type":"read_session","id":"r1","session_id":"…"}
// → {"type":"session_data","id":"r1","messages":[{"role":"user","text":"…"},…]}
```

Cross-project reads are not granted here (a non-matching `project_id`
returns nothing), and a `session_id` that tries to escape the project's
session directory is refused. Declare `RequireProtocol(3)`; an
unsupported host returns an empty list / `not_found`.

From the Go SDK, pass `ext.ReadOnly()` as a trailing option to declare
it — `ext.WithAuthority(...)` and `ext.Essential()` are trailing options too,
so a load-bearing read-only tool combines them:

```go
e.Tool("branch_list", "List branches.", schema, handler, ext.ReadOnly())

// A load-bearing search tool: stays advertised under lazy tool visibility so
// the guidance that tells the model to use it first isn't pointing at a
// deferred tool. See "Keep your model-facing footprint small" below.
e.Tool("index_search", "Search the workspace index.", schema, handler,
    ext.WithAuthority(ext.AuthorityLocalRead), ext.Essential())
```

Tool names live in the same namespace as built-in tools (`read`,
`write`, `edit`, `bash`, `skill`, the `worktree_*` five). Conflicts are
silently shadowed by the built-in.

#### `ready`

Sentinel telling terva "all initial registrations are flushed". Send it
right after your last `register_*` frame so the host can build the
agent's tool registry without racing the registration window.

```json
{"type":"ready"}
```

#### `tool_result`

Reply to a `tool_call` from the host. `content[]` is a list of
message blocks; each block is `{"type":"text","text":"..."}` or
`{"type":"image","mime_type":"image/png","data":"<base64>"}`. Set
`is_error: true` to mark the call as failed.

```json
{"type":"tool_result","id":"...",
 "content":[{"type":"text","text":"Berlin: 16°C, fog"}]}
```

#### `subscribe`

Declares which lifecycle events the extension wants to observe and
which it wants to intercept. Send once after `hello`, before `ready`.

```json
{"type":"subscribe",
 "events":["session_start","session_end","turn_start","tool_call","turn_end","user_message","assistant_message"],
 "intercept":["tool_call","turn_start","user_message","assistant_message"]}
```

Recognised event names: `session_start`, `session_end`, `turn_start`,
`turn_end`, `run_end`, `tool_call`, `tool_result`, `user_message`,
`assistant_message`, `workspace_changed`, `compact_start`,
`transcript_compacted`, `config_update`. (The host advertises the exact set it
emits in `hello_ack.supported_events`; subscribing to a name an older host
doesn't emit is harmless — it simply never fires.)

`run_end` fires once when the agent finishes a whole prompt — every step,
tool loop, and the at-close gate done. It's the per-prompt bookend to
`user_message`, distinct from the per-*step* `turn_end` (which fires
repeatedly inside a tool loop). Use it to act when the agent goes idle:
summarize the exchange, run a post-turn check, or flush state. The Go SDK
exposes it as `OnRunEnd`.

`compact_start` fires when the host is *about to* compact the transcript —
the pre-event paired with `transcript_compacted` (post). The `text` field
carries a short human-readable reason. Because compaction runs a slow LLM
summarization, a handler has time to read the full session (`read_session`)
and harvest detail before it's summarized away — the window the post-event
misses. The Go SDK exposes it as `OnCompactStart`.

`user_message` fires for every genuine user prompt — the initial submit
and any queued follow-ups — the symmetric counterpart to
`assistant_message`. Use it to harvest intent for a memory store or feed
a session index. The host's synthetic at-close gate nudge is **not**
delivered (it's a host re-prompt, not the user's words). The Go SDK
exposes it as `OnUserMessage`.

`workspace_changed` fires once at the end of each agent run with the net
set of files the turn touched, in a `files` array of
`{"path":"...","change":"added|modified|deleted"}` (workspace-relative,
slash-separated paths, sorted). A run that changed nothing fires no event.
The host derives it by diffing the workspace at run boundaries — honoring
`.gitignore` and pruning `.git` — so it catches `bash` side effects and
external edits, not just the agent's own write/edit tools. Scoped to the
workspace root only; oversized trees disable it (it reports nothing rather
than walk an unbounded tree each turn). Use it to keep a code index fresh
or note edits in a memory store. Additive/opt-in; the Go SDK exposes it as
`OnWorkspaceChanged`, and the change list also rides the generic `Event`
as `Files`.

`transcript_compacted` fires after the host compacts the conversation
(auto, near the context limit, or via `/compact`), before the next model
turn. It's the moment to re-snapshot a frozen context block: compaction
summarizes away the tool-results that recorded mid-session writes, so a
memory extension re-injects its notes here via `refresh_context` — the
same thing it does on `session_start`. It's a fire-and-forget signal,
purely additive and opt-in: subscribe to receive it, and a host too old
to emit it simply never fires it (your extension keeps its
session-boundary refresh). The Go SDK exposes it as `OnCompaction`.

`session_start` (protocol 2+) carries the active session's identity —
`session_id`, `session_path`, `session_title` — plus `cwd` and
`project_id`. Unlike the `cwd` in the hello handshake (frozen at launch),
these refresh on **every** `session_start`, including after a `/cd`, so
an extension follows the working directory instead of going stale.
`project_id` is the host's stable, collision-proof key for the cwd (a
readable, flattened path plus a short hash); use it to scope per-project
state without reinventing the keying. The SDK refreshes `Host().CWD` /
`Host().ProjectID` from these before any handler runs, and an `OnSession`
handler receives them on the `Session`. A no-session start (session
closed / `--no-session`) leaves `cwd`/`project_id` empty and the SDK
keeps the last known value (closing a session doesn't move the cwd).

`session_end` is the bookend to `session_start`, carrying the same
identity fields for the session that is ending. It fires for the
*outgoing* session just before a switch or close announces the next one,
and once more for the active session at host shutdown (the session_end is
queued ahead of the shutdown frame on the same FIFO outbox, so a healthy
extension sees it before exiting). Use it to flush a memory store or index
the just-finished session. It is **best-effort**: a hard kill (SIGKILL)
skips it, so persist incrementally and treat it as a flush point, not a
durability guarantee. Additive/opt-in; the Go SDK exposes it as
`OnSessionEnd`.

`config_update` fires when the user changes **this** extension's config (the
`/extensions` config dialog). The new resolved values ride the event's `config`
field, the same shape `hello_ack` carries — so an extension re-reads its
settings live instead of asking the user to restart terva. Fire-and-forget and
gracefully degrading (an older host never emits it, and the initial values still
arrive in `hello_ack` regardless). The Go SDK exposes it as `OnConfig`.

Interceptable events:

- `tool_call`: block the call (model sees `reason` as the tool
  error) or rewrite args via `modified_args`.
- `turn_start`: block the turn before the model is called. Useful
  for rate-limiting and business-hour gates. `reason` is shown to
  the user as a status line. No rewrite supported.
- `user_message`: block a prompt via `block` (it's neither recorded
  nor sent; `reason` is shown to the user), or rewrite the prompt the
  model sees via `replace_text` (the rewrite IS what lands in the
  transcript). Runs on the initial prompt and on queued follow-ups,
  so a guard can't be bypassed by typing while the agent is busy.
  Useful for input guardrails, secret redaction, and prompt
  augmentation. The Go SDK exposes it as `InterceptUserMessage`.
- `assistant_message`: suppress the message via `block`, or rewrite
  the user-visible text via `replace_text`. The model's original
  text stays in the transcript so the model sees what it actually
  said on subsequent turns.

#### `event_intercept_response`

Reply to an `event_intercept` from the host. All fields default to
"allow, pass through unmodified".

| field | meaning |
|---|---|
| `block` | `true` refuses the action. For `tool_call`, `reason` is shown to the model; for `turn_start` / `user_message` / `assistant_message`, `reason` is shown to the user. |
| `reason` | refusal text (on block) or pass-through note. |
| `modified_args` | for `tool_call`: rewritten JSON args the tool will actually see. Must be a valid JSON object. Ignored when `block` is true. |
| `replace_text` | for `user_message`: replaces the prompt the model receives (the rewrite also lands in the transcript). For `assistant_message`: replaces the user-visible text while the model's original output stays in the transcript. Ignored when `block` is true. |

Missing the response within 5s is treated as "allow" (i.e. an
unresponsive extension never stalls the agent). When multiple
extensions subscribe to the same event, they're consulted serially;
the first `block` wins and rewrites (args / text) chain: each
subsequent interceptor sees the previous one's output.

```json
{"type":"event_intercept_response","id":"...",
 "block":true,"reason":"refused: matches danger pattern \"rm -rf\""}

{"type":"event_intercept_response","id":"...",
 "modified_args":{"command":"echo GUARDED: ls"}}

{"type":"event_intercept_response","id":"...",
 "replace_text":"[redacted]"}
```

#### `command_response` (reply to `command_invoked`)

```json
{"type":"command_response","id":"...","action":"prompt",
 "prompt":"Show today's weather for Berlin in one line."}
```

`action` is one of:

- `"prompt"` — submits `prompt` as a fresh user message; the agent
  runs a turn against it.
- `"insert"` — inserts `insert` into the editor at the cursor without
  submitting.
- `"display"` — appends `display` to the chat as a one-shot styled
  note. No model call, nothing written to the transcript.
- `"open_panel"` — opens an extension-owned interactive panel inside
  terva. The panel content lives in `open_panel`.
- `"noop"` — the extension handled it itself (e.g. it pushed
  `notify` frames or kicked off background work). terva doesn't change
  the UI in response.

Example:

```json
{"type":"command_response","id":"...","action":"open_panel",
 "open_panel":{
   "id":"todos-main",
   "title":"Todos",
   "lines":["□ ship panel api","✓ persist state"],
   "footer":"↑/↓ navigate - a add - x complete - esc close"
 }}
```

If `error` is non-empty, terva renders it as a red status line
regardless of `action`.

#### `open_panel` (one-way, any time)

Opens an interactive panel **without** a command invocation to hang it off —
the spontaneous twin of the `open_panel` action inside `command_response`. Send
it from a tool handler, an event handler, or any background goroutine. The
payload is the same panel spec.

```json
{"type":"open_panel","panel":{
   "id":"todos-main",
   "title":"Todos",
   "lines":["□ ship panel api","✓ persist state"],
   "footer":"↑/↓ navigate - a add - x complete - esc close"}}
```

#### `panel_render` (one-way, while a panel is open)

Pushes a fresh frame for an already-open panel.

```json
{"type":"panel_render","panel_id":"todos-main",
 "title":"Todos",
 "lines":["□ ship panel api","✓ persist state"],
 "footer":"↑/↓ navigate - a add - x complete - esc close"}
```

#### `panel_close`

Closes a previously-open panel.

```json
{"type":"panel_close","panel_id":"todos-main"}
```

#### `notify` (one-way, any time)

```json
{"type":"notify","level":"info",
 "message":"refreshed cache (12 entries)"}
```

`level` is one of `info`, `success`, `warn`, `error`. The note shows
up below the transcript with the extension's name in brackets. Notes
are one-shot: they clear automatically when the user sends their next
prompt (and on `esc` / `/clear`).

#### `clear_notes` (one-way, any time)

Removes every note this extension previously pushed via `notify` /
`display`. Use it for transient status lines (e.g. an approval prompt)
so they do not stack up; notes from other extensions are untouched.

```json
{"type":"clear_notes"}
```

Under `terva rpc`, this surfaces to the host as an `ext_clear_notes`
event (alongside `ext_notify` / `ext_display`).

#### `submit` (one-way, any time)

Queues a plain model prompt in the interactive host, as if the user had typed
and sent it. The host routes it through its submit-or-queue path, so it is safe
to send while a turn is running — it lands at the next boundary rather than
racing the active turn. Empty/whitespace `text` is ignored.

```json
{"type":"submit","text":"summarize what changed in the last turn"}
```

Interactive-mode only (the RPC loop has no editor and takes its prompts from
the client, so it ignores this). The Go SDK has no helper for it yet — send the
frame directly, or use a `command_response` with `action:"prompt"` when the
prompt is the answer to a slash command.

#### `submit_slash` (one-way, any time)

Submits a slash command to the host's TUI as if the user had typed it.
Typically emitted from a `panel_key` handler — e.g. Enter on a selected
row to switch the host with `/cd <path>`. `text` must start with `/`.

```json
{"type":"submit_slash","text":"/cd /repo/.worktrees/feature-x"}
```

Interactive-mode only: the host ignores it in `-p` / `--json` / `rpc`
(no TUI to submit into). Reserved for opt-in extensions that the user
has installed and trusts — it lets an extension drive any host command,
so it is not something a casual extension should reach for. From the Go
SDK this is `e.SubmitSlash("/cd " + path)`.

#### `register_connector` (protocol 5, experimental)

Declares the [connector role](#connector-role-experimental) during the
register phase. No payload — capabilities travel in the inner hello at
session-open time. Refused (logged, ignored) unless the manifest
declares `"connector": true`.

```json
{"type":"register_connector"}
```

#### `chat` / `chat_down` (protocol 5, experimental)

The extension side of the connector tunnel. `chat` carries one frame of
the CONNECTOR protocol verbatim (see the frame reference in
[connectors.md](connectors.md) — this wire never mirrors that
vocabulary); `chat_down` ends the session from the extension side, with
`error` set when the connector engine died (the process and its tools
live on) or without for an orderly teardown. `id` on both is the
session id from the host's `chat_open`.

```json
{"type":"chat","id":"s1","frame":{"type":"message","chat_id":"c1","user_id":"u1","text":"hi"}}
{"type":"chat_down","id":"s1","error":"auth revoked"}
```

#### `shutdown_ack`

Sent in response to `shutdown`. Extension should exit promptly after.

### Host → extension

#### `hello_ack`

```json
{"type":"hello_ack","protocol_version":2,
 "zot_version":"0.0.7","terva_version":"0.0.7","provider":"anthropic",
 "model":"claude-opus-4-7","cwd":"/Users/pat/Developer/terva",
 "extension_dir":"/Users/pat/Developer/terva/.terva/extensions/todos",
 "data_dir":"/Users/pat/.terva/ext-data/todos",
 "supported_events":["session_start","turn_start","turn_end","tool_call",
   "tool_result","assistant_message","transcript_compacted"]}
```

Sent immediately after `hello`. The extension can use these fields to
decide which commands to register (e.g. only register a Python tool
on macOS, only register a model-specific shortcut for opus, etc.).

`zot_version` and `terva_version` carry the same host version string, and the <!-- rename:keep -->
old key is **always** on the wire — a frozen wire field kept for compatibility
with extensions written against the pre-fork protocol, and pinned by golden
tests (see [fork.md](fork.md)). Read `terva_version` and fall back to
`zot_version`. <!-- rename:keep -->

`supported_events` lists the lifecycle events this host can emit — a
finer-grained capability signal than `protocol_version`. Use it to adapt
or warn (the Go SDK exposes `Host().Emits("transcript_compacted")`); it's
**absent on an older host** that doesn't advertise, which you should read
as "unknown" and handle by subscribing optimistically and degrading if
the event never fires, rather than gating on it.

`extension_dir` is the **read-only install dir** — the extension's code
and any defaults/assets it ships. `data_dir` is the **writable state
dir**, `$TERVA_HOME/ext-data/<name>`, kept separate so a read-only or
system install still works and code never mixes with data. Persist your
state (e.g. `todos.json`, caches, scoped auth tokens) under `data_dir`.

> **Note:** `data_dir` used to alias the install dir. It now points at
> the separate `ext-data` location. The Go SDK's `Host().DataFS()` layers
> `data_dir` over `extension_dir` (read-through, copy-on-write), so a file
> written under the old location is still read until it's next written —
> a no-flag-day migration. Use `DataFS` for both "ship a default, let the
> user override" and reading legacy state. For per-project state, use
> `Host().ProjectDataDir()` (`data_dir/projects/<project_id>`, scoped by
> the `project_id` on `session_start`).

#### `command_invoked`

```json
{"type":"command_invoked","id":"...",
 "name":"weather","args":"berlin"}
```

`args` is everything the user typed after the command name, trimmed.

#### `tool_call`

Sent when the LLM invokes a tool the extension registered. `args` is
the parsed JSON object the model produced; the extension is
responsible for validating/coercing it.

```json
{"type":"tool_call","id":"...","name":"weather",
 "args":{"city":"Berlin"}}
```

Reply with `tool_result` within the host's tool timeout (default 60s).
Missing the timeout surfaces an error to the model and the call is
marked as failed.

#### `event`

Lifecycle notification for events the extension subscribed to via
`subscribe`. One-way — no response expected.

```json
{"type":"event","event":"turn_start","step":1}
{"type":"event","event":"tool_call",
 "tool_id":"...","tool_name":"read","tool_args":{"path":"foo.go"}}
{"type":"event","event":"turn_end","stop":"end_turn"}
```

#### `event_intercept`

Sent when terva wants to give the extension a chance to block, modify,
or annotate a lifecycle event before it happens. Reply with
`event_intercept_response` within 5s; missing the deadline is
treated as "allow".

Payload fields depend on the event:

```json
// tool_call: includes the tool id, name, and parsed args
{"type":"event_intercept","id":"...","event":"tool_call",
 "tool_id":"...","tool_name":"bash",
 "tool_args":{"command":"rm -rf /tmp/foo"}}

// turn_start: includes the step number
{"type":"event_intercept","id":"...","event":"turn_start",
 "step":3}

// assistant_message: includes the assembled text
{"type":"event_intercept","id":"...","event":"assistant_message",
 "text":"here is your api key: sk-ant-..."}
```

#### `panel_key`

Sent while an extension-owned panel is focused. `key` is a normalized
name (`up`, `down`, `left`, `right`, `enter`, `esc`, `tab`, `pageup`,
`pagedown`, `home`, `end`, `backspace`, `delete`, `rune`). For
`key:"rune"`, `text` carries the typed character.

```json
{"type":"panel_key","panel_id":"todos-main","key":"down"}
{"type":"panel_key","panel_id":"todos-main","key":"rune","text":"x"}
```

#### `panel_resize`

Tells the extension the panel's drawing area changed, so a renderer can re-wrap
its `lines` to the new `width` / `height` (in cells).

```json
{"type":"panel_resize","panel_id":"todos-main","width":80,"height":24}
```

Honest caveat: the frame is part of the wire spec, but **no host currently emits
it** — panels are re-rendered on the extension's own `panel_render` cadence.
Handle it defensively if you like; don't wait for it.

#### `panel_close`

Sent when the user closes the focused panel from terva (for example with
Esc or Ctrl+C). The extension should treat this as the panel lifetime
ending and stop sending `panel_render` updates for that `panel_id`.

```json
{"type":"panel_close","panel_id":"todos-main"}
```

#### `chat_open` / `chat` / `chat_close` (protocol 5, experimental)

The host side of the connector tunnel, sent only after this extension
registered the [connector role](#connector-role-experimental) AND a
chat consumer selected it by name. `chat_open` starts a session (the
extension answers by starting its connector engine, whose first output
is the inner connproto `hello` wrapped in a `chat` frame); `chat`
carries one connector-protocol frame verbatim; `chat_close` ends the
session host-side — tear the engine down and confirm with `chat_down`;
the process keeps serving tools. Frames whose `id` isn't the live
session's are stale stragglers: drop them.

```json
{"type":"chat_open","id":"s1"}
{"type":"chat","id":"s1","frame":{"type":"connect"}}
{"type":"chat_close","id":"s1"}
```

#### `shutdown`

Sent during graceful terva exit (or `/reload-ext` once that lands).
Reply with `shutdown_ack` and then exit.

## Managing extensions from the CLI

```
terva ext list                    list installed extensions and their state
terva ext install <path|git-url>  copy / clone into $TERVA_HOME/extensions/
terva ext upgrade <name>...       fast-forward-pull an installed extension's git checkout
terva ext remove <name>           delete an extension directory
terva ext enable <name>           re-enable a disabled extension
terva ext disable <name>          disable without removing
terva ext logs <name> [-f]        cat / tail the extension's stderr
terva ext config <name> [verb]    show / get / set / unset its declared settings
terva ext doctor                  diagnose extension discovery and registration
```

### `terva ext config` — settings without a browser

```
terva ext config <name>                     show the declared settings and their values
terva ext config <name> get <key>           print one value (never a secret)
terva ext config <name> set <key>=<value>…  set one or more values
terva ext config <name> set <key> --stdin   set one value read from stdin
terva ext config <name> unset <key>…        clear back to the declared default
```

Values are typed and validated against the manifest's declared schema, exactly
as the browser form and the terminal dialog are: a `bool` takes true/false, an
`int` takes a whole number, a `select` takes one of its options, and anything
else is refused before it is written.

**Where the write goes matters.** A running terva owns `config.json` and pushes
config changes to the live extension, so the command prefers to be a client of
that instance — the same path the browser takes, applied live.

It finds one in this order: `--endpoint`, then `TERVA_ATTACH` in the
environment, then the record `terva web` publishes to
`$TERVA_HOME/listen.json` when it binds. That last one means the common case
needs no flags at all:

```bash
terva ext config jmap-mail set enable_sieve_tools=true
```

Discovery is safe here specifically because the record lives *inside* the home
under discussion — a daemon named there is serving this `$TERVA_HOME` by
construction. Nothing is probed: a daemon answering on some default port may be
serving a different home entirely, and the handshake does not say which home it
holds.

With nothing serving, it writes `config.json` directly and says so, along with
what that does not do — a terva already running keeps the old value until it
reloads. `--offline` forces that path; a *named* endpoint that does not answer
is an error rather than a silent fall back to the file.

**Secrets.** A `secret`-typed field is never accepted on the command line, where
it would be readable in `ps` and would land in shell history — pass it with
`--stdin` or `--from-file <path>`. `get` refuses to print one and `show` reports
only whether a value exists. Setting any other field leaves a stored secret
alone; `unset <key>` is how one is cleared.

**Vendored extensions.** A `--ext` load lives outside the install roots, so
resolving it by name fails. Point at it with `--dir <path>`, or reach the terva
that loaded it with `--endpoint` — that instance knows where it came from.

`terva ext doctor` reads every installed manifest, then actually spawns the
extensions and reports what each one registered (and why one failed to load) —
the first thing to run when an extension is installed but its command, tool, or
context block never shows up.

`terva ext install <path>` does a recursive copy; `<git-url>` does a
shallow clone. Both validate that the destination contains an
`extension.json` and roll back if not.

### Disabling extensions by config (per user or project)

`terva ext disable <name>` is a global toggle (it flips the manifest).
For policy that travels with a directory, `config.json` has two
restrict-only lists, by extension name:

```json
{
  "disable_extensions": ["web"],
  "disable_context_extensions": ["noisy-ext"]
}
```

(These lists govern *extensions* only. The task tools are core built-ins
now — a per-run `--tools` allowlist is the way to drop them; a
`disable_extensions: ["terva-tasks"]` entry only affects a leftover install
of the retired extension.)

- **`disable_extensions`** — the extension is **never loaded**: not
  spawned, no tools/commands/panels/context. The strong "I don't want
  this running here" switch.
- **`disable_context_extensions`** — the extension loads normally, but
  its **model-context** contributions (`register_context` / cards) are
  suppressed; tools, commands, panels, and status still work.

A project's `.terva/config.json` may **add** to either list but never
remove from it (restrict-only union with the user layer): a cloned repo
can keep an extension from running in its directory, but can never make
one run that the user didn't install, nor re-enable one the user
disabled. Both compose with the manifest toggle and with `--ext` — any
one of them disabling wins.

## Loading an extension for one run

For iteration on a working copy, skip the install + reload cycle
and load straight from disk for one terva session:

```
terva --ext ./my-extension        # short form: -e ./my-extension
terva --ext ./a -e ./b            # repeatable
```

`--ext` paths take precedence over installed extensions of the same
name, so you can shadow an installed copy with a work-in-progress
version without uninstalling first. Nothing is copied or persisted;
the extension dies with terva like any other subprocess.

## SDKs

Writing the wire protocol by hand is fine for one-off scripts, but
for anything bigger the SDKs handle the boilerplate.

### Go — `packages/agent/ext`

```go
package main

import (
    "encoding/json"
    "terva.sh/terva/packages/agent/ext"
)

func main() {
    e := ext.New("hello", "1.0.0")

    // Slash command
    e.Command("hello", "say hi", func(args string) ext.Response {
        return ext.Prompt("Greet me in one short sentence.")
    })

    // LLM-callable tool
    e.Tool("weather", "Current weather for a city.",
        json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
        func(args json.RawMessage) ext.ToolResult {
            var in struct{ City string `json:"city"` }
            json.Unmarshal(args, &in)
            return ext.TextResult(in.City + ": sunny")
        })

    e.Run()
}
```

Build with `go build -o hello .`, drop the binary + an `extension.json`
into `$TERVA_HOME/extensions/hello/`.

The SDK has four interceptor hooks, all optional:

```go
// e is the *ext.Extension returned by ext.New(...).

// Refuse calls or rewrite args before they run.
e.InterceptToolCall(func(tool string, args json.RawMessage) (bool, string) {
    if tool == "bash" { /* inspect args, return false, reason */ }
    return true, ""
})

// Richer variant: returns ToolCallDecision so you can also rewrite
// args via ModifiedArgs.
e.InterceptToolCallX(func(tool string, args json.RawMessage) ext.ToolCallDecision {
    return ext.ToolCallDecision{
        ModifiedArgs: json.RawMessage(`{"command":"echo GUARDED"}`),
    }
})

// Block the next turn before the model is called.
e.InterceptTurnStart(func(step int) ext.TurnStartDecision {
    if time.Now().Hour() < 9 { return ext.TurnStartDecision{Block: true, Reason: "outside business hours"} }
    return ext.TurnStartDecision{}
})

// Scrub or rewrite the assistant's final text before the user sees it.
e.InterceptAssistantMessage(func(text string) ext.AssistantMessageDecision {
    return ext.AssistantMessageDecision{
        ReplaceText: strings.ReplaceAll(text, "SECRET", "[redacted]"),
    }
})
```

`OnSession` is the safe place to change the cached prefix — the `Session`
handle carries the cache-affecting mutators (see
[Responsible use](#responsible-use-context--tools)):

```go
e.OnSession(func(s ext.Session) {
    if gitRepo(s.CWD) {
        s.RefreshContext(standingNotes)
        if s.ProtocolVersion() >= 4 { s.RestoreAllTools() }
    } else {
        s.RefreshContext("")                                 // clear the block
        if s.ProtocolVersion() >= 4 { s.WithdrawAllTools() } // hide useless tools
    }
})
```

See:
- `examples/extensions/hello/` — slash commands
- `examples/extensions/clock/` — slash commands in plain Node, no SDK
- `examples/extensions/weather/` — LLM-callable tool
- `examples/extensions/guard/` — event subscriptions + tool-call
  interception (refuses dangerous bash patterns)
- `examples/extensions/repo-aware/` — per-session context + tool
  withdrawal via the `Session` handle (the responsible cache-safe pattern)
- `examples/extensions/repo-aware-ts/` — the same pattern on the raw wire
  (TypeScript, no SDK): `subscribe` to `session_start`, then
  `refresh_context` + `set_withdrawn_tools`
- `examples/extensions/repo-aware-py/` — the raw-wire pattern in Python
  (stdlib only), the twin of the TS one
- `examples/extensions/todo/` — interactive persistent panel + tool
- `examples/extensions/scratchpad/` — source-run TypeScript commands + tool
- `examples/extensions/chat-loopback/` — the connector role (experimental):
  one process that is BOTH an extension and a chat connector, with a tool
  reading the live chat session's state (see below)
- `examples/extensions/world/` — the "extension as a world" pattern: a
  procedural place the agent explores through tools (senses + effectors), with
  persisted state, a live context card, a map panel, `ext.Sequential()`
  effectors, and a bundled persona

### Hot reload

Type `/reload-ext` in the TUI to tear down every running extension
subprocess, re-read the manifests from disk, and respawn the set.
The agent's tool registry is rebuilt automatically, so freshly-
registered extension tools become callable without restarting terva.
Useful while developing an extension: edit, save, `/reload-ext`,
done. Explicit `--ext` paths are remembered and reloaded alongside
discovered extensions.

### TypeScript / Python

These SDKs aren't in the main repo yet; the wire format is small
enough that a `~30 line` raw script gets you started in either
language. See the [Quick start](#quick-start) Python example for the
shape. SDK packages will land in follow-up commits.

## Connector role (experimental)

An extension can ALSO be a **chat connector** — the thing that delivers
inbound messages that start agent turns and carries replies back out
(normally a standalone `terva bot` executable, see
[connectors.md](connectors.md)). One process, both roles: your tools
and your message stream share state, credentials, and a live service
connection. The loopback demo's `loopback_stats` tool reads the very
chat session that delivered the message it's asked about.

**Why "experimental", and what graduates it:** the role is fully
implemented and tested, and the envelope has already survived the
entire connector-protocol-2 build-out without a single frame change.
But the only connector that has shipped over the tunnel is the
loopback demo — it proves the wire, not the packaging (the built-in
Discord connector dogfoods the connector protocol over an in-process
carrier, deliberately not this envelope). The label drops when the
first real connector ships as an extension. Until then the envelope
frames sit outside the wire's usual compatibility promise: a protocol
bump may reshape them without a migration path, so don't build
production extensions on them yet.

Declare the role in the manifest (without this the host refuses it at
the wire):

```json
{ "name": "chatterbox", "exec": "./chatterbox", "connector": true }
```

and in code, next to your tools — the transport is a plain
`connsdk.Transport`, the SAME interface a standalone connector
implements, built lazily once per chat session:

```go
e := ext.New("chatterbox", "0.1.0")
e.Tool("history_search", ..., handler)          // extension half, unchanged
e.Connector(connsdk.Capabilities{MaxTextLen: 4096},
	func(s connsdk.Session) (connsdk.Transport, error) {
		return newTransport(s.DataDir), nil // s.DataDir: inbound attachment dir
	})
e.Run()
```

There is no second protocol to learn: the extension wire carries the
connector protocol **verbatim** through a small envelope (protocol 5:
`register_connector` + `chat_open`/`chat`/`chat_close`/`chat_down`), so
the [connectors.md](connectors.md) frame reference is the reference
here too, and a transport moves between standalone and
extension-bundled packaging by swapping a few lines of `main`. That
verbatim carry is why the whole protocol-2 surface — asks with
buttons, speakers, threads, group admission, edits/reactions,
attachment kinds — reaches extension-bundled connectors with zero
extension-wire changes: implement the optional `connsdk` interfaces
and declare the matching feature strings, exactly as a standalone
connector would.

What activates it — consent is layered, and every layer is deliberate:

1. the manifest's `"connector": true` (install-time visibility);
2. **global installs only** — project-local extensions are never
   offered as chat services, so a cloned repo cannot declare itself a
   message source;
3. explicit selection by name: `terva bot run --connector <name>`, or
   `/connect <name>` in the TUI (connector extensions appear in the
   `/connect` picker tagged "extension"). Merely being installed never
   makes an extension a message source;
4. inbound messages still pass the host-owned pairing/allowlist gate
   before any turn runs.

Operational notes: transports should reconnect through blips
internally — a fatal transport error ends the chat session while the
process and its tools live on, and the host redials it under the same
crash budget standalone connectors get (a few reopens per minute, then
permanently broken; a process crash is respawned the same way, with
your tools re-registered by the fresh process). Echo-loop hygiene is
your job — never deliver a message the bot itself sent, and never
report the bot's own reaction toggles back inbound.

Reference implementation: `examples/extensions/chat-loopback/` (a
filesystem-backed chat: drop a file in `inbox/`, the agent's reply
appends to `outbox.txt`). Design and trade-offs:
`docs/proposals/connector-extensions.md`.

## Security

Extensions run with **the user's full filesystem and network
permissions**. Treat installing an extension the same as installing
any other binary on your machine.

`terva ext install <git-url>` clones from any URL you give it. There's
no sandbox in v1; if you need isolation, install only extensions you
trust or run terva under your platform's sandboxing tool (`bwrap` /
`sandbox-exec` / AppContainer).

## Roadmap

Phase 1 (shipped):
- [x] subprocess lifecycle + hello handshake
- [x] `register_command` + `command_invoked`
- [x] `notify` + `clear_notes`
- [x] `terva ext` CLI

Phase 2 (shipped):
- [x] `register_tool` + `tool_call` + `tool_result`
- [x] `ready` sentinel for safe agent-registry build timing
- [x] tool result attribution surfaces extension name in details

Phase 3 (shipped):
- [x] event subscriptions (`session_start`, `turn_start`, `turn_end`,
      `tool_call`, `assistant_message`)
- [x] tool-call interception (block before execution)

Phase 4 (shipped):
- [x] interception for `turn_start` and `assistant_message` (in
      addition to `tool_call`)
- [x] modify tool args mid-flight via `modified_args`
- [x] rewrite user-visible assistant text via `replace_text`
- [x] `/reload-ext` slash command (hot-reload without restarting terva)

Phase 5 (shipped; experimental until the tunnel has a real consumer —
see [connector role](#connector-role-experimental)):
- [x] connector role (protocol 5): one extension process that is ALSO a
      chat connector — `"connector": true` in the manifest, a
      `register_connector` role declaration plus a `chat_open`/`chat`/
      `chat_close`/`chat_down` envelope that tunnels the CONNECTOR
      protocol (connproto) through this wire verbatim, so the two
      protocols evolve independently and the transport is plain
      `connsdk.Transport`. Activated only by explicit selection —
      `terva bot run --connector <name>` or `/connect <name>` in the
      TUI. Demo: `examples/extensions/chat-loopback`; design:
      `docs/proposals/connector-extensions.md`; connector side:
      [connectors.md](connectors.md).

Future (no firm timeline):
- [ ] TypeScript and Python SDK packages (currently the wire format
      is stable enough to hand-roll, see the Python quick-start)
- [ ] HTTP / WebSocket transport variants (today: subprocess stdio)
- [ ] per-extension permission scopes (today: full user privileges)

## Installing and managing extensions

```bash
terva ext install <path|git-url>   # copy / clone into $TERVA_HOME/extensions/
terva ext list                      # show installed extensions
terva ext logs <name> [-f]          # cat or tail the extension's stderr log
terva ext config <name> [verb]      # show / get / set / unset its declared settings
terva ext enable <name>             # re-enable a disabled extension
terva ext disable <name>            # disable without removing
terva ext upgrade <name>...         # fast-forward-pull just these extensions
terva ext remove <name>             # delete an extension directory
terva ext doctor                    # diagnose discovery + registration (what loaded, what didn't)
```

For development, point `terva --ext <path>` at a working directory and skip the install step entirely. Repeatable; takes precedence over installed extensions of the same name.

### Updating extensions

`terva ext upgrade <name>...` upgrades just the named extensions, and
`terva update` refreshes the terva binary **and** every installed
extension at once. Both run the same per-extension logic (below) — `ext
upgrade` is the targeted form when you only want to bump one or two.
Per-extension behaviour:

- Disabled extensions are skipped.
- Extensions without a `.git/` directory (installed by `terva ext install ./local-path`) are skipped — there is no remote to pull from.
- For the rest, terva stashes any dirty worktree state (including untracked runtime files like `todos.json` or `config.json`), runs `git pull --ff-only`, and pops the stash. If the pop produces conflicts, the conflict markers are left in place and you'll see a warning.
- Diverged branches, offline pulls, or any other git failure are reported as `failed` and the next extension is processed. `terva update` itself never aborts because of an extension.
- terva does **not** run any build step (`go build`, `npm install`, `make`) after the pull — building stays the extension's job. The recommended way to handle this is a [self-bootstrapping launcher](#recommended-a-self-bootstrapping-launcher): `terva update` pulls new source, and the next launch (or `/reload-ext`) rebuilds automatically because the sources are now newer than the binary. An extension that instead commits a prebuilt artifact (binary, transpiled JS) just keeps working from the pulled copy. Either way, if you need to force a rebuild now, do it manually and `/reload-ext`. **On a deployed service, prefer the prebuilt artifact**: the post-update rebuild happens on the next spawn and so competes with the 10-second hello timeout described [above](#recommended-a-self-bootstrapping-launcher), which is how an update can leave an extension silently skipped until something rebuilds it out-of-band.

This whole flow assumes terva **owns** the install tree: it pulls into it,
stashes into it, and writes there as the agent's own user. An externally
administered deployment — code installed and updated by root, or an install
tree mounted read-only — is outside what `terva update` models today. There,
skip terva's updater entirely, change the tree by whatever provisions it, and
restart the service; `terva ext upgrade` will report a failure rather than
mutate a tree it does not own. Defining that contract properly (release
identity, atomic switch, rollback) is the subject of the internal
managed-extension-catalog proposal.

### Theme-only extensions

An extension may ship only a theme: `extension.json` plus `theme.json` (or `themes/theme.json`) and no executable. terva loads it without spawning a subprocess and shows it in `/settings` with source information. See [docs/themes.md](themes.md).
