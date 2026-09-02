"""Build the TERSE arm: the word list cut down to its one working sentence.

The fifth arm. The fourth arm found that removing `## Words to replace with
plain ones` makes replies 14 percent longer, while its enumerated bans never
fire and it does none of the sentence-construction work. That reads as a
concision instruction wearing a vocabulary list.

This arm keeps "Prefer the short word." verbatim and drops the rest of the
section. Verbatim matters for the same reason it did in pin-minimal.py: a
reworded instruction would make the wording a second variable, and this arm
exists to isolate the enumeration.

The prediction is pre-registered and has two named outcomes. Reply length is
the endpoint, because it is the only effect the fourth arm found.

    lands near 114 words -> behaves like the full body, the enumeration is
                            dead weight and about 133 tokens can go
    lands near 130 words -> behaves like the no-list arm, the enumeration was
                            carrying the effect after all

Runs inside build-arm.sh's detached worktree, so the path is relative. Fails
loudly if the cut does not look like the section it is aimed at.

The shipped body no longer has this section. This arm helped establish that it
could go, and it went. So the script now fails against HEAD by design, and it
needs a ref from before the cut:

    build-arm.sh pinterse --ref f1015a92 --patch "python3 ...pin-terse.py"
"""

import sys

PATH = "packages/agent/skills/builtin/house-style/SKILL.md"
START = "## Words to replace with plain ones"
END = "## A project's own nouns are not jargon"
KEPT = "Prefer the short word."

SENTINELS = ["additionally, crucial, delve", "utilize or leverage", "serves as"]
KEEP = ["No em dashes.", "Say what a thing does, not how it feels.",
        "the codebase is the word list"]

src = open(PATH).read()

if src.count(START) != 1 or src.count(END) != 1:
    sys.exit("pin-terse: expected exactly 1 of each anchor, got %d and %d"
             % (src.count(START), src.count(END)))

i, j = src.index(START), src.index(END)
if not i < j:
    sys.exit("pin-terse: the section anchors are out of order")

removed = src[i:j]
for s in SENTINELS:
    if s not in removed:
        sys.exit("pin-terse: removed text is missing the sentinel %r, so the "
                 "cut is not the word-list section" % s)
if KEPT not in removed:
    sys.exit("pin-terse: %r is not in the section, so it cannot be kept "
             "verbatim" % KEPT)

out = src[:i] + START + "\n\n" + KEPT + "\n\n" + src[j:]
for s in KEEP:
    if s not in out:
        sys.exit("pin-terse: the cut also removed %r, which must survive" % s)

open(PATH, "w").write(out)
print("pin-terse: word list %d -> %d bytes, body %d -> %d"
      % (len(removed), len(START) + len(KEPT) + 4, len(src), len(out)))
