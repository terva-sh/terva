import type { VNode } from 'preact'
import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'
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
  ModelInfo,
  PanelView,
  PermissionRequest,
  PermissionsView,
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
import { applyEvent, itemsFromMessages, type Item, type ImageAttachment } from './store'
import { renderMarkdown } from './markdown'
import { applyServerCatalog, setLocale, t, tn } from './i18n'

// ToolView mirrors the TUI's ctrl+t tool-display cycle, plus a web-only
// 'grouped' state that collapses a run of tool calls to a single "N tool calls"
// line you can expand — so a dozen tools between replies don't eat the view.
type ToolView = 'full' | 'grouped' | 'minimal' | 'hidden'
const TOOL_VIEWS: ToolView[] = ['full', 'grouped', 'minimal', 'hidden']

// SlashCommand is one user-driven composer command (/compact, /skill, …). `run`
// receives everything after the command name; `arg` is a display hint for the
// autocomplete and marks whether the command takes an argument.
interface SlashCommand {
  name: string
  arg?: string
  desc: string
  run: (arg: string) => void
}

// copyToClipboard writes text to the clipboard, falling back to a hidden
// textarea + execCommand for insecure (plain-http) contexts where
// navigator.clipboard is unavailable — terva web is commonly served over http on
// a LAN/Tailscale, so the modern API is often absent. Returns whether it worked.
async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Permission denied or non-secure context — fall through to the legacy path.
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

// maxImageBytes caps a single pasted/dropped image's raw size. The WS carrier
// bounds one frame at 16 MiB (packages/agent/web/conn.go); base64 inflates ~4/3
// and the frame also carries JSON + prompt text, so 10 MiB of raw image keeps a
// comfortable margin — exceeding the frame cap would drop the connection.
const maxImageBytes = 10 * 1024 * 1024

