#!/usr/bin/env python3
"""Rank tool descriptions by the constraint-after-detail shape.

Triage only. Run it, then MEASURE the candidates — the sweep that produced this
script flagged 14 of 36 descriptions and exactly one of the two measured turned
out to matter. See the README: the shape predicts nothing on its own, only the
shape governing a choice the task does not already settle.

The defect the Haiku A/B caught: a rule the model must obey sits AFTER the
explanation it governs. With ALL-CAPS gone, position is the only salience left,
so a buried rule is a rule that gets missed.

This is a triage instrument, not a verdict. It ranks; a human reads.
"""
import json, re, sys

CATALOG = "packages/i18n/locales/tools/en.json"

# A sentence that constrains rather than explains.
CONSTRAINT = re.compile(
    r"^(do not|never|omit this|do these)\b"
    r"|\b(must not|cannot|can not|refuses|rejects|is rejected|are rejected"
    r"|not permitted|does not permit|only valid|is valid only|only when|only if"
    r"|must be|must always|you must)\b",
    re.I)

# A sentence that is pure explanation — the thing a constraint should precede.
SENT = re.compile(r"(?<=[.!?])\s+")


def analyse(key, text):
    paras = [p for p in text.split("\n\n") if p.strip()]
    flat = []                      # (para_idx, sent_idx_in_para, sentence)
    for pi, p in enumerate(paras):
        for si, s in enumerate(SENT.split(" ".join(p.split()))):
            if s.strip():
                flat.append((pi, si, s.strip()))
    if not flat:
        return None

    hits = []
    total = len(flat)
    for n, (pi, si, s) in enumerate(flat):
        if not CONSTRAINT.search(s):
            continue
        depth = n / total                     # how far through the whole text
        # Buried two ways: late overall, or not leading its own paragraph.
        buried_para = si > 0
        buried_text = depth > 0.5
        if not (buried_para and buried_text):
            continue
        score = round(depth * 100)
        # A constraint in a paragraph that spends sentences explaining first
        # is the exact shape session_inspect had.
        preceding = si
        hits.append({
            "score": score,
            "para": pi + 1,
            "of_paras": len(paras),
            "sent_in_para": si + 1,
            "explain_before": preceding,
            "text": s,
        })
    if not hits:
        return None
    hits.sort(key=lambda h: (-h["explain_before"], -h["score"]))
    return {"key": key, "paras": len(paras), "sentences": total, "hits": hits}


def main():
    cat = json.load(open(CATALOG))
    out = []
    for k in sorted(cat):
        r = analyse(k, cat[k])
        if r:
            out.append(r)
    out.sort(key=lambda r: (-r["hits"][0]["explain_before"], -r["hits"][0]["score"]))

    print(f"{len(out)} of {len(cat)} descriptions carry a buried constraint\n")
    for r in out:
        name = r["key"].replace("tool.", "").replace(".description", "")
        h = r["hits"][0]
        print(f"── {name}  ({r['paras']} paragraphs, {r['sentences']} sentences)")
        for h in r["hits"]:
            print(f"   para {h['para']}/{h['of_paras']}, sentence {h['sent_in_para']} "
                  f"({h['explain_before']} explaining first, {h['score']}% through)")
            print(f"     {h['text'][:150]}")
        print()


if __name__ == "__main__":
    main()
