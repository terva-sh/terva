# Deploying terva bots as services

`terva bot run` is a foreground process by design: logs on stdout,
dies with the terminal, `^C` stops it cleanly. That is exactly the
shape a service supervisor wants to own. This page covers running
persistent, resuming connector agents under systemd; the example unit
files live in [`examples/deploy/systemd/`](../examples/deploy/systemd/).

Two rules before anything else:

- **Supervise `bot run`, never `bot start`.** `bot start` is the
  self-daemonizing convenience for ad-hoc shells (managed by
  `bot stop`/`status`/`logs`); stacking it under systemd gives you two
  process managers fighting over one child.
- **`bot setup` is a one-time interactive step** (it prompts for the
  service token on your tty) — run it as the service's user, with the
  service's `TERVA_HOME`, before enabling the unit. Credentials land
  in `$TERVA_HOME/<service>.json` (mode 0600), never in the unit file.

## User-level unit (recommended)

[`terva-bot@.service`](../examples/deploy/systemd/terva-bot@.service)
is a template unit — the instance name is the connector, so one file
serves `terva-bot@discord`, `terva-bot@telegram`, or an extension
connector's name:

```bash
mkdir -p ~/.config/systemd/user ~/agents/discord
cp examples/deploy/systemd/terva-bot@.service ~/.config/systemd/user/
terva bot setup --connector discord      # one-time token prompt
systemctl --user daemon-reload
systemctl --user enable --now terva-bot@discord
loginctl enable-linger $USER             # keep it alive after logout
journalctl --user -u terva-bot@discord -f
```

Everything that shapes the agent is in the unit's `ExecStart` and two
settings around it:

- **`WorkingDirectory`** is the agent's workspace: what the built-in
  tools see, and where sessions bucket. One directory per bot keeps
  their worlds separate. It must exist before first start.
- **`--continue`** makes restarts resume the paired DM's conversation
  instead of forgetting it — the DM transcript persists message by
  message, so even a hard kill costs at most the in-flight turn.
- **Scoping flags are the security posture.** A bot admitted to group
  chats gives strangers reach to start turns, so give it the
  integrations that chat needs and nothing else:

  ```
  terva bot run --connector discord --continue --approval ask \
      --no-workspace-tools --extensions calendar --no-mcp
  ```

  `--extensions`/`--mcp` are per-run allowlists, `--no-workspace-tools`
  drops host filesystem/shell access, and `--approval ask` routes
  tool confirmations to you over the chat itself (buttons on Discord)
  instead of defaulting to yolo. See
  [connectors.md](connectors.md) for the full posture discussion.

Different bots want different postures — that's just different
`ExecStart` lines. Copy the template to a concrete name
(`terva-bot-planning.service`) when one connector needs settings the
shared template shouldn't carry.

## System-level unit

[`terva-bot-system.service`](../examples/deploy/systemd/terva-bot-system.service)
runs the bot as a dedicated `terva` user with
`TERVA_HOME=/var/lib/terva`, for shared hosts where the bot should
exist independent of any login session. Same `ExecStart` axes.

One honest note on unit hardening: the agent **runs commands** — that
is its job — so the meaningful sandbox is terva's own posture
(`--jail`, `--no-workspace-tools`, the allowlists), not systemd
directives tightened until the tools break. The example keeps the
process off the system directories (`ProtectSystem=full`,
`ProtectHome=true`) and writable only in its own state dir.

## Restart policy

The connector already rides out transient network trouble internally
(gateway reconnects, its own crash/respawn budget for connector
extensions) and exits only when something is *permanently* broken — a
bad token, a revoked bot, an exhausted budget. So:

- `Restart=on-failure` with `RestartSec=10` restarts the real
  failures;
- `StartLimitIntervalSec=300` / `StartLimitBurst=5` stops a dead
  credential from hot-looping — after five failures in five minutes
  the unit stays down until you `systemctl reset-failed` it.

A clean stop (`systemctl stop`, which sends SIGTERM) shuts the
extension subprocesses down gracefully and exits 0; it does not count
against the restart budget.

## Logs

The bridge logs to stdout → `journalctl`. Extension subprocess stderr
goes to per-extension files under `$TERVA_HOME/logs/`, and
`terva bot logs --connector <name>` tails the daemon-mode log file
when you're not under systemd.

