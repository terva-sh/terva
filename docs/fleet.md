# Fleet: one browser, many machines

A fleet is one `terva web` panel showing the sessions of terva daemons running
on other machines. The machine with the browser is the **hub**. Every other
machine is a **member**.

Members dial the hub. The hub never dials a member. That one rule decides most
of what follows: a member needs no inbound port, no public address, and no
certificate, so a laptop behind NAT can join a fleet. The hub is the only
machine anybody has to be able to reach.

The reasoning is recorded in decision 0014, "fleet members dial in", in the
development tree.

**Status, 2026-09-13: run as two processes on one machine, not yet across two
machines or a tunnel.** A hub and a member built from trunk met over a loopback
socket: the member checked in, `sessions.list` on the hub returned the member's
session under its origin, and the hub resumed after the member was killed and
restarted. The proof is recorded on `TKT-01M23ZB6P`. The two-machine run through
the tunnel below is `TKT-01M2EP9DQ`, so treat that part as untried. Nothing here
is on by default: `terva web` without `--fleet-addr` opens no extra socket and
behaves exactly as it always has.

## The flags

| Flag | Where | What |
|---|---|---|
| `--fleet-addr ADDR` | `terva web` | Open a second listener, separate from `--web-addr`, for members to check in on |
| `--hub URL` | `terva member` | The hub's member endpoint, `ws://` or `wss://` |
| `--origin NAME` | `terva member` | This machine's name in the fleet |
| `--fleet-token-file PATH` | both | Read the shared fleet bearer from a file |

The hub and the member endpoints are deliberately two listeners. The browser
talks to `--web-addr`; members talk to `--fleet-addr`. You can expose one
without the other.

`terva member` needs no web build tag. A member serves its workspace to the hub
and does not serve a browser, so a lean binary with no web client can still join
a fleet.

## The hub only binds loopback or a unix socket

`--fleet-addr 0.0.0.0:8081` is refused, and so is a tailnet address. The member
endpoint accepts loopback and `unix:/path` only.

The reason is the token. One shared bearer names the **whole fleet** rather than
one machine, so anything that reaches the port and holds that token is every
member at once. Until fleet identity replaces the shared bearer, the endpoint
stays off the network.

A member on another machine therefore reaches the hub through a tunnel. That is
not a workaround, it is the supported path.

## The token never goes in argv

There is no `--fleet-token` flag that takes a value, and there will not be one.
`ps` and `/proc/cmdline` are readable by any local user, and this token names
every machine in the fleet. Put it in a file:

```bash
umask 077
openssl rand -hex 32 > ~/.config/terva/fleet-token
```

Both ends read the same file path with `--fleet-token-file`. Under systemd, use
`LoadCredential=` and point the flag at the credential path.

## A worked two-machine setup

Call the machines `hub-host` and `neot`.

**On the hub**, start the panel with a member endpoint beside it:

```bash
terva web \
  --web-addr 127.0.0.1:8080 \
  --fleet-addr 127.0.0.1:8081 \
  --fleet-token-file ~/.config/terva/fleet-token
```

**From the member**, open a tunnel so the hub's member endpoint appears on the
member's own loopback:

```bash
ssh -N -L 8081:127.0.0.1:8081 hub-host
```

That listens on `neot`'s 127.0.0.1:8081 and forwards each connection to
`hub-host`, which delivers it to its own 127.0.0.1:8081 where the hub is
listening. Leave it running; a systemd unit or `autossh` is the durable form.

It is `-L` and not `-R`. The member is reaching **into** the hub, so it listens
locally and forwards to a port resolved on the hub. `-R` is the other direction:
it opens a port on the hub that forwards back to the member, which does not help
and collides with the hub's own listener.

If the member cannot ssh out but the hub can ssh in, run the mirror image from
the hub instead, and the member's `--hub` flag stays the same:

```bash
ssh -N -R 8081:127.0.0.1:8081 neot
```

**On the member**, dial through the tunnel:

```bash
terva member \
  --hub ws://127.0.0.1:8081 \
  --origin neot \
  --fleet-token-file ~/.config/terva/fleet-token
```

Open `http://127.0.0.1:8080` on the hub. `neot`'s sessions appear in the list
alongside the hub's own.

### Origins name the machine

`--origin neot` prefixes every session id the hub shows, so that machine's
sessions list as `neot/<id>`. Pick something stable and short; it is how you
will tell two machines apart in the panel.

`local` is reserved for the hub's own workspace.

