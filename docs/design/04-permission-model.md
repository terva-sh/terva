# 04 — The permission model

The model proposes; the harness disposes. This chapter is about the disposing.

Start from the threat, because it is less exotic than people assume. The
dangerous case is not a malicious model. It is a *confused* one — an agent that
misreads a path and deletes the wrong directory, or that reads a file
containing text addressed to it and follows the instructions it finds there.
Prompt injection is not a hypothetical: any tool that fetches a web page, reads
an issue tracker, or opens a file someone else wrote is a channel through which
a third party can put words in the model's mouth.

You cannot prompt your way out of this. Instructions telling the model to be
careful are advisory text competing with other advisory text in the same
window. **The only thing that reliably stops a tool call is code that refuses
to run it.**

## One chokepoint

The first design commitment: there is exactly one place where a tool call can
be denied, and every host goes through it. Not one gate per front end, not one
per tool, not a check inside each tool's implementation.

The reason is empirical. Every harness that has grown a second enforcement path
has shipped a bypass through it. If the terminal UI checks permissions and the
RPC server also checks permissions, the two will diverge — and the divergence
will be discovered by someone who found the unguarded one.

So: one gate, and every path into it is recorded in the same audit log with a
field naming which door it came through. Adding a new host means wiring the
gate, and the failure mode of forgetting is a completely ungated agent — which
is why *not being able to forget* is a recurring theme in
[chapter 08](08-lessons.md).

The gate does three things in order, and each layer can stop the call:

1. **Hooks** — user-configured external programs that see the call and can
   deny it, rewrite its arguments, or allow it. Yours, so they run first and
   they are the most powerful layer: an `allow` decision **skips the gate
   entirely**, and a `deny` is final. `ask` defers to the gate even in a mode
   that would have auto-allowed. This is deliberate — a hook is something you
   configured, not something the model or a plugin can install.
2. **The policy and approval gate** — the typed rules and the approval mode
   below. The core decision.
3. **Extension intercepts** — plugins that asked to see calls before they run.
   Last, and unable to self-approve: an extension can *block* a call the gate
   allowed, but it can never allow one the gate denied, because a denial
   returns before the interceptors run. A plugin that could grant itself
   permission is a plugin that has permission.

   One precision that matters, since "tighten only" would otherwise be the
   obvious reading: an interceptor **can rewrite arguments after approval**,
   so the arguments a human approved and the arguments that execute are not
   guaranteed to be the same ones. This is not a hole so much as a
   restatement of the trust boundary below — an extension is already local
   code running with your privileges, and does not need the tool pipeline to
   do anything it could not do directly. It is worth knowing exactly, rather
   than assuming the gate is the last word on content as well as on
   permission.

## Two axes that must not be merged

The second commitment, and the one most often gotten wrong:

> **Whether a tool may run** and **what it may touch once it runs** are
> different questions.

Conflating them into a single "trust level" is the known failure mode across
the field. It produces settings where the only way to let the agent run tests
without prompting is also to let it write outside the project.

terva keeps them separate:

**The approval axis — five modes.** How much runs without asking.

| Mode | Behavior |
|---|---|
| `plan` | Read-only tools only. Mutating calls are *refused outright*, not prompted — which steers the model to present a plan instead of attempting changes. |
| `ask` | Prompt for every call, reads included. |
| `auto-edit` | Reads and file edits run freely; everything else prompts. The practical middle: a file change is cheap to review as a diff, an arbitrary command is not. |
| `workspace` | Trusts first-party tools and all reads — the built-ins and any read-only tool, including read-only extension and MCP tools — while foreign tools that can have side effects prompt. **The interactive default.** |
| `yolo` | Everything runs. **The headless default.** |

The two defaults are the asymmetry to notice, and it is worth saying out loud
rather than leaving in a flag table: **an interactive session starts gated and
sandboxed; a headless one starts ungated and unsandboxed.** The reasoning is
that a prompt with nobody to answer it is not a safety measure — it is a hang,
or a refusal that silently breaks an automation. So the headless modes make the
opposite default and expect you to opt in, with `--approval` or with permission
rules, which are honored identically in both. When a mode *would* need a prompt
and there is no one to ask, the call is **refused** with a model-readable reason
rather than run unconfirmed, and a gate that will behave that way says so on
stderr at startup.

If you take one operational thing from this chapter: an unattended terva is as
permissive as you configured it to be, and the built-in default is permissive.

Note what distinguishes `workspace` from `auto-edit`: the axis is **origin**,
not just read-versus-write. Your own built-in tools are trusted to run; a
third-party MCP server's mutating tool is not, even though both write.

**The enforcement axis — the sandbox.** What a tool can reach once it has been
allowed to run. This one is narrower than its name suggests, and the precision
is worth having:

- It is a **write** boundary. Mutating tools are confined to the working
  directory, with symlinks resolved so a link out of the project does not
  escape, and nonexistent targets checked via their nearest existing parent.
- It is **deliberately not a read boundary.** A jailed agent may read anywhere
  except an enumerated set of secret paths — credentials, session transcripts,
  the logs they leak into. Reads get a *deny list*, not containment.
