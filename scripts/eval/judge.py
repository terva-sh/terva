"""Blind pairwise judge for the prose rules a regex cannot see.

The forbid regex in scenarios.json scores token rules: an em dash, a banned
word, a curly quote. It is silent on the rules that carry most of the style
standard, such as "say what a thing does, not how it feels" and "if the
sentence could appear unchanged in another project's documentation, cut it".
The always-on-skills eval measured every one of those at floor in all four
arms, so they stayed untested rather than confirmed.

This reads the transcripts that ab.sh already wrote, pairs the two arms by
scenario and repeat, and asks a judge model which reply is better on one
named property at a time.

Three commitments hold the result up.

Pairwise, not absolute. A 1-to-5 rubric drifts between runs and rounds small
differences away. A forced choice between two replies to the SAME prompt, with
ties allowed, detects a difference an absolute score buries.

The judge never sees the style rules. Asking "which better follows this style
guide" against an arm generated with that guide pinned measures whether the
pin happened, which is already known. Every rubric question below asks about
something a reader experiences, and none of them quotes a rule.

Position is randomized per call and the mapping is recorded. LLM judges carry
a large and well-documented first-position bias, so an unrandomized run
reports that bias as an effect.

Run --selftest before trusting any result. It scores hand-written pairs whose
answer is not in doubt, and it scores a reply against ITSELF. A judge that
cannot separate the obvious pairs makes a null meaningless, and a judge that
does not tie an identical pair is reporting position, not quality.

Usage:
    python3 scripts/eval/judge.py --selftest
    python3 scripts/eval/judge.py WORKDIR/results
    python3 scripts/eval/judge.py WORKDIR/results --only specificity --limit 8
"""

import argparse
import collections
import concurrent.futures as futures
import json
import math
import os
import random
import re
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))  # scripts/eval -> scripts -> repo root

# Each question names a property a reader feels. None of them quotes a rule,
# and none mentions a style guide, so the judge cannot simply detect which arm
# had the guide pinned.
#
# `invert` marks a question whose winner is the WORSE reply. Genericness is
# the clearest example: the reply that transplants more easily into another
# product's documentation is the one that says less about this one.
RUBRIC = {
    "specificity": {
        "q": ("Which answer names more concrete, checkable things (a mechanism, a "
              "number, a named part, an observable behaviour) rather than general "
              "qualities or how something feels?"),
        "invert": False,
        "why": "the flagship rule: say what a thing does, not how it feels",
    },
    "generic": {
        "q": ("Imagine copying each answer into the documentation of a completely "
              "different product. Which one would need fewer changes to fit?"),
        "invert": True,
        "why": "prose that transplants freely says nothing about its own subject",
    },
    "firstread": {
        "q": ("A tired engineer reads each answer once, at the end of a long day. "
              "Which one do they understand faster, with less need to re-read a "
              "sentence?"),
        "invert": False,
        "why": "the stated target of the standard, and the reason for the rest",
    },
}

JUDGE_PROMPT = """You are comparing two answers to the same request. Judge one property only, and ignore everything else.

THE REQUEST BOTH ANSWERED:
{prompt}

ANSWER 1:
{a}

ANSWER 2:
{b}

QUESTION: {question}

Answer with exactly one word: 1, 2, or TIE.
Choose TIE when the two are close enough that a reader would not care which they got.
Do not explain. One word."""


def final_text(path):
    """The last assistant text in a transcript, which is the answer a user reads."""
    last = None
    try:
        for line in open(path, encoding="utf-8", errors="ignore"):
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get("type") != "assistant_message":
                continue
            text = " ".join(c.get("text", "") for c in ev["message"]["content"]
                            if c.get("type") == "text").strip()
            if text:
                last = text
    except FileNotFoundError:
        return None
    return last


def ask_judge(binary, home, cwd, model, provider, prompt):
    """One judge call. Returns '1', '2', 'TIE', or None when the reply did not parse.

    The context-stripping flags matter as much as the prompt. A judge running
    with the pinned body in its own prefix has been told the answer, and a
    judge running inside this repository reads its AGENTS.md. Both would score
    the arm that matches their own instructions.
    """
    cmd = [binary, "--cwd", cwd, "--json",
           "--max-steps", "1",
           "--no-always-on-skills", "--no-builtin-skills",
           "--no-lore", "--no-memory"]
    if provider:
        cmd += ["--provider", provider]
    if model:
        cmd += ["--model", model]
    cmd.append(prompt)

    env = dict(os.environ, TERVA_HOME=home)
    try:
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=180,
                             env=env).stdout
    except subprocess.TimeoutExpired:
        return None

    text = None
    for line in out.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if ev.get("type") != "assistant_message":
            continue
        t = " ".join(c.get("text", "") for c in ev["message"]["content"]
                     if c.get("type") == "text").strip()
        if t:
            text = t
    if not text:
        return None
    m = re.search(r"\b(1|2|TIE)\b", text.strip().upper())
    return m.group(1) if m else None


