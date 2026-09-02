"""Build the OFF arm: make terva pin no skill body by default.

Pairs with the always-on-skills eval. The ON arm is plain HEAD, so this is the
only difference between the two binaries: `DefaultAlwaysOn` goes empty, the
house-style body leaves the prompt prefix, and its description returns to the
skill manifest.

Runs inside build-arm.sh's detached worktree, so the path is relative.
Fails loudly if it matched nothing, because an arm identical to its control
scores as a confident "no change".
"""

import sys

PATH = "packages/agent/skills/skills.go"
OLD = 'var DefaultAlwaysOn = []string{"house-style"}'
NEW = "var DefaultAlwaysOn = []string{}"

src = open(PATH).read()
if src.count(OLD) != 1:
    sys.exit("pin-off: expected exactly 1 occurrence of the default, got %d" % src.count(OLD))
open(PATH, "w").write(src.replace(OLD, NEW))
print("pin-off: DefaultAlwaysOn emptied")
