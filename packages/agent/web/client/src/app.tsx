import type { VNode } from 'preact'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { Client } from './ctrlproto'
import type {
  AskRequest,
  CatalogView,
  CommandsView,
  ContextBreakdown,
  ContextNode,
  Decision,
  ExtensionsView,
  LoreEntry,
  LoreView,
  MCPView,
  ChatView,
  ChatServiceInfo,
  ModelInfo,
  PanelView,
  PermissionRequest,
  PermissionsView,
  RaatiHistoryItem,
  RaatiInquiry,
  RaatiUnit,
  RaatiView,
  ResetInfo,
  ResetsListResult,
  ResetConsumeResult,
  SessionInfo,
  SettingsView,
  SkillInfo,
  Status,
  Surface,
  SurfaceMeta,
  TaskInfo,
  TaskList,
  UsageWindow,
  Widget,
  WireEvent,
  WireUsage,
} from './ctrlproto'
import { Composer, type SlashCommand } from './features/conversation/Composer'
import { ConversationTimeline } from './features/conversation/ConversationTimeline'
import type { ToolView } from './features/conversation/types'
import { AskRequest as AskRequestView } from './features/interactions/AskRequest'
import { PermissionRequest as PermissionRequestView } from './features/interactions/PermissionRequest'
import { ModelPicker } from './features/models/ModelPicker'
import { SessionInfo as SessionInfoView } from './features/sessions/SessionInfo'
import { SessionPicker } from './features/sessions/SessionPicker'
import type { ImageAttachment } from './platform/conversation/images'
import { applyEvent, itemsFromMessages, type Item } from './platform/conversation/store'
import { buildConveneArgs, raatiResultCopyText, raatiUnitCopyText, raatiVerdictWord } from './raati'
import { applyServerCatalog, setLocale, t, tn } from './i18n'
import { CopyButton } from './ui/CopyButton'
import { humanBytes, humanCount } from './ui/formatting'

const TOOL_VIEWS: ToolView[] = ['full', 'grouped', 'minimal', 'hidden']

