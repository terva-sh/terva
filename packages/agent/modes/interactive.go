package modes

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/relaunch"
	"terva.sh/terva/packages/tui"
)

// InteractiveConfig configures the interactive loop.
type InteractiveConfig struct {
	Terminal   tui.Terminal
	Theme      tui.Theme
	Model      string
	Provider   string
	AuthMethod string // "apikey" | "oauth" — used to tag cost as (sub) in status bar
	Reasoning  string
	CWD        string

	// InlineImagesEnabled overrides terminal image rendering. nil means
	// auto-detect and render when supported; false disables; true uses
	// the detected protocol when available.
	InlineImagesEnabled *bool

	// AutoSwarmEnabled mirrors the persisted config flag at startup so
	// the /settings dialog can render the current state without
	// re-reading config.json on every open.
	AutoSwarmEnabled *bool

	// RecursiveFileSuggest mirrors the persisted recursive_file_suggest
	// flag at startup. nil or true (the default — matching the web
	// composer) fuzzy-searches the whole project tree; false opts back
	// into one-directory-at-a-time browsing.
	RecursiveFileSuggest *bool

	// RespectGitignore mirrors the persisted respect_gitignore flag at
	// startup. nil means the default (on); when false the @-mention
	// picker shows files matched by the project's root .gitignore.
	RespectGitignore *bool

	// RemoteFiles, when set, lists workspace files from the daemon (the
	// files.list verb) for the @-mention picker instead of reading local
	// disk — the attach entry point wires it when the server hello
	// advertises the files-list feature. Runs on a background goroutine.
	RemoteFiles widgets.RemoteLister

	// StatusLineRows mirrors the persisted status_line.rows layout: the
	// user's segment-ID rows for the status bar. nil means the per-mode
	// defaults. User-config only (never project-supplied).
	StatusLineRows [][]string

	// StatusScripts are the user's status_line.scripts, keyed by
	// (lowercased) segment name. Code execution from config: the cli
	// only populates this from the trusted user layer — same rule as
	// Hooks.
	StatusScripts map[string]StatusScript

	// ThemeName mirrors the persisted config theme value. Empty means auto.
	ThemeName string

	// PersonaName is the agent's persona name shown in the welcome banner
	// (e.g. "Mieli"). PersonaPhonetic is its pronunciation hint, shown after
	// the name once the transient version suffix drops (e.g. "MYEH-lee");
	// empty for a custom persona whose pronunciation we can't guess. Both are
	// plumbed in from the cli so this package doesn't import agent (cycle).
	PersonaName     string
	PersonaPhonetic string
	// PersonaEmoji and PersonaAccent (a #RRGGBB) are the persona's display
	// metadata; when set they lead and tint the welcome banner. Empty falls
	// back to the theme's defaults, so a non-persona session is unchanged.
	PersonaEmoji  string
	PersonaAccent string
	// Experience is the meta-mode ("", "chat", "play"). When non-empty the TUI
	// suppresses coding chrome (the cwd path + jail/approval tags in the status
	// bar) and uses non-coding spinner/greeting flavor.
	Experience string
	// ExtensionThemes returns themes bundled with loaded extensions.
	ExtensionThemes func() []tui.ThemeOption

	SettingsStore SettingsStore

	// Ready reports whether the bound session can accept prompts at
	// startup. The TUI keeps its own copy from here on: /login sets it,
	// /logout of the current provider clears it, a session switch sets it.
	//
	// The host declares it rather than the TUI inferring it. A session id
	// resolving is not the signal — a credential-less boot has one before
	// /login, and a replay carrier binds a CarrierSession that can never
	// prompt at all. cli_ctrlproto sets it from whether a credential resolved.
	Ready bool

	// Carrier routes the TUI's control operations through the in-process
	// ctrlproto WorkspaceService (docs/proposals/tui-on-ctrlproto.md).
	// CarrierSession is the resolved session id the TUI operates on. Every
	// shipping entry point sets it (cli_ctrlproto.go, replay_mode.go). The
	// nil-Carrier branches that drove Agent directly are GONE: without a Carrier
	// the control surfaces degrade (no approval picker, "unavailable"), they
	// never reach for a local gate or a *core.Agent. Do not add such a fallback
	// back — see the no-new-seam rule in carrier.go.
	Carrier        Carrier
	CarrierSession string
	// CarrierTasks enables /swarm (the tasks surface). The entry point gates it
	// on HasBaseWorkspaceTools — withheld from immersive/no-tools sessions so
	// the dashboard can't re-inject the coding skin there.
	CarrierTasks bool
	// CarrierRemote marks the carrier as a network client of another process
	// (terva attach) rather than the in-process workspace. Remote-only
	// adaptations key on it explicitly — e.g. adopting the settings surface's
	// reasoning value for the status bar, which in-process would stomp a
	// --reasoning flag override the surface can't see.
	CarrierRemote bool
	// CarrierLogin finalizes a successful in-TUI login. It refreshes the workspace's
	// credential/defaults, then ensures a current session: creating the first
	// one on a credential-less boot (current == ""), or rebuilding the live
	// session's provider client on a re-login. Returns the session the TUI
	// should be bound to. Only an in-process carrier host sets it — its
	// presence is also what marks the carrier as login-capable (a remote or
	// replay carrier leaves it nil: the daemon owns credentials there).
	CarrierLogin func(current string) (ctrlproto.SessionInfo, error)

	InitialInput string

	// OpenSessionsOnBoot opens the /sessions picker over the freshly booted
	// session (--resume, terva attach --resume) — the same auto-open mechanic
	// as the credential-less login dialog. Esc falls through to the session
	// the boot already bound, i.e. exactly what the command would have done
	// without the flag: a fresh session for terva, the daemon's current
	// binding for attach. On a credential-less boot the login dialog opens
	// first and the picker follows the first successful login.
	OpenSessionsOnBoot bool

	// GenerateSessionTitle runs on-demand title generation for the session at
	// path and returns the persisted title — the picker's `g` binding. Blocks
	// on a model call; the TUI invokes it off the main loop. Left nil when the
	// host can't serve it (a pre-generate-title daemon on attach), and the
	// binding reports that instead of firing a method that can't land.
	GenerateSessionTitle func(path string) (string, error)

	// There is no AuthManager here any more, and nothing may put one back.
	//
	// The TUI used to hold the credential state machine and drive it directly —
	// which meant it also had to know that openai-compatible needs four fields,
	// that bedrock is not a form, and how to clear the openai/openai-codex split.
	// The web panel could not import any of that, so it knew the same things
	// separately, and the two had already drifted. /login now goes through the
	// carrier's ctrlproto.AuthController, exactly as the panel's does. A feature
	// that needs the manager adds a verb; it does not reach back through here.

	// CarrierLocal says this TUI is running on the SAME MACHINE as the workspace
	// it drives — true for the in-process carrier, false for `terva attach`.
	//
	// It decides whether a login may use the loopback flows: an OAuth callback
	// server binds a port on the DAEMON, so it is reachable exactly when the
	// browser is here too. Getting it wrong costs nothing worse than a browser
	// that opens on the wrong machine and a paste-back form that still works.
	CarrierLocal bool

	// LoggedInProviders returns the list of provider names that
	// currently have credentials. Used by /model to filter the
	// picker to only show reachable models.
	LoggedInProviders func() []string

	// FavoriteModels returns the user's favorited model keys ("provider/id"),
	// pinned and starred at the top of the /model picker. Nil-safe.
	FavoriteModels func() []string

	// SetFavoriteModel persists a favorite toggle for a "provider/id" key.
	SetFavoriteModel func(key string, on bool) error

	// TervaHome is the root directory for sessions/, used by /sessions
	// and the update-check cache.
	TervaHome string

	// Version is the binary's current version (from main.version).
	// Used only for display; the update check itself is done outside
	// this package to avoid an import cycle.
	Version string

	// BootNotice, when set, renders as a one-shot note above the editor on
	// the session's first frames — e.g. "restarted — was vX, now vY" after
	// a self-restart resume. Cleared on the next prompt, like ext notes.
	BootNotice string

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

	// RenameSessionFile persists a session rename from the /sessions picker.
	// Optional: when nil the picker writes the title straight to the session
	// file (core.RenameSession). The ctrlproto entry point routes it through
	// the service so a live session's title stays in sync everywhere.
	RenameSessionFile func(path, title string) error

	// ListSessions supplies the /sessions picker's rows. Optional: when nil
	// the picker scans the session store itself (core.DescribeSessions). The
	// ctrlproto entry point routes it through the service's session group,
	// which overlays live state the file's meta line can lag behind.
	ListSessions func() []core.SessionSummary

	// The session lifecycle verbs behind the /sessions picker. Nil means this
	// frontend does not serve them — a replay carrier has no directory to move
	// anything in — and the picker then does not offer the keys at all rather
	// than offering keys that report "unavailable" after the fact.
	//
	// ArchiveSession and DeleteSession take a session FILE path (what the picker
	// rows carry); RestoreArchivedSession takes an archived session's id, which
	// is the only handle an archived transcript has.
	ArchiveSession         func(path string) error
	DeleteSession          func(path string) error
	ListArchivedSessions   func() []core.ArchivedSession
	RestoreArchivedSession func(id string) error

	// NewSession closes the current session and starts a fresh one in
	// the same cwd: the agent keeps its provider/model/tools but its
	// transcript and running cost are reset, and a new session file is
	// opened (the old one stays on disk). providerName/model are the
	// live values so the new session's metadata is accurate. Returns an
	// error if no agent is running or the session can't be created.
	//
	// Optional: when nil, /new reports a clear error instead of no-oping.
	NewSession func(providerName, model string) error

	// (ChangeCWD switched the running session's working directory, rebuilding
	// the agent against the new cwd. Only the direct driver ever supplied it;
	// a carrier session is PINNED to its directory — sessions, trust,
	// extensions and the permission policy all resolve from it — so the hidden
	// /cd now says so unconditionally, which is what it already did.)

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

	// JailNotice is the launch-time message about the sandbox, or "" when
	// there is nothing to say. Non-empty when a SAVED rule took the jail down
	// for this directory — a decision the user made once and would otherwise
	// only learn about from the absence of a "jailed" badge, which is no
	// signal at all. Set by the host from build.JailNoticeFor.
	JailNotice string

	// TrustWorkspace persists trust for the current cwd (parent=true also
	// trusts descendants), then ideally re-applies project content for
	// the session. Wired by the cli to TrustPath + an agent rebuild. nil
	// disables the /trust command (embedders/tests without the wiring).
	TrustWorkspace func(parent bool) error

	// UntrustWorkspace removes the current cwd from the trust store. nil
	// disables /untrust.
	UntrustWorkspace func() error

	// TrustAppliesLive reports that TrustWorkspace/UntrustWorkspace do the whole
	// job themselves — reload the extensions, rebuild the tools, re-render the
	// system prompt — so nothing further is needed for the change to take hold.
	//
	// It exists because /trust used to lie. The host hook was assumed to only
	// PERSIST, with the live half done here by re-cd-ing through a ChangeCWD
	// hook; a host without one therefore got "restart terva to load its project
	// extensions/skills/context". The ctrlproto hosts (the classic TUI and
	// terva attach) never had that hook and never needed it — their Trust verb
	// re-applies across every open session, daemon-side — so the message told
	// people to restart for something that had already happened, which is how a
	// working feature comes to be remembered as broken. (Being the only hosts
	// left, they took ChangeCWD's last caller with them; it is gone now.)
	TrustAppliesLive bool

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

	// PromoteModelDefault persists the model as a default in the given scope
	// ("project" -> .terva/config.json, honored only in a trusted workspace;
	// "global" -> the user config.json). It also updates the session meta.
	// Wired to Ctrl+D in the /model picker and to the post-login flow.
	PromoteModelDefault func(providerName, model, scope string) error

	// (RefreshCompatModels and RefreshModels used to live here. They had exactly
	// one caller between them — the login-success handler — and moved into
	// agent.ApplyLoginSuccess with the rest of what a login means, so the daemon
	// gets them for free rather than having to remember them. ApplyLoginStart /
	// ApplyLogin / ApplyLogout followed them there, and OnAssistant /
	// OnToolResult went with the direct driver that used to call them — the
	// carrier's event stream carries both.)

	// Extensions, if non-nil, lets users invoke extension-registered
	// slash commands (plus status segments, /context injection, panels,
	// /reload-ext). Commands declared by extensions are looked up AFTER the
	// built-in catalog so a built-in name always wins. The carrier path passes
	// a session-resolving adapter (see ExtensionHost).
	Extensions ExtensionHost

	// (The /extensions, per-extension-config and /mcp dialogs used to take
	// eleven host callbacks here — ListExtensions, SetExtension*, ApplyExtension*,
	// ListMCP, SetMCP*, ApplyMCPChange. They were the direct driver's half of
	// those dialogs, and outlived it: every one was nil under every frontend,
	// so each dialog's `case i.cfg.Carrier != nil` arm was the only reachable
	// one. The dialogs now read the extensions/mcp surfaces unconditionally.)

	// ReadLogTail returns the tail of a log file for the in-TUI log viewer
	// (the 'l' key in /extensions and /mcp). kind is "ext" or "mcp". nil
	// disables the viewer.
	ReadLogTail func(kind, name string) []string

	// (Swarm held the in-process *swarm.Swarm the direct driver ran agents on.
	// /swarm and the dashboard now drive the daemon's tasks surface — see
	// CarrierTasks — so the local engine had no caller left. The foreign-backend
	// gate the local spawn path duplicated lives on daemon-side, in
	// Workspace.AllowSpawn.)

	// SkillSnapshot, if non-nil, returns the current list of
	// discovered SKILL.md files. Re-invoked each time /skills opens
	// so the picker reflects edits made during the session.
	SkillSnapshot func() []*skills.Skill

	// ReloadSkills re-discovers skills and refreshes the live `skill`
	// tool's catalog so a SKILL.md added or edited mid-session becomes
	// loadable by name. It returns the visible set for the picker.
	//
	// Cache-safe by design: it swaps only the tool's internal list — it
	// does NOT rebuild the system prompt or change the tool registry, so
	// the prompt cache survives a reload. The trade: the model's
	// system-prompt manifest of skill names goes stale until the next
	// session, but any skill is still loadable by name (e.g. via /skill).
	ReloadSkills func() []*skills.Skill

	// (LoreList, LoreFired, LoreDropped and LoreFiredReset used to live here —
	// the direct driver's half of /lore. /lore now reads the lore surface for
	// the authored entries; the fired/dropped sections the last three fed were
	// dark from the moment that driver went away. See lore_view.go for where
	// the per-turn trace actually lives on the wire.)

	// SkillCompletions, if non-nil, returns the skill names + descriptions
	// offered as `/skill <name>` argument completions. It must be CHEAP — it
	// is called every render — so it reads the live in-memory skill catalog
	// (which reloads keep current), not a disk rescan.
	SkillCompletions func() []SkillCompletion

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
	SetRecursiveFileSuggest(enabled bool) error
	SetRespectGitignore(enabled bool) error
	SetTheme(name string) error
	// SetStatusLineRows persists the status-bar segment layout; nil
	// clears it back to the built-in per-mode defaults (preserving any
	// configured status scripts).
	SetStatusLineRows(rows [][]string) error
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

	// Terminal teardown state: restoreRaw leaves raw mode (from EnterRaw);
	// teardownOnce makes teardownTerminal safe to reach from both Run's exit
	// defer and the self-restart pre-exec hook; shuttingDown stops new
	// frames once teardown has begun (redraw checks it).
	restoreRaw   func() error
	teardownOnce sync.Once
	shuttingDown atomic.Bool
	// restartFailQuit is closed by resumeAfterFailedRestart only when a failed
	// self-restart leaves the terminal unrecoverable (raw mode won't re-enter);
	// the main loop selects on it to exit cleanly. Buffered/closed once.
	restartFailQuit chan struct{}

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

	// updateInfo is the result of the async update check. Zero value
	// while the check hasn't completed or nothing is available.
	updateInfo UpdateInfo

	dialog            *dialogs.LoginDialog
	modelDialog       *dialogs.ModelDialog
	modelEditDialog   *dialogs.ModelEditDialog
	extensionsDialog  *dialogs.ExtensionsDialog
	extConfigDialog   *dialogs.ExtConfigDialog
	mcpDialog         *dialogs.MCPDialog
	logDialog         *dialogs.LogDialog
	contextDialog     *dialogs.ContextDialog
	usageDialog       *dialogs.UsageDialog
	resetsDialog      *dialogs.ResetsDialog
	rescueDialog      *dialogs.RescueDialog
	sessionDialog     *dialogs.SessionDialog
	swarmDialog       *dialogs.SwarmDialog
	jumpDialog        *dialogs.JumpDialog
	btwDialog         *dialogs.BtwDialog
	skillsDialog      *dialogs.SkillsDialog
	changelogDialog   *dialogs.ChangelogDialog
	permissionsDialog *dialogs.PermissionsDialog
	confirmDialog     *dialogs.ConfirmDialog
	questionDialog    *dialogs.QuestionDialog
	logoutDialog      *dialogs.LogoutDialog
	connectDialog     *dialogs.ConnectDialog
	settingsDialog    *dialogs.SettingsDialog
	sessionOpsDialog  *dialogs.SessionOpsDialog
	sessionTreeDialog *dialogs.SessionTreeDialog
	extPanel          *dialogs.ExtPanelDialog
	tasksDialog       *dialogs.TasksDialog
	worktreeDialog    *dialogs.WorktreeDialog
	workflowDialog    *dialogs.WorkflowDialog

	// overlays is the priority-ordered modal registry: key routing,
	// rendering, cursor ownership, and tick animation for every
	// dialog above all derive from this one slice. See
	// overlay_registry.go.
	overlays []overlayEntry

	// keymap is the global key-binding table (chords that work
	// outside dialogs and popups). See keymap.go.
	keymap []globalBinding

	// pendingFork is true when the user ran /session fork: the next
	// jump-picker selection should branch off that message instead
	// of scrolling. Flag resets after the action fires or the dialog
	// is dismissed, so repeated /jump calls don't turn into forks.
	pendingFork bool
	suggest     *slashSuggester
	fileSuggest *widgets.FileSuggester
	spin        *widgets.Spinner

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

	// costBaseAt / costBase anchor the status bar's burn rate: the
	// instant this run's cost-meter epoch began and the cumulative
	// cost already accrued at that instant. A resumed session preloads
	// its whole history's cost into cumUsage, which must not count
	// toward the live $/hr. Guarded by i.mu (snapshotted per frame);
	// reset on /new and on session load.
	costBaseAt time.Time
	costBase   float64

	// lastStatusMinute throttles the idle minute-boundary repaint that
	// keeps the status bar's minute-granular text (↻ countdowns, burn
	// rate) fresh. Main-goroutine only.
	lastStatusMinute time.Time

	// gitInfo is the async prober's latest snapshot of the working
	// directory's repository state (status-bar git segment). Guarded
	// by i.mu. gitPoke requests an out-of-band probe (turn end, /cd);
	// buffered-1 so pokes coalesce like the resize channel.
	gitInfo tui.GitInfo
	gitPoke chan struct{}

	// editsAdded/editsRemoved tally lines the agent's edit/write tools
	// changed this session (status-bar edits segment). Guarded by
	// i.mu; reset on the same epochs as the burn rate.
	editsAdded   int
	editsRemoved int

	// scriptSegs holds each status script's latest output (guarded by
	// i.mu); scriptFailing tracks failure streaks so a broken script
	// notes once, not per run. scriptPoke coalesces runner triggers;
	// scriptExec is the exec seam (nil = real shell execution).
	scriptSegs    map[string]string
	scriptFailing map[string]bool
	scriptPoke    chan struct{}
	scriptExec    scriptExec

	// personaAccentRGB is cfg.PersonaAccent parsed once at
	// construction; nil when the persona has no accent color.
	personaAccentRGB *tui.TerminalColor
	// welcomeGreeting is the rotating tagline (Theme.Greeting), picked
	// once at startup so it stays stable across re-renders.
	welcomeGreeting string

	// extNotes are one-shot styled lines pushed by extensions via
	// Notify / Display. They live above the editor (just below the
	// transcript) until cleared by /clear or another reset.
	extNotes []string

	// stallNudges counts stuck-loop nudges in the current run so the "loop
	// detected" note coalesces into ONE line that counts up, instead of stacking
	// a note per nudge (a wedged run fires many). Tied to the note's presence in
	// extNotes: it resets implicitly when those clear on the next prompt.
	stallNudges int

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

	// clipboardImages holds images pasted via ctrl+v / the /paste command
	// this turn, each tied to a visible "[clipboard image #N]" marker in the
	// editor. On submit, preparePromptWithClipboardImages attaches the ones
	// whose marker survived and drops the rest; cleared after every submit.
	// UI-goroutine only (key/slash handlers and submit), so no mutex.
	clipboardImages []clipboardImageAttachment

	// carrierPerm / carrierAsk track the ctrlproto path's pending
	// permission/ask round-trips by wire id, so an EventPermissionResolved /
	// EventAskResolved (another client answered; the turn cancelled) can
	// dismiss the matching dialog entry instead of leaving it up to
	// double-answer. Guarded by mu; carrier mode only.
	carrierPerm map[string]*dialogs.ConfirmRequest
	carrierAsk  map[string]*dialogs.QuestionRequest

	// carrierPumpCancel cancels the pump's CURRENT subscription (not the
	// whole pump): SwitchCarrierSession re-points cfg.CarrierSession and then
	// fires this, and the supervisor loop re-subscribes to the new session.
	// Guarded by mu; carrier mode only.
	carrierPumpCancel context.CancelFunc

	// carrierLastPrompt/Images track the running turn's user prompt for the
	// rescue picker (the wire "error" event carries no prompt). Set at
	// dispatch; the prompt also refreshes from user_message events so turns
	// this client didn't dispatch (daemon queue restarts) stay rescuable —
	// images only survive for locally-dispatched turns (raw bytes don't ride
	// the event wire). Guarded by mu; carrier mode only.
	carrierLastPrompt string
	carrierLastImages []provider.ImageBlock

	// carrierApprovalMode caches the daemon-side gate's approval mode for the
	// status-bar badge (fetching a surface per frame would be absurd).
	// Refreshed on snapshot (every (re)subscribe) and on the daemon's
	// surface_updated("settings") broadcast. Guarded by mu.
	carrierApprovalMode string

	// The bound session's wire-reported metadata the status bar reads every
	// frame: the transcript path (sess segment — cached here so an attached
	// client doesn't RPC per repaint), the model's context window (ctx gauge
	// denominator — the daemon's catalog is authoritative; a version-skewed
	// attach client's local lookup may disagree), and the subscription flag
	// (the "(sub)" cost tag). Captured wherever a full SessionInfo lands:
	// every binding snapshot, session_updated events, and
	// SwitchCarrierSession's resume. Guarded by mu.
	carrierSessPath     string
	carrierCtxWindow    int
	carrierSubscription bool

	// carrierJailed mirrors the daemon's workspace sandbox lock for the
	// status bar's jailed badge — an attached TUI holds no Sandbox object.
	// Seeded from the server hello at attach and on every reconnect
	// (SetCarrierJailed). Guarded by mu.
	carrierJailed bool

	// carrierPanelSurface is the surface id of the daemon-backed extension
	// panel currently mirrored in the overlay ("" when the overlay is empty or
	// shows a command-result panel, which has no surface). Lets the
	// surfaces_changed close check tell the two apart. Guarded by mu.
	carrierPanelSurface string

	// carrierTaskRows caches the tasks surface for the /swarm dashboard,
	// which re-reads its snapshot every frame while open. The cache serves
	// those reads; a fetch happens only when carrierTasksStale is set — on
	// the daemon's surface_updated("tasks") broadcast (≤1 per poll tick,
	// change-driven) and when the dashboard opens. Guarded by mu.
	carrierTaskRows      []swarm.AgentSnapshot
	carrierTasksStale    bool
	carrierTasksFetching bool // one async fill in flight; render never blocks

	// carrierTaskBoard caches the per-session task-board surface (the built-in
	// task_* list) that backs the /tasks panel and the status-bar glance, both of
	// which re-read every frame. Push-filled by the pump (refreshCarrierTaskBoard)
	// on the daemon's surface_updated("taskboard") broadcast (once per turn end)
	// and on a snapshot resync, so the render path never blocks on a fetch.
	// Guarded by mu. Distinct from carrierTaskRows, the workspace swarm dashboard.
	// Cached as ctrlproto items; taskBoardRows maps them to tasks.Task for the
	// shared renderers.
	carrierTaskBoard []ctrlproto.TaskBoardItem
	// carrierTaskBoardSession is the session id carrierTaskBoard was filled for.
	// The board fetch is async (refreshCarrierTaskBoard runs off the pump), so a
	// switch can land while a fetch is in flight; keying the cache lets a commit
	// verify it still matches the live session before landing, and lets reads
	// ignore a board left over from a previous binding. Guarded by mu.
	carrierTaskBoardSession string

	// carrierWorktrees caches the "worktrees" surface (the built-in worktree
	// engine's list + collect view) backing /worktree and the status glance.
	// No push event exists for worktree changes: filled on session bind, on
	// /worktree open, and on the panel's r key (worktree_view.go). Guarded by
	// mu; keyed by session like the task board above.
	carrierWorktrees        *ctrlproto.WorktreeView
	carrierWorktreesSession string

	// workflowRuns / workflowView cache the /workflows panel's two fetches.
	// Workspace-scoped, not session-scoped (a run belongs to the host, not to a
	// conversation), so unlike the worktree cache above there is no session key
	// to invalidate against.
	workflowRuns []ctrlproto.WorkflowRunInfo
	workflowView *ctrlproto.WorkflowRunView

	// carrierMessages is the pump-owned transcript on the carrier path —
	// the wire twin of the crutch agent's Messages(), and what buildChat
	// renders in carrier mode. Snapshots replace it wholesale (they ride
	// every subscribe and the daemon re-broadcasts one after compact,
	// auto-compact, and clear); user_message/assistant_message events
	// append; tool_result events fold into a trailing RoleTool message,
	// mirroring core.Agent's per-step batching in executeTools.
	// carrierMessagesRev bumps on every mutation and keys the chat render
	// cache where the legacy path uses agent.Revision(). Guarded by mu.
	carrierMessages    []provider.Message
	carrierMessagesRev int

	// Compaction scrollback (docs/proposals/scrollback-history.md). A compaction
	// replaces the transcript with a summary plus a short tail, which — before this
	// — simply deleted the conversation from the screen. The turns are still in the
	// session file, so conversation.reveal pages them back.
	//
	// revealed holds them, DISPLAY-ONLY, prepended to carrierMessages at the render
	// site. Kept out of carrierMessages on purpose: that field is the wire twin of
	// what the model sees, and nothing that is not in the model's context belongs in
	// it. It also means a snapshot — which lands at the end of EVERY turn and
	// replaces carrierMessages wholesale — cannot silently throw the user's history
	// away.
	//
	// revealAnchor is the summary text of the live divider the reveals hang off, so
	// a snapshot can tell "same checkpoint, keep them" from "auto-compact ran, these
	// are stale" (the summary is written by the checkpoint that produced it).
	//
	// revealNext is the ordinal to ask for next; revealClear is set when a /clear
	// sits behind it, which STOPS the automatic walk — crossing one is a deliberate
	// act (/reveal), not something scrolling does for you. Guarded by mu.
	revealed     []provider.Message
	revealAnchor string
	revealNext   int
	revealClear  bool
	revealDone   bool
	revealBusy   bool

	// The first-paint tail cap, in three steps, because the transcript now
	// arrives from the pump instead of off a crutch agent at construction:
	//
	//   armed    — on every session bind (construction, SwitchCarrierSession)
	//   resolved — when the bind's first snapshot replaces the transcript,
	//              setCarrierTranscript decides the limit from its length
	//   applied  — by buildChat, on the main goroutine, because i.view is
	//              the renderer's and must not be written from the pump
	//
	// carrierTailPending is -1 when there is nothing to apply. Guarded by mu.
	carrierTailArmed   bool
	carrierTailPending int

	// carrierSeedArmed is the same one-shot, for the cost/context meters that
	// the binding's first snapshot hydrates from SessionInfo. Guarded by mu.
	carrierSeedArmed bool

	// carrierUsage mirrors the provider's subscription picture (plan and
	// rate-limit windows, credits) from the usage.snapshot verb — the wire twin
	// of the crutch agent's Usage(). Refreshed once per turn, once per binding,
	// mid-turn on (throttled) usage events, and when /usage opens. The status
	// bar reads it every frame. Guarded by mu.
	carrierUsage ctrlproto.UsageInfo

	// carrierUsageFetched is when a mirror refresh was last kicked off, for
	// the mid-turn throttle: a tool-heavy turn emits a usage event per
	// provider call, and each refresh is a carrier round-trip. Guarded by mu.
	carrierUsageFetched time.Time

	// carrierChat mirrors the daemon's chat pane (the bridge state + the
	// registered services). The status bar reads it every frame; /connect
	// renders its picker from it. Seeded once per binding (carrierChatArmed)
	// and refreshed on surface_updated("chat"), so a bridge connected from
	// another client — or one that outlived a previous TUI — shows up here.
	// The TUI owns no bridge: the workspace does. Guarded by mu.
	carrierChat      ctrlproto.ChatView
	carrierChatArmed bool

	// carrierQueued mirrors the session's pending message queue — the wire
	// twin of the crutch agent's PendingQueuedMessages(). The daemon owns
	// the queue and broadcasts it on every mutation (queue_updated), plus on
	// every snapshot, so this is a complete mirror rather than a guess.
	// Guarded by mu.
	carrierQueued []string

	// carrierReady gates prompting: true when the bound session can accept a
	// turn. It replaced "is the crutch agent installed?", which was never
	// about the agent — the TUI nilled it on /logout and re-grabbed it on
	// /login, purely to open and close this gate. Guarded by mu.
	carrierReady bool

	// bootSessionsPending is the one-shot arming of OpenSessionsOnBoot: set
	// from cfg at construction, cleared by whichever site opens the boot
	// picker first — Run on a ready boot, or the first successful in-TUI
	// login on a credential-less one (the login dialog keeps overlay
	// priority until dismissed). Main-loop-only state, like the dialogs.
	bootSessionsPending bool

	// loginFlow is the handle of the login attempt the dialog is currently showing.
	// The daemon holds the pkce verifier against it and refuses a submit from a
	// superseded flow, so the frontend has to carry it. Main-loop-only state, like
	// the dialogs.
	loginFlow string

	// titleGenBusy serializes the picker's `g` (on-demand title generation):
	// one model call at a time, a second press while one runs is refused.
	// Main-loop-only state (set on key handling, cleared via runOnMain).
	titleGenBusy bool

	// replayState is the latest transport state of a replay session (position,
	// total, playing, speed), stashed from EventReplayState broadcasts and read
	// by the transport keys and the status-bar scrubber. Zero until the first
	// replay_state arrives (the carrier autoplays on subscribe). Guarded by mu.
	replayState ctrlproto.ReplayState
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
		gitPoke:           make(chan struct{}, 1),
		scriptPoke:        make(chan struct{}, 1),
		scriptSegs:        map[string]string{},
		scriptFailing:     map[string]bool{},
		actions:           make(chan func(), 64),
		dialog:            dialogs.NewLoginDialog(),
		modelDialog:       dialogs.NewModelDialog(),
		modelEditDialog:   dialogs.NewModelEditDialog(),
		extensionsDialog:  dialogs.NewExtensionsDialog(),
		extConfigDialog:   dialogs.NewExtConfigDialog(),
		mcpDialog:         dialogs.NewMCPDialog(),
		logDialog:         dialogs.NewLogDialog(),
		contextDialog:     dialogs.NewContextDialog(),
		usageDialog:       dialogs.NewUsageDialog(),
		resetsDialog:      dialogs.NewResetsDialog(),
		rescueDialog:      dialogs.NewRescueDialog(),
		sessionDialog:     dialogs.NewSessionDialog(),
		swarmDialog:       dialogs.NewSwarmDialog(),
		jumpDialog:        dialogs.NewJumpDialog(),
		btwDialog:         dialogs.NewBtwDialog(),
		skillsDialog:      dialogs.NewSkillsDialog(),
		changelogDialog:   dialogs.NewChangelogDialog(),
		permissionsDialog: dialogs.NewPermissionsDialog(),
		confirmDialog:     dialogs.NewConfirmDialog(),
		questionDialog:    dialogs.NewQuestionDialog(),
		logoutDialog:      dialogs.NewLogoutDialog(),
		connectDialog:     dialogs.NewConnectDialog(),
		settingsDialog:    dialogs.NewSettingsDialog(),
		sessionOpsDialog:  dialogs.NewSessionOpsDialog(),
		sessionTreeDialog: dialogs.NewSessionTreeDialog(),
		extPanel:          dialogs.NewExtPanelDialog(),
		tasksDialog:       dialogs.NewTasksDialog(),
		worktreeDialog:    dialogs.NewWorktreeDialog(),
		workflowDialog:    dialogs.NewWorkflowDialog(),
		suggest:           newSlashSuggester(),
		fileSuggest:       widgets.NewFileSuggester(),
		spin:              widgets.NewSpinner(cfg.Theme),
		inputHistoryIndex: -1,
		carrierPerm:       map[string]*dialogs.ConfirmRequest{},
		carrierAsk:        map[string]*dialogs.QuestionRequest{},
	}
	i.overlays = i.buildOverlays()
	i.keymap = i.buildGlobalKeymap()
	if cfg.PersonaAccent != "" {
		if rgb, ok := tui.ParseHexColor(cfg.PersonaAccent); ok {
			i.personaAccentRGB = &rgb
		}
	}
	// Immersive experiences default to minimal tool display: the
	// conversation is the product there, and boxed tool mechanics read
	// as stage machinery. ctrl+t cycles back to boxes (or to hidden),
	// and ctrl+o still force-expands everything.
	if cfg.Experience != "" {
		i.view.ToolDisplay = tui.ToolDisplayMinimal
	}
	i.fileSuggest.SetRecursive(cfg.RecursiveFileSuggest == nil || *cfg.RecursiveFileSuggest)
	i.fileSuggest.SetRespectGitignore(cfg.RespectGitignore == nil || *cfg.RespectGitignore)
	if cfg.RemoteFiles != nil {
		// @-file completion lists the daemon's tree over the wire; the fill
		// lands off the render path and invalidate pops the popup in.
		i.fileSuggest.SetRemoteLister(cfg.RemoteFiles, i.invalidate)
	}
	// Arm the initial binding: its first snapshot decides the first-paint
	// tail cap and seeds the cost/context meters. Neither the transcript nor
	// the meters are known yet — both arrive from the pump.
	i.carrierTailPending = -1
	i.armCarrierBind()
	i.carrierReady = cfg.Ready
	i.bootSessionsPending = cfg.OpenSessionsOnBoot
	return i
}

