#!/usr/bin/env python3
"""Report where two arms' assembled prompts differ, before either costs money.

Reads two `terva --dump-prompt=sizes` outputs and diffs them per tool and per
system-prompt source. `ab.sh` runs this as a pre-flight; you can also run it by
hand:

    scripts/eval/arm-diff.py .eval/<run>/results/sizes-a.txt sizes-b.txt

Why it exists. An A/B is only as good as the claim that its arms differ in the
one place you meant and nowhere else, and that claim is easy to get wrong in
ways that leave no trace in the result. The first control arm this harness ever
built was scraped out of the Go source with a regex, which swept quote
characters out of comments and produced a `bash` description that was corrupt
prose -- it ran, it scored, and the comparison it reported was fiction. A
second arm reverted 9 of 10 schema fields because the regex could not count
braces deep enough to see the tenth.

Both would have shown up here in a second: one tool's weight moving, or one
too many.

`sizes` is an offline lower bound -- it excludes live per-turn ephemeral
context and extension/MCP tool schemas that merge at runtime. That is fine for
this: those are equal across arms by construction, since the arms differ only
in text compiled in or overlaid.

Equal weight is not proof of equal text -- a same-length rewording lands here
as no difference at all. Use it to catch the arm that differs in the WRONG
place, not to prove two arms are identical.
"""
import re
import sys

ROW = re.compile(r"^\s{2}(\S+)\s+([\d,]+)\s+~?([\d,]+)\s*(.*)$")
SECTION = re.compile(r"^(system|messages|tools|tail|TOTAL)\s+([\d,]+)\s+([\d,]+)")


def parse(path):
    sections, tables, table = {}, {}, None
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if "— by weight" in line or "— by source" in line:
                table = line.split("—")[0].strip()
                tables[table] = {}
                continue
            if not line.strip():
                continue
            m = SECTION.match(line)
            if m:
                sections[m.group(1)] = int(m.group(2).replace(",", ""))
                table = None
                continue
            if table is not None:
                m = ROW.match(line)
                if m:
                    tables[table][m.group(1)] = int(m.group(2).replace(",", ""))
    return sections, tables


def main(pa, pb):
    sa, ta = parse(pa)
    sb, tb = parse(pb)
    if not ta and not tb:
        sys.exit("neither dump parsed -- is this `--dump-prompt=sizes` output?")

    print(f"{'':4}{'what':28}{'arm a':>10}{'arm b':>10}{'delta':>10}")
    print("-" * 62)
    for name in ("system", "tools", "TOTAL"):
        if name in sa or name in sb:
            a, b = sa.get(name, 0), sb.get(name, 0)
            print(f"{'':4}{name:28}{a:>10,}{b:>10,}{b - a:>+10,}")

    moved = 0
    for table in sorted(set(ta) | set(tb)):
        rows_a, rows_b = ta.get(table, {}), tb.get(table, {})
        diff = [k for k in sorted(set(rows_a) | set(rows_b))
                if rows_a.get(k) != rows_b.get(k)]
        if not diff:
            continue
        print(f"\n{table} rows that differ:")
        for k in diff:
            a, b = rows_a.get(k), rows_b.get(k)
            note = ""
            if a is None:
                note = "  (arm b only)"
            elif b is None:
                note = "  (arm a only)"
            print(f"{'':4}{k:28}{a if a is not None else '-':>10}"
                  f"{b if b is not None else '-':>10}"
                  f"{(b - a) if (a is not None and b is not None) else 0:>+10,}{note}")
            moved += 1

    print()
    if moved == 0:
        # Not fatal: a same-length rewording is a real experiment and lands
        # here as silence. But it is worth saying out loud, because the far
        # more common cause is an arm that was never actually applied.
        print("no row changed weight. either the rewording is length-neutral,")
        print("or the arm did not apply -- check the arm's source diff before paying.")
        return 1
    print(f"{moved} row(s) differ. every other row is byte-identical across the arms.")
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: arm-diff.py <sizes-a.txt> <sizes-b.txt>")
    raise SystemExit(main(sys.argv[1], sys.argv[2]))
