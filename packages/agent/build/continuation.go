package build

import (
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// OpenWorkGate builds the at-close open-work continuation gate, shared by
// every host (interactive, rpc, acp, workspace): when a turn ends naturally
// but an extension flags a blocking context card or a tracked task is still
// open, re-prompt once per Prompt with OpenWorkGateMessage. Either source may
// be nil.
func OpenWorkGate(extMgr *extensions.Manager, tasks *tasktool.Controller) core.ContinuationGate {
	return core.ContinuationGate{
		Cause: "open-work",
		Fire: func(provider.StopReason) (string, bool) {
			if (extMgr != nil && extMgr.HasBlockingContext()) || (tasks != nil && tasks.HasBlocking()) {
				return OpenWorkGateMessage(), true
			}
			return "", false
		},
	}
}
