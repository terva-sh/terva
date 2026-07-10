package vote

import (
	"encoding/json"
	"testing"
)

func b(unit string, v Verdict) Ballot { return Ballot{Unit: unit, Verdict: v, Confidence: 0.8} }

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		in      string
		want    Verdict
		wantErr bool
	}{
		{"approve", Approve, false},
		{"APPROVE", Approve, false},
		{"Reject", Reject, false},
		{"abstain", Abstain, false},
		{"yes", "", true},
		{"", "", true},
		{"approve.", "", true},
	}
	for _, tc := range cases {
		got, err := ParseVerdict(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseVerdict(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseVerdict(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMajority(t *testing.T) {
	cases := []struct {
		name    string
		ballots []Ballot
		rule    Majority
		want    Decision
	}{
		{"unanimous approve", []Ballot{b("A", Approve), b("B", Approve), b("C", Approve)}, Majority{}, Approved},
		{"2-1 approve", []Ballot{b("A", Approve), b("B", Approve), b("C", Reject)}, Majority{}, Approved},
		{"2-1 reject", []Ballot{b("A", Reject), b("B", Reject), b("C", Approve)}, Majority{}, Rejected},
		{"1-1 tie with abstain", []Ballot{b("A", Approve), b("B", Reject), b("C", Abstain)}, Majority{}, Escalated},
		{"all abstain", []Ballot{b("A", Abstain), b("B", Abstain), b("C", Abstain)}, Majority{}, Escalated},
		{"empty panel", nil, Majority{}, Escalated},
		{"quorum met with one absent", []Ballot{b("A", Approve), b("B", Approve), AbsentBallot("C", "timed out")}, Majority{}, Approved},
		{"quorum failed, two absent", []Ballot{b("A", Approve), AbsentBallot("B", "timed out"), AbsentBallot("C", "errored")}, Majority{}, Escalated},
		{"zero quorum normalizes to 2", []Ballot{b("A", Approve)}, Majority{Quorum: 0}, Escalated},
		{"explicit quorum 1 allows lone voice", []Ballot{b("A", Approve)}, Majority{Quorum: 1}, Approved},
		{"deliberate abstain counts toward quorum", []Ballot{b("A", Approve), b("B", Abstain), AbsentBallot("C", "timed out")}, Majority{}, Approved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Decide(tc.ballots); got != tc.want {
				t.Errorf("Decide = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnanimity(t *testing.T) {
	cases := []struct {
		name    string
		ballots []Ballot
		want    Decision
	}{
		{"all approve passes", []Ballot{b("A", Approve), b("B", Approve), b("C", Approve)}, Approved},
		{"one reject rejects", []Ballot{b("A", Approve), b("B", Approve), b("C", Reject)}, Rejected},
		{"one abstain escalates", []Ballot{b("A", Approve), b("B", Approve), b("C", Abstain)}, Escalated},
		{"one absent escalates (fails closed)", []Ballot{b("A", Approve), b("B", Approve), AbsentBallot("C", "timed out")}, Escalated},
		{"reject wins over absence", []Ballot{b("A", Reject), b("B", Approve), AbsentBallot("C", "timed out")}, Rejected},
		{"empty panel escalates", nil, Escalated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Unanimity{}).Decide(tc.ballots); got != tc.want {
				t.Errorf("Decide = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVeto(t *testing.T) {
	cases := []struct {
		name    string
		ballots []Ballot
		rule    Veto
		want    Decision
	}{
		{
			"holder reject blocks an approving majority",
			[]Ballot{b("A", Approve), b("B", Approve), b("C", Reject)},
			Veto{Holder: "C"},
			Rejected,
		},
		{
			"holder approve falls through to base",
			[]Ballot{b("A", Approve), b("B", Reject), b("C", Approve)},
			Veto{Holder: "C"},
			Approved,
		},
		{
			"absent holder has not vetoed; base sees the panel",
			[]Ballot{b("A", Approve), b("B", Approve), AbsentBallot("C", "timed out")},
			Veto{Holder: "C"},
			Approved,
		},
		{
			"absent holder under a unanimity base still fails closed",
			[]Ballot{b("A", Approve), b("B", Approve), AbsentBallot("C", "timed out")},
			Veto{Holder: "C", Base: Unanimity{}},
			Escalated,
		},
		{
			"holder abstain is not a veto",
			[]Ballot{b("A", Approve), b("B", Approve), b("C", Abstain)},
			Veto{Holder: "C"},
			Approved,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Decide(tc.ballots); got != tc.want {
				t.Errorf("Decide = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTallyOutcome(t *testing.T) {
	t.Run("2-1 approve carries the dissent as minority", func(t *testing.T) {
		dissent := Ballot{Unit: "C", Verdict: Reject, Confidence: 0.9, Rationale: "the mirror does not flatter"}
		out := Tally([]Ballot{b("A", Approve), b("B", Approve), dissent}, Majority{})
		if out.Decision != Approved {
			t.Fatalf("Decision = %q, want approved", out.Decision)
		}
		if len(out.Minority) != 1 || out.Minority[0].Unit != "C" {
			t.Fatalf("Minority = %+v, want the C dissent", out.Minority)
		}
		if out.Minority[0].Rationale != dissent.Rationale {
			t.Errorf("minority rationale not preserved")
		}
		if out.Degraded {
			t.Errorf("Degraded = true for a whole panel")
		}
		if out.Rule != "majority" {
			t.Errorf("Rule = %q, want majority", out.Rule)
		}
	})

	t.Run("veto puts the outvoted majority in the minority report", func(t *testing.T) {
		out := Tally([]Ballot{b("A", Approve), b("B", Approve), b("C", Reject)}, Veto{Holder: "C"})
		if out.Decision != Rejected {
			t.Fatalf("Decision = %q, want rejected", out.Decision)
		}
		if len(out.Minority) != 2 {
			t.Fatalf("Minority = %+v, want the two outvoted approvals", out.Minority)
		}
	})

	t.Run("absence degrades and is tallied separately", func(t *testing.T) {
		out := Tally([]Ballot{b("A", Approve), b("B", Approve), AbsentBallot("C", "timed out")}, Majority{})
		if !out.Degraded {
			t.Errorf("Degraded = false with an absent unit")
		}
		if out.Tally.Absent != 1 || out.Tally.Abstain != 0 {
			t.Errorf("Tally = %+v, want absent counted apart from abstain", out.Tally)
		}
		if len(out.Minority) != 0 {
			t.Errorf("Minority = %+v, absent abstention is not dissent", out.Minority)
		}
	})

	t.Run("escalated outcome carries no minority", func(t *testing.T) {
		out := Tally([]Ballot{b("A", Approve), b("B", Reject), b("C", Abstain)}, Majority{})
		if out.Decision != Escalated {
			t.Fatalf("Decision = %q, want escalated", out.Decision)
		}
		if len(out.Minority) != 0 {
			t.Errorf("Minority = %+v, want empty for escalated", out.Minority)
		}
	})

	t.Run("abstention never appears in the minority", func(t *testing.T) {
		out := Tally([]Ballot{b("A", Approve), b("B", Approve), b("C", Abstain)}, Majority{})
		if out.Decision != Approved || len(out.Minority) != 0 {
			t.Errorf("out = %+v, want approved with empty minority", out)
		}
	})
}

func TestOutcomeJSONRoundTrip(t *testing.T) {
	out := Tally([]Ballot{
		{Unit: "YATA-1", Verdict: Approve, Confidence: 0.7, Rationale: "evidence holds"},
		{Unit: "KUSANAGI-2", Verdict: Reject, Confidence: 0.9, Rationale: "unbounded risk"},
		AbsentBallot("MAGATAMA-3", "timed out after 120s"),
	}, Majority{})

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Outcome
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Decision != out.Decision || back.Rule != out.Rule || back.Tally != out.Tally || back.Degraded != out.Degraded {
		t.Errorf("round trip mismatch: got %+v, want %+v", back, out)
	}
	if len(back.Minority) != len(out.Minority) {
		t.Errorf("minority round trip mismatch: got %d, want %d", len(back.Minority), len(out.Minority))
	}
}
