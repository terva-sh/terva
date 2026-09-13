package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// A reader who cannot tell a recovery from a void will count laundered runs as
// recoveries, which is exactly how one 46-dispatch floor was read back as three
// short runs. These assertions are about the sentence a person actually sees,
// so they check the words rather than a field.
func TestCliffEventTextSeparatesTheTwoEndings(t *testing.T) {
	const dispatches, reread = 18, 699_965

	cases := []struct {
		name    string
		cliff   core.CacheCliff
		want    []string
		wantNot []string
	}{
		{
			name:    "an open run says so and claims no ending",
			cliff:   core.CacheCliff{Dispatches: 3, RereadTokens: 61_929, Ongoing: true},
			want:    []string{"opened", "3 dispatches"},
			wantNot: []string{"closed", "recovered", "voided"},
		},
		{
			name:    "a recovery is the run genuinely over",
			cliff:   core.CacheCliff{Dispatches: dispatches, RereadTokens: reread, End: core.CliffEndRecovered},
			want:    []string{"recovered", "18 dispatches"},
			wantNot: []string{"voided", "or more"},
		},
		{
			name:  "a void names the rebuild and refuses to report a total",
			cliff: core.CacheCliff{Dispatches: dispatches, RereadTokens: reread, End: core.CliffEndVoided},
			// "or more" is the load-bearing part: the run did not end, so its
			// length is a floor and must not read as a completed run.
			want:    []string{"voided", "or more", "rebuilt the prefix"},
			wantNot: []string{"recovered"},
		},
		{
			name: "a row written before the endings existed claims neither",
			// End is the zero value, which is what every close row on disk
			// before this change carries.
			cliff:   core.CacheCliff{Dispatches: dispatches, RereadTokens: reread},
			want:    []string{"closed", "not recorded"},
			wantNot: []string{"recovered", "voided"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cliffEventText(tc.cliff)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("rendered line is missing %q:\n  %s", w, got)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("rendered line must not contain %q:\n  %s", w, got)
				}
			}
		})
	}
}

// The whole point is that the two endings cannot be confused, so assert it
// directly rather than trusting the per-case words above to stay in sync.
func TestCliffEventTextVoidAndRecoveryNeverRenderAlike(t *testing.T) {
	same := core.CacheCliff{Dispatches: 40, RereadTokens: 2_233_844}

	voided := same
	voided.End = core.CliffEndVoided
	recovered := same
	recovered.End = core.CliffEndRecovered
	legacy := same

	v, r, l := cliffEventText(voided), cliffEventText(recovered), cliffEventText(legacy)
	if v == r {
		t.Fatalf("a void and a recovery render identically:\n  %s", v)
	}
	if v == l || r == l {
		t.Errorf("a pre-change row must not render as either named ending:\n  legacy=%s\n  void=%s\n  recovered=%s", l, v, r)
	}
}
