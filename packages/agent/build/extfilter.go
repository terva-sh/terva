package build

import (
	"context"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/core"
)

// ExtensionFilters returns the options that let extMgr's extensions stop a
// turn, and refuse or rewrite a user or an assistant message, for an agent
// built by Resolved.NewAgent. Pass them to NewAgent with the gate that
// BuildToolGate built over the same extMgr. A nil manager gives no options,
// so a host with no extensions passes the call through unchanged.
//
// 🔑 The filters are a component of the agent, connected at construction.
// They used to be three closures that each host copied onto the agent's
// fields after it was built, and three hosts carried the same copy.
// TestEveryExtensionGateCarriesItsFilters checks that each host building a
// gate over its manager also passes these.
func ExtensionFilters(ctx context.Context, extMgr *extensions.Manager) []core.Option {
	if extMgr == nil {
		return nil
	}
	return []core.Option{core.WithComponent(extensionFilter{ctx: ctx, m: extMgr})}
}

// extensionFilter asks the extensions before each step and each message.
type extensionFilter struct {
	ctx context.Context
	m   *extensions.Manager
}

var (
	_ core.TurnFilter    = extensionFilter{}
	_ core.MessageFilter = extensionFilter{}
)

// BeforeTurn implements core.TurnFilter.
func (f extensionFilter) BeforeTurn(step int) (bool, string) {
	res := f.m.InterceptTurnStart(f.ctx, step)
	return !res.Block, res.Reason
}

// BeforeAssistantMessage implements core.MessageFilter.
func (f extensionFilter) BeforeAssistantMessage(text string) (bool, string, string) {
	res := f.m.InterceptAssistantMessage(f.ctx, text)
	if res.Block {
		return false, res.Reason, ""
	}
	return true, "", res.ReplaceText
}

// BeforeUserMessage implements core.MessageFilter.
func (f extensionFilter) BeforeUserMessage(text string) (bool, string, string) {
	res := f.m.InterceptUserMessage(f.ctx, text)
	if res.Block {
		return false, res.Reason, ""
	}
	return true, "", res.ReplaceText
}