- The shell is **not path-jailed at all**, only screened for obvious escape and
  destructive patterns.

The read asymmetry is a decision, not an oversight, and its reasoning is
instructive. Since the shell cannot be path-confined without ceasing to be a
shell, anything a confined `read` refused was already one `cat` away. Enforcing
containment on one tool and not the other bought no confinement and cost real
turns: in the session that prompted the change, the model issued eight parallel
reads outside the root, had all eight refused, and fetched the same bytes
through a shell pipeline on the next turn. A refusal the model routes around in
one turn is not a control; it is a tax that also teaches the model to probe for
gaps.

The sandbox starts **locked for interactive sessions and unlocked for headless
ones**, and can be toggled at runtime. That asymmetry is deliberate: an
interactive user can unlock in one keystroke when they hit the boundary,
whereas a headless run that hits it just fails somewhere nobody is watching.

An honest caveat, stated in our own documentation and repeated here: **the jail
is a guardrail, not a security boundary.** It raises the cost of an accident by
a great deal and the cost of a determined escape by very little. Real isolation
is an OS-level sandbox, and it is the largest known gap in this design.

## Classifying what a tool can do

A boolean `read_only` flag is not enough, and the case that proves it is web
fetch. It reads nothing on your machine — so a boolean would call it read-only
— yet it can exfiltrate data, trigger remote logging, and reach hosts inside
your network. Auto-allowing it as "a read" is exactly wrong.

So every tool declares an **authority**, from a closed set of seven:

| Authority | Means | Auto-allowable? |
|---|---|---|
| `local-read` | Reads local files or state; no process, network, or external effect | Yes |
| `local-data` | Reads and writes *its own* host-managed data directory and nothing else | Yes — the write is confined to private storage the host controls |
| `workspace-mutation` | Writes files, edits workspace state | No |
| `process-execution` | Starts commands or subprocesses | No |
| `network-read` | Fetches URLs or search results | No — gated like a side-effecting tool until a network policy opts it in |
| `external-mutation` | Writes to third-party APIs, sends messages, opens pull requests | No |
| `user-interaction` | Blocks to ask the user a question, with no other effect | Always permitted — gating a question behind an approval prompt is nonsensical |

Two properties of this taxonomy are load-bearing. It is **advisory data**, not a
capability grant: a tool cannot widen its own reach by declaring a gentler
authority, because the host already controls what that tool can reach. And
unknown values are treated as side-effecting — an authority the harness does not
recognize fails closed, not open.

## Answers that persist

Prompting for the same command forty times is not security, it is training the
user to stop reading prompts. So approvals can be remembered:

- **For this call only** — the default.
- **For this session.**
- **Durably**, written to configuration and honored on future runs.

The scoping matters more than the persistence. "Always allow" for a shell tool
means nothing useful unless it can mean *always allow this command*, which
requires the durable grant to carry the argument pattern and not just the tool
name. A grant that records only "bash: allowed" is an unconditional grant
wearing a specific-looking label.

Compound commands are decomposed before judging, deny-first: `git diff && rm
-rf /` is two scopes, and the presence of the second decides the outcome. A
policy that judged the whole string as one opaque prompt was a real gap we
closed.

## Trust as a separate question

There is a third thing the two axes do not cover: **is the code in this
directory trusted at all?**

A cloned repository can contain configuration that adds hooks, registers MCP
servers, points at context files, or suggests permission rules. All of that is
useful for a project you own and is an attack surface for one you just cloned.
So project-supplied configuration is gated on **Workspace Trust** — an explicit
per-directory decision, stored durably, that determines whether the project's
own configuration is honored.

Two consequences worth stating. Trust is *live*: granting it mid-session must
actually re-derive what it gates — project hooks start running, project context
becomes discoverable, project-suggested rules reach the running gate — and
revoking it must tear the same things down. A trust flag that only takes effect
at next launch is a trust flag that lied. And the untrusted project layer is
*contained*: a project's configuration cannot point at files outside the project
root, which closes an exfiltration path we shipped and fixed.

## Where approvals are answered

A prompt has to reach a human, and the human is not always at a terminal. The
same gate decision therefore surfaces through several carriers: the terminal
dialog, the browser panel, a chat message, an editor protocol, an out-of-band
HTTP endpoint for headless review. While a decision is pending, the loop is
*parked*, not failed — it resumes at the same step when the answer arrives, and
a turn cancelled while parked releases the wait rather than leaking it.

The mode also propagates to spawned work. A subagent runs under a posture
derived from its parent's, and the derivation fails closed: a run mode the
harness does not recognize resolves to *ask, jailed*. The asymmetry justifies
itself — an unknown mode wrongly asked to confirm costs one prompt somebody can
answer, while an unknown mode wrongly handed `yolo` runs every tool
unconfirmed, anywhere on the filesystem, and says nothing.

---

*Implementation: the permission types live in `packages/core` (the engine
describes and stops; it does not enforce), enforcement in
`packages/agent/permissions` and the tool sandbox. The user-facing guide is
[permissions.md](../permissions.md); generalized guidance is in
[practices/permissions and sandboxing](../practices/04-permission-and-sandbox.md).*
