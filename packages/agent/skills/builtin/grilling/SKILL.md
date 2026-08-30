---
name: grilling
description: Interview the user before you build, so the open decisions are theirs and not yours. Use when the user says to grill them, or asks you to interview them first.
allowed-tools: [ask_user_question, swarm_spawn]
---

# Grilling

Run this when the user asks to be interviewed before you build. It prevents the
expensive failure: building the wrong thing confidently, from a goal you and the
user each read differently.

**The trigger is the ask, not your own read of whether a request is vague.** You
are poor at noticing that you are about to invent a decision, so do not wait to
feel uncertain. When you do spot unstated decisions and the user has not asked
for an interview, name the two or three that matter and offer to run this,
rather than starting an interview nobody requested.

Leave it alone when the shape is already clear: a one-line fix, a rename, a
question with one obvious answer. An interview over a settled task spends the
user's attention for nothing.

## The design tree and the frontier

Every decision branches into the decisions that hang off it. Picking a storage
engine opens questions about migration and backup; it settles none of the
questions about the interface above it.

The **frontier** is every decision whose prerequisites are already settled: the
questions answerable *now*, without guessing at an answer you have not heard
yet. A question whose answer depends on another question still open belongs to a
later round, not this one.

Working the frontier is what keeps an interview short. Asking a downstream
question early forces the user to invent a premise, and you then build on it.

## The loop

1. **Map the frontier.** From what the user has said, list every decision that
   is open *and* answerable now. Park the rest as later branches.
2. **Send every fact question to a sub-agent.** Anything answerable from the
   environment goes to `swarm_spawn` rather than to the user: what the config
   says, whether a symbol exists, how a test behaves today. Only the questions
   downstream of that exploration wait for it; ask the rest of the frontier now.
3. **Ask the whole frontier in one round**, using a single `ask_user_question`
   call. See the format below.
4. **Recompute the frontier** from the answers. New answers settle prerequisites
   and usually open new branches.
5. **Repeat** from step 2 until the frontier is empty.
6. **State the shared understanding back** and wait for confirmation.

Done when the frontier is empty: every branch of the tree visited, nothing left
silently assumed. Do not start building until step 6 is confirmed.

## Asking a round

One `ask_user_question` call per round, never one call per question. The
`questions` array renders together and the user answers the whole round in one
pass, so a round of six costs one interruption instead of six.

- **Give every question `options`, and put your recommended answer first.** A
  user who agrees is then done in one keystroke. A question with no
  recommendation makes the user do work you could have done.
- **Set `slug`** to one to three words naming the *decision*, not the answer.
  It is the tab label the user navigates by.
- **Set `multi_select: true`** when the options are not mutually exclusive.
- **Leave `allow_custom` at its default.** Set it false only for a genuinely
  closed set, where every other answer is meaningless.
- **The cap is 8 questions.** When the frontier is wider than that, ask the
  8 whose answers unlock the most branches, and take the remainder next round.

## Two rules that decide who answers

- **Finding facts is your job.** If an answer exists in the repository, the
  filesystem, the git history, or a command's output, go and get it. Ask the
  user only what lives in their head.
- **Making decisions is theirs.** Put each real choice to them and wait. Filling
  in an answer you find obvious is how an interview produces agreement with
  yourself instead of with the user.
