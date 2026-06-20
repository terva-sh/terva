package modes

import (
	"context"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/tui"
)

// InteractiveConfig configures the interactive loop.
type InteractiveConfig struct {
	Terminal     tui.Terminal
	Theme        tui.Theme
	Model        string
	Provider     string
	AuthMethod   string // "apikey" | "oauth" — used to tag cost as (sub) in status bar
	BaseURL      string
	Reasoning    string
	SystemPrompt string
	Tools        core.Registry
	MaxSteps     int
	CWD          string

	// InlineImagesEnabled overrides terminal image rendering. nil means
	// auto-detect and render when supported; false disables; true uses
	// the detected protocol when available.
	InlineImagesEnabled *bool

	// AutoSwarmEnabled mirrors the persisted config flag at startup so
	// the /settings dialog can render the current state without
	// re-reading config.json on every open.
	AutoSwarmEnabled *bool

	// RecursiveFileSuggest mirrors the persisted recursive_file_suggest
	// flag at startup. When true the @-mention picker fuzzy-searches the
	// whole project tree instead of browsing one directory at a time.
	RecursiveFileSuggest *bool

	// RespectGitignore mirrors the persisted respect_gitignore flag at
	// startup. nil means the default (on); when false the @-mention
	// picker shows files matched by the project's root .gitignore.
	RespectGitignore *bool

	// ThemeName mirrors the persisted config theme value. Empty means auto.
	ThemeName string
	// ExtensionThemes returns themes bundled with loaded extensions.
	ExtensionThemes func() []tui.ThemeOption

	// AutoSwarmSystemAddendum is the system-prompt block that gets
	// appended/stripped when the user toggles auto-swarm at runtime.
	// Plumbed in from the cli so this package doesn't have to import
	// agent (cycle).
	AutoSwarmSystemAddendum string
	SettingsStore           SettingsStore

	// RebuildExtensionContext re-folds the extensions' static context
	// into the system prompt after one of them sent refresh_context
	// (protocol 3), returning the rebuilt prompt and whether it changed.
	// Plumbed in from the cli (which owns the Resolved + extension
	// source) to avoid a modes->agent import cycle; nil when extensions
	// aren't enabled. Interactive applies the result to the running
	// agent's System on the main goroutine.
	RebuildExtensionContext func() (string, bool)

	// Agent is optional. If nil, terva opens without credentials; the
	// user must /login before they can prompt.
	Agent *core.Agent

	InitialInput string

	// Auth is required. When the user runs /login, Interactive talks to
	// AuthManager to open a browser and wait for the callback.
	AuthManager *auth.Manager
	// BuildAgent is called after a successful login to (re)construct the
	// agent with the fresh credential. It returns the new agent and
	// the concrete provider/model in use.
	BuildAgent func() (*core.Agent, string, string, error)

	// SetKimiCLIFallbackDisabled controls whether terva may fall back to
	// the official Kimi Code CLI token when terva has no stored Kimi token.
	SetKimiCLIFallbackDisabled func(disabled bool) error

	// Migration wires /migrate to the zot→terva migration engine. // rename:keep
	// Optional: when nil, /migrate reports that the host didn't
	// enable it.
	Migration *MigrationHooks

	// BuildAgentFor rebuilds the agent with an explicit provider/model
	// override (used by the /model picker when switching providers).
	// If providerOverride is empty, the current provider is kept.
	BuildAgentFor func(providerOverride, modelOverride string) (*core.Agent, string, string, error)

	// BuildAgentForRescue rebuilds the agent for the rescue picker that
	// opens after a recoverable provider failure. Unlike BuildAgentFor,
	// this builder drops launch-time --api-key and --base-url overrides
	// because those are usually the reason rescue triggered. Re-resolves
	// credentials from env vars / auth.json / provider defaults so the
	// retry has a real chance of succeeding. Falls back to BuildAgentFor
	// when nil so embedders that don't wire it keep working.
	BuildAgentForRescue func(providerOverride, modelOverride string) (*core.Agent, string, string, error)

	// LoggedInProviders returns the list of provider names that
	// currently have credentials. Used by /model to filter the
	// picker to only show reachable models.
	LoggedInProviders func() []string

	// TervaHome is the root directory for sessions/, used by /sessions
	// and the update-check cache.
	TervaHome string

	// Version is the binary's current version (from main.version).
	// Used only for display; the update check itself is done outside
	// this package to avoid an import cycle.
	Version string

	// UpdateInfoChan is an optional channel that delivers the result
	// of the github-release update check. Interactive reads at most
	// one value, drops it if the check reported nothing, and otherwise
	// surfaces a yellow "update available" banner at the top of the
	// chat. Nil channel = no banner, no startup cost.
	UpdateInfoChan <-chan UpdateInfo

	// Sandbox is the shared sandbox pointer. Toggled by /jail and /unjail.
	Sandbox *tools.Sandbox

	// LoadSession swaps the current session for the one at path. The
	// callback returns the new agent message slice so the TUI can invalidate.
	LoadSession func(path string) error

	// NewSession closes the current session and starts a fresh one in
	// the same cwd: the agent keeps its provider/model/tools but its
	// transcript and running cost are reset, and a new session file is
	// opened (the old one stays on disk). providerName/model are the
	// live values so the new session's metadata is accurate. Returns an
	// error if no agent is running or the session can't be created.
	//
	// Optional: when nil, /new reports a clear error instead of no-oping.
	NewSession func(providerName, model string) error

	// ChangeCWD switches the running terva session's working directory
	// to path. The host closes the current session, rebuilds the
	// agent so tools / AGENTS.md / sandbox bind to the new cwd, and
	// opens a fresh session there. Returns an error if path doesn't
	// exist, isn't a directory, or the host can't rebuild the agent.
	//
	// Optional: not wired by every embedder. When nil the hidden /cd
	// command surfaces a clear error rather than no-oping.
	ChangeCWD func(path string) error

	// Trusted is the Workspace Trust verdict for the launch cwd. When
	// false the workspace is RESTRICTED: its project extensions, skills,
	// and context files were not loaded. Interactive surfaces a one-line
	// reminder at launch (only when GatedContentPresent) telling the user
	// they can run /trust. See docs/plans/workspace-trust.md.
	Trusted bool

	// GatedContentPresent reports whether the cwd actually ships project
	// content that trust would unlock (a .terva/extensions|skills etc).
	// The untrusted reminder fires only when this is true, so a plain
	// repo never sees a trust nag.
	GatedContentPresent bool

	// TrustWorkspace persists trust for the current cwd (parent=true also
	// trusts descendants), then ideally re-applies project content for
	// the session. Wired by the cli to TrustPath + an agent rebuild. nil
	// disables the /trust command (embedders/tests without the wiring).
	TrustWorkspace func(parent bool) error

	// UntrustWorkspace removes the current cwd from the trust store. nil
	// disables /untrust.
	UntrustWorkspace func() error

	// CurrentSessionPath returns the path of the live session file
	// on disk (the one every AppendMessage writes to). Used by
	// /session export so the exporter ships the exact bytes on
	// disk. Returns an empty string when --no-session is set or
	// no session is open.
	CurrentSessionPath func() string

	// FlushSession writes any in-memory agent messages to the
	// session file that haven't been persisted yet. Called by
	// /session export right before reading the file so the
	// exported bytes reflect the full current conversation, not
	// just the rows the agent happened to write synchronously.
	// The default WriteNewTranscript-at-exit strategy means most
	// of a running session lives only in memory until the tui
	// closes; without a flush hook, /session export writes a
	// file that only has the meta row.
	FlushSession func()

	// UserModelsPath is the absolute path to $TERVA_HOME/models.json, the
	// user-override catalog layer. The /model editor (Ctrl+E) reads and
	// writes it here; empty disables editing. Plumbed in from the cli so
	// this package doesn't import agent (cycle).
	UserModelsPath string

	// PersistModel is called whenever the user switches model or provider.
	// It should update config.json and (if there's an active session)
	// write a new meta row so resume picks up the same model.
	PersistModel func(providerName, model string)

	// RefreshCompatModels, if set, kicks a background /v1/models discovery
	// for a configured openai-compatible endpoint so a fresh login surfaces
	// all of the server's models in the picker without a restart.
	RefreshCompatModels func()

	OnAssistant  func(m provider.Message)
	OnToolResult func(id string, r core.ToolResult)

	// Extensions, if non-nil, lets users invoke extension-registered
	// slash commands. Commands declared by extensions are looked up
	// AFTER the built-in catalog so a built-in name always wins.
	Extensions *extensions.Manager

	// The /extensions dialog's host hooks, plumbed from the cli so this
	// package never imports agent. ListExtensions returns the installed
	// set + state; SetExtensionGlobalEnabled flips the manifest `enabled`
	// flag; SetExtensionProjectEnabled adds/removes the name in this
	// project's disable_extensions; ApplyExtensionChange surgically starts
	// or stops just that one extension to match the new config (leaving
	// every other extension running). All nil disables the /extensions
	// command.
	ListExtensions             func() []ExtInfo
	SetExtensionGlobalEnabled  func(name string, on bool) error
	SetExtensionProjectEnabled func(name string, on bool) error
	ApplyExtensionChange       func(name string)

	// Swarm, if non-nil, enables the /swarm slash command and the
	// dashboard dialog. The cli constructs the Swarm once per
	// interactive run and tears it down on exit. nil disables the
	// feature entirely (used by embedders / tests that don't want
	// subprocesses).
	Swarm *swarm.Swarm

	// SetApprovalMode switches the approval mode live: it swaps
	// enforcement on the gate (ConfirmGate, below) and returns the
	// tool registry rebuilt for the new mode, which the TUI installs
	// on the running agent. nil disables the /settings approval-mode
	// picker (embedders / tests without a rebuild path).
	SetApprovalMode func(core.ApprovalMode) core.Registry

	// SkillSnapshot, if non-nil, returns the current list of
	// discovered SKILL.md files. Re-invoked each time /skills opens
	// so the picker reflects edits made during the session.
	SkillSnapshot func() []*skills.Skill

	// ChangelogChan, if non-nil, delivers release-notes for the
	// current binary version once at startup. Interactive opens a
	// dismissible overlay when the channel produces a non-empty
	// body. Receiver fires at most once.
	ChangelogChan <-chan ChangelogPayload

	// OnChangelogDismiss, if non-nil, is called once the user
	// closes the changelog overlay. The cli wires this to a
	// MarkChangelogShown call so the same version doesn't show
	// again on the next launch.
	OnChangelogDismiss func()

	// NoYolo is true when --no-yolo was passed. Interactive opens
	// a confirmation dialog before every tool call and blocks the
	// tool until the user picks yes / always-this-tool /
	// always-all / no. When false (default), tools run freely.
	NoYolo bool

	// ConfirmGate is the session-scoped gate wrapping this
	// interactive's Confirmer. When non-nil, /yolo can call
	// AllowAll() on it to disable confirmation for the rest of the
	// session. When nil (yolo mode), /yolo reports that there's
	// nothing to disable.
	ConfirmGate *core.ConfirmGate
}

