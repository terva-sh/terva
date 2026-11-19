# Always on: a tour

In this tour you turn on the always-on features of a team, and you watch
each one work. At the end you have asked a member for a check on a schedule,
approved its card, read what a run of it did, paused it, and seen a member
wake itself. [The introduction](always-on.md) says what the parts are, and
[talkoot.md](talkoot.md) is the reference for each.

The tour takes about 20 minutes. Most of that is the wait for a timer.

Stops 1 to 6 were walked on a `terva web` daemon built from commit
`32ecfe1c1` on 2026-11-19, with a scripted model in place of a provider. The
quoted texts are the ones that daemon showed. Your model writes its own
words and picks its own commands, so the members' messages differ. The
cards and the room's lines have the same form. Stop 7, the chat, was not
walked, because it needs an account on a chat service.

That walk met two more cards: one for the `talkoot_routine` call, and one for
the member's message to you. Neither opens now. Stops 3 and 5 were walked
again the same day, on a daemon with that change, and they say what it
showed.

## Before you start

You need three things:

- A terva that has `terva web` ([web.md](web.md)), with a provider you are
  signed in to.
- A git checkout that the team may work in. Use one you can throw away.
- Ten cents to a few dollars of model spend, by the model you use. The tour
  starts a handful of turns.

## 1. Turn the parts on

Each part is off by default. Put the keys in your user configuration,
`$TERVA_HOME/config.json`:

```json
{
  "talkoot_enabled": true,
  "talkoot_wakes": { "per_member": 3, "max_delay": "24h" },
  "talkoot_routines": { "max": 5 }
}
```

Start the daemon in the checkout, and open the Talkoot app:

```sh
cd ~/src/my-project
terva web
```

The app is at `/talkoot/` on the address that `terva web` prints. A daemon
that was already up reads the keys when it starts, so restart it.

## 2. Make a team

1. Type your name in **Your name in the room**. The room records it beside
   each thing you do.
2. Press **Coding team**. Name the team `crew`, and press **Preview**.
3. Read the preview. It lists six members. `mieli` is the coordinator, in
   `plan` posture. `developer` is the one member in `auto-edit` posture.
4. Press **Create the team**, and then **Skip them** on the **Start the
   team** card.

Look at the foot of the rail, below the members. It says what you turned on:

```text
Wakes on: a member may hold 3, each at most 24h ahead
Routines on: the team may hold 5
```

If a line is missing, the key did not read. The rail gives the cause for a
value that the daemon refused. A `config.json` that does not parse is
reported on standard error when the daemon starts.

## 3. Ask a member for a routine

A routine is work on a schedule with no post from you. A member cannot give
one to itself. It asks, and you approve.

Post this in the composer:

```text
@developer Check the open pull requests every 5 minutes, and report which ones wait for a review.
```

The tour asks `developer` and not the coordinator for a reason. A run works
with the tools of its member's posture. The coordinator is in `plan`
posture, which has no `bash`, so its run could read files and nothing else.

One card opens, and it is the routine. The call of `talkoot_routine` itself
needs no approval in any posture, because it only draws this card. The card
reads like this:

```text
developer asks for a task routine, every 5m
Schedule: every 5m
Each run reads this prompt:
  List the open pull requests. Report which ones wait for a review.
sothr asked for a check of the open pull requests on a schedule.
Every limit has the default of its kind.
Each run works with no person present, in a session of its own, and ends
in a report to its member. A routine that passes its daily spend limit
pauses, and a run is stopped when its cost passes that limit.
Nothing counts a review request, so the review limit binds nothing. The
talkoot_routines.review_commands key names the commands that ask for a
review.
A run may set a follow-up, which is one more run with no person present.
This routine allows 2 after each run at its time, while wakes are on.
```

The line after the prompt is the member's own reason for the request. The
line about a review request says the truth about this configuration: it
names no review command, so nothing counts one.

Read the prompt as you read a command. It is what runs each time, with
nobody there. Then read the limits. This card says that each one has its
default. A card whose member changed a limit lists that limit beside the
default.

Press **Approve**.

## 4. See what you approved

The room records the approval with every limit, the ones the card did not
list included:

```text
sothr approved a task routine of developer, every 5m (Spend in one day:
$5.00; Review requests in one day: 2; Follow-ups after one run: 2; After a
missed time: one late run; Ends after this many runs: no end; Ends after
this many days: no end)
```

The foot of the rail now lists the routine, with its next time:

```text
task developer: every 5m next 09:18
```

Open the row to read the rest:

```text
List the open pull requests. Report which ones wait for a review.
Spend in one day             $5.00
Review requests in one day   2
Follow-ups after one run     2
After a missed time          one late run
Ends after this many runs    no end
Ends after this many days    no end
Approved by sothr, 09:13
Runs started: 0
Spent today: $0.00 of $5.00
```

**Pause** and **Delete** are below it. The web panel's **Timers** pane lists
the same timer.

## 5. Read a run

Wait for the time in the rail. The room then shows the start of a run:

```text
A routine of developer   run, with no person present
List the open pull requests. Report which ones wait for a review.
```

