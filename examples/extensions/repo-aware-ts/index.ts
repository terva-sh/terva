// repo-aware-ts — the TypeScript twin of examples/extensions/repo-aware (Go).
//
// Raw protocol, no SDK, no build step (runs via `npx -y tsx ./index.ts`).
// Demonstrates the RESPONSIBLE per-session context/tool pattern from
// docs/extensions.md → "Responsible use: context & tools" in a language that
// talks to the wire directly:
//
//   - register one read-only tool (repo_root) and subscribe to session_start
//   - on each session_start (which re-fires on a /cd), look at the cwd and:
//       inside a git repo  -> publish a context block + restore the tool
//       outside a repo      -> clear the context block + withdraw the tool
//
// The context block and the tool set live in the model's CACHED prompt
// prefix, so they're changed only at a session boundary (session_start),
// never per turn. The tool half is gated on the host's protocol_version >= 4
// (set_withdrawn_tools is protocol 4); an older host ignores the frame and
// the tool simply stays visible. The host also no-ops an unchanged set, so we
// re-assert the decision on every session_start without tracking prior state.
//
// The Go SDK hides all of this behind OnSession + the Session handle; this
// file is what that looks like on the bare wire.

import { createInterface } from "node:readline";
import { stderr, stdin, stdout } from "node:process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";

const NAME = "repo-aware-ts";
const VERSION = "1.0.0";

type Frame = { type: string; [k: string]: unknown };

function send(frame: Frame): void {
  stdout.write(JSON.stringify(frame) + "\n");
}

function log(msg: string): void {
  // stderr is captured to $TERVA_HOME/logs/ext-<name>.log; stdout is the wire.
  stderr.write(`[${NAME}] ${msg}\n`);
}

// Host facts: protocol version (for feature-gating) learned at the handshake,
// and the working directory, refreshed on every session_start (incl. /cd).
let protocolVersion = 0;
let cwd = "";

// gitRoot walks up from dir looking for a .git entry; "" if none is found.
function gitRoot(dir: string): string {
  let cur = dir;
  while (cur) {
    if (existsSync(join(cur, ".git"))) return cur;
    const parent = dirname(cur);
    if (parent === cur) return ""; // reached the filesystem root
    cur = parent;
  }
  return "";
}

// Decide context + tool visibility for the current cwd. Called from each
// session_start. Re-sending the same decision is a free host-side no-op, so
// we don't bother tracking the previous state.
function applyForCwd(): void {
  const root = gitRoot(cwd);
  if (root) {
    send({
      type: "refresh_context",
      text:
        `The workspace is a git repository rooted at ${root}. ` +
        `Use the repo_root tool if you need that path again.`,
    });
    if (protocolVersion >= 4) {
      send({ type: "set_withdrawn_tools", tools: [], all: false }); // restore
    }
  } else {
    send({ type: "refresh_context", text: "" }); // nothing repo-specific here
    if (protocolVersion >= 4) {
      send({ type: "set_withdrawn_tools", all: true }); // hide the useless tool
    }
  }
}

// ---- handshake + registration ----

send({ type: "hello", name: NAME, version: VERSION, capabilities: ["tools", "events"] });

send({
  type: "register_tool",
  name: "repo_root",
  description: "Report the git repository root for the current workspace.",
  schema: { type: "object", properties: {} },
  read_only: true, // declared honestly — it only reads a path
});

// We must subscribe to receive session_start. (The Go SDK's OnSession does
// this for you; on the raw wire you ask for it explicitly.)
send({ type: "subscribe", events: ["session_start"] });

send({ type: "ready" });

// ---- frame loop ----

const rl = createInterface({ input: stdin, crlfDelay: Infinity });

rl.on("line", (line: string) => {
  let frame: Frame;
  try {
    frame = JSON.parse(line) as Frame;
  } catch (err) {
    log(`malformed frame: ${err}`);
    return;
  }

  switch (frame.type) {
    case "hello_ack":
      protocolVersion = (frame.protocol_version as number) ?? 0;
      cwd = (frame.cwd as string) ?? "";
      log(`connected (protocol ${protocolVersion}, cwd=${cwd})`);
      // The host fires session_start after the handshake, so the first
      // decision happens there — the canonical boundary — not here.
      break;

    case "event":
      if (frame.event === "session_start") {
        // cwd refreshes on every session_start (a /cd re-fires it); a
        // session close carries no cwd, so keep the last known one.
        if (typeof frame.cwd === "string" && frame.cwd) cwd = frame.cwd;
        applyForCwd();
      }
      break;

    case "tool_call":
      if (frame.name === "repo_root") {
        const root = gitRoot(cwd);
        // Belt-and-suspenders: the tool is withdrawn outside a repo, but a
        // stale call from earlier context still refuses gracefully.
        sendToolResult(frame.id as string, root || "not inside a git repository", !root);
      } else {
        sendToolResult(frame.id as string, `unknown tool ${frame.name}`, true);
      }
      break;

    case "shutdown":
      send({ type: "shutdown_ack" });
      rl.close();
      break;

    default:
      log(`ignoring frame: ${frame.type}`);
  }
});

rl.on("close", () => process.exit(0));

function sendToolResult(id: string, text: string, isError = false): void {
  send({ type: "tool_result", id, content: [{ type: "text", text }], is_error: isError || undefined });
}