// ChangelogPayload mirrors agent.ChangelogInfo without the import
// cycle. The cli builds one from the http response, the tui opens
// the overlay when one arrives.
type ChangelogPayload struct {
	Version string
	Body    string
	URL     string
}

// SettingsStore persists user-toggleable settings surfaced by /settings.
type SettingsStore interface {
	SetInlineImages(enabled bool) error
	SetAutoSwarm(enabled bool) error
	SetRecursiveFileSuggest(enabled bool) error
	SetRespectGitignore(enabled bool) error
	SetReasoning(level string) error
	SetTheme(name string) error
}

type Interactive struct {
	cfg  InteractiveConfig
	view *tui.View
	ed   *tui.Editor
	// rend's internal frame/viewport state is not thread-safe. It is
	// main-loop-only state, like ed and the dialogs: every rend call
	// after Run's startup happens on the Run goroutine (SIGWINCH is
	// marshalled there via the resize channel; off-main goroutines go
	// through runOnMain).
	rend *tui.Renderer

	mu sync.Mutex
	// turns owns the turn lifecycle: the agent pointer, the busy
	// flag, the active turn's cancel, the unified prompt queue, the
	// streaming typewriter, and the pacer — behind its own leaf lock
	// so submit-or-queue and turn-end transitions are atomic. See
	// turn_engine.go for the lock discipline (i.mu → engine.mu is
	// the one allowed nesting; never the reverse).
	turns        *turnEngine
	toolCalls    map[string]*tui.ToolCallView
	toolOrder    []string
	statusErr    string
	statusOK     string
	liveBlock    []string // live streaming/tool progress rendered outside scrollback
	helpBlock    []string // rendered above the chat when /help was typed
	cumUsage     provider.Usage
	lastCtxInput int // input_tokens of the most recent turn — approximates current context size
	dirty        chan struct{}
	// resize coalesces SIGWINCH callbacks onto the main loop. The
	// signal handler runs on its own goroutine; letting it call
	// redraw() directly raced the main loop's unlocked editor and
	// renderer state (caught by the -race harness), so it only pokes
	// this buffered-1 channel and the main loop does the real work.
	resize chan struct{}
	// actions marshals work onto the main Run() goroutine. Off-main
	// goroutines (spawned extension command invocations, the auth-code
	// submit goroutine, host hooks) enqueue a closure here instead of
	// touching main-loop-only state directly — tui.Editor and the
	// dialogs have no internal locking and are otherwise only mutated
	// from the key loop. The main select loop drains and runs each
	// func on the main goroutine. Buffered so a burst of enqueues from
	// a background goroutine doesn't block it; runOnMain falls back to
	// running inline only when the channel is full (best-effort).
	actions          chan func()
	scrollOffset     int // rows from the bottom; 0 = pinned to latest
	prevScrollOffset int // last value redraw snapped against; tracks intent

	// prevChatLen and prevChatCols track the chat buffer's size at the
	// last redraw so that when content grows below the user's viewport
	// while they're scrolled up reading history, we can bump
	// scrollOffset by exactly the growth and keep the visible content
	// pinned. Without this, every streamed line shifts the visible
	// window down through the buffer (because scrollOffset is measured
	// from the bottom) and the user's reading position drifts upward
	// and off the top.
	prevChatLen     int
	prevChatCols    int
	prevChatRows    int
	prevOverlayOpen bool

	// chatCache stores the built transcript/status-note rows for idle
	// frames. Editor typing changes only the bottom input region, so
	// reusing this cache avoids copying/walking/reassembling a long
	// session on every keypress.
	chatCache      []string
	chatCacheKey   chatCacheKey
	chatCacheValid bool

	// runCtx is the top-level context passed to Run(). Follow-up turns
	// drained from the queue are started against this context so they
	// survive past the ctx of the key event that enqueued them.
	runCtx context.Context

	// pendingPostCompactNote is a status_ok message to surface after
	// a successful auto-compact pass triggered by a 413 or by the
	// pre-turn fraction guard. Cleared by runCompact once shown.
	pendingPostCompactNote string

	// autoCompacting is true while a model-triggered compaction is in
	// flight. Surfaced in the status bar so the user can tell a
	// condense pass from a regular assistant turn.
	autoCompacting bool

	// updateInfo is the result of the async update check. Zero value
	// while the check hasn't completed or nothing is available.
	updateInfo UpdateInfo

	dialog            *loginDialog
	modelDialog       *modelDialog
	modelEditDialog   *modelEditDialog
	extensionsDialog  *extensionsDialog
	rescueDialog      *rescueDialog
	sessionDialog     *sessionDialog
	swarmDialog       *swarmDialog
	jumpDialog        *jumpDialog
	btwDialog         *btwDialog
	skillsDialog      *skillsDialog
	changelogDialog   *changelogDialog
	permissionsDialog *permissionsDialog
	confirmDialog     *confirmDialog
	questionDialog    *questionDialog
	logoutDialog      *logoutDialog
	connectDialog     *connectDialog
	settingsDialog    *settingsDialog
	chatBridge        *chat.Bridge
	sessionOpsDialog  *sessionOpsDialog
	sessionTreeDialog *sessionTreeDialog
	extPanel          *extPanelDialog
	migrateDialog     *migrateDialog

	// overlays is the priority-ordered modal registry: key routing,
	// rendering, cursor ownership, and tick animation for every
	// dialog above all derive from this one slice. See
	// overlay_registry.go.
	overlays []overlayEntry

	// keymap is the global key-binding table (chords that work
	// outside dialogs and popups). See keymap.go.
	keymap []globalBinding

	// swarmWatch tracks auto-swarm sub-agents the main agent spawned
	// via swarm_spawn. Each entry holds the agent + the task text;
	// a per-entry goroutine waits on the agent's terminal state. When
	// every tracked entry has finished, the watcher flushes a single
	// summary turn into the main chat (queued if the main agent is
	// busy, run immediately if idle).
	swarmWatchMu sync.Mutex
	swarmWatch   []*swarmWatchEntry

	// pendingFork is true when the user ran /session fork: the next
	// jump-picker selection should branch off that message instead
	// of scrolling. Flag resets after the action fires or the dialog
	// is dismissed, so repeated /jump calls don't turn into forks.
	pendingFork bool
	suggest     *slashSuggester
	fileSuggest *fileSuggester
	spin        *spinner

	// parkedTurn is the 1-based turn number the viewport is currently
	// scrolled to by /jump. 0 = not parked, showing the tail as usual.
	// Rendered as a muted footer at the bottom of the chat so users
	// don't forget they're looking at history.
	parkedTurn  int
	parkedTotal int

	// inputHistoryIndex is -1 when not browsing history. When the
	// editor is empty, Left/Right can walk previous user prompts for
	// quick manual testing without stealing normal cursor movement in
	// non-empty input.
	inputHistoryIndex int

	// lastCtrlC is when the user last pressed ctrl+c. The first press
	// clears the editor / cancels a turn / shows a hint; a second press
	// within ctrlCExitWindow exits. Mirrors the python-repl convention.
	lastCtrlC time.Time

	// welcomeStart is when the interactive run began. The welcome
	// banner shows the binary version for welcomeVersionDuration
	// after this point and reverts to plain text after.
	welcomeStart time.Time
	// welcomeGreeting is the rotating tagline (Theme.Greeting), picked
	// once at startup so it stays stable across re-renders.
	welcomeGreeting string

	// extNotes are one-shot styled lines pushed by extensions via
	// Notify / Display. They live above the editor (just below the
	// transcript) until cleared by /clear or another reset.
	extNotes []string

	// shellBlock holds the rendered terminal-log lines of the most
	// recent !command shell escape. It lives below the transcript
	// (under extNotes) until the user sends their next prompt or runs
	// /clear. shellRunning is true while a !command is executing; it
	// shares the turn engine's busy slot so esc cancels it and no turn or
	// other shell escape can start while one is in flight.
	shellBlock   []string
	shellRunning bool

	// sessionLoading is true while a /sessions selection is being read
	// on a background goroutine. Keeping this off the input goroutine
	// lets ctrl+c/exit remain responsive for very large JSONL sessions.
	sessionLoading bool

	// pendingRescuePrompt / pendingRescueImages stash the prompt and
	// images that should be re-run after the user picks a model in
	// the rescue dialog. Cleared once applyRescueSelection consumes
	// them (or when the dialog is dismissed via esc).
	pendingRescuePrompt string
	pendingRescueImages []provider.ImageBlock
}