The run is one turn in a new session. It is not in the developer's own
session, so a night of checks adds nothing to the conversation that you
read. While it works, the rail says `a run is at work`.

The developer's posture asks before each command, and so does its run. When
the run calls `bash`, a card opens:

```text
Approve tool: bash
just pr-status 12
```

Press **Allow**. The card also offers **Always allow “just pr-status”**,
which lets the next run call that command with no card. A card of a run
does not wait for ever. It closes after `question_wait`, which is 4 hours
unless you set it, and the run then ends.

When the turn ends, the run's last message goes to its member as a report:

```text
A routine of developer → developer   report
Pull request 12 has a review. Pull request 13 waits for one.
```

The member then gets a turn to read the report, and it decides what to tell
you. It tells you with `talkoot_send`. While routines are on, a message to
you alone needs no approval in any posture, so the message is in the room
at once:

```text
developer → sothr
The check ran. Pull request 13 waits for a review.
```

The member's plain last message stays in its own session, because no person
started this chain.

That was one card after the routine's own: the run's command. It is what
the `auto-edit` posture costs when nobody is there. A card of unattended
work waits for `question_wait` and then ends the work. For a routine that
must work alone, use **Always allow** on its commands. The second walk did,
and its run reported with no card at all. Or give its member the `workspace`
posture, in which the built-in tools run inside the sandbox with no card.
The tour did not walk that posture.

A time that comes while a run still works starts no run, and the room says
so:

```text
A routine of developer started no run: its last run is still at work
```

## 6. Pause the routine, and try a wake

Press **Pause** beside the routine. The rail shows `paused`, with **Resume**
in place of **Pause**, and the room records it:

```text
sothr paused a routine of developer
```

A pause stops the next run and not one that is at work. Press **Delete**
when you no longer want the routine. The app asks first: "Delete this
routine? A member must ask again, and you must approve again, to get it
back."

Now the smaller tool. A wake is one later turn inside work that you started.
Post this, to the coordinator:

```text
Look at the CI run of pull request 12 again in 2 minutes.
```

No card opens. A wake needs no approval, because your two caps bound it. The
room records what the member set:

```text
mieli set a wake for 09:25: Read the CI run of pull request 12 on the forge.
```

The foot of the rail lists the open wake below the `Wakes on` line, with a
button that cancels it:

```text
09:25
mieli: Read the CI run of pull request 12 on the forge.
```

Wait for the time. The room then shows the wake as a message that the
member sent to itself, marked **woke itself**, and the member's turn starts.

## 7. Reach the team from your phone

This stop needs a chat connector with a paired account, and the tour did
not walk it. [Reach the team from chat](talkoot.md#reach-the-team-from-chat)
has the steps. In short:

1. Set the connector up with `terva bot setup --connector NAME`, pair your
   account with `/start`, and stop that bot.
2. Add the key, and restart the daemon:

   ```json
   {
     "talkoot_chat": {
       "talkoot": "crew",
       "connector": "telegram",
       "person": "sothr",
       "brief": ["07:30", "19:00"],
       "quiet": "22:00-07:00"
     }
   }
   ```

3. Write to the bot. Your message enters the room as your post to the
   coordinator. Send `/status` for the team's state, and `stop` to pause the
   whole team.

A member's question arrives in the chat, and so does the last word of work
that finished. Every other message waits for the brief at 07:30 or 19:00.

## What a run cannot do

A run that no person started works under two rules that your own work does
not meet. An earlier walk on the same daemon showed both. Its routine had a
limit of one review request a day, and the configuration named the review
command with `"review_commands": ["^just pr-review\\b"]`. The run asked for
two reviews, and then it tried to push to `main`.

The first request ran. The second one was refused, after its approval card:

```text
tool call denied by the review limit of a routine: the routine has no
review request left today: its chains asked for 1 today, and its limit is 1
a day. Do not try another route. Say in your last message which pull
request waits for a review.
```

The push was refused before any card opened:

```text
tool call denied by the unattended merge rule: its push moves the protected
branch main. No person began this chain. Such a chain may push its own
branch and open a pull request, and it never merges. Do not try another
route. Leave the merge to a person, and say in your last message what
waits for them.
```

The run then wrote its report, and the remote's `main` had not moved.
[Work that a routine began never merges](talkoot.md#work-that-a-routine-began-never-merges)
and [each review request counts](talkoot.md#each-review-request-counts)
describe the two rules.

## Turn it off again

Delete the routine from the rail. Remove the keys from `config.json`, or set
`talkoot_enabled` to `false`, and restart the daemon. With routines off, a
routine that you left starts no run.

## What the tour did not show

| Part | Where to read |
|---|---|
| A follow-up, which is one more run that a run sets for itself | [A follow-up](talkoot.md#a-follow-up) |
| What happens to a question that nobody answers | [A question that nobody answers](talkoot.md#a-question-that-nobody-answers) |
| The brief and quiet hours | [When the team writes to you](talkoot.md#when-the-team-writes-to-you) |
| A routine's daily spend limit, and what pauses a routine | [What a run does](talkoot.md#what-a-run-does) |
| A watcher, which this version does not run | [The three kinds](talkoot.md#the-three-kinds) |
