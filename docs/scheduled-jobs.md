# Scheduled jobs

Running terva unattended on a timer: a check every ten minutes, a report every
morning, a follow-up in two days.

A scheduled job needs no scheduler from terva. It is an ordinary headless run
under `systemd`, `cron` or `launchd`, and everything it needs already exists.
This page is the recipe and, more importantly, the three rules that keep an
unattended agent from quietly wasting money or quietly going blind.

The `terva web` daemon also keeps timers of its own, for work that the daemon
starts by itself. Those are a different thing, and
[the last section](#the-daemons-timer-service) says how they differ.

For a terva process that runs *continuously*, a chat bot or a connector, see
[deploy.md](deploy.md) instead. This page is for work that happens on a
schedule and then stops.

## The shape

```bash
terva --json --cwd ~/work/myproject --approval workspace --task ~/jobs/backfill/task.md
```

That is the whole mechanism:

| Piece | Flag |
|---|---|
| the instructions | `--task PATH`, a file; headless runs its contents |
| the workspace | `--cwd PATH` |
| headless output | `--json` (events) or `-p` (final text only) |
| what it may do | `--approval`; see [safety](#safety-and-approval-mode) |
| the schedule | a systemd timer, `cron`, or `launchd` |

The instructions live in a **file**, not in the unit. Editing `task.md` changes
what the job does on its next wake, with no `systemctl` involved, and the file
is a plain artifact you can read, diff and keep in git.

## The three rules

### 1. Each run gets a fresh session

**Do not pass `--session`, `-c` or `--resume` to a repeating job.** Without them
terva creates a new session per run, which is what you want.

A watch job's value is in the *current* observation, not in the transcript of
the previous two hundred checks. A fresh session keeps every wake the same
price, which is what makes it safe to leave something running for months. Reuse
makes the cheapest possible task grow without bound and pay compaction over and
over for history it will never use.

Fresh sessions also mean a scheduled job can never collide with a session you
have open in a terminal, because it has its own file by construction.

Session files still accumulate one per run, which is a useful audit trail. Add
`--no-session` if you would rather not keep them.

**The exception is a one-shot follow-up**, where the history *is* the point:

```bash
# "in two days, remind me to remove the feature flag we shipped"
terva --json --session ~/jobs/flag-followup.jsonl --task ~/jobs/flag-followup/task.md
```

That job runs once, so it never grows. The rule of thumb: **repeating jobs start
fresh, one-shot follow-ups may reuse.**

### 2. Cross-run state lives in a file

This is the price of rule 1, and the rule people skip.

A fresh session cannot remember the previous run. So "tell me only what changed"
and "don't alert twice for the same failure" are **not** free: an agent told to
"ping me if the job stalls" will otherwise ping you on *every single wake*.

Give the job a state file and say so in `task.md`, exactly as a well-behaved
`cron` job keeps its own high-water mark:

```markdown
Read `state.json` in this directory for what you saw last time.

Check the backfill job's progress. Compare it against `last_offset`.

- If the offset has not moved since the previous run, that is a stall: report it.
- If you already reported a stall for this same offset, say nothing.
- If the job has finished, write `done` (see below).

Before you exit, write `state.json` with the current offset, the time, and
whether you have already reported a stall.
```

### 3. A job stops itself with a marker

A timer runs until something disables it. Let the job say when it is finished by
writing a marker file, and have the wrapper act on it.

Disable the timer only when **both** are true:

- terva exited `0`, **and**
- the marker file exists.

That conjunction is the safety property, and the asymmetry behind it matters.
Failing to stop a finished job wastes a little money and is obvious in the logs.
Stopping a watch job that *silently crashed* loses the thing you were watching
for, and is invisible. So when in doubt, **keep running**, which is exactly what
requiring an explicit, positive marker gives you.

This is also why the marker is a file and not an exit code. terva's exit status
says whether *terva* succeeded, not whether your *job* is done; collapsing two
independent facts into one channel is what breaks the conjunction above. A file
keeps them separate, works the same under `cron` and `launchd`, and answers
"why did this stop?" long after the log scrolled past.

## Safety and approval mode

**Nobody is there to approve anything.** That is the one way an unattended run
differs from every interactive one, and it decides `--approval`:

- `--approval workspace` is the right starting point. It runs the built-in tools
  and reads freely, and confirms foreign side effects, which, with no human
  present, means the job simply will not do them. Design the task to stay inside
  what `workspace` allows and it will run cleanly for months.
- `--approval ask` is useless here. It confirms everything, and there is nobody
  to confirm.
- `--approval yolo` runs freely. Reach for it only deliberately: an unattended
  agent with no confirmation step, waking every ten minutes for months, is a
  large blast radius for a saved credential. Prefer narrowing the task, or a
  typed permission rule, over opening the whole surface.

See [permissions.md](permissions.md) for typed rules and the sandbox.

Two more things worth setting before you walk away:

- **Credentials.** The job runs as whatever user the timer runs as, with that
  user's `$TERVA_HOME`. Point it somewhere deliberate.
- **Cost.** Every wake is a real model call. Ten minutes apart is 144 runs a day.
  Start hourly, read the logs, and tighten from there.

## A worked example

Three files. The job checks a backfill every ten minutes and stops on its own
when the backfill finishes.

**`~/jobs/backfill/task.md`**, the instructions, per rule 2:

```markdown
Read `~/jobs/backfill/state.json` for what you saw last time.

Run `mycli backfill status --json` and compare the offset against `last_offset`.

- If it finished, write `~/jobs/backfill/done` and say so.
- If the offset has not moved and you have not already reported this offset,
  report a stall.
- Otherwise say nothing.

Always write `~/jobs/backfill/state.json` with the current offset, the time,
and whether you have reported a stall for it.
```

**`~/jobs/backfill/run.sh`**, the wrapper, per rule 3:

```bash
#!/usr/bin/env bash
# No `set -e`: we must inspect terva's exit code rather than die on it.
set -uo pipefail

JOB=backfill
DIR="$HOME/jobs/$JOB"

terva --json \
  --cwd "$HOME/work/myproject" \
  --approval workspace \
  --task "$DIR/task.md" \
  >>"$DIR/run.log" 2>&1
rc=$?

# Stop only on success AND an explicit marker. Either alone keeps the timer.
if [ "$rc" -eq 0 ] && [ -f "$DIR/done" ]; then
  systemctl --user disable --now "terva-$JOB.timer"
fi

exit "$rc"
```

**`~/.config/systemd/user/terva-backfill.service`** and its timer:

```ini
[Unit]
Description=terva: watch the backfill job

[Service]
Type=oneshot
ExecStart=%h/jobs/backfill/run.sh
```

```ini
[Unit]
Description=terva: watch the backfill job every 10 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=10min
Persistent=true

[Install]
WantedBy=timers.target
```

Then:

```bash
chmod +x ~/jobs/backfill/run.sh
systemctl --user daemon-reload
systemctl --user enable --now terva-backfill.timer

systemctl --user list-timers 'terva-*'   # when does it next run?
journalctl --user -u terva-backfill -f   # what did it do?
```

`Persistent=true` catches up a run missed while the machine was off. Drop it if a
missed check should simply be skipped.

## cron and launchd

Nothing above is systemd-specific. The wrapper is an ordinary script; only the
last line changes, because it is the only part that knows how to disable a timer.

For `cron`, have the wrapper `touch` a `stopped` file and exit early on the next
run, because `crontab` has no per-entry disable:

```cron
*/10 * * * * $HOME/jobs/backfill/run.sh
```

```bash
[ -f "$DIR/stopped" ] && exit 0    # at the top of run.sh
...
if [ "$rc" -eq 0 ] && [ -f "$DIR/done" ]; then touch "$DIR/stopped"; fi
```

For `launchd`, use a `StartInterval` job and `launchctl bootout` in place of
`systemctl --user disable`.

## When not to use this

A scheduled job is for a **long horizon**, longer than one terva session.

If you only need to wait for something during a single conversation, do not
reach for a timer. Run the command in the foreground and let `bash` block. A
timer that fires every ten minutes to answer a question you are sitting there
waiting for is slower, costlier and harder to read than simply waiting.

## The daemon's timer service

The `terva web` and `terva member` daemons hold a timer service. It belongs to
the daemon and not to you: a part of the daemon sets a timer for work it will
start by itself. A team's chat bridge sets one timer for its brief and one
for the end of quiet hours
([talkoot.md](talkoot.md#when-the-team-writes-to-you)). A team member sets a
one-time timer for each wake, which is one later turn for itself
([talkoot.md](talkoot.md#let-a-member-check-back-later)). A routine that you
approved holds one recurring timer
([talkoot.md](talkoot.md#approve-work-on-a-schedule)), and each of its times
starts a run in a session of its own. Decision record 0029, "Unattended wakes",
describes the plan the three belong to.

Use the recipe on this page for a job of your own. A system timer starts a
fresh process with nothing left over from the last run. A daemon timer fires
inside a process that is already running, and only while it runs.

The service keeps three promises, and each one answers a way that a timer in a
long-lived process goes wrong.

- **A restart keeps every timer.** The timers live in a file under
  `$TERVA_HOME/timers/`, one directory for each directory a daemon runs in. A
  timer whose time passed while the daemon was down fires once at the next
  start.
- **A gap never replays.** A daemon that was down for a fortnight does not run
  fourteen morning jobs when it starts. A recurring timer fires once, for the
  latest window, and tells its owner how many windows passed. A suspended
  laptop and a corrected clock get the same answer.
- **A crash loses no firing.** The service records a firing before it starts
  the work and clears the record after. A daemon that dies between the two
  delivers the firing again at the next start, marked as a second delivery.

A clock that moves back delays a timer and never repeats one. A timer that the
file cannot record does not fire, because a daemon that crashes in a loop on a
full disk would otherwise fire the same window at every start.

A time of day that a clock change skips fires at the moment of the change, and
never before the time you wrote. A timer whose zone the machine does not know
waits and says so. It does not fire at that hour in some other zone.

Two daemons in one directory do not share the timers. The first one holds
them, and the second one fires nothing and says why.

The service has bounds. A recurring timer has an interval of at least one
minute, and a schedule of times of day holds at most 48 times. A daemon
holds at most 1,024 timers, and one timer carries at most 16 KiB of data
for its owner. A request past a bound is refused, and the owner tells the
agent or the person that asked.

To see what a daemon will do by itself, open the **Timers** pane in the web
app. It appears once the daemon holds a timer, and it lists each one with its
schedule and its next time. A controller reads the same list with the
[`timers.list`](controllers.md) verb.

## See also

- [deploy.md](deploy.md): running terva continuously as a service
- [permissions.md](permissions.md): approval modes, typed rules, the sandbox
- [cli.md](cli.md): every flag used here