// welcomeVersionDuration is how long the welcome banner shows the
// version suffix before reverting to the plain headline. 1.5s is
// enough to read at a glance and keeps the splash short.
const welcomeVersionDuration = 1500 * time.Millisecond

// initialResumeTailLimit caps how many messages from a freshly-resumed
// transcript we render on the first paint. The full transcript is
// still in memory; older messages are rendered (and their cached
// lines kept for the lifetime of the View) as soon as the user
// scrolls past the rendered tail. Picked to comfortably fill the
// largest realistic terminal viewport while keeping first paint
// snappy on multi-thousand-message sessions where markdown / syntax
// highlighting dominates the redraw cost.
const initialResumeTailLimit = 80

// resumeTailExpandStep is how many additional messages the tail
// limit grows by each time the user scrolls past the currently
// rendered top. Pre-rendering this many messages on a single tick
// keeps scroll-up smooth without falling back to a one-by-one
// reveal that would feel jerky.
const resumeTailExpandStep = 80

// NewInteractive constructs an Interactive from cfg.
func NewInteractive(cfg InteractiveConfig) *Interactive {
	renderer := tui.NewRenderer(cfg.Terminal)
	renderer.SetTheme(cfg.Theme)
	i := &Interactive{
		cfg: cfg,
		view: &tui.View{
			Theme:      cfg.Theme,
			ImageProto: effectiveImageProtocol(cfg.InlineImagesEnabled),
		},
		// Prompt is the standard half-block accent bar used by chat
		// speaker labels too, so the input gutter matches the rest
		// of the UI.
		ed:                tui.NewEditor(cfg.Theme.AccentBar(cfg.Theme.Accent)),
		rend:              renderer,
		toolCalls:         map[string]*tui.ToolCallView{},
		turns:             newTurnEngine(),
		dirty:             make(chan struct{}, 8),
		resize:            make(chan struct{}, 1),
		actions:           make(chan func(), 64),
		dialog:            newLoginDialog(),
		modelDialog:       newModelDialog(),
		modelEditDialog:   newModelEditDialog(),
		extensionsDialog:  newExtensionsDialog(),
		rescueDialog:      newRescueDialog(),
		sessionDialog:     newSessionDialog(),
		swarmDialog:       newSwarmDialog(),
		jumpDialog:        newJumpDialog(),
		btwDialog:         newBtwDialog(),
		skillsDialog:      newSkillsDialog(),
		changelogDialog:   newChangelogDialog(),
		permissionsDialog: newPermissionsDialog(),
		confirmDialog:     newConfirmDialog(),
		questionDialog:    newQuestionDialog(),
		logoutDialog:      newLogoutDialog(),
		connectDialog:     newConnectDialog(),
		settingsDialog:    newSettingsDialog(),
		sessionOpsDialog:  newSessionOpsDialog(),
		sessionTreeDialog: newSessionTreeDialog(),
		extPanel:          newExtPanelDialog(),
		migrateDialog:     newMigrateDialog(),
		suggest:           newSlashSuggester(),
		fileSuggest:       newFileSuggester(),
		spin:              newSpinner(cfg.Theme),
		inputHistoryIndex: -1,
	}
	i.overlays = i.buildOverlays()
	i.keymap = i.buildGlobalKeymap()
	i.fileSuggest.SetRecursive(cfg.RecursiveFileSuggest != nil && *cfg.RecursiveFileSuggest)
	i.fileSuggest.SetRespectGitignore(cfg.RespectGitignore == nil || *cfg.RespectGitignore)
	if cfg.Agent != nil {
		i.turns.SetAgent(cfg.Agent)
		i.view.Messages = cfg.Agent.Messages()
		i.cumUsage = cfg.Agent.Cost()
		// Rehydrate the "context used" gauge from the last persisted
		// turn. Without this the status bar reads 0.0% after a resume
		// until the next turn lands a usage event.
		if last := cfg.Agent.LastTurnUsage(); last.InputTokens > 0 || last.CacheReadTokens > 0 || last.CacheWriteTokens > 0 {
			i.lastCtxInput = last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
		}
		// Cap the first paint at the tail of the transcript so
		// resuming a multi-thousand-message session doesn't block
		// on rendering every prior turn before showing anything.
		if len(i.view.Messages) > initialResumeTailLimit {
			i.view.TailLimit = initialResumeTailLimit
		}
	}
	return i
}

