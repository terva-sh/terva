# HAProxy in front of terva web

Two ways to reverse-proxy a terva web daemon behind HAProxy for TLS and a
hostname. Pick by how terva is bound:

- **[`haproxy-tcp.cfg`](haproxy-tcp.cfg)** — terva on a loopback TCP port
  (`127.0.0.1:8730`). Simpler, and the **recommended default**. HAProxy
  terminates TLS and forwards to the port; terva enforces its own
  `--web-token-file` bearer token.

- **[`haproxy-unix.cfg`](haproxy-unix.cfg)** — terva on a filesystem socket, no
  TCP port at all. Keeps terva off every port, but the socket's
  ownership/permissions and HAProxy's chroot add constraints (spelled out at the
  top of the file). Best when HAProxy and terva share a host and you can
  group-share the socket.

Both are grounded in a real deployment: HAProxy on a tailnet interface,
TLS-terminating for one hostname, proxying to a loopback terva daemon with
bearer auth. Replace `terva.example`, the bind address, and the cert path.

## The WebSocket

Either way, the panel holds one long-lived WebSocket that can be silent for
minutes while the agent thinks. Set `timeout tunnel` (both files do, `1h`) or
the proxy cuts it on the 50s `timeout server` and the panel reconnects on a
fixed interval, forever. terva's 20s ping masks a 50s timeout — don't rely on
it. See docs/web.md "Behind a reverse proxy".

## Auth

TLS plus a network boundary (bind an overlay/tailnet address, not `0.0.0.0`)
keep strangers off; the daemon's bearer token gates whoever reaches it. Anything
HAProxy forwards reaches a daemon that can run `bash` as you — treat the whole
path as privileged. For real per-user SSO, front HAProxy with an identity proxy
(oauth2-proxy, an Authentik outpost, or SPOE) that sets a trusted header, and
start terva with `--web-auth-header` (see docs/web.md "Auth"). Plain HAProxy
does not do nginx-style `auth_request` on its own.

## The unix-socket pairing (`haproxy-unix.cfg`)

Serve the backend socket group-readable via socket activation, as a **system**
unit, so HAProxy (running as group `haproxy`) can open it without terva's 0600
default getting in the way:

```ini
# /etc/systemd/system/terva-web.socket
[Socket]
ListenStream=/run/terva/terva.sock
SocketMode=0660
SocketGroup=haproxy        # the group HAProxy runs as
[Install]
WantedBy=sockets.target
```

```ini
# /etc/systemd/system/terva-web.service  (drop-in sketch)
[Service]
# Ensure /run/terva exists and is traversable before the socket binds.
RuntimeDirectory=terva
ExecStart=/usr/local/bin/terva web --allow-restart
ExecReload=/bin/kill -HUP $MAINPID
```

terva inherits the bound fd (`--web-addr` is ignored under `LISTEN_FDS`), so the
socket keeps the `0660`/group systemd gave it. The per-user socket units in
`../systemd/` use `%t/terva.sock` (`0600`) instead — correct for a same-user
`terva attach`, but not reachable by a system HAProxy. See docs/web.md "systemd
socket activation".

## Validate

```bash
haproxy -c -f haproxy-tcp.cfg      # check the config parses
```