export function App() {
  const clientRef = useRef<Client | null>(null)
  const curRef = useRef('')
  const busyRef = useRef(false)

  const [status, setStatus] = useState<Status>('connecting')
  // Bumped whenever the active catalog changes (locale learned from the hello,
  // or the server catalog fetched/overlaid) to force a re-render — t()/tn() read
  // the module-level catalog, so the value itself needn't be the locale string.
  const [, bumpI18n] = useState(0)
  const reI18n = useCallback(() => bumpI18n((n) => n + 1), [])
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [curSess, setCurSess] = useState('')
  const [items, setItems] = useState<Item[]>([])
  const [models, setModels] = useState<ModelInfo[]>([])
  const [busy, setBusy] = useState(false)
  const [cost, setCost] = useState(0)
  const [permission, setPermission] = useState<PermissionRequest | null>(null)
  const [ask, setAsk] = useState<AskRequest | null>(null)
  const [drawer, setDrawer] = useState(false)
  const [toast, setToast] = useState('')
  const [toolView, setToolView] = useState<ToolView>(
    () => (localStorage.getItem('terva_toolview') as ToolView) || 'full',
  )
  const [curInfo, setCurInfo] = useState<SessionInfo | null>(null)
  const [infoOpen, setInfoOpen] = useState(false)
  const [queued, setQueued] = useState<string[]>([])
  // Whether the daemon advertised the self-restart capability (--web-allow-restart).
  const [canRestart, setCanRestart] = useState(false)
  // Available skills (from the snapshot), for /skill autocomplete.
  const [skills, setSkills] = useState<SkillInfo[]>([])
  const [pickerOpen, setPickerOpen] = useState(false)
  // Pane host (surfaces): context/usage/extension panels in a right rail.
  const [paneOpen, setPaneOpen] = useState(false)
  const [surfaces, setSurfaces] = useState<SurfaceMeta[]>([])
  const [activeSurface, setActiveSurface] = useState('context')
  const [surfaceData, setSurfaceData] = useState<Surface | null>(null)
  const [surfaceErr, setSurfaceErr] = useState('')
  const paneOpenRef = useRef(false)
  const activeSurfaceRef = useRef('context')

  curRef.current = curSess
  busyRef.current = busy
  paneOpenRef.current = paneOpen
  activeSurfaceRef.current = activeSurface

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
      const res = await c.send<{ models: ModelInfo[] }>('models.list', null, '')
      setModels(res.models ?? [])
    } catch {
      /* control group optional */
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

  const refreshSessions = useCallback(async (c: Client): Promise<SessionInfo[]> => {
    try {
      const res = await c.send<{ sessions: SessionInfo[] }>('sessions.list', null, '')
      setSessions(res.sessions ?? [])
      return res.sessions ?? []
    } catch {
      return []
    }
  }, [])

  const selectSession = useCallback((id: string) => {
    const c = clientRef.current
    if (!c || !id || id === curRef.current) {
      setDrawer(false)
      return
    }
    if (curRef.current) c.fire('unsubscribe', null, curRef.current)
    setItems([])
    setPermission(null)
    setAsk(null)
    setBusy(false)
    setCost(0)
    setQueued([])
    setCurInfo(null)
    setInfoOpen(false)
    setSurfaces([])
    setSurfaceData(null)
    curRef.current = id
    setCurSess(id)
    c.fire('subscribe', null, id)
    setDrawer(false)
  }, [])

  const newSession = useCallback(
    async (c?: Client) => {
      const cl = c ?? clientRef.current
      if (!cl) return
      try {
        const res = await cl.send<{ session: SessionInfo }>('sessions.create', { title: '' }, '')
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
      case 'snapshot':
        setItems(itemsFromMessages(ev.snapshot?.messages ?? []))
        setBusy(!!ev.snapshot?.busy)
        setCurInfo(ev.snapshot?.session ?? null)
        setQueued(ev.snapshot?.queued ?? [])
        setSkills(ev.snapshot?.skills ?? [])
        if (ev.snapshot?.session?.usage) setCost(ev.snapshot.session.usage.cost_usd || 0)
        // Restore any pending approval/question so a reconnecting tab shows it.
        setPermission(ev.snapshot?.permissions?.[0] ?? null)
        setAsk(ev.snapshot?.asks?.[0] ?? null)
        return
      case 'session_updated': {
        // A title settled, a rename, or a model switch — refresh the header and
        // the session-list entry live, without re-fetching.
        const info = ev.info
        if (!info) return
        setCurInfo((cur) => (cur ? { ...cur, ...info } : info))
        setSessions((ss) => ss.map((s) => (s.id === info.id ? { ...s, ...info } : s)))
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
      case 'surface_updated':
        // A live pane's content changed; re-fetch if we're showing it.
        if (paneOpenRef.current && ev.surface_id === activeSurfaceRef.current) {
          loadSurface(activeSurfaceRef.current)
        }
        return
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
  }, [listSurfaces, loadSurface, refreshI18n])

  useEffect(() => {
    const c = new Client()
    clientRef.current = c
    c.onStatus = setStatus
    c.onEvent = (sess, ev) => {
      if (sess === curRef.current) handleEvent(ev)
    }
    c.onReady = async (hello) => {
      // Match the PWA's bundled catalog to the daemon's active language for an
      // instant first paint, then overlay the server's effective catalog
      // (embedded ⊕ $TERVA_HOME overlay) so an operator's translation edits show
      // on reload. Both re-render via reI18n; catalog fetch is best-effort.
      const lang = setLocale(hello?.locale ?? 'en')
      document.documentElement.lang = lang
      setCanRestart(!!hello?.features?.includes('restart'))
      reI18n()
      refreshI18n()
      try {
        const res = await c.send<{ models: ModelInfo[] }>('models.list', null, '')
        setModels(res.models ?? [])
      } catch {
        /* control group optional */
      }
      const list = await refreshSessions(c)
      if (!list.length) {
        await newSession(c)
        return
      }
      const target = (list.find((s) => s.current) ?? list[0]).id
      if (target === curRef.current) {
        // Reconnect to the same session: selectSession would short-circuit on the
        // unchanged id and never re-subscribe, leaving the panel connected but
        // frozen (no snapshot, no live events). Fire the subscribe explicitly —
        // the server is idempotent and replies with a fresh snapshot that resyncs.
        c.fire('subscribe', null, target)
      } else {
        selectSession(target)
      }
    }
    c.connect()
    return () => c.close()
  }, [handleEvent, newSession, refreshSessions, selectSession, refreshI18n])

  // Restart the daemon. The socket drops as the process re-execs; the pre-exec
  // notice already reached us, and the client auto-reconnects to the new build,
  // so the "connection closed" rejection here is expected and swallowed.
  const restart = useCallback(() => {
    clientRef.current?.send('control.restart', null, '').catch(() => {})
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

  // sendPrompt dispatches a turn, optionally with pasted image attachments.
  // Returns true when it consumed the input (so the composer can clear). Images
  // ride only the prompt path — the queue verb is text-only server-side — so a
  // send with images while busy is refused rather than silently dropping them.
  const sendPrompt = useCallback((text: string, images?: ImageAttachment[]): boolean => {
    const c = clientRef.current
    const hasImages = !!images && images.length > 0
    if (!c || !curRef.current || (!text.trim() && !hasImages)) return false
    if (busyRef.current) {
      if (hasImages) {
        setToast(t('Finish the current turn before attaching images (the queue is text-only).'))
        return false
      }
      setQueued((q) => [...q, text])
      c.fire('queue', { text }, curRef.current)
      return true
    }
    setBusy(true)
    const wireImages = hasImages ? images!.map((im) => ({ mime_type: im.mime, data: im.data })) : undefined
    c.fire('prompt', wireImages ? { text, images: wireImages } : { text }, curRef.current)
    return true
  }, [])

  const decide = useCallback((callID: string, d: Decision) => {
    clientRef.current?.fire('approve', { call_id: callID, decision: d }, curRef.current)
    setPermission(null)
  }, [])

  const answer = useCallback((askID: string, text: string) => {
    clientRef.current?.fire('answer', { ask_id: askID, answer: { answer: text } }, curRef.current)
    setAsk(null)
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
      run: () => {
        const c = clientRef.current
        if (c) void newSession(c)
      },
    },
    {
      name: 'help',
      desc: t('List slash commands'),
      run: () => localNotice(t('Slash commands: /compact, /clear, /skill, /model, /context, /raati, /new. Type / in the composer to autocomplete.')),
    },
  ]

  // onSubmit is the composer's send path: a leading "/<known command>" is
  // intercepted and run; anything else (including a message that merely starts
  // with "/") is sent as a normal prompt. Returns true when the input was
  // consumed so the composer clears its text + attachments. Slash commands are
  // only recognized when no image is attached (an image send is always a prompt).
  const onSubmit = (text: string, images?: ImageAttachment[]): boolean => {
    const trimmed = text.trim()
    if (trimmed.startsWith('/') && !(images && images.length)) {
      const sp = trimmed.indexOf(' ')
      const head = (sp === -1 ? trimmed.slice(1) : trimmed.slice(1, sp)).toLowerCase()
      const cmd = slashCommands.find((c) => c.name === head)
      if (cmd) {
        cmd.run(sp === -1 ? '' : trimmed.slice(sp + 1))
        return true
      }
    }
    return sendPrompt(text, images)
  }

  // Refresh the pane when it opens, the active surface changes, or the session
  // changes; live panes also re-fetch on surface_updated (see handleEvent).
  useEffect(() => {
    if (!paneOpen || !curSess) return
    listSurfaces()
    loadSurface(activeSurface)
  }, [paneOpen, activeSurface, curSess, listSurfaces, loadSurface])

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

  const del = useCallback(
    async (s: SessionInfo) => {
      if (!window.confirm(t('Delete “%s”?', s.title || s.id))) return
      const c = clientRef.current
      if (!c) return
      await c.send('sessions.delete', null, s.id).catch((e) => setToast(String(e)))
      const list = await refreshSessions(c)
      if (s.id === curRef.current) {
        curRef.current = ''
        setCurSess('')
        if (list.length) selectSession(list[0].id)
        else newSession(c)
      }
    },
    [newSession, refreshSessions, selectSession],
  )

  const current = sessions.find((s) => s.id === curSess)
  const curModel = models.find((m) => m.current)
  const ctxTok = curInfo?.context_tokens ?? 0
  const ctxWin = curInfo?.context_window ?? 0
  const ctxPct = ctxWin > 0 ? Math.min(100, (ctxTok / ctxWin) * 100) : -1

  return (
    <div class={`app${paneOpen ? ' with-pane' : ''}`}>
      <header class="topbar">
        <button class="icon" title={t('Sessions')} onClick={() => setDrawer((d) => !d)}>
          ☰
        </button>
        <div class="ident">
          <span class="persona">{curInfo?.persona || 'terva'}</span>
          <span class="sess-title">{current?.title || curInfo?.title || t('new chat')}</span>
        </div>
        <button class="icon" title={t('Session info')} onClick={() => setInfoOpen((v) => !v)}>
          ⓘ
        </button>
        <button
          class="icon"
          title={t('Tool calls: %s — click to cycle (box / grouped / minimal / hidden)', toolView)}
          onClick={cycleToolView}
        >
          {toolView === 'full' ? '▤' : toolView === 'grouped' ? '⊟' : toolView === 'minimal' ? '≡' : '⌀'}
        </button>
        <button
          class={`icon${paneOpen ? ' on' : ''}`}
          title={t('Panes (usage, settings, extensions)')}
          onClick={() => (paneOpen ? setPaneOpen(false) : openPane(activeSurface))}
        >
          ⊞
        </button>
        <button class="model-btn" title={t('Switch model')} onClick={() => setPickerOpen(true)}>
          {curModel ? (curModel.favorite ? '★ ' : '') + curModel.id : t('model')}
        </button>
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
        <span class={`dot ${status}`} title={status} />
      </header>

      {infoOpen && (
        <SessionInfoView
          info={curInfo}
          cost={cost}
          onClose={() => setInfoOpen(false)}
          onContext={() => {
            setInfoOpen(false)
            openPane('context')
          }}
        />
      )}

      {pickerOpen && (
        <ModelPicker
          groups={modelGroups}
          favorites={favorites}
          current={curModel?.id}
          onSwitch={(id, provider) => {
            switchModel(id, provider)
            setPickerOpen(false)
          }}
          onToggleFavorite={favoriteModel}
          onClose={() => setPickerOpen(false)}
        />
      )}

      {drawer && (
        <SessionPicker
          sessions={sessions}
          current={curSess}
          onSelect={selectSession}
          onNew={() => newSession()}
          onRename={rename}
          onDelete={del}
          onClose={() => setDrawer(false)}
        />
      )}

      <div class="workspace">
        <div class="main">
          <ConversationTimeline
            items={items}
            busy={busy}
            toolView={toolView}
            queued={queued}
            onEditQueued={editQueued}
            onCancelQueued={cancelQueued}
          />

          {permission && <PermissionRequestView request={permission} onDecide={decide} />}
          {ask && <AskRequestView request={ask} onAnswer={answer} />}

          <Composer busy={busy} onSend={onSubmit} onToast={setToast} commands={slashCommands} skills={skills} onCancel={() => clientRef.current?.fire('cancel', null, curRef.current)} />
        </div>

        {paneOpen && (
          <PaneHost
            surfaces={surfaces}
            active={activeSurface}
            data={surfaceData}
            err={surfaceErr}
            onActivate={setActiveSurface}
            onAction={surfaceAction}
            onFetchNode={fetchNode}
            onListResets={listResets}
            onConsumeReset={consumeReset}
            onClose={() => setPaneOpen(false)}
            onRestart={canRestart ? restart : undefined}
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
function PaneHost({
  surfaces,
  active,
  data,
  err,
  onActivate,
  onAction,
  onFetchNode,
  onListResets,
  onConsumeReset,
  onClose,
  onRestart,
  trusted,
  onTrust,
  models,
}: {
  surfaces: SurfaceMeta[]
  active: string
  data: Surface | null
  err: string
  onActivate: (id: string) => void
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onListResets: () => Promise<ResetsListResult>
  onConsumeReset: (id: string) => Promise<ResetConsumeResult>
  onClose: () => void
  onRestart?: () => void
  trusted?: boolean
  onTrust?: (trust: boolean) => void
  models?: ModelInfo[]
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
            onAction={onAction}
            onFetchNode={onFetchNode}
            onListResets={onListResets}
            onConsumeReset={onConsumeReset}
            onRestart={onRestart}
            trusted={trusted}
            onTrust={onTrust}
            models={models}
          />
        )}
      </div>
    </aside>
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

function KanjiVerdict({ word, kanji, tone }: { word: string; kanji: string; tone: string }) {
  const [settled, setSettled] = useState(false)
  useEffect(() => {
    setSettled(false)
    const id = window.setTimeout(() => setSettled(true), 1600)
    return () => window.clearTimeout(id)
  }, [word, kanji])
  return <span class={`raati-kanji tone-${tone}${settled ? ' settled' : ' flash'}`}>{settled ? word : kanji}</span>
}

function RaatiBody({
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
function useRaatiTicker(v: RaatiView): string[] {
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
function raatiWhenLabel(when?: string): string {
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

function RaatiHistoryRow({ h, onShow }: { h: RaatiHistoryItem; onShow: () => void }) {
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

function RaatiBlock({ u }: { u: RaatiUnit }) {
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

function RaatiVerdictPanel({ v }: { v: RaatiView }) {
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
function RaatiInquiryList({ inquiries }: { inquiries: RaatiInquiry[] }) {
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

function raatiTheaterSlots(units?: RaatiUnit[]): { top?: RaatiUnit; left?: RaatiUnit; right?: RaatiUnit } {
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

function MagiPanel({
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

function RaatiTheater({
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
  const tickerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = tickerRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])
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
          <div class="magi-info">
            <div>
              <b>LEVEL</b> {v.binding || '—'}
            </div>
            <div>
              <b>CLASS</b> {v.class || '—'}
            </div>
            <div>
              <b>ROUND</b> {v.round ?? '—'}
            </div>
            <div>
              <b>SEAT</b> {v.seat_order || '—'}
            </div>
            <div>
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
          <div class="magi-ticker" ref={tickerRef} aria-live="polite">
            {lines.map((l, i) => (
              <div key={i} class="magi-ticker-line">
                {l}
              </div>
            ))}
          </div>
        ) : null}
        <footer class="magi-footer">
          <div class="magi-q">
            <span class="raati-dim">question:</span> {v.question || '—'}
          </div>
          <div class="magi-access">
            <span class="raati-dim">access:</span> {(v.when ?? '').replace('T', ' ').slice(0, 19) || 'MAGI_SYS'}
            <button class="magi-exit-hint" title={t('Exit theater (Esc)')} onClick={onExit}>
              <span class="magi-exit-key">esc: </span>
              {t('exit')}
            </button>
          </div>
        </footer>
      </div>
    </div>
  )
}

function SurfaceView({
  surface,
  onAction,
  onFetchNode,
  onListResets,
  onConsumeReset,
  onRestart,
  trusted,
  onTrust,
  models,
}: {
  surface: Surface
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onListResets: () => Promise<ResetsListResult>
  onConsumeReset: (id: string) => Promise<ResetConsumeResult>
  onRestart?: () => void
  trusted?: boolean
  onTrust?: (trust: boolean) => void
  models?: ModelInfo[]
}) {
  switch (surface.kind) {
    case 'context':
      return surface.context ? (
        <ContextBody
          d={surface.context}
          onFetchNode={onFetchNode}
          onListResets={onListResets}
          onConsumeReset={onConsumeReset}
        />
      ) : null
    case 'tasks':
      return surface.tasks ? <TasksBody list={surface.tasks} onAction={onAction} /> : null
    case 'raati':
      return <RaatiBody v={surface.raati ?? { running: false }} onAction={onAction} models={models ?? []} />
    case 'settings':
      return surface.settings ? <SettingsBody v={surface.settings} onAction={onAction} onRestart={onRestart} /> : null
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
    default:
      return <div class="pick-empty">{t('unsupported pane')}</div>
  }
}

// ChatBody renders the chat-connector pane: the live bridge and one row per
// registered service. Connect is async — the daemon returns immediately and the
// pane converges over surface_updated — so "connecting" is a real state here,
// not a spinner we invent.
//
// The mirror is bound to the session it was connected from; it does not follow
// this tab. That is why the connected card names its session explicitly.
function ChatBody({
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
function CommandsBody({
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
function ExtensionsBody({
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
        </div>
      ))}
    </div>
  )
}

// PermissionsBody renders the permissions inspector (kind=permissions): the
// workspace trust state (with a grant/revoke control), the approval mode, the
// session's live "always-allow" grants (each revocable), and the compiled rules
// (read-only). The mode's setter lives in Settings. Trust is workspace-global
// and gates whether project-scoped rules/lore/extensions load, so it heads the
// pane — granting it here is what unlocks the project scope elsewhere.
function PermissionsBody({
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
function AddRuleForm({
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
function LoreBody({
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
function MCPBody({
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
function WidgetBody({
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

function WidgetNode({ w, onAction }: { w: Widget; onAction: (actionID: string) => void }): VNode | null {
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
function WidgetGroup({ w, onAction }: { w: Widget; onAction: (actionID: string) => void }) {
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
function UsageSummary({
  tokens,
  window,
  estimated,
  cumulative,
  subscription,
  windows,
}: {
  tokens: number
  window: number
  estimated: boolean
  cumulative: WireUsage
  subscription?: boolean
  windows?: UsageWindow[]
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
    </>
  )
}

// TasksBody renders the background-agent (swarm) dashboard: one row per task
// with a status badge, live activity, expandable transcript tail, and actions.
function TasksBody({
  list,
  onAction,
}: {
  list: TaskList
  onAction: (id: string, action: string, args?: Record<string, string>) => void
}) {
  if (!list.tasks.length) return <div class="pick-empty">{t('no background agents')}</div>
  return (
    <div class="tasks-body">
      {list.tasks.map((task) => (
        <TaskRow key={task.id} task={task} onAction={onAction} />
      ))}
    </div>
  )
}

function TaskRow({
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
        {task.model && <span class="task-model">{task.model}</span>}
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
          <button class="btn sm danger" onClick={(e) => (e.stopPropagation(), act('remove'))}>
            {t('Remove')}
          </button>
        )}
      </div>
    </div>
  )
}

// SettingsBody renders the settings pane: enum settings as selects, bool
// settings as toggles. Changing one fires surface.action {action:"set"}.
function SettingsBody({
  v,
  onAction,
  onRestart,
}: {
  v: SettingsView
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onRestart?: () => void
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
    </div>
  )
}

// RestartRow is the daemon self-restart control, shown only when the server
// advertised the capability. Two-step (arm, then confirm) so it can't fire on a
// stray tap; it disarms itself after a few seconds.
function RestartRow({ onRestart }: { onRestart: () => void }) {
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
function PanelBody({
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
      {interactive && <div class="panel-hint">click to focus, then arrows / enter / type</div>}
      {p.ext && (
        <div class="panel-actions">
          <button class="btn sm" onClick={() => onAction(id, 'close')}>
            Close panel
          </button>
        </div>
      )}
    </div>
  )
}

// panelKey maps a browser KeyboardEvent to the extension panel key vocabulary
// (mirrors the TUI's panelKeyName/panelKeyText). Returns null for keys not
// forwarded (e.g. ctrl/meta combos).
function panelKey(e: KeyboardEvent): { name: string; text?: string } | null {
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

function stripAnsi(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*m/g, '')
}

// ResetsSection lists the provider's banked usage-reset credits under the usage
// summary and redeems one on demand. Redeeming spends a scarce, irreversible
// credit, so a click first arms an inline confirm (the credit's title + expiry)
// and only a second, explicit "Redeem" performs the spend — there is no
// auto-redeem. It self-fetches on mount and hides entirely when the provider
// offers no resets, so it's invisible everywhere except a codex subscription.
function ResetsSection({
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

function ResetRow({
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
  const expiry = r.expires_at ? r.expires_at.slice(0, 10) : ''
  const spent = r.status === 'redeemed'
  return (
    <div class={`reset-row${available ? '' : ' spent'}`}>
      <div class="reset-main">
        <span class="reset-title">{r.title || t('reset credit')}</span>
        <span class="reset-meta">
          {spent ? t('spent') : expiry ? t('expires %s', expiry) : r.status}
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

function ContextBody({
  d,
  onFetchNode,
  onListResets,
  onConsumeReset,
}: {
  d: ContextBreakdown
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
        windows={d.usage_windows}
      />

      <ResetsSection onList={onListResets} onConsume={onConsumeReset} />

      <div class="ctx-section-label">{t('Next request — estimated by size')}</div>
      <div class="ctx-rows">
        <CtxRow
          label={t('system prompt')}
          bytes={d.system_bytes}
          note={d.ext_guidance_bytes > 0 ? t('incl. %s ext guidance', humanBytes(d.ext_guidance_bytes)) : ''}
        />
        <CtxRow label={t('tool defs')} bytes={d.tool_bytes} note={tn(d.tool_count, '%d tool', '%d tools')} />
        <CtxRow label={t('ext context')} bytes={d.ext_bytes} note={t('cards, ephemeral')} />
        <CtxRow label={t('transcript')} bytes={d.transcript_bytes} note={tn(d.messages.length, '%d msg', '%d msgs')} />
        <div class="ctx-total">
          <CtxRow label={t('TOTAL')} bytes={d.total_bytes} note="" />
        </div>
      </div>
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
function ContextTree({
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
function TreeNode({
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

function CtxRow({ label, bytes, note }: { label: string; bytes: number; note: string }) {
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
function WindowRow({ w }: { w: UsageWindow }) {
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

function shortWindow(label: string): string {
  const l = label.trim().toLowerCase()
  if (l === 'weekly' || l === 'week') return 'wk'
  if (l === 'monthly' || l === 'month') return 'mo'
  return label
}

// countdown renders time until an RFC3339 instant as "3d17h" / "4h33m" / "12m".
function countdown(iso: string): string {
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
