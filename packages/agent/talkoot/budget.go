package talkoot

import (
	"bytes"
	"errors"
	"reflect"

	"gopkg.in/yaml.v3"
)

// SetTeamBudgetWaived changes only the waiver, preserving the configured cap,
// member limits, comments, and charter. Only a person's roster update uses it.
func SetTeamBudgetWaived(text []byte, waived bool) ([]byte, error) {
	before, err := Parse(text, FileName)
	if err != nil {
		return nil, err
	}
	front, body, ok := splitFrontmatter(text)
	if !ok {
		return nil, errors.New("talkoot: the roster has no YAML frontmatter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("talkoot: the roster frontmatter is not a mapping")
	}
	if waived {
		if err := setField(doc.Content[0], "team_budget_waived", true); err != nil {
			return nil, err
		}
	} else {
		dropField(doc.Content[0], "team_budget_waived")
	}
	out, after, err := encodeRoster(&doc, body)
	if err != nil {
		return nil, err
	}
	// 🚨 A merge key (<<: {team_budget_waived: true}) still waives once the
	// literal key is gone. An explicit key overrides a merged one, so the
	// restore writes false rather than leave the cap waived.
	if !waived && after.TeamBudgetWaived {
		if err := setField(doc.Content[0], "team_budget_waived", false); err != nil {
			return nil, err
		}
		if out, after, err = encodeRoster(&doc, body); err != nil {
			return nil, err
		}
	}
	before.TeamBudgetWaived = waived
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("talkoot: the roster text did not take the team budget waiver as set")
	}
	return out, nil
}

// encodeRoster writes doc back as frontmatter over body and parses the result.
func encodeRoster(doc *yaml.Node, body []byte) ([]byte, Roster, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, Roster{}, err
	}
	if err := enc.Close(); err != nil {
		return nil, Roster{}, err
	}
	out := append([]byte("---\n"), buf.Bytes()...)
	out = append(out, "---\n"...)
	out = append(out, body...)
	r, err := Parse(out, FileName)
	return out, r, err
}

// EnforceTeamBudget rechecks the cap when a person restores it. Existing
// spend counts at once, rather than allowing another turn above the cap.
func (rt *Router) EnforceTeamBudget() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.roll(rt.now())
	for _, trip := range rt.capTrips(Member{}, turnHold{}) {
		if err := rt.room.Append(Line{Type: LineGuard, At: rt.now(), Guard: trip.guard,
			Action: ActionPaused, Member: trip.member, Reason: trip.reason}); err != nil {
			return err
		}
	}
	return nil
}
