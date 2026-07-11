package workspace

import (
	"os"
	"slices"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The settings pane — the first management surface (see docs/proposals/
// web-surfaces.md). v1 exposes approval mode (per-session, live via the confirm
// gate), reasoning (config default + live), and auto-title (config). Persisted
// settings go through MutateConfig (concurrency-safe); per-session ones apply to
// the session's live agent/gate.

// The option labels are declared here (init time, before i18n.Configure) but
// displayed later, so they carry i18n.M marks (extractor records them, value
// stays English) and are translated at render time by localizeOptions.
var approvalOptions = []ctrlproto.SettingOption{
	{Value: "plan", Label: i18n.M("plan — read-only")},
	{Value: "ask", Label: i18n.M("ask — prompt everything")},
	{Value: "auto-edit", Label: i18n.M("auto-edit — auto reads/edits, ask commands")},
	{Value: "workspace", Label: i18n.M("workspace — auto in-workspace, ask foreign")},
	{Value: "yolo", Label: i18n.M("yolo — no prompts")},
}

var reasoningOptions = []ctrlproto.SettingOption{
	{Value: "", Label: i18n.M("off")},
	{Value: "minimum", Label: i18n.M("minimum")},
	{Value: "low", Label: i18n.M("low")},
	{Value: "medium", Label: i18n.M("medium")},
	{Value: "high", Label: i18n.M("high")},
	{Value: "maximum", Label: i18n.M("maximum")},
	{Value: "max", Label: i18n.M("max")},
}

// localizeOptions returns a copy of opts with each label translated to the
// active language — the render-time half of the i18n.M declared above.
func localizeOptions(opts []ctrlproto.SettingOption) []ctrlproto.SettingOption {
	out := make([]ctrlproto.SettingOption, len(opts))
	for i, o := range opts {
		out[i] = ctrlproto.SettingOption{Value: o.Value, Label: i18n.T(o.Label)}
	}
	return out
}

// settingsView builds the settings pane for this session.
func (s *wsSession) settingsView() ctrlproto.SettingsView {
	cfg, _ := config.LoadConfig()

	approval := string(core.ApprovalYolo)
	if s.gate != nil {
		approval = string(s.gate.Mode())
	}
	autoSwarm := cfg.AutoSwarmEnabled != nil && *cfg.AutoSwarmEnabled

	items := []ctrlproto.SettingItem{
		{
			Key: "approval", Label: i18n.T("Approval mode"), Type: "enum",
			Value: approval, Options: localizeOptions(approvalOptions),
			Description: i18n.T("How tool calls are gated for this session."),
			Note:        i18n.T("per-session — not saved (a security posture, like the TUI)"),
		},
		{
			Key: "reasoning", Label: i18n.T("Thinking"), Type: "enum",
			Value: cfg.Reasoning, Options: localizeOptions(reasoningOptions),
			Description: i18n.T("Reasoning effort. Applies live and becomes the default for new sessions."),
		},
		{
			Key: "auto_title", Label: i18n.T("Auto-title sessions"), Type: "bool",
			Value:       boolStr(cfg.AutoTitle != nil && *cfg.AutoTitle),
			Description: i18n.T("Generate a short session title with a small model call instead of the first message line."),
		},
		{
			Key: "language", Label: i18n.T("Language"), Type: "enum",
			Value: i18n.ActiveLang(), Options: localeOptions(),
			Description: i18n.T("UI language. Switches live for every open tab and is saved as the default."),
		},
		{
			Key: "auto_swarm", Label: i18n.T("Background sub-agents"), Type: "bool",
			Value:       boolStr(autoSwarm),
			Description: i18n.T("Let the agent spawn background sub-agents (swarm_spawn) for independent parallel sub-tasks."),
			Note:        i18n.T("applies to new sessions"),
		},
	}
	// The nudge is meaningful only when the tool is on (Toggle 2 nested under
	// Toggle 1); it re-appears when auto_swarm flips on (the surface re-fetches).
	if autoSwarm {
		items = append(items, ctrlproto.SettingItem{
			Key: "auto_swarm_nudge", Label: i18n.T("Proactive delegation"), Type: "bool",
			Value:       boolStr(cfg.AutoSwarmNudge == nil || *cfg.AutoSwarmNudge),
			Description: i18n.T("Nudge the agent to proactively split work across sub-agents. Off keeps the tool but lets the agent decide when to use it."),
			Note:        i18n.T("applies to new sessions"),
		})
	}
	return ctrlproto.SettingsView{Items: items}
}

// boolStr renders a bool as the "true"/"false" the SettingItem wire uses.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// availableLocales lists the languages the panel can switch to: English (the
// source), every embedded translation, and any on-disk overlay under
// $TERVA_HOME/locales — mirroring `terva locale list`.
func availableLocales() []string {
	set := map[string]bool{"en": true}
	for _, n := range i18n.EmbeddedLocaleNames() {
		set[n] = true
	}
	if entries, err := os.ReadDir(config.LocalesDir()); err == nil {
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".json")
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || name == "en" ||
				strings.HasSuffix(name, ".todo") || strings.HasSuffix(name, ".export") {
				continue
			}
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// localeOptions builds the language enum options (tag = value and label).
func localeOptions() []ctrlproto.SettingOption {
	langs := availableLocales()
	opts := make([]ctrlproto.SettingOption, 0, len(langs))
	for _, l := range langs {
		opts = append(opts, ctrlproto.SettingOption{Value: l, Label: l})
	}
	return opts
}

// settingsAction applies a settings change: {action:"set", args:{key,value}}.
func (s *wsSession) settingsAction(action string, args map[string]string) error {
	if action != "set" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown settings action %q", action)
	}
	key, val := args["key"], args["value"]
	switch key {
	case "approval":
		mode, err := core.ParseApprovalMode(val)
		if err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
		}
		if s.gate != nil {
			s.gate.SetMode(mode) // per-session, live; not persisted
		}
		// The gate blocks mutating calls, but the mode must also reshape the
		// model's tool VIEW — plan withholds mutating built-ins and non-read-
		// only extension/MCP tools (legacy parity: cli.go setApprovalMode
		// rebuilds the registry). Record the mode on the session's args so
		// this rebuild — and every later one (ext reload, MCP toggle, trust
		// flip) — resolves in it, then swap the tool set live.
		s.mu.Lock()
		s.args.Approval = val
		s.mu.Unlock()
		s.rebuildTools("approval-mode")
	case "reasoning":
		if err := config.MutateConfig(func(c *config.Config) { c.Reasoning = val }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
		s.ws.applyReasoning(val) // live to every session's agent
	case "auto_title":
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.AutoTitle = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "auto_swarm":
		// Tool availability (Toggle 1). Persist, then re-derive every live
		// session's view: rebuildTools re-runs injectExtraTools (which reads
		// the fresh AutoSwarmEnabled — the tool half) and re-resolves the
		// system prompt (the nudge half), so the model's tools[] and prompt
		// reflect the toggle on each session's next turn.
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.AutoSwarmEnabled = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
		s.ws.applyAutoSwarm()
	case "auto_swarm_nudge":
		// Proactive-delegation nudge (Toggle 2). Same live re-derivation: the
		// nudge addendum is composed at Resolve time, so a rebuild applies it.
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.AutoSwarmNudge = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
		s.ws.applyAutoSwarm()
	case "language":
		// Switch the daemon's active UI language live: swap the process-global
		// i18n, persist as the default, and tell every tab to re-fetch its string
		// catalog + panes and re-render. Server-rendered strings (surface titles,
		// these settings labels) resolve in the new language on the next fetch;
		// already-baked agent system prompts stay until a new session.
		if !slices.Contains(availableLocales(), val) {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown language %q", val)
		}
		if err := i18n.Configure(val, config.TervaHome()); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "configure locale: %v", err)
		}
		if err := config.MutateConfig(func(c *config.Config) { c.Language = val }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
		s.ws.BroadcastAll(ctrlproto.LocaleChangedEvent(i18n.ActiveLang()))
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown setting %q", key)
	}
	// Settings surface reflects per-session (approval) + shared config; nudge
	// every client to re-fetch so config changes converge across sessions.
	s.ws.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("settings"))
	return nil
}

// applyAutoSwarm rebuilds every live session's model-facing view (tool set +
// system prompt) after an auto-swarm toggle — both halves read the persisted
// config fresh inside rebuildTools' Resolve/inject pipeline.
func (w *Workspace) applyAutoSwarm() {
	w.rebuildAllSessions("auto-swarm")
}

// applyReasoning sets the reasoning level live on every session's agent (a
// lock-guarded field write), so a change takes effect immediately everywhere.
func (w *Workspace) applyReasoning(level string) {
	w.mu.Lock()
	sess := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		sess = append(sess, s)
	}
	w.mu.Unlock()
	for _, s := range sess {
		if s.agent != nil {
			s.agent.SetReasoning(level)
		}
	}
}