// fileToAttachment reads an image File into a base64 ImageAttachment, or null if
// it isn't an image or is over the size cap (the caller toasts the reason).
function fileToAttachment(f: File): Promise<ImageAttachment | { error: string } | null> {
  if (!f.type.startsWith('image/')) return Promise.resolve(null)
  if (f.size > maxImageBytes) {
    return Promise.resolve({ error: t('%s is too large (max 10 MB)', f.name || t('image')) })
  }
  return new Promise((resolve) => {
    const reader = new FileReader()
    reader.onload = () => {
      // readAsDataURL yields "data:<mime>;base64,<payload>"; keep the payload.
      const url = String(reader.result)
      const comma = url.indexOf(',')
      resolve(comma < 0 ? null : { mime: f.type, data: url.slice(comma + 1) })
    }
    reader.onerror = () => resolve(null)
    reader.readAsDataURL(f)
  })
}

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
      if (list.length) selectSession((list.find((s) => s.current) ?? list[0]).id)
      else await newSession(c)
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
        if (arg.trim()) switchModel(arg.trim())
        else setPickerOpen(true)
      },
    },
    {
      name: 'context',
      desc: t('Open the usage & context breakdown'),
      run: () => openPane('context'),
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
      run: () => localNotice(t('Slash commands: /compact, /clear, /skill, /model, /context, /new. Type / in the composer to autocomplete.')),
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
        <InfoPopover
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
        <SessionDrawer
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
          <MessageView
            items={items}
            busy={busy}
            toolView={toolView}
            queued={queued}
            onEditQueued={editQueued}
            onCancelQueued={cancelQueued}
          />

          {permission && <PermissionCard req={permission} onDecide={decide} />}
          {ask && <AskCard req={ask} onAnswer={answer} />}

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
            onClose={() => setPaneOpen(false)}
            onRestart={canRestart ? restart : undefined}
            trusted={!!curInfo?.trusted}
            onTrust={trustWorkspace}
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

function MessageView({
  items,
  busy,
  toolView,
  queued,
  onEditQueued,
  onCancelQueued,
}: {
  items: Item[]
  busy: boolean
  toolView: ToolView
  queued: string[]
  onEditQueued: (i: number, text: string) => void
  onCancelQueued: (i: number) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const pinnedRef = useRef(true)
  const [showJump, setShowJump] = useState(false)

  const onScroll = () => {
    const el = ref.current
    if (!el) return
    // Pinned = within 80px of the bottom. Scrolling up unpins so the stream
    // stops yanking the view back (TUI behavior).
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    pinnedRef.current = nearBottom
    setShowJump(!nearBottom)
  }

  useEffect(() => {
    const el = ref.current
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight
  }, [items, busy, queued])

  const jump = () => {
    const el = ref.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    pinnedRef.current = true
    setShowJump(false)
  }

  // Delegated copy for code blocks: markdown renders a .code-copy button per
  // block (see markdown.ts), and one listener here copies the adjacent <pre>'s
  // text — no per-block Preact handler inside the dangerouslySetInnerHTML.
  const onCodeCopy = useCallback((e: MouseEvent) => {
    const btn = (e.target as HTMLElement)?.closest?.('.code-copy') as HTMLElement | null
    if (!btn) return
    const text = btn.parentElement?.querySelector('pre')?.textContent ?? ''
    if (!text) return
    void copyToClipboard(text).then((ok) => {
      if (!ok) return
      btn.classList.add('copied')
      setTimeout(() => btn.classList.remove('copied'), 1200)
    })
  }, [])

  return (
    <div class="log-wrap">
      <div class="log" ref={ref} onScroll={onScroll} onClick={onCodeCopy}>
        {renderItems(items, toolView)}
        {busy && items[items.length - 1]?.kind !== 'assistant' && <div class="working">{t('working…')}</div>}
        {queued.map((q, i) => (
          <QueuedBubble
            key={'q' + i}
            text={q}
            onEdit={(t) => onEditQueued(i, t)}
            onCancel={() => onCancelQueued(i)}
          />
        ))}
      </div>
      {showJump && (
        <button class="jump" onClick={jump}>
          ↓ {t('jump to latest')}
        </button>
      )}
    </div>
  )
}

function InfoPopover({
  info,
  cost,
  onClose,
  onContext,
}: {
  info: SessionInfo | null
  cost: number
  onClose: () => void
  onContext: () => void
}) {
  if (!info) return null
  const copy = (s: string) => void copyToClipboard(s)
  return (
    <div class="info-scrim" onClick={onClose}>
      <div class="info-pop" onClick={(e) => e.stopPropagation()}>
        <div class="info-row">
          <span>{t('Persona')}</span>
          <b>{info.persona || '—'}</b>
        </div>
        <div class="info-row">
          <span>{t('Model')}</span>
          <b>
            {info.provider ? info.provider + ' / ' : ''}
            {info.model || '—'}
          </b>
        </div>
        <div class="info-row">
          <span>{t('Messages')}</span>
          <b>{info.messages}</b>
        </div>
        <div class="info-row">
          <span>{t('Cost')}</span>
          <b>${cost.toFixed(4)}</b>
        </div>
        <div class="info-row">
          <span>{t('Session')}</span>
          <b class="mono">{info.id}</b>
        </div>
        {info.path && (
          <div class="info-row path">
            <span>{t('Path')}</span>
            <code class="mono" title={info.path}>
              {info.path}
            </code>
            <button class="btn sm" onClick={() => copy(info.path!)}>
              {t('copy')}
            </button>
          </div>
        )}
        <div class="info-actions">
          <button class="btn sm" onClick={onContext}>
            {t('Context breakdown →')}
          </button>
        </div>
      </div>
    </div>
  )
}

// renderItems maps the transcript to bubbles. In 'grouped' mode it coalesces
// each run of consecutive tool calls into one collapsible ToolGroup, so a burst
// of tools reads as "N tool calls" until expanded.
function renderItems(items: Item[], toolView: ToolView): VNode[] {
  if (toolView !== 'grouped') {
    return items.map((it) => <Bubble key={it.id} item={it} toolView={toolView} />)
  }
  const out: VNode[] = []
  let run: Item[] = []
  const flush = () => {
    if (run.length > 0) {
      out.push(<ToolGroup key={'tg-' + run[0].id} tools={run} />)
      run = []
    }
  }
  for (const it of items) {
    if (it.kind === 'tool') {
      run.push(it)
      continue
    }
    flush()
    out.push(<Bubble key={it.id} item={it} toolView={toolView} />)
  }
  flush()
  return out
}

// ToolGroup collapses a run of tool calls to a single "N tool calls" line
// (with a summary of which tools + any failures), expandable to the full boxes.
function ToolGroup({ tools }: { tools: Item[] }) {
  const [expanded, setExpanded] = useState(false)
  const n = tools.length
  const failed = tools.reduce((c, t) => c + (t.kind === 'tool' && t.error ? 1 : 0), 0)
  return (
    <div class="tool-group">
      <button class={`tool-group-head${expanded ? ' open' : ''}`} onClick={() => setExpanded((e) => !e)}>
        <span class="tg-caret">{expanded ? '▾' : '▸'}</span>
        <span class="tg-count">{tn(n, '%d tool call', '%d tool calls')}</span>
        {!expanded && <span class="tg-summary">{summarizeTools(tools)}</span>}
        {failed > 0 && <span class="tg-failed">· {tn(failed, '%d failed', '%d failed')}</span>}
      </button>
      {expanded && (
        <div class="tool-group-body">
          {tools.map((t) => (
            <Bubble key={t.id} item={t} toolView="full" />
          ))}
        </div>
      )}
    </div>
  )
}

// summarizeTools names the tools in a group with counts: "bash ×3, read, edit".
function summarizeTools(tools: Item[]): string {
  const counts = new Map<string, number>()
  for (const t of tools) if (t.kind === 'tool') counts.set(t.name, (counts.get(t.name) ?? 0) + 1)
  const parts = [...counts.entries()].map(([name, c]) => (c > 1 ? `${name} ×${c}` : name))
  return parts.length > 4 ? parts.slice(0, 4).join(', ') + ', …' : parts.join(', ')
}

// CopyButton copies `text` to the clipboard and flashes a check for ~1.2s. Used
// on assistant replies (the raw markdown source, so fenced code round-trips) and
// wherever a one-tap copy helps.
function CopyButton({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const onClick = useCallback(() => {
    void copyToClipboard(text).then((ok) => {
      if (!ok) return
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    })
  }, [text])
  const title = copied ? t('Copied') : label || t('Copy')
  return (
    <button class={`copy-btn${copied ? ' copied' : ''}`} title={title} aria-label={title} onClick={onClick}>
      {copied ? (
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
          <path d="M20 6 9 17l-5-5" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <rect x="9" y="9" width="11" height="11" rx="2" />
          <path d="M5 15V5a2 2 0 0 1 2-2h10" />
        </svg>
      )}
    </button>
  )
}

// ImageGallery renders a message's image attachments as inline thumbnails that
// open full-size in a new tab. Data is base64 already on the item, so the src is
// a self-contained data: URL — no extra fetch.
function ImageGallery({ images }: { images: ImageAttachment[] }) {
  return (
    <div class="msg-images">
      {images.map((im, i) => {
        const src = `data:${im.mime};base64,${im.data}`
        return (
          <a key={i} class="msg-image-link" href={src} target="_blank" rel="noreferrer">
            <img class="msg-image" src={src} alt={t('attached image')} loading="lazy" />
          </a>
        )
      })}
    </div>
  )
}

function Bubble({ item, toolView }: { item: Item; toolView: ToolView }) {
  switch (item.kind) {
    case 'user':
      return (
        <div class="msg-wrap user-wrap">
          <div class="msg user">
            {item.text}
            {item.images && <ImageGallery images={item.images} />}
          </div>
          {item.text && <CopyButton text={item.text} />}
        </div>
      )
    case 'assistant':
      // Stream as raw text (re-parsing markdown per token is wasteful); render
      // markdown once the message finalizes.
      if (item.streaming) {
        return <div class="msg assistant streaming">{item.text}</div>
      }
      return (
        <div class="msg-wrap assistant-wrap">
          <div class="msg assistant md">
            <div dangerouslySetInnerHTML={{ __html: renderMarkdown(item.text) }} />
            {item.images && <ImageGallery images={item.images} />}
          </div>
          {item.text && <CopyButton text={item.text} />}
        </div>
      )
    case 'error':
      return <div class="msg err">{item.text}</div>
    case 'system':
      // Host-injected nudge (e.g. continue-on-open-work) — de-emphasized so it
      // doesn't read as something the user typed.
      return (
        <div class="sys-note">
          <span class="sys-tag">{t('auto')}</span>
          {item.text}
        </div>
      )
    case 'notice':
      // A one-shot result from an extension command (display/error). Attributed
      // to its extension, styled red when it's an error.
      return (
        <div class={`sys-note notice${item.level === 'error' ? ' err' : ''}`}>
          {item.ext && <span class="sys-tag">{item.ext}</span>}
          {item.text}
        </div>
      )
    case 'tool':
      if (toolView === 'hidden') return null
      if (toolView === 'minimal') {
        return (
          <div class="tool-min" aria-hidden="true">
            ▸ {item.name}
            {item.error ? ' · ' + t('failed') : ''}
          </div>
        )
      }
      return (
        <div class="tool">
          <div class="tool-head">
            <span class="tool-name">{item.name}</span>
            {item.args != null && <span class="tool-args">{compact(item.args)}</span>}
          </div>
          {item.result && <pre class={`tool-result${item.error ? ' err' : ''}`}>{truncate(item.result, 2000)}</pre>}
          {item.images && <ImageGallery images={item.images} />}
        </div>
      )
  }
}

function SessionDrawer(props: {
  sessions: SessionInfo[]
  current: string
  onSelect: (id: string) => void
  onNew: () => void
  onRename: (s: SessionInfo) => void
  onDelete: (s: SessionInfo) => void
  onClose: () => void
}) {
  return (
    <div class="drawer-scrim" onClick={props.onClose}>
      <aside class="drawer" onClick={(e) => e.stopPropagation()}>
        <div class="drawer-head">
          <strong>{t('Sessions')}</strong>
          <button class="btn" onClick={props.onNew}>
            + {t('New')}
          </button>
        </div>
        <div class="session-list">
          {props.sessions.map((s) => (
            <div key={s.id} class={`session${s.id === props.current ? ' active' : ''}`} onClick={() => props.onSelect(s.id)}>
              <div class="session-main">
                <div class="session-title">{s.title || s.id}</div>
                <div class="session-meta">
                  {s.model ? s.model + ' · ' : ''}
                  {tn(s.messages, '%d msg', '%d msgs')}
                  {s.usage?.cost_usd ? ' · $' + s.usage.cost_usd.toFixed(3) : ''}
                </div>
              </div>
              <button class="icon sm" title={t('Rename')} onClick={(e) => (e.stopPropagation(), props.onRename(s))}>
                ✎
              </button>
              <button class="icon sm" title={t('Delete')} onClick={(e) => (e.stopPropagation(), props.onDelete(s))}>
                ×
              </button>
            </div>
          ))}
        </div>
      </aside>
    </div>
  )
}

function PermissionCard({ req, onDecide }: { req: PermissionRequest; onDecide: (id: string, d: Decision) => void }) {
  return (
    <div class="card perm">
      <div class="card-head">
        {t('Approve tool:')} <code>{req.tool}</code>
      </div>
      {req.preview && <pre class="preview">{truncate(req.preview, 1500)}</pre>}
      <div class="card-actions">
        <button class="btn primary" onClick={() => onDecide(req.call_id, { allow: true })}>
          {t('Allow')}
        </button>
        <button class="btn" onClick={() => onDecide(req.call_id, { allow: true, remember_tool: true })}>
          {t('Allow & remember')}
        </button>
        <button class="btn danger" onClick={() => onDecide(req.call_id, { allow: false, reason: 'denied by user' })}>
          {t('Deny')}
        </button>
      </div>
    </div>
  )
}

function AskCard({ req, onAnswer }: { req: AskRequest; onAnswer: (id: string, text: string) => void }) {
  const [custom, setCustom] = useState('')
  return (
    <div class="card ask">
      <div class="card-head">{req.question}</div>
      <div class="card-actions">
        {(req.options ?? []).map((o) => (
          <button class="btn" onClick={() => onAnswer(req.ask_id, o)}>
            {o}
          </button>
        ))}
      </div>
      {req.allow_custom && (
        <form
          class="ask-custom"
          onSubmit={(e) => {
            e.preventDefault()
            if (custom.trim()) onAnswer(req.ask_id, custom.trim())
          }}
        >
          <input value={custom} onInput={(e) => setCustom((e.target as HTMLInputElement).value)} placeholder={t('custom answer…')} />
          <button class="btn primary" type="submit">
            {t('Send')}
          </button>
        </form>
      )}
    </div>
  )
}

function Composer({
  busy,
  onSend,
  onToast,
  commands,
  skills,
  onCancel,
}: {
  busy: boolean
  onSend: (t: string, images: ImageAttachment[]) => boolean
  onToast: (msg: string) => void
  commands: SlashCommand[]
  skills: SkillInfo[]
  onCancel: () => void
}) {
  const [text, setText] = useState('')
  const [images, setImages] = useState<ImageAttachment[]>([])
  const [sel, setSel] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const ref = useRef<HTMLTextAreaElement>(null)
  // Auto-grow to content (capped at 40vh), and shrink back when cleared — so
  // multiline is visible and signals there's more context in play.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, Math.round(window.innerHeight * 0.4)) + 'px'
  }, [text])

  // addFiles reads image files (from paste or drop) into attachments, toasting
  // any that are the wrong type or too big rather than silently dropping them.
  const addFiles = async (files: File[]) => {
    const results = await Promise.all(files.map(fileToAttachment))
    const ok: ImageAttachment[] = []
    for (const r of results) {
      if (r && 'error' in r) onToast(r.error)
      else if (r) ok.push(r)
    }
    if (ok.length) setImages((cur) => [...cur, ...ok])
  }

  const submit = () => {
    if (!text.trim() && images.length === 0) return
    // Clear only if the send was accepted (a busy image send is refused so the
    // attachments aren't lost).
    if (onSend(text, images)) {
      setText('')
      setImages([])
      setDismissed(false)
    }
  }
  // Choose a command from the menu: argless commands run immediately; commands
  // that take an argument prime "/name " and keep focus for the argument.
  const chooseCmd = (c: SlashCommand) => {
    if (c.arg) {
      setText('/' + c.name + ' ')
      ref.current?.focus()
    } else {
      onSend('/' + c.name, [])
      setText('')
    }
    setDismissed(false)
  }

  // Two autocomplete stages: command names while the text is a bare "/partial",
  // then skill names once "/skill " is being argued. Each item knows how to
  // apply itself on select.
  type MenuItem = { key: string; label: string; hint: string; apply: () => void }
  const cmdStage = /^\/(\S*)$/.exec(text)
  const skillStage = /^\/skill\s+(\S*)$/.exec(text)
  let menu: MenuItem[] = []
  if (cmdStage) {
    const q = cmdStage[1].toLowerCase()
    menu = commands
      .filter((c) => c.name.startsWith(q))
      .map((c) => ({
        key: c.name,
        label: '/' + c.name + (c.arg ? ' ' + c.arg : ''),
        hint: c.desc,
        apply: () => chooseCmd(c),
      }))
  } else if (skillStage) {
    const q = skillStage[1].toLowerCase()
    menu = skills
      .filter((s) => s.name.toLowerCase().startsWith(q))
      .map((s) => ({
        key: s.name,
        label: s.name,
        hint: s.description ?? '',
        apply: () => {
          setText('/skill ' + s.name + ' ')
          ref.current?.focus()
          setDismissed(false)
        },
      }))
  }
  const menuOpen = menu.length > 0 && !dismissed
  const clampedSel = Math.min(sel, Math.max(0, menu.length - 1))

  return (
    <footer
      class="composer"
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        const files = [...(e.dataTransfer?.files ?? [])].filter((f) => f.type.startsWith('image/'))
        if (files.length) {
          e.preventDefault()
          void addFiles(files)
        }
      }}
    >
      {menuOpen && (
        <div class="slash-menu" role="listbox">
          {menu.map((it, i) => (
            <button
              key={it.key}
              class={`slash-item${i === clampedSel ? ' sel' : ''}`}
              role="option"
              aria-selected={i === clampedSel}
              onMouseDown={(e) => {
                e.preventDefault()
                it.apply()
              }}
            >
              <span class="slash-name">{it.label}</span>
              {it.hint && <span class="slash-desc">{it.hint}</span>}
            </button>
          ))}
        </div>
      )}
      {images.length > 0 && (
        <div class="composer-chips">
          {images.map((im, i) => (
            <div key={i} class="composer-chip">
              <img src={`data:${im.mime};base64,${im.data}`} alt={t('attached image')} />
              <button
                class="chip-x"
                title={t('Remove')}
                aria-label={t('Remove')}
                onClick={() => setImages((cur) => cur.filter((_, j) => j !== i))}
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}
      <textarea
        ref={ref}
        rows={1}
        value={text}
        placeholder={t('Message terva…')}
        onPaste={(e) => {
          const files = [...(e.clipboardData?.items ?? [])]
            .filter((it) => it.kind === 'file' && it.type.startsWith('image/'))
            .map((it) => it.getAsFile())
            .filter((f): f is File => f != null)
          if (files.length) {
            e.preventDefault()
            void addFiles(files)
          }
        }}
        onInput={(e) => {
          setText((e.target as HTMLTextAreaElement).value)
          setSel(0)
          setDismissed(false)
        }}
        onKeyDown={(e) => {
          if (menuOpen) {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setSel((s) => Math.min(s + 1, menu.length - 1))
              return
            }
            if (e.key === 'ArrowUp') {
              e.preventDefault()
              setSel((s) => Math.max(s - 1, 0))
              return
            }
            if (e.key === 'Escape') {
              e.preventDefault()
              setDismissed(true)
              return
            }
            if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
              e.preventDefault()
              menu[clampedSel].apply()
              return
            }
          }
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            submit()
          }
        }}
      />
      {busy ? (
        <button class="btn danger" onClick={onCancel} title={t('Stop')}>
          {t('Stop')}
        </button>
      ) : (
        <button class="btn primary" onClick={submit}>
          {t('Send')}
        </button>
      )}
    </footer>
  )
}