// Run blocks until the user quits.
func (i *Interactive) Run(ctx context.Context) error {
	i.runCtx = ctx
	term := i.cfg.Terminal
	restore, err := term.EnterRaw()
	if err != nil {
		return err
	}
	defer restore()
	defer func() {
		if i.chatBridge != nil {
			i.chatBridge.Stop()
		}
	}()

	// Enabling mouse reporting steals click-drag selection from the
	// host terminal (VS Code, Ghostty, iTerm). The user prefers native
	// selection over the wheel-speed boost, so we no longer turn it
	// on automatically. Wheel events fall through to the terminal's
	// own scrollback handler.
	// Keep terva on the terminal's main screen. We intentionally do not
	// enter the alternate-screen buffer (CSI ?1049h). The renderer emits
	// chat as normal terminal flow/scrollback and redraws only the live
	// input/status block on normal typing.
	_, _ = term.Write([]byte(tui.SeqBracketedPasteOn + tui.SeqEnhancedKeyboardOn + tui.SeqResetScrollRegion + tui.SeqDeleteKittyImages + tui.SeqClearScreenNoHome + tui.SeqClearScrollback + tui.MoveTo(1, 1)))
	defer term.Write([]byte(tui.SeqResetScrollRegion + tui.SeqDeleteKittyImages + tui.SeqEnhancedKeyboardOff + tui.SeqBracketedPasteOff + tui.SeqShowCursor))
	// Erase the live status/input band on exit so the returning shell
	// prompt lands on a clean line right after the conversation instead of
	// underneath a stale frame. Runs before the resets above (defers are
	// LIFO) while the renderer's cursor/viewport state still matches what's
	// on screen. The chat transcript stays in scrollback.
	if i.rend != nil {
		defer i.rend.TeardownLog()
	}

	// Streaming pacer: drains buffered text deltas at a steady rate
	// so typewriter feel is identical across providers regardless of
	// upstream chunk size. Starts here so it lives for the whole
	// session and exits with ctx.
	go i.turns.runPacer(ctx, i.invalidate)

	cols, rows := term.Size()
	i.rend.Resize(cols, rows)
	term.OnResize(func() {
		// This callback runs on the signal-handler goroutine. The
		// editor and renderer it would need are main-loop-only state,
		// so hand the event to the main loop instead of doing the
		// work here. The buffered-1 send coalesces SIGWINCH storms
		// (drag-resize) into one repaint per loop pass; the main loop
		// redraws immediately on receipt — a window resize is a
		// discrete user action where the throttled invalidate path's
		// delay would read as brokenness.
		select {
		case i.resize <- struct{}{}:
		default:
		}
	})

	if i.cfg.InitialInput != "" {
		i.ed.SetValue(i.cfg.InitialInput)
	}

	// Stamp the welcome time and schedule a one-shot redraw at the
	// expiry so the version suffix disappears on its own even if the
	// user hasn't typed anything yet.
	i.welcomeStart = time.Now()
	i.welcomeGreeting = i.cfg.Theme.Greeting()
	time.AfterFunc(welcomeVersionDuration, i.invalidate)

	// If the agent was constructed with a pre-loaded transcript
	// (--continue, --resume, --session) pin the viewport at the
	// bottom so the most recent reply (and any prompt the user just
	// typed) is fully visible. Earlier behaviour parked the view at
	// the last user turn, which could leave the latest message clipped
	// off the bottom of the page on long sessions.
	if ag := i.turns.Agent(); ag != nil {
		if msgs := ag.Messages(); len(msgs) > 0 {
			i.scrollToBottom()
		}
	}

	// No credential at startup? Auto-open the login dialog, and mark
	// the status line. The user can Esc out of the dialog if they
	// want to dismiss it (e.g. to check /help or /exit first).
	if !i.turns.HasAgent() {
		i.statusErr = "not logged in. pick a login method below or press esc to dismiss."
		i.dialog.Open(i.cfg.TervaHome)
	} else if !i.cfg.Trusted && i.cfg.GatedContentPresent {
		// Workspace Trust reminder: the cwd ships project extensions/
		// skills/context that were NOT loaded because the directory is
		// untrusted. Tell the user once, on the status line, how to opt
		// in. No prompt/dialog (inform-don't-prompt, decision #2).
		i.statusOK = "restricted workspace: project extensions/skills/context not loaded — /trust to load them"
	}

	// Input goroutine. Buffered generously so a drag-drop that the
	// terminal delivers as a burst of single-character key events
	// (no bracketed paste) can be drained in one main-loop pass
	// instead of triggering a redraw per character.
	keys := make(chan tui.Key, 256)
	go func() {
		reader := tui.NewReaderWithPeek(term.ReadByte, term.PeekByteTimeout)
		for {
			k, err := reader.Read()
			if err != nil {
				return
			}
			keys <- k
		}
	}()

	// Subscribe to auth events.
	var authEvents <-chan auth.Event
	if i.cfg.AuthManager != nil {
		authEvents = i.cfg.AuthManager.Events()
	}

	// Animation ticker: drives spinner and dialog-related redraws when
	// nothing else changed. 120ms is slow enough that highlighting a huge
	// transcript doesn't spin the cpu.
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()

	// Redraw throttle: coalesce bursts of invalidate() calls so we paint
	// at most once every redrawMinInterval. Huge tool-result dumps can
	// fire hundreds of invalidations while the user is typing; without
	// this, the input goroutine never gets CPU and keystrokes lag.
	const redrawMinInterval = 16 * time.Millisecond
	var lastRedraw time.Time
	var pendingRedraw bool
	var pendingTimer *time.Timer

	drainPending := func() {
		if pendingTimer != nil {
			pendingTimer.Stop()
			pendingTimer = nil
		}
		if pendingRedraw {
			pendingRedraw = false
			lastRedraw = time.Now()
			i.redraw()
		}
	}

	requestRedraw := func() {
		since := time.Since(lastRedraw)
		if since >= redrawMinInterval {
			// Redrawing right now subsumes any pending redraw, so clear
			// the throttle state. Without this, a pending flag stays
			// stuck at true and subsequent invalidate() calls within
			// redrawMinInterval get dropped — which is exactly how the
			// final "turn finished" frame went missing until the user
			// nudged the ui by typing or scrolling.
			if pendingTimer != nil {
				pendingTimer.Stop()
			}
			pendingRedraw = false
			lastRedraw = time.Now()
			i.redraw()
			return
		}
		if pendingRedraw {
			return // already scheduled
		}
		pendingRedraw = true
		wait := redrawMinInterval - since
		if pendingTimer == nil {
			pendingTimer = time.AfterFunc(wait, func() {
				// Poke the dirty channel so the main loop wakes and
				// drains the pending redraw on its own goroutine. We
				// can't call drainPending here directly — it touches
				// closure state shared with the main loop.
				i.invalidate()
			})
		} else {
			pendingTimer.Reset(wait)
		}
	}

	i.invalidate()

	updates := i.cfg.UpdateInfoChan  // nil-safe; nil channel blocks forever in select
	changelog := i.cfg.ChangelogChan // single-shot, see case below

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case k := <-keys:
			if done := i.handleKey(ctx, k); done {
				return nil
			}
			// Drain any keystrokes that arrived during this iteration.
			// VS Code (and other terminals that don't bracket drops as
			// paste) deliver a path one rune at a time — without this
			// loop the editor would render between every rune and a
			// long path on a heavy transcript would visibly type in.
		drain:
			for {
				select {
				case k2 := <-keys:
					if done := i.handleKey(ctx, k2); done {
						return nil
					}
				default:
					break drain
				}
			}
			i.invalidate()
		case <-i.resize:
			// SIGWINCH, marshalled from the signal goroutine. Resize
			// the renderer and repaint right away, bypassing the
			// redraw throttle: a window resize is a discrete user
			// action where a stale frame reads as brokenness.
			c, r := term.Size()
			i.rend.Resize(c, r)
			i.redraw()
		case ev := <-authEvents:
			i.handleAuthEvent(ev)
			i.invalidate()
		case fn := <-i.actions:
			// Work marshalled onto the main goroutine by off-main
			// callers (see runOnMain). Drain a burst so a flurry of
			// editor inserts / dialog updates all land before the
			// next redraw rather than one per loop turn.
			fn()
		drainActions:
			for {
				select {
				case fn2 := <-i.actions:
					fn2()
				default:
					break drainActions
				}
			}
			i.invalidate()
		case info, ok := <-updates:
			if ok && info.Available {
				i.mu.Lock()
				i.updateInfo = info
				i.mu.Unlock()
				i.invalidate()
			}
			updates = nil // single-shot; subsequent iterations skip this case
		case cl, ok := <-changelog:
			if ok && cl.Body != "" {
				i.changelogDialog.Open(cl.Version, cl.URL, cl.Body)
				i.invalidate()
			}
			changelog = nil // single-shot
		case <-i.dirty:
			requestRedraw()
		case <-tick.C:
			// Always drain a pending redraw on the tick. This is the
			// safety net that catches the case where the dirty channel
			// was saturated when the final "turn finished" invalidate
			// fired, or where the throttle scheduled a deferred redraw
			// and the AfterFunc-driven invalidate got dropped on a
			// full channel.
			drainPending()
			// Only force a periodic redraw when something is actually
			// animating (the main spinner during a busy turn, or the
			// btw side-chat spinner while it's awaiting a response).
			// Static pickers (model, session, jump, etc.) don't need
			// the tick and firing it cancels the terminal's cursor
			// blink inside dialogs that host their own editor (btw),
			// because each frame re-emits hide-cursor + show-cursor.
			//
			// Which overlays animate (btw/migrate loading spinners,
			// the live swarm dashboard) is declared per-entry in the
			// overlay registry via the animating hook.
			if i.turns.Busy() || i.overlayAnimating() {
				requestRedraw()
			}
		}
	}
}