## Container

Each release publishes a multi-arch image (linux amd64 + arm64):
**`ghcr.io/terva-sh/terva`**, tagged per version and `latest`. It's
alpine with a real userland (bash, git, curl — a coding agent needs a
shell), runs as the unprivileged `terva` user, and keeps all state on
two volumes:

- **`/data`** — `TERVA_HOME`: config, credentials (bot tokens, 0600),
  sessions, extensions, logs. Mount it to persist across container
  replacements.
- **`/work`** — the agent's workspace (its working directory).

One-time interactive setup into the volume, then run detached with
docker as the supervisor (same reasoning as systemd: the container
runs the foreground `bot run`, restart policy lives outside):

```bash
docker volume create terva-data
docker run -it --rm -v terva-data:/data ghcr.io/terva-sh/terva \
    bot setup --connector discord

docker run -d --name terva-bot --restart unless-stopped \
    -v terva-data:/data -v "$HOME/agents/discord":/work \
    -e ANTHROPIC_API_KEY \
    ghcr.io/terva-sh/terva \
    bot run --connector discord --continue --approval ask \
    --no-workspace-tools --extensions calendar --no-mcp

docker logs -f terva-bot
```

Provider credentials: a headless container can't run the browser
`/login` flow, so either pass an API key env var (as above) or run one
interactive session against the volume and log in there — the token
lands in `/data/auth.json` and every later container reuses it.

The same image serves every run mode, not just bots — the entrypoint
is plain `terva`:

```bash
# one-shot prompt against a mounted project
docker run -it --rm -v terva-data:/data -v "$PWD":/work \
    ghcr.io/terva-sh/terva -p "summarize this repo"

# the full interactive TUI (it's just a terminal program)
docker run -it --rm -v terva-data:/data -v "$PWD":/work \
    ghcr.io/terva-sh/terva
```

### Extensions and MCP in the container

Both live on the `/data` volume — installed extensions under
`/data/extensions/`, MCP servers in `/data/config.json` — so they
persist across container replacements like everything else. But both
are **subprocesses terva executes**, and that's where containers
change the rules:

- **Don't mount your host `TERVA_HOME` into the container.** Your
  host-installed extensions are binaries for your host OS/arch
  (darwin/arm64 on a Mac) and won't execute on linux. Give the
  container its own volume and install into it *from inside*:

  ```bash
  docker run -it --rm -v terva-data:/data ghcr.io/terva-sh/terva \
      ext install <pack-or-source>
  # or mount an extension's source and install from the mount:
  docker run -it --rm -v terva-data:/data -v "$PWD/my-ext":/mnt/my-ext \
      ghcr.io/terva-sh/terva ext install /mnt/my-ext
  ```

  For iterating on an extension, the usual `--ext /mnt/my-ext` flag
  works on a mounted directory the same way.

- **The base image is deliberately lean**: bash, git, curl — no
  python, node, or uv. Extensions shipped as **static linux binaries**
  (CGO-free Go, matching the image arch) run as-is; **script
  extensions** (Python/TypeScript) and the common **`npx`/`uvx`
  MCP servers** need their interpreter in the image. The intended
  pattern is a derived image, not a fatter base — see
  [`examples/deploy/docker/Dockerfile.tools`](../examples/deploy/docker/Dockerfile.tools):

  ```dockerfile
  FROM ghcr.io/terva-sh/terva
  USER root
  RUN apk add --no-cache python3 py3-pip nodejs npm uv
  USER terva
  ```

  One `docker build -t my-terva .` and every `npx`/`uvx` MCP server
  and script extension works; your MCP config on the volume needs no
  changes. (Alpine is musl-based: a rare glibc-linked prebuilt binary
  won't run — prefer static builds or add the interpreter.)

- **Scope what you ship.** The container boundary composes with —
  never replaces — terva's own posture: `--extensions`/`--mcp`
  allowlists and `--no-workspace-tools` still say what the *agent*
  may reach, which matters exactly as much inside the container as
  out.

## macOS

There is no launchd example yet; `terva bot start` (the built-in
daemonizer, per-connector pid files, `bot stop`/`status`/`logs`) is
the practical answer on a Mac today — or the container above.
