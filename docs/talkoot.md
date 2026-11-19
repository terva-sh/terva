# Talkoot: a team of agents

A talkoot is a persistent team of agents that works in one checkout, with you
in the loop. *Talkoot* is Finnish for a community work party, where neighbours
gather to do together what one household cannot. Each member of the team has a
role, a persona, and limits of its own. Members talk to each other in a shared
room and hand work over as tickets, branches, commits, paths, and notes. When a
decision is yours, a member asks you with a card and waits for your answer.

The terva daemon runs the team, and the Talkoot app, which `terva web` serves,
is where you watch and steer it. A member is a native terva session or an external harness that terva
drives as a worker, such as Claude Code. Every member works under the same
permission model, and a member's authority comes from the team's roster, never
from its persona.

Talkoot shipped in v0.139.2 on 2026-09-25. [What it does not do
yet](#what-it-does-not-do-yet) lists the pieces that are still missing.

## Turn it on

Talkoot is off by default. Turn on **Talkoot teams** under **Delegation &
panels** in the settings, from `/settings` in the terminal or the settings
pane of the web panel. The row writes the key to your user configuration,
`$TERVA_HOME/config.json`, and you can also set it there by hand:

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
it after you change the key, from the settings row or by hand. The row says
so. The app switcher in the web panel's header opens
the Talkoot app at `/talkoot/`. [web.md](web.md) covers `terva web` itself.

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

A workspace with no team opens the Talkoot app on a first-run page. It says
what a talkoot is, names the directory the teams belong to, asks for your name
in the room, lists the templates, and shows the steps below. Pick a template
there, or choose **Build a team member by member**. A team view without your
name asks for it in place, above the conversation, before you post, answer,
or pause.

1. Open the Talkoot app from the panel's app switcher, and press **New team**.
2. Pick a template, such as `coding`. Name the team, and set a daily budget in
   US dollars, or keep the template's.
3. Press **Preview**. The preview lists each member with its role, driver,
   model, posture, workspace, and limits, and shows the `talkoot.md` the form
   writes. A member that this machine cannot run, such as one whose driver is
   not installed, must be dropped before you continue.
4. Press **Create the team**.

A new team starts no work on its own. A **Start the team** card waits in the
conversation, with the estimated cost of the introductions. **Run the introductions**
has each member introduce itself, and then the coordinator asks you the first
question. **Skip them** starts the team without them.

Then write to the team in the composer. A post without an `@member` reaches
the coordinator, and `@id` reaches that member. A chain of messages between
members starts with a post from a person, and the room refuses a chain that
does not. The one other start is the run of a
[routine that you approved](#approve-work-on-a-schedule).

The team's home is the directory the daemon runs in, so a daemon serves the
teams of one checkout. A roster whose `home` names another directory does not
load.

### The coding template

`coding` is one of the six built-in templates, and [Templates](#templates)
lists the others. Its six members are all native terva sessions, with a daily
budget of 20 USD for the team.

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

The Talkoot app is the one place to watch a talkoot. [The Talkoot section of
web.md](web.md#talkoot-watching-a-team) describes the app in full: the rail of
teams and members, the room and its threads, each member's own thread, the
side panel with the member card, its work, and the team's notes, and pause
and resume.

- **Cards** hold what waits for you: a member's question, a tool call its
  posture asks about, a roster proposal, a request for a
  [routine](#approve-work-on-a-schedule), and a new team's kickoff. Each sits
  in the conversation at the time it opened. One answer resolves the card in
  the app and in the member's own session. The room keeps your answer to a
  question, so a teammate may see it.
- **Your name** is what the room records beside your posts, answers, and
  pauses. The browser keeps it. The name records who acted, and the `steer`
  capability decides who may act.
- **The team daily limit** appears below today's spend. **Waive team daily
  limit** keeps it off until you press **Restore team daily limit**, including
  across a restart or a daily reset. Member spend and turn limits still apply.
  Restoring the limit counts today's existing spend at once. The room records
  who changed it.
- **Pause** stops one member, one chain of messages, or the whole team.
  **Resume** lifts the pause.

The browser tab carries the team. Its title reads the team name and its
state, and its icon is the team's mark. The desktop window takes the same
title.

**Notify me when a card waits**, below your name in the rail, posts one
notification for each new card while the app is not in front. It names the
team and the member, such as "lead asks you to approve a tool call", and
never the card's text, because a notification can show on a locked screen.
A team you do not have open posts one notification when it starts to wait
on you, and not one for each card after that. The setting is off until you turn it on, and the browser keeps it. A browser
asks for its permission when you turn it on. The desktop window posts a
native notification instead, because its webview refuses the web API. On
macOS that needs terva inside an app bundle.

The rail and member card show each native member's resolved provider and
model, even before it has a session. An existing session's model takes
precedence and is labelled "live session". Reading the roster creates no
sessions and makes no provider calls. An external backend shows its requested
model or tier, or "backend default", rather than claiming a live model.

The member card offers the native model catalog, a search box, and a custom
model input. Tier choices are `weak`, `medium`, `strong`, and `cheap`.
Choosing a model clears the tier in the same edit. Choosing a tier clears
the model. External backends keep their own model names and do not receive
the native catalog.

A member can reply to the person who started its current chain with
`talkoot_send` to `human:<name>`, `human`, or that person's exact name. A
member id takes precedence over a person's exact name. No member can have
the id `human`, so that word always means the person. While routines are
on, a send whose every recipient is `human` or a `human:<name>` address
needs no approval card in `ask` and `auto-edit` posture. A member cannot
choose another person, and
a handoff still needs a member recipient. Replies to a person stay in the
room and wake nobody.

A successful native turn's completed final reply also appears in the room,
with the member as sender and the original chain and thread. That holds in a
chain that a person began. In a chain that a routine began, the room gets
only what the member sends with `talkoot_send`. The router
suppresses a matching recent body that member already sent in that chain.
Failed, cancelled, incomplete, and tool-step output stays out of the room.
The normal rate, hop, body-size, and pause guards still apply.

## Reach the team from chat

A chat bridge binds one chat connector's direct room to one team. You then
talk to the coordinator from a phone, and what the team says to you arrives
there. Decision 0029 records why: the coordinator is the entity you talk to,
and the Talkoot app is not where you are for most of the day.

The bridge is off until your user configuration names a team. A project
configuration cannot set the key, because the bridge decides who may write in
your name.

```json
{
  "talkoot_enabled": true,
  "talkoot_chat": {
    "talkoot": "crew",
    "connector": "telegram",
    "person": "sothr"
  }
}
```

| Key | Holds |
|---|---|
| `talkoot` | The id of the team. This daemon must run it |
| `connector` | The name of a configured chat connector. Leave it out for the default one |
| `person` | The name the room records beside your messages from chat. Use the name you use in the app. It holds 1 to 64 letters, digits, and `. _ @ -` |
| `brief` | Times of day at which the bridge sends the updates it held, such as `["07:30", "19:00"]`. Leave it out, and they go out when you next write |
| `quiet` | A range in which the bridge stays quiet, such as `"22:00-07:00"`. A range whose two ends are the same time is no quiet hours |

The key starts no bridge while `talkoot_enabled` is off, or when it names no
talkoot. The daemon says so on standard error when it starts, and the
**Chat** pane of the web panel shows the reason.

Set the connector up and pair your account before you turn the bridge on:

1. Run `terva bot setup --connector NAME`.
2. Run `terva bot run --connector NAME`, and send `/start` to the bot from
   your account. The first account that sends `/start` claims the connector.
3. Stop that bot. A bot that runs on the connector blocks the bridge.

[connectors.md](connectors.md) covers each step. The bridge refuses to start
on a connector with no paired account, and the foot of the rail
shows that refusal. A bridge starts with the daemon and stays up, so an
unpaired one would wait for the first account that found the bot. That
account could then post to your team under your name.

`terva web` reads the key when it starts. It does not wait for the connector.
A chat server that is down at start does not keep the Talkoot app down, and
the daemon dials again, first after 30 seconds and then at longer waits up to
10 minutes. A key that names a team this daemon does not run is reported on
standard error, and the daemon starts without the bridge.

While the bridge is on:

- **Your message** enters the room as your post to the coordinator. The
  router treats it as it treats a post from the app, so the same guards and
  the same pauses apply. The bridge never writes into a member's session.
- **What a member says to you** arrives in the chat, at once or later. The
  next section says which. The coordinator speaks with no name in front. Any
  other member is named, as `jev: ...`. Messages between members stay in the
  room.
- **A post you make in the app** arrives in the chat as `you: ...`, so the
  chat holds both sides of the conversation.
- **Another person's messages** stay in the room. The bridge carries what you
  posted and what a member addressed to the name in `person`. A second person
  who uses the team through the app did not choose to send their words to a
  chat service.
- **A member's question** arrives in the chat. Answer it there or in the app.
  The first answer settles the card in both places. The chat tells you when
  a question closed somewhere else. That happens when the app answered first,
  when the member's turn ended, and when
  [its deadline passed](#a-question-that-nobody-answers).
- **`/status`** returns the team's state, the members that work or are
  paused, and the count of cards that wait.
- **`/stop`**, or plain `stop`, pauses the whole team. Resume it in the app.

A question with no fixed choices takes your next message as its answer. The
question says so. Send `skip` to leave it for the app. A question that gets
no answer in 30 minutes leaves the chat and stays open in the app. The bridge
never answers for you.

Chat cannot approve a tool call, a roster proposal, or a kickoff. The chat
names the card and the member, and you decide in the app. Decision 0029
permits an approval from chat only from a device you verified, and the bridge
cannot check that yet.

The bridge forwards text only. It drops an attachment and tells you.

### When the team writes to you

A team that sends you every message becomes a log you stop reading. So the
daemon decides when the chat hears a message, and no member decides it. A
member writes to you as it always does. The daemon sends the message at once
only for one of these reasons.

| Reason | What the daemon reads it from |
|---|---|
| **A question** | A member's question or a request for an approval is open in the inbox. A member's turn waits on it |
| **A reply** | You posted in the last 10 minutes, from chat or from the app, so you are in the conversation |
| **The work finished** | The team stopped working, no member waits on you, and five seconds passed. The newest message of each piece of work goes out as its last word |
| **A decision** | A roster proposal or a kickoff waits in the app |
| **A routine paused itself** | A run failed, or the routine passed its daily spend limit. The routine starts no run until you resume it. Only the person who approved the routine gets this |
| **A watched change** | Something you asked the team to watch changed. Nothing produces this reason yet |

Every other message is an update. The daemon holds it for the brief.

A run that ended at a question that nobody answered also waits for the
brief. The chat already said that the question closed. The brief then holds
the question and what the run left undone, for the person who approved the
routine.

**The brief** is one message that lists the held updates, each with its time
and its sender. It lists the newest 30 and says how many older ones are in
the room. A message that the daemon holds keeps its first 600 characters,
and the room keeps the whole text. That holds for an update, for the last
word of finished work, and for what quiet hours held. A message that goes
out at once goes out whole. It goes out at the times in `brief`. With no `brief`, it goes
out when you next write. A daemon that was down at a brief time sends one
brief when it starts, and not one for each time it missed. The brief lists
what was held. No model writes it.

**Quiet hours** are the range in `quiet`. In that range the daemon holds
everything except a question, a reply, and a routine that paused itself. A
question still goes out, because a member's turn waits on it, and a question
that nobody hears parks the work until it expires. A reply goes out because
a person who writes at night is awake. A routine that paused itself goes out
because it does nothing at each of its times until you resume it. One
failure of the network at night is enough to pause a routine, so it is
enough to wake you. At the end of the range the daemon sends what it held
for a reason,
the oldest first. The updates stay for the brief. When you write in quiet
hours, you also get what was held for a reason.

Both keys read times in the daemon's zone. A zone name after a time, as in
`"07:30 Europe/Helsinki"`, is read and has no effect. The daemon keeps the
brief and the end of quiet hours as timers, and the web app's **Timers** pane
lists them. A daemon that keeps no timers sends the brief when you next
write, and only its log says so.

A `brief` or a `quiet` value that does not read stops the bridge. `terva web`
reports it on standard error and starts without the bridge. It does not start
the bridge with the value left out, because a bridge without the quiet hours
you asked for would write to you at night.

The chat never gets a tool call. For an approval it gets the member's name
and where to decide.

The held messages are in a file, so a daemon that restarts at night still
sends them in the morning. The queue holds up to 200 messages. Past that it
drops the oldest update, and the brief counts it with the older ones.
The room itself always holds every message in full.

The foot of the rail lists what the bridge holds, with why each
message waits and when the next brief goes out. Read anything there without
waiting for it.

A chat server that stops taking messages holds the bridge up while the team
goes on. The bridge keeps up to 1,024 updates for the chat. Past that it
counts what it could not keep, and it tells you the count when the chat
answers again.

The foot of the rail shows the bridge while it is on: the connector
and the account it connected as. A bridge that failed to connect shows its
error there.

A workspace runs one chat bridge. `terva web --web-chat-connector` binds a
connector to a session, and it takes the place of `talkoot_chat` when you set
both. A `terva bot` daemon on the same connector also blocks the bridge,
because two readers of one bot each lose messages to the other.

## Let a member check back later

Some work has a part that is not ready yet: a CI run, a deploy, a long
import. A member that waits for it inside its turn pays for the wait. A
member that ends its turn never looks again. A **wake** is the third way.
The member sets one later turn for itself with the `talkoot_wake` tool and
ends its turn. The daemon starts the later turn when the time comes.

Wakes are off by default. Turn them on in your user configuration,
`$TERVA_HOME/config.json`:

```json
{
  "talkoot_enabled": true,
  "talkoot_wakes": {
    "per_member": 3,
    "max_delay": "24h"
  }
}
```

| Key | What it caps |
|---|---|
| `per_member` | The wakes that one member may have open at a time. Wakes are off below 1 |
| `max_delay` | The longest delay of one wake, as a duration such as `90m` or `24h`. Empty means `24h`, and the least is one minute |

The key works from the user layer only, for the reason `talkoot_enabled`
does. It lets a member's turn start with no new post from you, and a cloned
repository must not turn that on. A value that does not read turns wakes
off. It never leaves a cap out, because a cap that is silently missing is no
cap. The foot of the rail then says why.

A value of the wrong JSON type is another failure, and it holds for each key
on this page. A number in quotes, as in `"per_member": "3"`, is one. The
whole `config.json` then does not read. terva says so on standard error and
starts with its defaults, so Talkoot itself is off.

Restart the daemon after you turn wakes on. A member gets the tool when its
session is built, so a running session does not gain it. A daemon that keeps
no timers, such as one whose timer file does not open, still gives the tool,
and the tool then refuses with that reason. Turning wakes off
needs no restart. The daemon reads the key again each time a member sets a
wake and each time a wake fires.

### What a wake is

- **A delay and one line.** The member gives a delay in minutes and a reason
  of one short line, at most 240 bytes. When the wake fires, the member reads
  that line and no other text, and it fetches what it needs itself. The line
  is short on purpose: the size of the text that enters a session sets what
  the turn costs.
- **Inside work you started.** A member can set a wake only in a chain that
  began with a person's post. The wake fires into the same chain, so the
  member's reply reaches you there. A member that reads the report of a
  routine's run is in a chain that no person began, so it can send there and
  it cannot set a wake. A routine's run sets a [follow-up](#a-follow-up)
  with the same tool, and the routine's card bounds it.
- **One hop, and no send.** A wake takes one hop of its chain, as a message
  between members does. A turn that a wake started can set the next wake, so
  without the hop one post from you would give a member turns for as long as
  its budget lasts. At the hop limit the chain pauses, and you resume it or
  you do not. A wake does not count against the member's send rate. The turn
  it starts counts toward the member's budget and its turn cap, as any turn
  does.
- **It waits for its member.** A member at work reads the wake at the next
  step of its turn, and never in the middle of a tool call. A paused member
  reads it when you resume it. With four members at work already, it waits
  for a free place.
- **A wake past a cap is refused.** The member reads the cap's value and the
  key that sets it, so it can tell you instead of trying again.

Only a `native` member has `talkoot_wake`. A roster's `tools` list narrows
it as it narrows any tool of a job: a member with a list has the tool only
when the list names it. A member in `plan` posture keeps it. To approve each
wake yourself, write an `ask` rule for `talkoot_wake`
([permissions.md](permissions.md)).

### See a wake, and cancel it

The foot of the rail in the Talkoot app says that wakes are on,
with the two caps. It lists each open wake with its time, its member and its
reason. The cancel button beside a wake removes it, and the member gets no
turn from it.

A wake is open until it comes due. A wake that comes due while its member or
its chain is paused becomes a message in the room that waits for your resume,
as a teammate's message does. It then leaves the list of open wakes and the
member's cap, and the cancel button no longer reaches it. To keep that turn
from the member, leave the chain paused.

The room records each step. A line says when a member set a wake, when you
cancelled one, and when one came due and did not fire, with the cause. A
wake that fires shows as a message that the member sent to itself, marked
**woke itself**. The web app's **Timers** pane lists the open wakes too.

### What happens at the edges

| When | What the daemon does |
|---|---|
| The daemon was down at the wake's time | The wake fires when the daemon starts, and its text says how late it is |
| You turned wakes off | An open wake does not fire. The room records it as dropped |
| The member left the roster | The wake is dropped, and the room says so |
| The member or its chain is paused | The wake comes due and waits in the room as a message. The member reads it when you resume |
| The chain has no hop left | The chain pauses, as it does for a message past the limit. A wake that comes due waits for your resume, and a new wake is refused |
| A roster update runs at the wake's time | The daemon tries the wake again each half minute, for up to ten minutes. The wake's text does not call that wait late. After the last try the wake is dropped, and the room says so |
| `talkoot_enabled` is off | Open wakes wait in the **Timers** pane with the reason, and none fires |
| This daemon does not run the team at the wake's time | The wake waits for the team. A team that did not start has its problem in the list of teams, and that text counts the wakes that wait. The wake fires after the start of a daemon that runs the team, and its text says how late it is. You cannot cancel it while it waits |
| The wake waited 7 days for its team, or the team's roster file is gone | The wake is dropped. Only the daemon's log says so, because no room is open to write |

## Approve work on a schedule

A wake is one later turn inside work you started. A **routine** is work that
recurs with no post from you at all: a check of the open pull requests each
quarter hour, a brief each weekday morning. That is more authority than a
wake, so a member cannot give it to itself. The member asks with the
`talkoot_routine` tool, the request waits in the inbox as a card, and only
your approval stores the routine.

An approved task or brief runs at each of its times, in a session of its
own. [What a run does](#what-a-run-does) describes that session and its
report. This version of terva is not complete in one place, and the card
and the list of routines each say so:

- **A watcher does not run.** You can approve one, and the daemon keeps its
  schedule. No part of this version runs its command.

One limit binds only after you name what it counts: `reviews_per_day`. The
daemon counts a review request where your configuration names the commands
that ask for one, and [each review request counts](#each-review-request-counts)
says how. With no command named, the card and the list say that the limit
binds nothing.

Routines are off by default. Turn them on in your user configuration,
`$TERVA_HOME/config.json`:

```json
{
  "talkoot_enabled": true,
  "talkoot_routines": {
    "max": 5,
    "question_wait": "8h",
    "review_commands": ["^just pr-review\\b"],
    "kinds": {
      "task": { "spend_usd_per_day": 3, "reviews_per_day": 1 }
    }
  }
}
```

| Key | What it sets |
|---|---|
| `max` | The routines that one team may hold: active, paused and ended. An ended routine, and one whose file does not read, holds its place until you delete it. Routines are off below 1 |
| `kinds` | The default limits of each kind: `watcher`, `brief`, and `task`. Leave a kind or a limit out to take the built-in default. A default has the top that a member's request has: 10000 dollars a day, 1000 reviews a day, 100 wakes for one run, and 100000 runs or days |
| `question_wait` | How long [a question waits for you](#a-question-that-nobody-answers) in work that a routine began, as a duration such as `90m` or `8h`. From one minute to 7 days. Leave it out to take 4 hours |
| `merge_commands` | Patterns for a command that merges, beside the ones that terva knows. [Work that a routine began never merges](#work-that-a-routine-began-never-merges) |
| `protected_branches` | Branches that such work may not push to, beside `main`, `master`, `trunk` and each remote's default branch |
| `review_commands` | Patterns for a command that asks for a model review. [Each review request counts](#each-review-request-counts) against the `reviews_per_day` of its routine. With no pattern, nothing counts |

The key works from the user layer only, for the reason `talkoot_wakes` does.
A value that does not read turns routines off: a kind the daemon does not
know, a limit that is negative or above its top, a `question_wait` outside its range, or a `max` below 1. It never becomes a built-in
default in silence. The foot of the rail then says why. A
`question_wait` that reads stays in force beside a problem in another field,
for work of a routine that has not ended yet. So does each pattern of
`merge_commands`, `protected_branches` and `review_commands` that reads. A
pattern that does not compile is such a problem, and so is an entry that is
empty.

Restart the daemon after you turn routines on, because a member gets the
tool when its session is built. Turning routines off needs no restart. The
daemon reads the key again at each request, each approval and each resume.

### The three kinds

| Kind | What a run is | What the member writes |
|---|---|---|
| `task` | One turn in a session of its own, started from the prompt. It ends in a report to the member | A prompt of at most 4096 bytes |
| `brief` | The same, and the prompt asks for a text that the member sends to you | A prompt of at most 4096 bytes |
| `watcher` | A shell command with no model. The member gets a turn only when the output changes. This version does not run it | A command of at most 1024 bytes |

A routine recurs, so its schedule is one of the forms that recur: `every
15m`, `daily 07:30`, `daily 07:30,19:00`, `weekdays 07:30`, or `mon,thu
09:00`. A day form takes a time zone after it, such as `weekdays 07:30
Europe/Helsinki`. The shortest interval is one minute, and a day form holds
at most 48 times. A one-time schedule is refused, and the refusal names a
wake as the tool for one later turn.

### The limits

Each routine holds six limits. A member may set any of them in its request,
and a limit it leaves out takes the default of the routine's kind.

| Limit | What it bounds | `watcher` | `brief` | `task` |
|---|---|---|---|---|
| `spend_usd_per_day` | The most that the runs may spend in one day, in dollars | 1 | 1 | 5 |
| `reviews_per_day` | The most [review requests](#each-review-request-counts) that work of the routine may make in one day | 0 | 0 | 2 |
| `follow_ups` | The [follow-ups](#a-follow-up) after one run at the routine's time. Each is one more run | 0 | 0 | 2 |
| `catch_up` | Whether a time the daemon missed gets one late run | yes | no | yes |
| `end_after_runs` | The routine ends after this many runs. 0 is no such end | 0 | 0 | 0 |
| `end_after_days` | The routine ends this many days after the approval. 0 is no such end | 0 | 0 | 0 |

The columns show the built-in defaults, and `kinds` in your configuration
replaces any of them. A time counts as missed when the daemon was down at
it, and also when the daemon comes to it more than 5 minutes late, as after
a suspend of the machine. A watcher and a task catch up because a check that the
daemon missed still has to run one time. A brief does not, because a brief
that arrives six hours late is worse than no brief. No kind ends by default.
You delete a routine from its list when you no longer want it.

The defaults are read at the approval and not at the request. A card can
wait for days, and the routine takes the defaults that hold when you decide.

### Read the card

The card shows the member, the kind, the schedule, and the prompt or the
command exactly as the member wrote it, with the member's reason. You approve
what you read, so the daemon refuses text that a card cannot show as it is
stored. The refusal comes before any card opens.

- **A control character.** An escape sequence can hide a part of a command.
  A line break and a tab pass.
- **A character that a reader does not see.** A bidirectional override puts
  the text in another order. A zero-width space and a tag character hold
  text that you do not see and a model still reads. Persian, the Indic
  scripts and many emoji need a joiner or a selector, and a prompt keeps one
  where a script puts it: a joiner between two characters that you see, and
  an emoji selector after one. A run of them, or one that stands alone, is
  refused, and a command keeps none. This check takes away the cheap ways to
  hide text. It does not prove that a card shows all of a text, so read the
  prompt of a routine as you read a command.
- **Text in the field its kind does not take.** A task with a command, even
  a command of spaces, is refused, and so is a watcher with a prompt.

The rule behind the list is that each character must be one that a reader
sees. A code point with no character, a private use character, and a letter
or a mark that shows as nothing are refused too. A command also takes no
space but the plain one, because another space looks the same and does not
split a shell's words.

Your approval is for the card on your screen. The card carries a digest of
what it shows, the hidden defaults included, and the app sends it back with
the approval. When the request or a default changed after the card was
drawn, the daemon refuses the approval, and the card shows the request as it
is now for you to read again.

Below that, the card lists only the limits that differ from the defaults of
the kind, each beside its default. A card with no such list says that every
limit has its default. The short card is deliberate. A card that lists six
values each time teaches you to stop reading them, and the value that
matters is the one a member changed. An amount is compared as a number, so a
spend of 5.004 against a default of 5 shows on the card, with every digit.

One limit is on the card each time that it gives authority. A
[follow-up](#a-follow-up) is one more run with nobody present, and the
default of a task allows two. So the card of a task or a brief that allows
a follow-up says how many, also when that number is the default. A card
that allows none says nothing of follow-ups.

The room keeps the full record. The line of an approval holds all six limits
as the routine got them, the zeros included, with your name and the time.

A routine card takes an approval or a decline. You cannot edit it, and a
member cannot undo it as it can undo a roster change. To change a routine,
delete it and let the member ask again. An approval is refused, and the card
keeps the reason, in each of these cases:

- You turned routines off after the member asked.
- The team already holds `max` routines.
- The member left the roster.
- The request no longer passes the checks it passed when the member made it.

A member has one request for a routine open at a time. The next one is
refused until you decide the first, so a member in a loop cannot fill the
inbox with the same card.

Only a `native` member has `talkoot_routine`, and a roster's `tools` list
narrows it as it narrows `talkoot_wake`. A member in every posture calls it
with no approval card, because the tool only asks. The routine's own card
is the approval. An `ask` rule or a `deny` rule that you write for
`talkoot_routine` still decides the call.

While routines are on, a member's message to you alone needs no approval
card either, in attended work too. The foot of the rail says so.
[permissions.md](permissions.md#approval-modes) has the rule and its reason.

### What a run does

When a routine comes due, the daemon starts a **run**. A run is one turn in
a new session, apart from the session of the member that owns the routine.
A night of checks then adds nothing to the conversation that you read.

The run works as its member. It takes the member's persona, model, posture,
`tools` list and working directory from the roster, and its cost counts
against the member's daily budget and the team's. Each run also counts as
one turn against the member's daily turn cap, so a member with a cap and a
routine that runs often can pause itself. It reads the routine's prompt,
with a line that says no person started it.

The posture decides what a run can do. A member in `plan` posture has no
`bash`, `write` or `edit`, and neither has its run. A run of a member in
`ask` or `auto-edit` posture asks you before each command, and that card
waits as [a question does](#a-question-that-nobody-answers). **Always
allow** on the card lets the next run call that command with no card. A
message to you alone needs no card in any posture while routines are on, so
the member can always tell you what the run found. A message to another
member still asks, because it starts that member's turn. Give a routine to a
member whose posture lets the work run with nobody there.

A run works in its member's directory. For a member with a worktree of its
own, `workspace: worktree` or `either`, the daemon keeps one turn at a time
in that directory:

| When | What the daemon does |
|---|---|
| A time comes while the member is in a turn | That time starts no run. The room says `started no run: developer is at work in its checkout` |
| A time comes while another run of the member works | That time starts no run, and the room says that another run is still at work |
| A message for the member arrives while its run works | The message waits. The room records that it is queued, and it goes out after the run's report |

A turn that you type into the member's own session still starts at once. A
follow-up that comes due while the directory is in use waits and tries
again. A routine of a busy member therefore skips often. Give a check that
must not skip to a member that does no other work.

A member in the shared checkout has no such rule. It already works beside
every other member, and so do its runs. Two turns there that edit the same
files, or that run `git` at one moment, can collide.

A routine has a daily spend limit of its own, `spend_usd_per_day`. The
daemon counts what the routine's runs spent today, and it reads that count
at three moments.

| When | What the daemon does |
|---|---|
| A time comes, and the routine has passed its limit | That time starts no run. The room says so one time, and not at each time |
| A run's cost passes the limit while the run works | The run is stopped at its next step. No further request goes to the model. A tool that the model already asked for may still start. The run counts as failed, so the routine pauses |
| A run ends, and the routine has now passed its limit | The run reports as any run does, and the routine pauses. The room's line gives what the routine spent and its limit |

A paused routine waits for you to resume it, as a member over its budget
does. A routine that you resume on the same day starts no run until the day
changes, because the limit is for one day. A limit of 0 means that a run may
spend nothing. A run on a model with no price still runs.

The member's daily budget and the team's work another way. Each is tested
when a run starts, and a run's cost is counted when the run ends. A budget
is therefore no strict cap on runs. The runs of several routines can work at
one time, beside the member's own turn, and each started inside the budget.
A run takes none of the four places of the guard on members that work at
one time.
Together they can pass it by what they spend before they end. Each
routine's own limit bounds that amount: one run passes its routine's limit
by one response of the model at most. Keep `max` low, and keep the sum of
the routines' limits below what you can lose in one day.

A run speaks to no member. It has the roster and the team notes, and it has
no tool that sends a message, hands work off, or proposes a change. It can
ask you a question, and
[a question that nobody answers](#a-question-that-nobody-answers) ends it.
When its card allows one, it can set a
[follow-up](#a-follow-up). Its last message is its **report**. The daemon sends
the report to the member's own session, marked as the output of the member's
routine and not as an instruction. The member then decides what to tell the
team or you, and it tells you with `talkoot_send`. Its plain last message
stays in its session, because the chain has no person to reply to. One member speaks one time, so a check and its own reviewer
cannot both answer for one piece of work.

That mark is text that a model reads, and the daemon enforces nothing with
it. A run that read a pull request or a log can carry a line of it into its
report. The member that reads the report has its own posture and tools, and
it can send to a teammate with no person present. What bounds that work is
the member's posture, the hop limit of the chain, and the budgets. Give a
member that owns a routine the posture you would give it with nobody there.

The report is at most 2048 bytes. A run puts detail in a team note and names
the note in its report.

The room shows each run. The run's first line is the routine's prompt,
marked **run, with no person present**, and it is the start of a chain of
its own. No person is at
the start of that chain, and the room does not treat a message in it as an
answer to you. The report follows as a message from the routine to its
member. A member that writes to `human` inside that chain reaches the person
who approved the routine, and that message waits in the room and wakes
nobody.

The run's session stays in the list of sessions with the team's id, the
member and the kind in its title, so you can read what a run did.

| When | What the daemon does |
|---|---|
| The run's turn ends | The report goes to the member, and the member gets a turn to read it |
| The run fails, or works for more than 30 minutes | The routine pauses, and the room gives the cause. The member reads a note about it at its next turn, and gets no turn from it. You resume the routine when the cause is gone. The 30 minutes do not count the time that a question or an approval of the run waits for you |
| The run asks you a question or an approval, and nobody answers | The run ends at [the deadline](#a-question-that-nobody-answers). The routine stays active |
| You stop the run's turn, or the daemon stops in order | The run ends with no report. The routine stays active |
| The daemon died while a run worked | The next start ends that run as failed, because its cost is not on record. The routine pauses |
| The room cannot record the end of a run | The team pauses until you resume it. The routine may run again after that. The next start of the daemon ends that run as failed, because the room holds no end for it, and the routine pauses |
| The routine's last run still works at the next time | That time starts no run, and the room says so |
| The member or the team is paused | That time starts no run. The room says so one time for each cause, and not at each time |
| The daemon was down at one or more times | A routine with `catch_up` runs one time, and its text says how many times it missed and how late it is. A routine without it starts no run, and the room says so |
| A roster update changes the member, or removes it | The update stops the member's run. That is no failure, so the routine stays active. A routine whose member left the roster starts no run again |
| The routine reaches `end_after_runs` or its last day | The routine gets the status `ended` and loses its timer, and the room says so. An ended routine does not pause and does not resume. Delete it, and let the member ask for a new one |
| You turned routines off | No routine starts a run. The timers stay, for the day you turn routines on again |

### A follow-up

A run can set one **follow-up** with `talkoot_wake` when two things hold.
The routine's `follow_ups` allows one, and you turned wakes on with
[`talkoot_wakes`](#let-a-member-check-back-later). A follow-up is one more
run of the same routine after a delay. The run gives a delay in minutes and
a reason of one line, as a member does for a wake. The new run reads that
line, and it reports to the member as any run does. The member's own
session gets no turn from the follow-up.

`follow_ups` counts the follow-ups after one run at the routine's time. With
2, that run may set one follow-up, and the follow-up's run may set one more.
The run after that has no `talkoot_wake`. A run that sets no follow-up ends
the line, and each run at one of the routine's own times starts a new line.
The room holds the count, so a restart does not give a line more
follow-ups.

A follow-up is a run of its routine. It counts toward `end_after_runs` and
toward the routine's daily limit. `per_member` counts an open follow-up with
the member's own wakes, and `max_delay` bounds its delay. The list of open
wakes shows it as a routine's follow-up, and you cancel it there.

| When a follow-up comes due | What the daemon does |
|---|---|
| Nothing holds it | A new run starts. Its text gives its number in the line and the reason |
| A run of the routine still works | The follow-up waits, and tries again each 30 seconds. After 20 tries it is dropped |
| The routine is paused, ended or deleted | The follow-up is dropped, and the room gives the cause |
| You turned wakes or routines off | The follow-up is dropped, and the room says which |
| A pause holds the member or the team, or the routine has passed its daily limit | The follow-up is dropped, and the room gives the cause |
| The daemon was down | The follow-up starts its run after the next start, and the run's text says how late it is |

### A question that nobody answers

Work that a routine began may need you when you are away. A run can ask you
a question, and so can the member that reads its report. A tool call of
either can need your approval. Each is a card in the inbox, and it arrives
in your [chat](#reach-the-team-from-chat) when the team has one.

Such a card waits for `question_wait`, which is 4 hours unless you set it.
The chat holds a question for its first 30 minutes, and after that only the
app takes the answer. After the wait the card closes, and the work that
needed it ends. The daemon
never answers for you. The agent reads that nobody answered. A call that
nobody approved does not run.

| Who asked | What the daemon does at the deadline |
|---|---|
| A run | The run ends. Its member gets a note with the question and the run's last text, and gets no turn from it. The routine stays active, and it runs again at its next time |
| A run that calls a tool after the deadline | The run is stopped at that call, and it ends in the same way |
| A member, in the chain of a run's report | The daemon pauses that chain. The room's line names the member and the question, and it has a button to resume the chain. The member can still write to you, and it can send nothing to a teammate in that chain. Any other tool call stops the member's turn |

A run's question is text that the run wrote, and nobody read it first. A run
that read a log or a pull request can carry a line of it into its question.
When the team has a chat, that text leaves the daemon for the chat service,
as a member's message to you does. Before this version a run reached you
only through its member. Give a routine a prompt that you would let write to
your chat.

The deadline is for work that no person began. A question in work that you
started waits for as long as the member's turn does, as before. So does a
question in a turn that you typed into the member's session.

A run that waits holds its routine, so the routine starts no run at a time
that comes while a card of its last run is open. Keep `question_wait`
shorter than the gap between two runs when each run must happen.

To go on after a deadline, read the note or the line, and then write to the
member. Resume the chain when the member's old work in it should continue.

### Work that a routine began never merges

A run may push its own branch and open a pull request. It cannot merge, and
it cannot push to a protected branch. The member that reads the run's report
works under the same rule, for as long as its turn is in the chain that the
routine began. In work that you started, a merge reaches your approval as
every other command does.

The daemon refuses the call before your hooks and your rules see it, in
every posture. The refusal tells the agent what the rule found and to leave
the merge to you. So a run's report says what waits for you, and you merge.

[The unattended merge rule](permissions.md#the-unattended-merge-rule) lists
what the rule reads and the two keys that add to it. It also says what the
rule cannot stop. Give the daemon of unattended work a forge account that has
no right to merge to trunk, because that is the guard that holds.

### Each review request counts

A model review runs on the forge, and the daemon cannot see what it costs.
It can count the requests. So a routine has a `reviews_per_day` limit, and
each review request in work that the routine began counts against it. The
member that reads a run's report asks under the same count, for as long as
its turn is in the chain that the routine began. A request in work that you
started counts nothing.

The daemon knows no review command by itself. Name yours in
`review_commands`, as in `"^just pr-review\\b"`. The daemon then counts each
such command after your hooks and your rules allowed it, and before it
runs. A command that needs your approval therefore shows its card first, and
the count can refuse the command that you allowed. It refuses the request
past the limit, and the refusal tells the agent to say in its last message
which pull request waits for a review. The
count starts again each day, and a restart of the daemon keeps it.

The daemon counts a request that the shell runs one time, as a command of
its own. It refuses a request in a loop, in a shell in a shell, or in a
variable, because it cannot count one there. The agent then asks again in a
plain command.

[The review count of unattended work](permissions.md#the-review-count-of-unattended-work)
lists what the daemon reads and what it cannot count. The count is no
budget. A script that asks for a review passes with no count.

### Pause, resume, or delete a routine

The foot of the rail in the Talkoot app says that routines are on,
with the number that the team may hold. It lists each routine with its
member, its kind, its schedule, its next time, all six limits, and who
approved it. It shows how many runs the routine started, what its runs
spent today, and whether a run is at work now. While `review_commands` names
a command, it also shows how many reviews the routine's work asked for
today. A routine has **Pause** or **Resume**, and **Delete**. An ended
routine, and one whose file does not read, has **Delete** alone.

- **Pause** removes the routine's timer and keeps the routine. A paused
  routine still holds its place under `max`.
- **Resume** sets the timer again. It gives authority back, so it passes the
  checks an approval does. It is refused while routines are off, after the
  routine's last day, and when its member left the roster. The daemon reads
  the routine's file again first, and it runs every check of a request on
  what the file holds then.
- **Delete** removes the routine and its timer, after you confirm it. To get
  it back, a member asks again and you approve again.

A pause and a delete do not wait for the room. The routine's file changes
first, because a pause is how you stop work. When the room or the timer then
fails, the error says to pause or delete again, and the second try writes
the line that the first one could not.

The room records each of the three with your name. A pause that follows a
failed run names no person, and the room gives the failure as its cause. The
web app's **Timers** pane lists the timer of each active routine too.

A pause or a delete stops the next run and not the one at work. The run reads
the routine's file when it starts. To stop a run that is at work, open its
session and stop its turn.

### What happens at the edges

| When | What the daemon does |
|---|---|
| You turned routines off | The routines stay in the list. You can pause and delete them, and none resumes. A request and an approval are refused. A card that waits lists the limits its member set, with no default beside them, and says that the defaults are not known |
| The daemon stopped between the routine's file and its timer | The daemon sets the missing timer of each active routine when it starts, while routines are on. A routine past its last day, or one whose member left the roster, gets none |
| A routine's file does not read, or holds what no approval could store | The routine gets no timer and does not resume. The list names it with the cause, it holds its place under `max`, and **Delete** clears it |
| An active routine has no timer | The list says so beside the routine, with the cause when a resume would be refused. Otherwise pause it and resume it to set the timer again |
| A routine's file exists and the room holds no approval of it | The routine gets no timer at the start and does not resume. This is the state after an approval that failed part way and could not remove its file. The list says so, and **Delete** clears it |
| The file of a deleted routine comes back, for example from a backup | The room still says that you deleted it, so it gets no timer and does not resume. The list says so, and **Delete** clears it |
| A routine's file is not the routine that you approved | The room's line of your approval holds the whole routine, and it has a seal that the file does not have. A file with another prompt, command, member, schedule or limit starts no run, gets no timer and does not resume. The list says so, and **Delete** clears it |
| A routine's file says active, and the room says that it is paused | The routine starts no run and gets no timer. Pause it again and then resume it |
| A routine's file counts fewer runs than the room shows | The room's count decides when the routine reaches `end_after_runs` |
| The room cannot record an approval | The approval fails and stores nothing: no routine, no timer, and the card waits again |
| The daemon keeps no timers | The member has the tool, and the tool refuses with that reason. The list says that routines are off, with the cause |

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

The Talkoot app's **New team** form writes this file for you. You can also
write it by hand, and the daemon reads it when it starts.

### Team keys

| Key | Required | Meaning |
|---|---|---|
| `name` | yes | The team's name |
| `title` | no | A longer title for the view |
| `home` | yes | The checkout the team works in. It must be the daemon's directory |
| `budget_usd_per_day` | yes | The team's daily spend cap in US dollars, above 0 |
| `team_budget_waived` | no | `true` waives the team cap until a person restores it. Default `false`. Member limits still apply |
| `color` | no | The team mark's colour, as `#RRGGBB`. Absent, the id picks one |
| `members` | yes | The members, below |

### Member keys

| Key | Meaning |
|---|---|
| `id` | The member's address in the room, as in `@developer`. Lower case letters, digits, and dashes. Not `human`, which is the word for the person |
| `role` | `coordinator`, `planner`, or `specialist`. A team has exactly one coordinator |
| `title` | A display title, such as `Developer` |
| `persona` | The persona the member speaks as. See [personas.md](personas.md) |
| `mark` | The member's mark, as `shape` and `color`. Absent, it comes from the persona |
| `driver` | What runs the member. Default `native`. See [Drivers](#drivers) |
| `model` or `tier` | The model, as `provider/id` or a bare id, or a tier: `weak`, `medium`, `strong`, or `cheap`. Not both. A bare id uses the team's default provider when that provider lists it. See [Naming a model in a setting](models.md#naming-a-model-in-a-setting) |
| `posture` | The member's approval mode. Default `auto-edit` for `workspace: worktree` or `either`, `plan` otherwise. A movable member runs in `plan` while it is outside its worktree |
| `workspace` | `shared`, the team's home checkout, `worktree`, a worktree leased for the member, or `either`, which starts in the home checkout and lets the member move. Default `shared` |
| `reviewer` | `true` lets a specialist close a ticket another member worked |
| `budget_usd_per_day` | The member's own daily spend cap, no higher than the team's |
| `turns_per_day` | A daily cap on the member's turns |
| `tools` | Narrows the member to the named tools. See below |
| `idle_stop` | For a worker member: how long its process stays up with no turn, such as `30m`, or `off`. Default `30m`, shortest `1m` |

The daemon checks the roster when it loads it, and names every problem it
finds. Beyond the formats above, the rules are these:

- At most one member can write to the shared checkout. Give every other
  member `posture: plan`, `workspace: worktree`, or `workspace: either`.
- `posture: yolo` needs `workspace: worktree`, and a worker member can never
  have it.
- `workspace: either` is for a native member only.
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
3. The built-in set: `coding`, `personal-assistant`, `research`,
   `decision-council`, `documentation`, and `review-pair`. Every member of a
   built-in team runs on the native driver, so each one works on a fresh
   install.
4. The repository's own, in `.terva/talkoot/*.md`, named `repo:<name>`. A
   repository template never hides another.

A repository template is listed by name only until you trust the workspace,
because a cloned repository controls it. terva reads at most 64 template
files of at most 64 KiB each from the repository, follows no symbolic links,
and skips `README.md`.

When no template fits, choose **Build a team** in the **New team** form. The
builder starts empty or from any template. It edits each member with the
member card's fields and holds the charter on the same screen. **Preview**
sends the composed roster to `talkoot.preview` as `text`, so the daemon
checks every roster rule before you create the team.

To make a template from a team you tuned, choose **Save as template** beside
the team's budget in the Talkoot app, or call `talkoot.save_template`. terva
writes the roster to `$TERVA_HOME/talkoot-templates/<name>.md`. It keeps the
members, the budget, and the charter, and leaves out `home`, `color`, and
`team_budget_waived`, which belong to the one team. A saved name that the
built-in set also uses hides the built-in template.

## Drivers

| Driver | What runs the member | What it needs |
|---|---|---|
| `native` | A terva session inside the daemon | Nothing more |
| `claude` | Claude Code, as a worker | `claude` on `PATH`, signed in with its own login, and `external_workers_enabled` |
| `terva:portable` | `terva rpc --portable`, as a worker | `external_workers_enabled` |
| `terva` | `terva rpc`, as a worker | `external_workers_enabled` |

A `native` member can use every seat tool: `talkoot_send`,
`talkoot_handoff`, `talkoot_roster`, `talkoot_note_write`,
`talkoot_note_read`, `talkoot_propose`, and `ask_user_question`. With wakes
on, it also has `talkoot_wake`
([Let a member check back later](#let-a-member-check-back-later)).

A `claude` or `terva:portable` member reaches the team through the Talkoot
MCP bridge, which gives it `talkoot_send`, `talkoot_handoff`,
`talkoot_roster`, `talkoot_propose`, and `ask_user_question`. Its proposal
waits in the inbox under the member's name, as a native member's does. Each
call takes the same path as a
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
and the next message wakes it. A member in a worktree, native or worker, keeps
its lease across a restart. If the lease fails, the delivery fails, and the
member never falls back to the home checkout.

A native member with `workspace: worktree` runs its session in its own
worktree: `read`, `write`, `edit`, and `bash` work in that checkout. Its
session file stays with the team's other sessions. A member that moves between
its worktree and the home checkout gets a fresh session, and a member that
leaves its worktree gives it back. A worktree that holds work is kept, with its
claim dropped.

A native member with `workspace: either` starts in the home checkout in `plan`,
whatever its `posture` says, so it can read there and cannot write. It moves
with the `talkoot_workspace` tool, and keeps its conversation:

- `enter` takes the member's worktree lease and moves the member in. There
  the member runs in its own `posture`, and checks out a branch itself with
  `git switch <branch>`. The move applies to its next tool call, in the same
  turn.
- `leave` moves the member back to the home checkout in `plan`. The member
  commits first and runs `git switch --detach`, so another checkout can take
  its branch. The member keeps its lease, so a later `enter` returns to the
  same worktree. The lease goes back when the run starts again or the roster
  changes, never on a member's own `leave`, because giving a worktree back
  runs git in it.
- `status` says where the member works.

The tool runs no git inside the member's worktree. The daemon runs outside the
member's sandbox, so the member runs git there in its own `bash`, which its
sandbox and gate cover. `enter` still takes the lease through the worktree
engine, whose `git worktree add` runs from the home checkout, as every delivery
to a `workspace: worktree` member does. `write` and `edit` refuse git's own
files, so a member cannot plant a hook or filter for that git without an
approved `bash` call.

Each run starts a movable member in the home checkout, after a restart too.

Check the terms of your provider before you seat a member on a subscription.
[providers.md](providers.md) says why.

## Add a member

A member proposes a roster change with `talkoot_propose`, and the proposal
waits in your inbox. Each built-in template's charter tells its members to
propose when the work needs a skill no member has, or a member's model,
posture, or tools no longer fit. Its card lists each member it touches, field by field,
before and after, and it flags every change that grants a member more.
Nothing changes until you approve it.

To look for a new member, press **Find a member** at the foot of the team's
rail in the Talkoot app, then **Open the recruiter**. The recruiter opens in
the control panel, and its proposal waits in the team's conversation as a
card. From a shell, the same recruiter opens with:

```sh
terva attach --recruit docs-crew
```

The recruiter session runs the Hautoja persona, or the one `--persona` names.
It makes no model call until you write. It reads the roster, asks about the
job, and proposes a member for you to approve. It offers only the drivers
installed on this machine. [cli.md](cli.md) documents the flag.

The member card in the Talkoot app changes a member directly, and the room
records each edit as a roster line in your name.

## The team manager

The team manager is one conversation for every team on the daemon. Press
**Team manager** at the foot of the rail in the Talkoot app, then **Open the
team manager**. It opens in the control panel. Each workspace keeps one
manager, and `$TERVA_HOME/talkoot/manager.json` names its session. Every
client and every restart opens the same conversation.

The manager runs the Hautoja persona and holds no seat. It makes no model call
until you write. It has five tools:

- `talkoot_teams` lists every team on the daemon, with the members of each
  team that the daemon runs.
- `talkoot_roster` reads one team's roster, the installed drivers, and the
  persona library.
- `talkoot_propose` proposes a change to any team.
- `talkoot_templates` lists the team templates, or returns the text of one.
- `talkoot_draft` saves a new team as a draft for you to review.

The manager can draft a new team. Describe the team you want, and it starts
from a template or composes a roster. The daemon checks the draft as the
builder's preview does. It refuses a draft whose id names a team that exists, a
draft with a home, and a member this machine cannot run. The draft waits in
`$TERVA_HOME/talkoot-drafts/`, in a directory for the workspace that made
it. Each workspace has its own draft ids and holds at most 20 drafts. The first-run page and the rail show each draft with **Review** and
**Discard**. **Review** opens the builder on the draft, where you edit it,
preview it, and create it. No team exists until you press **Create**, and a
create removes the draft of its id. The drafts sit beside the teams, not under
`$TERVA_HOME/talkoot/`, because a directory there would read as a team.

The manager writes no roster itself. Each proposal waits in that team's
conversation as a card, and the card names the team manager as its proposer.
Unlike a recruiter, the manager may propose to remove a member or to set the
`yolo` posture. It may also propose a change to the team itself: its title,
its colour, its charter, or its daily budget. The card shows each team field
before and after, the charter in full, and flags a raised budget. No proposal
waives the team budget. You approve or decline each of these, as you do any
other proposal.

## Guards and spend

A team spends on its own between your posts, so its limits are part of the
roster rather than an afterthought.

- **Budgets.** The team's `budget_usd_per_day` is required. A member can have
  its own budget and a daily turn cap too. The daemon checks the caps after
  each turn, so one turn can take a member past its cap before it pauses.
  A person may persistently waive only the aggregate team cost cap with
  `team_budget_waived: true`. Accounting continues. Enabling it lifts an
  existing team-spend pause, but no other pause. Restoring it counts current
  spend immediately. Templates cannot enable the waiver; create the team
  first and then make an explicit update.
- **The day** is the daemon's local day. The counters start again at local
  midnight. A pause stays until you resume it.
- **Pauses stack.** A member can be paused for its spend, its turns, the
  team's spend, a guard, a failed turn, or by a person, and it runs again
  only when every pause is lifted. A failed turn pauses its member until you
  resume it.
- **Guards** stop a team that talks to itself. They have fixed values today:

  | Guard | Limit |
  |---|---|
  | Messages between members in one chain, and wakes | 12, then the chain pauses |
  | Messages one member sends | 20 in 10 minutes |
  | The same message to the same member | dropped inside 10 minutes |
  | Members working at once | 4, and the rest wait |

- **Wakes** have two caps of their own, which you set: the open wakes of one
  member, and the longest delay. A wake starts a turn, so a member's budget
  and its turn cap count that turn
  ([Let a member check back later](#let-a-member-check-back-later)).
- **The room is sealed.** Each line of the room log carries a keyed hash of
  the one before it. A line that fails the check pauses the team.

## Permissions

A member's posture is its approval mode, the same `plan`, `ask`,
`auto-edit`, `workspace`, and `yolo` a session has
([permissions.md](permissions.md)). You cannot change a member session's
approval mode from its session. The roster sets it.

A client needs the `steer` capability to create a team, change its roster,
post to it, answer for it, pause it, or resume it. It needs the same to
cancel a wake and to pause, resume or delete a routine. The verb for a
routine needs `spend` too, because a resume gives back the authority to
spend. A `viewer` can watch a
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

A talkoot keeps its own files under `$TERVA_HOME/talkoot/<id>/`:

| Path | Holds |
|---|---|
| `talkoot.md` | The roster and charter |
| `room.jsonl` | The room log: every message, roster change, pause, and answer |
| `room.key`, `room.head` | The seal's key and the last line it covers. The sandbox denies both to every agent |
| `notes/<member>/` | Notes a member writes for the team, up to 200 of at most 256 KiB each |
| `proposals/` | Roster proposals and requests for a routine, with their decisions |
| `kickoff.json` | The kickoff's state |
| `routines/<id>.json` | One file for each routine that you approved |
| `chat-outbound.json` | The messages that the chat bridge holds for the brief or for the end of quiet hours |
| `watch/`, `events/` | The state of each watcher, and the files of its events. This version writes neither, because no watcher runs |

The timers are not in that directory. Each wake, follow-up, routine
schedule, brief time and end of quiet hours is a timer of the daemon, in
`$TERVA_HOME/timers/<hash of the daemon's directory>/timers.json`
([scheduled-jobs.md](scheduled-jobs.md#the-daemons-timer-service)).

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

- The terminal interface has no Talkoot view. Watch a team in the Talkoot app.
- There is no driver for Codex or for other ACP agents.
- The guard values and the daily budget period are fixed.
- A member on another machine cannot join a team.
- A worker member cannot set a wake. Only a `native` member has
  `talkoot_wake`.
- A watcher routine does not run. The daemon stores it and keeps its
  schedule.
- A routine's review limit counts only the commands that `review_commands`
  names. A script that asks for a review passes with no count.
- A worker member's question or approval has no deadline of this kind, and
  it ends no chain. Only a `native` member's turn and a run have
  [the deadline](#a-question-that-nobody-answers).
- Nothing removes the session of a run that ended. Delete or archive the
  ones you do not want.
- A worker member cannot ask for a routine, and you cannot make one yourself.
  A `native` member asks, and you approve.
