#!/usr/bin/env python3
"""Score the caps-removal A/B from the NDJSON transcripts.

Scores only what each scenario declares. A scenario with `check: manual` is
counted as unscored and listed for reading, never silently folded into a pass
rate — a summary that quietly drops the cases it could not judge is the same
failure as a silent cap on coverage.
"""
import collections, json, os, re, sys

T = os.path.dirname(os.path.abspath(__file__))
SPEC = json.load(open(os.path.join(T, "scenarios.json")))["scenarios"]
BYID = {s["id"]: s for s in SPEC}


def tool_calls(path):
    """Every tool call in one transcript, as (name, args), in order.

    terva's json mode streams a call as a `tool_use_start` naming the tool
    and a run of `tool_use_args` deltas carrying its arguments a fragment at
    a time, correlated by id. Reading only the start event gets the name but
    silently loses every argument, which would score every expect_arg
    scenario as a failure in both arms and look like "no change".
    """
    names, buf, order = {}, collections.defaultdict(str), []
    try:
        for line in open(path, encoding="utf-8", errors="ignore"):
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            t, cid = ev.get("type"), ev.get("id")
            if t == "tool_use_start" and cid:
                names[cid] = ev.get("name", "")
                order.append(cid)
            elif t == "tool_use_args" and cid and ev.get("delta"):
                buf[cid] += ev["delta"]
    except FileNotFoundError:
        return []
    out = []
    for cid in order:
        try:
            args = json.loads(buf[cid]) if buf[cid] else {}
        except json.JSONDecodeError:
            args = {}
        out.append((names.get(cid, ""), args if isinstance(args, dict) else {}))
    return out


def final_text(path):
    """The last assistant text in the transcript -- the answer the user reads.

    None when no assistant message carried text at all: a run that died before
    answering holds no evidence about the answer, and folding it in as a 0
    would charge the wording with a crash it did not cause.
    """
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


def score_final(spec, text):
    """-> True pass, False fail, None unscorable, for the `final` block.

    Scores the ANSWER, not the call. The two are separate failure surfaces:
    the schema-field run scored 20/20 on tool calls while 0/40 of its final
    answers contained the cost the model had just fetched -- every one
    acknowledged the inactive-tools note instead. Call-level scoring alone
    certifies a run in which the user never got their answer.
    """
    fin = spec.get("final")
    if not fin or text is None:
        return None
    if fin.get("forbid") and re.search(fin["forbid"], text, re.I):
        return False
    if fin.get("expect") and not re.search(fin["expect"], text, re.I):
        return False
    return True


def score(spec, calls):
    """-> True pass, False fail, None unscorable.

    A run in which the model never touched the tool under test is VOID, not a
    failure. It means the prompt did not put the description in the decision
    path at all, so the run carries no evidence either way — and averaging it
    in as a 0 would report a confident "no change" from a scenario that
    measured nothing.
    """
    names = [n for n, _ in calls]
    if spec.get("check") == "manual":
        return None
    # require_call inverts the void rule: the scenario's question IS "does
    # the model reach for the tool at all" (a proactive save, a self-status
    # check), so a missing call is the failure being measured, never a void.
    if spec.get("require_call") and spec.get("tool") not in names:
        return False
    # Void only when NOTHING decision-relevant happened. For an expect_tool
    # scenario the relevant set is {expect, failure} — spec["tool"] there names
    # the WRONG choice (write, when the right answer is edit), so testing it
    # alone marks a correct run void.
    if "engaged_tools" in spec:
        # Scenarios where the decision point only exists once the model has
        # engaged the task by ANY of several routes (edit, write, or a bash
        # one-liner all count as "did the fix"). A run that never engaged
        # carries no evidence about the rule under test.
        if not (set(spec["engaged_tools"]) & set(names)):
            return None
    elif "expect_tool" in spec:
        relevant = {spec["expect_tool"]} | ({spec["failure_tool"]} if spec.get("failure_tool") else set())
        if not (relevant & set(names)):
            return None
    elif "expect_no_tool" not in spec and spec.get("tool") not in names:
        return None
    if "expect_no_tool" in spec:
        return spec["expect_no_tool"] not in names
    if "expect_calls" in spec:
        want = spec["expect_calls"]
        return names.count(spec["tool"]) >= want
    if "expect_tool" in spec:
        # The FIRST decision-relevant call, not the first call. A model that
        # reads notes.txt before changing it is behaving correctly, and
        # scoring that preparatory read as a miss would mark both arms wrong
        # and report a confident "no change" that means nothing.
        want, bad = spec["expect_tool"], spec.get("failure_tool")
        for n in names:
            if n == want:
                return True
            if bad and n == bad:
                return False
        return False
    if "illegal_combo" in spec:
        # Passes unless the model builds the forbidden call. A scenario that
        # only rewards the right answer cannot tell "knew the rule" from "the
        # task made the right answer obvious"; this one is only reachable by a
        # model that does NOT know the rule.
        combo = spec["illegal_combo"]
        field, ok_values = combo["field"], set(combo["only_with"])
        for n, a in calls:
            if n != spec["tool"] or field not in a:
                continue
            if str(a.get(combo["guard"], "")) not in ok_values:
                return False
        return True
    if "arg_regex" in spec:
        # Scores the CONTENT of an argument rather than which tool was picked.
        # Some text governs how a call is written, not whether it is made: both
        # arms of the `cd` question call bash every time, and the whole
        # difference is what the command string starts with. expect_tool cannot
        # see that and expect_arg only tests equality against a known value.
        #
        # EVERY matching call must be clean, not just one — the behaviour under
        # test is a habit, and a model that writes one bare command and three
        # prefixed ones has the habit.
        rule = spec["arg_regex"]
        field, forbid = rule["field"], rule.get("forbid")
        seen = False
        for n, a in calls:
            if n != spec["tool"] or field not in a:
                continue
            seen = True
            if forbid and re.search(forbid, str(a[field])):
                return False
        # Never called it with that field: no evidence either way, so void
        # rather than a pass. Passing here would score "the model did nothing"
        # as "the model obeyed".
        return True if seen else None
    if "forbid_call" in spec:
        # Same philosophy as illegal_combo: passes unless the model builds the
        # forbidden call, because only a model that does not know the rule can
        # reach it. Arguments are matched as their JSON dump, so the regex sees
        # into any argument shape without naming fields.
        fc = spec["forbid_call"]
        for n, a in calls:
            if n == fc["tool"] and re.search(fc["args_re"], json.dumps(a), re.I):
                return False
        return True
    if "expect_arg" in spec:
        for n, a in calls:
            if n != spec["tool"]:
                continue
            if all(a.get(k) == v for k, v in spec["expect_arg"].items()):
                for bad in spec.get("forbid_arg", []):
                    if a.get(bad):
                        return False
                return True
        return False
    if spec.get("require_call"):
        return True   # the call happened and nothing further was asserted
    return None


