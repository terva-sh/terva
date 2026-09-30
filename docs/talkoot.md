# Talkoot: a team of agents

A talkoot is a persistent team of agents that works in one checkout, with you
in the loop. *Talkoot* is Finnish for a community work party, where neighbours
gather to do together what one household cannot. Each member of the team has a
role, a persona, and limits of its own. Members talk to each other in a shared
room and hand work over as tickets, branches, commits, paths, and notes. When a
decision is yours, a member asks you in the inbox and waits for your answer.

The terva daemon runs the team, and the web panel is where you watch and steer
it. A member is a native terva session or an external harness that terva
drives as a worker, such as Claude Code. Every member works under the same
permission model, and a member's authority comes from the team's roster, never
from its persona.

Talkoot shipped in v0.139.2 on 2026-09-25. [What it does not do
yet](#what-it-does-not-do-yet) lists the pieces that are still missing.

## Turn it on

Talkoot is off by default. Set the key in your user configuration,
`$TERVA_HOME/config.json`:

```json
{
  "talkoot_enabled": true
}
```

The key works from the user layer only. A project configuration cannot turn
it on, because a roster names what its members may do and what they may
spend, and a cloned repository must not decide that for you.

Then start the web panel from the checkout the team works in:

```sh
cd ~/src/my-project
terva web
```

A daemon that was already running reads the key when it starts, so restart
it after you change the key. The **⁂** button in the web panel's header opens
the Talkoot view. [web.md](web.md) covers `terva web` itself.

A member whose driver is not `native` also needs external workers:

```json
{
  "talkoot_enabled": true,
  "external_workers_enabled": true
}
```

While `external_workers_enabled` is off, such a member shows as offline and
receives nothing. The settings pane shows the same key as **External agent
workers** while `auto_swarm` is on. Without `auto_swarm`, set it in
`config.json`.

## Your first team

1. Open the Talkoot view with **⁂**, and press **New team**.
2. Pick a template, such as `coding`. Name the team, and set a daily budget in
   US dollars, or keep the template's.
3. Press **Preview**. The preview lists each member with its role, driver,
   model, posture, workspace, and limits, and shows the `talkoot.md` the form
   writes. A member that this machine cannot run, such as one whose driver is
   not installed, must be dropped before you continue.
4. Press **Create the team**.

A new team starts no work on its own. A **Start the team** card waits in the
inbox, with the estimated cost of the introductions. **Run the introductions**
has each member introduce itself, and then the coordinator asks you the first
question. **Skip them** starts the team without them.

Then write to the team in the composer. A post without an `@member` reaches
the coordinator, and `@id` reaches that member. Every chain of messages
between members starts with a post from a person, and the room refuses a
chain that does not.

The team's home is the directory the daemon runs in, so a daemon serves the
teams of one checkout. A roster whose `home` names another directory does not
load.

### The coding template

`coding` is the one built-in template. Its six members are all native terva
sessions, with a daily budget of 20 USD for the team.

| Member | Role | Tier | Posture | Job |
|---|---|---|---|---|
| `mieli` | coordinator | strong | `plan` | Takes each request from you and decides who works on it |
| `arkkitehti` | planner | strong | `plan` | Writes the plan before the work starts |
| `developer` | specialist | strong | `auto-edit` | The only member that changes files in the checkout |
| `koestaja` | specialist | medium | `plan` | Checks the tests |
| `vartija` | specialist, reviewer | medium | `plan` | Checks the security of each change, and reviews every branch before a merge |
| `kirjuri` | specialist | weak | `plan` | Checks the documentation |

The template's charter tells the members to work from tickets, to hand work
over as something the next member can open rather than as a summary, and to
ask you when a decision is yours.

## Watch and steer it

The web panel is the one place to watch a talkoot. [The Talkoot section of
web.md](web.md#talkoot-watching-a-team) describes the view in full: the
sidebar of members and their states, the room, each member's own view, the
member card, and pause and resume.

- **The inbox** holds what waits for you: a member's question, a tool call its
  posture asks about, a roster proposal, and a new team's kickoff. One answer
  resolves the card in the inbox and in the member's own session. The room
  keeps your answer to a question, so a teammate may see it.
- **Your name** is what the room records beside your posts, answers, and
  pauses. The browser keeps it. The name records who acted, and the `steer`
  capability decides who may act.
- **Pause** stops one member, one chain of messages, or the whole team.
  **Resume** lifts the pause.

The browser tab carries the team: its title reads the team name and its
state, and its icon is the team's mark.

## The roster

Each talkoot is one directory, `$TERVA_HOME/talkoot/<id>/`, and its roster is
the `talkoot.md` file there. The id is the directory name. It is lower case
letters, digits, and dashes, starting with a letter, and never changes. The
file is YAML front matter, and every member receives the body below it as the
team's charter.

```markdown
---
name: docs-crew
title: Documentation crew
home: /home/you/src/my-project
budget_usd_per_day: 5
members:
  - id: lead
    role: coordinator
    persona: mieli
    tier: medium
  - id: writer
    role: specialist
    persona: kirjuri
    tier: weak
    posture: auto-edit
  - id: checker
    role: specialist
    persona: vartija
    tier: medium
    reviewer: true
---

Keep the user guides true to the code. The writer changes files, the checker
reviews each branch, and the lead asks the person before a merge.
```

The web panel's **New team** form writes this file for you. You can also
write it by hand, and the daemon reads it when it starts.

### Team keys

| Key | Required | Meaning |
|---|---|---|
| `name` | yes | The team's name |
| `title` | no | A longer title for the view |
| `home` | yes | The checkout the team works in. It must be the daemon's directory |
| `budget_usd_per_day` | yes | The team's daily spend cap in US dollars, above 0 |
| `color` | no | The team mark's colour, as `#RRGGBB`. Absent, the id picks one |
| `members` | yes | The members, below |

### Member keys

| Key | Meaning |
|---|---|
| `id` | The member's address in the room, as in `@developer`. Lower case letters, digits, and dashes |
| `role` | `coordinator`, `planner`, or `specialist`. A team has exactly one coordinator |
| `title` | A display title, such as `Developer` |
| `persona` | The persona the member speaks as. See [personas.md](personas.md) |
| `mark` | The member's mark, as `shape` and `color`. Absent, it comes from the persona |
| `driver` | What runs the member. Default `native`. See [Drivers](#drivers) |
| `model` or `tier` | The model id, or a tier: `weak`, `medium`, `strong`, or `cheap`. Not both |
| `posture` | The member's approval mode. Default `auto-edit` in a worktree, `plan` otherwise |
| `workspace` | `shared`, the team's home checkout, or `worktree`, a worktree leased for the member. Default `shared` |
| `reviewer` | `true` lets a specialist close a ticket another member worked |
| `budget_usd_per_day` | The member's own daily spend cap, no higher than the team's |
| `turns_per_day` | A daily cap on the member's turns |
| `tools` | Narrows the member to the named tools. See below |
| `idle_stop` | For a worker member: how long its process stays up with no turn, such as `30m`, or `off`. Default `30m`, shortest `1m` |

The daemon checks the roster when it loads it, and names every problem it
finds. Beyond the formats above, the rules are these:

- At most one member can write to the shared checkout. Give every other
  member `posture: plan` or `workspace: worktree`.
- `posture: yolo` needs `workspace: worktree`, and a worker member can never
  have it.
- `reviewer` is for a specialist only.
- `idle_stop` is for a worker member only, since a native member has no
  process to stop.

`tools` lists at most 64 entries. An entry is a tool name, a prefix such as
`ticket_*`, or `mcp:<server>` for every tool of an MCP server. The list only
narrows: a tool the member's posture refuses stays refused. The member's seat
tools, which it uses to talk to the team, stay without a listing.

## Templates

A template is a roster without a `home`, plus an optional `description`. Its
budget is a suggestion that the **New team** form lets you change. terva looks
for templates in four places, and a name found in an earlier place hides the
same name in a later one:

1. Your own, in `$TERVA_HOME/talkoot-templates/*.md`.
2. Each enabled global extension's `talkoot-templates/` directory, named
   `ext:<extension>`.
3. The built-in `coding`.
4. The repository's own, in `.terva/talkoot/*.md`, named `repo:<name>`. A
   repository template never hides another.

A repository template is listed by name only until you trust the workspace,
because a cloned repository controls it. terva reads at most 64 template
files of at most 64 KiB each from the repository, follows no symbolic links,
and skips `README.md`.

## Drivers

| Driver | What runs the member | What it needs |
|---|---|---|
| `native` | A terva session inside the daemon | Nothing more |
| `claude` | Claude Code, as a worker | `claude` on `PATH`, signed in with its own login, and `external_workers_enabled` |
| `terva:portable` | `terva rpc --portable`, as a worker | `external_workers_enabled` |
| `terva` | `terva rpc`, as a worker | `external_workers_enabled` |

A `native` member can use every seat tool: `talkoot_send`,
`talkoot_handoff`, `talkoot_roster`, `talkoot_note_write`,
`talkoot_note_read`, `talkoot_propose`, and `ask_user_question`.

A `claude` or `terva:portable` member reaches the team through the Talkoot
MCP bridge, which gives it `talkoot_send`, `talkoot_handoff`,
`talkoot_roster`, and `ask_user_question`. Each call takes the same path as a
native member's: your hooks, then the policy of the member's posture with
your rules, and a card in the inbox when the policy asks.

- A `claude` member keeps Claude Code's own login. terva passes it no
  credentials. Its `model` crosses as the family alias `opus`, `sonnet`, or
  `haiku`, and otherwise Claude Code picks its own model. Its posture maps to
  a Claude Code permission mode that is never wider: `plan` to plan mode,
  `auto-edit` to accept-edits mode, and `ask` or `workspace` to the default
  mode, with its questions sent to your inbox. Its `tools` can narrow only to
  `read`, `write`, `edit`, `bash`, `grep`, and `glob`.
- A `terva:portable` member receives the same briefing a Claude Code member
  does, and nothing of terva's own context. It cannot narrow its tools, so a
  roster that sets `tools` for it does not load.
- A `terva` member has no bridge, so it cannot send to the team. Its last
  message reaches the coordinator as a note.

A worker member runs as a process. It stops after `idle_stop` with no turn,
and the next message wakes it. A worker in a worktree keeps its lease across
a restart. If the lease fails, the delivery fails, and the member never falls
back to the home checkout.

Check the terms of your provider before you seat a member on a subscription.
[providers.md](providers.md) says why.

## Add a member

A member proposes a roster change with `talkoot_propose`, and the proposal
waits in your inbox. Its card lists each member it touches, field by field,
before and after, and it flags every change that grants a member more.
Nothing changes until you approve it.

To look for a new member, open a recruiter:

```sh
terva attach --recruit docs-crew
```

The recruiter session runs the Hautoja persona, or the one `--persona` names.
It makes no model call until you write. It reads the roster, asks about the
job, and proposes a member for you to approve. It offers only the drivers
installed on this machine. [cli.md](cli.md) documents the flag.

The member card in the web panel changes a member directly, and the room
records each edit as a roster line in your name.

## Guards and spend

A team spends on its own between your posts, so its limits are part of the
roster rather than an afterthought.

- **Budgets.** The team's `budget_usd_per_day` is required. A member can have
  its own budget and a daily turn cap too. The daemon checks the caps after
  each turn, so one turn can take a member past its cap before it pauses.
- **The day** is the daemon's local day. The counters start again at local
  midnight. A pause stays until you resume it.
- **Pauses stack.** A member can be paused for its spend, its turns, the
  team's spend, a guard, a failed turn, or by a person, and it runs again
  only when every pause is lifted. A failed turn pauses its member until you
  resume it.
- **Guards** stop a team that talks to itself. They have fixed values today:

  | Guard | Limit |
  |---|---|
  | Messages between members in one chain | 12, then the chain pauses |
  | Messages one member sends | 20 in 10 minutes |
  | The same message to the same member | dropped inside 10 minutes |
  | Members working at once | 4, and the rest wait |

- **The room is sealed.** Each line of the room log carries a keyed hash of
  the one before it. A line that fails the check pauses the team.

## Permissions

A member's posture is its approval mode, the same `plan`, `ask`,
`auto-edit`, `workspace`, and `yolo` a session has
([permissions.md](permissions.md)). You cannot change a member session's
approval mode from its session. The roster sets it.

A client needs the `steer` capability to create a team, change its roster,
post to it, answer for it, pause it, or resume it. A `viewer` can watch a
room and cannot steer it. [Steering a
talkoot](permissions.md#steering-a-talkoot) covers the rule, the MCP bridge,
and the gap it leaves: a member that can run `bash` runs as your user, and so
can reach what the daemon can.

Members work tickets through the ticket tools, which write under the
member's own name, as in `agent:native/developer`. A built-in rule denies
`git ticket` through `bash` to every member, and only a reviewer who did not
work a ticket may close it.

## Faces and the team mark

Each member has a mark, a small shape in its own colour, with eyes that show
its state: working, waiting for you, paused, or offline. The whole team has a
mark too, in the team's colour, and its eyes show the team's state. The team
is offline when nothing can run it, and it needs you when a member waits on
you or a turn failed. Otherwise it is busy while a member works, paused while
a member is paused, and online.

The **Team colour** picker in the view's header sets the team's colour, and
**Default** returns it to the one the id picks. The **Marks** picker sets how
much the faces move. When your system asks for reduced motion, the faces stay
still.

## Where it lives

Everything a talkoot keeps is under `$TERVA_HOME/talkoot/<id>/`:

| Path | Holds |
|---|---|
| `talkoot.md` | The roster and charter |
| `room.jsonl` | The room log: every message, roster change, pause, and answer |
| `room.key`, `room.head` | The seal's key and the last line it covers. The sandbox denies both to every agent |
| `notes/<member>/` | Notes a member writes for the team, up to 200 of at most 256 KiB each |
| `proposals/` | Roster proposals and their decisions |
| `kickoff.json` | The kickoff's state |

A native member's conversation is an ordinary daemon session. A worker
member's state lives with the other workers, under `$TERVA_HOME/swarm/`.

## From a script

Every action in the view is a control verb in the `talkoot` group, such as
`talkoot.list`, `talkoot.post`, `talkoot.pause`, and `talkoot.decide`.
`terva ctl` calls one from the command line:

```sh
terva ctl talkoot.list
```

[controllers.md](controllers.md) lists every verb, its parameters, the
capability it needs, and the events a talkoot sends.

## What it does not do yet

- The terminal interface has no Talkoot view. Watch a team in the web panel.
- A native member cannot run in a worktree, so a native team shares one
  checkout, and one member at most can write to it. No member can run `yolo`
  as a result.
- There is no driver for Codex or for other ACP agents.
- The guard values and the daily budget period are fixed.
- A member on another machine cannot join a team.
