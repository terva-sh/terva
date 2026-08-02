# 05 — One core, many front ends

terva presents the same agent through a terminal UI, a browser panel, an
immersive browser app, an editor protocol, chat platforms, several headless
modes, an embedding library, and a session replayer. This chapter is about how
that stays one system instead of nine.

## The failure mode being avoided

The obvious way to add a second front end is to write a second front end: give
it its own copy of the agent, its own message queue, its own approval prompt,
its own idea of when a session is finished. It works immediately and rots
immediately. Six months later the terminal supports a feature the browser does
not, the two disagree about what "cancel" means, and a permission check exists
in one and not the other.

The observation that drove terva's design — and independently drove Codex,
OpenCode, Goose, Gemini CLI and OpenHands to the same place — is that **the
split does not belong between front ends. It belongs between the engine and all
of them at once.**

So there is one **control plane**: a typed protocol of verbs (things a client
asks for) and events (things that happened). Everything interactive is a client
of it.

## The daemon owns the state

Behind the protocol sits the **workspace** — the daemon-side owner of
everything that has to be authoritative in exactly one place:

- the live sessions and the agents running in them
- the pending message queue for each session
- pending approvals, and who has answered them
- the surfaces (see below)
- session listing, archiving, grouping, search
- background subagents and their inboxes

A client asks; the workspace decides and announces. That direction is the whole
design. When a user types while the agent is busy, the message is queued
*daemon-side*, and every connected client learns about it through a queue-updated
event — so two people watching the same session see the same pending work, and
closing a laptop does not delete a queued message.

## The terminal UI is a network client

The sharpest consequence, and the one that surprises people: **the terminal
interface holds no agent.** No model client, no credential manager, no
permission gate, no queue. It parses keystrokes, sends verbs, renders events.

That is what makes three otherwise-unrelated capabilities the same code path:

- **In-process** — the ordinary case. `terva` starts, hosts the workspace
  itself, and connects to it over an in-memory carrier. The user sees a normal
  terminal app; internally it is a client that happens to share an address
  space with its server.
- **Attached** — `terva attach` connects the same UI to a *different* process's
  workspace over a socket. Multiple terminals can attach to one running agent.
- **Replay** — a recorded session file is served through the same protocol with
  no agent behind it at all. The UI cannot tell the difference, which is why
  replay renders exactly like the live run did rather than approximately.

A **carrier** is just which of those transports is in use: in-memory, a socket
(TCP or Unix domain), or a file being replayed. It is the front end's entire
backend contract.

The discipline that keeps this honest is a direction rule: **the UI package may
not import the application wiring.** It cannot reach around the protocol even
by accident, because the type it would need to reach for is not in scope. A
composition root at the top binds the workspace to the UI's carrier seam, and
nothing else knows about both.

## Surfaces: server-rendered panes

Some UI is genuinely shared — the settings pane, the task list, the permissions
view, the usage breakdown, the lore inspector. Rebuilding each of those in the
terminal and again in the browser is the divergence problem in miniature.

So they are **surfaces**: the daemon builds a structured description of the
pane's content and pushes it to clients on change. The terminal draws it with
box characters, the browser draws it with DOM, and neither decides what is in
it. Adding a field to a surface makes it appear in both.

This is not a universal answer — anything genuinely native to a medium (the
text editor, the diff renderer, the immersive Stage) stays with the client that
owns it. Surfaces are for the panes whose content is *information the daemon
has*.

## Where headless modes fit

Not everything should pay for a control plane. One-shot print mode, the NDJSON
stream, the embedding SDK and the chat bots bind the engine directly: no
daemon, no protocol, no surfaces. They share the loop and the persistence
layer, and differ only in rendering.

This is a deliberate two-tier arrangement rather than an inconsistency. The
control plane buys multi-client session ownership, live surfaces, and detach/
reattach — real capabilities with real complexity. A pipeline that runs one
prompt and exits needs none of it, and making it pay would be the same mistake
in the other direction.

The cost of the split is that **host capability becomes an asymmetry you have
to state.** Which host wires which tools, which gets extensions, which gets
MCP, which gets the full protocol surface — that is a matrix, and an emergent
one is a bug farm. Ours is written down and pinned by tests, because the
alternative turned out to be discovering that one host had no permission gate
at all.

## Negotiation, and how a feature ships dark

Clients and servers can be different versions — an old `attach` against a new
daemon, a browser tab held open across an upgrade. So the connection opens with
a handshake in which the server states which optional features it supports.

There is a trap here we walked straight into. A feature was fully built on the
server, fully requested by the client, and never *advertised* in the handshake —
so negotiation silently failed and the feature was dark in production for
weeks, while every code path involved looked correct in review.

The lesson generalizes past protocols: **when a capability is announced in one
place and implemented in another, the announcement is the part with no natural
reader.** The fix was not the missing constant; it was a test that asserts the
advertisement, so the class cannot recur.

## Three wires, not one

Honesty about a wart: terva has three protocol surfaces, not one. The control
plane, a JSON-RPC embedding wire, and the editor protocol. They share the event
vocabulary and answer the approval question three different ways.

There is a defensible reason — two of them are *someone else's* protocol, and
adapting to a standard you do not own is not the same as inventing a second one
— but the duplication is real, and it is the largest structural tension in this
part of the system. It is recorded as such rather than explained away; see
[chapter 08](08-lessons.md) on the difference.

---

*Implementation: the protocol, the daemon, the carriers and the browser apps
live under `packages/agent` in `ctrlproto/`, `workspace/`, `web/` and `modes/`.
The user-facing references are [controllers.md](../controllers.md) (the protocol),
[web.md](../web.md) (the browser panel) and [cli.md](../cli.md) (which run mode to
use when).*