def verdict_for(op, np_):
    if op == 1.0 and np_ == 1.0:
        # A scenario both arms ace measures nothing: the text was not the
        # deciding factor, so it cannot detect a difference either way.
        return "no signal (both 100%)"
    if op == np_:
        return "no change"
    if np_ < op:
        return f"REGRESSION  -{(op-np_)*100:.0f}pp"
    return f"improvement +{(np_-op)*100:.0f}pp"


def main(outdir):
    res = collections.defaultdict(lambda: collections.defaultdict(list))
    fin = collections.defaultdict(lambda: collections.defaultdict(list))
    for f in sorted(os.listdir(outdir)):
        m = re.fullmatch(r"(a|b)\.([a-z0-9-]+)\.(\d+)\.ndjson", f)
        if not m:
            continue
        arm, sid, _ = m.groups()
        if sid not in BYID:
            continue
        path = os.path.join(outdir, f)
        res[sid][arm].append(score(BYID[sid], tool_calls(path)))
        if "final" in BYID[sid]:
            fin[sid][arm].append(score_final(BYID[sid], final_text(path)))

    print(f"{'scenario':26}{'differs in':22}{'arm a':>10}{'arm b':>10}   verdict")
    print("-" * 84)
    manual, totals = [], {"a": [0, 0], "b": [0, 0]}
    ftotals = {"a": [0, 0], "b": [0, 0]}
    for sid in [s["id"] for s in SPEC]:
        arms = res.get(sid)
        if not arms:
            continue
        row = {}
        for arm in ("a", "b"):
            vals = [v for v in arms.get(arm, []) if v is not None]
            row[arm] = (sum(vals), len(vals))
            totals[arm][0] += sum(vals)
            totals[arm][1] += len(vals)
        if row["a"][1] == 0 and row["b"][1] == 0:
            manual.append(sid + " (no scorable run: the tool was never called)"
                          if BYID[sid].get("check") != "manual" else sid)
            continue
        o, n = row["a"], row["b"]
        op = o[0] / o[1] if o[1] else 0
        np_ = n[0] / n[1] if n[1] else 0
        print(f"{sid:26}{BYID[sid]['removed'][:20]:22}{o[0]}/{o[1]:<8}{n[0]}/{n[1]:<8} "
              f"{verdict_for(op, np_)}")
        # The final-answer row sits directly under its scenario's call row so
        # the pair reads as one experiment. They are tallied apart: mixing the
        # two would let a fixed answer mask a broken call, or the reverse.
        if sid in fin:
            frow = {}
            for arm in ("a", "b"):
                vals = [v for v in fin[sid].get(arm, []) if v is not None]
                frow[arm] = (sum(vals), len(vals))
                ftotals[arm][0] += sum(vals)
                ftotals[arm][1] += len(vals)
            fo, fn = frow["a"], frow["b"]
            fop = fo[0] / fo[1] if fo[1] else 0
            fnp = fn[0] / fn[1] if fn[1] else 0
            print(f"{'  ... final answer':26}{'':22}{fo[0]}/{fo[1]:<8}{fn[0]}/{fn[1]:<8} "
                  f"{verdict_for(fop, fnp)}")

    print("-" * 84)
    for arm in ("a", "b"):
        got, tot = totals[arm]
        print(f"  {arm:4} {got}/{tot}" + (f"  ({100*got/tot:.0f}%)" if tot else ""))
    for arm in ("a", "b"):
        got, tot = ftotals[arm]
        if tot:
            print(f"  {arm} final {got}/{tot}  ({100*got/tot:.0f}%)")
    if manual:
        print("\nunscored, read by hand:", ", ".join(manual))


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else os.path.join(T, "results"))