// teardownTerminal returns the terminal to the shell's state: stop new
// frames, erase the live status/input band (while the renderer's
// cursor/viewport state still matches what's on screen — the chat transcript
// stays in scrollback), reset the modes the session enabled (scroll region,
// kitty images, enhanced keyboard, bracketed paste, cursor), and leave raw
// mode. Once-only: it runs on Run's exit defer AND from the self-restart
// pre-exec hook — syscall.Exec skips defers, so a restart must tear down
// eagerly or the next image inherits a raw, half-painted terminal.
func (i *Interactive) teardownTerminal() {
	i.teardownOnce.Do(func() {
		i.shuttingDown.Store(true)
		if i.rend != nil {
			i.rend.TeardownLog()
		}
		_, _ = i.cfg.Terminal.Write([]byte(tui.SeqResetScrollRegion + tui.SeqDeleteKittyImages + tui.SeqEnhancedKeyboardOff + tui.SeqBracketedPasteOff + tui.SeqShowCursor))
		if i.restoreRaw != nil {
			_ = i.restoreRaw()
		}
	})
}

// armTerminalModes enables the input/display modes the TUI runs under
// (bracketed paste, enhanced keyboard, a clean scroll region, no leftover kitty
// images). Emitted once on startup and again when recovering the terminal after
// a failed self-restart — the exact inverse of the *Off sequences teardown
// writes. It deliberately does NOT clear the screen or scrollback, so recovery
// keeps the chat transcript intact.
func (i *Interactive) armTerminalModes() {
	_, _ = i.cfg.Terminal.Write([]byte(tui.SeqBracketedPasteOn + tui.SeqEnhancedKeyboardOn + tui.SeqResetScrollRegion + tui.SeqDeleteKittyImages))
}