## What a fleet does today, and what it refuses

Check-in is **read-only aggregation**. You can see a member's sessions and read
them: the transcript, context, usage, surfaces, models.

Anything that would **change** a member's session is refused, with an error
naming the origin. Sending a prompt, cancelling a turn, switching a model, or
starting a session on a member all refuse today. Driving a member is the
`fleet-control` milestone.

The refusal is an allowlist rather than a blocklist, so a capability added later
refuses until somebody deliberately promotes it. That is on purpose: the failure
worth preventing is a command quietly reaching a machine nobody meant to expose.

Hub-scoped actions apply to the hub itself. Restarting from the panel restarts
the hub, not the fleet.

## Keeping the connection alive

A fleet connection is silent whenever nobody is watching that member, which is
most of the time, and a middlebox reads silence as death. The tunnel is one more
hop that can time out without telling either end.

So the hub pings every 20 seconds, and a connection that goes 65 seconds without
any traffic is reaped. A reaped member re-dials and rejoins on its own; a member
that is gone stops appearing in the session list rather than lingering as a dead
tile.

Both ends carry that deadline, and they catch different failures. The hub's
catches a member whose machine lost power, where nothing is left on the far end
to notice. The member's catches the tunnel dying underneath two live processes,
which the member would otherwise never see, because it only re-dials when a
connection actually errors.

If you put something more aggressive than a plain `ssh -R` in the path, the same
advice applies as for the browser socket. A client that reconnects on a **fixed
interval**, forever, is that middlebox's idle timeout and not a terva bug, and
the interval is its value. See
[behind a reverse proxy](web.md#behind-a-reverse-proxy-the-websocket-is-a-long-quiet-connection).

## Hub and member under systemd on one host

The smallest fleet that runs unattended is a hub and one member on the same
machine, both as user units. It proves nothing about tunnels, but it puts the
carrier under real uptime: the hub restarts for a new build, the member redials,
and the panel keeps showing both.

The example units are in `examples/deploy/systemd/`. `terva-web.socket` and
`terva-web.service` are the hub; `terva-member@.service` is the member, with
the instance name as its origin.

```bash
mkdir -p ~/.config/systemd/user ~/.config/terva ~/fleet/terva
cp examples/deploy/systemd/terva-web.socket \
   examples/deploy/systemd/terva-web.service \
   examples/deploy/systemd/terva-member@.service ~/.config/systemd/user/
umask 077; openssl rand -hex 32 > ~/.config/terva/fleet-token
systemctl --user daemon-reload
systemctl --user enable --now terva-web.socket terva-member@terva
loginctl enable-linger $USER
```

Two facts decide the shape of the units.

**The fleet endpoint is a flag on the hub, never a second socket.** The daemon
adopts exactly one systemd activation socket, and that one is the browser
endpoint. Passing two `ListenStream=` lines is refused at startup. So the web
unit's `ExecStart` carries `--fleet-addr` and `--fleet-token-file`, as the
commented line in `terva-web.service` shows, and the token reaches it through
`LoadCredential=`. A `systemctl --user reload terva-web` re-execs the hub in
place: the browser socket is re-adopted, the fleet port is bound again, and
the member redials within its keepalive window.

**Hub and member can share a `TERVA_HOME` if their working directories
differ.** Sessions bucket per working directory, and the sweep of empty
transcripts that every daemon runs at boot looks only in its own bucket and
removes only transcripts with no message in them. A member serving a
different directory is never touched by the hub's sweep, and a session with
one message in it is never touched by any sweep. Sharing a home also means
the member inherits the provider credential the hub uses, so there is nothing
to seed on the member side.

Then upgrade both ends from the tree with one install and two reloads:

```bash
just install-dev                          # atomic rename over ~/.local/bin/terva
systemctl --user reload terva-web         # hub re-execs into the new build
systemctl --user restart terva-member@terva
```

Reload is a real restart with a short gap, and it interrupts an in-flight
turn, so do it between turns.

## Where the pieces live

- [`docs/web.md`](web.md) for the panel itself, its auth, and its deployment.
- [`docs/deploy.md`](deploy.md) for running terva as a systemd unit, which is
  what both ends of a real fleet want.
- Decision 0014, "fleet members dial in", for why members dial rather than the
  hub reaching out.
- The fleet single-pane plan for the roadmap: identity beyond a shared bearer,
  then driving a member rather than only reading it.

Both live in the development tree rather than this published set.
