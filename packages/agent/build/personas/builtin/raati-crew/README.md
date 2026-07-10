# Raati crew

The three deliberation panelists convened by RAATI, terva's consensus
primitive (docs/proposals/raati-deliberation.md). A raati puts one question to
all three; each judges it through a deliberately different prior, deliberates
blind, cross-examines, and casts a structured ballot. The panel's value is the
diversity of the priors — and the recorded dissent when they disagree.

The cast is the Imperial Regalia of Japan: three treasures that must be
present together to legitimate a succession — which is what a quorum is.

| unit | | treasure | prior | judges by… |
|---|---|---|---|---|
| YATA-1 | 🪞 | the mirror | truth | evidence, verifiability, internal consistency |
| KUSANAGI-2 | 🗡️ | the sword | decisiveness | risk of acting vs. risk of standing still, failure modes |
| MAGATAMA-3 | 🧿 | the jewel | benevolence | human impact, the unrepresented, second-order harm |

Unlike the review crew, these are **not** dispatchable one at a time by a
coordinator (`good_for` is empty by design): a lone panelist is an opinion,
not a tally. They are convened as a whole panel by the raati coordinator, and
remain usable directly via `--persona raati-crew:<stem>` for flavor.

Every charter ends with the same ballot contract: each deliberation reply must
close with a fenced ` ```ballot ` block holding one JSON object
(`{"verdict", "confidence", "rationale"}`) — the coordinator reads only that
block as the vote — and verdicts may move between rounds only on genuine
persuasion, never to close the gap with the panel. Keep the paragraph when
editing a charter; `TestRaatiCrewChartersCarryBallotContract` pins it.
