# Always on: a team that works while you are away

A [talkoot](talkoot.md) is a team of agents that works when you post to it.
The always-on features let the same team go on when you are not there. It
reaches you on your phone. It looks at a result again later. It works on a
schedule that you approved.

This page is the five-minute introduction. [The tour](always-on-tour.md)
walks through each part on a running daemon, and [talkoot.md](talkoot.md) is
the reference. Decision record 0029, "Unattended wakes", holds the reasons
for the shape of each part.

## One entity, and a team behind it

You talk to one member, the team's coordinator. In the `coding` template the
coordinator is `mieli`, and it speaks as Mieli. It hands work to the other
members, and it answers to you. The always-on features do not add a second assistant above
the team. They give the team you already have three things it lacked: a way
to reach you, a clock, and the right to start work that you approved before.

Every part is off until you turn it on, and each has a key of its own. Each
key works from your user configuration only. A project configuration cannot
set one, because a cloned repository must not decide what runs in your name.

## The four parts

| Part | What it gives the team | Who decides | Key |
|---|---|---|---|
| **Reach** | Your chat becomes the coordinator's thread. Your message enters the room as your post, and what the team says to you arrives in the chat | You, in configuration | `talkoot_chat` |
| **A wake** | One later turn for a member, inside work that you started: "look at this CI run again in ten minutes" | The member, under two caps that you set | `talkoot_wakes` |
| **A routine** | Work that recurs with no post from you: a check each quarter hour, a brief each morning | The member asks, and only your approval of its card stores the routine | `talkoot_routines` |
| **A run** | What a routine starts at each of its times: one turn in a session of its own, which ends in a report to the routine's member | The routine that you approved | |

A timer service in the daemon is under all four. It keeps each wake, each
routine and each brief time in a file, so a restart loses none of them. A
daemon that was down for a week does not replay the week. A timer fires one
time for the latest time that passed, and says how late it is.

## The daemon decides when your phone hears from the team

A team that sends you every message becomes a log that you stop reading. So
no member decides when the chat hears a message. The daemon sends one at
once for a reason only: a member asks you a question, you are in the
conversation, the work finished, or a decision waits for you. It holds every
other message for a **brief** at the times you set. In **quiet hours** it
holds everything except a question and a reply to you.

## Work that no person began has less authority

This is the rule that the rest is built around. A turn that you started has
the authority that its member's posture gives it. A turn that a routine
started has that authority with parts taken away.

- **It never merges.** A run may push its own branch and open a pull request.
  The daemon refuses a merge and a push to a protected branch, before your
  hooks and your rules see the call. The member that reads the run's report
  works under the same rule.
- **Its review requests are counted.** A model review runs on the forge, and
  the daemon cannot see what it costs. It counts each request against a
  daily limit of the routine, and it refuses the request past the limit.
- **Its spend has a limit of its own.** Each routine has a daily spend limit
  inside the member's budget and the team's. A routine that passes it
  pauses.
- **Its questions have a deadline.** A run can ask you a question. When
  nobody answers in time, the work ends. The daemon never answers for you.
- **A failed run pauses its routine**, and the room says why.

Both forge rules read the text of a command. They steer an agent that
follows its instructions, and a script can get past them. So give the daemon
of unattended work a forge account that has no right to merge to trunk. The
rule explains, and the forge account binds.

## What you approve, and what you see

A member cannot give itself recurring work. It asks with a tool, and the
request waits for you as a card. The card shows the schedule and the prompt
exactly as the member wrote them, and each limit that differs from the
default of its kind. Your approval is for the card on your screen. When the
request or a default changed after the card was drawn, the daemon refuses
the approval and shows the card again.

The state is on screen while a part is on. The foot of the rail in the
Talkoot app says that wakes and routines are on, lists each open wake and
each routine, and has the buttons to cancel, pause and delete. The room
records each step with a name and a time: who approved a routine, when a run
started, what it reported, and why a time started no run.

## What is not built yet

- **A watcher does not run.** A watcher is a routine that runs a shell
  command with no model, and wakes a member only when the output changes.
  You can approve one. No part of this version runs its command.
- **Chat cannot approve.** A question takes its answer from the chat. An
  approval of a tool call, a roster change or a routine waits in the app.
  Decision 0029 permits an approval from chat only from a device that you
  verified. A connector can report that, and nothing reads the report yet.
- **A merge that you ask for** still happens in your own shell. The card
  that releases your merge token for one merge is not built.
- **One coordinator for each project.** Mieli on one project knows nothing
  that Mieli on another learned.
- **A swarm child** of a member has a gate of its own, and the two forge
  rules do not reach it.

## Where to go next

| To | Read |
|---|---|
| Walk through the parts on a running daemon | [The tour](always-on-tour.md) |
| Set up the chat, the brief and quiet hours | [Reach the team from chat](talkoot.md#reach-the-team-from-chat) |
| Set the caps of a wake | [Let a member check back later](talkoot.md#let-a-member-check-back-later) |
| Read each field of a routine and its card | [Approve work on a schedule](talkoot.md#approve-work-on-a-schedule) |
| See what the two forge rules read, and what they miss | [The unattended merge rule](permissions.md#the-unattended-merge-rule) and [the review count](permissions.md#the-review-count-of-unattended-work) |
| Know what the timer service promises | [The daemon's timer service](scheduled-jobs.md#the-daemons-timer-service) |
| Drive it from a program | [controllers.md](controllers.md) |