// resumeAfterFailedRestart reverses the pre-exec teardown when a deferred
// self-restart exec fails and relaunch keeps this process serving (see
// relaunch.OnFailure). Without it the pre-exec hook has already set
// shuttingDown, spent teardownOnce, left cooked mode, and erased the live band —
// so the surviving process can no longer paint or read keys. This runs on the
// main loop (terminal + renderer state is main-loop-only): it re-enters raw
// mode, re-arms the modes teardown turned off, re-arms teardownOnce so a later
// real exit still restores the terminal, clears the no-frames latch, and
// repaints (TeardownLog left the renderer marked for a full redraw). If raw mode
// can't be re-entered the terminal is unusable for input, so fall back to a
// clean exit rather than claim the process can continue.
func (i *Interactive) resumeAfterFailedRestart(cause error) {
	restore, err := i.cfg.Terminal.EnterRaw()
	if err != nil {
		// Terminal is still in cooked mode (teardown restored it), so the shell
		// is usable — just end the run loop cleanly rather than live on half-torn.
		fmt.Fprintf(os.Stderr, "relaunch: restart failed (%v) and the terminal could not be recovered (%v); exiting\n", cause, err)
		select {
		case <-i.restartFailQuit:
		default:
			close(i.restartFailQuit)
		}
		return
	}
	i.restoreRaw = restore
	i.teardownOnce = sync.Once{}
	i.shuttingDown.Store(false)
	i.armTerminalModes()
	cols, rows := i.cfg.Terminal.Size()
	i.rend.Resize(cols, rows)
	i.setStatusErr(i18n.T("restart failed: %s — continuing on the current build", cause.Error()))
	i.invalidate()
}