def judge_pair(cfg, prompt, text_a, text_b, question, rng):
    """Judge one pair with a randomized position. Returns 'a', 'b', 'tie', or None.

    The return names the ARM that won, not the slot it sat in. The caller never
    sees which slot each arm took, which is the point.
    """
    a_first = rng.random() < 0.5
    first, second = (text_a, text_b) if a_first else (text_b, text_a)
    verdict = ask_judge(cfg["bin"], cfg["home"], cfg["cwd"], cfg["model"],
                        cfg["provider"],
                        JUDGE_PROMPT.format(prompt=prompt, a=first, b=second,
                                            question=question))
    if verdict is None:
        return None
    if verdict == "TIE":
        return "tie"
    won_first = verdict == "1"
    return "a" if won_first == a_first else "b"


def normalize_dashes(text):
    """Replace em dashes with commas, to take a visible cue away from the judge.

    The pinned arm removes almost every em dash, so an unnormalized comparison
    lets the judge separate the arms on punctuation alone. If a semantic
    verdict survives this, it is not reading the dash. Applied to BOTH arms, so
    it removes a cue rather than editing one side toward the other.
    """
    out = re.sub(r"\s*\u2014\s*", ", ", text)
    return re.sub(r",\s*,", ",", out)


def binom_two_sided(k, n):
    """Exact two-sided sign test: the chance of a split at least this lopsided."""
    if n == 0:
        return 1.0
    probs = [math.comb(n, i) * 0.5 ** n for i in range(n + 1)]
    return min(1.0, sum(p for p in probs if p <= probs[k] + 1e-12))


# Hand-written pairs whose answer is not in doubt. `better` names the side a
# competent judge must pick. These exist so a null result can be trusted: a
# judge that scores these at chance cannot be believed when it reports no
# difference between two real arms.
SELFTEST = [
    {
        "prompt": "Explain what a prompt cache is and why it matters for a coding agent.",
        "concrete": ("A prompt cache stores the tokens the model has already processed for a "
                     "fixed prefix, so a later request that starts with the same bytes skips "
                     "that work. Anthropic bills a cache read at a tenth of a fresh input "
                     "token. For a coding agent the system prompt and tool definitions run to "
                     "tens of thousands of tokens and do not change between turns, so the "
                     "saving applies to almost every request. Reordering anything early in "
                     "the prefix invalidates the cache from that byte onward."),
        "slop": ("A prompt cache is a crucial component that significantly enhances the "
                 "performance of modern coding agents. By leveraging cached context, it "
                 "fosters a more seamless and efficient developer experience. This is "
                 "particularly important in today's fast-paced landscape, where responsiveness "
                 "is key. Ultimately, prompt caching stands as a testament to the power of "
                 "thoughtful engineering, delivering meaningful value across numerous use cases."),
    },
    {
        "prompt": "Write the opening paragraph of a README for a database migration tool.",
        "concrete": ("mig applies versioned SQL files in order and records each one in a "
                     "migrations table inside the database it just changed. A migration that "
                     "fails leaves the transaction rolled back and the table unchanged, so a "
                     "rerun starts from the same place. `mig plan` prints the exact statements "
                     "it would send, and nothing else reads that output, so you can diff it "
                     "against the last release."),
        "slop": ("Welcome to mig, a powerful and modern solution for all your database "
                 "migration needs. Built with developers in mind, mig provides a seamless and "
                 "intuitive experience that streamlines your workflow. Whether you are working "
                 "on a small project or a large enterprise application, mig has you covered "
                 "with its comprehensive feature set and robust architecture."),
    },
    {
        "prompt": "Describe the benefits of adding integration tests to a service.",
        "concrete": ("An integration test starts the service against a real database and a "
                     "real queue, so it catches the failures a unit test mocks away: a missing "
                     "index that makes a query time out, a migration that never ran, a "
                     "serializer that drops a field the consumer needs. It costs about 40 "
                     "seconds a run against 2 seconds for the unit suite, which is why they "
                     "usually run on merge rather than on save."),
        "slop": ("Integration tests are absolutely essential for ensuring the reliability and "
                 "robustness of your service. They provide invaluable confidence and help "
                 "foster a culture of quality across the team. By utilizing integration tests, "
                 "teams can significantly enhance their development workflow and deliver "
                 "better outcomes for their users."),
    },
]


