package build

// The auto-seated level-2 panel: what raati.level2 would have said if the
// operator had written it out, derived from the providers they are actually
// logged into and each one's strong tier rung.
//
// It exists because the alternative was a wall. Level 2 is the only rigor
// level with real error decorrelation, so it is the only one a gate-class
// profile can honestly hold — and it was reachable exclusively by hand-writing
// three provider+model pairs into config.json. A model that reached for the
// shipped `code-review` profile got a refusal that read like a permissions
// denial, and the fix was a config edit it could not make and had not been
// told to ask for.
//
// Two rules do the work, and both are about not lying:
//
//   - A seat needs a CREDENTIAL. Seating a provider terva cannot authenticate
//     to turns a convening into three failed spawns.
//   - A seat needs a LINEAGE nobody else has. Anthropic and GitHub Copilot
//     both serve Claude; Bedrock serves it a third time. A panel of three
//     Claudes is level 0 wearing level 2's label, which is worse than no
//     panel at all, because the label is what a gate is trusted on.
//
// Under-filling therefore refuses rather than substituting. Two providers is
// not a three-seat panel, and padding the third seat from a provider already
// present would produce exactly the correlated panel the level exists to rule
// out.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider/auth"
)

// AutoPanelCandidate is one provider the derivation considered, and what
// became of it. The report is assembled from these rather than logged as it
// goes, because the question an operator actually asks is "why is my panel
// not seated", and that is answered by the providers that were REJECTED.
type AutoPanelCandidate struct {
	Provider string
	Model    string
	// Reasoning is the effort the seat would run at ("" = the model's own).
	Reasoning string
	Lineage   string
	// Seated is true when this candidate took a seat. Otherwise Why says
	// what stopped it, already translated.
	Seated bool
	Why    string
}

// AutoRaatiPanel derives a level-2 panel from the operator's logged-in
// providers, or reports why it could not.
//
// Returns nil unless it can fill EVERY seat — a partial panel is not a
// weaker panel, it is a different rigor level with the wrong name on it.
// The candidate list is always populated, including when the panel is nil
// and when the feature is off, so a surface can explain the outcome instead
// of showing an empty pane.
func AutoRaatiPanel(uc config.Config, seats int) ([]raati.Binding, []AutoPanelCandidate) {
	if !uc.Raati.AutoPanel || seats <= 0 {
		return nil, nil
	}
	overrides := SwarmTierMap(uc.SwarmTiers)
	// A failed load is "nobody is logged in", which is the correct panel
	// (none) rather than an error to propagate — same reading auth.Describe's
	// callers already take.
	creds, _ := config.AuthStoreFor().Load()
	states := auth.Describe(creds)

	var (
		cands   []AutoPanelCandidate
		panel   []raati.Binding
		lineage = map[string]string{} // lineage -> the provider holding it
	)
	for _, p := range autoPanelOrder(uc) {
		c := AutoPanelCandidate{Provider: p}
		if !hasCredential(p, creds, states) {
			c.Why = i18n.T("not logged in")
			cands = append(cands, c)
			continue
		}
		picks, _ := tools.SwarmTierLadder(p, overrides)
		strong := picks[len(picks)-1] // the strong rung
		c.Model, c.Reasoning = strong.Model, strong.Reasoning
		if c.Model == "" {
			c.Why = i18n.T("no strong tier resolves — set swarm_tiers.%s", p)
			cands = append(cands, c)
			continue
		}
		c.Lineage = tools.ModelLineage(c.Model)
		if held, dup := lineage[c.Lineage]; dup {
			c.Why = i18n.T("same weights as %s (both %s) — a panel of one lineage is not decorrelated", held, c.Lineage)
			cands = append(cands, c)
			continue
		}
		if len(panel) >= seats {
			c.Why = i18n.T("the panel was already full")
			cands = append(cands, c)
			continue
		}
		c.Seated = true
		lineage[c.Lineage] = p
		panel = append(panel, raati.Binding{Provider: p, Model: c.Model, Reasoning: c.Reasoning})
		cands = append(cands, c)
	}
	if len(panel) < seats {
		return nil, cands
	}
	return panel, cands
}