// Run blocks until the user quits.
func (i *Interactive) Run(ctx context.Context) error {
	i.runCtx = ctx
	i.restartFailQuit = make(chan struct{})
	// Rides the one-shot note area rather than statusOK: startup already
	// competes for the status line (the restricted-workspace notice), and
	// the last writer would silently win.
	if i.cfg.BootNotice != "" {
		i.extNotes = append(i.extNotes, "  "+i.cfg.Theme.FG256(i.cfg.Theme.Accent, "↻ "+i.cfg.BootNotice))
	}

	// Streaming-repaint cap (see resolveBusyRedrawInterval). The note is
	// emitted to stderr only when overridden, so a bug report from a user
	// running a non-default rate shows it; it lands before raw mode so it
	// doesn't disturb the TUI.
	busyRedrawInterval, redrawNote := resolveBusyRedrawInterval()
	if redrawNote != "" {
		fmt.Fprintln(os.Stderr, "note:", redrawNote)
	}

	term := i.cfg.Terminal
	restore, err := term.EnterRaw()
	if err != nil {
		return err
	}
	i.restoreRaw = restore
	defer i.teardownTerminal()

	// Enabling mouse reporting steals click-drag selection from the
	// host terminal (VS Code, Ghostty, iTerm). The user prefers native
	// selection over the wheel-speed boost, so we no longer turn it
	// on automatically. Wheel events fall through to the terminal's
	// own scrollback handler.
	// Keep terva on the terminal's main screen. We intentionally do not
	// enter the alternate-screen buffer (CSI ?1049h). The renderer emits
	// chat as normal terminal flow/scrollback and redraws only the live
	// input/status block on normal typing.
	i.armTerminalModes()
	_, _ = term.Write([]byte(tui.SeqClearScreenNoHome + tui.SeqClearScrollback + tui.MoveTo(1, 1)))
	// Tell the terminal our working directory (OSC 7) so "new tab / split
	// here" opens in the launch cwd instead of inheriting a stale directory
	// from an extension subprocess. Harmless on terminals that ignore it.
	if seq := tui.ReportCWD(i.cfg.CWD); seq != "" {
		_, _ = term.Write([]byte(seq))
	}

	// Self-restart (--allow-restart): syscall.Exec skips the teardown defer
	// above, so the pre-exec hook must return the terminal to the shell's
	// state eagerly — and hand the live session id to the next image so it
	// resumes this conversation instead of starting fresh. The hook fires on
	// the trigger goroutine (the tool call or control verb), but rend is
	// main-loop-only state: marshal the teardown there and wait, bounded —
	// relaunch.Delay outlasts the wait — falling back to a direct
	// once-guarded call if the main loop is wedged. Torn-down-off-main beats
	// exec'ing into a raw, half-painted terminal.
	if relaunch.Enabled() {
		relaunch.OnPreExec(func(string) {
			relaunch.SetHandoff("SESSION", i.carrierSession())
			done := make(chan struct{})
			i.runOnMain(func() { i.teardownTerminal(); close(done) })
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
				i.teardownTerminal()
			}
		})
		// If the deferred exec fails, relaunch keeps this process serving — but
		// the pre-exec hook already tore the terminal down. Marshal a recovery
		// onto the main loop so the survived process becomes a live TUI again
		// (or exits cleanly) instead of a dead one holding a torn-down terminal.
		relaunch.OnFailure(func(err error) {
			i.runOnMain(func() { i.resumeAfterFailedRestart(err) })
		})
	}

	// Streaming pacer: drains buffered text deltas at a steady rate
	// so typewriter feel is identical across providers regardless of
	// upstream chunk size. Starts here so it lives for the whole
	// session and exits with ctx.
	go i.turns.runPacer(ctx, i.invalidate)
	go i.runGitProber(ctx)
	go i.runStatusScripts(ctx)
	// ctrlproto mode: the event pump replaces the synchronous per-turn sink —
	// one reliable subscription for the whole session (tui-on-ctrlproto.md).
	if i.cfg.Carrier != nil {
		go i.runCarrierLoop(ctx)
		// ...and its sibling on the workspace address, for the events that are
		// not about any session (workspace surfaces, locale, notices).
		go i.runWorkspaceLoop(ctx)
	}

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

	// Anchor the burn-rate epoch: cost accrued before this run (a
	// resumed session's history, preloaded into cumUsage) belongs to
	// the base, not to the live $/hr.
	i.mu.Lock()
	i.costBaseAt = time.Now()
	i.costBase = i.cumUsage.CostUSD
	i.mu.Unlock()

	// The resume pin (--continue, --resume, --session) used to live here,
	// calling scrollToBottom off the crutch agent's transcript. It was a
	// no-op: everything scrollToBottom zeroes — scrollOffset, parkedTurn,
	// parkedTotal, prevChatLen, prevChatCols — is still zero this early in
	// Run, and the invalidate it also does is redundant before the first
	// paint. Verified by probe: the block ran and found every field zero.
	//
	// A real resume pin has to hang off the first snapshot, because the
	// transcript now arrives from the pump after Run has started. Nothing
	// currently needs it — the viewport starts pinned to the bottom.

	// No credential at startup? Auto-open the login dialog, and mark
	// the status line. The user can Esc out of the dialog if they
	// want to dismiss it (e.g. to check /help or /exit first). On a
	// carrier, only a login-capable host (in-process carrier, marked by
	// CarrierLogin) logs in here — a remote or replay carrier leaves it
	// nil because the daemon owns credentials (and a replay carrier has
	// no agent at all), so skip it there.
	if !i.ready() && (i.cfg.Carrier == nil || i.cfg.CarrierLogin != nil) {
		i.statusErr = i18n.T("not logged in. pick a login method below or press esc to dismiss.")
		i.dialog.Open(i.cfg.TervaHome)
	} else if i.cfg.JailNotice != "" {
		// A saved rule took the sandbox down for this directory. Say it before
		// the trust nag: trust withholds capability (the safe direction, and
		// the message is an offer), while this GRANTED capability — and unlike
		// a flag, the user is not being reminded by having just typed it.
		i.statusErr = i18n.T("%s", i.cfg.JailNotice)
	} else if !i.cfg.Trusted && i.cfg.GatedContentPresent {
		// Workspace Trust reminder: the cwd ships project extensions/
		// skills/context that were NOT loaded because the directory is
		// untrusted. Tell the user once, on the status line, how to opt
		// in. No prompt/dialog (inform-don't-prompt, decision #2).
		i.statusOK = i18n.T("restricted workspace: project extensions/skills/context not loaded — /trust to load them")
	}
	// --resume: open the session picker over the fresh boot. On a
	// credential-less boot the login dialog above stays armed instead;
	// finishCarrierLogin opens the picker after the first successful login.
	// Esc closes the picker onto the session the boot already bound — what
	// the command would have done without the flag.
	if i.bootSessionsPending && i.ready() {
		i.bootSessionsPending = false
		i.openSessionsDialog()
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

	// Auth events are NOT consumed here any more. A provider login is a fact about
	// the workspace, not about this frontend, and it arrives as an auth_state event
	// on the workspace address like any other — through runWorkspaceLoop, the same
	// pump the web panel's equivalent uses. That is what lets a device-code login
	// approved on a phone reach a TUI that never started it.

	// Animation ticker: drives spinner and dialog-related redraws when
	// nothing else changed. 120ms is slow enough that highlighting a huge
	// transcript doesn't spin the cpu.
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()

	// Redraw throttle: coalesce bursts of invalidate() calls so we paint
	// at most once per interval. Huge tool-result dumps can fire hundreds
	// of invalidations while the user is typing; without this, the input
	// goroutine never gets CPU and keystrokes lag.
	//
	// The interval is idleRedrawInterval normally, but widens to
	// busyRedrawInterval (the streaming-repaint cap, default 30fps) while a
	// turn is busy — the only time paints fire at high frequency. Capping
	// there cuts terminal-emulator load and SSH traffic and bounds
	// worst-case CPU, without slowing keystroke echo at an idle prompt.
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
		minInterval := idleRedrawInterval
		if i.turns.Busy() {
			minInterval = busyRedrawInterval
		}
		since := time.Since(lastRedraw)
		if since >= minInterval {
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
		wait := minInterval - since
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
		case <-i.restartFailQuit:
			// A failed self-restart left the terminal unrecoverable; the
			// resume path restored cooked mode and asked us to exit cleanly.
			return nil
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
			prevC, prevR := i.rend.Size()
			i.rend.Resize(c, r)
			if c == prevC && r == prevR {
				// A same-size SIGWINCH is not a resize — it is a terminal
				// multiplexer reattach (dtach/abduco/screen re-send WINCH
				// on attach) or a spurious signal. Resize() no-ops on an
				// unchanged size and the renderer still believes its cached
				// frame is on screen, so a plain redraw would emit nothing
				// and the reattached terminal would stay blank until the
				// next keypress. Force the clean full repaint a reattaching
				// user expects — the same clear+repaint Ctrl+L (keyRepaint)
				// runs. A real resize already clears inside Resize(), so
				// this only fires for the reattach case.
				i.rend.Clear()
			}
			i.redraw()
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
			// Minute-boundary refresh: the status bar renders minute-
			// granular text (↻ reset countdowns, the burn rate) that
			// otherwise goes stale while idle — nothing else invalidates
			// the frame. One redraw per minute; when no rendered text
			// actually changed, DrawLog's idle no-op fast path makes it
			// free (and keeps the cursor-blink behavior intact).
			if m := time.Now().Truncate(time.Minute); m != i.lastStatusMinute {
				i.lastStatusMinute = m
				i.pokeStatusScripts()
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
	i.statusErr = i18n.T("unknown command: %s", parts[0])
	i.mu.Unlock()
	return false
}