def run_selftest(cfg, rng, workers):
    """Prove the judge discriminates, and prove it is not just reading position."""
    print("SELFTEST 1: can the judge separate concrete prose from slop?")
    print("(the concrete side must win; a tie or a loss means the judge is not usable)\n")

    jobs = []
    with futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for case in SELFTEST:
            for name, spec in RUBRIC.items():
                # arm a = slop, arm b = concrete. On an inverted question the
                # slop side is the correct winner, because it is the reply that
                # transplants anywhere.
                want = "a" if spec["invert"] else "b"
                jobs.append((case["prompt"][:34], name, want, pool.submit(
                    judge_pair, cfg, case["prompt"], case["slop"], case["concrete"],
                    spec["q"], rng)))
        rows = [(p, n, w, f.result()) for p, n, w, f in jobs]

    good = bad = unclear = 0
    for prompt, name, want, got in rows:
        if got is None:
            mark, unclear = "UNPARSED", unclear + 1
        elif got == want:
            mark, good = "ok", good + 1
        elif got == "tie":
            mark, unclear = "tie", unclear + 1
        else:
            mark, bad = "WRONG", bad + 1
        print(f"  {prompt:36} {name:12} -> {mark}")
    print(f"\n  correct {good}, wrong {bad}, tie or unparsed {unclear} "
          f"(of {len(rows)})")

    print("\nSELFTEST 2: does the judge tie a reply against itself?")
    print("(a systematic winner here is position bias, and it would fake an effect)\n")
    jobs = []
    with futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for case in SELFTEST:
            for name, spec in RUBRIC.items():
                jobs.append((name, pool.submit(
                    judge_pair, cfg, case["prompt"], case["concrete"],
                    case["concrete"], spec["q"], rng)))
        same = [(n, f.result()) for n, f in jobs]

    counts = collections.Counter(v for _, v in same)
    print(f"  tie {counts['tie']}, arm a {counts['a']}, arm b {counts['b']}, "
          f"unparsed {counts[None]} (of {len(same)})")
    nontie = counts["a"] + counts["b"]
    if nontie:
        print(f"  sign test on the non-ties: p={binom_two_sided(counts['b'], nontie):.3f} "
              f"(want a high p, meaning no systematic side)")

    ok = bad == 0 and good >= 2 * len(rows) // 3
    print("\n  VERDICT:", "judge is usable" if ok else
          "judge is NOT usable, do not trust a null from it")
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("results", nargs="?", help="an ab.sh results directory")
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--bin", default=os.path.join(ROOT, "bin", "terva"))
    ap.add_argument("--model", default="claude-sonnet-5")
    ap.add_argument("--provider", default="anthropic")
    ap.add_argument("--only", help="one rubric key, else all of them")
    ap.add_argument("--limit", type=int, default=0, help="cap the pairs per scenario")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--seed", type=int, default=17)
    ap.add_argument("--normalize-dashes", action="store_true",
                    help="replace em dashes with commas in BOTH arms before judging")
    ap.add_argument("--dump", help="write per-pair verdicts as JSONL, with the "
                                   "word count of each arm, for confound analysis")
    args = ap.parse_args()

    if not os.path.exists(args.bin):
        sys.exit(f"judge: no terva binary at {args.bin} (run `just build`, or pass --bin)")

    rng = random.Random(args.seed)
    home = tempfile.mkdtemp(prefix="terva-judge-home-")
    cwd = tempfile.mkdtemp(prefix="terva-judge-cwd-")
    real = os.environ.get("TERVA_HOME") or os.path.expanduser("~/.terva")
    auth = os.path.join(real, "auth.json")
    if os.path.exists(auth):
        os.symlink(auth, os.path.join(home, "auth.json"))
    cfg = {"bin": args.bin, "home": home, "cwd": cwd,
           "model": args.model, "provider": args.provider}

    rubric = {k: v for k, v in RUBRIC.items() if not args.only or k == args.only}
    if not rubric:
        sys.exit(f"judge: no rubric key named {args.only!r}")

    if args.selftest:
        sys.exit(run_selftest(cfg, rng, args.workers))
    if not args.results:
        sys.exit("judge: give a results directory, or --selftest")

    spec = {s["id"]: s for s in
            json.load(open(os.path.join(HERE, "scenarios.json")))["scenarios"]}
    pairs = []
    for f in sorted(os.listdir(args.results)):
        m = re.fullmatch(r"a\.([a-z0-9-]+)\.(\d+)\.ndjson", f)
        if not m:
            continue
        sid, n = m.groups()
        b = os.path.join(args.results, f"b.{sid}.{n}.ndjson")
        if not os.path.exists(b) or sid not in spec:
            continue
        ta = final_text(os.path.join(args.results, f))
        tb = final_text(b)
        if ta and tb:
            if args.normalize_dashes:
                ta, tb = normalize_dashes(ta), normalize_dashes(tb)
            pairs.append((sid, int(n), spec[sid]["prompt"], ta, tb))

    if args.limit:
        kept, seen = [], collections.Counter()
        for p in sorted(pairs, key=lambda x: (x[0], x[1])):
            if seen[p[0]] < args.limit:
                kept.append(p)
                seen[p[0]] += 1
        pairs = kept

    if not pairs:
        sys.exit(f"judge: no a/b transcript pairs under {args.results}")
    print(f"{len(pairs)} pair(s) x {len(rubric)} question(s) = "
          f"{len(pairs) * len(rubric)} judge calls\n")

    jobs = []
    with futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        for sid, n, prompt, ta, tb in pairs:
            for name, r in rubric.items():
                jobs.append((sid, n, name, len(ta.split()), len(tb.split()),
                             pool.submit(judge_pair, cfg, prompt, ta, tb,
                                         r["q"], rng)))
        rows = [(sid, n, name, wa, wb, f.result())
                for sid, n, name, wa, wb, f in jobs]
    results = [(sid, name, got) for sid, _, name, _, _, got in rows]

    # A verdict can track a property the question never asked about. Reply
    # length is the usual one: a longer answer has more room to name things
    # and costs more to read. Dumping the word counts next to each verdict is
    # what makes that testable after the fact.
    if args.dump:
        with open(args.dump, "w", encoding="utf-8") as fh:
            for sid, n, name, wa, wb, got in rows:
                fh.write(json.dumps({"scenario": sid, "repeat": n,
                                     "question": name, "verdict": got,
                                     "words_a": wa, "words_b": wb}) + "\n")
        print(f"wrote {len(rows)} per-pair verdict(s) to {args.dump}\n")

    tally = collections.defaultdict(lambda: collections.Counter())
    per_scen = collections.defaultdict(lambda: collections.Counter())
    for sid, name, got in results:
        tally[name][got] += 1
        per_scen[(sid, name)][got] += 1

    print(f"{'question':13}{'scenario':22}{'arm a':>7}{'arm b':>7}{'tie':>6}   verdict")
    print("-" * 78)
    for name in rubric:
        for (sid, nm), c in sorted(per_scen.items()):
            if nm != name:
                continue
            print(f"{name:13}{sid:22}{c['a']:>7}{c['b']:>7}{c['tie']:>6}")
        c = tally[name]
        # `invert` already resolved: on an inverted question arm b winning
        # means arm b transplants MORE easily, which is the worse outcome.
        better, worse = ("a", "b") if RUBRIC[name]["invert"] else ("b", "a")
        nontie = c[better] + c[worse]
        p = binom_two_sided(c[better], nontie)
        note = ("no signal" if nontie == 0 else
                f"arm b better, p={p:.4f}" if c[better] > c[worse] else
                f"arm b worse, p={p:.4f}" if c[worse] > c[better] else "dead even")
        if RUBRIC[name]["invert"]:
            note += "  (inverted: fewer wins is better)"
        print(f"{'':13}{'TOTAL':22}{c['a']:>7}{c['b']:>7}{c['tie']:>6}   {note}")
        if c[None]:
            print(f"{'':13}{'':22}{'':>7}{'':>7}{'':>6}   {c[None]} call(s) did not parse")
        print("-" * 78)


if __name__ == "__main__":
    main()