// hasCredential asks only whether a credential EXISTS for a provider. It
// deliberately does not go through ResolveCredential, which refreshes an
// expired OAuth token — a network call and a write to auth.json.
//
// The derivation runs when the convene tool is WIRED, once per session, over
// every provider in the registry. Resolving there would mean a fan of token
// refreshes on session start as a side effect of a raati setting, for
// providers the session will never speak to. Presence is the question being
// asked; an expired token is caught at convene time by the code that has
// always caught it.
func hasCredential(providerID string, creds auth.Credentials, states map[string]auth.ProviderState) bool {
	if spec, ok := specFor(providerID); ok {
		for _, ev := range spec.apiKeyEnv {
			if os.Getenv(ev) != "" {
				return true
			}
		}
	}
	switch providerID {
	case "anthropic":
		if os.Getenv("ANTHROPIC_OAUTH_TOKEN") != "" {
			return true
		}
	case "amazon-bedrock":
		// The AWS chain, mirroring ResolveCredential.
		if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" ||
			os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
			return true
		}
	}
	// auth.Describe owns the one wrinkle worth not re-deriving: openai holds a
	// platform key and a ChatGPT/Codex subscription in ONE store slot, and they
	// are two separate logins that can seat two separate providers.
	if st, ok := states[providerID]; ok {
		return st.Method != ""
	}
	return creds.Method(providerID) != ""
}

// autoPanelOrder is the sequence seats are offered in: the operator's own
// list when they gave one, otherwise every known provider in the registry's
// order — the same order `terva models tiers` prints, so what they read
// there is what they get here.
//
// terva does not rank providers by quality. Registry order is arbitrary with
// respect to capability and is documented as such; raati.auto_panel_providers
// is how an operator states a preference, and it is the only opinion on the
// subject that belongs in a config file.
func autoPanelOrder(uc config.Config) []string {
	if len(uc.Raati.AutoPanelProviders) > 0 {
		out := make([]string, 0, len(uc.Raati.AutoPanelProviders))
		for _, p := range uc.Raati.AutoPanelProviders {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	known := ProviderIDs()
	// A provider the operator pinned tiers for is one they care about and
	// may not be in the registry (a named openai-compatible endpoint).
	seen := map[string]bool{}
	for _, p := range known {
		seen[p] = true
	}
	var extra []string
	for p := range uc.SwarmTiers {
		if !seen[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	return append(known, extra...)
}

// AutoPanelReport renders the derivation for a human: the seats it took, then
// what it passed over and why. Written for `terva models tiers`, where the
// question is always "what will actually happen when I convene".
func AutoPanelReport(uc config.Config, seats int) string {
	if !uc.Raati.AutoPanel {
		return i18n.T("raati auto panel: off — set raati.auto_panel to seat level 2 from your logged-in providers.")
	}
	panel, cands := AutoRaatiPanel(uc, seats)
	var b strings.Builder
	if panel == nil {
		b.WriteString(i18n.T("raati auto panel: ON, but it cannot fill %d seats — level 2 stays unavailable.", seats))
	} else {
		b.WriteString(i18n.T("raati auto panel: ON — level 2 seats these, without any raati.level2 entry."))
	}
	b.WriteString("\n")
	for _, c := range cands {
		if c.Seated {
			fmt.Fprintf(&b, "  seat  %-18s %-30s (%s)\n", c.Provider, tools.TierPick{Model: c.Model, Reasoning: c.Reasoning}.Label(), c.Lineage)
		}
	}
	// Two kinds of rejection, reported differently on purpose. "You are not
	// logged in" is true of most of a 25-provider registry and says nothing;
	// listed one per line it buries the handful of rejections that are
	// actionable. So those get their own line — every one of them named, since
	// which provider is missing IS the answer when the panel came up short —
	// and everything else gets a line of its own.
	notLoggedIn := i18n.T("not logged in")
	var absent []string
	skipped := false
	for _, c := range cands {
		if c.Seated {
			continue
		}
		if c.Why == notLoggedIn {
			absent = append(absent, c.Provider)
			continue
		}
		if !skipped {
			b.WriteString(i18n.T("  passed over:") + "\n")
			skipped = true
		}
		fmt.Fprintf(&b, "    %-18s %s\n", c.Provider, c.Why)
	}
	if len(absent) > 0 {
		b.WriteString(wrapList("  "+notLoggedIn+": ", absent, 78))
	}
	return b.String()
}

// wrapList prints a labelled comma-separated list folded to width, indented
// under its label. Nothing is elided — a truncated list would read as a
// complete one, and the whole point of this line is that the operator can see
// which provider is missing.
func wrapList(label string, items []string, width int) string {
	var b strings.Builder
	b.WriteString(label)
	col, indent := len(label), strings.Repeat(" ", 4)
	for i, it := range items {
		sep := ""
		if i > 0 {
			sep = ", "
		}
		if col+len(sep)+len(it) > width && i > 0 {
			b.WriteString(",\n" + indent)
			col = len(indent)
			sep = ""
		}
		b.WriteString(sep + it)
		col += len(sep) + len(it)
	}
	b.WriteString("\n")
	return b.String()
}
