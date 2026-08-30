package build

import (
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// HiddenModelKeys returns the "provider/id" keys of every active model the
// user's visibility rules hide.
//
// The pickers want a SET of hidden models, but the config stores ordered
// patterns — the two are not the same thing, and only the catalogue can bridge
// them: "openrouter/*" names no model until you know what OpenRouter serves.
// Expanding here keeps the rule engine in one place and hands each front end
// the flat membership it actually renders, exactly like the favourites list it
// sits beside.
//
// A config that fails to load yields no rules, which hides nothing. The failure
// mode of an unreadable config must be a cluttered picker, never a picker
// silently missing the model somebody needs.
func HiddenModelKeys() []string {
	cfg, _ := config.LoadConfig()
	vis := config.NewModelVisibility(cfg.HiddenModels)
	if vis.Empty() {
		return nil
	}
	var out []string
	for _, m := range provider.Active() {
		if vis.Hidden(m.Provider, m.ID) {
			out = append(out, config.ModelKey(m.Provider, m.ID))
		}
	}
	return out
}
