package raati

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/vote"
)

// round1Prompt is the blind-round task a unit is spawned with. The
// blind framing is load-bearing: without it, anchoring and sycophancy
// collapse the prior diversity the panel exists to create — the
// charter's ballot contract carries the format, this prompt carries
// the isolation.
func round1Prompt(u Unit, panel int, class Class, question, evidence string, inquire bool) string {
	var sb strings.Builder
	sb.WriteString(i18n.P("raati.round1.header",
		"A raati — a deliberation panel — has been convened, and you sit on it as %s, one of %d panelists. This round is blind: the others are deliberating the same question in parallel, you cannot see them, and they cannot see you. Deliberate strictly through your charter's lens, then end your reply with your ballot exactly as your charter specifies — a fenced code block tagged `ballot`.",
		u.Name, panel))
	sb.WriteString("\n\n")
	sb.WriteString(i18n.P("raati.round1.class", "Decision class: %s.", string(class)))
	sb.WriteString("\n\n")
	sb.WriteString(i18n.P("raati.round1.question", "The question before the panel:"))
	sb.WriteString("\n")
	sb.WriteString(question)
	sb.WriteString("\n\n")
	if strings.TrimSpace(evidence) != "" {
		sb.WriteString(i18n.P("raati.round1.evidence", "Evidence submitted to the panel:"))
		sb.WriteString("\n")
		sb.WriteString(evidence)
	} else {
		sb.WriteString(i18n.P("raati.round1.no_evidence", "No evidence was submitted; judge from the question and your own reasoning, and weigh what is unknowable accordingly."))
	}
	if inquire {
		sb.WriteString("\n\n")
		sb.WriteString(inquirySolicitation())
	}
	return sb.String()
}

// inquirySolicitation is the prompt-carried contract for the inquiry
// protocol: the optional `inquiries` field rides inside the ballot
// JSON, so charters stay unchanged and non-inquiring deliberations
// never mention it.
func inquirySolicitation() string {
	return i18n.P("raati.inquire",
		"If specific missing information would change your ballot, you may add an `inquiries` array (up to two short questions) inside your ballot JSON. The clerk answers between rounds, strictly from the record; a question the record cannot answer is recorded as open. Ask only what is decision-relevant — vote on what you have either way.")
}

