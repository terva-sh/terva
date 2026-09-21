#!/usr/bin/env python3
"""Print what the advisory reviews on a pull request actually said.

No status context carries this. `terva-review/code` reports pass or fail and
nothing else, so before this existed, reading a finding meant opening a
browser or hand-decoding base64 out of the API.

🪤 A review publishes in two different shapes, and reading only one of them
reports a healthy run as nothing at all:

  findings  a PR REVIEW object whose body holds `terva-result:<base64>`.
            That base64 decodes to a single JSON OBJECT.

  clean     NO review object at all. Under `publication-policy: summary` the
            action instead maintains ONE issue COMMENT holding
            `terva-clean-results:<base64>`, and that base64 decodes to an
            ARRAY of {key, result} entries.

So `GET /pulls/N/reviews` returns an empty list for every clean run, which
looks exactly like "no review was ever requested". Both sources are read here
and merged on time order.

Usage: pr-reviews.py --reviews FILE --comments FILE --pr N
"""

import argparse
import base64
import json
import re
import sys

def decode(marker, body):
    """Pull `marker:<base64>` out of a comment body and decode the JSON.

    Returns None rather than raising. A body that carries no marker is the
    normal case (most comments are people talking), and a malformed one is
    not worth failing the whole listing over.
    """
    m = re.search(re.escape(marker) + r":([A-Za-z0-9+/=]+)", body or "")
    if not m:
        return None
    try:
        return json.loads(base64.b64decode(m.group(1)))
    except Exception:
        return None


def load(path):
    """Read a JSON array, or fail loudly.

    Deliberately not forgiving. An earlier version returned [] for anything it
    could not parse, which turned a failed fetch into the sentence "no
    advisory review has been published". pr.sh checks the HTTP status before
    calling this, so a body that does not parse here means something changed
    that nobody has looked at, and silence would hide it twice.
    """
    with open(path) as f:
        d = json.load(f)
    if not isinstance(d, list):
        raise SystemExit("%s: expected a JSON array, got %s"
                         % (path, type(d).__name__))
    return d


def when_of(res, fallback):
    """When the review itself finished, not when its container appeared.

    The clean-results comment is MAINTAINED across runs: the action rewrites
    one comment and keeps up to 32 results inside it. Its created_at is
    therefore the moment the FIRST clean result landed, and reading it stamps
    every later result with that same stale instant. They then sort as a tie
    and print in an order that has nothing to do with when they ran.

    Each result carries its own finishedAt, so use that and keep the
    container's timestamp only as a fallback.
    """
    return res.get("finishedAt") or res.get("startedAt") or fallback or ""


def collect(reviews_path, comments_path):
    """Every published result, as (when, result), oldest first."""
    rows = []
    for r in load(reviews_path):
        res = decode("terva-result", r.get("body"))
        if res:
            rows.append((when_of(res, r.get("submitted_at")), res))
    for c in load(comments_path):
        arr = decode("terva-clean-results", c.get("body"))
        if isinstance(arr, list):
            for e in arr:
                res = e.get("result") if isinstance(e, dict) else None
                if res:
                    rows.append((when_of(res, c.get("created_at")), res))
    rows.sort(key=lambda t: t[0])
    return rows


def short(sha, n=12):
    return (sha or "?")[:n]


def render(rows):
    for when, res in rows:
        rev = res.get("revision") or {}
        ex = res.get("execution") or {}
        findings = res.get("findings") or []
        print("  %s  head %s  base %s"
              % (when[:19] or "?", short(rev.get("headSha")),
                 short(rev.get("baseSha"))))
        print("    profile %s, model %s, %s, %d finding(s)"
              % ((res.get("profile") or {}).get("id", "?"),
                 ex.get("model", "?"),
                 (res.get("completion") or {}).get("state", "?"),
                 len(findings)))

        # Which project config reached the prompt. Absent means the review ran
        # on the action's shipped templates, which is worth seeing: it is the
        # difference between a finding judged against this repository's stated
        # conventions and one judged against nothing.
        cfg = ex.get("reviewConfig")
        if isinstance(cfg, dict):
            files = cfg.get("files") or {}
            named = ", ".join("%s=%s" % kv for kv in sorted(files.items()))
            print("    config %s at %s (%s)"
                  % (cfg.get("path", "?"), short(cfg.get("commitSha")),
                     named or "no entries"))

        for f in findings:
            sev = (f.get("severity") or "?").lower()
            anchor = f.get("anchor") or {}
            where = anchor.get("path") or ""
            if where and anchor.get("startLine"):
                where = "%s:%s" % (where, anchor["startLine"])
            print("    [%s] %s" % (sev, f.get("title", "(untitled)")))
            if where:
                print("           %s" % where)
        if not findings:
            print("    no findings")
        print()

    # Said every time on purpose. The review is advisory, and the failure mode
    # this tooling invites is treating a green line as permission.
    print("A clean review does not authorize a merge, and a finding is a claim")
    print("to weigh rather than an order. Record a disposition either way:")
    print("  docs/reviews/pr-reviews.md")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--reviews", required=True)
    ap.add_argument("--comments", required=True)
    ap.add_argument("--pr", default="N")
    a = ap.parse_args()

    rows = collect(a.reviews, a.comments)
    if not rows:
        print("  no advisory review has been published on this pull request")
        print("  ask for one with: just pr-review %s" % a.pr)
        return 0
    render(rows)
    return 0


if __name__ == "__main__":
    sys.exit(main())
