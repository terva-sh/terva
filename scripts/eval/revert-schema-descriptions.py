#!/usr/bin/env python3
"""Revert the schema-field `description` texts in a Go tool file to an older ref.

Schema fields are the model-facing text that the i18n catalogs do NOT cover: a
tool's Description() can be swapped at runtime through locales/tools/en.json,
but the `description` inside Schema()'s JSON cannot. Testing a change to one
therefore needs a second BINARY, and this builds the source for it.

    python3 scripts/eval/revert-schema-descriptions.py \
        --from 091af729 packages/agent/tools/session_inspect.go

Run it inside an arm worktree (see build-arm.sh), never in your own tree.

Matched by property key, never by position. The keys are stable across a
rewording, the order in the source is not, and a positional swap would pair the
wrong texts while reporting a perfectly plausible count.

Braces are matched by scanning, not by regex. A regex with a fixed nesting
depth silently skips any property that nests deeper than it counts on --
`event_kinds` carries an `items` map and was passed over in exactly that way,
producing an arm that claimed to revert the schema and left one field on the
new text. A partial arm still runs, still scores, and reports a number that
looks like an answer.
"""
import argparse
import subprocess
import sys


def _skip_string(src: str, i: int) -> int:
    """Index just past the Go interpreted-string literal starting at src[i]."""
    i += 1
    while i < len(src):
        if src[i] == "\\":
            i += 2
            continue
        if src[i] == '"':
            return i + 1
        i += 1
    raise ValueError("unterminated string literal")


def _match_brace(src: str, i: int) -> int:
    """Index just past the `}` closing the `{` at src[i]. String-aware."""
    depth = 0
    while i < len(src):
        c = src[i]
        if c == '"':
            i = _skip_string(src, i)
            continue
        if c == "`":  # raw string
            i = src.index("`", i + 1) + 1
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return i + 1
        i += 1
    raise ValueError("unbalanced braces")


def _properties_block(src: str):
    """(start, end) of the body of the "properties" map, braces excluded."""
    key = '"properties":'
    at = src.find(key)
    if at < 0:
        return None
    open_at = src.index("{", at)
    return open_at + 1, _match_brace(src, open_at) - 1


def _entries(src: str, lo: int, hi: int):
    """Yield (key, body_start, body_end) for each top-level property."""
    i = lo
    while i < hi:
        c = src[i]
        if c != '"':
            i += 1
            continue
        end = _skip_string(src, i)
        key = src[i + 1 : end - 1]
        j = end
        while j < hi and src[j] in " \t\r\n":
            j += 1
        if j >= hi or src[j] != ":":
            i = end
            continue
        j += 1
        while j < hi and src[j] in " \t\r\n":
            j += 1
        brace = src.find("{", j)
        if brace < 0 or brace > hi:
            i = end
            continue
        close = _match_brace(src, brace)
        yield key, brace + 1, close - 1
        i = close


def _description(src: str, lo: int, hi: int):
    """(start, end, text) of the top-level "description" value in a body."""
    i = lo
    while i < hi:
        c = src[i]
        if c == "{":
            i = _match_brace(src, i)  # never descend into `items` etc.
            continue
        if c != '"':
            i += 1
            continue
        end = _skip_string(src, i)
        if src[i + 1 : end - 1] != "description":
            i = end
            continue
        j = end
        while j < hi and src[j] in " \t\r\n":
            j += 1
        if j >= hi or src[j] != ":":
            i = end
            continue
        j += 1
        while j < hi and src[j] in " \t\r\n":
            j += 1
        if j >= hi or src[j] != '"':
            i = end
            continue
        vend = _skip_string(src, j)
        return j, vend, src[j + 1 : vend - 1]
    return None


def descriptions(src: str) -> dict:
    block = _properties_block(src)
    if not block:
        return {}
    out = {}
    for key, lo, hi in _entries(src, *block):
        d = _description(src, lo, hi)
        if d:
            out[key] = d[2]
    return out


def revert(path: str, ref: str) -> int:
    old_src = subprocess.run(
        ["git", "show", f"{ref}:{path}"], capture_output=True, text=True
    )
    if old_src.returncode != 0:
        sys.exit(f"{path}: not in {ref}: {old_src.stderr.strip()}")
    with open(path, encoding="utf-8") as fh:
        cur_src = fh.read()

    old, cur = descriptions(old_src.stdout), descriptions(cur_src)
    if not cur:
        sys.exit(f"{path}: found no schema properties -- is this a tool file?")

    print(f"{path}: {len(old)} keys at {ref}, {len(cur)} now")
    for k in sorted(set(cur) - set(old)):
        print(f"  added since {ref}, left on current text: {k}")
    for k in sorted(set(old) - set(cur)):
        print(f"  gone since {ref}, nothing to revert:     {k}")

    # Rewrite right-to-left so each splice keeps the earlier offsets valid.
    block = _properties_block(cur_src)
    edits, same = [], []
    for key, lo, hi in _entries(cur_src, *block):
        d = _description(cur_src, lo, hi)
        if not d or key not in old:
            continue
        if old[key] == d[2]:
            same.append(key)
            continue
        edits.append((d[0], d[1], key, old[key]))

    for start, end, _key, text in reversed(edits):
        cur_src = cur_src[:start] + '"' + text + '"' + cur_src[end:]
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(cur_src)

    for _s, _e, key, _t in edits:
        print(f"  reverted: {key}")
    if same:
        print(f"  unchanged since {ref}: {', '.join(same)}")
    return len(edits)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--from", dest="ref", required=True, help="git ref to take the old text from")
    ap.add_argument("files", nargs="+", help="Go tool files to revert")
    args = ap.parse_args()

    total = sum(revert(p, args.ref) for p in args.files)
    # A patch that applied to nothing produces an arm identical to its
    # control, which then scores as a confident "no change".
    if total == 0:
        sys.exit("NOTHING CHANGED -- the arm would be identical to its control")
    print(f"reverted {total} schema descriptions to {args.ref}")


if __name__ == "__main__":
    main()
