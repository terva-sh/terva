"""Structural A/B statistics, blocked on scenario.

Reads an `ab.sh` results directory and reports reply length, comma density,
sentence length, em dashes and banned-word fires for both arms, with a
significance test and an interval on each.

    scripts/eval/structure.py ~/.cache/terva-eval/<run>/results
    scripts/eval/structure.py <dir> --a-label NO-LIST --b-label FULL
    scripts/eval/structure.py docs/proposals/<slug>/   # an export.py archive

It reads either an `ab.sh` results directory or an `export.py` archive, and
gives the same answer from both, because the archive stores reply text and this
module recomputes every metric from it. Prefer the archive: a results directory
lives in a cache, and a cache gets cleaned.

Why this exists, and why it blocks.

The fourth arm of the always-on-skills eval reported that cutting the
house-style word list made replies 15.6 words longer at p=0.042, on an
unpaired permutation test over 48 replies per arm. A direct replication at the
same n put the effect at +2.4 words with a 95 percent interval of [-4.5, +9.2],
which excludes the original estimate. The finding was noise.

The unpaired test is what let it through. These scenarios have very different
natural lengths: in one batch sem-landing averaged 86 words and sem-stakeholder
173. An unpaired test charges all of that between-scenario spread to the error
term, even though every scenario is run in both arms and the spread cancels.
Blocking on scenario removes it. On the same 96 replies that halved the
interval, from a half-width near 13.7 words to 6.9.

So the blocked test is the primary one and the unpaired test is printed only so
older numbers stay comparable. If the two disagree, the blocked one is right.

Both are permutation tests, which assume nothing about the shape of the
distribution. The blocked test shuffles arm labels within each scenario and
never across. The interval is a bootstrap that resamples within each
scenario-and-arm cell, so it inherits the same blocking.

What this cannot tell you. It scores what a regex can count. For the rules that
need a reader, use judge.py. And an interval here describes one batch: the same
pinned arm has measured 114.2, 121.5 and 121.0 words in three batches, so treat
any cross-batch comparison as suspect and re-run the control arm instead.
"""

import argparse
import glob
import json
import os
import random
import re
import statistics as st
import sys

BANNED = (
    r"\b(additionally|crucial|delve|enduring|enhanc\w*|fostering|garner\w*"
    r"|interplay|intricate|pivotal|showcas\w*|tapestry|testament|vibrant"
    r"|utiliz\w*|leverag\w*|facilitat\w*|numerous|serves as|stands as|boasts"
    r"|in order to)\b"
)
METRICS = ("words", "commas", "sentlen", "emdash", "banned")
COUNTS = ("emdash", "banned")  # reported as totals, not means


def final_text(path):
    """The last assistant message with text, which is the reply under test."""
    last = None
    with open(path, encoding="utf-8", errors="ignore") as fh:
        for line in fh:
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            if ev.get("type") != "assistant_message":
                continue
            text = " ".join(
                c.get("text", "")
                for c in ev["message"]["content"]
                if c.get("type") == "text"
            ).strip()
            if text:
                last = text
    return last


def measure(text):
    words = len(text.split()) or 1
    sents = [len(s.split()) for s in re.split(r"(?<=[.!?])\s+", text) if s.strip()]
    return {
        "words": words,
        "commas": 100.0 * text.count(",") / words,
        "sentlen": st.mean(sents) if sents else 0.0,
        "emdash": text.count("\u2014"),
        "banned": len(re.findall(BANNED, text, re.I)),
    }


def load_archive(path):
    """Rows from an export.py replies.jsonl."""
    rows = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            rec = json.loads(line)
            rows.append((rec["arm"], rec["scenario"], measure(rec["text"])))
    return rows


def load(target):
    """Rows from an ab.sh results directory or an export.py archive."""
    if os.path.isfile(target):
        return load_archive(target)
    archived = os.path.join(target, "replies.jsonl")
    if os.path.isfile(archived):
        return load_archive(archived)
    rows = []
    for arm in ("a", "b"):
        for path in sorted(glob.glob(os.path.join(target, f"{arm}.*.ndjson"))):
            parts = os.path.basename(path).split(".")
            if len(parts) < 3:
                continue
            text = final_text(path)
            if text:
                rows.append((arm, parts[1], measure(text)))
    return rows


def cell(rows, arm, scen, key):
    return [m[key] for a, s, m in rows if a == arm and s == scen]


def blocked_effect(cells):
    """Mean over scenarios of the within-scenario arm difference."""
    return st.mean([st.mean(a) - st.mean(b) for a, b in cells.values()])


