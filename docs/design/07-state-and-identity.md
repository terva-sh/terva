# 07 — State and identity

Two kinds of state matter to a harness. **What happened** — the durable record
of a conversation, which must outlive the process. And **who the agent is** —
the identity, framing and background knowledge that shape how it behaves before
the user says anything.

## Sessions: an append-only file

A session is a file of newline-delimited JSON records, one per line, appended
as the run proceeds. Not a database. The reasons are worth stating because the
field is split on this — several peer harnesses use SQLite.

- **You can read it while it runs.** `tail -f` on a live agent is a debugging
  tool nobody has to build.
- **Append-only survives a crash.** A process killed mid-write loses at most
  the last line, and every line before it is still valid.
- **No dependency, no schema migration, no lock contention.** For a single-user
  harness, the queryability a database buys does not outweigh what it costs.
- **Diffable, greppable, mailable.** Ordinary tools work on it.

The record kinds are more varied than "the messages" suggests. A header row
carrying session metadata; the messages themselves; usage rows recording
tokens, cache reads and writes, and the model that produced them; compaction
checkpoints; amendments; stall and escalation records. Errors go to a separate
redacted sidecar file rather than into the transcript, so a session's history
is not interleaved with its diagnostics.

Sessions are bucketed by working directory, which means "resume what I was
doing in this project" needs no session ID and no picker.

### The rule that makes it robust

A file format is only as good as its treatment of records it does not
understand. The failure we shipped: a switch over record kinds with no default
arm, so an unrecognized row was silently skipped and the transcript folded
*wrong* rather than failing loudly. A newer version's file read by an older
binary produced a conversation missing pieces, with no error anywhere.

The correction generalizes past this codebase: **a format version, and an
explicit answer for the unknown case.** Refusing is fine. Warning is fine.
Silently continuing with partial data is not, because the reader has no way to
know it happened.

### Resume, replay, branch, export

Because the file is the source of truth, four capabilities fall out of one
design rather than being built separately:

**Resume.** Read the file, fold the rows into a transcript, continue. If there
is a compaction checkpoint, the checkpoint *is* the effective transcript. One
repair is needed on the way: a session interrupted between a tool call and its
result leaves an unmatched call that providers reject, so stub results are
synthesized to close them.

**Replay.** Serve a recorded file through the control plane with no agent
behind it ([chapter 05](05-one-core-many-front-ends.md)). The UI renders it
identically to the live run, because it is the same UI consuming the same
events.

**Branch.** Fork a session at a point and continue differently. It is a copy of
a prefix — cheap, because the format is a list.

**Export.** A portable form of a session, importable elsewhere.

The lesson from building the export path is one to steal: a **last-wins
timeline** read as if it were a single record loses everything that was
superseded. Metadata that is written repeatedly over a session's life is a
sequence, and a reader that takes only the first or only the last row silently
drops the rest — in our case fourteen fields.

## Everything on disk in one place

All persistent state lives under a single directory: configuration,
credentials, sessions, subagent state, skills, extensions and their private
data directories, personas, cards, worlds, lore, themes, locales, logs.

Two properties of that arrangement earn their keep. **Backup and inspection are
one path**, not a hunt through platform-specific application-support
conventions. And **project-scoped mode is a redirection** — point the data
entries at a directory inside the project while keeping credentials and trust
global, and you get per-project isolation with no separate code path.

Credentials are the exception to "just files": owner-only permissions on both
the file and its directory. Encryption at rest is not yet done, and saying so
is more useful than implying otherwise.

## Identity: who the agent is

A harness that only has a system prompt has one personality. terva separates
several things that are usually conflated, and the separation is the point.

**Experience** — which product terva is being right now: coding (the default),
chat, or play. It is not cosmetic; it gates which tools are registered, which
skills are discovered, and how the prompt is framed. Ambient coding context —
the project instruction files a coding session loads automatically — is
deliberately *not* loaded in the immersive experiences, because a fiction
session that silently ingests your repository's contribution guidelines is
doing something nobody asked for.

**Persona** — who the agent is. A library of documents, composable and tiered,
that supply identity and voice. Some are machine-bound: named roles the system
dispatches to by a fixed identifier, which is why user overrides of those names
need validation rather than silent shadowing.

**Card** — a portable character document, in an interchange format the wider
ecosystem uses. Cards are content, not configuration: importable, versioned
with history, and checkable by a linter.

**Lore** — keyed context. Entries that carry a set of trigger keys and a body,
where the body enters the request only when a key appears in what is being
discussed. This is the general primitive for "a large body of background
knowledge that must not all be in context at once," and it is a genuinely
different mechanism from summarization: nothing is lost, it is simply not
present until it is relevant.

**World** — saved play state: which cast is present, where the scene stands,
what has happened.

**Deliberation** — a panel of several seats reasoning over a question and
returning a considered answer, seated on top of the subagent machinery. Also
the clearest demonstration that the subagent primitive is general rather than
a coding feature.

One rule holds these apart: **keyed context and conversation memory are
unrelated mechanisms and must not be merged.** They look similar — both inject
text that was not in the conversation — and they answer opposite questions.
Keyed context is *authored, static, and retrieved by match*. Conversation
memory is *accumulated, mutable, and retrieved by recency or salience*. Sharing
an implementation between them makes both worse.

## What identity costs

Every mechanism in this chapter puts bytes in front of the conversation, which
means [chapter 03](03-context-and-cost.md) applies to all of it. Two
consequences shape the designs:

**Anything mutable belongs in the ephemeral tail, not the system prompt.**
A persona that can be edited mid-session and is spliced into the system prompt
invalidates the cache for the entire conversation on every edit. This is a real
mistake with a real price — one peer harness rebuilds its system prompt per
turn to support a mid-session memory write, and busts its prefix cache doing
it. The alternative pattern is a **frozen snapshot**: read the mutable state
once at session start, use it for the whole session, and let writes take effect
on the next one.

**Retrieval beats inclusion.** The lore mechanism, the skill manifest, and the
subagent are three instances of one idea: keep the index in context and the
body out of it, and pay for the body only when it is needed.

---

*Implementation: sessions and keyed context live in `packages/core`; the
persona, card, world and deliberation stores under `packages/agent`. User-facing
guides: [personas.md](../personas.md) and [raati.md](../raati.md).*
