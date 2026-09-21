# terva serve: one terva per person, on one host

`terva serve` runs a supervisor in front of many `terva web` daemons. Each
person who signs in gets their own terva: a separate process with its own
`TERVA_HOME`, its own sessions, its own MCP servers, and its own extensions. The
supervisor runs no agent itself. It authenticates the caller, decides whose
environment the caller reaches, starts that environment if it is not running,
and proxies the connection to it.

Everything a single daemon does is documented in [web.md](web.md). This page
covers what the supervisor adds on top: identity, enrolment, containment, and
the operator panel. It needs a build with `-tags terva_web`, which the release
binaries include.

## Start it

The supervisor refuses to start without a way to tell people apart. A bearer
token authenticates an operator, not a person: everyone holding it would resolve
to the same environment and read each other's work. So pick one of the two
identity sources before anything else.

- **Single sign-on.** Put a `web_oidc` block in the user-layer `config.json`
  under the supervisor's `$TERVA_HOME`, with a `role_map` that maps at least one
  identity-provider group to a terva role. The block and its fields are
  documented under [Single sign-on](web.md#single-sign-on). Then run
  `terva serve`.
- **A proxy that has already authenticated the caller.** Run
  `terva serve --web-auth-header X-Forwarded-User` behind it, and name the
  proxy with `--web-trusted-proxy` so a spoofed header from anywhere else is
  ignored.

A token may be set alongside either. The listener, proxy, and token flags are
`terva web`'s and mean the same thing here. See
[Web-mode flags](cli.md#web-mode-flags).

On start the supervisor prints where tenant homes and sockets live, how idle
environments are handled, which containment is in force, and where the operator
panel is.

## Choose a containment

A per-tenant `TERVA_HOME` is a routing decision, not a security boundary. A
tenant's agent runs shell commands, so two tenants under one uid could read each
other's homes. The `--containment` flag says how each tenant's process is
confined. The default is the mode that does not claim to isolate, rather than
the one that can.

| Mode | What it does | Who it serves |
|---|---|---|
| `none` (default) | Children share the supervisor's uid, separated only by `TERVA_HOME`. | Exactly one tenant. A second person is refused by name rather than placed beside the first. |
| `systemd` | One instance of a unit template per tenant. systemd allocates a transient uid and a persistent state directory for each. | As many people as sign in. |

To let a second person in, install the template and the polkit rule from
`examples/deploy/systemd/` as root, then start the supervisor with
`--containment systemd`:

```sh
cp examples/deploy/systemd/terva-tenant@.service /etc/systemd/system/
cp examples/deploy/systemd/terva-tenant@.socket /etc/systemd/system/
cp examples/deploy/systemd/49-terva-tenants.rules /etc/polkit-1/rules.d/
systemctl daemon-reload
terva serve --containment systemd
```

The supervisor holds no privilege to become another user. It asks systemd to
start `terva-tenant@<id>.socket` over a polkit grant scoped to that one
template, and systemd starts the service from that socket. The
`--containment-template` flag names a different template prefix. Keep the
prefix short: a Linux login name accepts 31 characters and a tenant id spends 18
of them.

The supervisor verifies the template rather than trusting it. systemd names a
`DynamicUser=` after the template unless the unit also sets `User=` per
instance, so a template with the first line and not the second runs every tenant
as one uid. Before starting a tenant, the supervisor asks systemd which user the
instance resolves to and refuses the tenant if the answer does not name that
instance. Editing the unit to something instance-less stops tenants starting,
loudly, rather than weakening the boundary quietly.

## How a person gets an environment

A successful sign-in that carries one of the supervisor's roles gets an
environment, created on first arrival. The supervisor keys the environment on
the identity provider's stable subject, never on the email or username. Every
identity provider lets people change those, and a rename would otherwise strand
an environment or hand it to whoever inherits the address.

A sign-in with no matching role is authenticated but unprovisioned. The refusal
names the roles that would work, so an operator debugging "why can't Ada get
in" does not have to guess. The roles are `owner`, `operator`, `member`, and
`viewer`, and what each may do is tabled under
[Single sign-on](web.md#single-sign-on). Map an identity-provider group to one
of them in `web_oidc.role_map`.

The supervisor also records such a sign-in against the enrolment, and acts on
nothing. An identity-provider outage, a mistyped group mapping, and a revoked
role look the same from here, so what accumulates is a dated fact for an
operator to weigh, never an automatic suspension.

## What is cleaned up, and what never is

Two kinds of cleanup exist, and they carry different risks.

An idle environment is **stopped**. After `--tenant-idle-timeout` with no
connection (default `30m`), the tenant's daemon exits. Nothing of theirs is
touched. The next request starts a fresh daemon over the same home. Pass
`--tenant-idle-timeout off` to keep environments up.

An environment is **never deleted**. There is no flag for it and no code path.
Deleting a home destroys somebody's work, so that action needs a suspend, a
grace period, and a person, and an environment that cannot be destroyed cannot
be destroyed by a bug.

## The operator panel

The panel is at `/supervisor` and needs the `owner` role. It lists every
environment and whether it is running. It also lists the people who
authenticated and were turned away, which is the only trace a newcomer whose
groups nobody mapped leaves anywhere. Suspend and resume live there. Both are
reversible, and neither deletes anything.

The same picture is on the wire at `/supervisor/ws` as the `tenants` method
group of [ctrlproto](controllers.md). No tenant's connection carries that group,
and no single `terva web` daemon serves it. That absence is the design: a
supervisor that served `tenants.*` on a tenant's connection and merely checked
for an admin role would be one bug away from tenant-to-admin escalation.

## Stage under the supervisor

Whether [Stage](web.md#stage-the-immersive-chatplay-surface) is available has
two halves that belong to different people. The supervisor's own `--web-stage`
flag, or `web_stage` in its config, decides whether `/stage/` exists on the
host. Each tenant's own `web_stage`, in their own home, decides whether they are
offered it. The supervisor cannot make that second decision for them, because
the containment that separates tenants is what stops it reading their config.
Without the host flag, a tenant who turns `web_stage` on is not offered a link,
rather than being pointed at a route that returns 404.

## Flags

| Flag | Description |
|---|---|
| `--containment none\|systemd` | How each tenant's process is confined. `none` serves one tenant. `systemd` serves many. Default `none`. |
| `--containment-template NAME` | The systemd unit template behind `--containment systemd`. Default `terva-tenant`, giving `terva-tenant@<id>.service` and `.socket`. |
| `--tenant-idle-timeout DUR` | Stop an environment with no connection for this long. Default `30m`. `off` keeps them up. Stops a process, never a home. |
| `--tenant-root DIR` | Where per-tenant homes live, one directory per person. Default `$TERVA_HOME/tenants`. Ignored under `systemd` containment, where the unit's `StateDirectory=` decides. |
| `--tenant-run-dir DIR` | Where per-tenant sockets live. Default `$XDG_RUNTIME_DIR/terva-tenants`, else `$TERVA_HOME/run`. Keep it short: the kernel caps a unix socket path at about 104 bytes and a tenant id spends 34. |
| `--web-stage` | Mount Stage at `/stage/` on this host. Off by default. |

The listener and auth flags (`--web-addr`, `--web-auth-header`,
`--web-trusted-proxy`, `--web-insecure-cidr`, and the token flags) are the
[web-mode flags](cli.md#web-mode-flags). `terva serve --help` prints all of
them.

## Where things live

| Under `--containment none` | Path |
|---|---|
| Tenant homes | `--tenant-root`, default `$TERVA_HOME/tenants/<id>` |
| Tenant sockets | `--tenant-run-dir` |
| Enrolment registry | the supervisor's `$TERVA_HOME` |

Under `--containment systemd`, each tenant's home is the unit's
`StateDirectory=`, which the example template places at `/var/lib/terva-<id>`,
and its socket is `/run/terva-<id>.sock` from the example `.socket` unit.

The design record for the supervisor is the daemon-access-auth proposal in the
development repository's `docs/proposals/`. The `tenants` method group is
specified in [controllers.md](controllers.md).
