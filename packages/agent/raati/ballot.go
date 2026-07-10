package raati

import (
	"encoding/json"
	"errors"
	"strings"

	"terva.sh/terva/packages/vote"
)

// wireBallot is the JSON object a unit's charter tells it to emit
// inside the fenced ```ballot block. Inquiries are the optional
// between-round questions of the inquiry protocol; the field is
// prompt-carried (charters unchanged) and simply ignored by older
// prompts.
type wireBallot struct {
	Verdict    string   `json:"verdict"`
	Confidence float64  `json:"confidence"`
	Rationale  string   `json:"rationale"`
	Inquiries  []string `json:"inquiries,omitempty"`
}

// Inquiry caps: a unit may pose at most two questions per round, each
// bounded — the clerk answers a docket, not an essay.
const (
	maxInquiriesPerUnit = 2
	maxInquiryLen       = 300
)

// parseBallot extracts a unit's ballot (and any posed inquiries) from
// its reply text. The charter contract is a fenced code block tagged
// `ballot` holding one JSON object; the LAST such block wins, so
// deliberation prose that quotes an earlier ballot can't shadow the
// current one. As a fallback (small local models flub fencing
// constantly, and the toy binding is the point of level 0) it accepts
// a trailing bare JSON object that carries a verdict. Anything else is
// an error — the caller records the unit as absent, because an
// unparseable vote must never be silently counted as an abstention.
func parseBallot(unit, text string) (vote.Ballot, []string, error) {
	raw, ok := lastBallotFence(text)
	if !ok {
		raw, ok = trailingJSONObject(text)
	}
	if !ok {
		return vote.Ballot{}, nil, errors.New("no ballot block in the reply")
	}
	var wb wireBallot
	if err := json.Unmarshal([]byte(raw), &wb); err != nil {
		return vote.Ballot{}, nil, errors.New("ballot block is not valid JSON")
	}
	verdict, err := vote.ParseVerdict(wb.Verdict)
	if err != nil {
		return vote.Ballot{}, nil, err
	}
	var inqs []string
	for _, q := range wb.Inquiries {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if len(q) > maxInquiryLen {
			q = q[:maxInquiryLen]
		}
		inqs = append(inqs, q)
		if len(inqs) == maxInquiriesPerUnit {
			break
		}
	}
	return vote.Ballot{
		Unit:       unit,
		Verdict:    verdict,
		Confidence: clamp01(wb.Confidence),
		Rationale:  strings.TrimSpace(wb.Rationale),
	}, inqs, nil
}

// lastBallotFence returns the contents of the last ```ballot fenced
// block in text.
func lastBallotFence(text string) (string, bool) {
	const open = "```ballot"
	start := strings.LastIndex(text, open)
	if start < 0 {
		return "", false
	}
	body := text[start+len(open):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	end := strings.Index(body, "```")
	if end < 0 {
		// An unterminated fence at the end of the reply still counts:
		// the block was opened for us, the model just forgot to close it.
		end = len(body)
	}
	return strings.TrimSpace(body[:end]), true
}

// trailingJSONObject returns the last brace-balanced JSON object in
// text, provided it mentions a verdict — the lenient path for models
// that emit the object without the fence.
func trailingJSONObject(text string) (string, bool) {
	end := strings.LastIndexByte(text, '}')
	if end < 0 {
		return "", false
	}
	depth := 0
	for i := end; i >= 0; i-- {
		switch text[i] {
		case '}':
			depth++
		case '{':
			depth--
			if depth == 0 {
				obj := text[i : end+1]
				if strings.Contains(obj, `"verdict"`) {
					return obj, true
				}
				return "", false
			}
		}
	}
	return "", false
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}
