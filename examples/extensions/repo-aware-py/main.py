#!/usr/bin/env python3
# repo-aware-py — the Python twin of examples/extensions/repo-aware (Go) and
# repo-aware-ts (TypeScript). Raw protocol, stdlib only, no SDK.
#
# Demonstrates the RESPONSIBLE per-session context/tool pattern from
# docs/extensions.md -> "Responsible use: context & tools":
#
#   - register one read-only tool (repo_root) and subscribe to session_start
#   - on each session_start (which re-fires on a /cd), look at the cwd and:
#       inside a git repo  -> publish a context block + restore the tool
#       outside a repo      -> clear the context block + withdraw the tool
#
# The context block and the tool set live in the model's CACHED prompt prefix,
# so they're changed only at a session boundary (session_start), never per
# turn. The tool half is gated on the host's protocol_version >= 4
# (set_withdrawn_tools is protocol 4); an older host ignores the frame and the
# tool simply stays visible. The host also no-ops an unchanged set, so we
# re-assert the decision on every session_start without tracking prior state.
#
# The Go SDK hides all of this behind OnSession + the Session handle; this is
# what that looks like on the bare wire.

import json
import os
import sys

NAME = "repo-aware-py"
VERSION = "1.0.0"

# Host facts, refreshed during the session.
state = {"protocol_version": 0, "cwd": ""}


def emit(obj):
    # stdout is the wire — one JSON object per line, always flushed so a
    # frame never sits in a buffer on a slow handler.
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def log(msg):
    # stderr is captured to $TERVA_HOME/logs/ext-<name>.log; never print
    # debug to stdout, which would corrupt the protocol.
    sys.stderr.write(f"[{NAME}] {msg}\n")
    sys.stderr.flush()


def git_root(path):
    """Walk up from path looking for a .git entry; '' if none is found."""
    cur = path
    while cur:
        if os.path.exists(os.path.join(cur, ".git")):
            return cur
        parent = os.path.dirname(cur)
        if parent == cur:  # reached the filesystem root
            return ""
        cur = parent
    return ""


def send_tool_result(call_id, text, is_error=False):
    result = {"type": "tool_result", "id": call_id, "content": [{"type": "text", "text": text}]}
    if is_error:
        result["is_error"] = True
    emit(result)


def apply_for_cwd():
    """Decide context + tool visibility for the current cwd. Called on each
    session_start; re-sending the same decision is a free host-side no-op."""
    root = git_root(state["cwd"])
    if root:
        emit({"type": "refresh_context",
              "text": f"The workspace is a git repository rooted at {root}. "
                      "Use the repo_root tool if you need that path again."})
        if state["protocol_version"] >= 4:
            emit({"type": "set_withdrawn_tools", "tools": [], "all": False})  # restore
    else:
        emit({"type": "refresh_context", "text": ""})  # nothing repo-specific here
        if state["protocol_version"] >= 4:
            emit({"type": "set_withdrawn_tools", "all": True})  # hide the useless tool


# ---- handshake + registration ----

emit({"type": "hello", "name": NAME, "version": VERSION, "capabilities": ["tools", "events"]})
emit({"type": "register_tool", "name": "repo_root",
      "description": "Report the git repository root for the current workspace.",
      "schema": {"type": "object", "properties": {}},
      "read_only": True})  # declared honestly — it only reads a path
# Must subscribe to receive session_start (the Go SDK's OnSession does this for you).
emit({"type": "subscribe", "events": ["session_start"]})
emit({"type": "ready"})

# ---- frame loop ----

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        frame = json.loads(line)
    except json.JSONDecodeError as err:
        log(f"malformed frame: {err}")
        continue

    t = frame.get("type")
    if t == "hello_ack":
        state["protocol_version"] = frame.get("protocol_version", 0)
        state["cwd"] = frame.get("cwd", "")
        log(f"connected (protocol {state['protocol_version']}, cwd={state['cwd']})")
        # The host fires session_start after the handshake, so the first
        # decision happens there — the canonical boundary — not here.
    elif t == "event" and frame.get("event") == "session_start":
        # cwd refreshes on every session_start (a /cd re-fires it); a session
        # close carries no cwd, so keep the last known one.
        if frame.get("cwd"):
            state["cwd"] = frame["cwd"]
        apply_for_cwd()
    elif t == "tool_call":
        if frame.get("name") == "repo_root":
            root = git_root(state["cwd"])
            # Belt-and-suspenders: the tool is withdrawn outside a repo, but a
            # stale call from earlier context still refuses gracefully.
            send_tool_result(frame["id"], root or "not inside a git repository", is_error=not root)
        else:
            send_tool_result(frame["id"], f"unknown tool {frame.get('name')}", is_error=True)
    elif t == "shutdown":
        emit({"type": "shutdown_ack"})
        break
    # other frames are ignored
