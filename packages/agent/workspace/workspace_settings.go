package workspace

import (
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/build"
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

// The labels fold in each level's rough token budget (the SettingOption wire
// carries no per-option description), so both the web dropdown and the TUI
// picker render self-describing choices from this one source.
var reasoningOptions = []ctrlproto.SettingOption{
	{Value: "", Label: i18n.M("off")},
	{Value: "minimum", Label: i18n.M("minimum — very brief (~1k tokens)")},
	{Value: "low", Label: i18n.M("low — light (~2k tokens)")},
	{Value: "medium", Label: i18n.M("medium — moderate (~8k tokens)")},
	{Value: "high", Label: i18n.M("high — deep (~16k tokens)")},
	{Value: "maximum", Label: i18n.M("maximum — highest (~32k tokens)")},
	{Value: "max", Label: i18n.M("max — native max (GPT-5.6 / adaptive Claude)")},
}

var autoCompactOptions = []ctrlproto.SettingOption{
	{Value: "steps", Label: i18n.M("steps — condense mid-turn as the window fills")},
	{Value: "turns", Label: i18n.M("turns — condense only at turn boundaries")},
	{Value: "off", Label: i18n.M("off — never auto-condense")},
}

var themeOptions = []ctrlproto.SettingOption{
	{Value: "auto", Label: i18n.M("auto — match the terminal")},
	{Value: "dark", Label: i18n.M("dark")},
	{Value: "light", Label: i18n.M("light")},
	{Value: "dark-daltonized", Label: i18n.M("dark (daltonized)")},
	{Value: "light-daltonized", Label: i18n.M("light (daltonized)")},
	{Value: "daltonized", Label: i18n.M("daltonized")},
}

var temperatureOptions = []ctrlproto.SettingOption{
	{Value: "", Label: i18n.M("default — the model/provider default")},
	{Value: "0", Label: i18n.M("0.0 — most deterministic")},
	{Value: "0.3", Label: i18n.M("0.3")},
	{Value: "0.7", Label: i18n.M("0.7")},
	{Value: "1", Label: i18n.M("1.0 — most varied")},
}

// optionsWithCurrent prepends value as a bare option when it is non-empty and
// absent from opts, so an enum setting whose current value is a custom theme or
// an off-preset temperature (set by hand in config.json) still round-trips —
// the SettingItem.Value always matches a listed option.
func optionsWithCurrent(opts []ctrlproto.SettingOption, value string) []ctrlproto.SettingOption {
	if value == "" {
		return opts
	}
	for _, o := range opts {
		if o.Value == value {
			return opts
		}
	}
	// A custom value (e.g. a user theme name, an off-preset temperature) is
	// displayed verbatim; localizeOptions' i18n.T leaves an unknown string as-is.
	return append([]ctrlproto.SettingOption{{Value: value, Label: value}}, opts...)
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
		{
			Key: "lazy_tools", Label: i18n.T("Lazy tool loading"), Type: "bool",
			Value:       boolStr(cfg.LazyTools),
			Description: i18n.T("Advertise only the core coding tools at first and let the agent load extension/MCP tool groups on demand (activate_tools), trimming the tool schemas that fill context every turn."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "auto_compact", Label: i18n.T("Auto-condense"), Type: "enum",
			Value: autoCompactValue(cfg), Options: localizeOptions(autoCompactOptions),
			Description: i18n.T("When to automatically condense the transcript as the context window fills."),
			Note:        i18n.T("applies live to every session"),
		},
		{
			Key: "temperature", Label: i18n.T("Temperature"), Type: "enum",
			Value:       temperatureValue(cfg),
			Options:     localizeOptions(optionsWithCurrent(temperatureOptions, temperatureValue(cfg))),
			Description: i18n.T("Sampling temperature (0–2). Higher is more varied; the default defers to the model."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "theme", Label: i18n.T("Theme"), Type: "enum",
			Value:       themeValue(cfg),
			Options:     localizeOptions(optionsWithCurrent(themeOptions, themeValue(cfg))),
			Description: i18n.T("Color theme for the terminal UI."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "inline_images", Label: i18n.T("Inline images"), Type: "bool",
			Value:       boolStr(cfg.InlineImagesEnabled == nil || *cfg.InlineImagesEnabled),
			Description: i18n.T("Render images inline in terminals that support an image protocol."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "recursive_file_suggest", Label: i18n.T("Recursive file search"), Type: "bool",
			Value:       boolStr(cfg.RecursiveFileSuggest == nil || *cfg.RecursiveFileSuggest),
			Description: i18n.T("Fuzzy-search the whole tree in the @-mention file picker instead of browsing one directory at a time."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "respect_gitignore", Label: i18n.T("Respect .gitignore"), Type: "bool",
			Value:       boolStr(cfg.RespectGitignore == nil || *cfg.RespectGitignore),
			Description: i18n.T("Hide git-ignored files from the @-mention file picker."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "lore", Label: i18n.T("Lore (keyed context)"), Type: "bool",
			Value:       boolStr(cfg.Lore == nil || *cfg.Lore),
			Description: i18n.T("Discover and inject keyword-triggered context entries (lore) into the prompt when their trigger keys appear. Off is the persistent form of --no-lore."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "swarm_worktrees", Label: i18n.T("Swarm worktrees"), Type: "bool",
			Value:       boolStr(cfg.SwarmWorktrees != nil && *cfg.SwarmWorktrees),
			Description: i18n.T("Give each background sub-agent its own git worktree so parallel work never collides in the tree."),
			Note:        i18n.T("applies to new sessions"),
		},
		{
			Key: "core_pack_offer", Label: i18n.T("Offer the core tool pack"), Type: "bool",
			Value:       boolStr(!cfg.DisableCorePackOffer),
			Description: i18n.T("Offer to install the recommended extension pack on the first run in a new workspace."),
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
	// Engine features project into the same pane (the seam build.EngineFeatures
	// declares). A lazy-tools-bound feature nests under the lazy_tools toggle
	// the same way the nudge nests under auto_swarm: hidden while meaningless,
	// re-appearing when the parent flips on (the surface re-fetches).
	for _, f := range build.EngineFeatures {
		if f.RequiresLazyTools && !cfg.LazyTools {
			continue
		}
		items = append(items, ctrlproto.SettingItem{
			Key: f.ID, Label: i18n.T(f.Title), Type: "bool",
			Value:       boolStr(build.EngineFeatureOn(cfg.EngineFeatures, f)),
			Description: i18n.T(f.Desc),
			Note:        i18n.T("applies live to every session"),
		})
	}
	return ctrlproto.SettingsView{Items: items}
}

// autoCompactValue is the current auto-compact policy for the enum, defaulting
// unset/blank to "steps" (core.autoCompactMode's fallback).
func autoCompactValue(cfg config.Config) string {
	if v := strings.ToLower(strings.TrimSpace(cfg.AutoCompact)); v != "" {
		return v
	}
	return "steps"
}

// themeValue maps the stored theme to its enum value: "" (the canonical "auto")
// displays as "auto".
func themeValue(cfg config.Config) string {
	if cfg.Theme == "" {
		return "auto"
	}
	return cfg.Theme
}

// temperatureValue renders the stored *float32 as the enum value ("" when unset
// = the model/provider default), in the shortest form that round-trips.
func temperatureValue(cfg config.Config) string {
	if cfg.Temperature == nil {
		return ""
	}
	return strconv.FormatFloat(float64(*cfg.Temperature), 'g', -1, 32)
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
	case "lazy_tools":
		// Persist-only, like auto_title: lazy visibility is resolved at session
		// build (NewAgent → EnableLazyTools + the activate_tools registration),
		// so it applies to new sessions rather than reshaping a running one.
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.LazyTools = on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "auto_compact":
		switch val {
		case "steps", "turns", "off":
		default:
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown auto_compact %q (steps|turns|off)", val)
		}
		// Live for every session: Agent.AutoCompactPolicy re-reads config on each
		// threshold check, so this applies on the next check with no rebuild.
		if err := config.MutateConfig(func(c *config.Config) { c.AutoCompact = val }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "temperature":
		var tp *float32
		if val != "" {
			f, err := strconv.ParseFloat(val, 32)
			if err != nil || f < 0 || f > 2 {
				return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "temperature must be a number between 0 and 2")
			}
			t := float32(f)
			tp = &t
		}
		if err := config.MutateConfig(func(c *config.Config) { c.Temperature = tp }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "theme":
		// "auto" is stored as the canonical empty string (SetTheme's convention).
		theme := val
		if theme == "auto" {
			theme = ""
		}
		if err := config.MutateConfig(func(c *config.Config) { c.Theme = theme }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "inline_images":
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.InlineImagesEnabled = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "recursive_file_suggest":
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.RecursiveFileSuggest = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "respect_gitignore":
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.RespectGitignore = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "lore":
		// Persist-only: lore discovery/injection is resolved at session build
		// (cfg.Lore == nil || *cfg.Lore), so it applies to new sessions — the
		// persistent equivalent of the per-run --no-lore flag.
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.Lore = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "swarm_worktrees":
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) { c.SwarmWorktrees = &on }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case "core_pack_offer":
		// Inverted: the toggle reads "offer" but the config flag is "disable".
		off := val != "true"
		if err := config.MutateConfig(func(c *config.Config) { c.DisableCorePackOffer = off }); err != nil {
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
		f, ok := build.EngineFeatureByID(key)
		if !ok {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown setting %q", key)
		}
		// An engine feature: persist the override, then flip every live
		// session's agent through the feature's own Apply (new sessions read
		// the override at build). A session mid-Prompt picks it up on its
		// next Prompt — the loop snapshots its gates per Prompt.
		on := val == "true"
		if err := config.MutateConfig(func(c *config.Config) {
			if c.EngineFeatures == nil {
				c.EngineFeatures = map[string]bool{}
			}
			c.EngineFeatures[key] = on
		}); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
		s.ws.applyEngineFeature(f, on)
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

// applyEngineFeature flips an engine feature live on every session's agent —
// the same fan-out as applyReasoning, through the feature's own Apply.
func (w *Workspace) applyEngineFeature(f build.EngineFeature, on bool) {
	w.mu.Lock()
	sess := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		sess = append(sess, s)
	}
	w.mu.Unlock()
	for _, s := range sess {
		if s.agent != nil {
			f.Apply(s.agent, on)
		}
	}
}
