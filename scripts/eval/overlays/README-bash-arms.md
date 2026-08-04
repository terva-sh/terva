# The two bash arms: run them on a model that HAS the habit

`bash-cwd-fact.json` and `bash-errexit-fact.json` test whether stating a FACT
beats stating a PROHIBITION, for two habits seen in dogfooded sessions:

- opening every command with `cd <cwd>`, which is a no-op (the bash tool sets
  `cmd.Dir` per call, so a shell's cwd never carries over between them);
- writing `set +e`, which is also a no-op (the tool spawns `sh -c`, errexit
  starts off, and a script's exit status is its last command's either way).

## Both ran at n=20 on Haiku and returned NO SIGNAL. Read why before re-running.

`bash-cd-preamble` scored 20/20 on both arms. So did `bash-set-plus-e`. Not a
win — 80 bash commands across the two experiments and **not one of them used
`cd` or `set +e`, on either arm, including the shipped control**. The habit was
absent, so there was nothing for either text to improve.

## The cause: the habit is model-driven, and Haiku does not have it

Measured across every local session (`$TERVA_HOME/sessions`), bash calls
attributed to the model that was live when each was made:

| model | bash calls | `cd`-prefixed | `set +e` | distinct cwds |
|---|---|---|---|---|
| gpt-5.6-sol | 2140 | **0.0%** | 0.1% | 5 |
| claude-opus-5 | 1113 | **99.7%** | **58.0%** | 1 |
| gpt-5.5 | 333 | **0.0%** | 0.0% | 9 |
| k3-256k | 111 | 80.2% | 0.0% | 1 |
| claude-opus-4-8 | 105 | 68.6% | 0.0% | 2 |
| glm-5.2 | 90 | 65.6% | 0.0% | 4 |
| deepseek-v4-pro | 51 | 25.5% | 0.0% | 2 |
| gemma-4-26b (ultra) | 20 | 15.0% | 0.0% | 2 |

Two things this corrects about the original framing:

1. **The `cd` habit is not anthropic-specific.** It is close to universal —
   kimi, GLM, deepseek and gemma all do it. **The OpenAI models are the
   outliers**, at 0.0% across 2,473 calls and 14 distinct working directories.
   The first write-up generalized from a single session that happened to contain
   only codex and anthropic, and concluded "a Claude prior". Wrong shape.
2. **The `set +e` habit is essentially one model.** Only `claude-opus-5` shows
   it (58.0%); `claude-opus-4-8` is at 0.0%. An arm for it has power on opus-5
   and almost nowhere else.

`glm-5.2` at 65.6% across **4 distinct cwds** is the evidence that this is
model-driven rather than an artefact of one deep repo path.

## The methodological trap, which is the reusable part

The standing rule is *measure on a weak model*, because a weak model is most
sensitive to wording. That rule is right when the text has to STEER a choice the
model is ambivalent about — and **wrong when the behaviour under test is a prior
the test model does not hold**. If the control arm does not exhibit the baseline,
the experiment cannot detect a fix, and it reports `no signal` rather than
anything about the text. Check the control arm reproduces the behaviour before
paying for the full matrix.

## Re-ran on habit-exhibiting models. Still no signal. The harness cannot see these.

`z-ai/glm-5.2` via openrouter for `cd`, `claude-opus-5` on anthropic for
`set +e` — both chosen because the table above says they HAVE the habit. Both
returned `no signal (both 100%)` again, and the control arm is the line that
matters: 164 bash commands across three models (haiku, glm-5.2, opus-5) and
**not one `cd`, not one `set +e`**, control arms included.

Hypotheses this kills, in order:

- *Shallow workspace.* No. The eval cwd is **116 characters** — deeper than the
  72-character repo path where the habit was measured — and it IS inside a git
  repo.
- *Wrong model.* No. Two models measured at 58–100% in real sessions produce 0%
  here.

What is left is session shape: the harness runs one cold turn at
`--max-steps 3`, and the habits belong to long sessions doing project work.

## The `set +e` arm tests the wrong lever, and the session proves it

Bash calls in order, anthropic era of one dogfooded session:

| calls | `cd` | `set +e` |
|---|---|---|
| 0–50 | 100.0% | 0.0% |
| 50–200 | 98.0% | 0.0% |
| 200–600 | 100.0% | 19.0% |
| 600–1114 | 100.0% | 100.0% |

**`set +e` is SELF-PRIMED.** It is absent for 200 calls, appears, and ratchets to
total. The tool description was byte-identical at call 100 (0%) and call 1000
(100%), so **the description cannot be the operative variable** — the transcript
is. Rewording it is not a fix for this, and an A/B of the wording cannot detect
one. Read that as a falsified hypothesis, not a pending experiment.

**`cd` is different and remains a live hypothesis.** It is at 100% from the FIRST
anthropic call in that session — a call that inherited a transcript carrying 62
codex bash commands with zero `cd`s. It started at ceiling against contrary
in-context evidence, which is what a trained prior looks like and what
self-priming does not.

So the honest split: the `cd` arm is worth measuring if the harness can be made
to reproduce the baseline; the `set +e` arm should not be run again as-is.

## To actually run these

Pick a model from the table with a non-zero rate in the column you are testing.
`glm-5.2` is the cheapest that shows the `cd` habit across several cwds;
`set +e` realistically needs `claude-opus-5`.

```bash
REPEATS=20 scripts/eval/ab.sh --b-overlay tools=scripts/eval/overlays/bash-cwd-fact.json \
  --only bash-cd-preamble --work .eval/cd-posture -- --provider zai --model glm-5.2
```

One more caveat that this run could not settle: the eval workspace is a shallow
scratch dir and the observed sessions ran in a deep git-repo path, over many
turns, where a model can see its own earlier `cd`s and repeat them. If a
habit-exhibiting model still scores 100% on the control, suspect the workspace
next, not the text.
