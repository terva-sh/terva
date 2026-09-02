"""Archive an A/B batch as a compact dataset that outlives the transcript cache.

    scripts/eval/export.py <results-dir> --out <dir> \
        --a-label NO-LIST --b-label FULL --verdicts .eval/verdicts.jsonl

Writes three files:

    replies.jsonl   one row per scored reply: arm, label, scenario, repeat, text
    verdicts.jsonl  the judge.py --dump rows, copied verbatim, if given
    manifest.json   provenance and a sha256 over replies.jsonl

Why this exists. `ab.sh` writes full transcripts to a cache directory, and the
fourth arm of the always-on-skills eval had its cache cleaned up between the
run and the replication that questioned it. The two batches could then never be
pooled, so a 96-run replication had to stand alone against a published summary
instead of adding to it. A batch is expensive and a cache is not durable.

What it stores, and what it deliberately does not. It stores the reply text and
the identity of each run. It does not store word counts, comma density or any
other measurement, because those are defined by structure.py and storing them
would let the archive drift from the code that computes them. Re-run
structure.py against the archive and it recomputes everything from the text.

Judge verdicts are the exception and are copied as-is. They are model output,
not a function of the text, so nothing can recompute them.

The full transcripts hold tool calls, timing and token counts, which this drops
in exchange for a file roughly a sixth the size. Keep the cache too if you want
those. This is the part worth committing.
"""

import argparse
import glob
import hashlib
import json
import os
import subprocess
import sys


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


def git(*args):
    try:
        out = subprocess.run(("git",) + args, capture_output=True, text=True,
                             check=True)
        return out.stdout.strip()
    except (subprocess.CalledProcessError, OSError):
        return None


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("results", help="an ab.sh results directory")
    ap.add_argument("--out", required=True, help="directory to write")
    ap.add_argument("--a-label", default="a")
    ap.add_argument("--b-label", default="b")
    ap.add_argument("--verdicts", help="a judge.py --dump file to copy in")
    ap.add_argument("--note", default="", help="one line of provenance")
    args = ap.parse_args()

    labels = {"a": args.a_label, "b": args.b_label}
    rows = []
    for arm in ("a", "b"):
        for path in sorted(glob.glob(os.path.join(args.results,
                                                  f"{arm}.*.ndjson"))):
            parts = os.path.basename(path).split(".")
            if len(parts) < 4:
                continue
            text = final_text(path)
            if not text:
                continue
            rows.append({
                "arm": arm,
                "label": labels[arm],
                "scenario": parts[1],
                "repeat": int(parts[2]) if parts[2].isdigit() else parts[2],
                "text": text,
            })
    if not rows:
        sys.exit(f"no scored replies under {args.results}")

    os.makedirs(args.out, exist_ok=True)
    replies_path = os.path.join(args.out, "replies.jsonl")
    with open(replies_path, "w", encoding="utf-8") as fh:
        for r in sorted(rows, key=lambda r: (r["arm"], r["scenario"],
                                             str(r["repeat"]))):
            fh.write(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n")

    digest = hashlib.sha256(open(replies_path, "rb").read()).hexdigest()

    n_verdicts = 0
    if args.verdicts:
        with open(args.verdicts, encoding="utf-8") as src:
            body = src.read()
        n_verdicts = sum(1 for line in body.splitlines() if line.strip())
        with open(os.path.join(args.out, "verdicts.jsonl"), "w",
                  encoding="utf-8") as dst:
            dst.write(body if body.endswith("\n") else body + "\n")

    scens = sorted({r["scenario"] for r in rows})
    manifest = {
        "note": args.note,
        "arms": {"a": args.a_label, "b": args.b_label},
        "replies": {"a": sum(1 for r in rows if r["arm"] == "a"),
                    "b": sum(1 for r in rows if r["arm"] == "b")},
        "scenarios": scens,
        "verdict_rows": n_verdicts,
        "source_commit": git("rev-parse", "HEAD"),
        "replies_sha256": digest,
        "produced_by": "scripts/eval/export.py",
        "analyse_with": "scripts/eval/structure.py <this-dir>",
    }
    with open(os.path.join(args.out, "manifest.json"), "w",
              encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2, sort_keys=True)
        fh.write("\n")

    size = os.path.getsize(replies_path)
    print(f"wrote {len(rows)} replies ({size:,} bytes) to {replies_path}")
    if n_verdicts:
        print(f"wrote {n_verdicts} verdict row(s)")
    print(f"sha256 {digest[:16]}...  scenarios: {', '.join(scens)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
