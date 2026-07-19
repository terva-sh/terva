package workspace

import (
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// Wire-to-core session mapping. It reads ctrlproto.SessionInfo, which this
// package owns, so it belongs here rather than in the composition root that
// happened to need it first.

// SessionSummariesFromInfos maps the session group's wire view back onto the
// picker's native row type. FirstUserText stays empty deliberately: the
// service already folds it into Title (titleFromFirstText), which is the only
// thing the picker uses it for.
func SessionSummariesFromInfos(infos []ctrlproto.SessionInfo) []core.SessionSummary {
	out := make([]core.SessionSummary, 0, len(infos))
	for _, in := range infos {
		started, _ := time.Parse(time.RFC3339, in.Created)
		out = append(out, core.SessionSummary{
			Path:         in.Path,
			Started:      started,
			Model:        in.Model,
			Provider:     in.Provider,
			MessageCount: in.Messages,
			TotalCost:    in.Usage.CostUSD,
			Title:        in.Title,
			Live:         in.Live,
			Busy:         in.Busy,
		})
	}
	return out
}
