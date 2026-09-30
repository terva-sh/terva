package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/look"
	"terva.sh/terva/packages/agent/talkoot"
)

// A talkoot has a mark of its own (TKT-01M3NSM9R): one team body in the
// roster's colour, with eyes that show the team's combined state.
// docs/proposals/talkoot-members.md, "The talkoot's own mark", is the design.

// teamState is the team state the run's last flush found.
func (r *talkootRun) teamState() string {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	return r.team
}

// announceTalkoots tells every client on #workspace that the talkoot list
// changed, so a list that shows each team's state and colour reads it again.
func (w *Workspace) announceTalkoots() {
	w.BroadcastAll(ctrlproto.TalkootsChangedEvent())
}

// talkootColor sets the team colour as a person, or with an empty color
// returns it to the default. It edits only the colour, so an edit made
// meanwhile to another field survives. It refuses a change nobody would see:
// the colour the roster holds, or a colour the team already shows, in any hex
// case. A reset of an own colour that equals the default still goes through,
// because it removes the key.
func (w *Workspace) talkootColor(ctx context.Context, id, by, color string) (talkootView, error) {
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return talkootView{}, fmt.Errorf("talkoot: %q must name a person in 1 to 64 letters, digits, and . _ @ -", by)
	}
	if color != "" && !look.ValidColor(color) {
		return talkootView{}, fmt.Errorf("talkoot: color %q is not a #RRGGBB value", color)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkootView{}, err
	}
	run.update.Lock()
	defer run.update.Unlock()
	cur := run.roster.Load().Color
	if cur == color || (color != "" && strings.EqualFold(look.TeamColor(id, color), look.TeamColor(id, cur))) {
		return talkootView{}, fmt.Errorf("talkoot: the team colour is already %s", look.TeamColor(id, cur))
	}
	text, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil {
		return talkootView{}, fmt.Errorf("talkoot: read the roster: %w", err)
	}
	next, err := talkoot.SetColor(text, color)
	if err != nil {
		return talkootView{}, err
	}
	nr, err := w.parseRoster(id, next)
	if err != nil {
		return talkootView{}, err
	}
	return w.applyRosterLocked(ctx, run, by, next, nr, rosterSource{})
}
