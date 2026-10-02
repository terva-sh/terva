package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/talkoot"
)

// talkootBudgetWaiver changes only the team's waiver through the same
// serialized, recorded roster update as a colour change. Member proposals
// cannot call this path or edit team fields.
func (w *Workspace) talkootBudgetWaiver(ctx context.Context, id, by string, waived bool) (talkootView, error) {
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return talkootView{}, fmt.Errorf("talkoot: %q must name a person in 1 to 64 letters, digits, and . _ @ -", by)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkootView{}, err
	}
	run.update.Lock()
	defer run.update.Unlock()
	if run.roster.Load().TeamBudgetWaived == waived {
		return talkootView{}, fmt.Errorf("talkoot: the team budget waiver is already %t", waived)
	}
	text, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil {
		return talkootView{}, fmt.Errorf("talkoot: read the roster: %w", err)
	}
	next, err := talkoot.SetTeamBudgetWaived(text, waived)
	if err != nil {
		return talkootView{}, err
	}
	nr, err := w.parseRoster(id, next)
	if err != nil {
		return talkootView{}, err
	}
	return w.applyRosterLocked(ctx, run, by, next, nr, rosterSource{})
}