// inquiryDigest renders one round's resolved docket as the pooled Q&A
// block every seat receives in the next round — symmetric information,
// unanswered questions included (an evidence gap the whole panel
// should weigh, not a private disappointment).
func inquiryDigest(docket []Inquiry) string {
	var sb strings.Builder
	sb.WriteString(i18n.P("raati.digest.header", "The clerk's answers to the panel's questions:"))
	sb.WriteString("\n")
	for _, q := range docket {
		fmt.Fprintf(&sb, "- %s: %s\n", q.Unit, q.Question)
		if q.Source == SourceUnanswered || strings.TrimSpace(q.Answer) == "" {
			sb.WriteString("  → ")
			sb.WriteString(i18n.P("raati.digest.unanswered", "the record does not answer this — weigh the gap accordingly"))
		} else {
			sb.WriteString("  → ")
			sb.WriteString(q.Answer)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// round2ColdPrompt is the cross-examination turn for a RESEATED unit
// (the per-turn seat order): a fresh child with no blind-round history
// takes over the seat, so it receives the full question + evidence
// again, the seat's own provisional ballot to re-examine, and the
// reveal. Honest framing — the model is told it now holds the seat,
// not that it cast the earlier ballot.
func round2ColdPrompt(u Unit, panel int, class Class, question, evidence string, own vote.Ballot, revealed []vote.Ballot, digest string, solicit bool) string {
	var sb strings.Builder
	sb.WriteString(i18n.P("raati.round2cold.header",
		"A raati — a deliberation panel — is in session, and you now hold the seat of %s, one of %d panelists, for the FINAL round. The blind round is complete. Re-examine this seat's provisional position through your charter's lens against the other panelists' arguments below — rebut the strongest one, or be genuinely persuaded by it. Then end your reply with your FINAL ballot as a fenced code block tagged `ballot`. Revise the verdict only if an argument changes your assessment; never to close the gap with the panel. Your confidence is your own calibration — the panel's certainty is not evidence.",
		u.Name, panel))
	sb.WriteString("\n\n")
	sb.WriteString(i18n.P("raati.round1.class", "Decision class: %s.", string(class)))
	sb.WriteString("\n\n")
	sb.WriteString(i18n.P("raati.round1.question", "The question before the panel:"))
	sb.WriteString("\n")
	sb.WriteString(question)
	sb.WriteString("\n\n")
	if strings.TrimSpace(evidence) != "" {
		sb.WriteString(i18n.P("raati.round1.evidence", "Evidence submitted to the panel:"))
		sb.WriteString("\n")
		sb.WriteString(evidence)
		sb.WriteString("\n\n")
	}
	sb.WriteString(i18n.P("raati.round2cold.own", "This seat's provisional ballot from the blind round: %s — %s", string(own.Verdict), own.Rationale))
	sb.WriteString("\n\n")
	for _, b := range revealed {
		if b.Absent {
			sb.WriteString(i18n.P("raati.round2.absent", "- %s: absent (%s)", b.Unit, b.Rationale))
		} else {
			sb.WriteString(fmt.Sprintf("- %s: %s — %s", b.Unit, string(b.Verdict), b.Rationale))
		}
		sb.WriteString("\n")
	}
	if digest != "" {
		sb.WriteString("\n")
		sb.WriteString(digest)
	}
	if solicit {
		sb.WriteString("\n")
		sb.WriteString(inquirySolicitation())
	}
	return sb.String()
}

// round2Prompt is the cross-examination turn: the reveal of the other
// seats' provisional ballots, and the request for a final ballot.
//
// The reveal deliberately carries verdict + rationale ONLY — never the
// confidence numbers. Numeric confidences in the reveal act as an
// anchor and pull the panel's certainty toward its mean (observed in
// the first live deliberations); arguments should be cross-examined,
// not calibration. The numbers stay on the record and the board.
func round2Prompt(u Unit, revealed []vote.Ballot, digest string, solicit bool) string {
	var sb strings.Builder
	sb.WriteString(i18n.P("raati.round2.header",
		"The blind round is complete. The other panelists' provisional ballots follow. Weigh the strongest argument against your position — rebut it, or be genuinely persuaded by it, through your own charter's lens. Then end your reply with your FINAL ballot as a fenced code block tagged `ballot`. Revise your verdict only if an argument changes your assessment; never to close the gap with the panel. Your confidence is your own calibration — the panel's certainty is not evidence."))
	sb.WriteString("\n\n")
	for _, b := range revealed {
		if b.Absent {
			sb.WriteString(i18n.P("raati.round2.absent", "- %s: absent (%s)", b.Unit, b.Rationale))
		} else {
			sb.WriteString(fmt.Sprintf("- %s: %s — %s", b.Unit, string(b.Verdict), b.Rationale))
		}
		sb.WriteString("\n")
	}
	if digest != "" {
		sb.WriteString("\n")
		sb.WriteString(digest)
	}
	if solicit {
		sb.WriteString("\n")
		sb.WriteString(inquirySolicitation())
	}
	return sb.String()
}

// round3Prompt is the convergence turn: cross-examination flipped at
// least one verdict, so the panel responds once to the configuration
// that actually exists. Deliberately framed as stabilization — hold
// unless genuinely moved — because a third exposure round otherwise
// buys conformity, not accuracy.
func round3Prompt(revealed []vote.Ballot, digest string) string {
	var sb strings.Builder
	sb.WriteString(i18n.P("raati.round3.header",
		"Cross-examination changed positions on the panel; this one CONVERGENCE round lets you respond to the final configuration below — it is the last round either way. Hold your position unless an argument genuinely changes your assessment; do not move to close the gap with the panel. End your reply with your FINAL ballot as a fenced code block tagged `ballot`."))
	sb.WriteString("\n\n")
	for _, b := range revealed {
		if b.Absent {
			sb.WriteString(i18n.P("raati.round2.absent", "- %s: absent (%s)", b.Unit, b.Rationale))
		} else {
			sb.WriteString(fmt.Sprintf("- %s: %s — %s", b.Unit, string(b.Verdict), b.Rationale))
		}
		sb.WriteString("\n")
	}
	if digest != "" {
		sb.WriteString("\n")
		sb.WriteString(digest)
	}
	return sb.String()
}
