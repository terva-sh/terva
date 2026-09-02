"""Build the MINIMAL arm: pin the em-dash rule alone, not the whole body.

The third arm of the always-on-skills eval. Arm B pins all 5665 bytes of
house-style. This arm pins the same em-dash rule verbatim and nothing else, so
the only variable between B and C is the absence of the other rules, not a
reworded version of the one rule under test.

The frontmatter stays byte-identical, so B and C vacate the same manifest row
and their skill manifests match. Then a B-versus-C difference can only come
from the pinned body.

Runs inside build-arm.sh's detached worktree, so the path is relative.
Fails loudly if it matched nothing, because an arm identical to its control
scores as a confident "no change".
"""

import sys

PATH = "packages/agent/skills/builtin/house-style/SKILL.md"

RULE = """- **No em dashes.** Separate thoughts with a period or a comma. End the
  sentence, or join it with a comma. Parentheses and en dashes are the same
  move in a different hat, so they do not substitute."""

src = open(PATH).read()

if src.count(RULE) != 1:
    sys.exit("pin-minimal: expected exactly 1 copy of the em-dash rule, got %d. "
             "The body changed, so this arm would no longer isolate it."
             % src.count(RULE))

parts = src.split("---\n")
if len(parts) < 3 or parts[0] != "":
    sys.exit("pin-minimal: could not read the frontmatter block")
frontmatter = "---\n" + parts[1] + "---\n"

open(PATH, "w").write(frontmatter + "\n# House style\n\n" + RULE + "\n")
print("pin-minimal: body cut from %d to %d bytes" % (len(src), len(frontmatter) + len(RULE) + 20))
