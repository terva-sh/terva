// Minimal Node client for the `terva rpc` JSON protocol.
//
// Usage:
//   node terva-client.js "fix the failing test"
//
// Spawns `terva rpc`, sends one prompt, prints assistant text as it
// streams, and exits when the turn finishes. Pure stdlib — no
// dependencies. See docs/rpc.md for the full schema.
"use strict";

const { spawn } = require("node:child_process");
const readline = require("node:readline");
const { randomBytes } = require("node:crypto");

class TervaClient {
  constructor(...flags) {
    this.proc = spawn("terva", ["rpc", ...flags], {
      stdio: ["pipe", "pipe", "inherit"],
      env: process.env,
    });
    this.rl = readline.createInterface({
      input: this.proc.stdout,
      crlfDelay: Infinity,
    });
  }

  send(command) {
    if (!command.id) command.id = randomBytes(4).toString("hex");
    this.proc.stdin.write(JSON.stringify(command) + "\n");
    return command.id;
  }

  async *events() {
    for await (const line of this.rl) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        yield JSON.parse(trimmed);
      } catch {
        // ignore garbled lines
      }
    }
  }

  close() {
    try {
      this.proc.stdin.end();
    } catch {}
  }
}

async function main() {
  const prompt = process.argv.slice(2).join(" ");
  if (!prompt) {
    console.error("usage: node terva-client.js <prompt>");
    process.exit(2);
  }

  const client = new TervaClient();

  const token = process.env.TERVACORE_RPC_TOKEN;
  if (token) client.send({ type: "hello", token });

  client.send({ type: "prompt", message: prompt });

  let exitCode = 0;
  try {
    for await (const ev of client.events()) {
      switch (ev.type) {
        case "text_delta":
          process.stdout.write(ev.delta || "");
          break;
        case "tool_call":
          process.stderr.write(
            `\n[tool] ${ev.name}(${JSON.stringify(ev.args || {})})`,
          );
          break;
        case "tool_result":
          if (ev.is_error) process.stderr.write("\n[tool error]");
          break;
        case "usage": {
          const cum = ev.cumulative || {};
          process.stderr.write(
            `\n[usage] cum input=${cum.input} output=${cum.output} cost=$${(cum.cost_usd || 0).toFixed(4)}`,
          );
          break;
        }
        case "error":
          process.stderr.write(`\n[error] ${ev.message}\n`);
          exitCode = 1;
          break;
        case "done":
          process.stdout.write("\n");
          client.close();
          process.exit(exitCode);
      }
    }
  } finally {
    client.close();
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
