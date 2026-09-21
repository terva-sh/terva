# `terva web`: the browser control panel

`terva web` serves a browser UI for a self-hosted terva: chat with full
tool-call fidelity, switch and nickname sessions, switch models, and watch
usage, from any device that can reach the page. It is the TUI's reach, over
the wire, plus a control panel.

This page is how you run it: the listener, the reverse proxy, auth, deployment,
self-restart, and building the client. For what the panel shows once it is open,
surface by surface, see [web-interface.md](web-interface.md).

It is an **opt-in build** (build tag `terva_web`), excluded from the `min`
binary exactly like the `terva acp` mode and the chat connectors. The full
install one-liner and `just install` include it.

The protocol it speaks is the `ctrlproto` control plane.
[docs/controllers.md](controllers.md) is the reference. The design record
(`docs/proposals/terva-web.md`), the protocol rationale
(`docs/proposals/control-plane-protocol.md`), and the platform horizon
(ticket `TKT-01M1RRT6N`) live in the development repository, not the public
release tree.

## Running it

```bash
just install            # full build (includes terva_web)
terva web               # serves http://127.0.0.1:8730 (loopback, no auth)
```

Open the address in a browser. On mobile, use *Add to Home Screen*. It is an
installable PWA.

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `--web-addr` | `127.0.0.1:8730` | listen address |
| `--web-token` | — | require `Authorization: Bearer <token>` (or `?token=` on the socket). Readable by any local user via `ps`. Prefer the two below for anything long-lived |
| `--web-token-file` | — | read the bearer token from a file; never enters the environment (systemd `LoadCredential=`) |
| `TERVA_WEB_TOKEN` (env) | — | the bearer token (systemd `EnvironmentFile=`), scrubbed from `os.Environ()` once read but **still readable via `/proc/<pid>/environ`**. See [Auth](#auth); prefer `--web-token-file` |
| `--web-token-require-file` | off | hardening opt-in: accept the token only from `--web-token-file`; refuse `--web-token` and `TERVA_WEB_TOKEN` as a startup error |
| `--web-auth-header` | — | trust this forward-auth header as the authenticated user (only from loopback or a trusted proxy; see [Auth](#auth)) |
| `--web-trusted-proxy` | — | IP/CIDR(s) allowed to assert `--web-auth-header` (comma-separated; loopback is always allowed) |
| `--web-insecure` | off | permit a non-loopback bind with **no** auth mode (dangerous: open to any source) |
| `--web-insecure-cidr` | — | grant **no**-auth access to these source IP/CIDR(s) only (comma-separated; loopback always allowed). The scoped, safer form of `--web-insecure` for a trusted overlay network (see [Auth](#auth)) |
| `--allow-restart` | off | enable Tier-1 self-restart (see [Self-restart](#self-restart)); `--web-allow-restart` is the accepted older spelling |
| `--web-stage` | off | mount **Stage**, the immersive chat/play surface, at `/stage/`. A second web app alongside the panel, gated by the same auth, opted into per deployment (see [Stage](#stage-the-immersive-chatplay-surface)) |
| `--web-allow-login` | off | serve the provider-login group so the web UI can add / repair / revoke the **model-provider** credential (Anthropic/OpenAI/Kimi/…). Off by default and, like `--allow-restart`, must never ride an unauthenticated listener. Writing a credential is more authority than driving a conversation. **Also decides what a credential-less start means**: with login on, the daemon boots with no credential at all and you sign in from the panel; with it off there is no route to one, so that start is refused (see [Auth](#auth)) |
| `--web-allow-secrets` | off | serve the **secrets** group: terva's at-rest encryption posture (what is sealed, what is still plaintext, which components hold a key) and the secret store's grants. Its own flag rather than a corner of `--web-allow-login`, because the two grant different authority. Login writes the credential terva uses to reach a model provider, while this reports on the key that opens *everything*. **No verb returns a secret value**, and neither rotation mode is on the wire at all; both stay on the CLI, where a bug in a client cannot brick the install. Refused on an unauthenticated listener for the same reason login is: the report names every scope, component and grant on the host, which is a map worth withholding from a stranger who can reach an open port. The control panel shows it as a **Secrets** tab in the workspace drawer, beside Providers; the tab is absent entirely when the daemon did not negotiate the group |
| `--web-methods` | — | serve **only** these ctrlproto verbs (comma-separated), refusing every other one before dispatch. May be given **once**: a second occurrence is an error rather than a union or an override, because unioning would widen a flag whose job is to narrow, and overriding would discard a list without saying so. The one flag here that **narrows** rather than widens, which is why it is not spelled `--web-allow-*`. Use it when the group and capability axes cannot express the limit you want: `prompt` and `suggest.next_step` share a capability set, so no mask separates them, and they share a group, so no negotiation does either. Unknown names are rejected at startup, because a typo would otherwise produce a daemon refusing the verb you meant to allow. For a **headless daemon driven by one known program**. A person driving it interactively would hit refusals with no way to widen short of a restart |

Standard flags apply too: `--cwd` pins the workspace, `--model` / `--provider`
pick the default, `--yolo` runs without approval prompts, `--jail` / `--no-jail`
set the sandbox default.

> **Bind address.** `--web-addr 0.0.0.0:8730` binds all IPv4 interfaces. That is the
> reliable form for reaching the panel over an IPv4 overlay (tailscale, most
> LANs). A bare `--web-addr :8730` binds the IPv6 wildcard (`[::]`), which is
> dual-stack on a typical host but will NOT accept IPv4 on a host configured
> `net.inet6.ip6.v6only=1`; prefer `0.0.0.0:PORT` unless you specifically want
> IPv6. Either way a non-loopback bind needs an auth mode or `--web-insecure-cidr`.

## Behind a reverse proxy: the WebSocket is a long, quiet connection

The panel holds one WebSocket open for the life of the session, and it says
**nothing at all** while the agent is thinking. Proxies read that silence as a
dead connection.

terva pings every 20 seconds so the traffic keeps the proxy's idle timer from
firing, which is enough for the defaults you are likely to meet. But a proxy
configured tightly enough will still cut the socket, and it is worth knowing what
to look for: a panel that reconnects on a **fixed interval**, forever, is not a
terva bug. It is the proxy's idle timeout, and the interval is its value.

The same wall stands in front of a fleet, where the tunnel carrying a member is
one more hop that can time out quietly. `terva web --fleet-addr` opens a second
listener for members to check in on, with its own ping and its own reap. See
[fleet.md](fleet.md).

- **haproxy** applies `timeout client` / `timeout server` to an upgraded
  WebSocket unless you say otherwise. Give tunnels their own, longer timeout:

  ```
  defaults
    timeout client  50s
    timeout server  50s
    timeout tunnel  1h   # WebSockets — without this they inherit the 50s above
  ```

- **nginx** defaults `proxy_read_timeout` to 60s. Raise it for the panel's
  location, and pass the upgrade headers:

  ```nginx
  proxy_http_version 1.1;
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection "upgrade";
  proxy_read_timeout 1h;
  ```

Complete, validated HAProxy configs are in
[`examples/deploy/haproxy/`](../examples/deploy/haproxy/): a loopback-TCP
backend and a filesystem-socket backend, with the tunnel timeout, host routing,
and the auth model wired up.

## Auth

terva's identity is single-user, so the auth here is a **gate** ("keep
strangers out") more than an identity system. [Single sign-on](#single-sign-on)
is the exception, and it resolves a real principal with roles. Either way the
gate fails closed: binding a non-loopback address with no auth mode is refused
unless you pass `--web-insecure`.

- **Front it (recommended).** Bind loopback and put a reverse proxy that does
  real auth in front. The author runs [Authentik](https://goauthentik.io/)
  forward-auth. Point terva at the header the proxy sets:

  ```bash
  terva web --web-addr 127.0.0.1:8730 --web-auth-header X-Forwarded-User
  ```

  The forward-auth header is a **proxy assertion**, so terva honors it **only
  from a peer it can identify**: a loopback connection (the proxy runs on the
  same host, the shape above) or an IP inside a `--web-trusted-proxy` CIDR.
  Otherwise the header is forgeable by anyone who can reach the port directly.
  For that reason a **non-loopback bind under header auth requires
  `--web-trusted-proxy`** (naming the proxy's address) and is refused without it:

  ```bash
  # remote proxy fronting a non-loopback bind → name its network
  terva web --web-addr 0.0.0.0:8730 --web-auth-header X-Forwarded-User \
            --web-trusted-proxy 10.0.0.0/24
  ```

- **Bearer token (proxy-less).** For quick remote access over Tailscale/WireGuard.
  The shortest safe route is to let terva issue the token, which writes it
  owner-only to `$TERVA_HOME/web-token` and shows the value once:

  ```bash
  terva secret web-token init          # mint it (see cli.md)
  terva web --web-addr 0.0.0.0:8730    # picks the file up automatically
  ```

  That file is the LAST source in the resolution order below, so it never
  overrides an explicit one, and `terva attach` on the same `$TERVA_HOME`
  finds it too. `terva secret web-token rotate` replaces it. A running daemon
  keeps accepting the old token until it restarts. Supplying your own token
  works exactly as before:

  ```bash
  terva web --web-addr 0.0.0.0:8730 --web-token "$(openssl rand -hex 24)"
  ```

  **Don't leave it on the command line for anything long-lived.**
  `/proc/<pid>/cmdline` is world-readable, so `ps` hands the token to every other
  user on the box, and under systemd it also sits in the unit file and in
  `systemctl show`. Two routes keep it out:

  ```bash
  TERVA_WEB_TOKEN="$(openssl rand -hex 24)" terva web --web-addr 0.0.0.0:8730
  terva web --web-addr 0.0.0.0:8730 --web-token-file /etc/terva/web-token
  ```

  `TERVA_WEB_TOKEN` is scrubbed from `os.Environ()` once read, so the agent's own
  shell tool, which inherits that environment, cannot read the token back out
  with `env`. It does **not** vanish from `/proc/<pid>/environ`, though: the kernel
  wrote the value to the process's initial stack at `execve`, and the scrub cannot
  reach that region, so any same-UID process (the agent's shell included) can still
  recover it there. A self-restart re-exposes it each generation. On startup
  terva warns when the token is still readable that way. `--web-token-file` keeps it
  out of the environment *and* out of `/proc`, which is also what systemd's
  `LoadCredential=` wants; note that on a single-user host the token file is itself
  readable by that user's agent shell, so this closes the ambient leak, not a
  determined same-UID read. A token file that is missing or empty is a startup
  error, never a silent fall back to no auth. For a host that has committed to the
  file route, `--web-token-require-file` (off by default) turns `--web-token` and
  `TERVA_WEB_TOKEN` into a startup error, so a later change can't silently regress
  to a leaky source. The minted `$TERVA_HOME/web-token` satisfies it: it is a
  file, and its value never passes through argv or the environment.

  **Resolution order** (daemon): `--web-token` → `--web-token-file` →
  `TERVA_WEB_TOKEN` → `$TERVA_HOME/web-token`. The attach client reads the same
  chain under its own spellings (`--token`, `--token-file`). The minted file is
  last, so it turns auth on when nothing else is configured and gets out of the
  way when something is. Both the secrets key and this token file are on the
  agent's read deny-list. Reading the token is reading a live grant to drive
  the agent.

  **In the browser, just open the panel.** An unauthenticated page load gets a
  login form; type the token and the daemon exchanges it for an HttpOnly,
  SameSite=Strict cookie. The cookie carries the app *and* the WebSocket
  handshake, so from then on the token appears in no URL at all. Browsers send
  cookies on a same-origin socket, which is the one thing they will not let you
  set a header on. Guesses at the form are compared in constant time and
  throttled per source IP.

  `?token=<secret>` still works and is still how you hand someone a one-click
  link, but it is no longer the only way in, and it is the worse one: a token in a
  URL lands in the fronting proxy's access log, the browser's history and
  autocomplete, and the `Referer` of anything the page links to. terva's own logs
  never record it (auth failures log the path only), but the rest is out of its
  hands. The form posts the token in a request body, which none of them record.

  Non-browser clients send `Authorization: Bearer <token>` and never touch either
  path.

- **Trusted network, no per-request auth.** To expose the panel over a private
  overlay (Tailscale/WireGuard/VPN) and let the *network* be the boundary (no
  token, no proxy), scope no-auth access to the overlay's source range with
  `--web-insecure-cidr` instead of the blanket `--web-insecure`:

  ```bash
  # reachable only from tailnet peers (100.64.0.0/10); everyone else gets 403
  terva web --web-addr 0.0.0.0:8730 --web-insecure-cidr 100.64.0.0/10
  ```

  Requests are admitted only when the **source IP** is loopback or inside a named
  range; the `Host` check is relaxed for those peers so reaching the panel by its
  real overlay IP/name works, and [self-restart](#self-restart) is permitted
  (the range bounds who can trigger it). This is strictly narrower than
  `--web-insecure`, which admits *any* source. There is still **no per-user auth
  inside the range**: anyone who can source-spoof into it, or any device on the
  overlay, has full owner access. Use it only where you trust the network, and
  layer a token or forward-auth on top for anything shared.

### Single sign-on

The three modes above are gates. They decide whether a request gets in, and
whatever gets in is the owner. Single sign-on is the one mode that produces an
**identity**. A sign-in resolves a principal, carrying a subject, a display name,
and a set of roles, so two people reaching the same daemon can hold different
authority.

It stays off until `config.json` carries a `web_oidc` block, so a daemon without
one keeps exactly the auth modes it had. Put that block in the **user** layer
under `$TERVA_HOME`, never in a project's `.terva/config.json`. A repository that
could name an issuer would be a repository that decides who terva trusts to log
in.

```json
{
  "web_oidc": {
    "issuer": "https://id.example.com/application/o/terva/",
    "client_id": "kAgbCNRmQ1…",
    "client_secret": "enc:age:v2:…",
    "redirect_url": "https://terva.example.com/auth/oidc/callback",
    "role_map": {
      "terva-admins": "owner",
      "terva-users": "member"
    }
  }
}
```

| Field | Default | Notes |
|---|---|---|
| `issuer` | required | terva fetches `.well-known/openid-configuration` under it at startup |
| `client_id` | required | identifies terva to the provider |
| `client_secret` | optional | omit it for a public client, which authenticates with PKCE alone. Seal it when you do set one (below) |
| `redirect_url` | required | absolute, and its path must be `/auth/oidc/callback` |
| `scopes` | `profile`, `email`, `groups` | `openid` is always sent, whatever you list |
| `groups_claim` | `groups` | Authentik and Keycloak both use that name |
| `role_map` | empty | maps one provider group to one terva role |
| `allow_insecure_issuer` | `false` | permits a plaintext `http://` issuer. Development only, because discovery over plaintext is unauthenticated, so whoever can rewrite it points the key fetch at their own keys and signs any identity they like |

The four roles, in descending authority:

| Role | May |
|---|---|
| `owner` | everything the carrier offers |
| `operator` | run and reconfigure the host, but not touch credentials or the at-rest posture |
| `member` | converse and manage their own sessions. No models, lore, extensions, prompt overrides, or jail, and no credentials |
| `viewer` | read, and nothing else. A viewer cannot call a model, so a viewer cannot spend your subscription |

Name a role that is not one of those four and terva logs `role map names "x",
which is not a terva role — ignoring` at startup and drops it.

**Seal the client secret rather than pasting it**, when the provider gives you
one. `terva secret encrypt` takes the config path the value will live at, because
a sealed value is bound to that path and refuses to open anywhere else:

```bash
printf %s "$CLIENT_SECRET" | terva secret encrypt web_oidc.client_secret
```

Paste the `enc:age:v2:…` output into `client_secret`. A plaintext secret still
works, and `terva secret status` will keep reporting it as plaintext until you
seal it.

Four things are worth knowing before the first login.

**`redirect_url` is explicit and is never derived from the request's `Host`.**
A derived value would let a forged `Host` header steer the authorize request,
and while a correctly configured provider rejects an unregistered `redirect_uri`,
that makes your identity provider's allowlist terva's only defense against its
own input handling. Register the same absolute URL with the provider. Omit the
field and startup fails with a message naming the path.

**An unmapped user gets nothing.** Authenticating proves who somebody is. It
says nothing about whether you meant to give them access, so a user whose groups
map to no role signs in successfully and is then refused, and the daemon logs
the groups it actually received. Start with an empty `role_map` and terva warns
at startup that every sign-in will succeed and then be refused.

**Map every group you grant, including nested ones.** A provider's groups claim
usually carries **direct** memberships only, even where that provider's own
access rules honor group nesting. Authentik is the case to watch. Binding a
parent group to an application admits a member of a child group, but the default
`profile` mapping emits the child alone. So a user in `terva-admins`, nested
under `terva-users`, arrives carrying `terva-admins` and nothing else. Map both
names. terva unions every role a user's groups match, so naming both costs
nothing and naming one silently grants less than you intended, or nothing at all.

**Sessions do not survive a restart.** They are held server-side, with only an
opaque random id in the cookie, so there is nothing to forge and no signing key
to keep in a model-writable `$TERVA_HOME`. The cost is that restarting the daemon
logs everybody out. For one daemon that is a cheap re-login rather than a design
problem.

Single sign-on and a bearer token coexist, and the login page offers both when
both are configured. Keep the bearer. It is the way back in when the provider is
unreachable, and it is what a non-browser client uses. Configuring single sign-on
counts as configuring auth, so it satisfies the same startup check the other
modes do and relaxes the `Host` check the no-auth mode depends on.

The routes are `/auth/oidc/start` to begin a sign-in, `/auth/oidc/callback` for
the provider to return to, and `/auth/oidc/logout` to drop the session.

### The first model-provider credential

The auth above gates *reaching* the panel. A separate credential lets terva reach
a **model provider**, and on a box with no terminal there used to be no way to
supply the first one: the daemon refused to start without it, and the pane that
could add it lives inside the daemon that would not start.

`--web-allow-login` resolves that, because it is what decides whether a
credential-less start is recoverable:

```bash
# no credential anywhere — boots, serves, and waits for you to sign in
TERVA_WEB_TOKEN="$(openssl rand -hex 24)" \
  terva web --web-addr 0.0.0.0:8730 --web-allow-login
```

The daemon starts, says it has none, and serves the panel; sign in from the
Providers pane and sessions work from that moment, with no restart. Without
`--web-allow-login` nothing reachable can supply a credential, so a
credential-less start is still refused, and the error names the flag.

A credential seeded any other way works as before and makes all of this moot:
`/login` at a terminal, an API key in the environment, a provisioned
`auth.json`.

**DNS-rebinding defense.** In no-auth mode terva also requires the request's
`Host` to be a loopback name, so a malicious web page can't rebind its own
hostname to `127.0.0.1` and drive your local panel through your browser.
Authenticated modes don't restrict `Host` (proxy hostnames vary); a
`--web-insecure-cidr` peer inside the range is likewise unrestricted (a loopback
*source* still gets the check, so your local browser stays protected).

> **The auth gate is the whole application-layer boundary.** Once reachable, this
> endpoint can run `bash` as you. Prefer Tailscale/WireGuard + forward-auth over
> raw public exposure; the VM perimeter is the real isolation, and the in-process
> jail is a guardrail, not a boundary.

## Deployment

The intended shape is a dedicated LXC/VM running `terva web` as a systemd
service, pinned to one project directory:

```ini
# /etc/systemd/system/terva-web.service
[Service]
ExecStart=/usr/local/bin/terva web --web-addr 127.0.0.1:8730 --web-auth-header X-Forwarded-User
WorkingDirectory=/srv/workspace
Restart=always
```

Fronting it with a plain proxy instead (no forward-auth), give it a bearer
token, and keep the token out of `ExecStart`, which `ps` shows to every local
user:

```ini
[Service]
# systemd reads the file (0600, root-owned) and hands it over out of band
LoadCredential=web-token:/etc/terva/web-token
ExecStart=/usr/local/bin/terva web --web-addr 127.0.0.1:8730 --web-token-file %d/web-token

# or, the EnvironmentFile route — TERVA_WEB_TOKEN=<secret> in a 0600 file
EnvironmentFile=/etc/terva/web.env
ExecStart=/usr/local/bin/terva web --web-addr 127.0.0.1:8730
```

**Where the token lives decides whether it is doing anything.** If the proxy
*injects* `Authorization: Bearer …` on the way through, it stamps that header for
everyone it proxies, an attacker included, so the token buys nothing against
whoever can reach the proxy, and only closes the direct-to-loopback path. For the
token to be real auth, the **client** has to carry it: open the panel, type the
token into the login form, and the cookie takes it from there. If instead the
network genuinely is your boundary, say so with `--web-insecure` rather than
running a token that only looks like a lock.

Sessions persist under `$TERVA_HOME`, so a restart (deploy, crash, reboot)
loses no history. The daemon defaults its working directory to the pinned
project; the panel can flip the jail off so tools can reach beyond it when a
task needs to.

### Unix socket

`--web-addr unix:/path/to/terva.sock` serves the same HTTP + WebSocket stack
on a filesystem socket instead of TCP. The socket is created `0600` and the
file's permissions **are** the auth boundary, so there is no token dance for a
same-user client (a `--web-token`, if set, is still enforced on top; the
IP-based options are meaningless here). Browsers can't dial filesystem sockets, so
this form is for `terva attach unix:/path/to/terva.sock` and programmatic
ctrlproto clients; a stale socket left by a crash is cleared on the next
start, and a live daemon's socket is refused rather than stolen.

Clients need not be told the path. Whatever the daemon binds, whether TCP, a
filesystem socket, or a systemd-passed fd, it publishes as
`$TERVA_HOME/listen.json` (`0600`, no secret in it: an `auth` flag says a
token is required, never what it is), refreshes a heartbeat while it runs,
and removes it on exit. `terva attach` with no URL and `terva ext config`
with no `--endpoint` read that record, so both reach the daemon serving
*this* home without a flag. A record that stops being heartbeated, after a
crash, reads as absent rather than as a daemon that will not answer.

### systemd socket activation

When `LISTEN_FDS` names the process (a `.socket` unit started the service),
the passed socket (unix or TCP, whichever the unit declares) is served and
`--web-addr` is ignored. The daemon starts on the first connection rather
than at boot, and Tier-1 self-restart re-adopts the inherited socket across
the exec (the pid is unchanged, so `LISTEN_PID` stays valid):

```ini
# ~/.config/systemd/user/terva-web.socket
[Socket]
ListenStream=%t/terva.sock
SocketMode=0600

[Install]
WantedBy=sockets.target
```

```ini
# ~/.config/systemd/user/terva-web.service
[Service]
ExecStart=/usr/local/bin/terva web --allow-restart
# `systemctl --user reload terva-web` re-execs into the installed binary in
# place — a real restart, not a config re-read. See "Self-restart" below.
ExecReload=/bin/kill -HUP $MAINPID
WorkingDirectory=%h/workspace
```

`systemctl --user enable --now terva-web.socket`, then
`terva attach unix:/run/user/1000/terva.sock`. The first attach spawns the
daemon.

### A persistent terminal

The socket-activated daemon plus a supervised `terva attach` gives you a
terminal that is always your terva session: close the laptop, SSH back
tomorrow, reattach, and the conversation is exactly where you left it, still
live, because the daemon never stopped. `examples/deploy/systemd/` ships the
three user units:

- **`terva-web.socket`**: the activation point (`%t/terva.sock`, `0600`).
- **`terva-web.service`**: the daemon; it holds every session, credential, and
  tool. Reloading it is what preserves your work across a new build.
- **`terva-attach.service`**: `terva attach` held in a detached
  [`dtach`](https://github.com/crigler/dtach) pty, so a live TUI keeps running
  with nobody watching.

The example uses `dtach` rather than a full multiplexer on purpose: it holds
*one* program in a detachable pty with no windows, no config, and no emulation
layer of its own, so terva's colors, mouse, and escapes reach your terminal
unmediated. terva already owns its transcript and scrollback, which is the one
thing a multiplexer's screen buffer would otherwise add. (`abduco` and GNU
`screen` work the same way; tmux does too if you already live in it.)

Two layers persist independently. The **daemon** holds the conversation, so
even a killed client resyncs to the live session on reconnect (and sessions
are on disk regardless). **`dtach` keeps the client process alive** with its
own state (the input you were typing, where you'd scrolled), so on reattach
terva repaints exactly that, not a fresh session. Because dtach keeps no screen
buffer, terva repaints itself on reattach; `dtach -r ctrl_l` triggers that, and
terva's same-size-SIGWINCH repaint covers the buffer-less muxes that can't
inject a key.

```
# install (see the comments in terva-web.socket for the full sequence)
cp examples/deploy/systemd/terva-{web.socket,web.service,attach.service} ~/.config/systemd/user/
# credentials: either seed one first (run `terva`, then /login), or start the
# daemon with --web-allow-login and sign in from the panel's Providers pane
systemctl --user enable --now terva-web.socket terva-attach.service
loginctl enable-linger $USER

# reach it from any shell
dtach -a $XDG_RUNTIME_DIR/terva.dtach -r ctrl_l              # detach: Ctrl-\
```

That last line reattaches from a shell already on the box. To dial straight in
from your workstation in one hop, with no intermediate shell, let SSH run it
(the remote shell expands `$(id -u)`):

```
ssh -t you@host 'exec dtach -a /run/user/$(id -u)/terva.dtach -r ctrl_l'
```

or make it a one-command alias in `~/.ssh/config`, so `ssh terva` drops you
straight into the terminal:

```
Host terva
    HostName host.example
    RequestTTY force
    RemoteCommand exec dtach -a /run/user/$(id -u)/terva.dtach -r ctrl_l
```

Mind what that terminal is: it *is* the daemon, running with its tools,
filesystem, and (if configured) sudo, so SSH access to it is administrative
access. It is not a sandboxed "terminal key," and no substitute for `sshd`
restricting who may log in at all.

Deploying a new build is then two reloads, and nobody loses their place:

```
systemctl --user reload terva-web       # daemon re-execs in place; clients blink + resync
systemctl --user reload terva-attach    # the client terva re-execs on the same pty + reconnects
```

`reload terva-web` keeps the socket up throughout (systemd holds it, the daemon
re-adopts it), so the client's reconnect is immediate. `reload terva-attach`
SIGHUPs the terva process (the client-side self-restart), so it comes back on
the new build on the same pty, without disturbing your dtach session. Note that
`/restart` *inside* the attached TUI restarts the daemon over the wire, not the
client; the client is reloaded from the outside, by signal.

## Self-restart

With `--allow-restart` (also accepted as `--web-allow-restart`, its original
web-only spelling), `terva web` can restart itself into the
currently-installed binary, with no external supervisor, to pick up a new
build. It is a Tier-1 restart: it re-execs the same executable (via
`exec(2)`) with the original arguments and environment, so the PID is
preserved and the process image is replaced in place. Because install is atomic (`go install` /
`just install-dev` rename over the same path), the next restart runs the new
code.

It is **off by default** and refused outright on a *blanket* insecure listener.
If you pass `--web-insecure` (open to any source) with no `--web-token` or
`--web-auth-header`, restart stays disabled (a stranger must never be able to
re-exec the daemon). A `--web-insecure-cidr` listener bounds who can reach it to
a trusted source range, so restart **is** permitted there. It is unix-only
(`exec(2)`); running from a `go run` temp binary is rejected with a clear error.

Three ways to trigger it, all funneling through the same path:

- **From the browser**: a **Restart terva** control appears in the **Settings**
  pane (arm, then confirm). It calls the `control.restart` ctrlproto method.
- **From the OS**: a **SIGHUP** re-execs the daemon, so a systemd unit with
  `ExecReload=/bin/kill -HUP $MAINPID` turns `systemctl reload terva-web` into an
  in-place upgrade to the installed build. Unlike the browser and tool paths,
  SIGHUP carries no bearer token: the kernel only lets the unit's owner or root
  signal the process, so the signal *is* the authorization. It still honors
  `--allow-restart`: started without it, the daemon **logs the reload and keeps
  serving** rather than restarting (and, deliberately, rather than letting the
  unhandled signal terminate it, which is SIGHUP's default).

  > **`reload` here is a real restart, not a config re-read.** It runs the same
  > internal Tier-1 mechanics as every other trigger: `exec(2)` replaces the
  > running process in place, so despite the **preserved PID there is a brief
  > outage**: open connections drop and the endpoint is unavailable in the gap
  > between the old image handing off and the new one finishing startup
  > (credential resolve, MCP spawn, tool listing) and re-binding. An in-flight
  > turn is interrupted. It is for picking up a new **build**, exactly like
  > `restart`. Reload while idle; do not wire it into a per-file hot-reload
  > loop.
- **From the agent**: a `terva_restart` tool. It is registered only when the
  flag is set and **prompts for your approval** in the browser before running in
  every gating approval mode (it is left unclassified in the permission model, so
  the default `workspace`/`ask`/`auto-edit` modes all confirm it). This is the
  loop where terva edits its own code, reinstalls, and relaunches on the new
  build with a human in the middle.

  > **Caveat: `--yolo` skips this prompt too.** yolo bypasses the approval gate
  > for *every* tool, so with self-restart enabled **and** `--yolo`, the agent can
  > re-exec the daemon without confirmation. That was a deliberate v1 choice
  > (yolo means "run freely"); combine the two only when that's acceptable.

On restart the daemon broadcasts a `terva vX is restarting — reconnecting
shortly` notice (naming the outgoing build), then replaces the image after a
brief flush delay. The version is visible on both sides of the hop: stderr
logs the running build with the restart request and the new image logs
`self-restart complete — was vX, now vY` on boot (the prior version rides the
exec env), while in the browser the Settings pane shows the build serving the
panel and a toast announces the version change once the client reconnects to a
different build. Sessions persist to disk per-message, and the PWA
auto-reconnects and restores from the on-disk snapshot, so no history is lost.
But an **in-flight turn is interrupted** (Tier 1 does not preserve active tool
calls). Prefer restarting while idle.

## Attaching the TUI

`terva attach [URL]` connects the interactive TUI to a running `terva web`
daemon as a second client: same sessions, same live stream as the browser
panel, with the PWA's reconnect/resync discipline. See docs/tui.md
§"Attaching to a running daemon".

## Stage: the immersive chat/play surface

**Stage** is terva's SillyTavern-inspired chat/play front end, a second web app
served by the same daemon, for driving a character card or persona through an
immersive conversation or roleplay rather than a coding session. It is **off by
default**; `--web-stage` mounts it at `/stage/`, alongside the control panel at
`/`.

- **A distinct app, one daemon.** Stage is its own composition root and its own
  installable, offline-capable PWA (`start_url`/`scope` `/stage/`; it reuses the
  terva app icons until it earns its own), not a third view mode of the panel. It
  shares the panel's transport, auth gate, and embed: one `/ws`, one login.
  Exactly like the panel, Stage's **bundle, service worker, manifests, and icons
  are served ungated under `/stage/`** so the `/stage/`-scoped worker can precache
  them (a precache entry the client cannot fetch is a worker that cannot install),
  while its **shell (`stage.html`) stays auth-gated** so an unauthenticated
  navigation answers with the login form. See `packages/agent/web/assets.go`
  (`stagePwaShellPaths`) and `stage_sw_gate_test.go`.
- **What it is made of.** Immersive sessions are ordinary sessions carrying an
  *experience*, either `chat` (pure conversation, no tools) or `play` (embodied
  in a world via extension/MCP tools), created next to coding sessions in the
  same workspace. The character library (the
  [Characters](web-interface.md#panes) pane), the transcript-revision grammar
  (swipe / regenerate / edit any message), scene backgrounds, and greeting
  seeding are all engine/wire primitives. Stage is one consumer of them, the
  panel's focus view is another.
- **Enabling it.** Pass `--web-stage`, or set `"web_stage": true` in config (the
  flag OR the knob turns it on). The flag suits a one-off, the knob a
  deployment that always wants Stage. There is also a **Settings toggle**
  ("Stage surface") that writes the config knob; because the `/stage/` route is
  mounted when the web server starts, the toggle takes effect on the next launch
  (a persist-only setting, like lazy tool loading). Once on, the daemon advertises the `stage` hello feature,
  which a panel reads to offer an "open in Stage" link. Access is gated by the same
  auth as the panel: Stage inherits it, and there is no separate gate.
- **Under `terva serve`, the answer has two halves and they belong to different
  people.** The supervisor's own `--web-stage` (or `web_stage` in *its* config)
  decides whether `/stage/` exists **on the host**. The app is embedded build
  output, identical for everyone and holding no tenant data, so the supervisor
  serves it directly rather than proxying it to a child. Each tenant's own
  `web_stage`, in their own home, decides whether *they* are offered it. The
  supervisor cannot make that second decision for them: under
  `--containment systemd` it does not even start the child (systemd does, from a
  unit the operator installed), and under any containment the tenant's config
  lives inside the home the isolation exists to keep it out of.
  So a tenant who turns `web_stage` on where the operator has not gets the `stage`
  feature **stripped from the hello** the supervisor forwards, rather than an
  "open in Stage" link that lands on a route the host does not serve. See
  `tenant.Carrier` and `NarrowHello`.
- **Status.** Stage is **opt-in while it matures**. That is progressive
  disclosure: the panel is the power surface, and Stage is turned on per
  deployment once it's good. The design of record is
  `docs/proposals/stage-surface.md`: the session model, the interaction grammar,
  the content library, and the two-app split.

## Building the client

The web client is a Preact + Vite **multi-page app (MPA)** under
`packages/agent/web/client/`, building **two** apps from one workspace: the
control **panel** (`index.html`) and, behind `--web-stage`, the immersive
**[Stage](#stage-the-immersive-chatplay-surface)** app (`stage.html`). Rollup
chunk-splits the code they share, so the two apps share bytes in `dist/` rather
than duplicating them, and one npm project keeps a single typecheck, i18n catalog,
mock-backend smoke harness, and `web-verify-dist` gate across both. The build
output (`client/dist/`) is **committed** and embedded via `go:embed`, so
`go build -tags terva_web` and the release pipeline need no Node.js. After
changing anything under `client/src`, rebuild and commit:

```bash
just web-build     # npm ci + vite build -> client/dist (commit the result)
```

The client source is organized by dependency direction, low to high:

- `src/platform/`: Preact-free protocol, conversation-state, and image-policy
  modules; **fully shared** by both apps (the consistency layer);
- `src/ui/`: small shared presentation primitives plus browser and formatting
  helpers;
- `src/features/`: reusable Preact features and feature-local behavior
  (conversation attachments, interactions, sessions, model UI), shared
  selectively;
- `src/app.tsx`: the control-**panel** composition root (owns the transport and
  every verb); and
- `src/apps/stage/`: the **Stage** composition root, with its own routes,
  shell, theme, and PWA manifest.

The layering is enforced by `boundaries.test.ts`: an app may import
`platform`/`ui`/`features`; those lower layers never import an app; and **the
Stage app and the panel never import each other**: anything both need is
promoted down into a shared layer, never reached across. Keep transport calls in the
composition/controller layer and pass typed data and callbacks into visual
components. Run `just web-test`, the client `typecheck` and `i18n-check` scripts,
and `just web-build` after source changes.

## Languages

The panel follows terva's operator language (the `TERVA_LANG` env var; the
legacy `ZOT_LANG` spelling still works <!-- rename:keep -->; else config
`language`. The OS `LANG` is *not* consulted. See
[docs/localization.md](localization.md)), and you can change it from **Settings →
Language**: it switches the daemon's active language live, saves it as the
default, and broadcasts to every open tab (each re-fetches its catalog + panes
and re-renders, server-rendered titles/labels included). It's a single active
language for the daemon, not a per-browser preference; already-generated
conversation text and a running session's baked system prompt stay as they were.
New sessions and freshly rendered UI pick up the change.

Two halves cooperate:

- **Server-originated text** (surface/pane titles, settings labels and options)
  is localized server-side with `i18n.T` before it reaches the wire, to the
  daemon's active language. Conversation content is already localized by the core
  agent. The client renders all of this verbatim.
- **Client-owned chrome** (buttons, placeholders, tooltips, empty states) is
  translated in the browser through a small English-as-key `t()` / `tn()` layer
  (`client/src/i18n.ts`, mirroring the Go `i18n` package; English source strings
  are the keys, plurals via `Intl.PluralRules`, and English is the implicit
  fallback).
  The daemon advertises its locale in the ctrlproto hello (`Hello.locale`); the
  PWA shows its bundled catalog immediately, then fetches the daemon's **effective**
  catalog (`i18n.catalog`) and overlays it, so operator edits win.

### Translating the web panel

The web strings are a first-class catalog in the `terva locale` workflow, the
same one used for the TUI and CLI (see [docs/localization.md](localization.md)).
They live in their own `web/` file, English-as-key like the main UI catalog:

```
terva locale init <lang>      # seeds web/<lang>.json alongside the rest
# … edit $TERVA_HOME/locales/web/<lang>.json …
# reload the browser → the daemon re-reads the overlay and serves it; the panel
# re-renders in your edits (server-rendered titles refresh too, via re-Configure)
terva locale export <lang>    # writes web/<lang>.export.json to PR back
```

`terva locale list` / `diff` report web coverage (`[web done/total]`); `validate`
checks a `web/<lang>.json` against the web reference. Because the client fetches
the effective (embedded ⊕ `$TERVA_HOME/locales/web` overlay) catalog on every
connect, a browser reload is the whole check loop, with no daemon restart.

Shipped translations live in `packages/i18n/locales/web/<lang>.json`; the client
bundle is a build-time mirror (regenerated by `just web-build`) for offline /
first-paint. The client's English reference (`web/en.json`) is extracted from the
`t()`/`tn()` calls by `scripts/i18n-extract.mjs`, the client-side twin of
`cmd/terva-i18n-lint`. Finnish ships as the reference translation.
