import type { ComponentChildren, VNode } from 'preact'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { ADDR_WORKSPACE, Client } from './ctrlproto'
import type { ClientLike, ConnectableClient } from './ctrlproto'
import { restartRejection } from './restart'
import type {
  AskRequest,
  CatalogView,
  CommandsView,
  CacheSample,
  ContextBreakdown,
  ContextCache,
  ContextNode,
  Decision,
  ExtensionsView,
  ExtensionConfigField,
  FilesListResult,
  LoreEntry,
  LoreView,
  MCPView,
  ChatView,
  ChatServiceInfo,
  ModelInfo,
  ModelParamsView,
  ModelsResult,
  ReasoningRungInfo,
  AuthFlowStep,
  PanelView,
  PermissionRequest,
  PermissionsView,
  ProviderInfo,
  ProvidersView,
  SecretsStatus,
  RaatiHistoryItem,
  RaatiInquiry,
  RaatiUnit,
  RaatiView,
  ResetInfo,
  WireFileEntry,
  ResetsListResult,
  ResetConsumeResult,
  ArchivedSessionInfo,
  SessionInfo,
  Group,
  SettingsView,
  SkillInfo,
  Status,
  Surface,
  SurfaceMeta,
  TaskInfo,
  TaskList,
  TaskBoardView,
  UsageInfo,
  UsageSnapshotResult,
  UsageWindow,
  Widget,
  WireEvent,
  WireMessage,
  WireUsage,
  WorkflowRunInfo,
  WorkflowRunsResult,
  WorkflowRunView,
  WorktreeCollectItem,
  WorktreeView,
  WorktreeViewItem,
} from './ctrlproto'
import { Composer, type SlashCommand } from './features/conversation/Composer'
import { ConversationTimeline } from './features/conversation/ConversationTimeline'
import type { ToolView } from './features/conversation/types'
import { AskRequest as AskRequestView } from './features/interactions/AskRequest'
import { PermissionRequest as PermissionRequestView } from './features/interactions/PermissionRequest'
import { ModelParamsForm } from './features/models/ModelParamsForm'
import { ModelPicker } from './features/models/ModelPicker'
import { modelLabel } from './features/models/label'
import { SessionsBoard } from './features/board/SessionsBoard'
import { SwarmLane } from './features/board/SwarmLane'
import { WorkflowLane } from './features/board/WorkflowLane'
import { WorkflowRunDetail } from './features/board/WorkflowRunDetail'
import { PanelLanding } from './features/landing/PanelLanding'
import { applyBoardBusy, forgetBoardBusy, type BoardBusy } from './platform/board/store'
import { applyBoardApproval, forgetBoardApprovals, waitingByAgent, type BoardApprovals } from './platform/board/approvals'
import { pickBootTarget } from './platform/bootsession'
import { applyGroupFilter, cycleGroup, stageSystemGroup, SYS_STAGE, type GroupFilter } from './platform/groups'
import { AuthStepForm } from './features/providers/AuthStepForm'
import { SessionInfo as SessionInfoView } from './features/sessions/SessionInfo'
import { SessionPicker } from './features/sessions/SessionPicker'
import type { ImageAttachment } from './platform/conversation/images'
import { uploadFile, type FileAttachment } from './features/conversation/attachments'
import {
  applyEvent,
  mergeSnapshot,
  prependHistory,
  spliceRevealed,
  type Divider,
  type Item,
  type RevealSpan,
  type Window,
} from './platform/conversation/store'
import { PACE_INTERVAL_MS, StreamPacer } from './platform/conversation/pacer'
import { buildConveneArgs, raatiResultCopyText, raatiUnitCopyText, raatiVerdictWord } from './raati'
import { applyServerCatalog, setLocale, t, tn } from './i18n'
import { ReasoningPick, reasoningLabel } from './ui/ReasoningPick'
import { CopyButton } from './ui/CopyButton'
import { ConnectionBanner } from './ui/Loading'
import { deadlineClass, deadlineOf, deadlineStyle } from './ui/deadline'
import { humanBytes, humanCount, localInstant } from './ui/formatting'
import { stageHref, takeNavParams } from './ui/navlinks'
import { usePinnedTail } from './ui/pinnedtail'

const TOOL_VIEWS: ToolView[] = ['full', 'grouped', 'minimal', 'hidden']

// Per-TAB session memory. sessionStorage (NOT localStorage) is deliberate: it is
// scoped to the one tab, so a tab that has opened a session returns to it on
// reload/reconnect, while two tabs never converge on one session. This is the
// fix for the concurrent-client bug where a fresh panel with no `?session=`
// deep link silently adopted the server's global `current` (latest-mtime)
// session — so two clients drove the same session and a model switch or login
// in one moved the other. A fresh tab now lands on the session picker instead.
const TAB_SESSION_KEY = 'terva_tab_session'
function rememberedTabSession(): string {
  try {
    return sessionStorage.getItem(TAB_SESSION_KEY) || ''
  } catch {
    return '' // private-mode / storage-disabled: no memory, land on the picker
  }
}
function rememberTabSession(id: string) {
  try {
    if (id) sessionStorage.setItem(TAB_SESSION_KEY, id)
    else sessionStorage.removeItem(TAB_SESSION_KEY)
  } catch {
    /* storage disabled — memory is best-effort */
  }
}

// Deep-link params from Stage (`?session=`, `?pane=`), read once at module load
// because takeNavParams also strips them from the address bar. Fields are
// cleared as they are consumed, so a reconnect does not re-apply them.
const bootNav = takeNavParams()

// How many live tiles the board streams at once (phase B). Each is one server
// pump + one snapshot on subscribe; the cap bounds the reconnect snapshot storm
// on a large workspace. The rest fall back to the periodic list's busy flag.
const BOARD_SUB_CAP = 16