func (i *Interactive) invalidate() {
	select {
	case i.dirty <- struct{}{}:
	default:
	}
}

// runOnMain queues fn to execute on the main Run() goroutine. Use it
// from any goroutine other than the key loop to mutate main-loop-only
// state (the editor, dialogs, etc.), which have no internal locking.
// If the action buffer is somehow saturated we fall back to running
// fn inline so the work isn't silently dropped; that path is best-
// effort and only reachable under extreme back-pressure, where a
// rare unsynchronised mutation is preferable to losing the user's
// inserted text entirely.
func (i *Interactive) runOnMain(fn func()) {
	if fn == nil {
		return
	}
	select {
	case i.actions <- fn:
	default:
		fn()
		i.invalidate()
	}
}

func (i *Interactive) runSlash(ctx context.Context, cmd string) (done bool) {
	parts := strings.Fields(cmd)
	if spec, ok := lookupSlash(parts[0]); ok {
		return spec.run(i, ctx, parts, cmd)
	}
	// Last-resort fallback: try the extension manager. Built-in
	// commands always win; this branch only fires for slash commands
	// the extension manager registered. Same routing as the editor's
	// submit-handler dispatch path so the autocomplete "enter on
	// highlighted suggestion" flow also works.
	extName := strings.TrimPrefix(parts[0], "/")
	if i.cfg.Extensions != nil && i.cfg.Extensions.HasCommand(extName) {
		rest := ""
		if len(parts) > 1 {
			rest = strings.Join(parts[1:], " ")
		}
		go i.invokeExtensionCommand(ctx, extName, rest)
		return false
	}
	i.mu.Lock()
	i.statusErr = "unknown command: " + parts[0]
	i.mu.Unlock()
	return false
}