// QueuedBubble is a pending user message shown while a turn runs. It can be
// edited in place or removed before the agent consumes it.
function QueuedBubble({
  text,
  onEdit,
  onCancel,
}: {
  text: string
  onEdit: (t: string) => void
  onCancel: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(text)
  const editRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    if (editing) editRef.current?.focus()
  }, [editing])
  if (editing) {
    return (
      <form
        class="msg user queued editing"
        onSubmit={(e) => {
          e.preventDefault()
          setEditing(false)
          if (draft.trim() !== text) onEdit(draft)
        }}
      >
        <textarea
          value={draft}
          rows={1}
          ref={editRef}
          onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              ;(e.currentTarget.form as HTMLFormElement).requestSubmit()
            } else if (e.key === 'Escape') {
              setDraft(text)
              setEditing(false)
            }
          }}
        />
        <div class="queued-actions">
          <button type="submit" class="btn sm primary">
            {t('Save')}
          </button>
          <button
            type="button"
            class="btn sm"
            onClick={() => {
              setDraft(text)
              setEditing(false)
            }}
          >
            {t('Cancel')}
          </button>
        </div>
      </form>
    )
  }
  return (
    <div class="msg user queued" title={t('queued — sends when the current turn reaches a safe point')}>
      <span class="queued-mark">⟳</span>
      <span class="queued-text">{text}</span>
      <span class="queued-tools">
        <button
          class="icon sm"
          title={t('Edit queued message')}
          onClick={() => {
            setDraft(text)
            setEditing(true)
          }}
        >
          ✎
        </button>
        <button class="icon sm" title={t('Remove from queue')} onClick={onCancel}>
          ×
        </button>
      </span>
    </div>
  )
}

