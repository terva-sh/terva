# 04 — Permission and sandboxing

The threat is less exotic than the discourse suggests. The dangerous case is not
a malicious model; it is a *confused* one — an agent that misreads a path and
deletes the wrong directory, or that reads a file containing text addressed to
it and follows the instructions it finds there.

Prompt injection is not hypothetical. Any tool that fetches a web page, reads an
issue tracker, or opens a file someone else wrote is a channel through which a
third party can put words in your model's mouth.

You cannot prompt your way out of this. Instructions telling the model to be
careful are advisory text competing with other advisory text in the same window.
**The only thing that reliably stops a tool call is code that refuses to run
it.**

---

## Structure

### One chokepoint, and it is not negotiable

**Converged, scarred.** There must be exactly one place a tool call can be
denied, and every host must go through it.

The empirical basis is uncomfortable: every harness that grew a second
enforcement path has shipped a bypass through it. If the terminal UI checks
permissions and the RPC server also checks permissions, the two will diverge,
and the divergence will be found by whoever needed the unguarded one.

Two corollaries:

- **Record which door each call came through**, in the audit log, as a field.
  When you eventually have several entry points, you will need to answer "which
  host allowed this" and you will not be able to reconstruct it.
- **Make forgetting impossible.** Ours was a function pointer a host had to
  assign, and a host that forgot got a completely ungated agent with no
  compile-time or runtime signal. We found one that had. Prefer a shape where an
  unwired gate cannot start.

### Order the layers, and forbid self-approval

**Converged.** A workable ordering, each layer able to stop the call:

1. **User hooks** — external programs the user configured. Theirs; first.
2. **Policy and approval** — the typed rules and the mode. The core decision.
3. **Plugin intercepts** — extensions that asked to see calls.

The rule that makes layer 3 safe: **a plugin may tighten a decision, never
loosen it.** Gemini CLI enforces the same constraint on extension-contributed
policy rules, and for the same reason — a plugin that can grant itself
permission is a plugin that has permission. The cheapest way to guarantee it is
structural: return on denial *before* the interceptors run, so there is no code
path in which an interceptor is even asked about a call the policy refused.

Note the ordering asymmetry, and decide it consciously rather than inheriting
it. Layer 1 is the user's own configuration, so it is reasonable for its
`allow` to bypass the policy outright; layer 3 is third-party code, so its
`allow` must mean nothing. If both layers get the same power you have quietly
given every plugin the user's authority.

And be precise about what "tighten only" covers. If your interceptors can also
**rewrite arguments**, then the arguments a human approved and the arguments
that execute may differ, even though the allow/deny decision was never
loosened. That is defensible — a plugin running as local code needs no such
route to do damage — but document it, because "the gate is the last word" is
what readers will otherwise assume.

### Keep mutable decisions separate from observation

**Reported.** A peer harness conflated its "can veto" hook with its
observe-only events and shipped documented bypass bugs as a result. If the same
callback can both watch and veto, every observer becomes a security surface, and
every new observer is a new place to audit.

---

## The two axes

### Never merge "may it run" with "what may it touch"

**Converged.** This is the most common structural mistake in the category, and
the field has explicitly separated the axes: Codex models `sandbox_mode` and
`approval_policy` as orthogonal, and terva keeps approval modes distinct from
the jail for the same reason.

Merged into a single "trust level," you get settings where the only way to let
the agent run tests without prompting is also to let it write outside the
project. Users then pick the permissive option and you have shipped nothing.

### Name the approval modes; a boolean is not enough

**Converged.** A spectrum with about five rungs is where everyone lands:

| Mode | Behavior |
|---|---|
| **plan** | Read-only tools only; mutating calls **refused outright**, not prompted |
| **ask** | Prompt for everything, reads included |
| **auto-edit** | Reads and file edits free; everything else prompts |
| **workspace** | First-party tools and all reads free; foreign side-effecting tools prompt |
| **yolo** | Everything runs |

Two details worth copying. **Plan mode refuses rather than prompts** — that is
what steers the model to present a plan instead of attempting a change and
waiting on a dialog. And the distinction between the middle rungs is **origin**,
not just read-versus-write: your own built-in tools may be trusted to run when a
third-party server's mutating tool is not, even though both write.

### Classify authority; a read-only boolean is wrong

**Scarred, converged.** The case that proves it is web fetch. It reads nothing
locally — so a boolean calls it read-only — yet it can exfiltrate data, trigger
remote logging, and reach hosts inside your network. Auto-allowing it as "a
read" is exactly backwards.

A workable taxonomy, closed set:

| Class | Auto-allowable |
|---|---|
| local read | yes |
| local data — reads and writes *only its own* host-managed directory | yes |
| workspace mutation | no |
| process execution | no |
| network read | **no** |
| external mutation | no |
| user interaction — blocks to ask a question, no other effect | always permitted |

Three properties matter more than the exact list. The classification is
**advisory data, not a capability grant** — a tool cannot widen its reach by
declaring a gentler class, because the host controls what it can reach.
**Unknown values fail closed** — an unrecognized class is side-effecting.
And the *user-interaction* class exists because gating a question behind an
approval prompt, or refusing it in plan mode, is nonsensical, and without a
class for it you will special-case it somewhere worse.

---

## Answers that persist

### Prompt fatigue is a security failure

**Reported, converged.** Prompting forty times for the same command trains the
user to approve without reading. Anthropic reports that adding sandboxing cut
permission prompts by 84% — a vendor-reported figure, but the direction is
uncontroversial and the framing is right: **reducing prompts is a security
outcome, not a convenience one.**

Offer at least: this call only, this session, and durably.

### A durable grant must carry its scope

