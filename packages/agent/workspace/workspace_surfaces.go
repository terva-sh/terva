package workspace

import (
	"context"
	"sort"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Surfaces are the web UI's auxiliary panes — the generalization of the context
// modal (see docs/proposals/web-surfaces.md). Core panes (context, usage) are
// always present; extension panels and aggregated status segments appear via the
// webExtHooks bridge as their extensions open/update/close them.

// webPanel is one open extension panel, keyed in wsSession.extPanels by its
// surface id (see panelSurfaceID). A panel may carry a rich widget tree
// (rendered as kind=widgets) and/or text lines (kind=panel fallback).
type webPanel struct {
	ext, id, title, footer string
	lines                  []string
	widgets                []extproto.Widget
}

func panelSurfaceID(ext, id string) string { return "ext:" + ext + ":" + id }

// webExtHooks routes the extension driver's panel/status callbacks into the
// session's surface registry. It embeds nonInteractiveExtHooks so every other
// host hook (Notify/Display/…) keeps its non-interactive behavior.
type webExtHooks struct {
	build.NonInteractiveExtHooks
	s *wsSession
}

func (h webExtHooks) OpenPanel(ext string, spec extproto.PanelSpec) { h.s.paneOpen(ext, spec) }
func (h webExtHooks) UpdatePanel(ext, id, title string, lines []string, footer string, widgets []extproto.Widget) {
	h.s.paneUpdate(ext, id, title, lines, footer, widgets)
}
func (h webExtHooks) ClosePanel(ext, id string) { h.s.paneClose(ext, id) }
func (h webExtHooks) RefreshStatus()            { h.s.paneStatusChanged() }

// RefreshContext / RefreshTools fire when an extension changes its injected
// context (refresh_context, protocol 3) or its withdrawn tool set
// (set_withdrawn_tools, protocol 4). rebuildTools re-Resolves and re-merges the
// extension source, so it re-folds the fresh static context into the system
// prompt AND rebuilds the tool registry against the current withdrawn set — the
// daemon twin of the legacy RebuildExtensionContext/RebuildExtensionTools
// closures. Without these overrides both inherit no-ops from
// nonInteractiveExtHooks, so a dynamic ext (e.g. memory) never reaches the
// model. Shared by terva web, which drives the same webExtHooks.
func (h webExtHooks) RefreshContext() { h.s.rebuildTools("extension-context") }
func (h webExtHooks) RefreshTools()   { h.s.rebuildTools("tool-withdrawal") }

// --- pane registry mutators (called from ext driver goroutines) ---

func (s *wsSession) paneOpen(ext string, spec extproto.PanelSpec) {
	sid := panelSurfaceID(ext, spec.ID)
	s.paneMu.Lock()
	s.extPanels[sid] = &webPanel{ext: ext, id: spec.ID, title: spec.Title, lines: spec.Lines, footer: spec.Footer, widgets: spec.Widgets}
	s.paneMu.Unlock()
	s.broadcast(ctrlproto.SurfacesChangedEvent())
	s.broadcast(ctrlproto.SurfaceUpdatedEvent(sid))
}

func (s *wsSession) paneUpdate(ext, id, title string, lines []string, footer string, widgets []extproto.Widget) {
	sid := panelSurfaceID(ext, id)
	s.paneMu.Lock()
	p := s.extPanels[sid]
	created := p == nil
	if created {
		p = &webPanel{ext: ext, id: id}
		s.extPanels[sid] = p
	}
	titleChanged := p.title != title
	p.title, p.lines, p.footer, p.widgets = title, lines, footer, widgets
	s.paneMu.Unlock()
	if created || titleChanged {
		s.broadcast(ctrlproto.SurfacesChangedEvent())
	}
	s.broadcast(ctrlproto.SurfaceUpdatedEvent(sid))
}

func (s *wsSession) paneClose(ext, id string) {
	sid := panelSurfaceID(ext, id)
	s.paneMu.Lock()
	_, existed := s.extPanels[sid]
	delete(s.extPanels, sid)
	s.paneMu.Unlock()
	if existed {
		s.broadcast(ctrlproto.SurfacesChangedEvent())
	}
}

func (s *wsSession) paneStatusChanged() {
	// The status-segment set changed: both the "status" surface's existence and
	// its content may have shifted.
	s.broadcast(ctrlproto.SurfacesChangedEvent())
	s.broadcast(ctrlproto.SurfaceUpdatedEvent("status"))
}

// --- surface registry + content ---

func (s *wsSession) surfaceList() []ctrlproto.SurfaceMeta {
	metas := []ctrlproto.SurfaceMeta{
		// One "Usage" pane carries both the live usage picture (gauge, cumulative
		// cost, subscription windows) and the context-size breakdown that explains
		// where that usage goes. Id stays "context" (stable); the old separate
		// "usage" pane was a strict subset of this one.
		{ID: "context", Title: i18n.T("Usage"), Icon: "📊", Kind: "context", Scope: "session", Live: true},
		{ID: "settings", Title: i18n.T("Settings"), Icon: "⚙️", Kind: "settings", Scope: "session", Actions: true},
	}
	if s.ws != nil && s.ws.hasTasks(s.id) {
		// Titled "Agents", id still "tasks": the id is wire contract and clients
		// key off it, but the pane shows background SUB-AGENTS, and calling it
		// Tasks collided with the model's own task board — two panes, one name,
		// different data. "Agents" is what it always was.
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "tasks", Title: i18n.T("Agents"), Icon: "🐝", Kind: "tasks", Scope: "workspace", Live: true, Actions: true})
	}
	// The model's own task board. Session-scoped (each session tracks its own
	// work) and Live — buildSession broadcasts surface_updated("taskboard") at
	// every turn end, which is the only time it can change. Read-only on purpose:
	// the model owns this list, and a human editing it mid-turn would create a
	// state the model cannot see it lost.
	if s.hasTaskBoard() {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "taskboard", Title: i18n.T("Tasks"), Icon: "✓", Kind: "taskboard", Scope: "session", Live: true})
	}
	if s.ws != nil && s.ws.swarm != nil {
		// The deliberation board is always offered (an idle board is the
		// convene form); the deliberation itself is workspace-global,
		// like the swarm that runs it.
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "raati", Title: i18n.T("Raati"), Icon: "⚖️", Kind: "raati", Scope: "workspace", Live: true, Actions: true})
	}
	// Managed git worktrees — the same view the TUI's /worktree panel renders.
	// Gated on the STRICT InRepo check (not GitAvailable's nearby-repo
	// leniency) so the tab only appears where surface.get will succeed.
	// Session-scoped: the repo resolves from this session's cwd and the claim
	// identity is this session's own. Read-only, and not Live — no push event
	// exists for worktree changes, so the pane fetches on open (the same
	// freshness the TUI panel has).
	if worktree.InRepo(s.cwd) {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "worktrees", Title: i18n.T("Worktrees"), Icon: "🌳", Kind: "worktrees", Scope: "session"})
	}
	if s.extMgr != nil && len(s.extMgr.Commands()) > 0 {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "commands", Title: i18n.T("Commands"), Icon: "⌘", Kind: "commands", Scope: "session", Actions: true})
	}
	if s.extMgr != nil && s.extMgr.Count() > 0 {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "extensions", Title: i18n.T("Extensions"), Icon: "🔌", Kind: "extensions", Scope: "session", Actions: true})
	}
	if s.gate != nil {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "permissions", Title: i18n.T("Permissions"), Icon: "🔐", Kind: "permissions", Scope: "session", Actions: true})
	}
	if len(s.loreSnapshot()) > 0 {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "lore", Title: i18n.T("Lore"), Icon: "📖", Kind: "lore", Scope: "session", Actions: true})
	}
	// Offered whenever memory is ON, not only when it holds something: an empty
	// memory is a state a user should be able to see (and is the state right
	// before the first fact lands), unlike lore, where an absent pane means
	// there are no entries anywhere to inspect.
	if s.hasMemory() {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "memory", Title: i18n.T("Memory"), Icon: "🧠", Kind: "memory", Scope: "session", Live: true, Actions: true})
	}
	if s.ws != nil && s.ws.mcpAdapter != nil && len(build.ListMCPServers(s.cwd, s.trusted.Load(), s.ws.mcpAdapter.Mgr)) > 0 {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "mcp", Title: i18n.T("MCP"), Icon: "🔗", Kind: "mcp", Scope: "workspace", Actions: true})
	}
	// The character/persona library — a workspace-global pane so the panel
	// manages the same store the Stage app plays from. Always offered (the
	// embedded persona crew is never empty); Live so an import/edit refreshes it.
	if s.ws != nil {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "characters", Title: i18n.T("Characters"), Icon: "🎭", Kind: "characters", Scope: "workspace", Live: true})
	}
	// Always offered. A credential is workspace-scoped (one auth.json, one
	// daemon), and the pane's whole job is to explain an ABSENCE — why the model
	// picker is short, why a subscription stopped working — so hiding it when
	// nothing is logged in would hide it exactly when it is needed. Read-only for
	// now: no Actions until the auth group can serve them.
	if s.ws != nil {
		// Live: an auth_state event moves this pane (a login lands, a device flow
		// completes in a browser on another device). Actions only when the daemon
		// will actually serve them — see EnableAuth.
		metas = append(metas, ctrlproto.SurfaceMeta{
			ID: "providers", Title: i18n.T("Providers"), Icon: "🔑", Kind: "providers",
			Scope: "workspace", Live: true, Actions: s.ws.canLogin(),
		})
	}
	// Offered whenever any chat service is registered, so the pane can explain
	// "not configured" / "run terva bot setup" rather than silently missing.
	if len(chat.Services()) > 0 {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "chat", Title: i18n.T("Chat"), Icon: "💬", Kind: "chat", Scope: "workspace", Live: true, Actions: true})
	}
	if s.extMgr != nil && len(s.extMgr.StatusSegments()) > 0 {
		metas = append(metas, ctrlproto.SurfaceMeta{ID: "status", Title: i18n.T("Status"), Icon: "🔔", Kind: "panel", Scope: "session", Live: true})
	}
	s.paneMu.Lock()
	ids := make([]string, 0, len(s.extPanels))
	for id := range s.extPanels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := s.extPanels[id]
		title := p.title
		if title == "" {
			title = p.ext
		}
		kind := "panel"
		if len(p.widgets) > 0 {
			kind = "widgets"
		}
		metas = append(metas, ctrlproto.SurfaceMeta{
			ID: id, Title: title, Icon: "🧩", Kind: kind, Scope: "session", Live: true, Actions: true,
		})
	}
	s.paneMu.Unlock()
	return metas
}