// ModelPicker is the searchable model switcher: a filter box over favorites +
// per-provider groups, each row a click-to-switch with its own ★ toggle.
function ModelPicker({
  groups,
  favorites,
  current,
  onSwitch,
  onToggleFavorite,
  onClose,
}: {
  groups: [string, ModelInfo[]][]
  favorites: ModelInfo[]
  current?: string
  onSwitch: (id: string, provider?: string) => void
  onToggleFavorite: (provider: string, id: string, on: boolean) => void
  onClose: () => void
}) {
  const [q, setQ] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)
  useEffect(() => searchRef.current?.focus(), [])
  const needle = q.trim().toLowerCase()
  const match = (m: ModelInfo) =>
    !needle || m.id.toLowerCase().includes(needle) || m.provider.toLowerCase().includes(needle)
  const favMatched = favorites.filter(match)
  const groupsMatched = groups
    .map(([prov, ms]) => [prov, ms.filter(match)] as [string, ModelInfo[]])
    .filter(([, ms]) => ms.length > 0)

  const row = (m: ModelInfo, keyPrefix: string) => (
    <div
      key={keyPrefix + m.provider + '/' + m.id}
      class={`pick-row${m.id === current ? ' current' : ''}`}
      onClick={() => onSwitch(m.id, m.provider)}
    >
      <button
        class="pick-star"
        title={m.favorite ? t('Unfavorite') : t('Favorite')}
        onClick={(e) => {
          e.stopPropagation()
          onToggleFavorite(m.provider, m.id, !m.favorite)
        }}
      >
        {m.favorite ? '★' : '☆'}
      </button>
      <span class="pick-id">{m.id}</span>
      <span class="pick-meta">
        {m.provider}
        {m.context_window ? ' · ' + humanCount(m.context_window) : ''}
      </span>
    </div>
  )

  return (
    <div class="modal-scrim" onClick={onClose}>
      <div class="modal picker" onClick={(e) => e.stopPropagation()}>
        <input
          class="pick-search"
          ref={searchRef}
          placeholder={t('Search models…')}
          value={q}
          onInput={(e) => setQ((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => e.key === 'Escape' && onClose()}
        />
        <div class="pick-list">
          {favMatched.length > 0 && <div class="pick-group">★ {t('favorites')}</div>}
          {favMatched.map((m) => row(m, 'fav:'))}
          {groupsMatched.map(([prov, ms]) => (
            <div key={'grp' + prov}>
              <div class="pick-group">{prov}</div>
              {ms.map((m) => row(m, 'g:'))}
            </div>
          ))}
          {favMatched.length === 0 && groupsMatched.length === 0 && (
            <div class="pick-empty">{t('no models match “%s”', q)}</div>
          )}
        </div>
      </div>
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
  onClose,
  onRestart,
  trusted,
  onTrust,
}: {
  surfaces: SurfaceMeta[]
  active: string
  data: Surface | null
  err: string
  onActivate: (id: string) => void
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onClose: () => void
  onRestart?: () => void
  trusted?: boolean
  onTrust?: (trust: boolean) => void
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
            onRestart={onRestart}
            trusted={trusted}
            onTrust={onTrust}
          />
        )}
      </div>
    </aside>
  )
}

function SurfaceView({
  surface,
  onAction,
  onFetchNode,
  onRestart,
  trusted,
  onTrust,
}: {
  surface: Surface
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
  onRestart?: () => void
  trusted?: boolean
  onTrust?: (trust: boolean) => void
}) {
  switch (surface.kind) {
    case 'context':
      return surface.context ? <ContextBody d={surface.context} onFetchNode={onFetchNode} /> : null
    case 'tasks':
      return surface.tasks ? <TasksBody list={surface.tasks} onAction={onAction} /> : null
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
    default:
      return <div class="pick-empty">{t('unsupported pane')}</div>
  }
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

function ContextBody({
  d,
  onFetchNode,
}: {
  d: ContextBreakdown
  onFetchNode: (id: string, op?: string) => Promise<ContextNode>
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

function humanBytes(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB'
  return n + ' B'
}

function humanCount(n: number): string {
  if (n >= 1_000_000) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1e3).toFixed(0) + 'k'
  return String(n)
}

function compact(v: unknown): string {
  try {
    return truncate(JSON.stringify(v), 200)
  } catch {
    return ''
  }
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s
}