// createClient is the transport seam. App owns the socket's LIFECYCLE — it
// connects on mount and closes on unmount — so it takes a factory rather than an
// instance; handing it a live client would split that ownership. Defaulting to
// `new Client()` keeps main.tsx a bare <App />.
//
// It exists because App was untestable without it: `new Client()` sat inside the
// mount effect with no prop, context or factory, so nothing could observe the
// 1193 lines of orchestration below — the event demux, the reconnect
// re-subscribe, the hello feature-gating. Stage had already solved this by
// threading its client down as a prop, which is why Stage's screens have tests
// and this one had none.
export function App({ createClient = () => new Client() }: { createClient?: () => ConnectableClient } = {}) {
  const clientRef = useRef<ConnectableClient | null>(null)
  const curRef = useRef('')
  const busyRef = useRef(false)

  const [status, setStatus] = useState<Status>('connecting')
  // Bumped whenever the active catalog changes (locale learned from the hello,
  // or the server catalog fetched/overlaid) to force a re-render — t()/tn() read
  // the module-level catalog, so the value itself needn't be the locale string.
  const [, bumpI18n] = useState(0)
  const reI18n = useCallback(() => bumpI18n((n) => n + 1), [])
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  // Whether sessions.list has ever ANSWERED. Distinct from `status === 'open'`,
  // which only means the hello landed — the list is a further round trip inside
  // onReady. Without this the empty array above (a useState default, not an
  // answer) rendered as "No sessions in this workspace yet.", so a panel that
  // had merely finished painting asserted an empty workspace. Only a successful
  // list flips it: a rejected one leaves us with no answer at all, which is the
  // placeholder's case and not the empty state's.
  const [sessionsLoaded, setSessionsLoaded] = useState(false)
  // Session groups (a membership bucket over sessions; absent on an older
  // daemon). The board/picker filter by include/exclude (platform/groups).
  const [sessionGroups, setSessionGroups] = useState<Group[]>([])
  // The session filter persists across reloads and DEFAULTS to hiding Stage play
  // sessions (the derived `sys:stage` group) so they don't clutter the coding
  // board; the user clears that chip to see them. A malformed stored value falls
  // back to the default rather than throwing.
  const [sessionFilter, setSessionFilter] = useState<GroupFilter>(() => {
    const raw = localStorage.getItem('terva_session_filter')
    if (raw) {
      try {
        const p = JSON.parse(raw)
        if (Array.isArray(p?.include) && Array.isArray(p?.exclude)) return { include: p.include, exclude: p.exclude }
      } catch {
        /* fall through to default */
      }
    }
    return { include: [], exclude: [SYS_STAGE] }
  })
  const [curSess, setCurSess] = useState('')
  const [items, setItems] = useState<Item[]>([])
  // The compaction divider currently paging in its history, if any. The ref is the
  // guard (a second click while the first is in flight would splice twice); the
  // state is only there to render the spinner on the right divider.
  const [revealingID, setRevealingID] = useState('')
  const revealingRef = useRef('')
  // Where the transcript window we hold sits. A snapshot carries only the tail (we
  // negotiate 'history-window'), so `base` is the index of the oldest message we have —
  // base > 0 means there is more above, fetched with conversation.history — and `epoch`
  // identifies the transcript itself. The ref is what the merge and the history call
  // read; the state is what the UI renders.
  const [win, setWin] = useState({ epoch: 0, base: 0, total: 0 })
  const winRef = useRef({ epoch: 0, base: 0, total: 0 })
  const [loadingEarlier, setLoadingEarlier] = useState(false)
  const loadingEarlierRef = useRef(false)
  const [models, setModels] = useState<ModelInfo[]>([])
  // The reasoning ladders that came with the model list. Held beside `models`
  // rather than inside it because they are shared: 440 catalog models resolve
  // to a dozen ladders, and ModelInfo.ladder is the key into this table.
  const [ladders, setLadders] = useState<Record<string, ReasoningRungInfo[]>>({})
  // The workspace-wide thinking level, so the picker's inherit row can name what
  // inheriting would actually mean instead of pointing at "the global setting"
  // without saying what it is. It lives in the settings surface (the daemon's
  // own `reasoning` SettingItem) rather than in a field of its own — the value
  // was already on the wire, just behind a pane nobody opens to read it.
  const [globalReasoning, setGlobalReasoning] = useState('')
  const [busy, setBusy] = useState(false)
  const [cost, setCost] = useState(0)
  const [permission, setPermission] = useState<PermissionRequest | null>(null)
  const [ask, setAsk] = useState<AskRequest | null>(null)
  const [drawer, setDrawer] = useState(false)
  const [toast, setToast] = useState('')
  const [toolView, setToolView] = useState<ToolView>(
    () => (localStorage.getItem('terva_toolview') as ToolView) || 'full',
  )
  // focus (single-session conversation) ⇄ board (a tile per session). Persisted
  // like toolView; the board is a monitor + focus + lifecycle view — clicking a
  // tile focuses that session and returns to focus mode.
  const [viewMode, setViewMode] = useState<'focus' | 'board'>(
    () => (localStorage.getItem('terva_viewmode') === 'board' ? 'board' : 'focus'),
  )
  const viewModeRef = useRef(viewMode)
  const wfLiveRef = useRef(false)
  // The board's second lane: the workspace swarm (the tasks surface), fetched
  // while the board is open and refreshed by surface_updated("tasks").
  const [boardTasks, setBoardTasks] = useState<TaskInfo[]>([])
  // Whether the tasks surface has answered. Boot into board mode and the lane
  // renders before the fetch resolves — as "No swarm agents running.", which is
  // exactly the claim an operator opening the board to check on a swarm would
  // read as an answer.
  const [boardTasksLoaded, setBoardTasksLoaded] = useState(false)
  // The spawn capability the tasks surface advertises: which worker backends a
  // human may dispatch, and whether external workers are enabled (foreign spawn
  // is gated on it — the picker shows them greyed otherwise).
  const [boardBackends, setBoardBackends] = useState<string[]>([])
  const [boardWorkersEnabled, setBoardWorkersEnabled] = useState(false)
  // Phase B: per-tile busy from live subscriptions the board holds while open.
  // boardSubsRef is the set of sessions currently subscribed FOR the board
  // (never the focused one — the focus view owns that). Capped so a large
  // workspace doesn't open dozens of server pumps at once.
  const [boardBusy, setBoardBusy] = useState<BoardBusy>({})
  const boardSubsRef = useRef<Set<string>>(new Set())
  // Which swarm workers are parked on a human approval (call_id -> worker+session).
  // Fed from every approval event regardless of address (a worker's request rides
  // its dispatching session's stream — the focused one or a board sub); read only
  // in board mode, to badge the stalled lane tile.
  const [boardApprovals, setBoardApprovals] = useState<BoardApprovals>({})
  // The board's THIRD lane: workflow runs this host has on disk. Read from the
  // run records, not from a live engine — `terva workflow run` is a separate
  // foreground process this daemon never sees, so what is knowable is what a run
  // LEFT. `workflowsOn` starts true and latches off the first time the daemon
  // answers `unsupported`, which is how a build without the controller (or a
  // replay carrier, which has no run root at all) renders no lane instead of an
  // empty one that looks like "no runs".
  const [workflowRuns, setWorkflowRuns] = useState<WorkflowRunInfo[]>([])
  // Whether workflows.list has answered. The poll that fills it is itself gated
  // on the socket being open (see its effect), so on a panel that boots straight
  // into board mode this lane provably has not asked yet — and said so as "No
  // workflow runs on this host yet."
  const [workflowRunsLoaded, setWorkflowRunsLoaded] = useState(false)
  const [workflowsOn, setWorkflowsOn] = useState(true)
  // The opened run: null while none, and while one is loading (the modal shows
  // its own loading state off `wfOpen`).
  const [wfOpen, setWfOpen] = useState('')
  const [wfView, setWfView] = useState<WorkflowRunView | null>(null)
  const [wfErr, setWfErr] = useState('')
  const [curInfo, setCurInfo] = useState<SessionInfo | null>(null)
  const [infoOpen, setInfoOpen] = useState(false)
  const [queued, setQueued] = useState<string[]>([])
  // Whether the daemon advertised the self-restart capability (--web-allow-restart).
  const [canRestart, setCanRestart] = useState(false)
  // Whether the daemon SERVES the secrets group (--web-allow-secrets). A GROUP
  // rather than a feature, so the check is hello.groups: it decides whether the
  // verbs exist at all, and a tab that calls one the daemon never negotiated is
  // a tab whose every control answers "method group not negotiated".
  const [canReadSecrets, setCanReadSecrets] = useState(false)
  const [wsSecrets, setWsSecrets] = useState<SecretsStatus | null>(null)
  const [wsSecretsErr, setWsSecretsErr] = useState('')
  // Whether the daemon serves the Stage app (--web-stage). When set, the topbar
  // shows a link across to /stage/; the panel is otherwise unaware of Stage.
  const [stageEnabled, setStageEnabled] = useState(false)
  // Whether this carrier can stage file attachments (ctrlproto "attachments"),
  // and the per-file ceiling it advertised. 0 = it did not say, so let the
  // daemon be the judge rather than inventing a bound client-side.
  const [canAttachFiles, setCanAttachFiles] = useState(false)
  const [maxAttachmentBytes, setMaxAttachmentBytes] = useState(0)
  // ...and whether it serves the files an agent shared BACK (ctrlproto
  // "shared-files"). The record reaches every client either way; this is
  // whether there is a link behind it, or only a label.
  const [canDownloadShares, setCanDownloadShares] = useState(false)
  // The daemon's workspace file listing for the composer's @-stage, fetched
  // lazily on first "@" and refreshed by TTL (the tree moves as the agent
  // works). null = feature absent or nothing fetched yet.
  const [fileList, setFileList] = useState<WireFileEntry[] | null>(null)
  const canListFiles = useRef(false)
  const fileListAt = useRef(0)
  const fileListInFlight = useRef(false)
  // The daemon's build display string from the ctrlproto hello. The ref
  // remembers the previous connection's value so a reconnect that lands on a
  // different build (a self-restart picked up a new binary) can announce it.
  const [serverVersion, setServerVersion] = useState('')
  const verRef = useRef('')
  // Available skills (from the snapshot), for /skill autocomplete.
  const [skills, setSkills] = useState<SkillInfo[]>([])
  const [pickerOpen, setPickerOpen] = useState(false)
  const [reasoningOpen, setReasoningOpen] = useState(false)
  // The per-model overrides (models.json). Held here rather than in the picker so
  // a failed save keeps its error next to the form that produced it.
  const [modelParams, setModelParams] = useState<ModelParamsView | null>(null)
  const [modelParamsBusy, setModelParamsBusy] = useState(false)
  const [modelParamsErr, setModelParamsErr] = useState('')
  // Pane host (surfaces): context/usage/extension panels in a right rail.
  const [paneOpen, setPaneOpen] = useState(false)
  const [surfaces, setSurfaces] = useState<SurfaceMeta[]>([])
  const [activeSurface, setActiveSurface] = useState('context')
  const [surfaceData, setSurfaceData] = useState<Surface | null>(null)
  // The provider's usage picture, mirrored from the usage.snapshot verb. It
  // exists because ContextBreakdown.usage_windows is a PASSIVE read of whatever
  // the provider client happens to have observed already, and only half the
  // providers ever observe anything that way — see loadUsageSnapshot.
  const [usageSnap, setUsageSnap] = useState<UsageInfo | null>(null)
  // The workspace drawer's own state — the landing has no session, so it cannot
  // ask for surfaces (every one of them is served through a session handle, and
  // an empty address MINTS one). It asks the session-independent verbs instead.
  const [wsTab, setWsTab] = useState<'providers' | 'secrets' | 'about'>('providers')
  const [wsProviders, setWsProviders] = useState<ProvidersView | null>(null)
  const [wsProvidersErr, setWsProvidersErr] = useState('')
  // The provider-login flow in progress, if any: the daemon hands us a step to
  // render, we hand back a name→value map. null means "show the provider list".
  const [authFlow, setAuthFlow] = useState<AuthFlowStep | null>(null)
  const [authBusy, setAuthBusy] = useState(false)
  const [authErr, setAuthErr] = useState('')
  const [surfaceErr, setSurfaceErr] = useState('')
  const paneOpenRef = useRef(false)
  const activeSurfaceRef = useRef('context')

  // The streaming pacer sits between the wire and handleEvent, so the transcript
  // reveals at an even rate no matter how coarsely the provider chunks its
  // deltas (see platform/conversation/pacer.ts). handleEvent is a useCallback and
  // changes identity, so the pacer reaches it through a ref rather than being
  // rebuilt — rebuilding it would drop whatever text it still had buffered.
  const handleEventRef = useRef<(ev: WireEvent) => void>(() => {})
  const pacerRef = useRef<StreamPacer | null>(null)
  if (!pacerRef.current) pacerRef.current = new StreamPacer((ev) => handleEventRef.current(ev))

  curRef.current = curSess
  busyRef.current = busy
  paneOpenRef.current = paneOpen
  activeSurfaceRef.current = activeSurface
  viewModeRef.current = viewMode
  // Whether any run could still be moving. Read by the workflow poll below, so
  // a board full of finished runs stops re-scanning journals it already read.
  wfLiveRef.current = workflowRuns.some((r) => r.status === 'incomplete')

  const cycleToolView = useCallback(() => {
    setToolView((v) => {
      const next = TOOL_VIEWS[(TOOL_VIEWS.indexOf(v) + 1) % TOOL_VIEWS.length]
      localStorage.setItem('terva_toolview', next)
      return next
    })
  }, [])

  const modelGroups = useMemo(() => {
    const g = new Map<string, ModelInfo[]>()
    for (const m of models) {
      const arr = g.get(m.provider) ?? []
      arr.push(m)
      g.set(m.provider, arr)
    }
    // Favorites float to the top of their provider group (stable sort keeps the
    // rest in catalog order), matching the TUI.
    for (const arr of g.values()) {
      arr.sort((a, b) => (a.favorite === b.favorite ? 0 : a.favorite ? -1 : 1))
    }
    return [...g.entries()]
  }, [models])
  const favorites = useMemo(() => models.filter((m) => m.favorite), [models])

  const reloadModels = useCallback(async () => {
    const c = clientRef.current
    if (!c) return
    try {
      // Frame the focused session so the daemon flags Current from THIS
      // session's model, not a workspace-global value (empty = the default).
      const res = await c.send<ModelsResult>('models.list', null, curRef.current)
      setModels(res.models ?? [])
      setLadders(res.reasoning_ladders ?? {})
    } catch {
      /* control group optional */
    }
  }, [])

  // Refetch the model list whenever the focused session changes so the picker's
  // "current" flag tracks THIS session's model. reloadModels reads curRef.current,
  // set synchronously before curSess in selectSession, so it frames the new one.
  useEffect(() => {
    reloadModels()
  }, [curSess, reloadModels])

  // requestFiles backs the composer's @-stage: one fetch per TTL window,
  // recursive with gitignore filtering (the TUI picker's defaults), entries
  // cached until the TTL lapses or a reconnect drops them.
  const requestFiles = useCallback(async () => {
    const c = clientRef.current
    if (!c || !canListFiles.current || fileListInFlight.current) return
    if (Date.now() - fileListAt.current < 30_000) return
    fileListInFlight.current = true
    try {
      const res = await c.send<FilesListResult>('files.list', { recursive: true, respect_gitignore: true }, '')
      setFileList(res.files ?? [])
      fileListAt.current = Date.now()
    } catch {
      // A daemon mid-restart or an older build: leave the stage empty; the
      // next "@" (past the TTL) retries.
      fileListAt.current = Date.now()
    } finally {
      fileListInFlight.current = false
    }
  }, [])

  const favoriteModel = useCallback(
    async (provider: string, id: string, on: boolean) => {
      const c = clientRef.current
      if (!c) return
      try {
        await c.send('models.favorite', { provider, model: id, on }, '')
        await reloadModels()
      } catch (e) {
        setToast(String(e))
      }
    },
    [reloadModels],
  )

  // Adopt a model as the default for NEW sessions. Session-independent (''),
  // like models.favorite — and deliberately NOT a switch: this session stays on
  // whatever it was on, because trying a model and adopting it are different
  // decisions. The daemon confirms by re-listing, so the ◉ moves only once the
  // write actually landed.
  const setDefaultModel = useCallback(
    async (provider: string, id: string, scope: 'global' | 'project') => {
      const c = clientRef.current
      if (!c) return
      try {
        await c.send('models.set_default', { provider, model: id, scope }, '')
        await reloadModels()
        setToast(
          scope === 'project'
            ? t('%s is the default for this project', id)
            : t('%s is the default for new sessions', id),
        )
      } catch (e) {
        setToast(String(e))
      }
    },
    [reloadModels],
  )

  const refreshSessions = useCallback(async (c: ClientLike): Promise<SessionInfo[]> => {
    try {
      const res = await c.send<{ sessions: SessionInfo[] }>('sessions.list', null, '')
      setSessions(res.sessions ?? [])
      setSessionsLoaded(true)
      // Session groups ride the same refresh (and the sessions_changed event a
      // group mutation broadcasts). An older daemon answers "unsupported".
      c.send<{ groups: Group[] }>('sessiongroups.list', null, '')
        .then((r) => setSessionGroups(r.groups ?? []))
        .catch(() => {})
      return res.sessions ?? []
    } catch {
      return []
    }
  }, [])

  // File a session in or out of a group (the sole membership mutation is the
  // group's whole new list), then refresh so every tile's badge updates.
  const toggleSessionGroup = useCallback(
    async (s: SessionInfo, groupId: string) => {
      const c = clientRef.current
      if (!c) return
      const g = sessionGroups.find((x) => x.id === groupId)
      if (!g) return
      const members = g.members.includes(s.id) ? g.members.filter((m) => m !== s.id) : [...g.members, s.id]
      await c.send('sessiongroups.set_members', { id: g.id, members }, '').catch((e) => setToast(String(e)))
      await refreshSessions(c)
    },
    [sessionGroups, refreshSessions],
  )
  // Create a group and drop this session into it in one step.
  const createSessionGroup = useCallback(
    async (s: SessionInfo) => {
      const c = clientRef.current
      if (!c) return
      const name = window.prompt(t('Name the new group:'), '')
      if (name == null || !name.trim()) return
      try {
        const g = await c.send<Group>('sessiongroups.save', { name: name.trim() }, '')
        await c.send('sessiongroups.set_members', { id: g.id, members: [s.id] }, '')
      } catch (e) {
        setToast(String(e))
        return
      }
      await refreshSessions(c)
    },
    [refreshSessions],
  )
  // Cycle a chip in the board/picker group filter (off → show-only → hide → off).
  const cycleSessionGroup = useCallback((id: string) => setSessionFilter((f) => cycleGroup(f, id)), [])
  // Persist the filter so the "hide Stage" default (and any manual change) rides
  // across reloads.
  useEffect(() => {
    localStorage.setItem('terva_session_filter', JSON.stringify(sessionFilter))
  }, [sessionFilter])
  // While the sessions drawer is open, lock background scroll. On iOS a fixed
  // overlay can otherwise leave the document scrolled when it closes — the other
  // way the top bar ends up under the notch (the --safe-* freeze handles the
  // inset itself). A no-op where nothing scrolls behind the overlay.
  useEffect(() => {
    if (!drawer) return
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [drawer])

  // Fetch the tasks surface for the board's swarm lane. Workspace-scoped (any
  // sess returns the global list), so it rides the focused session's address.
  const fetchBoardTasks = useCallback(async () => {
    const c = clientRef.current
    if (!c) return
    try {
      const res = await c.send<{ surface: Surface }>('surface.get', { id: 'tasks' }, curRef.current)
      const tasks = res.surface?.tasks?.tasks ?? []
      setBoardTasks(tasks)
      setBoardBackends(res.surface?.tasks?.backends ?? [])
      setBoardWorkersEnabled(!!res.surface?.tasks?.workers_enabled)
      // Prune stalled-approval state for workers that have since vanished (a
      // stopped/removed agent), so a missed resolved can't leave a stale badge.
      setBoardApprovals((s) => forgetBoardApprovals(s, new Set(tasks.map((tk) => tk.id))))
    } catch {
      setBoardTasks([])
    } finally {
      // In `finally` rather than only on success, deliberately: the catch above
      // already treats ANY error as an answer of "no agents", so marking the
      // fetch answered here matches the contract this function already has
      // instead of quietly changing it. The pre-existing wart that survives:
      // a transient failure (a dropped socket) still blanks the lane rather
      // than holding the last list. The connection banner now explains that
      // case, which is the part the operator could not previously see at all.
      setBoardTasksLoaded(true)
    }
  }, [])

  // Fetch the workflow run list for the board's third lane. Session-independent
  // (a run belongs to the workspace), so it passes no session address at all.
  //
  // An `unsupported` refusal is a capability answer, not a failure: it means
  // this daemon does not serve the verb, and the lane should disappear rather
  // than sit there empty. Any other error leaves the last list up — a dropped
  // poll should not blank a lane the operator is reading.
  const fetchWorkflowRuns = useCallback(async () => {
    const c = clientRef.current
    if (!c) return
    try {
      const res = await c.send<WorkflowRunsResult>('workflows.list', undefined, '')
      setWorkflowRuns(res.runs ?? [])
      setWorkflowRunsLoaded(true)
      setWorkflowsOn(true)
    } catch (e) {
      if (String((e as { message?: string })?.message || '').startsWith('unsupported')) {
        setWorkflowsOn(false)
        setWorkflowRuns([])
        // A refusal IS an answer — the lane is about to disappear, but for the
        // frame before it does it must not claim the host has no runs.
        setWorkflowRunsLoaded(true)
      }
    }
  }, [])

  // Open one run: its record, the script as it ran, and the reports it journaled.
  const openWorkflowRun = useCallback(async (id: string) => {
    const c = clientRef.current
    if (!c) return
    setWfOpen(id)
    setWfView(null)
    setWfErr('')
    try {
      setWfView(await c.send<WorkflowRunView>('workflows.get', { id }, ''))
    } catch (e) {
      setWfErr((e as { message?: string })?.message || t('could not open that run'))
    }
  }, [])

  // Spawn a swarm agent from the board's lane — native (empty backend) or a
  // foreign worker. The daemon applies the same gate the model's swarm_spawn tool
  // does, so a disallowed foreign backend is refused; use send (awaited), not the
  // fire-and-forget surface action, to surface that refusal instead of a silent
  // no-op. On success the new agent arrives via the tasks surface_updated broadcast.
  const spawnWorker = useCallback(async (task: string, backend: string) => {
    const c = clientRef.current
    if (!c) return
    const args: Record<string, string> = { task }
    if (backend) args.backend = backend
    try {
      await c.send<unknown>('surface.action', { id: 'tasks', action: 'spawn', args }, curRef.current)
    } catch (e) {
      setToast((e as { message?: string })?.message || t('could not spawn the agent'))
    }
  }, [])

  // Reconcile the board's live subscriptions to the currently-live tiles (minus
  // the focused session, whose subscription the focus view owns): subscribe
  // newcomers, unsubscribe those gone, cap the total. Each subscribe opens with
  // one snapshot — bounded by the cap — from which the store takes only busy.
  const reconcileBoardSubs = useCallback((liveIds: string[]) => {
    const c = clientRef.current
    if (!c) return
    const want = new Set(liveIds.filter((id) => id && id !== curRef.current).slice(0, BOARD_SUB_CAP))
    const have = boardSubsRef.current
    for (const id of want) if (!have.has(id)) c.fire('subscribe', null, id)
    for (const id of have) if (!want.has(id)) c.fire('unsubscribe', null, id)
    boardSubsRef.current = want
    setBoardBusy((s) => forgetBoardBusy(s, want))
  }, [])

  // Drop every board subscription (leaving board mode). Never the focused
  // session: clicking a tile focuses it AND leaves board mode in the same tick,
  // so by the time this runs curRef.current may be a session that was a board
  // sub a moment ago and now backs the focus view — unsubscribing it would
  // freeze the conversation we just opened.
  const clearBoardSubs = useCallback(() => {
    const c = clientRef.current
    if (c) for (const id of boardSubsRef.current) {
      if (id !== curRef.current) c.fire('unsubscribe', null, id)
    }
    boardSubsRef.current = new Set()
    setBoardBusy({})
  }, [])

  const selectSession = useCallback((id: string) => {
    const c = clientRef.current
    if (!c || !id || id === curRef.current) {
      setDrawer(false)
      return
    }
    if (curRef.current) c.fire('unsubscribe', null, curRef.current)
    setItems([])
    // The window belongs to the session we are leaving. Zeroing the epoch also makes
    // the next snapshot's merge a rebuild, which is what it must be.
    winRef.current = { epoch: 0, base: 0, total: 0 }
    setWin(winRef.current)
    setPermission(null)
    setAsk(null)
    setBusy(false)
    setCost(0)
    setQueued([])
    setCurInfo(null)
    setInfoOpen(false)
    setSurfaces([])
    setSurfaceData(null)
    // Anything the pacer still has buffered belongs to the session we are leaving;
    // replaying it here would splice one transcript into another.
    pacerRef.current?.reset()
    curRef.current = id
    setCurSess(id)
    rememberTabSession(id)
    c.fire('subscribe', null, id)
    setDrawer(false)
  }, [])

  // goToLanding leaves the focused session and returns to the session picker
  // (curSess === ''), the boot state for a tab with no remembered or deep-linked
  // session. Mirrors selectSession's teardown but subscribes to nothing and
  // forgets the tab's session, so a reload stays on the landing rather than
  // re-adopting a session.
  const goToLanding = useCallback(() => {
    const c = clientRef.current
    if (curRef.current && c) c.fire('unsubscribe', null, curRef.current)
    setItems([])
    winRef.current = { epoch: 0, base: 0, total: 0 }
    setWin(winRef.current)
    setPermission(null)
    setAsk(null)
    setBusy(false)
    setCost(0)
    setQueued([])
    setCurInfo(null)
    setInfoOpen(false)
    setSurfaces([])
    setSurfaceData(null)
    pacerRef.current?.reset()
    curRef.current = ''
    setCurSess('')
    rememberTabSession('')
    setDrawer(false)
  }, [])

  const newSession = useCallback(
    async (opts?: { persona?: string; model?: string; provider?: string }) => {
      const cl = clientRef.current
      if (!cl) return
      try {
        // CreateOpts already carries persona/model/provider — the landing's
        // "new session" flow threads a chosen persona (and optional model) here
        // so the harness opens in-character, which is how it is meant to be used.
        const res = await cl.send<{ session: SessionInfo }>('sessions.create', { title: '', ...(opts ?? {}) }, '')
        await refreshSessions(cl)
        selectSession(res.session.id)
      } catch (e) {
        setToast(String(e))
      }
    },
    [refreshSessions, selectSession],
  )

  const listSurfaces = useCallback(async () => {
    const c = clientRef.current
    if (!c || !curRef.current) return
    try {
      const res = await c.send<{ surfaces: SurfaceMeta[] }>('surfaces.list', null, curRef.current)
      setSurfaces(res.surfaces ?? [])
    } catch {
      /* session group optional */
    }
  }, [])

  // The provider picture, without a session. auth.providers and the "providers"
  // surface are the same data by construction (the daemon's own comment: one
  // shape, one implementation, nothing to drift) — so the drawer renders it with
  // the very same body the session rail uses, and a change to either follows.
  const loadWorkspaceProviders = useCallback(async () => {
    const c = clientRef.current
    if (!c) return
    setWsProvidersErr('')
    try {
      setWsProviders(await c.send<ProvidersView>('auth.providers', null, ''))
    } catch (e) {
      setWsProviders(null)
      setWsProvidersErr(String(e))
    }
  }, [])

  // The at-rest posture, without a session — a fact about the daemon, like the
  // provider picture beside it. Session-independent by construction: the verb
  // ignores the address (a key belongs to the home, not to a conversation).
  //
  // Called only when the daemon negotiated the group. Nothing here is a secret
  // value; the daemon builds this report to be safe on a screen someone else
  // can see, which is exactly what a browser pane is.
  const loadWorkspaceSecrets = useCallback(async () => {
    const c = clientRef.current
    if (!c) return
    setWsSecretsErr('')
    try {
      setWsSecrets(await c.send<SecretsStatus>('secrets.status', null, ''))
    } catch (e) {
      setWsSecrets(null)
      setWsSecretsErr(String(e))
    }
  }, [])

  const loadSurface = useCallback(async (id: string) => {
    const c = clientRef.current
    if (!c || !curRef.current) return
    setSurfaceErr('')
    try {
      const res = await c.send<{ surface: Surface }>('surface.get', { id }, curRef.current)
      setSurfaceData(res.surface)
    } catch (e) {
      setSurfaceData(null)
      setSurfaceErr(String(e))
    }
  }, [])

  // refreshGlobalReasoning reads the workspace thinking level out of the
  // settings surface.
  //
  // It is a separate fetch rather than a read of `surfaceData` because that
  // holds only the ONE pane currently open, and the reasoning button sits in the
  // topbar where no pane need ever have been opened. Failure is silent and
  // leaves the level empty, which the picker already renders honestly as an
  // unnamed global.
  const refreshGlobalReasoning = useCallback(async () => {
    const c = clientRef.current
    if (!c || !curRef.current) return
    try {
      const res = await c.send<{ surface: Surface }>('surface.get', { id: 'settings' }, curRef.current)
      const item = res.surface?.settings?.items?.find((i) => i.key === 'reasoning')
      setGlobalReasoning(item?.value ?? '')
    } catch {
      /* the picker degrades to an unnamed global */
    }
  }, [])

  // Keyed on the session rather than fired during the bootstrap: surface.get is
  // framed by a session, and at bootstrap time none has been adopted yet — the
  // call would return early and the level would stay empty for the whole
  // connection. Re-running on a session change also keeps it right for a daemon
  // whose settings differ per workspace.
  useEffect(() => {
    if (curSess) void refreshGlobalReasoning()
  }, [curSess, refreshGlobalReasoning])

  // loadUsageSnapshot mirrors the provider's subscription picture from the
  // usage.snapshot verb.
  //
  // The pane cannot rely on ContextBreakdown.usage_windows alone: that field is
  // filled from the provider client's already-observed snapshot, and providers
  // fall into two families. Header-family clients (anthropic, codex,
  // openai-compat) record their windows off every inference response, so the
  // field is warm for free. Poll-family clients (kimi, openrouter, deepseek)
  // report nothing at all until somebody calls their usage endpoint — and until
  // this, nothing on the web ever did, so a kimi subscription rendered no
  // windows whatsoever while the TUI showed them.
  //
  // refresh=true asks the daemon to make that call. It blocks server-side on
  // the provider's GET, which is why it is a deliberate act rather than
  // something the breakdown does on its own; the fetch is TTL-cached at the
  // client (provider/usage_client.go), so calling this on every usage event
  // costs one local round-trip and at most one provider GET a minute. For a
  // header-family provider it degrades to the passive read, so it is safe to
  // call for every provider. The TUI has kept the same mirror all along
  // (modes/interactive_usage.go).
  const loadUsageSnapshot = useCallback(async (refresh: boolean) => {
    const c = clientRef.current
    const sess = curRef.current
    if (!c || !sess) return
    try {
      const r = await c.send<UsageSnapshotResult>('usage.snapshot', { refresh }, sess)
      // A refresh blocks on the provider's endpoint, which is long enough for
      // the user to switch sessions underneath it. Late arrivals belong to the
      // session that asked, so drop them rather than showing one session's
      // subscription picture under another's.
      if (curRef.current !== sess) return
      setUsageSnap(r.usage ?? null)
    } catch {
      /* older daemon, or the session group is unavailable — the breakdown's
         own windows stand in, which is exactly the pre-mirror behaviour */
    }
  }, [])

  // refreshI18n fetches the daemon's effective string catalog (base ⊕ overlay)
  // and overlays it onto the bundle, then re-renders. Run on connect and when
  // the locale changes; best-effort (the bundle stands in on failure).
  const refreshI18n = useCallback(() => {
    clientRef.current
      ?.send<{ catalog: CatalogView }>('i18n.catalog', {}, '')
      .then((r) => {
        if (!r?.catalog) return
        document.documentElement.lang = applyServerCatalog(r.catalog)
        reI18n()
      })
      .catch(() => {
        /* older server / offline — the bundle stands in */
      })
  }, [reI18n])

  const handleEvent = useCallback((ev: WireEvent) => {
    switch (ev.type) {
      case 'snapshot': {
        // A snapshot is authoritative — and one lands at the end of EVERY turn, not
        // just on open. MERGE it: keep everything above the window and everything not
        // in the live transcript at all (history paged in behind a compaction), and
        // swap in the window. Rebuilding instead is what used to remount every row and
        // silently discard the user's scrollback.
        const snap: Window = {
          epoch: ev.snapshot?.epoch ?? 0,
          base: ev.snapshot?.base ?? 0,
          total: ev.snapshot?.total ?? (ev.snapshot?.messages?.length ?? 0),
          messages: ev.snapshot?.messages ?? [],
        }
        setItems((prev) => mergeSnapshot(prev, snap, winRef.current.epoch))
        // The window we now hold. A merge KEEPS what was above the snapshot's base, so
        // our base is the lower of the two — not the snapshot's.
        const held = winRef.current
        const base = held.epoch === snap.epoch ? Math.min(held.base, snap.base) : snap.base
        winRef.current = { epoch: snap.epoch, base, total: snap.total }
        setWin(winRef.current)
        setBusy(!!ev.snapshot?.busy)
        setCurInfo(ev.snapshot?.session ?? null)
        setQueued(ev.snapshot?.queued ?? [])
        setSkills(ev.snapshot?.skills ?? [])
        if (ev.snapshot?.session?.usage) setCost(ev.snapshot.session.usage.cost_usd || 0)
        // Restore any pending approval/question so a reconnecting tab shows it.
        setPermission(ev.snapshot?.permissions?.[0] ?? null)
        setAsk(ev.snapshot?.asks?.[0] ?? null)
        return
      }
      case 'session_updated': {
        // A title settled, a rename, or a model switch — refresh the header and
        // the session-list entry live, without re-fetching.
        const info = ev.info
        if (!info) return
        // The session-list row always updates. But the header (curInfo) belongs
        // to the FRAMED session only: a session_updated rides the session's own
        // hub so it should already be for curSess, yet a mis-addressed event must
        // never overwrite the model/title of the session this tab is viewing.
        setSessions((ss) => ss.map((s) => (s.id === info.id ? { ...s, ...info } : s)))
        if (info.id && info.id !== curRef.current) return
        setCurInfo((cur) => (cur ? { ...cur, ...info } : info))
        return
      }
      case 'queue_updated':
        // Authoritative queue state from the server — replaces the optimistic
        // list so edits/cancels from any client converge.
        setQueued(ev.queued ?? [])
        return
      case 'surfaces_changed':
        // The set of panes changed (ext panel opened/closed, status appeared).
        if (paneOpenRef.current) listSurfaces()
        return
      case 'sessions_changed':
        // The session SET changed on the daemon — created, renamed, deleted, or
        // a cold one materialized. Re-list so the drawer and the board reflect
        // it; sessions.list carries fresh busy/live per row.
        if (clientRef.current) refreshSessions(clientRef.current)
        return
      case 'surface_updated':
        // A live pane's content changed; re-fetch if we're showing it.
        if (paneOpenRef.current && ev.surface_id === activeSurfaceRef.current) {
          loadSurface(activeSurfaceRef.current)
        }
        // The cached thinking level rides the settings surface, and it has to
        // follow a change made there whether or not that pane is the open one —
        // otherwise the picker keeps naming a global the user just replaced.
        if (ev.surface_id === 'settings') void refreshGlobalReasoning()
        // The board's swarm lane rides the tasks surface (the daemon diffs the
        // swarm every 800ms and pushes this) — keep it live while the board's up.
        if (viewModeRef.current === 'board' && ev.surface_id === 'tasks') {
          fetchBoardTasks()
        }
        return
      case 'auth_state': {
        // A provider login moved. It may have MOVED SOMEWHERE ELSE — a device
        // flow finishes in a browser on another device, and the daemon finds out
        // by polling — so this is the only way the panel learns it landed.
        const a = ev.auth
        if (!a) return
        if (a.kind === 'success' || a.kind === 'canceled') {
          setAuthFlow(null)
          setAuthBusy(false)
          setAuthErr('')
          if (a.kind === 'success' && a.method !== 'logout') {
            setToast(t('signed in to %s', a.provider ?? ''))
          }
        }
        if (a.kind === 'error') {
          setAuthBusy(false)
          setAuthErr(a.message ?? t('login failed'))
        }
        // The workspace drawer's provider list has no surface_updated behind it
        // (it is a plain verb, not a surface), so nothing else would move it —
        // and this event is precisely the moment it went stale.
        if (paneOpenRef.current && !curRef.current) void loadWorkspaceProviders()
        return
      }
      case 'locale_changed':
        // The daemon's UI language changed: re-fetch the client catalog (and
        // re-render), and re-fetch any open pane so server-rendered titles/labels
        // pick up the new language too.
        refreshI18n()
        if (paneOpenRef.current) {
          listSurfaces()
          loadSurface(activeSurfaceRef.current)
        }
        return
      case 'user_message': {
        // Reconcile the optimistic queued list: once the agent consumes a
        // queued message it echoes here, so drop the matching pending entry.
        const text = (ev.message?.content ?? [])
          .filter((c) => c.type === 'text')
          .map((c) => c.text)
          .join('')
        setQueued((q) => {
          const i = q.indexOf(text)
          return i < 0 ? q : [...q.slice(0, i), ...q.slice(i + 1)]
        })
        setItems((it) => applyEvent(it, ev))
        return
      }
      case 'usage':
        if (ev.cumulative) setCost(ev.cumulative.cost_usd || 0)
        // Keep the live context gauge fresh: the per-turn usage is the most
        // recent request's input+cache tokens ≈ current context size.
        if (ev.usage) {
          const u = ev.usage
          const ctx = (u.input || 0) + (u.cache_read || 0) + (u.cache_write || 0)
          setCurInfo((cur) => (cur ? { ...cur, context_tokens: ctx } : cur))
        }
        // The context/usage pane isn't signalled by surface_updated (no server
        // broadcast for it), so refresh it here when it's the open pane — the
        // gauge, cumulative cost, and windows all move on a usage event.
        if (paneOpenRef.current && activeSurfaceRef.current === 'context') {
          loadSurface('context')
          // Re-poll alongside it, or a poll-family provider's meter would stay
          // frozen at whatever it read when the pane opened. Unthrottled, like
          // the surface fetch beside it: this verb is far the cheaper of the
          // two (that one rebuilds the whole context tree), and the provider
          // GET behind it is TTL-capped at once a minute.
          void loadUsageSnapshot(true)
        }
        return
      case 'permission_request':
        setPermission(ev.permission ?? null)
        return
      case 'permission_resolved':
        setPermission((p) => (p && ev.resolved?.call_id === p.call_id ? null : p))
        return
      case 'ask_request':
        setAsk(ev.ask ?? null)
        return
      case 'ask_resolved':
        setAsk((a) => (a && ev.resolved?.ask_id === a.ask_id ? null : a))
        return
      case 'turn_start':
        setBusy(true)
        return
      case 'turn_end':
      case 'done':
        setBusy(false)
        return
      case 'error':
        setToast(ev.error ?? 'error')
        setBusy(false)
        setItems((it) => applyEvent(it, ev))
        return
      default:
        setItems((it) => applyEvent(it, ev))
    }
  }, [listSurfaces, loadSurface, loadUsageSnapshot, refreshI18n, refreshSessions, fetchBoardTasks])

  handleEventRef.current = handleEvent

  // Drive the pacer. One 16ms timer for the life of the panel: tick() is a no-op
  // on an empty queue, which is what it is for all but the seconds a reply is
  // actually streaming.
  useEffect(() => {
    const id = setInterval(() => pacerRef.current?.tick(), PACE_INTERVAL_MS)
    return () => clearInterval(id)
  }, [])

  useEffect(() => {
    const c = createClient()
    clientRef.current = c
    c.onStatus = setStatus
    c.onEvent = (sess, ev) => {
      // Worker approvals: a swarm worker parked on a tool call has its approval
      // routed to the dispatching session's card, riding that session's stream as
      // a permission_request whose `agent` names the worker. Fold every approval
      // event address-agnostically (the dispatcher may be the focused session or a
      // board sub) so the swarm lane can badge a stalled tile; a non-worker
      // approval carries no agent and is ignored inside.
      if (ev.type === 'permission_request' || ev.type === 'permission_resolved' || ev.type === 'snapshot') {
        setBoardApprovals((s) => applyBoardApproval(s, sess, ev))
      }
      // Two addresses reach this panel: the focused session, and the workspace
      // itself. Workspace events (a workspace-scoped surface changing, the
      // locale, a notice) used to arrive stamped with each live session's id —
      // which is the only reason this equality test ever saw them. They arrive on
      // their own address now, so the test has to admit it, or they vanish.
      if (sess === ADDR_WORKSPACE || sess === curRef.current) {
        pacerRef.current?.push(ev)
        return
      }
      // A board subscription (some OTHER live session, held while the board is
      // open): derive its busy for the tile, but never merge its transcript —
      // the board wants the flag, not the conversation.
      if (boardSubsRef.current.has(sess)) {
        setBoardBusy((s) => applyBoardBusy(s, sess, ev))
      }
    }
    c.onReady = async (hello) => {
      // Match the PWA's bundled catalog to the daemon's active language for an
      // instant first paint, then overlay the server's effective catalog
      // (embedded ⊕ $TERVA_HOME overlay) so an operator's translation edits show
      // on reload. Both re-render via reI18n; catalog fetch is best-effort.
      const lang = setLocale(hello?.locale ?? 'en')
      document.documentElement.lang = lang
      setCanRestart(!!hello?.features?.includes('restart'))
      setCanReadSecrets(!!hello?.groups?.includes('secrets'))
      setStageEnabled(!!hello?.features?.includes('stage'))
      canListFiles.current = !!hello?.features?.includes('files-list')
      // Whether this carrier has an upload route at all, and what it will take.
      // Gating the composer on it keeps a drop target from appearing where a
      // drop would go nowhere (a native client over a unix socket).
      setCanAttachFiles(!!hello?.features?.includes('attachments'))
      setMaxAttachmentBytes(hello?.max_attachment_bytes ?? 0)
      setCanDownloadShares(!!hello?.features?.includes('shared-files'))
      // Subscribe to the workspace itself, once per connection. It is not a
      // session: it does not change when the focused session does, and it must
      // stay subscribed even when no session is focused at all — which is the
      // whole point of it having an address. A daemon too old to know it relays
      // these events into the session subscription instead, so skipping the
      // subscribe is the correct degradation rather than a lost capability.
      if (hello?.features?.includes('workspace-events')) {
        c.fire('subscribe', null, ADDR_WORKSPACE)
      }
      // A reconnect is a fresh connection with NO server-side subscriptions, but
      // boardSubsRef still holds the previous connection's set — so when the board
      // reconcile next runs it would see want ⊆ have and skip re-subscribing the
      // tiles, leaving their busy/idle and approval indicators frozen on stale
      // data. Forget the board subs (and their busy) so the reconcile re-subscribes
      // every live tile on the new connection.
      boardSubsRef.current = new Set()
      setBoardBusy({})
      // A (re)connect may be a different build or a moved tree — refetch on
      // the next "@".
      setFileList(null)
      fileListAt.current = 0
      const ver = hello?.version ?? ''
      if (ver && verRef.current && ver !== verRef.current) {
        setToast(t('terva restarted: v%s → v%s', verRef.current, ver))
      }
      verRef.current = ver
      setServerVersion(ver)
      reI18n()
      refreshI18n()
      try {
        const res = await c.send<ModelsResult>('models.list', null, curRef.current)
        setModels(res.models ?? [])
        setLadders(res.reasoning_ladders ?? {})
      } catch {
        /* control group optional */
      }
      const list = await refreshSessions(c)
      // A `?session=` deep link (from Stage) outranks everything, but only while
      // the session still exists and only on the FIRST connect — bootNav is
      // consumed once, so a later reconnect falls back to the normal pick rather
      // than dragging you back to where you arrived.
      const linked = bootNav.session && list.some((s) => s.id === bootNav.session) ? bootNav.session : ''
      if (linked) bootNav.session = ''
      // Precedence: deep link → this TAB's remembered session → the landing.
      // Crucially NOT the server's global `current`, which every client shares
      // and which made two panels converge on one session (a model switch or
      // login in one moved the other). A fresh tab with no deep link and no
      // memory lands on the picker and adopts NOTHING. See pickBootTarget.
      const target = pickBootTarget({
        linked,
        remembered: rememberedTabSession(),
        sessionIds: list.map((s) => s.id),
      })
      if (!target) {
        // No session to resume: land on the session-focused picker.
        goToLanding()
      } else if (target === curRef.current) {
        // Reconnect to the same session: selectSession would short-circuit on the
        // unchanged id and never re-subscribe, leaving the panel connected but
        // frozen (no snapshot, no live events). Fire the subscribe explicitly —
        // the server is idempotent and replies with a fresh snapshot that resyncs.
        c.fire('subscribe', null, target)
      } else {
        selectSession(target)
      }
      // `?pane=` opens a surface on arrival — this is what makes Stage's
      // "inspect context" a single hop instead of a hunt. Consumed like the
      // session, so it fires once and not on every reconnect.
      //
      // The two setters rather than openPane: openPane is declared below this
      // effect, so naming it in the dependency array would evaluate it in its
      // temporal dead zone during render. These are what it wraps, and useState
      // setters are stable.
      if (bootNav.pane) {
        setActiveSurface(bootNav.pane)
        setPaneOpen(true)
        bootNav.pane = ''
      }
    }
    c.connect()
    return () => c.close()
  }, [newSession, refreshSessions, selectSession, goToLanding, refreshI18n])

  // While the board is open, re-list on a slow cadence: a tile's busy/live moves
  // on a session's turn boundaries, which don't emit sessions_changed, so the
  // board polls sessions.list (a cheap disk scan + live overlay — it never
  // materializes a cold session) to stay fresh without a per-tile subscription.
  // Off entirely in focus mode, where the subscribed session is authoritative.
  useEffect(() => {
    if (viewMode !== 'board') {
      clearBoardSubs() // leaving the board: drop its live subscriptions
      return
    }
    // Gated on the socket being OPEN, and not on viewMode alone — the same trap
    // the workflow lane below already documents, which this effect had too.
    //
    // viewMode is persisted, so a panel reopened on the board runs this on its
    // FIRST render, while the socket is still connecting. Client.send rejects
    // "not connected" immediately there; fetchBoardTasks catches that and leaves
    // the lane empty. With viewMode as the only real dependency nothing ever
    // re-ran it: the swarm lane sat empty for the life of the page, and (once it
    // could tell empty from unloaded) would have gone on to state positively
    // that no agents were running. sessions.list survived only because a 4s poll
    // happens to re-issue it; the tasks surface has no such poll.
    if (status !== 'open') return
    if (clientRef.current) refreshSessions(clientRef.current)
    fetchBoardTasks() // the swarm lane; refreshed thereafter by surface_updated("tasks")
    // The 4s poll now only refreshes tile metadata (cost, message counts) and
    // catches live→cold; busy comes live from the subscriptions below.
    const id = setInterval(() => {
      if (clientRef.current) refreshSessions(clientRef.current)
    }, 4000)
    return () => clearInterval(id)
  }, [viewMode, status, refreshSessions, fetchBoardTasks, clearBoardSubs])

  // The same re-list for the DRAWER, which groups its rows by busy/idle/cold and
  // so needs those flags to be true while it is open.
  //
  // Deliberately its own effect rather than a widened gate on the board's: that
  // one also owns clearBoardSubs() and fetchBoardTasks(), and opening the drawer
  // in focus mode must not touch either. In focus mode the board effect returns
  // immediately, so without this nothing re-lists at a turn boundary — a session
  // that started working while you were reading would sit in COLD until some
  // unrelated sessions_changed happened to fire.
  //
  // sessions.list is a disk scan plus a live overlay and never materializes a
  // cold session (ctrlproto SessionInfo.Live), so polling it cannot wake
  // anything up. Gated on the socket being OPEN and not on `drawer` alone — a
  // send on a still-connecting socket rejects, and the drawer would then hold
  // whatever the list said before it opened.
  useEffect(() => {
    if (!drawer || status !== 'open') return
    if (clientRef.current) refreshSessions(clientRef.current)
    const id = setInterval(() => {
      if (clientRef.current) refreshSessions(clientRef.current)
    }, 4000)
    return () => clearInterval(id)
  }, [drawer, status, refreshSessions])

  // The workflow lane on its own, slower cadence, and only while something could
  // still be moving.
  //
  // There is no broadcast to hang this on: a run is written by a separate
  // foreground process the daemon knows nothing about, so polling is the only
  // way the lane learns anything. It is also not free — each poll re-reads every
  // run's journal to count what completed — so it stops as soon as every run has
  // closed. A run that starts while the board sits open needs the lane's Refresh,
  // which is the honest trade until a run can announce itself.
  // Gated on `status` and not just on viewMode, because a panel that BOOTS into
  // board mode (the view is persisted) runs this effect before the socket is
  // open. A send on a still-connecting socket rejects, and with viewMode as the
  // only dependency nothing would ever change again — the lane would sit empty
  // for the life of the page. Same shape that once broke the persona shelves.
  useEffect(() => {
    if (viewMode !== 'board' || !workflowsOn || status !== 'open') return
    fetchWorkflowRuns()
    const id = setInterval(() => {
      if (wfLiveRef.current) fetchWorkflowRuns()
    }, 10000)
    return () => clearInterval(id)
  }, [viewMode, workflowsOn, status, fetchWorkflowRuns])

  // Keep the board's live subscriptions in step with the live tiles: when the
  // set of live sessions changes (a materialize, a delete, a new spawn), re-aim
  // the subscriptions so busy stays streamed for exactly the tiles on screen.
  useEffect(() => {
    if (viewMode !== 'board') return
    reconcileBoardSubs(sessions.filter((s) => s.live).map((s) => s.id))
  }, [viewMode, sessions, reconcileBoardSubs])

  // Restart the daemon. A successful restart acks first and re-execs a beat
  // later, so the client auto-reconnects to the new build. Only a real failure
  // rejects here — an unsupported platform, a go-run build, a failed preflight,
  // a disabled capability — so surface those as a toast rather than swallowing
  // them (a deferred exec failure arrives separately as a broadcast notice).
  const restart = useCallback(() => {
    clientRef.current?.send('control.restart', null, '').catch((e: unknown) => {
      const msg = restartRejection(e)
      if (msg !== null) setToast(t('restart failed: %s', msg))
    })
  }, [])

  // Trust / untrust the workspace (control-group verbs). Workspace-global, so no
  // session id; the daemon brings project extensions/lore/permission rules live
  // (or tears them down) across every open session and pushes a session_updated
  // that refreshes the trust-gated panes.
  const trustWorkspace = useCallback((trust: boolean) => {
    const c = clientRef.current
    if (!c) return
    c.send(trust ? 'control.trust' : 'control.untrust', trust ? { parent: false } : null, '').catch((e) =>
      setToast(String(e)),
    )
  }, [])

  // sendPrompt dispatches a turn, optionally with pasted images and staged file
  // attachments. Returns true when it consumed the input (so the composer can
  // clear). Both kinds of attachment ride only the prompt path — the queue verb
  // is text-only server-side — so a send carrying either while busy is refused
  // rather than silently dropping them on the floor.
  const sendPrompt = useCallback((text: string, images?: ImageAttachment[], attachments?: FileAttachment[]): boolean => {
    const c = clientRef.current
    const hasImages = !!images && images.length > 0
    const hasFiles = !!attachments && attachments.length > 0
    if (!c || !curRef.current || (!text.trim() && !hasImages && !hasFiles)) return false
    if (busyRef.current) {
      if (hasImages || hasFiles) {
        setToast(t('Finish the current turn before attaching files (the queue is text-only).'))
        return false
      }
      setQueued((q) => [...q, text])
      c.fire('queue', { text }, curRef.current)
      return true
    }
    setBusy(true)
    const params: { text: string; images?: unknown[]; attachments?: { id: string }[] } = { text }
    if (hasImages) params.images = images!.map((im) => ({ mime_type: im.mime, data: im.data }))
    // Only the id: the daemon resolves name, type, and size from what it
    // actually wrote, so nothing the client believes about the file is trusted.
    if (hasFiles) params.attachments = attachments!.map((f) => ({ id: f.id }))
    c.fire('prompt', params, curRef.current)
    return true
  }, [])

  // stageFile uploads to the session the composer is currently on. The concrete
  // id, never the empty "current session" shorthand: staging is keyed by session
  // directory, and a prompt frame's blank sess resolves daemon-side to something
  // the upload would not have matched.
  const stageFile = useCallback((f: File) => uploadFile(f, curRef.current), [])

  const decide = useCallback((callID: string, d: Decision) => {
    clientRef.current?.fire('approve', { call_id: callID, decision: d }, curRef.current)
    setPermission(null)
  }, [])

  const answer = useCallback((askID: string, answers: { answer: string; note?: string }[]) => {
    // `answers` is the set, one per question in the order asked; `answer`
    // mirrors the first so a daemon built before question sets still
    // resolves a one-question ask instead of reading an empty reply.
    clientRef.current?.fire(
      'answer',
      { ask_id: askID, answer: answers[0] ?? { answer: '' }, answers },
      curRef.current,
    )
    setAsk(null)
  }, [])

  // The session's own thinking depth. '' clears the override and puts the
  // session back under the global setting — a real choice, not a no-op, which
  // is why it is sent rather than skipped.
  const setSessionReasoning = useCallback((level: string) => {
    clientRef.current?.fire('models.reasoning', { level }, curRef.current)
  }, [])

  const switchModel = useCallback((id: string, provider?: string) => {
    // provider qualifies the id: model ids are not globally unique across
    // providers, and the daemon may hold a credential for only one of them.
    clientRef.current?.fire('models.switch', provider ? { model: id, provider } : { model: id }, curRef.current)
    setModels((ms) => ms.map((m) => ({ ...m, current: m.id === id })))
  }, [])

  // Queue edit/cancel: replace the whole pending queue on the server (which
  // re-broadcasts it), and update optimistically for instant feedback.
  const setQueueList = useCallback((next: string[]) => {
    setQueued(next)
    clientRef.current?.fire('queue.set', { texts: next }, curRef.current)
  }, [])
  const cancelQueued = useCallback(
    (i: number) => setQueueList(queued.filter((_, idx) => idx !== i)),
    [queued, setQueueList],
  )
  const editQueued = useCallback(
    (i: number, text: string) => {
      const t = text.trim()
      setQueueList(t ? queued.map((v, idx) => (idx === i ? t : v)) : queued.filter((_, idx) => idx !== i))
    },
    [queued, setQueueList],
  )

  const openPane = useCallback((id: string) => {
    setActiveSurface(id)
    setPaneOpen(true)
  }, [])

  const surfaceAction = useCallback((id: string, action: string, args?: Record<string, string>) => {
    clientRef.current?.fire('surface.action', { id, action, args }, curRef.current)
  }, [])

  // --- provider login (the auth group; only served when the daemon says so) ---
  //
  // Every call here is addressed to no session. A credential belongs to the
  // daemon, not to a conversation, and the flow may well complete while no
  // session is in focus — the auth_state event that finishes it rides the
  // workspace address for exactly that reason.
  const startLogin = useCallback(async (provider: string, method: string) => {
    const c = clientRef.current
    if (!c) return
    setAuthErr('')
    setAuthBusy(true)
    try {
      setAuthFlow(await c.send<AuthFlowStep>('auth.login.start', { provider, method }, ''))
    } catch (e) {
      setAuthErr(authMessage(e))
    } finally {
      setAuthBusy(false)
    }
  }, [])

  const submitLogin = useCallback(
    async (values: Record<string, string>) => {
      const c = clientRef.current
      const flow = authFlow?.flow
      if (!c || !flow) return
      setAuthErr('')
      setAuthBusy(true)
      try {
        // The one call in this client that carries a secret. It goes one way.
        await c.send('auth.login.submit', { flow, values }, '')
        setAuthFlow(null)
        // The newly logged-in provider's models should appear at once, not only
        // after the next reconnect — refresh the catalog now. Non-fatal: the
        // login already succeeded.
        try {
          const res = await c.send<ModelsResult>('models.list', null, curRef.current)
          setModels(res.models ?? [])
          setLadders(res.reasoning_ladders ?? {})
        } catch {
          /* a models refresh failure does not undo the login */
        }
      } catch (e) {
        // A refusal here is usually the provider rejecting the credential — a
        // mistyped key, a dead code — so it is the user's to fix, and the
        // daemon's message is more useful than anything we could invent.
        setAuthErr(authMessage(e))
      } finally {
        setAuthBusy(false)
      }
    },
    [authFlow],
  )

  const cancelLogin = useCallback(() => {
    const flow = authFlow?.flow
    if (flow) clientRef.current?.fire('auth.login.cancel', { flow }, '')
    setAuthFlow(null)
    setAuthErr('')
    setAuthBusy(false)
  }, [authFlow])

  const logoutProvider = useCallback(async (provider: string) => {
    const c = clientRef.current
    if (!c) return
    setAuthErr('')
    try {
      await c.send('auth.logout', { provider }, '')
    } catch (e) {
      setAuthErr(authMessage(e))
    }
  }, [])

  // Forgets a named endpoint outright — its config entry and any key stored under
  // it. Deliberately not routed through logout: that clears a secret and leaves the
  // provider there to sign back into, while this deletes the operator's only record
  // of which machine, which port, which context window.
  const removeEndpoint = useCallback(async (id: string) => {
    const c = clientRef.current
    if (!c) return
    setAuthErr('')
    try {
      await c.send('auth.endpoint.remove', { id }, '')
    } catch (e) {
      setAuthErr(authMessage(e))
    }
  }, [])

  // Per-model settings: the daemon describes them, we render whatever it sends.
  const openModelParams = useCallback(async (provider: string, id: string) => {
    const c = clientRef.current
    if (!c) return
    setModelParamsErr('')
    try {
      const v = await c.send<ModelParamsView>('models.params', { provider, model: id }, '')
      setModelParams(v)
    } catch (e) {
      setToast(authMessage(e))
    }
  }, [])

  const saveModelParams = useCallback(
    async (values: Record<string, string>) => {
      const v = modelParams
      const c = clientRef.current
      if (!v || !c) return
      setModelParamsBusy(true)
      setModelParamsErr('')
      try {
        await c.send('models.params.set', { provider: v.provider, model: v.model, values }, '')
        setModelParams(null)
        setPickerOpen(false)
        setToast(t('saved settings for %s', `${v.provider}/${v.model}`))
      } catch (e) {
        // Kept open, with the daemon's own words: it names the setting that was
        // wrong, and closing the form would take that away along with the typing.
        setModelParamsErr(authMessage(e))
      } finally {
        setModelParamsBusy(false)
      }
    },
    [modelParams],
  )

  const resetModelParams = useCallback(async () => {
    const v = modelParams
    const c = clientRef.current
    if (!v || !c) return
    setModelParamsBusy(true)
    setModelParamsErr('')
    try {
      await c.send('models.params.reset', { provider: v.provider, model: v.model }, '')
      setModelParams(null)
      setPickerOpen(false)
      setToast(t('reset %s to its defaults', `${v.provider}/${v.model}`))
    } catch (e) {
      setModelParamsErr(authMessage(e))
    } finally {
      setModelParamsBusy(false)
    }
  }, [modelParams])

  // Lazily fetch a context node's content/children on expand (context.node).
  const fetchNode = useCallback(
    (id: string, op?: string): Promise<ContextNode> =>
      clientRef.current
        ? clientRef.current.send<{ node: ContextNode }>('context.node', { id, op }, curRef.current).then((r) => r.node)
        : Promise.reject(new Error('not connected')),
    [],
  )


  // Usage resets (codex banked resets): list is read-only; consume spends a
  // scarce credit and is only ever reached from the section's explicit confirm.
  const listResets = useCallback(
    (): Promise<ResetsListResult> =>
      clientRef.current
        ? clientRef.current.send<ResetsListResult>('usage.resets.list', null, curRef.current)
        : Promise.reject(new Error('not connected')),
    [],
  )
  const consumeReset = useCallback(
    (id: string): Promise<ResetConsumeResult> =>
      clientRef.current
        ? clientRef.current.send<ResetConsumeResult>('usage.resets.consume', { id }, curRef.current)
        : Promise.reject(new Error('not connected')),
    [],
  )

  // Client-side ephemeral note (help / usage hints) — an in-stream notice that
  // isn't sent anywhere and is dropped on the next snapshot, like server notices.
  const localNotice = useCallback((text: string) => {
    setItems((it) => [...it, { kind: 'notice', id: 'ln-' + Date.now(), level: 'info', text }])
  }, [])

  // Page in the turns a compaction folded away (conversation.reveal), so the
  // scrollback keeps them instead of losing them to what is, after all, only a
  // context-management act. The revealed span never overlaps what we already have —
  // the daemon subtracts the tail its checkpoint kept, and a Go test pins that — so
  // this splices, never merges.
  // Page in the part of the LIVE transcript the window did not carry. Distinct from
  // revealTurns below, and the distinction is the shape of the whole system: this walks
  // within the transcript the model still has, served from the daemon's memory; reveal
  // goes BEHIND a compaction to turns it no longer has, read from the session file.
  // Scrolling up runs this until base is 0, then meets the compaction divider.
  const loadEarlier = useCallback(() => {
    const c = clientRef.current
    const held = winRef.current
    if (!c || loadingEarlierRef.current || held.base <= 0) return
    loadingEarlierRef.current = true
    setLoadingEarlier(true)
    c.send<{ epoch: number; base: number; total: number; messages: WireMessage[] }>(
      'conversation.history',
      { before: held.base, epoch: held.epoch },
      curRef.current,
    )
      .then((page) => {
        // The daemon refuses a stale epoch rather than indexing into a transcript that
        // has been replaced, so a page that arrives is one we can trust to belong here.
        if (page.epoch !== winRef.current.epoch) return
        setItems((it) => prependHistory(it, page))
        winRef.current = { ...winRef.current, base: page.base, total: page.total }
        setWin(winRef.current)
      })
      .catch((e) => {
        // A conflict means the conversation was compacted or cleared while we scrolled;
        // the next snapshot rebuilds us at the new epoch, so there is nothing to do but
        // say so. Anything else is worth the same one line.
        localNotice(t('could not load earlier messages: %s', String(e)))
      })
      .finally(() => {
        loadingEarlierRef.current = false
        setLoadingEarlier(false)
      })
  }, [localNotice])

  const revealTurns = useCallback(
    (item: Divider) => {
      const c = clientRef.current
      if (!c || revealingRef.current) return
      revealingRef.current = item.id
      setRevealingID(item.id)
      c.send<{ messages: WireMessage[]; prev_ordinal: number; prev_clear?: boolean }>(
        'conversation.reveal',
        { ordinal: item.ordinal },
        curRef.current,
      )
        .then((r) => {
          const span: RevealSpan = {
            messages: r.messages ?? [],
            prevOrdinal: r.prev_ordinal,
            // A /clear behind this checkpoint ends the automatic walk: the divider we
            // mint for it offers the crossing, and the user has to mean it.
            prevClear: r.prev_clear,
          }
          // No cache to keep it in any more. Revealed rows are marked `history` — not
          // in the live transcript — and mergeSnapshot preserves them, so the turn-end
          // snapshot no longer sweeps them away and nothing has to put them back.
          setItems((it) => spliceRevealed(it, item.id, span))
        })
        .catch((e) => {
          // An ephemeral session has no file to read back, and an older daemon has
          // no such method. Neither is the user's fault. Retire the control rather
          // than leave it inviting a click that cannot work, and say why once.
          setItems((it) =>
            it.map((i) =>
              i.id === item.id && (i.kind === 'compaction' || i.kind === 'clear') ? { ...i, revealed: true } : i,
            ),
          )
          localNotice(t('earlier turns are not available for this session: %s', String(e)))
        })
        .finally(() => {
          revealingRef.current = ''
          setRevealingID('')
        })
    },
    [localNotice],
  )

  // User-driven slash commands typed in the composer. Distinct from the
  // extension Commands pane (buttons): these are operator chrome (compact,
  // skill, model, …). Built each render so descriptions track the active locale;
  // the set is tiny. See onSubmit for dispatch and Composer for the autocomplete.
  const slashCommands: SlashCommand[] = [
    {
      name: 'compact',
      desc: t('Summarize the conversation to reclaim context'),
      run: () => {
        const label = t('Compacting…')
        setToast(label)
        clientRef.current
          ?.send('compact', null, curRef.current)
          // Compact is synchronous server-side, so the resp lands after the
          // transcript is replaced: clear the sticky progress toast on ack.
          // Guard on the label so a newer toast set meanwhile isn't clobbered.
          .then(() => setToast((cur) => (cur === label ? '' : cur)))
          .catch((e) => setToast(String(e)))
      },
    },
    {
      name: 'clear',
      desc: t('Wipe the conversation (no summary)'),
      run: () => {
        clientRef.current?.send('clear', null, curRef.current).catch((e) => setToast(String(e)))
      },
    },
    {
      name: 'skill',
      arg: '<name> [task]',
      desc: t('Invoke a skill by name'),
      run: (arg) => {
        const m = /^(\S+)\s*([\s\S]*)$/.exec(arg.trim())
        if (!m) {
          setToast(t('Usage: /skill <name> [task]'))
          return
        }
        const name = m[1]
        const task = m[2].trim()
        // Prime the model to load the skill (it then calls the `skill` tool),
        // mirroring the TUI's /skill directive.
        sendPrompt(task ? `Use the "${name}" skill for: ${task}` : `Use the "${name}" skill.`)
      },
    },
    {
      name: 'model',
      arg: '[id]',
      desc: t('Switch model, or open the model picker'),
      run: (arg) => {
        const id = arg.trim()
        if (!id) {
          setPickerOpen(true)
          return
        }
        // A bare id can exist under several providers (an api-key twin and a
        // subscription one); prefer the current provider's entry so /model
        // never silently hops backends the way a global first-match would.
        const cur = models.find((m) => m.current)
        const match =
          models.find((m) => m.id === id && m.provider === cur?.provider) ||
          models.find((m) => m.id === id)
        switchModel(id, match?.provider)
      },
    },
    {
      name: 'reasoning',
      arg: '[level]',
      desc: t("Set this session's thinking depth, or open the picker"),
      run: (arg) => {
        const lv = arg.trim().toLowerCase()
        if (!lv) {
          setReasoningOpen(true)
          return
        }
        // 'inherit' is spelled out here rather than mapped to '' by the caller:
        // the daemon accepts both, and typing the word is how someone clears an
        // override without guessing that an empty argument would do it.
        setSessionReasoning(lv === 'inherit' || lv === 'default' ? '' : lv)
      },
    },
    {
      name: 'login',
      desc: t('Open the Providers pane to sign in'),
      // The TUI's /login opens a dialog; here the Providers pane IS that dialog,
      // so this opens the pane rather than inventing a second login surface. Like
      // the tab, it opens even when the daemon serves no login (no
      // --web-allow-login): the pane then explains why it is read-only, which is
      // exactly when someone types /login and needs an answer.
      run: () => openPane('providers'),
    },
    {
      name: 'context',
      desc: t('Open the usage & context breakdown'),
      run: () => openPane('context'),
    },
    {
      name: 'raati',
      desc: t('Open the deliberation board'),
      run: () => openPane('raati'),
    },
    {
      name: 'new',
      desc: t('Start a new session'),
      run: () => void newSession(),
    },
    {
      name: 'help',
      desc: t('List slash commands'),
      run: () => localNotice(t('Slash commands: /compact, /clear, /skill, /model, /login, /context, /raati, /new. Type / in the composer to autocomplete.')),
    },
  ]

  // onSubmit is the composer's send path: a leading "/<known command>" is
  // intercepted and run; anything else (including a message that merely starts
  // with "/") is sent as a normal prompt. Returns true when the input was
  // consumed so the composer clears its text + attachments. Slash commands are
  // only recognized when nothing is attached (a send carrying an image or a
  // staged file is always a prompt — a slash command would discard it).
  const onSubmit = (text: string, images?: ImageAttachment[], attachments?: FileAttachment[]): boolean => {
    const trimmed = text.trim()
    if (trimmed.startsWith('/') && !(images && images.length) && !(attachments && attachments.length)) {
      const sp = trimmed.indexOf(' ')
      const head = (sp === -1 ? trimmed.slice(1) : trimmed.slice(1, sp)).toLowerCase()
      const cmd = slashCommands.find((c) => c.name === head)
      if (cmd) {
        cmd.run(sp === -1 ? '' : trimmed.slice(sp + 1))
        return true
      }
    }
    return sendPrompt(text, images, attachments)
  }

  // A usage snapshot belongs to the session that produced it. Clear it when the
  // session changes — declared ahead of the pane refresh below so the wipe
  // lands before the re-fetch — or a provider that reports nothing would
  // inherit the previous session's windows through statusWindows' fallback.
  useEffect(() => {
    setUsageSnap(null)
  }, [curSess])

  // Refresh the pane when it opens, the active surface changes, or the session
  // changes; live panes also re-fetch on surface_updated (see handleEvent).
  useEffect(() => {
    if (!paneOpen || !curSess) return
    listSurfaces()
    loadSurface(activeSurface)
    // Opening the usage pane is the deliberate "show me where I stand" act, so
    // it is the one that pays for a provider fetch — the same trigger the TUI
    // uses for /usage.
    if (activeSurface === 'context') void loadUsageSnapshot(true)
  }, [paneOpen, activeSurface, curSess, listSurfaces, loadSurface, loadUsageSnapshot])

  // ...and the workspace drawer when it opens with no session behind it.
  // auth.providers is addressed to nothing on purpose (serve.go ignores Sess: a
  // credential belongs to the daemon, not to a conversation), which is what lets
  // this work on a landing that has not created a session yet.
  useEffect(() => {
    if (!paneOpen || curSess) return
    void loadWorkspaceProviders()
  }, [paneOpen, curSess, loadWorkspaceProviders])

  // The posture is fetched on demand rather than beside the providers above:
  // it walks the credential home, the component registry and every component's
  // state file, so a drawer opened to read "what version is this" should not
  // pay for it.
  useEffect(() => {
    if (!paneOpen || curSess || wsTab !== 'secrets' || !canReadSecrets) return
    void loadWorkspaceSecrets()
  }, [paneOpen, curSess, wsTab, canReadSecrets, loadWorkspaceSecrets])

  const rename = useCallback(
    async (s: SessionInfo) => {
      const title = window.prompt(t('Rename session'), s.title || '')
      if (title == null) return
      const c = clientRef.current
      if (!c) return
      await c.send('sessions.rename', { title }, s.id).catch((e) => setToast(String(e)))
      await refreshSessions(c)
    },
    [refreshSessions],
  )

  const generateTitle = useCallback(
    async (s: SessionInfo) => {
      const c = clientRef.current
      if (!c) return
      setToast(t('Generating title…'))
      try {
        const r = await c.send<{ title: string }>('sessions.generate_title', null, s.id)
        setToast(t('Titled: %s', r.title))
      } catch (e) {
        setToast(String(e))
        return
      }
      await refreshSessions(c)
    },
    [refreshSessions],
  )

  // Archive: the session leaves every list without leaving the disk. No confirm —
  // it is reversible, and confirming a reversible act trains people to click
  // through the one that is not.
  const archive = useCallback(
    async (s: SessionInfo) => {
      const c = clientRef.current
      if (!c) return
      try {
        await c.send('sessions.archive', null, s.id)
      } catch (e) {
        setToast(String(e))
        return
      }
      setArchived(null) // the archive changed; re-fetch on next open
      await refreshSessions(c)
      setToast(t('Archived — find it under Archived in the session drawer'))
      // Archiving the session you're in returns you to the landing, exactly as
      // deleting it does: the session this tab held is no longer listed.
      if (s.id === curRef.current) goToLanding()
    },
    [goToLanding, refreshSessions],
  )

  const [archived, setArchived] = useState<ArchivedSessionInfo[] | null>(null)
  const [showArchived, setShowArchived] = useState(false)

  const loadArchived = useCallback(async () => {
    const c = clientRef.current
    if (!c) return
    try {
      const r = await c.send<{ sessions: ArchivedSessionInfo[] }>('sessions.archived', null, '')
      setArchived(r.sessions ?? [])
    } catch (e) {
      setToast(String(e))
      setArchived([])
    }
  }, [])

  const toggleArchived = useCallback(() => {
    setShowArchived((on) => {
      if (!on) void loadArchived()
      return !on
    })
  }, [loadArchived])

  const restore = useCallback(
    async (id: string) => {
      const c = clientRef.current
      if (!c) return
      try {
        await c.send('sessions.restore', { id }, '')
      } catch (e) {
        setToast(String(e))
        return
      }
      await loadArchived()
      await refreshSessions(c)
      setToast(t('Restored — it is back in the session list'))
    },
    [loadArchived, refreshSessions],
  )

  const del = useCallback(
    async (s: SessionInfo) => {
      if (!window.confirm(t('Delete “%s”?', s.title || s.id))) return
      const c = clientRef.current
      if (!c) return
      await c.send('sessions.delete', null, s.id).catch((e) => setToast(String(e)))
      await refreshSessions(c)
      // Deleting the session you're in returns you to the landing picker rather
      // than auto-adopting another session (consistent with boot: a tab only
      // holds a session it explicitly opened).
      if (s.id === curRef.current) goToLanding()
    },
    [goToLanding, refreshSessions],
  )

  const current = sessions.find((s) => s.id === curSess)
  // The board/picker filter by include/exclude group. The chip list is the user's
  // session groups plus a DERIVED `stage` group (every immersive session, from
  // its experience flag) — the one the filter hides by default. Its members are
  // synthesized here, never stored. filterGroups feeds the filter bar;
  // sessionGroups (user-only) still feeds the per-session assign menu.
  const stageGroup = stageSystemGroup(sessions, t('Stage'))
  const filterGroups = stageGroup ? [stageGroup, ...sessionGroups] : sessionGroups
  const shownSessions = applyGroupFilter(sessions, filterGroups, sessionFilter, (s) => s.id)
  // The focused session's tile reads the focus view's own busy state (its
  // subscription lives there, not in boardSubsRef); the rest come from the
  // board subscriptions. One merged map for the tiles.
  const liveBusy = curSess ? { ...boardBusy, [curSess]: busy } : boardBusy
  const curModel = models.find((m) => m.current)
  // The session's own reasoning override, '' when it follows the global. Read
  // from the session row rather than kept locally so a change made in another
  // client (or the TUI) shows up here through session_updated.
  const curSessReasoning = sessions.find((s) => s.id === curSess)?.reasoning ?? ''
  const ctxTok = curInfo?.context_tokens ?? 0
  const ctxWin = curInfo?.context_window ?? 0
  const ctxPct = ctxWin > 0 ? Math.min(100, (ctxTok / ctxWin) * 100) : -1

  return (
    <div class={`app${paneOpen ? ' with-pane' : ''}`}>
      <header class="topbar">
        <button class="icon" title={t('Sessions')} onClick={() => setDrawer((d) => !d)}>
          ☰
        </button>
        {/* The wordmark doubles as a home button: clicking it leaves the focused
            session for the landing (goToLanding also forgets the tab's session, so
            a reload stays on the landing). Only interactive when a session IS
            focused — on the landing it is already home, so it renders as plain
            text rather than a button that navigates nowhere. */}
        {curSess ? (
          <button class="ident ident-home" title={t('Back to landing')} onClick={goToLanding}>
            <span class="persona">{curInfo?.persona || 'terva'}</span>
            <span class="sess-title">{current?.title || curInfo?.title || t('new chat')}</span>
          </button>
        ) : (
          <div class="ident">
            <span class="persona">{curInfo?.persona || 'terva'}</span>
            <span class="sess-title">{current?.title || curInfo?.title || t('new chat')}</span>
          </div>
        )}
        {/* Session-specific controls (info, board/focus, model) act on the
            framed session, so they are hidden on the session-less landing —
            in particular the model button would otherwise fire models.switch
            with an empty session id, which the server resolves to the global
            latest session (the concurrent-client bug this landing fixes). */}
        {curSess && (
          <button class="icon" title={t('Session info')} onClick={() => setInfoOpen((v) => !v)}>
            ⓘ
          </button>
        )}
        <button
          class="icon"
          title={t('Tool calls: %s — click to cycle (box / grouped / minimal / hidden)', toolView)}
          onClick={cycleToolView}
        >
          {toolView === 'full' ? '▤' : toolView === 'grouped' ? '⊟' : toolView === 'minimal' ? '≡' : '⌀'}
        </button>
        {/* The same control opens two different rails, so it has to say which:
            on the landing it is the workspace, not a session's panes. */}
        <button
          class={`icon${paneOpen ? ' on' : ''}`}
          title={curSess ? t('Panes (usage, settings, extensions)') : t('Workspace (providers, about)')}
          onClick={() => (paneOpen ? setPaneOpen(false) : openPane(activeSurface))}
        >
          ⊞
        </button>
        {curSess && (
          <button
            class={`icon${viewMode === 'board' ? ' on' : ''}`}
            title={t('Sessions board / focus view')}
            onClick={() =>
              setViewMode((m) => {
                const next = m === 'board' ? 'focus' : 'board'
                localStorage.setItem('terva_viewmode', next)
                return next
              })
            }
          >
            ▦
          </button>
        )}
        {curSess && (
          <button class="model-btn" title={t('Switch model')} onClick={() => setPickerOpen(true)}>
            {curModel ? (curModel.favorite ? '★ ' : '') + modelLabel(curModel) : t('model')}
          </button>
        )}
        {/* Always present once there is a session to set it ON. Gating the
            button on the label being non-empty meant it appeared only after an
            override already existed — and the picker was the thing that set
            one, so the first override could never be made here at all. The
            glyph is the fallback for a workspace with no global level either. */}
        {curSess && (
          <button
            class="reasoning-btn"
            title={t("Reasoning for this session")}
            onClick={() => setReasoningOpen(true)}
          >
            {reasoningLabel(curSessReasoning, globalReasoning) || '◐'}
          </button>
        )}
        {ctxPct >= 0 && ctxTok > 0 && (
          <button
            class={`ctx-chip${ctxPct >= 85 ? ' hot' : ctxPct >= 70 ? ' warn' : ''}`}
            title={t('context: %s / %s tokens — click for breakdown', ctxTok.toLocaleString(), ctxWin.toLocaleString())}
            onClick={() => openPane('context')}
          >
            <span class="ctx-chip-bar">
              <span class="ctx-chip-fill" style={{ width: ctxPct + '%' }} />
            </span>
            <span class="ctx-chip-pct">{Math.round(ctxPct)}%</span>
          </button>
        )}
        {cost > 0 && (
          <button class="cost" title={t('Usage')} onClick={() => openPane('context')}>
            ${cost.toFixed(4)}
          </button>
        )}
        {stageEnabled && (
          <a
            class="icon"
            href={stageHref(curInfo?.experience ? curSess : '')}
            title={t('Open Stage')}
            aria-label={t('Open Stage')}
          >
            🎭
          </a>
        )}
        {/* Colour-only was the whole of this signal: no text, no aria-label, so
            it read as nothing at all to a colourblind or screen-reader user.
            The visible message lives in the banner below (the top bar is
            already wrapping to two rows on a phone and has no room for a
            word); this makes the indicator itself announce what it means. */}
        <span class={`dot ${status}`} role="img" title={status} aria-label={t('connection: %s', status)} />
      </header>

      {/* Under the bar, in flow: the one place a not-connected panel says so out
          loud. Silent on a fast local connect (it waits out a grace period),
          immediate once the connection has been live and then dropped. */}
      <ConnectionBanner status={status} />

      {infoOpen && (
        <SessionInfoView
          info={curInfo}
          cost={cost}
          version={serverVersion}
          onClose={() => setInfoOpen(false)}
          onContext={() => {
            setInfoOpen(false)
            openPane('context')
          }}
        />
      )}

      {wfOpen && (
        <WorkflowRunDetail
          view={wfView}
          err={wfErr}
          onClose={() => {
            setWfOpen('')
            setWfView(null)
            setWfErr('')
          }}
        />
      )}

      {/* The settings form OWNS the overlay while it is open. Leaving the model
          list behind it invites picking a second model mid-edit, and the typing
          would go to whichever one the form still thought it was editing. */}
      {pickerOpen && modelParams ? (
        <div class="modal-scrim" onClick={() => setModelParams(null)}>
          <div class="modal picker" onClick={(e) => e.stopPropagation()}>
            <ModelParamsForm
              view={modelParams}
              busy={modelParamsBusy}
              error={modelParamsErr}
              onSave={saveModelParams}
              onReset={resetModelParams}
              onCancel={() => setModelParams(null)}
            />
          </div>
        </div>
      ) : reasoningOpen ? (
        <ReasoningPick
          override={curSessReasoning}
          global={globalReasoning}
          modelDefault={curModel?.default_reasoning}
          maxIsNative={curModel?.max_native}
          rungs={curModel?.ladder ? ladders[curModel.ladder] : undefined}
          onPick={setSessionReasoning}
          onClose={() => setReasoningOpen(false)}
        />
      ) : pickerOpen ? (
        <ModelPicker
          groups={modelGroups}
          favorites={favorites}
          current={curModel?.id}
          onSwitch={(id, provider) => {
            switchModel(id, provider)
            setPickerOpen(false)
          }}
          onToggleFavorite={favoriteModel}
          onSetDefault={setDefaultModel}
          onEdit={openModelParams}
          onClose={() => setPickerOpen(false)}
        />
      ) : null}

      {drawer && (
        <SessionPicker
          sessions={shownSessions}
          current={curSess}
          liveBusy={liveBusy}
          onSelect={selectSession}
          onNew={() => newSession()}
          // Only offer "back to landing" when there is a focused session to
          // leave; from the landing the drawer is already home.
          onGoLanding={curSess ? goToLanding : undefined}
          onRename={rename}
          onGenerateTitle={generateTitle}
          onDelete={del}
          onArchive={archive}
          archived={archived}
          showArchived={showArchived}
          onToggleArchived={toggleArchived}
          onRestore={restore}
          onClose={() => setDrawer(false)}
          groups={sessionGroups}
          filterGroups={filterGroups}
          filter={sessionFilter}
          onCycleGroup={cycleSessionGroup}
          onToggleGroup={toggleSessionGroup}
          onCreateGroup={createSessionGroup}
        />
      )}

      <div class="workspace">
        <div class="main">
          {!curSess ? (
            // Session-less boot state: the session-focused landing. A fresh tab
            // lands here rather than adopting the global-current session.
            <PanelLanding
              client={clientRef.current}
              status={status}
              stageEnabled={stageEnabled}
              models={models}
              onNewSession={(opts) => void newSession(opts)}
              sessions={shownSessions}
              loaded={sessionsLoaded}
              current={curSess}
              liveBusy={liveBusy}
              onSelect={(id) => {
                selectSession(id)
                setViewMode('focus')
                localStorage.setItem('terva_viewmode', 'focus')
              }}
              onRename={rename}
              onDelete={del}
              onArchive={archive}
              groups={sessionGroups}
              filterGroups={filterGroups}
              filter={sessionFilter}
              onCycleGroup={cycleSessionGroup}
              onToggleGroup={toggleSessionGroup}
              onCreateGroup={createSessionGroup}
            />
          ) : viewMode === 'board' ? (
            <div class="board-view">
              <SessionsBoard
                sessions={shownSessions}
                loaded={sessionsLoaded}
                current={curSess}
                liveBusy={liveBusy}
                onSelect={(id) => {
                  selectSession(id)
                  setViewMode('focus')
                  localStorage.setItem('terva_viewmode', 'focus')
                }}
                onNew={() => newSession()}
                onRename={rename}
                onDelete={del}
                onArchive={archive}
                groups={sessionGroups}
                filterGroups={filterGroups}
                filter={sessionFilter}
                onCycleGroup={cycleSessionGroup}
                onToggleGroup={toggleSessionGroup}
                onCreateGroup={createSessionGroup}
              />
              <SwarmLane
                tasks={boardTasks}
                loaded={boardTasksLoaded}
                backends={boardBackends}
                workersEnabled={boardWorkersEnabled}
                onSpawn={spawnWorker}
                onAction={surfaceAction}
                waiting={waitingByAgent(boardApprovals)}
                onOpenSession={(id) => {
                  selectSession(id)
                  setViewMode('focus')
                  localStorage.setItem('terva_viewmode', 'focus')
                }}
              />
              {workflowsOn && (
                <WorkflowLane
                  runs={workflowRuns}
                  loaded={workflowRunsLoaded}
                  onOpen={openWorkflowRun}
                  onRefresh={fetchWorkflowRuns}
                />
              )}
            </div>
          ) : (
            <>
              <ConversationTimeline
                items={items}
                busy={busy}
                toolView={toolView}
                queued={queued}
                onEditQueued={editQueued}
                onCancelQueued={cancelQueued}
                onReveal={revealTurns}
                revealingID={revealingID}
                earlier={win.base}
                onLoadEarlier={loadEarlier}
                loadingEarlier={loadingEarlier}
                sess={curSess}
                canDownload={canDownloadShares}
              />

              {permission && <PermissionRequestView request={permission} onDecide={decide} />}
              {ask && <AskRequestView request={ask} onAnswer={answer} />}

              <Composer
                busy={busy}
                onSend={onSubmit}
                onToast={setToast}
                commands={slashCommands}
                skills={skills}
                files={fileList}
                onFilesNeeded={() => void requestFiles()}
                onCancel={() => clientRef.current?.fire('cancel', null, curRef.current)}
                onUpload={stageFile}
                canAttachFiles={canAttachFiles}
                maxAttachmentBytes={maxAttachmentBytes}
                // Not for sending — stageFile closes over the session itself.
                // The composer needs it to know when the session CHANGED, so
                // ids staged into the one you left do not ride a prompt here.
                // This element keeps its identity across a session switch (no
                // key, and both branches of the view-mode ternary render it),
                // so nothing else would clear them.
                sessionID={curSess}
              />
            </>
          )}
        </div>

        {/* Which rail depends on whether there is a session to be about. */}
        {paneOpen && !curSess && (
          <WorkspaceDrawer
            tab={wsTab}
            onTab={setWsTab}
            providers={wsProviders}
            err={wsProvidersErr}
            auth={{
              flow: authFlow,
              busy: authBusy,
              error: authErr,
              start: startLogin,
              submit: submitLogin,
              cancel: cancelLogin,
              logout: logoutProvider,
              removeEndpoint,
            }}
            onClose={() => setPaneOpen(false)}
            onRefresh={() => {
              void loadWorkspaceProviders()
              if (canReadSecrets) void loadWorkspaceSecrets()
            }}
            onRestart={canRestart ? restart : undefined}
            version={serverVersion}
            trusted={!!curInfo?.trusted}
            onTrust={trustWorkspace}
            canReadSecrets={canReadSecrets}
            secrets={wsSecrets}
            secretsErr={wsSecretsErr}
          />
        )}
        {paneOpen && curSess && (
          <PaneHost
            surfaces={surfaces}
            active={activeSurface}
            data={surfaceData}
            usage={usageSnap}
            err={surfaceErr}
            onActivate={setActiveSurface}
            onAction={surfaceAction}
            onFetchNode={fetchNode}
            onListResets={listResets}
            onConsumeReset={consumeReset}
            onClose={() => setPaneOpen(false)}
            onRefresh={loadSurface}
            onRestart={canRestart ? restart : undefined}
            version={serverVersion}
            auth={{
              flow: authFlow,
              busy: authBusy,
              error: authErr,
              start: startLogin,
              submit: submitLogin,
              cancel: cancelLogin,
              logout: logoutProvider,
              removeEndpoint,
            }}
            trusted={!!curInfo?.trusted}
            onTrust={trustWorkspace}
            models={models}
          />
        )}
      </div>

      {toast && (
        <div class="toast" onClick={() => setToast('')}>
          {toast}
        </div>
      )}
    </div>
  )
}

// PaneHost is the switchable pane region — a right rail on desktop, a full sheet
// on mobile. It shows a switcher of available surfaces and renders the active
// one by kind (the generalization of the old context modal).
export function PaneHost({
  surfaces,
  active,
  data,
  usage,
  err,
  onActivate,
  onAction,
  onFetchNode,
  onListResets,
  onConsumeReset,
  onClose,
  onRefresh,
  onRestart,
  version,
  trusted,
  onTrust,
  models,
  auth,
}: {
  surfaces: SurfaceMeta[]
  active: string
  data: Surface | null
  usage?: UsageInfo | null
  err: string
  onActivate: (id: string) => void
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onListResets: () => Promise<ResetsListResult>
  onConsumeReset: (id: string) => Promise<ResetConsumeResult>
  onClose: () => void
  onRefresh?: (id: string) => void
  onRestart?: () => void
  version?: string
  trusted?: boolean
  onTrust?: (trust: boolean) => void
  models?: ModelInfo[]
  auth?: AuthPaneProps
}) {
  // Core panes (context/usage/tasks/status) stay on row 1; dynamic extension
  // panels get their own row so a long ext title can't shove the core tabs
  // off-screen. Ext tab labels are width-bounded (full title on hover).
  const core = surfaces.filter((s) => !s.id.startsWith('ext:'))
  const ext = surfaces.filter((s) => s.id.startsWith('ext:'))
  const tab = (s: SurfaceMeta) => (
    <button
      key={s.id}
      class={`pane-tab${s.id === active ? ' active' : ''}`}
      title={s.title}
      onClick={() => onActivate(s.id)}
    >
      {s.icon && <span class="pane-tab-icon">{s.icon}</span>}
      <span class="pane-tab-label">{s.title}</span>
      {s.badge && <span class="pane-tab-badge">{s.badge}</span>}
    </button>
  )
  return (
    <aside class="pane-rail">
      <div class="pane-head">
        <div class="pane-tabrow">
          <div class="pane-tabs">{core.map(tab)}</div>
          <button class="icon sm" title={t('Close panes')} onClick={onClose}>
            ×
          </button>
        </div>
        {ext.length > 0 && <div class="pane-tabs ext-row">{ext.map(tab)}</div>}
      </div>
      <div class="pane-body">
        {err && <div class="pick-empty">{err}</div>}
        {!data && !err && <div class="pick-empty">{t('loading…')}</div>}
        {data && (
          <SurfaceView
            surface={data}
            usage={usage}
            onAction={onAction}
            onFetchNode={onFetchNode}
            onListResets={onListResets}
            onConsumeReset={onConsumeReset}
            onRefresh={onRefresh}
            onRestart={onRestart}
            version={version}
            trusted={trusted}
            onTrust={onTrust}
            models={models}
            auth={auth}
          />
        )}
      </div>
    </aside>
  )
}

// WorkspaceDrawer is the right rail when there is no session behind it.
//
// The rail used to be one thing: a switcher over the SURFACES a session offers.
// On the landing there is no session, every surface is served through a session
// handle, and an empty address does not mean "no session" — it mints one. So the
// panel refused to fetch, and the rail sat on "loading…" forever: a control that
// opens onto nothing, which is worse than one that is not there.
//
// It is not that nothing belongs there. The daemon has facts of its own, and the
// two most useful ones when you have not started anything yet are exactly these:
// which providers you are signed in to (on a fresh panel, the answer is usually
// "none", and this is where you fix it), and what this daemon is. Both are
// served by verbs that ignore the session address by design, which is what makes
// this possible without inventing a workspace surface.
//
// Providers renders through the SAME body as the session rail's providers pane,
// because they are the same data — the daemon builds that surface by calling
// auth.providers. Two renderers would drift; this one cannot.
export function WorkspaceDrawer({
  tab,
  onTab,
  providers,
  err,
  auth,
  onClose,
  onRefresh,
  onRestart,
  version,
  trusted,
  onTrust,
  canReadSecrets,
  secrets,
  secretsErr,
}: {
  tab: 'providers' | 'secrets' | 'about'
  onTab: (tab: 'providers' | 'secrets' | 'about') => void
  providers: ProvidersView | null
  err: string
  auth?: AuthPaneProps
  onClose: () => void
  onRefresh: () => void
  onRestart?: () => void
  version?: string
  trusted?: boolean
  onTrust?: (trust: boolean) => void
  // Whether the daemon negotiated the secrets group (--web-allow-secrets).
  // The tab is absent otherwise, rather than present and failing.
  canReadSecrets?: boolean
  secrets?: SecretsStatus | null
  secretsErr?: string
}) {
  return (
    <aside class="pane-rail">
      <div class="pane-head">
        <div class="pane-tabrow">
          <div class="pane-tabs">
            <button class={`pane-tab${tab === 'providers' ? ' active' : ''}`} onClick={() => onTab('providers')}>
              <span class="pane-tab-icon">🔑</span>
              <span class="pane-tab-label">{t('Providers')}</span>
            </button>
            {canReadSecrets && (
              <button class={`pane-tab${tab === 'secrets' ? ' active' : ''}`} onClick={() => onTab('secrets')}>
                <span class="pane-tab-icon">🔒</span>
                <span class="pane-tab-label">{t('Secrets')}</span>
              </button>
            )}
            <button class={`pane-tab${tab === 'about' ? ' active' : ''}`} onClick={() => onTab('about')}>
              <span class="pane-tab-icon">☰</span>
              <span class="pane-tab-label">{t('About')}</span>
            </button>
          </div>
          <button class="icon sm" title={t('Close panes')} onClick={onClose}>
            ×
          </button>
        </div>
      </div>
      <div class="pane-body">
        {tab === 'providers' && (
          <>
            {err && <div class="pick-empty">{err}</div>}
            {!providers && !err && <div class="pick-empty">{t('loading…')}</div>}
            {providers && (
              <ProvidersBody
                v={providers}
                flow={auth?.flow ?? null}
                onStart={(p, m) => auth?.start(p, m)}
                onSubmit={(vals) => auth?.submit(vals)}
                onCancel={() => auth?.cancel()}
                onLogout={(p) => auth?.logout(p)}
                onRemoveEndpoint={(id) => auth?.removeEndpoint(id)}
                busy={auth?.busy ?? false}
                error={auth?.error ?? ''}
              />
            )}
          </>
        )}
        {tab === 'secrets' && (
          <>
            {secretsErr && <div class="pick-empty">{secretsErr}</div>}
            {!secrets && !secretsErr && <div class="pick-empty">{t('loading…')}</div>}
            {secrets && <SecretsBody v={secrets} />}
          </>
        )}
        {tab === 'about' && (
          <div class="ws-about">
            <p class="ws-about__lead">
              {t('This is the workspace itself — what is true of the daemon whether or not a session is open.')}
            </p>
            <dl class="ws-about__facts">
              <dt>{t('Version')}</dt>
              <dd>{version || t('unknown')}</dd>
              <dt>{t('Workspace')}</dt>
              <dd>{trusted ? t('trusted') : t('not trusted')}</dd>
            </dl>
            <div class="ws-about__actions">
              <button class="btn" onClick={onRefresh}>
                {t('Refresh')}
              </button>
              {onTrust && (
                <button class="btn" onClick={() => onTrust(!trusted)}>
                  {trusted ? t('Revoke trust') : t('Trust this workspace')}
                </button>
              )}
              {/* Only offered when the daemon says it can restart itself — the
                  same gate the session rail's settings pane uses. */}
              {onRestart && (
                <button class="btn" onClick={onRestart}>
                  {t('Restart the daemon')}
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    </aside>
  )
}

// ---- secrets: terva's at-rest posture (workspace drawer) ----
//
// What is encrypted, what is still plaintext, and what the agent may read. It
// renders `secrets.status`, which the CLI renders too — one producer, so this
// pane and `terva secret status` cannot disagree about whether a file is
// sealed.
//
// NOTHING here is a secret value: paths, modes, counts, names, states and
// reasons only. That is a property of the wire, not of this component — the
// daemon never sends one — which is what makes a posture report safe on a
// screen someone else can see. Rotation is deliberately absent from the group,
// so there is no control here that could destroy a key.

// SecretsRow is one labelled finding. `tone` drives the dot: a report whose
// whole job is to say what is wrong should not make the reader parse prose to
// find it.
function SecretsRow({ label, tone, children }: { label: string; tone: 'ok' | 'warn' | 'bad'; children: ComponentChildren }) {
  return (
    <div class={`secrets-row secrets-row--${tone}`}>
      <div class="secrets-row__label">{label}</div>
      <div class="secrets-row__value">{children}</div>
    </div>
  )
}

export function SecretsBody({ v }: { v: SecretsStatus }) {
  const key = v.key
  // The absent/missing split is the one distinction here a reader can act on
  // wrongly, so it gets two different tones and two different sentences:
  // absent means encryption was never turned on, missing means ciphertext
  // exists that this key was meant to open.
  const keyTone = key.state === 'present' ? (key.owner_only === false ? 'warn' : 'ok') : key.state === 'absent' ? 'warn' : 'bad'
  const plaintext = v.config.plaintext ?? []
  const grants = v.grants ?? []
  // An expired grant is a finding — it is the thing a reader came to renew or
  // remove — so the row cannot sit under an "all fine" dot while the only red
  // text in it says otherwise.
  const grantsTone = grants.some((g) => g.expired) ? 'warn' : 'ok'
  const files = v.files ?? []
  const reads = (v.reads ?? []).filter((r) => !r.readable)

  return (
    <div class="secrets-pane">
      <p class="ws-about__lead">
        {t('What is encrypted at rest on this daemon, and what its own agent may read. No secret value is sent here.')}
      </p>

      <SecretsRow label={t('Key')} tone={keyTone}>
        {key.state === 'present' && (
          <>
            <code>{key.path}</code>
            {key.from_env && <span class="secrets-note"> {t('(supplied via environment)')}</span>}
            {!key.from_env && key.owner_only && <span class="secrets-note"> {key.mode} {t('owner-only')}</span>}
            {!key.from_env && key.owner_only === false && <div class="secrets-note secrets-note--bad">{key.reason}</div>}
          </>
        )}
        {key.state !== 'present' && <div class="secrets-note">{key.reason || key.state}</div>}
      </SecretsRow>

      {v.recipient && (
        <SecretsRow label={t('Recipient')} tone="ok">
          {/* A PUBLIC key. Shown because a component operator needs to read it
              off the screen to seal a value by hand. */}
          <code class="secrets-recipient">{v.recipient}</code>
        </SecretsRow>
      )}

      {/* Not a warning: a lazy `terva secret rotate` is DESIGNED to leave these,
          and files heal onto the new key as they are rewritten. Flagging the
          normal outcome of a supported operation would train the reader to
          ignore the dots. The sentence says what they still do. */}
      {!!v.retired_keys && (
        <SecretsRow label={t('Retired keys')} tone="ok">
          {tn(
            v.retired_keys,
            '%d retired key still opens files that have not been rewritten, and never seals',
            '%d retired keys still open files that have not been rewritten, and never seal',
          )}
        </SecretsRow>
      )}

      {files.map((f) => (
        <SecretsRow key={f.name} label={f.name} tone={f.state === 'encrypted' || f.state === 'absent' ? 'ok' : 'warn'}>
          {f.state}
          {f.note && <div class="secrets-note">{f.note}</div>}
        </SecretsRow>
      ))}

      <SecretsRow label={t('Store')} tone={v.store.present && !v.store.encrypted ? 'warn' : 'ok'}>
        {v.store.error && <div class="secrets-note secrets-note--bad">{v.store.error}</div>}
        {!v.store.error && !v.store.present && t('absent — no scoped secrets stored')}
        {!v.store.error && v.store.present && (
          <>
            <div>{v.store.encrypted ? t('encrypted') : t('PLAINTEXT')}</div>
            <ul class="secrets-list">
              {(v.store.scopes ?? []).map((sc) => (
                <li key={sc.scope}>
                  <code>{sc.scope}</code> <span class="secrets-note">{tn(sc.keys, '%d key', '%d keys')}</span>
                </li>
              ))}
            </ul>
          </>
        )}
      </SecretsRow>

      <SecretsRow label={t('config.json')} tone={plaintext.length ? 'warn' : 'ok'}>
        {v.config.total === 0 && t('no secret-bearing values')}
        {v.config.total > 0 &&
          plaintext.length === 0 &&
          tn(v.config.total, '%d secret value, all encrypted', '%d secret values, all encrypted')}
        {plaintext.length > 0 && (
          <>
            <div>{t('%d of %d still plaintext', plaintext.length, v.config.total)}</div>
            <ul class="secrets-list">
              {plaintext.map((p) => (
                <li key={p}>
                  <code>{p}</code>
                </li>
              ))}
            </ul>
          </>
        )}
        {!!v.config.unclassified?.length && (
          <div class="secrets-note">{t('unclassifiable (no manifest): %s', v.config.unclassified.join(', '))}</div>
        )}
      </SecretsRow>

      <SecretsRow label={t('Agent reads')} tone={v.config.agent_can_read && reads.length === 0 ? 'ok' : 'warn'}>
        <div>
          {v.config.agent_can_read
            ? t('config.json readable (nothing secret left in it)')
            : t('config.json denied — %s', v.config.reason ?? '')}
        </div>
        {/* Only the components that are NOT plainly readable. A row per healthy
            component would bury the ones that need action. */}
        {reads.map((r) => (
          <div key={r.scope} class="secrets-note">
            <code>{r.scope}</code>{' '}
            {r.enforced ? t('denied —') : t('will be denied in a future release —')} {r.reason}
          </div>
        ))}
      </SecretsRow>

      <SecretsRow label={t('Grants')} tone={grantsTone}>
        {grants.length === 0 && t('none — every scope is reachable only by its own principal')}
        <ul class="secrets-list">
          {grants.map((g) => (
            <li key={`${g.principal}:${g.scope}`}>
              <code>{g.principal}</code> → <code>{g.scope}</code> <span class="secrets-note">({g.mode})</span>
              {g.expired && <span class="secrets-note secrets-note--bad"> {t('expired')}</span>}
            </li>
          ))}
        </ul>
      </SecretsRow>

      <p class="secrets-note">
        {t('Rotating or replacing the key is a terminal-only operation: run `terva secret rotate`.')}
      </p>
    </div>
  )
}

// ---- raati: the deliberation board (kind=raati) ----
// Three panelist blocks + the tallied verdict, fed live by
// surface_updated pushes. The homage beat: a verdict lands as kanji
// with a flash, then fades into the viewer's language.

const RAATI_KANJI: Record<string, string> = {
  approve: '承認',
  reject: '否定',
  abstain: '棄権',
  approved: '承認',
  rejected: '否定',
  escalated: '保留',
}

export function KanjiVerdict({ word, kanji, tone }: { word: string; kanji: string; tone: string }) {
  const [settled, setSettled] = useState(false)
  useEffect(() => {
    setSettled(false)
    const id = window.setTimeout(() => setSettled(true), 1600)
    return () => window.clearTimeout(id)
  }, [word, kanji])
  return <span class={`raati-kanji tone-${tone}${settled ? ' settled' : ' flash'}`}>{settled ? word : kanji}</span>
}

export function RaatiBody({
  v,
  onAction,
  models,
}: {
  v: RaatiView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  models?: ModelInfo[]
}) {
  const [question, setQuestion] = useState('')
  const [cls, setCls] = useState('advisory')
  const [level, setLevel] = useState('0')
  // Convening profile: picking one defers class/level to the server's
  // profile resolution ('' selections mean "the profile decides");
  // choosing either explicitly overrides the profile for that field.
  const [profile, setProfile] = useState('')
  const pickProfile = (p: string) => {
    setProfile(p)
    if (p) {
      setCls('')
      setLevel('')
    } else {
      if (!cls) setCls('advisory')
      if (!level) setLevel('0')
    }
  }
  const [seatOrder, setSeatOrder] = useState('')
  const [binding, setBinding] = useState('') // level 0: index into models, '' = workspace default
  const [ladderProv, setLadderProv] = useState('') // level 1: provider, '' = workspace default
  const [seatPicks, setSeatPicks] = useState<string[]>(['', '', '']) // level 2: per-seat model indexes, all '' = config
  const [evidence, setEvidence] = useState('')
  const [conversation, setConversation] = useState('')
  const [singleRound, setSingleRound] = useState(false)
  const [inquire, setInquire] = useState('')
  const [converge, setConverge] = useState(false)
  // Theater: the fullscreen MAGI console. The default stays the pane
  // (most integrated); a user who prefers theater gets it remembered.
  const [theater, setTheater] = useState(() => {
    try {
      return localStorage.getItem('raati.view') === 'theater'
    } catch {
      return false
    }
  })
  const showTheater = (on: boolean) => {
    setTheater(on)
    try {
      localStorage.setItem('raati.view', on ? 'theater' : 'pane')
    } catch {
      /* private mode: honor the toggle for this session only */
    }
  }
  const idle = !v.running && (v.units?.length ?? 0) === 0 && !v.decision && !v.error
  const ticker = useRaatiTicker(v)
  // Archive browsing: decision chips + question search, client-side over
  // the server's 50-record window.
  const [histFilter, setHistFilter] = useState('')
  const [histQuery, setHistQuery] = useState('')
  const [histShow, setHistShow] = useState(10)
  const histNeedle = histQuery.trim().toLowerCase()
  const hist = (v.history ?? []).filter(
    (h) => (!histFilter || h.decision === histFilter) && (!histNeedle || h.question.toLowerCase().includes(histNeedle)),
  )
  const byProvider = useMemo(() => {
    const g = new Map<string, { m: ModelInfo; idx: number }[]>()
    ;(models ?? []).forEach((m, idx) => {
      const list = g.get(m.provider) ?? []
      list.push({ m, idx })
      g.set(m.provider, list)
    })
    return [...g.entries()]
  }, [models])
  const seatsChosen = seatPicks.filter((s) => s !== '').length
  const seatsPartial = level === '2' && seatsChosen > 0 && seatsChosen < seatPicks.length
  const convene = () => {
    const args = buildConveneArgs(
      { question, profile, cls, level, singleRound, inquire, converge, seatOrder, binding, ladderProv, seatPicks, evidence, conversation },
      models,
    )
    if (!args) return
    onAction('raati', 'convene', args)
    setQuestion('')
    setEvidence('')
  }
  return (
    <div class="raati-body">
      <div class="raati-board">
        <div class="raati-head">
          {/* i18n-exempt — the RAATI wordmark, part of the theater prop */}
          <span class="raati-logo">RAATI</span>
          {v.class ? <span class="raati-chip">{v.class}</span> : null}
          {v.seat_order ? <span class="raati-chip">{t('seats: %s', v.seat_order)}</span> : null}
          {v.running ? (
            <span class="raati-chip round">
              {v.phase === 'briefing'
                ? t('preparing brief')
                : v.phase === 'inquiry'
                  ? t('inquiry gap')
                  : v.round === 3
                  ? t('convergence round')
                  : v.round === 2
                    ? t('cross-examination')
                    : t('blind round')}
            </span>
          ) : null}
          {v.archived ? <span class="raati-chip">{t('archived %s', (v.when ?? '').slice(0, 10))}</span> : null}
          {v.binding ? <span class="raati-binding">{v.binding}</span> : null}
          <button class="raati-theater-btn" title={t('Theater view (fullscreen)')} onClick={() => showTheater(true)}>
            ⛶
          </button>
        </div>
        {v.question ? <div class="raati-question">{v.question}</div> : null}
        {v.running && v.phase === 'briefing' ? (
          <div class="raati-briefing">
            <span class="raati-kanji pulse">摘要</span>
            <div class="raati-dim">{t('the clerk is preparing a brief of the conversation for the panel…')}</div>
          </div>
        ) : null}
        {(v.units?.length ?? 0) > 0 ? (
          <div class="raati-units">
            {(v.units ?? []).map((u) => (
              <RaatiBlock key={u.name} u={u} />
            ))}
          </div>
        ) : null}
        {v.decision ? <RaatiVerdictPanel v={v} /> : null}
        {v.error ? <div class="raati-error">{t('deliberation failed: %s', v.error)}</div> : null}
        {idle ? (
          <div class="raati-form">
            <textarea
              value={question}
              rows={3}
              placeholder={t('Put a question before the panel…')}
              onInput={(e) => setQuestion((e.target as HTMLTextAreaElement).value)}
            />
            <details class="raati-evidence">
              <summary>{t('evidence (optional)')}</summary>
              <textarea
                value={evidence}
                rows={5}
                placeholder={t('Paste what the panel should see — diffs, logs, constraints. The panel judges only what it is shown.')}
                onInput={(e) => setEvidence((e.target as HTMLTextAreaElement).value)}
              />
            </details>
            <div class="raati-form-row">
              <select value={conversation} onChange={(e) => setConversation((e.target as HTMLSelectElement).value)}>
                <option value="">{t('conversation: not shared')}</option>
                <option value="summary">{t('conversation: summarized for the panel (one model pass)')}</option>
                <option value="full">{t('conversation: recent context, capped')}</option>
              </select>
            </div>
            <div class="raati-form-row">
              {(v.profiles?.length ?? 0) > 0 ? (
                <select
                  class="raati-profile"
                  value={profile}
                  title={v.profiles?.find((p) => p.name === profile)?.description || t('convene under a named profile from your config (raati.profiles)')}
                  onChange={(e) => pickProfile((e.target as HTMLSelectElement).value)}
                >
                  <option value="">{t('profile: none')}</option>
                  {(v.profiles ?? []).map((p) => (
                    <option key={p.name} value={p.name} title={p.description}>
                      {p.description ? `${p.name} — ${p.description}` : p.name}
                    </option>
                  ))}
                </select>
              ) : null}
              <select value={cls} onChange={(e) => setCls((e.target as HTMLSelectElement).value)}>
                {profile ? <option value="">{t('class: profile default')}</option> : null}
                <option value="advisory">{t('advisory — majority, dissent attached')}</option>
                <option value="gate">{t('gate — unanimity, fails closed')}</option>
                <option value="veto">{t('veto — MAGATAMA-3 may block')}</option>
              </select>
              <select value={level} onChange={(e) => setLevel((e.target as HTMLSelectElement).value)}>
                {profile ? <option value="">{t('level: profile default')}</option> : null}
                <option value="0">{t('level 0 kaiku — one binding, correlated')}</option>
                <option value="1">{t('level 1 kuoro — the provider ladder')}</option>
                <option value="2">{t('level 2 käräjät — cross-provider')}</option>
              </select>
              {level === '0' && byProvider.length > 0 ? (
                <select value={binding} onChange={(e) => setBinding((e.target as HTMLSelectElement).value)}>
                  <option value="">{t('binding: workspace default')}</option>
                  {byProvider.map(([prov, list]) => (
                    <optgroup key={prov} label={prov}>
                      {list.map(({ m, idx }) => (
                        <option key={`${m.provider}/${m.id}`} value={String(idx)}>
                          {m.id}
                        </option>
                      ))}
                    </optgroup>
                  ))}
                </select>
              ) : null}
              {level === '1' && byProvider.length > 0 ? (
                <select value={ladderProv} onChange={(e) => setLadderProv((e.target as HTMLSelectElement).value)}>
                  <option value="">{t('ladder: workspace default provider')}</option>
                  {byProvider.map(([prov]) => (
                    <option key={prov} value={prov}>
                      {t('ladder: %s', prov)}
                    </option>
                  ))}
                </select>
              ) : null}
              {level === '2' && byProvider.length > 0
                ? seatPicks.map((pick, i) => (
                    <select
                      key={i}
                      value={pick}
                      onChange={(e) => {
                        const val = (e.target as HTMLSelectElement).value
                        setSeatPicks((prev) => prev.map((p, j) => (j === i ? val : p)))
                      }}
                    >
                      <option value="">{t('seat %d: config default', i + 1)}</option>
                      {byProvider.map(([prov, list]) => (
                        <optgroup key={prov} label={prov}>
                          {list.map(({ m, idx }) => (
                            <option key={`${m.provider}/${m.id}`} value={String(idx)}>
                              {m.id}
                            </option>
                          ))}
                        </optgroup>
                      ))}
                    </select>
                  ))
                : null}
              <select value={seatOrder} onChange={(e) => setSeatOrder((e.target as HTMLSelectElement).value)}>
                <option value="">{profile ? t('seats: profile default') : t('seats: config default')}</option>
                <option value="convene">{t('seats: shuffled per convening')}</option>
                <option value="fixed">{t('seats: fixed pool order')}</option>
                <option value="turn">{t('seats: reshuffled per round (respawns, costlier)')}</option>
              </select>
              <label class="raati-check">
                <input
                  type="checkbox"
                  checked={singleRound}
                  onChange={(e) => setSingleRound((e.target as HTMLInputElement).checked)}
                />
                {t('single round')}
              </label>
              <select
                value={inquire}
                title={t('panelists may pose up to two questions each; the clerk answers between rounds')}
                onChange={(e) => setInquire((e.target as HTMLSelectElement).value)}
              >
                <option value="">{profile ? t('inquiries: profile default') : t('inquiries: off')}</option>
                {profile ? <option value="off">{t('inquiries: off')}</option> : null}
                <option value="record">{t('inquiries: clerk answers from the record')}</option>
                <option value="convener">{t('inquiries: clerk may also ask me')}</option>
              </select>
              <label class="raati-check" title={t('one extra reveal round, only if cross-examination flipped a verdict')}>
                <input type="checkbox" checked={converge} onChange={(e) => setConverge((e.target as HTMLInputElement).checked)} />
                {t('converge')}
              </label>
              <button
                class="raati-convene"
                onClick={convene}
                disabled={!question.trim() || seatsPartial}
                title={seatsPartial ? t('seat the whole panel or leave every seat on the config default') : undefined}
              >
                {t('Convene')}
              </button>
            </div>
          </div>
        ) : null}
        {!v.running && !idle ? (
          <div class="raati-form-row">
            <button class="raati-reset" onClick={() => onAction('raati', 'reset')}>
              {t('New deliberation')}
            </button>
          </div>
        ) : null}
        {!v.running && (v.history?.length ?? 0) > 0 ? (
          <div class="raati-history">
            <div class="raati-history-title">{t('previous deliberations')}</div>
            <div class="raati-history-controls">
              {['', 'approved', 'rejected', 'escalated'].map((f) => (
                <button
                  key={f}
                  class={`raati-chipbtn${histFilter === f ? ` on tone-${f || 'all'}` : ''}`}
                  onClick={() => setHistFilter(f)}
                >
                  {f === '' ? t('all') : raatiVerdictWord(f)}
                </button>
              ))}
              <input
                class="raati-history-search"
                value={histQuery}
                placeholder={t('filter questions…')}
                onInput={(e) => setHistQuery((e.target as HTMLInputElement).value)}
              />
            </div>
            {hist.slice(0, histShow).map((h) => (
              <RaatiHistoryRow key={h.id} h={h} onShow={() => onAction('raati', 'show', { id: h.id })} />
            ))}
            {hist.length === 0 ? <div class="raati-dim">{t('no matching deliberations')}</div> : null}
            {hist.length > histShow ? (
              <button class="raati-history-more" onClick={() => setHistShow((n) => n + 25)}>
                {t('show %d more', hist.length - histShow)}
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
      {theater ? <RaatiTheater v={v} lines={ticker} onAction={onAction} onExit={() => showTheater(false)} /> : null}
    </div>
  )
}

// useRaatiTicker narrates a live deliberation by diffing consecutive
// view states — the server pushes state, not events, so the feed is
// synthesized client-side. Archive replays narrate nothing (the record
// is history, not news); the feed clears when the board resets.
export function useRaatiTicker(v: RaatiView): string[] {
  const [lines, setLines] = useState<string[]>([])
  const prevRef = useRef<RaatiView | null>(null)
  useEffect(() => {
    const prev = prevRef.current
    prevRef.current = v
    if (v.archived) return
    const idle = !v.running && (v.units?.length ?? 0) === 0 && !v.decision && !v.error
    if (idle) {
      if (prev && ((prev.units?.length ?? 0) > 0 || prev.decision || prev.archived)) setLines([])
      return
    }
    const add: string[] = []
    if (v.question && (!prev || prev.archived || prev.question !== v.question)) add.push(t('convened: %s', v.question))
    if (v.phase === 'briefing' && prev?.phase !== 'briefing') add.push(t('the clerk prepares the brief'))
    if (v.phase === 'inquiry' && prev?.phase !== 'inquiry') add.push(t('the clerk takes the panel\u2019s questions'))
    if (v.round === 1 && prev?.round !== 1) add.push(t('blind round begins'))
    if (v.round === 2 && prev?.round !== 2) add.push(t('cross-examination begins'))
    if (v.round === 3 && prev?.round !== 3) add.push(t('positions changed — the panel converges'))
    const seenInq = prev && !prev.archived ? (prev.inquiries?.length ?? 0) : 0
    for (const q of (v.inquiries ?? []).slice(seenInq)) {
      add.push(t('%s asks: %s', q.unit, q.question))
      add.push(
        q.source === 'unanswered' || !q.answer
          ? t('clerk: not in the record')
          : q.source === 'convener'
            ? t('convener: %s', q.answer)
            : t('clerk: %s', q.answer),
      )
    }
    const before = new Map((prev && !prev.archived ? (prev.units ?? []) : []).map((u) => [u.name, u]))
    for (const u of v.units ?? []) {
      const p = before.get(u.name)
      if (u.status === 'deliberating' && p?.status !== 'deliberating') add.push(t('%s deliberating', u.name))
      if (u.status === 'voted' && p?.status !== 'voted' && u.verdict) {
        const conf = typeof u.confidence === 'number' ? ` ${u.confidence.toFixed(2)}` : ''
        const revised = u.blind && u.blind !== u.verdict ? ` — ${t('revised from %s', raatiVerdictWord(u.blind))}` : ''
        add.push(`${u.name}: ${raatiVerdictWord(u.verdict)}${conf}${revised}`)
      }
      if (u.status === 'absent' && p?.status !== 'absent') add.push(`${u.name}: ${t('absent')}${u.why ? ` — ${u.why}` : ''}`)
    }
    if (v.decision && prev?.decision !== v.decision) {
      const tally = v.tally ? ` ${v.tally.approve}·${v.tally.reject}·${v.tally.abstain}` : ''
      add.push(`${raatiVerdictWord(v.decision)}${tally}${v.degraded ? ' ⚠' : ''}`)
    }
    if (v.error && prev?.error !== v.error) add.push(t('deliberation failed: %s', v.error))
    if (add.length) {
      const d = new Date()
      const two = (n: number) => String(n).padStart(2, '0')
      const stamp = `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`
      setLines((cur) => [...cur, ...add.map((l) => `${stamp}  ${l}`)].slice(-100))
    }
  }, [v])
  return lines
}

// raatiWhenLabel compresses an RFC 3339 stamp for archive rows: time of
// day for today's records, month-day for this year, full date otherwise.
export function raatiWhenLabel(when?: string): string {
  if (!when) return ''
  const d = new Date(when)
  if (isNaN(d.getTime())) return when.slice(0, 10)
  const now = new Date()
  const two = (n: number) => String(n).padStart(2, '0')
  if (d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()) {
    return `${two(d.getHours())}:${two(d.getMinutes())}`
  }
  const md = `${two(d.getMonth() + 1)}-${two(d.getDate())}`
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`
}

export function RaatiHistoryRow({ h, onShow }: { h: RaatiHistoryItem; onShow: () => void }) {
  return (
    <button class="raati-history-row" onClick={onShow} title={h.question}>
      <div class="raati-history-top">
        <span class={`raati-history-decision tone-${h.decision}`}>
          {RAATI_KANJI[h.decision] ?? ''} {h.decision}
        </span>
        {h.tally ? (
          <span class="raati-history-tally">
            {h.tally.approve}·{h.tally.reject}·{h.tally.abstain}
          </span>
        ) : null}
        {h.degraded ? <span class="raati-history-degraded">⚠</span> : null}
        {h.class && h.class !== 'advisory' ? <span class="raati-history-class">{h.class}</span> : null}
        {(h.minority?.length ?? 0) > 0 ? (
          <span class="raati-history-dissent">
            {t('dissent:')} {(h.minority ?? []).join(' ')}
          </span>
        ) : null}
        <span class="raati-history-when">{raatiWhenLabel(h.when)}</span>
      </div>
      <div class="raati-history-q">{h.question}</div>
    </button>
  )
}

export function RaatiBlock({ u }: { u: RaatiUnit }) {
  const accent = u.accent || '#7aa2f7'
  return (
    <div class={`raati-block s-${u.status}`} style={{ borderColor: accent }}>
      <div class="raati-block-head">
        <div class="raati-block-name" style={{ color: accent }}>
          {u.name}
        </div>
        {u.status === 'voted' || u.status === 'absent' ? <CopyButton text={raatiUnitCopyText(u)} label={t('Copy this ballot')} /> : null}
      </div>
      {u.binding ? <div class="raati-block-binding">{u.binding}</div> : null}
      {u.status === 'deliberating' ? (
        <div class="raati-deliberating">
          <span class="raati-kanji pulse">審議中</span>
          <div class="raati-dim">{u.verdict ? t('reconsidering — held %s', u.verdict) : t('deliberating…')}</div>
        </div>
      ) : null}
      {u.status === 'voted' && u.verdict ? (
        <div class="raati-vote">
          <KanjiVerdict word={raatiVerdictWord(u.verdict)} kanji={RAATI_KANJI[u.verdict] ?? u.verdict} tone={u.verdict} />
          {typeof u.confidence === 'number' ? (
            <div class="raati-conf">
              <div
                class="raati-conf-fill"
                style={{ width: `${Math.round(u.confidence * 100)}%`, background: accent }}
              />
            </div>
          ) : null}
          {u.blind && u.blind !== u.verdict ? <div class="raati-dim">{t('blind ballot: %s', u.blind)}</div> : null}
          {u.rationale ? <div class="raati-rationale">{u.rationale}</div> : null}
        </div>
      ) : null}
      {u.status === 'absent' ? (
        <div class="raati-absent">
          <span class="raati-offline">{t('OFFLINE')}</span>
          {u.why ? <div class="raati-dim">{u.why}</div> : null}
        </div>
      ) : null}
    </div>
  )
}

export function RaatiVerdictPanel({ v }: { v: RaatiView }) {
  const d = v.decision ?? ''
  return (
    <div class={`raati-verdict tone-${d}`}>
      <div class="raati-verdict-copy">
        <CopyButton text={raatiResultCopyText(v)} label={t('Copy the full result')} />
      </div>
      <KanjiVerdict word={raatiVerdictWord(d)} kanji={RAATI_KANJI[d] ?? d} tone={d} />
      {v.tally ? (
        <div class="raati-tally">
          {t('%d approve / %d reject / %d abstain / %d absent', v.tally.approve, v.tally.reject, v.tally.abstain, v.tally.absent)}
          {v.degraded ? <span class="raati-degraded"> — {t('degraded: not every unit voted')}</span> : null}
        </div>
      ) : null}
      {(v.minority?.length ?? 0) > 0 ? (
        <div class="raati-minority">
          <div class="raati-minority-title">{t('minority report')}</div>
          {(v.minority ?? []).map((m) => (
            <div class="raati-minority-row" key={m.unit}>
              <b>{m.unit}</b>: {m.rationale}
            </div>
          ))}
        </div>
      ) : null}
      {(v.inquiries?.length ?? 0) > 0 ? <RaatiInquiryList inquiries={v.inquiries ?? []} /> : null}
    </div>
  )
}

// RaatiInquiryList renders the panel's between-round Q&A docket — the
// open (unanswered) questions matter most: the decision was made with
// these gaps on the record.
export function RaatiInquiryList({ inquiries }: { inquiries: RaatiInquiry[] }) {
  return (
    <div class="raati-inquiries">
      <div class="raati-minority-title">{t('the panel asked')}</div>
      {inquiries.map((q, i) => (
        <div class="raati-inquiry-row" key={i}>
          <div>
            <b>{q.unit}</b>: {q.question}
          </div>
          <div class={`raati-inquiry-answer${q.source === 'unanswered' || !q.answer ? ' open' : ''}`}>
            {q.source === 'unanswered' || !q.answer ? t('not in the record — decided with this open') : `→ ${q.answer}`}
          </div>
        </div>
      ))}
    </div>
  )
}

// ---- raati: theater mode (the fullscreen MAGI console) ----
// The same RaatiView the pane renders, staged as the Evangelion MAGI
// console: three panels placed by unit number (KUSANAGI-2 top, YATA-1
// lower-right, MAGATAMA-3 lower-left), the hub reading RAATI, panels
// pulsing while they deliberate and settling to their verdict color.
// Terva palette, not screen-accurate cyan. Web-client only — it consumes
// the pushed surface like everything else, so there is no server change.

export function raatiTheaterSlots(units?: RaatiUnit[]): { top?: RaatiUnit; left?: RaatiUnit; right?: RaatiUnit } {
  const u = units ?? []
  const byNum: Record<number, RaatiUnit> = {}
  let numbered = u.length > 0
  for (const x of u) {
    const m = x.name.match(/(\d+)\s*$/)
    if (m) byNum[Number(m[1])] = x
    else numbered = false
  }
  // Faithful placement by the classic MAGI numbering (·2 top, ·1 lower-
  // right, ·3 lower-left). A recast panel that doesn't number cleanly
  // falls back to feed order.
  if (numbered && byNum[1] && byNum[2] && byNum[3]) return { top: byNum[2], right: byNum[1], left: byNum[3] }
  return { top: u[0], left: u[1], right: u[2] }
}

export function MagiPanel({
  u,
  pos,
  selected,
  onSelect,
}: {
  u?: RaatiUnit
  pos: 'top' | 'left' | 'right'
  selected: boolean
  onSelect: () => void
}) {
  if (!u) return <div class={`magi-panel pos-${pos} empty`} />
  const tone = u.status === 'voted' ? u.verdict ?? '' : u.status === 'absent' ? 'absent' : ''
  return (
    <button
      class={`magi-panel pos-${pos} s-${u.status} tone-${tone}${selected ? ' selected' : ''}`}
      style={`--accent:${u.accent || '#7aa2f7'}`}
      onClick={onSelect}
      title={u.rationale ?? u.why ?? u.name}
    >
      <div class="magi-name">{u.name}</div>
      {u.binding ? <div class="magi-binding">{u.binding}</div> : null}
      <div class="magi-state">
        {u.status === 'deliberating' ? <span class="raati-kanji pulse magi-glyph">審議中</span> : null}
        {u.status === 'voted' && u.verdict ? (
          <KanjiVerdict word={raatiVerdictWord(u.verdict)} kanji={RAATI_KANJI[u.verdict] ?? u.verdict} tone={u.verdict} />
        ) : null}
        {u.status === 'absent' ? <span class="raati-offline">{t('OFFLINE')}</span> : null}
      </div>
      {u.status === 'voted' && typeof u.confidence === 'number' ? (
        <div class="magi-conf">
          <div class="magi-conf-fill" style={{ width: `${Math.round(u.confidence * 100)}%` }} />
        </div>
      ) : null}
    </button>
  )
}

export function RaatiTheater({
  v,
  lines,
  onAction,
  onExit,
}: {
  v: RaatiView
  lines: string[]
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onExit: () => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onExit()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onExit])
  // The ticker follows the deliberation, but a deliberation is precisely when
  // an operator scrolls back to re-read an earlier event — so it unpins like
  // every other feed rather than snapping to the newest line under them. No
  // jump button: the pane is four lines tall, and it re-pins the moment they
  // scroll back down.
  const { ref: tickerRef, onScroll: onTickerScroll } = usePinnedTail<HTMLDivElement>([lines])
  const slots = raatiTheaterSlots(v.units)
  const idle = !v.running && (v.units?.length ?? 0) === 0 && !v.decision && !v.error
  const [sel, setSel] = useState<string | null>(null)
  // The stacked (mobile) conduits are right-angle circuit traces between
  // elements whose positions depend on content — CSS can't reference
  // sibling geometry, so measure the anchor lines into CSS variables.
  const panelsRef = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const el = panelsRef.current
    if (!el) return
    const measure = () => {
      const hub = el.querySelector<HTMLElement>('.magi-hub')
      if (!hub) return
      const base = el.getBoundingClientRect()
      const set = (name: string, px: number) => el.style.setProperty(name, `${Math.round(px)}px`)
      const hr = hub.getBoundingClientRect()
      set('--hubl', hr.left - base.left)
      set('--hubr', hr.right - base.left)
      set('--hubm', hr.top + hr.height / 2 - base.top)
      ;(['top', 'left', 'right'] as const).forEach((p, i) => {
        const r = el.querySelector<HTMLElement>(`.magi-panel.pos-${p}`)?.getBoundingClientRect()
        if (!r) return
        set(`--p${i}t`, r.top - base.top)
        set(`--p${i}b`, r.bottom - base.top)
      })
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    el.querySelectorAll('.magi-panel, .magi-hub').forEach((n) => ro.observe(n))
    return () => ro.disconnect()
  }, [v.units?.length, v.decision, v.running, v.phase])
  // The information console reads the dissent by default — dissent is the
  // product — then whatever panel the operator selects.
  const dissent = (v.minority ?? [])[0]?.unit ?? null
  const focusName = sel ?? dissent
  const focus = (v.units ?? []).find((u) => u.name === focusName) ?? null
  const phase = v.running
    ? v.phase === 'briefing'
      ? t('preparing brief')
      : v.phase === 'inquiry'
        ? t('inquiry gap')
        : v.round === 3
        ? t('convergence round')
        : v.round === 2
          ? t('cross-examination')
          : t('blind round')
    : v.decision
      ? t('decided')
      : t('standby')
  return (
    <div class="magi-overlay" role="dialog" aria-modal="true">
      <div class="magi-stage">
        <header class="magi-header">
          <span class="magi-title left">質問</span>
          {/* i18n-exempt×5 below — the MAGI-style HUD labels are part of the
              theater prop, like the RAATI wordmark and the 解決 kanji: the
              all-caps English IS the aesthetic, not interface prose. */}
          <div class="magi-info">
            <div>
              {/* i18n-exempt */}
              <b>LEVEL</b> {v.binding || '—'}
            </div>
            <div>
              {/* i18n-exempt */}
              <b>CLASS</b> {v.class || '—'}
            </div>
            <div>
              {/* i18n-exempt */}
              <b>ROUND</b> {v.round ?? '—'}
            </div>
            <div>
              {/* i18n-exempt */}
              <b>SEAT</b> {v.seat_order || '—'}
            </div>
            <div>
              {/* i18n-exempt */}
              <b>MODE</b> {phase}
            </div>
          </div>
          <span class={`magi-title right${v.decision ? ' lit' : ''}`}>解決</span>
        </header>
        <div ref={panelsRef} class={`magi-panels${v.running ? ' running' : ''}${v.decision ? ` decided tone-${v.decision}` : ''}`}>
          <div class="magi-conduit c-up" />
          <div class="magi-conduit c-left" />
          <div class="magi-conduit c-right" />
          <MagiPanel u={slots.top} pos="top" selected={focusName === slots.top?.name} onSelect={() => slots.top && setSel(slots.top.name)} />
          <div class={`magi-hub${v.decision ? ' decided' : ''}`}>
            {v.decision ? (
              <KanjiVerdict word={raatiVerdictWord(v.decision)} kanji={RAATI_KANJI[v.decision] ?? v.decision} tone={v.decision} />
            ) : (
              // i18n-exempt — the RAATI wordmark, part of the theater prop
              <span class="magi-hub-logo">RAATI</span>
            )}
            {v.tally ? (
              <div class="magi-tally">
                {v.tally.approve}·{v.tally.reject}·{v.tally.abstain}
                {v.degraded ? ' ⚠' : ''}
              </div>
            ) : null}
          </div>
          <MagiPanel u={slots.left} pos="left" selected={focusName === slots.left?.name} onSelect={() => slots.left && setSel(slots.left.name)} />
          <MagiPanel u={slots.right} pos="right" selected={focusName === slots.right?.name} onSelect={() => slots.right && setSel(slots.right.name)} />
        </div>
        <div class="magi-console">
          {!v.running && !idle ? (
            <button class="magi-archive-back" onClick={() => onAction('raati', 'reset')}>
              {t('← archive')}
            </button>
          ) : null}
          <div class="magi-console-tag">情報</div>
          {v.error ? (
            <div class="magi-console-body magi-console-err">{t('deliberation failed: %s', v.error)}</div>
          ) : idle && (v.history?.length ?? 0) > 0 ? (
            <div class="magi-console-body magi-archive">
              <div class="magi-archive-title">{t('previous deliberations')}</div>
              {(v.history ?? []).slice(0, 20).map((h) => (
                <button key={h.id} class="magi-archive-row" onClick={() => onAction('raati', 'show', { id: h.id })} title={h.question}>
                  <span class={`raati-kanji tone-${h.decision}`}>{RAATI_KANJI[h.decision] ?? '·'}</span>
                  <span class="magi-archive-q">{h.question}</span>
                  {h.tally ? (
                    <span class="magi-archive-tally">
                      {h.tally.approve}·{h.tally.reject}·{h.tally.abstain}
                      {h.degraded ? ' ⚠' : ''}
                    </span>
                  ) : null}
                  {(h.minority?.length ?? 0) > 0 ? <span class="magi-archive-dissent">{(h.minority ?? []).join(' ')}</span> : null}
                  <span class="magi-archive-when">{raatiWhenLabel(h.when)}</span>
                </button>
              ))}
            </div>
          ) : focus ? (
            <div class="magi-console-body">
              <div class="magi-console-head">
                <b style={`color:${focus.accent || '#7aa2f7'}`}>{focus.name}</b>
                {focus.binding ? <span class="raati-dim"> {focus.binding}</span> : null}
                {focus.status === 'voted' && focus.verdict ? (
                  <span class="magi-console-verdict">
                    {' · '}
                    {raatiVerdictWord(focus.verdict)}
                    {typeof focus.confidence === 'number' ? ` ${focus.confidence.toFixed(2)}` : ''}
                  </span>
                ) : null}
                {dissent === focus.name ? <span class="magi-console-dissent"> · {t('minority')}</span> : null}
              </div>
              <div class="magi-console-text">{focus.rationale || focus.why || t('no statement yet')}</div>
            </div>
          ) : (
            <div class="magi-console-body dim">{v.running ? t('the panel is deliberating…') : t('select a unit to read its reasoning')}</div>
          )}
          {(v.inquiries?.length ?? 0) > 0 ? <RaatiInquiryList inquiries={v.inquiries ?? []} /> : null}
        </div>
        {lines.length > 0 ? (
          <div class="magi-ticker" ref={tickerRef} onScroll={onTickerScroll} aria-live="polite">
            {lines.map((l, i) => (
              <div key={i} class="magi-ticker-line">
                {l}
              </div>
            ))}
          </div>
        ) : null}
        <footer class="magi-footer">
          <div class="magi-q">
            <span class="raati-dim">{t('question:')}</span> {v.question || '—'}
          </div>
          <div class="magi-access">
            <span class="raati-dim">{t('access:')}</span> {(v.when ?? '').replace('T', ' ').slice(0, 19) || 'MAGI_SYS'}
            <button class="magi-exit-hint" title={t('Exit theater (Esc)')} onClick={onExit}>
              {/* i18n-exempt — the Esc key's name */}
              <span class="magi-exit-key">esc: </span>
              {t('exit')}
            </button>
          </div>
        </footer>
      </div>
    </div>
  )
}

export function SurfaceView({
  surface,
  usage,
  onAction,
  onFetchNode,
  onListResets,
  onConsumeReset,
  onRefresh,
  onRestart,
  version,
  trusted,
  onTrust,
  models,
  auth,
}: {
  surface: Surface
  usage?: UsageInfo | null
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onListResets: () => Promise<ResetsListResult>
  onConsumeReset: (id: string) => Promise<ResetConsumeResult>
  onRefresh?: (id: string) => void
  onRestart?: () => void
  version?: string
  trusted?: boolean
  onTrust?: (trust: boolean) => void
  models?: ModelInfo[]
  auth?: AuthPaneProps
}) {
  switch (surface.kind) {
    case 'context':
      return surface.context ? (
        <ContextBody
          d={surface.context}
          usage={usage}
          onFetchNode={onFetchNode}
          onListResets={onListResets}
          onConsumeReset={onConsumeReset}
        />
      ) : null
    case 'tasks':
      return surface.tasks ? <TasksBody list={surface.tasks} onAction={onAction} /> : null
    case 'taskboard':
      return surface.task_board ? <TaskBoardBody v={surface.task_board} /> : null
    case 'worktrees':
      return (
        <WorktreesBody
          v={surface.worktrees ?? { repo_key: '' }}
          onRefresh={onRefresh ? () => onRefresh(surface.id) : undefined}
        />
      )
    case 'raati':
      return <RaatiBody v={surface.raati ?? { running: false }} onAction={onAction} models={models ?? []} />
    case 'settings':
      return surface.settings ? <SettingsBody v={surface.settings} onAction={onAction} onRestart={onRestart} version={version} /> : null
    case 'panel':
      return surface.panel ? <PanelBody id={surface.id} p={surface.panel} onAction={onAction} /> : null
    case 'widgets':
      return <WidgetBody id={surface.id} widgets={surface.widgets ?? []} onAction={onAction} />
    case 'commands':
      return <CommandsBody v={surface.commands ?? { commands: [] }} onAction={onAction} />
    case 'extensions':
      return <ExtensionsBody v={surface.extensions ?? { extensions: [] }} onAction={onAction} />
    case 'permissions':
      return <PermissionsBody v={surface.permissions ?? { mode: '' }} onAction={onAction} trusted={trusted} onTrust={onTrust} />
    case 'lore':
      return <LoreBody v={surface.lore ?? { entries: [] }} onAction={onAction} />
    case 'mcp':
      return <MCPBody v={surface.mcp ?? { servers: [] }} onAction={onAction} />
    case 'chat':
      return <ChatBody v={surface.chat ?? { bridge: { state: 'idle' } }} onAction={onAction} />
    case 'providers':
      return (
        <ProvidersBody
          v={surface.providers ?? { providers: [] }}
          flow={auth?.flow ?? null}
          busy={!!auth?.busy}
          error={auth?.error ?? ''}
          onStart={auth?.start ?? (() => {})}
          onSubmit={auth?.submit ?? (() => {})}
          onCancel={auth?.cancel ?? (() => {})}
          onLogout={auth?.logout ?? (() => {})}
          onRemoveEndpoint={auth?.removeEndpoint ?? (() => {})}
        />
      )
    default:
      return <div class="pick-empty">{t('unsupported pane')}</div>
  }
}

// AuthPaneProps is everything the Providers pane needs to drive a login. Bundled
// because it threads App → PaneHost → SurfaceView → ProvidersBody, and seven
// loose props at each hop is how a prop gets dropped silently.
interface AuthPaneProps {
  flow: AuthFlowStep | null
  busy: boolean
  error: string
  start: (provider: string, method: string) => void
  submit: (values: Record<string, string>) => void
  cancel: () => void
  logout: (provider: string) => void
  // Forgets a named endpoint's DEFINITION, not just its key — a separate verb
  // from logout on purpose. See ProviderInfo.endpoint.
  removeEndpoint: (id: string) => void
}

// authMessage unwraps a ctrlproto error for display. The daemon's text is the
// useful part — "that key was not accepted", "this login was superseded" — and
// inventing our own would be strictly less informative.
export function authMessage(e: unknown): string {
  const m = e instanceof Error ? e.message : String(e)
  // Frames arrive as "code: message"; the code is for us, the message is for them.
  const i = m.indexOf(': ')
  return i > 0 ? m.slice(i + 2) : m
}

// ProvidersBody renders the daemon's MODEL-PROVIDER credentials, and — when the
// daemon will serve it — drives a login.
//
// It exists to explain an absence. The model picker silently omits every provider
// terva was never logged into, and an expired subscription arrives as a turn that
// fails for no stated reason. Both look like bugs and are actually missing
// credentials, and this is where you find that out and fix it.
//
// It is deliberately not called "Login": in this UI that word already means the
// bearer-token form that let you in. This is about who the DAEMON can talk to.
export function ProvidersBody({
  v,
  flow,
  onStart,
  onSubmit,
  onCancel,
  onLogout,
  onRemoveEndpoint,
  busy,
  error,
}: {
  v: ProvidersView
  flow: AuthFlowStep | null
  onStart: (provider: string, method: string) => void
  onSubmit: (values: Record<string, string>) => void
  onCancel: () => void
  onLogout: (provider: string) => void
  onRemoveEndpoint: (id: string) => void
  busy: boolean
  error: string
}) {
  const all = v.providers ?? []
  // The operator's own servers are their own group. Sorted by credential they
  // would land in "not signed in" — a keyless local server has no credential and
  // never will — and be offered a login it does not need, next to providers it has
  // nothing to do with.
  const endpoints = all.filter((p) => p.endpoint)
  const shipped = all.filter((p) => !p.endpoint)
  const active = shipped.filter((p) => p.method)
  const available = shipped.filter((p) => !p.method)
  const canLogin = !!v.can_login
  // Removing an endpoint deletes terva's only record of a machine — which host,
  // which port, which context window. Cheap to re-type only if you remember it, so
  // the button asks once.
  const [confirmRemove, setConfirmRemove] = useState('')

  const methodLabel = (p: ProviderInfo) => {
    if (p.method === 'oauth') return t('subscription')
    if (p.method === 'apikey') return t('api key')
    return ''
  }

  // A flow in progress owns the pane. Leaving the provider list visible behind a
  // half-finished login only invites starting a second one — and a second one
  // SUPERSEDES the first, which is exactly the collision the flow handle exists to
  // make refusable rather than silent.
  if (flow) {
    return (
      <div class="ext-body">
        <AuthStepForm step={flow} onSubmit={onSubmit} onCancel={onCancel} busy={busy} error={error} />
      </div>
    )
  }

  return (
    <div class="ext-body">
      {active.length === 0 ? (
        <div class="prov-warn">
          {t('terva has no model-provider credentials. It cannot reach any model until it does.')}
        </div>
      ) : null}
      {error ? <div class="prov-warn">{error}</div> : null}

      {active.map((p) => (
        <div key={p.id} class="ext-card">
          <div class="ext-head">
            <span class="ext-name">{p.label}</span>
            <span class={`ext-badge${p.expired ? ' s-failed' : ' s-running'}`}>
              {p.expired ? t('expired') : methodLabel(p)}
            </span>
          </div>
          {p.expired ? (
            <div class="ext-desc">
              {canLogin
                ? t('This subscription has expired. Sign in again to repair it.')
                : t('This subscription has expired. Run /login in the terminal to sign in again.')}
            </div>
          ) : null}
          {p.base_url ? (
            <div class="ext-meta">
              {p.base_url}
              {p.model ? ` · ${p.model}` : ''}
            </div>
          ) : null}
          {p.expiry && !p.expired ? (
            <div class="ext-meta">{t('expires %s', localInstant(p.expiry))}</div>
          ) : null}
          {canLogin ? (
            <div class="prov-actions">
              {p.expired && p.offers?.includes('oauth') ? (
                <button class="btn" onClick={() => onStart(p.id, 'oauth')}>
                  {t('Sign in again')}
                </button>
              ) : null}
              <button class="btn ghost" onClick={() => onLogout(p.id)}>
                {t('Sign out')}
              </button>
            </div>
          ) : null}
        </div>
      ))}

      {endpoints.length ? (
        <>
          <div class="prov-note">
            {t('Your OpenAI-compatible endpoints — each is its own provider and lists its own models.')}
          </div>
          {endpoints.map((p) => (
            <div key={p.id} class="ext-card">
              <div class="ext-head">
                <span class="ext-name">{p.label}</span>
                <span class="ext-badge s-running">{t('endpoint')}</span>
              </div>
              <div class="ext-meta">{p.base_url}</div>
              {canLogin ? (
                <div class="prov-actions">
                  {confirmRemove === p.id ? (
                    <>
                      {/* Say what will be lost, not just "are you sure". The base URL is
                          the thing nobody remembers. */}
                      <span class="prov-help">{t('Forget %s? terva keeps no other record of it.', p.base_url ?? p.id)}</span>
                      <button
                        class="btn"
                        onClick={() => {
                          setConfirmRemove('')
                          onRemoveEndpoint(p.id)
                        }}
                      >
                        {t('Forget it')}
                      </button>
                      <button class="btn ghost" onClick={() => setConfirmRemove('')}>
                        {t('Cancel')}
                      </button>
                    </>
                  ) : (
                    <button class="btn ghost" onClick={() => setConfirmRemove(p.id)}>
                      {t('Remove')}
                    </button>
                  )}
                </div>
              ) : null}
            </div>
          ))}
        </>
      ) : null}

      {available.length ? (
        <>
          <div class="prov-note">
            {canLogin
              ? t('Not signed in:')
              : t('Not signed in. terva can only be signed in from the terminal — run /login there.')}
          </div>
          {available.map((p) => (
            <div key={p.id} class="ext-card muted">
              <div class="ext-head">
                <span class="ext-name">{p.label}</span>
                {p.offers?.includes('env') ? <span class="ext-badge">{t('environment')}</span> : null}
              </div>
              {p.note?.length ? <pre class="prov-env">{p.note.join('\n')}</pre> : null}
              {canLogin && !p.offers?.includes('env') ? (
                <div class="prov-actions">
                  {p.offers?.includes('oauth') ? (
                    <button class="btn ghost" onClick={() => onStart(p.id, 'oauth')}>
                      {t('Subscription')}
                    </button>
                  ) : null}
                  {p.offers?.includes('apikey') ? (
                    <button class="btn ghost" onClick={() => onStart(p.id, 'apikey')}>
                      {t('API key')}
                    </button>
                  ) : null}
                </div>
              ) : null}
            </div>
          ))}
        </>
      ) : null}
    </div>
  )
}

// ChatBody renders the chat-connector pane: the live bridge and one row per
// registered service. Connect is async — the daemon returns immediately and the
// pane converges over surface_updated — so "connecting" is a real state here,
// not a spinner we invent.
//
// The mirror is bound to the session it was connected from; it does not follow
// this tab. That is why the connected card names its session explicitly.
export function ChatBody({
  v,
  onAction,
}: {
  v: ChatView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const b = v.bridge ?? { state: 'idle' }
  const busy = b.state === 'connecting'
  const connected = b.state === 'connected'

  const tag = (s: ChatServiceInfo) => {
    const parts: string[] = []
    if (s.kind) parts.push(s.kind)
    if (s.dev) parts.push('dev')
    return parts.join(', ')
  }

  return (
    <div class="pane-body">
      {b.state === 'error' && b.error ? <div class="pane-error">{b.error}</div> : null}

      {connected ? (
        <div class="kv-card">
          <div class="kv-row">
            <span class="kv-k">{t('connector')}</span>
            <span class="kv-v">{b.connector}</span>
          </div>
          {b.username ? (
            <div class="kv-row">
              <span class="kv-k">{t('bot')}</span>
              <span class="kv-v">@{b.username}</span>
            </div>
          ) : null}
          <div class="kv-row">
            <span class="kv-k">{t('paired')}</span>
            <span class="kv-v">
              {b.paired_id ? b.paired_id : t('awaiting /start from your phone')}
            </span>
          </div>
          {b.session ? (
            <div class="kv-row">
              <span class="kv-k">{t('mirroring session')}</span>
              <span class="kv-v mono">{b.session}</span>
            </div>
          ) : null}
          <div class="kv-actions">
            <button onClick={() => onAction('chat', 'disconnect')}>{t('disconnect')}</button>
            <button onClick={() => onAction('chat', 'rebind')}>{t('mirror this session')}</button>
          </div>
        </div>
      ) : null}

      {v.daemon_pid ? (
        <div class="pane-note">
          {t('a terva bot daemon is running (pid %s) — stop it before connecting', String(v.daemon_pid))}
        </div>
      ) : null}

      {!connected
        ? (v.services ?? []).map((s) => (
            <div class="kv-row" key={s.name}>
              <span class="kv-k">
                {s.name}
                {tag(s) ? <span class="dim"> ({tag(s)})</span> : null}
              </span>
              <span class="kv-v">
                {!s.configured ? (
                  <span class="dim">{t('not configured — run `terva bot setup`')}</span>
                ) : (
                  <button
                    disabled={busy || !!v.daemon_pid}
                    onClick={() => onAction('chat', 'connect', { name: s.name })}
                  >
                    {busy ? t('connecting…') : s.paired ? t('connect') : t('connect & pair')}
                  </button>
                )}
              </span>
            </div>
          ))
        : null}

      {!connected && (v.services ?? []).length === 0 ? (
        <div class="pick-empty">{t('no chat connectors compiled into this binary')}</div>
      ) : null}
    </div>
  )
}

// CommandsBody renders every extension-registered command as a button, grouped
// by extension — the web's take on the TUI's slash menu (no command line here).
// Clicking runs it: surface.action{action:"run", args:{name}}. The daemon
// applies the command's response (opens a panel, submits a prompt, or posts a
// one-shot note back into the conversation).
export function CommandsBody({
  v,
  onAction,
}: {
  v: CommandsView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const cmds = v.commands ?? []
  if (!cmds.length) return <div class="pick-empty">{t('No extension commands.')}</div>
  // Preserve the server's (ext, name) ordering while collecting per-ext groups.
  const groups: { ext: string; items: CommandsView['commands'] }[] = []
  for (const c of cmds) {
    let g = groups.find((x) => x.ext === c.ext)
    if (!g) {
      g = { ext: c.ext, items: [] }
      groups.push(g)
    }
    g.items.push(c)
  }
  return (
    <div class="cmd-body">
      {groups.map((g) => (
        <div key={g.ext} class="cmd-group">
          <div class="cmd-ext">{g.ext}</div>
          {g.items.map((c) => (
            <button
              key={c.name}
              class="cmd-item"
              title={c.description}
              onClick={() => onAction('commands', 'run', { name: c.name })}
            >
              <span class="cmd-name">/{c.name}</span>
              {c.description && <span class="cmd-desc">{c.description}</span>}
            </button>
          ))}
        </div>
      ))}
    </div>
  )
}

// ExtensionsBody renders the extension-management pane (kind=extensions): one
// card per installed/loaded extension with a status badge, version/scope, tool +
// command counts, any crash reason, and an enable/disable toggle (persisted to
// the project + applied live). Gated (untrusted) extensions can't be toggled on.
export function ExtensionsBody({
  v,
  onAction,
}: {
  v: ExtensionsView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const exts = v.extensions ?? []
  if (!exts.length) return <div class="pick-empty">{t('No extensions installed.')}</div>
  const statusLabel: Record<string, string> = {
    running: t('running'),
    stopped: t('stopped'),
    disabled: t('disabled'),
    gated: t('gated'),
  }
  return (
    <div class="ext-body">
      {exts.map((e) => (
        <div key={e.name} class="ext-card">
          <div class="ext-head">
            <span class="ext-name">{e.name}</span>
            {e.version && <span class="ext-ver">{e.version}</span>}
            <span class={`ext-badge s-${e.status}`}>{statusLabel[e.status] ?? e.status}</span>
            {e.status !== 'gated' && (
              <button
                class={`set-toggle${e.enabled ? ' on' : ''}`}
                role="switch"
                aria-checked={e.enabled}
                title={e.enabled ? t('Disable') : t('Enable')}
                onClick={() => onAction('extensions', 'toggle', { name: e.name, enabled: String(!e.enabled) })}
              >
                <span class="set-knob" />
              </button>
            )}
          </div>
          {e.description && <div class="ext-desc">{e.description}</div>}
          <div class="ext-meta">
            {e.scope && <span>{e.scope}</span>}
            {e.language && <span>{e.language}</span>}
            <span>{tn(e.tools ?? 0, '%d tool', '%d tools')}</span>
            <span>{tn(e.commands ?? 0, '%d command', '%d commands')}</span>
          </div>
          {e.note && <div class="ext-note">{e.note}</div>}
          {e.has_config && (e.config ?? []).length > 0 && (
            <ExtensionConfigForm
              name={e.name}
              fields={e.config ?? []}
              onSave={(values) =>
                onAction('extensions', 'set_config', { name: e.name, values: JSON.stringify(values) })
              }
            />
          )}
        </div>
      ))}
    </div>
  )
}

// ExtensionConfigForm is the browser's half of an extension's declared config.
//
// It edits WORKING STRINGS and submits them as-is; the daemon types each one
// against the schema before persisting. That split is deliberate — three
// clients each deciding whether a field is a bool is three chances to write a
// config the extension cannot read.
//
// A secret seeds EMPTY even when one is stored, and the placeholder says so.
// The stored value is never sent here, and submitting the field blank leaves it
// untouched on the host — which is what lets someone change the field next to a
// secret without ever being handed it.
export function ExtensionConfigForm({
  name,
  fields,
  onSave,
}: {
  name: string
  fields: ExtensionConfigField[]
  onSave: (values: Record<string, string>) => void
}) {
  const [open, setOpen] = useState(false)
  const [working, setWorking] = useState<Record<string, string>>({})
  const [saved, setSaved] = useState(false)

  // Seed from the server's values each time the form opens, so a re-open after
  // a save shows what is actually stored rather than the last edit.
  const start = () => {
    const next: Record<string, string> = {}
    for (const f of fields) next[f.key] = f.secret ? '' : (f.saved ?? '')
    setWorking(next)
    setSaved(false)
    setOpen(true)
  }
  const set = (k: string, v: string) => setWorking((w) => ({ ...w, [k]: v }))

  if (!open)
    return (
      <div class="ext-cfg-row">
        <button class="btn sm" onClick={start}>
          {t('Configure')}
        </button>
      </div>
    )

  return (
    <div class="ext-cfg">
      {fields.map((f) => (
        <div class="ext-cfg-field" key={f.key}>
          <label class="ext-cfg-label" for={`cfg-${name}-${f.key}`}>
            {f.label || f.key}
            {f.required && <span class="ext-cfg-req">*</span>}
          </label>
          {f.type === 'bool' ? (
            <button
              id={`cfg-${name}-${f.key}`}
              class={`set-toggle${working[f.key] === 'true' ? ' on' : ''}`}
              role="switch"
              aria-checked={working[f.key] === 'true'}
              onClick={() => set(f.key, working[f.key] === 'true' ? 'false' : 'true')}
            >
              <span class="set-knob" />
            </button>
          ) : f.type === 'select' ? (
            <select
              id={`cfg-${name}-${f.key}`}
              class="set-input"
              value={working[f.key] ?? ''}
              onChange={(ev) => set(f.key, (ev.target as HTMLSelectElement).value)}
            >
              <option value="">{f.default ? t('default (%s)', f.default) : t('unset')}</option>
              {(f.options ?? []).map((o) => (
                <option value={o}>{o}</option>
              ))}
            </select>
          ) : (
            <input
              id={`cfg-${name}-${f.key}`}
              class="set-input"
              type={f.secret ? 'password' : f.type === 'int' ? 'number' : 'text'}
              value={working[f.key] ?? ''}
              placeholder={
                f.secret
                  ? f.has_saved
                    ? t('saved — leave blank to keep')
                    : t('not set')
                  : f.default
                    ? t('default: %s', f.default)
                    : ''
              }
              onInput={(ev) => set(f.key, (ev.target as HTMLInputElement).value)}
            />
          )}
          {f.description && <div class="ext-cfg-desc">{f.description}</div>}
        </div>
      ))}
      <div class="ext-cfg-actions">
        <button
          class="btn sm primary"
          onClick={() => {
            onSave(working)
            setSaved(true)
            setOpen(false)
          }}
        >
          {t('Save')}
        </button>
        <button class="btn sm" onClick={() => setOpen(false)}>
          {t('Cancel')}
        </button>
        {saved && <span class="ext-cfg-ok">{t('saved')}</span>}
      </div>
    </div>
  )
}

// PermissionsBody renders the permissions inspector (kind=permissions): the
// workspace trust state (with a grant/revoke control), the approval mode, the
// session's live "always-allow" grants (each revocable), and the compiled rules
// (read-only). The mode's setter lives in Settings. Trust is workspace-global
// and gates whether project-scoped rules/lore/extensions load, so it heads the
// pane — granting it here is what unlocks the project scope elsewhere.
export function PermissionsBody({
  v,
  onAction,
  trusted,
  onTrust,
}: {
  v: PermissionsView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  trusted?: boolean
  onTrust?: (trust: boolean) => void
}) {
  const rules = v.rules ?? []
  const grants = v.grants ?? []
  return (
    <div class="perm-body">
      {onTrust && (
        <div class="perm-mode">
          <span class="perm-mode-label">{t('Workspace trust')}</span>
          <span class={`perm-mode-val${trusted ? '' : ' warn'}`}>{trusted ? t('trusted') : t('untrusted')}</span>
          <button class="btn sm" onClick={() => onTrust(!trusted)}>
            {trusted ? t('Untrust') : t('Trust workspace')}
          </button>
        </div>
      )}

      <div class="perm-mode">
        <span class="perm-mode-label">{t('Approval mode')}</span>
        <span class="perm-mode-val">{v.mode}</span>
      </div>

      {(v.allow_all || grants.length > 0) && (
        <div class="perm-section">
          <div class="perm-head">
            <span>{t('Allowed this session')}</span>
            <button class="btn sm" onClick={() => onAction('permissions', 'revoke_all', {})}>
              {t('Revoke all')}
            </button>
          </div>
          {v.allow_all && <div class="perm-grant warn">{t('All tools auto-approved this session')}</div>}
          {grants.map((tool) => (
            <div key={tool} class="perm-grant">
              <span class="perm-grant-name">{tool}</span>
              <button class="btn sm" title={t('Revoke')} onClick={() => onAction('permissions', 'revoke', { tool })}>
                {t('Revoke')}
              </button>
            </div>
          ))}
        </div>
      )}

      <div class="perm-section">
        <div class="perm-head">
          <span>{t('Rules')}</span>
        </div>
        {rules.length === 0 ? (
          <div class="perm-empty">{t('No rules configured.')}</div>
        ) : (
          rules.map((r, i) => (
            <div key={i} class="perm-rule">
              <span class={`perm-dec d-${r.decision}`}>{r.decision}</span>
              <span class="perm-tool">
                {r.tool}
                {r.args && <span class="perm-args">{r.args}</span>}
              </span>
              {r.source && <span class="perm-src">{r.source}</span>}
              {r.removable && (
                <button
                  class="perm-x"
                  title={t('Remove')}
                  aria-label={t('Remove')}
                  onClick={() =>
                    onAction('permissions', 'remove_rule', {
                      tool: r.tool,
                      args: r.args ?? '',
                      decision: r.decision,
                      scope: r.source === 'project' ? 'project' : 'user',
                    })
                  }
                >
                  ×
                </button>
              )}
            </div>
          ))
        )}
        <AddRuleForm onAction={onAction} />
      </div>
    </div>
  )
}

// AddRuleForm adds a user permission rule (tool + decision + optional args). The
// rule persists to user config and applies live to every session.
export function AddRuleForm({
  onAction,
}: {
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const [tool, setTool] = useState('')
  const [decision, setDecision] = useState('allow')
  const [args, setArgs] = useState('')
  const [scope, setScope] = useState('user')
  const setScopeSafe = (sc: string) => {
    setScope(sc)
    // Project rules are restrict-only (self-approval ban): no allow.
    if (sc === 'project' && decision === 'allow') setDecision('deny')
  }
  const add = () => {
    if (!tool.trim()) return
    onAction('permissions', 'add_rule', { tool: tool.trim(), decision, args: args.trim(), scope })
    setTool('')
    setArgs('')
  }
  return (
    <div class="perm-add">
      <input
        class="perm-in"
        value={tool}
        placeholder={t('tool (e.g. bash or mcp_*)')}
        onInput={(e) => setTool((e.target as HTMLInputElement).value)}
      />
      <select class="perm-in" value={decision} onChange={(e) => setDecision((e.target as HTMLSelectElement).value)}>
        {scope !== 'project' && <option value="allow">{t('allow')}</option>}
        <option value="deny">{t('deny')}</option>
        <option value="ask">{t('ask')}</option>
      </select>
      <select class="perm-in" value={scope} onChange={(e) => setScopeSafe((e.target as HTMLSelectElement).value)}>
        <option value="user">{t('user')}</option>
        <option value="project">{t('project')}</option>
      </select>
      <input
        class="perm-in"
        value={args}
        placeholder={t('args regex (optional)')}
        onInput={(e) => setArgs((e.target as HTMLInputElement).value)}
      />
      <button class="btn sm" onClick={add}>
        {t('Add rule')}
      </button>
    </div>
  )
}

// LoreBody renders the lore inspector/editor (kind=lore): one card per entry
// (triggers, source, content preview), with edit/delete on web-managed user
// entries and an add/edit form. Edits show in the pane immediately; the actual
// per-turn injection applies to new sessions.
type LoreDraft = { name: string; keys: string; constant: boolean; content: string; scope: string; existing: boolean }
export function LoreBody({
  v,
  onAction,
}: {
  v: LoreView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const entries = v.entries ?? []
  const canProject = !!v.can_project
  const [draft, setDraft] = useState<LoreDraft | null>(null)
  const openNew = () => setDraft({ name: '', keys: '', constant: false, content: '', scope: 'user', existing: false })
  const openEdit = (e: LoreEntry) =>
    setDraft({
      name: e.name,
      keys: (e.keys ?? []).join(', '),
      constant: !!e.constant,
      content: e.content ?? '',
      scope: e.scope ?? 'user',
      existing: true,
    })
  const save = () => {
    if (!draft || !draft.name.trim() || !draft.content.trim()) return
    onAction('lore', 'save', {
      name: draft.name.trim(),
      keys: draft.keys,
      constant: String(draft.constant),
      content: draft.content,
      scope: draft.scope,
    })
    setDraft(null)
  }
  return (
    <div class="lore-body">
      {!entries.length && !draft && <div class="pick-empty">{t('No lore entries.')}</div>}
      {entries.map((e, i) => (
        <div key={i} class="lore-card">
          <div class="lore-head">
            <span class="lore-name">{e.name}</span>
            {e.constant ? (
              <span class="lore-badge">{t('always')}</span>
            ) : (
              <span class="lore-keys">{(e.keys ?? []).map((k) => <span class="lore-key">{k}</span>)}</span>
            )}
            {e.editable && (
              <span class="lore-actions">
                <button class="perm-x" title={t('Edit')} aria-label={t('Edit')} onClick={() => openEdit(e)}>
                  ✎
                </button>
                <button
                  class="perm-x"
                  title={t('Remove')}
                  aria-label={t('Remove')}
                  onClick={() => onAction('lore', 'delete', { name: e.name, scope: e.scope ?? 'user' })}
                >
                  ×
                </button>
              </span>
            )}
          </div>
          {e.content && <div class="lore-preview">{e.content.length > 200 ? e.content.slice(0, 200) + '…' : e.content}</div>}
          {e.source && <div class="lore-src">{e.source}</div>}
        </div>
      ))}

      {draft ? (
        <div class="lore-edit">
          <input
            class="perm-in"
            value={draft.name}
            placeholder={t('name')}
            onInput={(e) => setDraft({ ...draft, name: (e.target as HTMLInputElement).value })}
          />
          <input
            class="perm-in"
            value={draft.keys}
            placeholder={t('trigger keywords, comma-separated')}
            disabled={draft.constant}
            onInput={(e) => setDraft({ ...draft, keys: (e.target as HTMLInputElement).value })}
          />
          <label class="lore-const">
            <input
              type="checkbox"
              checked={draft.constant}
              onChange={(e) => setDraft({ ...draft, constant: (e.target as HTMLInputElement).checked })}
            />
            {t('always active (no keywords)')}
          </label>
          {canProject && (
            <select
              class="perm-in"
              value={draft.scope}
              disabled={draft.existing}
              onChange={(e) => setDraft({ ...draft, scope: (e.target as HTMLSelectElement).value })}
            >
              <option value="user">{t('user')}</option>
              <option value="project">{t('project')}</option>
            </select>
          )}
          <textarea
            class="perm-in lore-content"
            rows={5}
            value={draft.content}
            placeholder={t('lore content…')}
            onInput={(e) => setDraft({ ...draft, content: (e.target as HTMLTextAreaElement).value })}
          />
          <div class="lore-edit-btns">
            <button class="btn sm primary" onClick={save}>
              {t('Save')}
            </button>
            <button class="btn sm" onClick={() => setDraft(null)}>
              {t('Cancel')}
            </button>
          </div>
        </div>
      ) : (
        <button class="btn sm lore-add" onClick={openNew}>
          {t('Add lore')}
        </button>
      )}
    </div>
  )
}

// MCPBody renders the MCP-management pane (kind=mcp): one card per server with a
// status badge, scope, tool count, any startup error, and an enable/disable
// toggle (reuses the extension card styles). Toggling restarts/stops the shared
// server and rebuilds every session's tools.
export function MCPBody({
  v,
  onAction,
}: {
  v: MCPView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const servers = v.servers ?? []
  if (!servers.length) return <div class="pick-empty">{t('No MCP servers configured.')}</div>
  const statusLabel: Record<string, string> = {
    running: t('running'),
    stopped: t('stopped'),
    disabled: t('disabled'),
    gated: t('gated'),
    failed: t('failed'),
  }
  return (
    <div class="ext-body">
      {servers.map((m) => (
        <div key={m.name} class="ext-card">
          <div class="ext-head">
            <span class="ext-name">{m.name}</span>
            <span class={`ext-badge s-${m.status}`}>{statusLabel[m.status] ?? m.status}</span>
            {m.status !== 'gated' && (
              <button
                class={`set-toggle${m.enabled ? ' on' : ''}`}
                role="switch"
                aria-checked={m.enabled}
                title={m.enabled ? t('Disable') : t('Enable')}
                onClick={() =>
                  onAction('mcp', 'toggle', { name: m.name, enabled: String(!m.enabled), scope: m.scope ?? 'global' })
                }
              >
                <span class="set-knob" />
              </button>
            )}
          </div>
          {m.description && <div class="ext-desc">{m.description}</div>}
          <div class="ext-meta">
            {m.scope && <span>{m.scope}</span>}
            <span>{tn(m.tools ?? 0, '%d tool', '%d tools')}</span>
          </div>
          {m.note && <div class="ext-note">{m.note}</div>}
        </div>
      ))}
    </div>
  )
}

// WidgetBody renders an extension's generic widget tree (kind=widgets) natively:
// each widget is a semantic node (meter → a bar, keyvalue → rows, …), so an
// extension gets a rich pane without any per-extension client code. Actions fire
// surface.action{action:"action", id:<action_id>} back to the extension.
export function WidgetBody({
  id,
  widgets,
  onAction,
}: {
  id: string
  widgets: Widget[]
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  if (!widgets.length) return <div class="pick-empty">{t('empty pane')}</div>
  return (
    <div class="wg-body">
      {widgets.map((w, i) => (
        <WidgetNode key={i} w={w} onAction={(aid) => onAction(id, 'action', { id: aid })} />
      ))}
    </div>
  )
}

export function WidgetNode({ w, onAction }: { w: Widget; onAction: (actionID: string) => void }): VNode | null {
  const tone = w.tone && w.tone !== 'default' ? ` t-${w.tone}` : ''
  switch (w.type) {
    case 'heading':
      return <div class={`wg-heading lvl${Math.min(Math.max(w.level ?? 2, 1), 4)}`}>{w.text}</div>
    case 'text':
      return <div class={`wg-text${tone}`}>{w.text}</div>
    case 'note':
      return <div class={`wg-note${tone}`}>{w.text}</div>
    case 'divider':
      return <div class="wg-divider" />
    case 'meter': {
      const max = w.max && w.max > 0 ? w.max : 0
      const pct = max > 0 ? Math.min(100, ((w.value ?? 0) / max) * 100) : 0
      const hot = w.tone === 'danger' || pct >= 85
      return (
        <div class="wg-meter">
          <div class="wg-meter-head">
            <span class="wg-meter-label">{w.label}</span>
            <span class="wg-meter-val">
              {humanCount(w.value ?? 0)}
              {max > 0 ? ' / ' + humanCount(max) : ''}
              {w.unit ? ' ' + w.unit : ''}
            </span>
          </div>
          {max > 0 && (
            <div class="ctx-bar">
              <div class={`ctx-bar-fill${hot ? ' hot' : ''}`} style={{ width: pct + '%' }} />
            </div>
          )}
        </div>
      )
    }
    case 'keyvalue':
      return (
        <div class="wg-kv">
          {(w.rows ?? []).map((r, i) => (
            <div class="wg-kv-row" key={i}>
              <span class="wg-kv-key">{r.key}</span>
              <span class={`wg-kv-val${r.mono ? ' mono' : ''}`}>{r.value}</span>
              {r.note && <span class="wg-kv-note">{r.note}</span>}
            </div>
          ))}
        </div>
      )
    case 'table':
      return (
        <div class="wg-table-wrap">
          <table class="wg-table">
            {(w.columns ?? []).length > 0 && (
              <thead>
                <tr>{(w.columns ?? []).map((c, i) => <th key={i}>{c}</th>)}</tr>
              </thead>
            )}
            <tbody>
              {(w.cells ?? []).map((row, i) => (
                <tr key={i}>{row.map((cell, j) => <td key={j}>{cell}</td>)}</tr>
              ))}
            </tbody>
          </table>
        </div>
      )
    case 'list':
      return (
        <div class="wg-list">
          {(w.items ?? []).map((it, i) =>
            it.action_id ? (
              <button key={i} class={`wg-list-item act t-${it.tone ?? 'default'}`} onClick={() => onAction(it.action_id!)}>
                <span class="wg-list-text">{it.text}</span>
                {it.note && <span class="wg-list-note">{it.note}</span>}
              </button>
            ) : (
              <div key={i} class={`wg-list-item t-${it.tone ?? 'default'}`}>
                <span class="wg-list-text">{it.text}</span>
                {it.note && <span class="wg-list-note">{it.note}</span>}
              </div>
            ),
          )}
        </div>
      )
    case 'action':
      return (
        <button
          class={`btn wg-action${w.tone === 'danger' ? ' danger' : w.tone === 'ok' ? ' primary' : ''}`}
          disabled={!w.action_id}
          onClick={() => w.action_id && onAction(w.action_id)}
        >
          {w.label ?? w.text}
        </button>
      )
    case 'group': {
      return <WidgetGroup w={w} onAction={onAction} />
    }
    default:
      return null
  }
}

// WidgetGroup is a labelled, optionally-collapsible container (the tool-group
// shape). Its own useState is why it's a component, not a switch branch.
export function WidgetGroup({ w, onAction }: { w: Widget; onAction: (actionID: string) => void }) {
  const [open, setOpen] = useState(!(w.tone === 'muted'))
  const collapsible = !!w.label
  return (
    <div class="wg-group">
      {w.label &&
        (collapsible ? (
          <button class={`wg-group-head${open ? ' open' : ''}`} onClick={() => setOpen((o) => !o)}>
            <span class="tg-caret">{open ? '▾' : '▸'}</span>
            {w.label}
          </button>
        ) : (
          <div class="wg-group-head static">{w.label}</div>
        ))}
      {open && (
        <div class="wg-group-body">
          {(w.children ?? []).map((c, i) => (
            <WidgetNode key={i} w={c} onAction={onAction} />
          ))}
        </div>
      )}
    </div>
  )
}

// UsageSummary renders the shared usage picture: context gauge + cumulative
// tokens/cost + subscription windows. Used by both the context and usage panes.
export function UsageSummary({
  tokens,
  window,
  estimated,
  cumulative,
  subscription,
  windows,
  cache,
}: {
  tokens: number
  window: number
  estimated: boolean
  cumulative: WireUsage
  subscription?: boolean
  windows?: UsageWindow[]
  cache?: ContextCache
}) {
  const pct = window > 0 ? Math.min(100, (tokens / window) * 100) : 0
  const note = estimated && tokens === 0 ? ' — ' + t('no turn yet') : estimated ? ' — ' + t('estimated') : ''
  return (
    <>
      {window > 0 && (
        <div class="ctx-bar-wrap">
          <div class="ctx-bar">
            <div class={`ctx-bar-fill${pct >= 85 ? ' hot' : ''}`} style={{ width: pct + '%' }} />
          </div>
          <div class="ctx-bar-label">
            {estimated ? '~' : ''}
            {humanCount(tokens)} / {humanCount(window)} {t('tokens')} ({pct.toFixed(0)}%){note}
          </div>
        </div>
      )}
      <div class="ctx-usage">
        <span title={t('input tokens')}>↑ {humanCount(cumulative.input || 0)}</span>
        <span title={t('output tokens')}>↓ {humanCount(cumulative.output || 0)}</span>
        {cumulative.cache_read ? <span title={t('cache read')}>⚡ {humanCount(cumulative.cache_read)}</span> : null}
        <span class="ctx-usage-cost">
          ${(cumulative.cost_usd || 0).toFixed(4)}
          {subscription ? ' (sub)' : ''}
        </span>
      </div>
      {windows && windows.length > 0 && (
        <div class="ctx-windows">
          {windows.map((w) => (
            <WindowRow key={w.label} w={w} />
          ))}
        </div>
      )}
      <CacheSummary cache={cache} />
    </>
  )
}

// hitRate is cache reads over the whole prompt — the same definition the server
// uses (provider.Usage.CacheHitRate), including the "no prompt at all" case,
// which must not read as a 0% miss.
export function hitRate(u: WireUsage): number | null {
  const prompt = (u.input || 0) + (u.cache_read || 0) + (u.cache_write || 0)
  if (prompt <= 0) return null
  return (u.cache_read || 0) / prompt
}

// CacheSummary is the prompt-cache reading: how much of what the model read came
// from cache, what that was worth, and how it moved request by request.
//
// It sits below the gauge and the totals because it explains them. A 180k
// context that costs pennies and a 180k context that costs dollars look
// identical on every other row of this pane; the difference is entirely here.
export function CacheSummary({ cache }: { cache?: ContextCache }) {
  // A server that predates the field sends nothing. Rendering an empty cache
  // would report a working cache as dead.
  if (!cache) return null

  const sessPrompt = (cache.session.input || 0) + (cache.session.cache_read || 0) + (cache.session.cache_write || 0)
  if (!cache.supported) {
    return (
      <div class="ctx-cache">
        <div class="ctx-section-label">{t('Prompt cache')}</div>
        <div class="ctx-cache-none">
          {sessPrompt === 0
            ? t('no requests yet')
            : /* Real traffic and no cache reported: an endpoint without a prefix
                 cache, or prompts under its cacheable minimum. Saying "0%" here
                 would send someone hunting a cache that does not exist. */
              t('this provider reported no cache activity')}
        </div>
      </div>
    )
  }

  const rate = hitRate(cache.session) ?? 0
  const pct = rate * 100
  const saved = cache.session.cache_saved_usd || 0
  const last = cache.last_request
  const lastRate = hitRate(last)
  const recent = cache.recent ?? []

  return (
    <div class="ctx-cache">
      <div class="ctx-section-label">{t('Prompt cache')}</div>
      <div class="ctx-bar-wrap">
        <div class="ctx-bar">
          {/* Polarity is inverted from the context gauge next to it: a FULL bar
              is the good state here, and the alarm colour belongs at the empty
              end. */}
          <div class={`ctx-bar-fill${pct < 50 ? ' hot' : ''}`} style={{ width: pct + '%' }} />
        </div>
        <div class="ctx-bar-label">
          {t('%s of the prompt served from cache, this session', pct.toFixed(0) + '%')}
        </div>
      </div>
      <div class="ctx-usage">
        {lastRate !== null && (
          // Words, not glyphs. The three shares round to similar-looking
          // numbers on a steady turn ("2k" written, "2k" fresh), and a row of
          // bare counts behind ⚡/✎/◦ is unreadable at a glance however good
          // the tooltips are.
          <>
            <span title={t('the last request')}>{t('%s read', humanCount(last.cache_read || 0))}</span>
            {(last.cache_write || 0) > 0 && <span>{t('%s written', humanCount(last.cache_write || 0))}</span>}
            <span>{t('%s fresh', humanCount(last.input || 0))}</span>
          </>
        )}
        {saved !== 0 && (
          // A cache that never gets read back costs 25% MORE than no cache at
          // all, so this figure is signed — and "saved -$0.42" is a phrase that
          // reads as a saving. Change the words, not just the sign.
          <span class={`ctx-usage-cost${saved < 0 ? ' bad' : ''}`}>
            {saved > 0
              ? t('saved $%s', saved.toFixed(2))
              : t('cost $%s extra', Math.abs(saved).toFixed(2))}
          </span>
        )}
      </div>
      {recent.length > 1 && (
        <>
          <CacheStrip samples={recent} />
          <div class="ctx-bar-label">{t('hit rate over the last %d requests', recent.length)}</div>
        </>
      )}
    </div>
  )
}

// CacheStrip draws one bar per recent request, oldest left.
//
// The average is already on the line above; what this adds is WHERE. A prefix
// change — a model switch, an extension reload, a tool set that moved — shows up
// as one short bar in an otherwise full strip, and that bar dates the
// invalidation to a request you can still remember making.
export function CacheStrip({ samples }: { samples: CacheSample[] }) {
  return (
    <div class="ctx-cache-strip" role="img" aria-label={t('cache hit rate over the last %d requests', samples.length)}>
      {samples.map((s, i) => {
        const pct = Math.max(0, Math.min(1, s.hit_rate)) * 100
        return (
          <div
            key={i}
            class="ctx-cache-bar"
            title={t('%s hit · %s prompt tokens', pct.toFixed(0) + '%', humanCount(s.prompt_tokens))}
          >
            {/* The honest height, including 0 for a total miss. Keeping that
                mark visible is a min-height in CSS, not a lie in the markup. */}
            <div class={`ctx-cache-fill${pct < 50 ? ' hot' : ''}`} style={{ height: pct + '%' }} />
          </div>
        )
      })}
    </div>
  )
}

// TasksBody renders the background-agent (swarm) dashboard: one row per task
// with a status badge, live activity, expandable transcript tail, and actions.
export function TasksBody({
  list,
  onAction,
}: {
  list: TaskList
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  // No archived tally. An archived agent is unreachable from terva on purpose
  // (swarm.Archive), and a count here would be the read path that grows into a
  // list. The Archive button's own title says where the record goes; after that
  // it is the filesystem's, not ours.
  if (!list.tasks.length) return <div class="pick-empty">{t('no background agents')}</div>
  return (
    <div class="tasks-body">
      {list.tasks.map((task) => (
        <TaskRow key={task.id} task={task} onAction={onAction} />
      ))}
    </div>
  )
}

export function TaskRow({
  task,
  onAction,
}: {
  task: TaskInfo
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [msg, setMsg] = useState('')
  const act = (a: string, args?: Record<string, string>) => onAction('tasks', a, { id: task.id, ...args })
  const running = task.status === 'running' || task.status === 'pending'
  const removable = task.status === 'done' || task.status === 'failed' || task.status === 'killed' || task.status === 'detached'
  const send = () => {
    const trimmed = msg.trim()
    if (!trimmed) return
    act('send', { text: trimmed })
    setMsg('')
  }
  return (
    <div class="task-row">
      <div class="task-head" onClick={() => setExpanded((e) => !e)}>
        <span class={`task-status s-${task.status}`}>{task.status}</span>
        <span class="task-title">{task.task || task.id}</span>
        {task.backend && <span class="task-backend">{task.backend}</span>}
        {task.model && <span class="task-model">{task.model}</span>}
        {task.cost_usd ? (
          <span class="task-cost" title={t('spend so far')}>
            ${task.cost_usd.toFixed(4)}
          </span>
        ) : null}
      </div>
      {task.activity && <div class="task-activity">{task.activity}</div>}
      {task.error && <div class="task-error">{task.error}</div>}
      {expanded && task.tail && <pre class="task-tail">{task.tail}</pre>}
      {running && (
        <form
          class="task-send"
          onSubmit={(e) => {
            e.preventDefault()
            send()
          }}
        >
          <input
            value={msg}
            placeholder={t('send a message…')}
            onInput={(e) => setMsg((e.target as HTMLInputElement).value)}
          />
          <button class="btn sm" type="submit" disabled={!msg.trim()}>
            {t('Send')}
          </button>
        </form>
      )}
      <div class="task-actions">
        {running && (
          <button class="btn sm" onClick={(e) => (e.stopPropagation(), act('stop'))}>
            {t('Stop')}
          </button>
        )}
        {task.status === 'detached' && (
          <button class="btn sm" onClick={(e) => (e.stopPropagation(), act('resume'))}>
            {t('Resume')}
          </button>
        )}
        {removable && (
          <button
            class="btn sm"
            title={t('compress the transcript into swarm/archive/ and drop it from terva — one way, no undo, recover it with gunzip')}
            onClick={(e) => (e.stopPropagation(), act('archive'))}
          >
            {t('Archive')}
          </button>
        )}
        {removable && (
          <button class="btn sm danger" onClick={(e) => (e.stopPropagation(), act('remove'))}>
            {t('Remove')}
          </button>
        )}
      </div>
    </div>
  )
}

// ---- worktrees: managed git worktrees (kind=worktrees) ----
// The web face of the TUI's /worktree panel: the same one-fetch view (the
// list plus the merge-back collect overview), read-only. No push event exists
// for worktree changes, so the pane fetches on open and the ↻ button
// re-fetches on demand — the same freshness the TUI panel's r key has. The
// status vocabulary (claimed(self), ✱dirty, ⇡unpushed, (here)) mirrors the
// engine renderer in packages/agent/worktree/render.go so both frontends
// describe a worktree in the same words.

const wtSHA = (s: string) => (s.length > 7 ? s.slice(0, 7) : s)

export function wtStatus(it: WorktreeViewItem): { label: string; cls: string } {
  if (it.unmanaged) return { label: 'unmanaged', cls: 's-unmanaged' }
  if (it.claimed_by === 'self') return { label: 'claimed(self)', cls: 's-self' }
  if (it.status === 'claimed' && it.claimed_by) return { label: `claimed(${it.claimed_by})`, cls: 's-claimed' }
  if (it.stale_reason) return { label: 'stale', cls: 's-stale' }
  return { label: 'available', cls: 's-available' }
}

// TaskBoardBody renders what the MODEL is tracking — the built-in task_* list —
// as opposed to TasksBody, which is the swarm of background sub-agents.
//
// Read-only by design. The model owns this list; a human toggling a status here
// would change state the model has no way to learn it lost, and the next
// task_update would silently overwrite the edit. Watching is the affordance.
//
// Ordering is the store's, not re-sorted here: the model's sequence is the plan,
// and re-grouping by status would hide the order the work is meant to happen in.
// taskStatusLabel spells each status literally so the extractor can see it —
// t(it.status) would be one line shorter and untranslatable.
function taskStatusLabel(status: string): string {
  switch (status) {
    case 'pending':
      return t('pending')
    case 'active':
      return t('active')
    case 'blocked':
      return t('blocked')
    case 'done':
      return t('done')
    case 'cancelled':
      return t('cancelled')
    default:
      return status
  }
}

export function TaskBoardBody({ v }: { v: TaskBoardView }) {
  const items = v.tasks ?? []
  if (!items.length)
    return <div class="pick-empty">{t('Nothing tracked yet — tasks appear here as the agent plans its work.')}</div>
  const open = items.filter((i) => i.status === 'pending' || i.status === 'active').length
  return (
    <div class="tb-body">
      <div class="tb-head">{t('%s of %s open', String(open), String(items.length))}</div>
      {items.map((it) => (
        <div class={`tb-row ${it.status}`} key={it.id}>
          <div class="tb-row-head">
            <span class={`tb-status ${it.status}`}>{taskStatusLabel(it.status)}</span>
            {/* The active form ("running the tests") is what the model says it
                is DOING; fall back to the title when it is absent. */}
            <span class="tb-title">{it.status === 'active' && it.active_form ? it.active_form : it.title}</span>
            <span class="tb-id mono">{it.id}</span>
          </div>
          {it.note && <div class="tb-note">{it.note}</div>}
          {it.evidence && <div class="tb-evidence mono">{it.evidence}</div>}
        </div>
      ))}
    </div>
  )
}

export function WorktreesBody({ v, onRefresh }: { v: WorktreeView; onRefresh?: () => void }) {
  const [collect, setCollect] = useState(false)
  const pending = (v.collect ?? []).filter((c) => c.ahead > 0 || c.dirty).length
  return (
    <div class="wt-body">
      <div class="wt-head">
        <div class="wt-tabs">
          <button class={`wt-tab${!collect ? ' active' : ''}`} onClick={() => setCollect(false)}>
            {t('List')}
          </button>
          <button class={`wt-tab${collect ? ' active' : ''}`} onClick={() => setCollect(true)}>
            {t('Merge-back')}
            {pending > 0 && <span class="wt-pending">{pending}</span>}
          </button>
        </div>
        <span class="wt-repo">{v.repo_key}</span>
        {onRefresh && (
          <button class="icon sm" title={t('Refresh')} onClick={onRefresh}>
            ↻
          </button>
        )}
      </div>
      {collect ? <WorktreeCollect items={v.collect ?? []} /> : <WorktreeList v={v} />}
    </div>
  )
}

export function WorktreeList({ v }: { v: WorktreeView }) {
  const items = v.items ?? []
  if (!items.length)
    return <div class="pick-empty">{t('No worktrees yet — create one with the worktree_create tool.')}</div>
  return (
    <div class="wt-list">
      {items.map((it) => {
        const st = wtStatus(it)
        const here = it.name === v.cwd_worktree
        return (
          <div key={it.name} class={`wt-row${here ? ' here' : ''}`}>
            <div class="wt-row-head">
              <span class="wt-name">{it.name}</span>
              {here && <span class="wt-here">{t('here')}</span>}
              {it.dirty && <span class="wt-dirty">{t('✱dirty')}</span>}
              <span class={`wt-status ${st.cls}`}>{st.label}</span>
            </div>
            <div class="wt-row-detail">
              {it.branch && <span class="wt-branch">{it.branch}</span>}
              <span class="wt-base">
                {it.base_ref}
                {it.base_commit ? '@' + wtSHA(it.base_commit) : ''}
                {it.head_commit && it.head_commit !== it.base_commit ? ' → ' + wtSHA(it.head_commit) : ''}
              </span>
              {it.stale_reason && <span class="wt-stale-why">{it.stale_reason}</span>}
            </div>
            <div class="wt-path">{it.path}</div>
          </div>
        )
      })}
    </div>
  )
}

// WorktreeCollect is the read-only merge-back overview: per worktree, how far
// its branch is ahead of base and the pending commit subjects. Like the TUI
// view it ends with a reminder that merging back is a manual act.
export function WorktreeCollect({ items }: { items: WorktreeCollectItem[] }) {
  if (!items.length) return <div class="pick-empty">{t('No worktrees to collect.')}</div>
  const pending = items.filter((it) => it.ahead > 0 || it.dirty).length
  return (
    <div class="wt-list">
      {items.map((it) => (
        <div key={it.name} class="wt-row">
          <div class="wt-row-head">
            <span class="wt-name">{it.name}</span>
            {it.branch && <span class="wt-branch">{it.branch}</span>}
            {it.dirty && <span class="wt-dirty">{t('✱dirty')}</span>}
            {it.unpushed && <span class="wt-dirty">{t('⇡unpushed')}</span>}
            <span class="wt-ahead">{tn(it.ahead, '+%d commit', '+%d commits')}</span>
          </div>
          <div class="wt-row-detail">
            <span class="wt-base">{t('ahead of %s', it.base_ref ?? '')}</span>
          </div>
          {it.commits && it.commits.length > 0 && (
            <ul class="wt-commits">
              {it.commits.map((c, idx) => (
                <li key={idx}>{c}</li>
              ))}
            </ul>
          )}
          {it.ahead === 0 && !it.dirty && <div class="wt-even">{t('nothing to collect')}</div>}
        </div>
      ))}
      <div class="wt-footnote">
        {pending === 0
          ? t('All worktrees are even with their base — nothing to merge back.')
          : t('Review, then merge manually (e.g. git merge <branch>). No auto-merge.')}
      </div>
    </div>
  )
}

// SettingsBody renders the settings pane: enum settings as selects, bool
// settings as toggles. Changing one fires surface.action {action:"set"}.
export function SettingsBody({
  v,
  onAction,
  onRestart,
  version,
}: {
  v: SettingsView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onRestart?: () => void
  version?: string
}) {
  const set = (key: string, value: string) => onAction('settings', 'set', { key, value })
  return (
    <div class="settings-body">
      {v.items.map((it) => (
        <div class="set-row" key={it.key}>
          <div class="set-head">
            <span class="set-label">{it.label}</span>
            {it.type === 'enum' ? (
              <select class="set-input" value={it.value} onChange={(e) => set(it.key, (e.target as HTMLSelectElement).value)}>
                {(it.options ?? []).map((o) => (
                  <option value={o.value}>{o.label}</option>
                ))}
              </select>
            ) : (
              <button
                class={`set-toggle${it.value === 'true' ? ' on' : ''}`}
                role="switch"
                aria-checked={it.value === 'true'}
                title={it.value === 'true' ? t('on') : t('off')}
                onClick={() => set(it.key, it.value === 'true' ? 'false' : 'true')}
              >
                <span class="set-knob" />
              </button>
            )}
          </div>
          {it.description && <div class="set-desc">{it.description}</div>}
          {it.note && <div class="set-note">{it.note}</div>}
        </div>
      ))}
      {onRestart && <RestartRow onRestart={onRestart} />}
      {version && (
        <div class="set-row">
          <div class="set-head">
            <span class="set-label">{t('terva version')}</span>
            <span class="set-value mono">v{version}</span>
          </div>
          <div class="set-desc">{t('The build serving this panel (re-announced on every reconnect).')}</div>
        </div>
      )}
    </div>
  )
}

// RestartRow is the daemon self-restart control, shown only when the server
// advertised the capability. Two-step (arm, then confirm) so it can't fire on a
// stray tap; it disarms itself after a few seconds.
export function RestartRow({ onRestart }: { onRestart: () => void }) {
  const [armed, setArmed] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  const disarm = () => {
    setArmed(false)
    if (timer.current) clearTimeout(timer.current)
  }
  const arm = () => {
    setArmed(true)
    timer.current = window.setTimeout(() => setArmed(false), 4000)
  }
  useEffect(() => () => timer.current && clearTimeout(timer.current), [])
  return (
    <div class="set-row restart-row">
      <div class="set-head">
        <span class="set-label">{t('Restart terva')}</span>
        {armed ? (
          <span class="restart-confirm">
            <button class="btn sm danger" onClick={() => (disarm(), onRestart())}>
              {t('Confirm restart')}
            </button>
            <button class="btn sm" onClick={disarm}>
              {t('Cancel')}
            </button>
          </span>
        ) : (
          <button class="btn sm" onClick={arm}>
            {t('Restart')}
          </button>
        )}
      </div>
      <div class="set-desc">{t('Re-exec into the currently-installed binary. Interrupts sessions; clients reconnect and restore from disk.')}</div>
    </div>
  )
}

// PanelBody renders an extension panel (or aggregated status) as styled lines.
// ANSI SGR sequences from the TUI-oriented panel are stripped for the web. An
// extension-owned panel (has an ext) is focusable and forwards keystrokes to the
// extension via surface.action, so navigable panels (e.g. a memory browser) work
// — the ext replies with a panel_render that re-fetches the surface.
export function PanelBody({
  id,
  p,
  onAction,
}: {
  id: string
  p: PanelView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  const text = (p.lines ?? []).map(stripAnsi).join('\n')
  const interactive = !!p.ext
  const onKey = (e: KeyboardEvent) => {
    const k = panelKey(e)
    if (!k) return
    e.preventDefault()
    onAction(id, 'key', k.text !== undefined ? { key: k.name, text: k.text } : { key: k.name })
  }
  return (
    <div class="panel-body">
      <pre
        class={`panel-lines${interactive ? ' interactive' : ''}`}
        tabIndex={interactive ? 0 : undefined}
        onKeyDown={interactive ? onKey : undefined}
      >
        {text || '(empty)'}
      </pre>
      {p.footer && <div class="panel-footer">{stripAnsi(p.footer)}</div>}
      {interactive && <div class="panel-hint">{t('click to focus, then arrows / enter / type')}</div>}
      {p.ext && (
        <div class="panel-actions">
          <button class="btn sm" onClick={() => onAction(id, 'close')}>
            {t('Close panel')}
          </button>
        </div>
      )}
    </div>
  )
}

// panelKey maps a browser KeyboardEvent to the extension panel key vocabulary
// (mirrors the TUI's panelKeyName/panelKeyText). Returns null for keys not
// forwarded (e.g. ctrl/meta combos).
export function panelKey(e: KeyboardEvent): { name: string; text?: string } | null {
  const named: Record<string, string> = {
    ArrowUp: 'up',
    ArrowDown: 'down',
    ArrowLeft: 'left',
    ArrowRight: 'right',
    Enter: 'enter',
    Escape: 'esc',
    Tab: 'tab',
    Backspace: 'backspace',
    Delete: 'delete',
    Home: 'home',
    End: 'end',
    PageUp: 'pageup',
    PageDown: 'pagedown',
  }
  if (named[e.key]) return { name: named[e.key] }
  if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) return { name: 'rune', text: e.key }
  return null
}

export function stripAnsi(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*m/g, '')
}

// ResetsSection lists the provider's banked usage-reset credits under the usage
// summary and redeems one on demand. Redeeming spends a scarce, irreversible
// credit, so a click first arms an inline confirm (the credit's title + expiry)
// and only a second, explicit "Redeem" performs the spend — there is no
// auto-redeem. It self-fetches on mount and hides entirely when the provider
// offers no resets, so it's invisible everywhere except a codex subscription.
export function ResetsSection({
  onList,
  onConsume,
}: {
  onList: () => Promise<ResetsListResult>
  onConsume: (id: string) => Promise<ResetConsumeResult>
}) {
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [supported, setSupported] = useState(false)
  const [resets, setResets] = useState<ResetInfo[]>([])
  const [err, setErr] = useState('')
  const [arming, setArming] = useState('') // credit id awaiting confirm
  const [busy, setBusy] = useState('') // credit id being redeemed
  const [done, setDone] = useState('') // outcome line after a redeem

  const refresh = useCallback(() => {
    setState('loading')
    onList()
      .then((r) => {
        setSupported(r.supported)
        setResets(r.resets ?? [])
        setState('ready')
      })
      .catch((e) => {
        setErr(String(e?.message ?? e))
        setState('error')
      })
  }, [onList])

  useEffect(() => {
    refresh()
  }, [refresh])

  // Hide the whole section unless the provider supports resets and there's
  // something to show — usage panes for non-codex providers stay unchanged.
  if (state === 'loading') return null
  if (state === 'error') {
    return (
      <>
        <div class="ctx-section-label">{t('usage resets')}</div>
        <div class="resets-err">{err}</div>
      </>
    )
  }
  if (!supported || resets.length === 0) return null

  const redeem = (id: string) => {
    setBusy(id)
    setArming('')
    onConsume(id)
      .then((r) => {
        const n = r.windows_reset ?? 0
        setDone(n === 1 ? t('Redeemed — 1 usage window cleared.') : t('Redeemed — %d usage windows cleared.', n))
        setBusy('')
        refresh()
      })
      .catch((e) => {
        setErr(String(e?.message ?? e))
        setBusy('')
        refresh()
      })
  }

  const avail = resets.filter((r) => r.status === 'available').length
  return (
    <>
      <div class="ctx-section-label">
        {t('usage resets')} <span class="resets-count">{t('%d available', avail)}</span>
      </div>
      {done && <div class="resets-done">{done}</div>}
      <div class="resets-list">
        {resets.map((r) => (
          <ResetRow
            key={r.id}
            r={r}
            arming={arming === r.id}
            busy={busy === r.id}
            onArm={() => setArming(r.id)}
            onCancel={() => setArming('')}
            onRedeem={() => redeem(r.id)}
          />
        ))}
      </div>
    </>
  )
}

export function ResetRow({
  r,
  arming,
  busy,
  onArm,
  onCancel,
  onRedeem,
}: {
  r: ResetInfo
  arming: boolean
  busy: boolean
  onArm: () => void
  onCancel: () => void
  onRedeem: () => void
}) {
  const available = r.status === 'available'
  const spent = r.status === 'redeemed'
  const expiry = localInstant(r.expires_at)
  const due = deadlineOf(r.expires_at)
  // A redeemed credit has no deadline left to miss — it reads as spent, not as
  // the most urgent row on screen.
  const urgency = spent ? '' : deadlineClass(due)
  let meta = r.status
  if (spent) meta = t('spent')
  else if (expiry && due.level === 'expired') meta = t('expired %s', expiry)
  else if (expiry) meta = t('expires %s', expiry)
  return (
    <div
      class={`reset-row${available ? '' : ' spent'}${urgency ? ' ' + urgency : ''}`}
      style={spent ? undefined : deadlineStyle(due)}
    >
      <div class="reset-main">
        <span class="reset-title">{r.title || t('reset credit')}</span>
        {/* ui-deadline-meta is inert without a .ui-deadline ancestor, so it can
            ride every row rather than being toggled in step with the parent. */}
        <span class="reset-meta ui-deadline-meta" title={r.expires_at || undefined}>
          {meta}
        </span>
      </div>
      {available &&
        (busy ? (
          <span class="reset-busy">{t('redeeming…')}</span>
        ) : arming ? (
          <span class="reset-confirm">
            <span class="reset-warn">{t('Spend this credit? Cannot be undone.')}</span>
            <button class="reset-btn danger" onClick={onRedeem}>
              {t('Redeem')}
            </button>
            <button class="reset-btn" onClick={onCancel}>
              {t('Cancel')}
            </button>
          </span>
        ) : (
          <button class="reset-btn" onClick={onArm}>
            {t('Redeem')}
          </button>
        ))}
    </div>
  )
}

export function ContextBody({
  d,
  usage,
  onFetchNode,
  onListResets,
  onConsumeReset,
}: {
  d: ContextBreakdown
  usage?: UsageInfo | null
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onListResets: () => Promise<ResetsListResult>
  onConsumeReset: (id: string) => Promise<ResetConsumeResult>
}) {
  const realTok = d.context_tokens ?? 0
  const estTok = Math.round(d.total_bytes / 4)
  const gaugeTok = realTok > 0 ? realTok : estTok
  let largest = 0
  for (let i = 1; i < d.messages.length; i++) if (d.messages[i].bytes > d.messages[largest].bytes) largest = i
  // Prefer the context-tree outline (turns → messages, collapsible); fall back to
  // the flat message list when talking to a server without the context-tree feature.
  const transcriptNode = d.tree?.children?.find((c) => c.id === 'tr')
  const useTree = !!transcriptNode && (transcriptNode.children?.length ?? 0) > 0
  const windows = statusWindows(d, usage)
  return (
    <div class="ctx-body">
      {(d.provider || d.model) && (
        <div class="ctx-section-label">
          {d.provider}
          {d.provider && d.model ? ' / ' : ''}
          {d.model}
        </div>
      )}
      <UsageSummary
        tokens={gaugeTok}
        window={d.window}
        estimated={realTok === 0}
        cumulative={d.cumulative}
        subscription={d.subscription}
        windows={windows}
        cache={d.cache}
      />

      <ResetsSection onList={onListResets} onConsume={onConsumeReset} />

      <div class="ctx-section-label">{t('Next request — estimated by size')}</div>
      <div class="ctx-rows">
        <CtxRow
          label={t('system prompt')}
          bytes={d.system_bytes}
          note={d.ext_guidance_bytes > 0 ? t('incl. %s ext guidance', humanBytes(d.ext_guidance_bytes)) : ''}
        />
        <CtxRow
          label={t('tool defs')}
          bytes={d.tool_bytes}
          note={
            d.tool_count_installed && d.tool_count_installed > d.tool_count
              ? t('%d of %d tools · %s installed', d.tool_count, d.tool_count_installed, humanBytes(d.tool_bytes_installed ?? 0))
              : tn(d.tool_count, '%d tool', '%d tools')
          }
        />
        <CtxRow
          label={t('ext context')}
          bytes={d.ext_bytes}
          note={
            d.lazy_note_bytes
              ? t('cards, ephemeral · incl. %s lazy-tool note', humanBytes(d.lazy_note_bytes))
              : t('cards, ephemeral')
          }
        />
        <CtxRow label={t('transcript')} bytes={d.transcript_bytes} note={tn(d.messages.length, '%d msg', '%d msgs')} />
        <div class="ctx-total">
          <CtxRow label={t('TOTAL')} bytes={d.total_bytes} note="" />
        </div>
      </div>
      {d.lore_fired && d.lore_fired.length > 0 && (
        <>
          <div class="ctx-section-label">{t('lore fired last turn')}</div>
          <div class="ctx-lore">
            {d.lore_fired.map((e, i) => (
              <div key={i} class={`ctx-lore-row${e.dropped ? ' dropped' : ''}`}>
                <span class="ctx-lore-name">{e.name}</span>
                {e.constant ? (
                  <span class="ctx-lore-tag">{t('always on')}</span>
                ) : (
                  (e.keys ?? []).length > 0 && <span class="ctx-lore-keys">{(e.keys ?? []).join(', ')}</span>
                )}
                {e.dropped && <span class="ctx-lore-tag dropped">{t('budget dropped')}</span>}
              </div>
            ))}
          </div>
        </>
      )}
      {useTree ? (
        <>
          <div class="ctx-section-label">{t('transcript — by turn')}</div>
          <ContextTree node={transcriptNode!} onFetchNode={onFetchNode} />
        </>
      ) : (
        d.messages.length > 0 && (
          <div class="ctx-msgs">
            {d.messages.map((m) => (
              <div key={m.index} class={`ctx-msg${m.index === largest && d.messages.length > 1 ? ' largest' : ''}`}>
                <span class="ctx-msg-i">[{m.index}]</span>
                <span class="ctx-msg-kind">{m.kind}</span>
                <span class="ctx-msg-b">{humanBytes(m.bytes)}</span>
                {m.index === largest && d.messages.length > 1 && <span class="ctx-msg-tag">← {t('largest')}</span>}
              </div>
            ))}
          </div>
        )
      )}
      <div class="ctx-note">{t('sizes are bytes; token counts are ~bytes/4 estimates')}</div>
    </div>
  )
}

// ContextTree renders the transcript section's turns as a collapsible tree.
// Turns start collapsed — you browse turn headers and drill into one — and the
// single largest message across the transcript is flagged. Stage 1 has no lazy
// content fetch: every node is already inline (pre-expanded stubs), so expanding
// only toggles visibility. See docs/proposals/context-inspector.md.
export function ContextTree({
  node,
  onFetchNode,
}: {
  node: ContextNode
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
}) {
  const turns = node.children ?? []
  let largestId = ''
  let largestBytes = -1
  for (const turn of turns)
    for (const m of turn.children ?? [])
      if (m.bytes > largestBytes) {
        largestBytes = m.bytes
        largestId = m.id
      }
  return (
    <div class="ctx-tree">
      {turns.map((turn) => (
        <TreeNode key={turn.id} node={turn} largestId={largestId} onFetchNode={onFetchNode} />
      ))}
    </div>
  )
}

// TreeNode renders one context node. A node with inline children (turns) toggles
// visibility; an `expandable` node with no inline children fetches its content
// via context.node on first open; a node carrying `content` shows it in a
// scrollable body. Every level is collapsed by default — you drill in.
export function TreeNode({
  node,
  largestId,
  onFetchNode,
}: {
  node: ContextNode
  largestId: string
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
}) {
  const inlineKids = node.children ?? []
  const fetchable = !!node.expandable || !!node.reveal
  const canOpen = inlineKids.length > 0 || fetchable || !!node.content
  const [open, setOpen] = useState(false)
  const [fetched, setFetched] = useState<ContextNode | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const isLargest = node.id === largestId
  const isMsg = node.kind === 'message'

  const eff = fetched ?? node
  const kids = eff.children ?? []
  const content = eff.content

  const toggle = () => {
    const next = !open
    setOpen(next)
    // Fetch on first open: expand (content/children) or reveal (a compaction's
    // replaced span). The node names its own reveal op; expand passes none.
    if (next && fetchable && !fetched && inlineKids.length === 0) {
      setLoading(true)
      setErr('')
      onFetchNode(node.id, node.reveal || undefined)
        .then((n) => setFetched(n))
        .catch((e) => setErr(String(e)))
        .finally(() => setLoading(false))
    }
  }

  return (
    <div class="ctx-tnode">
      <div
        class={`ctx-trow${canOpen ? ' has-kids' : ''}${isLargest ? ' largest' : ''}${node.kind === 'event' ? ' ctx-event' : ''}`}
        onClick={canOpen ? toggle : undefined}
      >
        <span class="ctx-tchev">{canOpen ? (open ? '▾' : '▸') : ''}</span>
        {isMsg && node.meta?.index != null && <span class="ctx-msg-i">[{node.meta.index}]</span>}
        <span class="ctx-tlabel">{node.label}</span>
        {node.summary && <span class="ctx-tsummary">{node.summary}</span>}
        {node.reveal && <span class="ctx-treveal">⤢ {t('reveal')}</span>}
        {node.meta?.msgs && <span class="ctx-tcount">{tn(Number(node.meta.msgs), '%d msg', '%d msgs')}</span>}
        <span class="ctx-tbytes">{humanBytes(node.bytes)}</span>
        {isLargest && <span class="ctx-msg-tag">← {t('largest')}</span>}
      </div>
      {open && (
        <div class="ctx-tkids">
          {loading && <div class="ctx-tmuted">{t('loading…')}</div>}
          {err && <div class="ctx-terr">{err}</div>}
          {content && <pre class="ctx-tcontent">{content}</pre>}
          {kids.map((c) => (
            <TreeNode key={c.id} node={c} largestId={largestId} onFetchNode={onFetchNode} />
          ))}
        </div>
      )}
    </div>
  )
}

export function CtxRow({ label, bytes, note }: { label: string; bytes: number; note: string }) {
  return (
    <div class="ctx-row">
      <span class="ctx-row-label">{label}</span>
      <span class="ctx-row-bytes">{humanBytes(bytes)}</span>
      <span class="ctx-row-tok">~{humanCount(Math.round(bytes / 4))} {t('tok')}</span>
      {note && <span class="ctx-row-note">{note}</span>}
    </div>
  )
}

// WindowRow renders one subscription usage window: a labelled meter with the
// percent used and a reset countdown (mirrors the TUI status bar's usage meter).
export function WindowRow({ w }: { w: UsageWindow }) {
  const known = w.used_percent >= 0
  const pct = known ? Math.min(100, w.used_percent) : 0
  const reset = w.resets_at ? countdown(w.resets_at) : ''
  return (
    <div class="ctx-win">
      <span class="ctx-win-label">{shortWindow(w.label)}</span>
      <div class="ctx-bar sm">
        <div class={`ctx-bar-fill${pct >= 85 ? ' hot' : ''}`} style={{ width: pct + '%' }} />
      </div>
      <span class="ctx-win-pct">{known ? Math.round(w.used_percent) + '%' : '?'}</span>
      {reset && <span class="ctx-win-reset">↻ {reset}</span>}
    </div>
  )
}

// statusWindows picks which subscription windows the usage pane renders.
//
// The breakdown's own list wins whenever it has one: it is refreshed by every
// turn and already filtered to the status picture. The usage.snapshot mirror is
// the fallback, and in practice it only ever fills in for the poll-family
// providers whose breakdown list stays empty until their usage endpoint has
// been called. Its windows are filtered here rather than server-side because
// that verb deliberately returns everything the provider reports, rate-limit
// windows included, and leaves the choice to the caller. Dropping them keeps
// one meaning for this pane whichever family filled it — plan and credit
// budgets, never ephemeral throughput limits.
export function statusWindows(d: ContextBreakdown, usage?: UsageInfo | null): UsageWindow[] | undefined {
  if (d.usage_windows?.length) return d.usage_windows
  const fallback = usage?.windows?.filter((w) => w.kind !== 'rate_limit')
  return fallback?.length ? fallback : undefined
}

export function shortWindow(label: string): string {
  const l = label.trim().toLowerCase()
  if (l === 'weekly' || l === 'week') return 'wk'
  if (l === 'monthly' || l === 'month') return 'mo'
  return label
}

// countdown renders time until an RFC3339 instant as "3d17h" / "4h33m" / "12m".
export function countdown(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (isNaN(ms) || ms <= 0) return ''
  const mins = Math.floor(ms / 60000)
  if (mins < 1) return '<1m'
  const days = Math.floor(mins / 1440)
  const hours = Math.floor((mins % 1440) / 60)
  const m = mins % 60
  if (days > 0) return `${days}d${hours}h`
  if (hours > 0) return `${hours}h${m}m`
  return `${m}m`
}