func (s *wsSession) surface(id string) (ctrlproto.Surface, error) {
	switch id {
	case "context":
		b := s.contextBreakdown()
		return ctrlproto.Surface{ID: id, Title: i18n.T("Usage"), Kind: "context", Context: &b}, nil
	case "tasks":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Agents"), Kind: "tasks", Tasks: s.ws.taskList(s.id)}, nil
	case "taskboard":
		// The per-session task board (built-in task_* tools) — what the MODEL is
		// tracking, as opposed to the "tasks" pane above, which is the swarm.
		// NotFound when the session has no board (chat/play/--no-tools) so a
		// client can say "unavailable" rather than show an empty panel that will
		// never populate.
		if !s.hasTaskBoard() {
			return ctrlproto.Surface{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no task board in this session"))
		}
		return ctrlproto.Surface{ID: id, Title: i18n.T("Tasks"), Kind: "taskboard", TaskBoard: taskBoardView(s.tasks)}, nil
	case "worktrees":
		// Managed git worktrees (built-in engine). The TUI's /worktree panel +
		// glance fetch it by explicit id; the web tab comes from surfaceList
		// (gated on the same InRepo check). NotFound outside a git repo.
		wv, err := s.worktreesView()
		if err != nil {
			return ctrlproto.Surface{}, err
		}
		return ctrlproto.Surface{ID: id, Title: i18n.T("Worktrees"), Kind: "worktrees", Worktrees: wv}, nil
	case "raati":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Raati"), Kind: "raati", Raati: s.ws.raatiView()}, nil
	case "settings":
		sv := s.settingsView()
		return ctrlproto.Surface{ID: id, Title: i18n.T("Settings"), Kind: "settings", Settings: &sv}, nil
	case "commands":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Commands"), Kind: "commands", Commands: s.commandsView()}, nil
	case "extensions":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Extensions"), Kind: "extensions", Extensions: s.extensionsView()}, nil
	case "permissions":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Permissions"), Kind: "permissions", Permissions: s.permissionsView()}, nil
	case "lore":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Lore"), Kind: "lore", Lore: s.loreView()}, nil
	case "memory":
		// NotFound rather than an empty pane when memory is off (--no-memory):
		// a client can then say "switched off" instead of showing a list that
		// will never populate and reads as broken.
		if !s.hasMemory() {
			return ctrlproto.Surface{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("memory is not enabled for this session"))
		}
		return ctrlproto.Surface{ID: id, Title: i18n.T("Memory"), Kind: "memory", Memory: s.memoryView()}, nil
	case "characters":
		return ctrlproto.Surface{ID: id, Title: i18n.T("Characters"), Kind: "characters", Characters: s.charactersView()}, nil
	case "mcp":
		return ctrlproto.Surface{ID: id, Title: i18n.T("MCP"), Kind: "mcp", MCP: s.mcpView()}, nil
	case "chat":
		cv := s.ws.chatView()
		return ctrlproto.Surface{ID: id, Title: i18n.T("Chat"), Kind: "chat", Chat: &cv}, nil
	case "providers":
		// The pane and the auth.providers method are the same data: one shape,
		// one implementation, nothing to drift.
		pv, err := s.ws.AuthProviders(context.Background())
		if err != nil {
			return ctrlproto.Surface{}, err
		}
		return ctrlproto.Surface{ID: id, Title: i18n.T("Providers"), Kind: "providers", Providers: &pv}, nil
	case "status":
		var segs []string
		if s.extMgr != nil {
			segs = s.extMgr.StatusSegments()
		}
		return ctrlproto.Surface{ID: id, Title: i18n.T("Status"), Kind: "panel", Panel: &ctrlproto.PanelView{Lines: segs}}, nil
	}
	s.paneMu.Lock()
	p := s.extPanels[id]
	s.paneMu.Unlock()
	if p == nil {
		return ctrlproto.Surface{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no surface %q", id))
	}
	title := p.title
	if title == "" {
		title = p.ext
	}
	// A panel with a widget tree renders natively (kind=widgets); otherwise it's
	// the text-lines fallback (kind=panel). An extension targeting both frontends
	// sends both, so the TUI keeps its lines while the web gets the rich pane.
	if len(p.widgets) > 0 {
		return ctrlproto.Surface{ID: id, Title: title, Kind: "widgets", Widgets: mapExtWidgets(p.widgets)}, nil
	}
	return ctrlproto.Surface{
		ID: id, Title: title, Kind: "panel",
		Panel: &ctrlproto.PanelView{Ext: p.ext, Lines: p.lines, Footer: p.footer},
	}, nil
}

// mapExtWidgets converts the extension-protocol widget tree to the control-plane
// vocabulary (structurally identical; kept as separate types so extproto and
// ctrlproto stay decoupled, like PanelSpec → PanelView).
func mapExtWidgets(ws []extproto.Widget) []ctrlproto.Widget {
	out := make([]ctrlproto.Widget, len(ws))
	for i, w := range ws {
		out[i] = ctrlproto.Widget{
			Type: w.Type, Text: w.Text, Tone: w.Tone, Level: w.Level, Label: w.Label,
			Value: w.Value, Max: w.Max, Unit: w.Unit, Columns: w.Columns, Cells: w.Cells,
			ActionID: w.ActionID, Children: mapExtWidgets(w.Children),
		}
		for _, r := range w.Rows {
			out[i].Rows = append(out[i].Rows, ctrlproto.KV{Key: r.Key, Value: r.Value, Note: r.Note, Mono: r.Mono})
		}
		for _, it := range w.Items {
			out[i].Items = append(out[i].Items, ctrlproto.ListItem{Text: it.Text, Note: it.Note, Tone: it.Tone, ActionID: it.ActionID})
		}
	}
	return out
}

func (s *wsSession) surfaceAction(id, action string, args map[string]string) error {
	if id == "tasks" {
		return s.ws.taskAction(s.id, action, args)
	}
	if id == "raati" {
		return s.ws.raatiAction(s, action, args)
	}
	if id == "settings" {
		return s.settingsAction(action, args)
	}
	if id == "commands" {
		return s.commandAction(action, args)
	}
	if id == "permissions" {
		return s.permissionsAction(action, args)
	}
	if id == "extensions" {
		return s.extensionsAction(action, args)
	}
	if id == "mcp" {
		return s.mcpAction(action, args)
	}
	if id == "lore" {
		return s.loreAction(action, args)
	}
	if id == "memory" {
		return s.memoryAction(action, args)
	}
	if id == "worktrees" {
		return s.worktreeAction(action, args)
	}
	if id == "chat" {
		// The issuing session is the bind target for connect: the mirror lands
		// where the user asked for it, and stays there.
		return s.ws.chatAction(s.id, action, args)
	}
	s.paneMu.Lock()
	p := s.extPanels[id]
	s.paneMu.Unlock()
	if p == nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no actionable surface %q", id))
	}
	switch action {
	case "key":
		s.extMgr.SendPanelKey(p.ext, p.id, args["key"], args["text"])
	case "action":
		// A widget action/list button: deliver its action_id to the extension as
		// a panel key, reusing the panel-key channel (the ext's OnPanelKey handler
		// dispatches on it and re-renders).
		s.extMgr.SendPanelKey(p.ext, p.id, args["id"], "")
	case "close":
		s.extMgr.SendPanelClose(p.ext, p.id)
		s.paneClose(p.ext, p.id)
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown surface action %q", action))
	}
	return nil
}

// usageView builds the usage/budget pane (also reused by the context breakdown):
// real last-turn context tokens, cumulative session usage, subscription windows.
func (s *wsSession) usageView() ctrlproto.UsageView {
	prov, model := s.currentModel()
	uv := ctrlproto.UsageView{Provider: prov, Model: model, Subscription: s.subscription}
	if ag := s.agent; ag != nil {
		last := ag.LastTurnUsage()
		uv.ContextTokens = last.PromptTokens()
		uv.Cumulative = toCtrlUsage(ag.Cost())
		if snap, ok := ag.Usage(); ok {
			uv.Windows = usageWindows(snap.Windows)
		}
	}
	if model != "" {
		uv.Window = provider.ContextGauge("", model)
	}
	return uv
}

// --- WorkspaceService surface group ---

func (w *Workspace) Surfaces(ctx context.Context, sess string) ([]ctrlproto.SurfaceMeta, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	return s.surfaceList(), nil
}

func (w *Workspace) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.Surface{}, err
	}
	return s.surface(id)
}

func (w *Workspace) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.surfaceAction(id, action, args)
}

// Catalog serves the effective web string catalog for the browser. It first
// re-runs i18n.Configure so an operator's edits under $TERVA_HOME/locales show
// up on reconnect — both the server-rendered strings (surface titles, settings
// labels via i18n.T) AND, via WebCatalog reading fresh, the client-owned
// strings returned here. A browser reload re-fetches this and re-fetches
// surfaces, so the whole panel reflects the edit without a daemon restart.
func (w *Workspace) Catalog(ctx context.Context, lang string) (ctrlproto.CatalogView, error) {
	_ = i18n.Configure(config.Language(), config.TervaHome())
	if lang == "" {
		lang = i18n.ActiveLang()
	}
	doc, err := i18n.WebCatalog(lang, config.TervaHome())
	if err != nil {
		return ctrlproto.CatalogView{}, err
	}
	return ctrlproto.CatalogView{Lang: lang, Singular: doc.Singular, Plural: doc.Plural}, nil
}