def perm_unpaired(rows, key, iters, rng):
    x = [m[key] for a, _, m in rows if a == "a"]
    y = [m[key] for a, _, m in rows if a == "b"]
    obs = abs(st.mean(x) - st.mean(y))
    pool, k, hits = x + y, len(x), 0
    for _ in range(iters):
        rng.shuffle(pool)
        if abs(st.mean(pool[:k]) - st.mean(pool[k:])) >= obs - 1e-12:
            hits += 1
    return hits / iters


def perm_blocked(rows, scens, key, iters, rng):
    base = {s: (cell(rows, "a", s, key), cell(rows, "b", s, key)) for s in scens}
    obs = abs(blocked_effect(base))
    pools = {s: base[s][0] + base[s][1] for s in scens}
    n_a = {s: len(base[s][0]) for s in scens}
    hits = 0
    for _ in range(iters):
        shuffled = {}
        for s in scens:
            p = pools[s][:]
            rng.shuffle(p)
            shuffled[s] = (p[: n_a[s]], p[n_a[s]:])
        if abs(blocked_effect(shuffled)) >= obs - 1e-12:
            hits += 1
    return hits / iters


def boot_blocked(rows, scens, key, iters, rng):
    base = {s: (cell(rows, "a", s, key), cell(rows, "b", s, key)) for s in scens}
    draws = []
    for _ in range(iters):
        per = []
        for s in scens:
            a, b = base[s]
            per.append(
                st.mean([rng.choice(a) for _ in a]) - st.mean([rng.choice(b) for _ in b])
            )
        draws.append(st.mean(per))
    draws.sort()
    return draws[int(0.025 * iters)], draws[int(0.975 * iters)]


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("results",
                    help="an ab.sh results directory, or an export.py archive")
    ap.add_argument("--a-label", default="arm a")
    ap.add_argument("--b-label", default="arm b")
    ap.add_argument("--seed", type=int, default=97)
    ap.add_argument("--iters", type=int, default=20000)
    args = ap.parse_args()

    rows = load(args.results)
    if not rows:
        sys.exit(f"no scored replies under {args.results}")
    manifest = os.path.join(args.results, "manifest.json")
    if os.path.isdir(args.results) and os.path.isfile(manifest):
        with open(manifest, encoding="utf-8") as fh:
            arms = json.load(fh).get("arms", {})
        if args.a_label == ap.get_default("a_label") and arms.get("a"):
            args.a_label = arms["a"]
        if args.b_label == ap.get_default("b_label") and arms.get("b"):
            args.b_label = arms["b"]
    scens = sorted({s for _, s, _ in rows})
    n_a = sum(1 for a, _, _ in rows if a == "a")
    n_b = len(rows) - n_a
    unbalanced = [
        s for s in scens
        if len(cell(rows, "a", s, "words")) != len(cell(rows, "b", s, "words"))
    ]

    rng = random.Random(args.seed)
    print(f"{args.a_label} vs {args.b_label}   n={n_a} / {n_b}   "
          f"scenarios={len(scens)}\n")
    if unbalanced:
        print(f"note: unequal arm counts in {', '.join(unbalanced)}; "
              f"blocking still valid, the interval is wider there\n")

    head = f"{'metric':10}{args.a_label:>11}{args.b_label:>11}{'a-b':>9}"
    print(f"{head}{'unpaired p':>12}{'blocked p':>11}   95% CI (blocked)")
    for key in METRICS:
        xa = [m[key] for a, _, m in rows if a == "a"]
        xb = [m[key] for a, _, m in rows if a == "b"]
        if key in COUNTS:
            print(f"{key:10}{sum(xa):>11}{sum(xb):>11}{'':>9}{'':>12}{'':>11}"
                  f"   (totals)")
            continue
        lo, hi = boot_blocked(rows, scens, key, args.iters, rng)
        print(f"{key:10}{st.mean(xa):>11.2f}{st.mean(xb):>11.2f}"
              f"{st.mean(xa) - st.mean(xb):>+9.2f}"
              f"{perm_unpaired(rows, key, args.iters, rng):>12.4f}"
              f"{perm_blocked(rows, scens, key, args.iters, rng):>11.4f}"
              f"   [{lo:+7.2f}, {hi:+7.2f}]")

    print(f"\nper-scenario reply length (words)")
    print(f"  {'scenario':22}{args.a_label:>11}{args.b_label:>11}{'a-b':>9}")
    for s in scens:
        a = cell(rows, "a", s, "words")
        b = cell(rows, "b", s, "words")
        print(f"  {s:22}{st.mean(a):>11.1f}{st.mean(b):>11.1f}"
              f"{st.mean(a) - st.mean(b):>+9.1f}")
    longer = sum(1 for s in scens
                 if st.mean(cell(rows, "a", s, "words"))
                 > st.mean(cell(rows, "b", s, "words")))
    print(f"  scenarios where {args.a_label} is longer: {longer}/{len(scens)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