**Scarred.** "Always allow" for a shell tool is meaningless unless it can mean
*always allow this command*, which requires the grant to record the argument
pattern and not just the tool name. A grant that records only `bash: allowed` is
an unconditional grant wearing a specific-looking label.

### Decompose compound commands, deny-first

**Scarred, converged.** `git diff && rm -rf /` is two scopes, and the second
decides. Judging the whole string as one opaque prompt was a real gap we shipped
and closed.

Do the decomposition in the *policy*, not only in the sandbox — otherwise a
rules-free session judges the compound as one unit while the sandbox splits it,
and the two disagree. The most accurate approach in the field parses the shell
with a real grammar rather than pattern-matching; that is worth the dependency
if you can afford it.

### Escalate with the specific missing permission

**Reported.** Codex's flow is the one to copy: a sandboxed command fails → the
approval request names *the specific extra permission needed* → retry. This is
far better UX than a generic "this was denied, allow?" and it gives the user
enough information to make a real decision.

---

## Trust as a third question

### Project-supplied configuration needs an explicit trust decision

**Converged, scarred.** A cloned repository can contain configuration that adds
hooks, registers external tool servers, points at context files, or suggests
permission rules. Useful for a project you own; an attack surface for one you
just cloned. Codex honors project-scoped configuration only in trusted
directories; we gate the same set on an explicit per-directory Workspace Trust
decision.

Two properties this needs, both learned by not having them:

**Trust must be live.** Granting it mid-session has to actually re-derive what
it gates — project hooks start running, project context becomes discoverable,
project-suggested rules reach the running gate — and revoking must tear the same
things down. A trust flag that only takes effect at next launch is a trust flag
that lied.

**The untrusted layer must be contained.** A project's configuration must not be
able to point at files outside the project root. We shipped that exfiltration
path and fixed it; it is the same threat class as project-scoped tool servers.

### Derive subagent posture, and fail closed

**Scarred.** A spawned agent's permission posture must be derived from its
parent's, and the derivation must fail closed. Ours reached its permissive
answer by falling off the end of a boolean chain rather than by deciding, so an
unrecognized mode got full autonomy.

The asymmetry is the argument, and it is worth writing down next to the default:
an unknown mode wrongly asked to confirm costs one prompt somebody can answer;
an unknown mode wrongly handed full autonomy runs every tool unconfirmed,
anywhere on the filesystem, and is silent about it.

Also check what the child actually inherits, rather than what its command line
says. Ours passed no plugin flags — and the child then ran full default
discovery anyway, with a permissive posture and no gate object. The argv was
honest; the child was not.

---

## Isolation

### Say plainly what your jail is not

**Scarred.** Path canonicalization with symlinks followed, rejection of paths
outside the root (including for files that do not exist yet, checked via the
nearest existing parent), and command-pattern refusal for obvious escapes — this
is a **guardrail, not a security boundary.** It raises the cost of an accident
by a great deal and the cost of a determined escape by very little.

Document that. The failure mode of overstating it is that someone builds a real
trust decision on top of it.

### A control the model routes around in one turn is worse than no control

**Scarred.** This one is genuinely counter-intuitive and it changed our design.

If you ship a shell tool, it cannot be path-confined without ceasing to be a
shell. Which means path-confining *reads* on your structured tools confines
nothing: whatever `read` refuses is one `cat` away. In the session that forced
this for us, the model issued eight parallel reads outside the root, had all
eight refused, and fetched the same bytes through a shell pipeline on the very
next turn.

So the refusal bought no confinement, cost a full turn, and — the part that
matters — **taught the model to probe for the gap.** A guardrail that is
trivially circumventable does not degrade to "no guardrail"; it degrades to
"training data for circumvention," and the behavior it teaches persists after
the model leaves the case that taught it.

Our resolution was to make the axes honest about what they actually do:
**writes are contained; reads get a deny list** of the things genuinely worth
denying and that shell access reaching them anyway does not excuse —
credentials, session transcripts, the log sink they leak into. Enumerated by the
host, not inferred.

> **Rule.** For each control, ask what the cheapest route around it costs the
> model. If the answer is "one turn," you have a tax, not a boundary — either
> close the route or drop the control and say what is actually protected.

The related claim to avoid: a plugin system whose *tool calls* are permission-
gated is not a sandbox for the *plugin process*, which typically runs with your
full privileges. Installing a plugin is consent to run a local program. State it.

### OS-level sandboxing is the real answer, and it is portable enough

**Reported.** The field's state, for anyone deciding whether to build it:

- **macOS** — `sandbox-exec` with a generated profile. A subprocess, not an API,
  but it ships with the OS.
- **Linux** — bubblewrap plus seccomp is the current mainstream path; Landlock
  is plain syscalls and needs no C bindings, which makes it viable as a
  no-dependency fallback.
- **Windows** — restricted tokens.

Network policy is the piece people forget: Claude Code enforces a domain
allowlist from a **separate proxy process outside the sandbox**, because a
policy enforced inside the thing you are containing is not a policy.

### Harden the subprocesses you spawn

**Converged.** When spawning plugins, connectors or tool servers, filter the
environment. Goose maintains a blocklist of roughly thirty-one variables —
`LD_PRELOAD`, `PYTHONPATH`, `NODE_OPTIONS`, `PATH` and friends — because
inheriting them hands the child an injection surface you did not intend to open.

Cheap, and it closes a real class.

---

## Audit

**Converged.** Log every gate decision with: the tool, the arguments (or a
scoped digest of them), the decision, the reason, the rule or grant that
produced it, and which host asked. Put the record *inside* the gate rather than
at the call sites — we had three doors into ours and only one of them logged,
which is exactly the arrangement that makes an audit log worse than none.
