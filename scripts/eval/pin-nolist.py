"""Build the NO-LIST arm: the full house-style body minus the word list.

The fourth arm of the always-on-skills eval. Across roughly 214 replies in
four conditions, no reply in any arm ever used crucial, leverage, utilize,
numerous, or serves as. The banned-word section has never fired, and it is a
large fraction of the body. This arm asks whether it can go.

The question is not whether the literal bans fire, which is settled. The third
arm showed the rules are load-bearing on each other: the em-dash ban alone
causes the blind substitution that a second rule repairs. So a section whose
bans never fire could still shape the register. Only a run can tell.

Cuts `## Words to replace with plain ones` and nothing else. It deliberately
keeps `## A project's own nouns are not jargon`, whose real rule is to write
the actual symbol or flag name rather than a synonym. That is a specificity
rule, and specificity is the one semantic rule that measurably works, so
cutting it would confound this arm with the previous result.

Runs inside build-arm.sh's detached worktree, so the path is relative.
Fails loudly if the cut does not look like the section it is aimed at, because
an arm that removed the wrong text scores as a confident answer to a question
nobody asked.

The shipped body no longer has this section. This arm and the replication that
followed it are why it went. So the script now fails against HEAD by design,
and it needs a ref from before the cut:

    build-arm.sh pinnolist --ref f1015a92 --patch "python3 ...pin-nolist.py"
"""

import sys

PATH = "packages/agent/skills/builtin/house-style/SKILL.md"
START = "## Words to replace with plain ones"
END = "## A project's own nouns are not jargon"

# If the section is reworded, the anchors can still match while the slice holds
# something else. These must all appear inside the removed text.
SENTINELS = ["additionally, crucial, delve", "utilize or leverage",
             "serves as", "in order to"]
# These must all SURVIVE the cut, proving the slice did not run long.
KEEP = ["No em dashes.", "Say what a thing does, not how it feels.",
        "the codebase is the word list", "Straight quotes,"]

src = open(PATH).read()

if src.count(START) != 1 or src.count(END) != 1:
    sys.exit("pin-nolist: expected exactly 1 of each anchor, got %d and %d"
             % (src.count(START), src.count(END)))

i, j = src.index(START), src.index(END)
if not i < j:
    sys.exit("pin-nolist: the section anchors are out of order")

removed = src[i:j]
for s in SENTINELS:
    if s not in removed:
        sys.exit("pin-nolist: removed text is missing the sentinel %r, so the "
                 "cut is not the word-list section" % s)

out = src[:i] + src[j:]
for s in KEEP:
    if s not in out:
        sys.exit("pin-nolist: the cut also removed %r, which must survive" % s)

open(PATH, "w").write(out)
print("pin-nolist: cut %d bytes of word list, body %d -> %d"
      % (len(removed), len(src), len(out)))
