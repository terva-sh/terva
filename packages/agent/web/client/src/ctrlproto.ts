// Typed ctrlproto client. Mirrors packages/agent/ctrlproto's wire shapes; the
// Go golden-frame tests pin the exact JSON these interfaces describe.

export interface WireUsage {
  input: number
  output: number
  cache_read: number
  cache_write: number
  cost_usd: number
}

export interface WireBlock {
  type: string
  text?: string
  name?: string
  args?: unknown
  id?: string
  call_id?: string
  is_error?: boolean
  content?: WireBlock[]
  mime_type?: string
  // data is the base64 image payload (Go []byte → base64 string), present on
  // image blocks only when the "image-data" feature is negotiated; bytes is
  // the size, always present. See docs/controllers.md.
  data?: string
  bytes?: number
}

// PromptImage is one inbound attachment on a prompt (the "images" feature).
// data is base64 — the JSON shape of ctrlproto.Image (Go []byte marshals to a
// base64 string).
export interface PromptImage {
  mime_type: string
  data: string
}

export interface WireMessage {
  role: string
  content: WireBlock[]
  time?: string
  synthetic?: boolean // host-injected (e.g. a continue-on-open-work nudge), not user-typed
}

export interface SessionInfo {
  id: string
  title?: string
  provider?: string
  model?: string
  persona?: string
  path?: string
  created?: string
  updated?: string
  messages: number
  usage: WireUsage
  current?: boolean
  trusted?: boolean
  context_tokens?: number
  context_window?: number
}

export interface ModelInfo {
  id: string
  provider: string
  context_window?: number
  max_output?: number
  reasoning?: boolean
  current?: boolean
  favorite?: boolean
}

export interface PermissionRequest {
  call_id: string
  tool: string
  preview?: string
}

export interface AskRequest {
  ask_id: string
  question: string
  options?: string[]
  allow_custom?: boolean
}

export interface SkillInfo {
  name: string
  description?: string
}

export interface Snapshot {
  session: SessionInfo
  messages: WireMessage[]
  busy: boolean
  permissions?: PermissionRequest[]
  asks?: AskRequest[]
  queued?: string[]
  skills?: SkillInfo[]
}

export interface ContextMessage {
  index: number
  kind: string
  bytes: number
}

// ContextNode is one node of the context-tree outline (ContextBreakdown.tree).
// The kind vocabulary is open — render an unknown kind by its label + bytes. ids
// are opaque, passed back to context.node (later stages). Mirrors ctrlproto.ContextNode.
export interface ContextNode {
  id: string
  kind: string // section | turn | message | block | event
  label: string
  bytes: number
  tokens?: number
  summary?: string
  content?: string // full leaf body, populated on a context.node expand
  expandable?: boolean
  reveal?: string
  meta?: Record<string, string>
  children?: ContextNode[]
}

export interface UsageWindow {
  label: string
  used_percent: number
  window_minutes?: number
  resets_at?: string
  kind?: string
}

export interface ContextBreakdown {
  provider?: string
  model?: string
  window: number
  system_bytes: number
  ext_guidance_bytes: number
  tool_bytes: number
  tool_count: number
  ext_bytes: number
  transcript_bytes: number
  total_bytes: number
  messages: ContextMessage[]
  context_tokens?: number
  cumulative: WireUsage
  subscription?: boolean
  usage_windows?: UsageWindow[]
  // Hierarchical outline (context-tree feature): sections → transcript turns →
  // message stubs. A superset of `messages`; render this when present, else the
  // flat list. `rev` is the transcript epoch the ids were minted at.
  tree?: ContextNode
  rev?: number
}

// Surfaces: the auxiliary panes (context, usage, extension panels) the pane host
// switches between. See docs/proposals/web-surfaces.md.
export interface SurfaceMeta {
  id: string
  title: string
  icon?: string
  kind: string // context | usage | panel | widgets | settings | tasks | commands
  scope?: string
  live?: boolean
  actions?: boolean
  badge?: string
}

// CatalogView is the effective web string catalog for one language (i18n.catalog):
// English-as-key singular translations + plurals keyed by the "one|other"
// composite. The client overlays it onto its bundled base.
export interface CatalogView {
  lang: string
  singular?: Record<string, string>
  plural?: Record<string, Record<string, string>>
}

export interface UsageView {
  provider?: string
  model?: string
  context_tokens?: number
  window?: number
  cumulative: WireUsage
  subscription?: boolean
  windows?: UsageWindow[]
}

export interface PanelView {
  ext?: string
  lines: string[]
  footer?: string
}

// Widget is one node of a generic pane tree (kind=widgets) — the extensible
// content model an extension sends instead of flat panel lines. `type`
// discriminates; the other fields are per-type. Mirrors ctrlproto.Widget.
export type WidgetTone = 'default' | 'muted' | 'danger' | 'ok' | 'warn'
export interface KV {
  key: string
  value: string
  note?: string
  mono?: boolean
}
export interface ListItemW {
  text: string
  note?: string
  tone?: WidgetTone
  action_id?: string
}
export interface Widget {
  type: 'heading' | 'text' | 'meter' | 'keyvalue' | 'table' | 'list' | 'group' | 'note' | 'action' | 'divider'
  text?: string
  tone?: WidgetTone
  level?: number
  label?: string
  value?: number
  max?: number
  unit?: string
  rows?: KV[]
  columns?: string[]
  cells?: string[][]
  items?: ListItemW[]
  children?: Widget[]
  action_id?: string
}

export interface WidgetsView {
  widgets: Widget[]
}

export interface TaskInfo {
  id: string
  task: string
  status: string
  activity?: string
  model?: string
  provider?: string
  persona?: string
  started?: string
  finished?: string
  error?: string
  tail?: string
}

export interface TaskList {
  tasks: TaskInfo[]
}

export interface SettingOption {
  value: string
  label: string
}

export interface SettingItem {
  key: string
  label: string
  type: string // "enum" | "bool"
  value: string
  options?: SettingOption[]
  description?: string
  note?: string
}

export interface SettingsView {
  items: SettingItem[]
}

// CommandEntry / CommandsView: the extension-command pane (kind=commands). Each
// entry is a registered slash command, rendered as a button (the web has no
// command line); running one is surface.action {action:"run", args:{name}}.
export interface CommandEntry {
  ext: string
  name: string
  description?: string
}

export interface CommandsView {
  commands: CommandEntry[]
}

// ExtensionInfo / ExtensionsView: the extension-management pane (kind=extensions)
// — a read-only inventory + health rollup of the session's extensions.
export interface ExtensionInfo {
  name: string
  version?: string
  language?: string
  description?: string
  scope?: string
  status: string // running | stopped | disabled | gated
  enabled: boolean // config says it should run (the toggle state)
  tools?: number
  commands?: number
  note?: string
}

export interface ExtensionsView {
  extensions: ExtensionInfo[]
}

// Lore inspector pane (kind=lore): read-only listing of authored keyword-
// triggered context entries.
export interface LoreEntry {
  name: string
  keys?: string[]
  constant?: boolean
  source?: string
  content?: string
  editable?: boolean
  scope?: string // user | project (where the entry's file lives)
}

export interface LoreView {
  entries: LoreEntry[]
  can_project?: boolean
}

// MCP management pane (kind=mcp): the workspace's MCP servers with live status +
// an enable/disable toggle.
export interface MCPServerInfo {
  name: string
  scope?: string
  description?: string
  status: string // running | stopped | disabled | gated | failed
  enabled: boolean
  tools?: number
  note?: string
}

export interface MCPView {
  servers: MCPServerInfo[]
}

// Permissions inspector pane (kind=permissions): approval mode + compiled rules
// (read-only) and the session's live "always-allow" grants (revocable).
export interface PermissionRuleInfo {
  tool: string
  args?: string
  decision: string // allow | deny | ask
  reason?: string
  source?: string
  removable?: boolean
}

export interface PermissionsView {
  mode: string
  rules?: PermissionRuleInfo[]
  allow_all?: boolean
  grants?: string[]
}

// Notice is a one-shot host-originated message shown in the conversation area
// without joining the transcript — e.g. an extension command's display/error
// result. Mirrors ctrlproto.Notice. kind, when set, is the machine-readable
// notice type (e.g. "prompt_rebuilt") with its structured payload in data;
// text always stands alone, so rendering it verbatim is a complete fallback —
// a kind-aware client may filter or re-render instead.
export interface Notice {
  level: string // info | error
  text: string
  ext?: string
  kind?: string
  data?: Record<string, string>
}

export interface Surface {
  id: string
  title: string
  kind: string
  context?: ContextBreakdown
  usage?: UsageView
  tasks?: TaskList
  settings?: SettingsView
  panel?: PanelView
  widgets?: Widget[]
  commands?: CommandsView
  extensions?: ExtensionsView
  permissions?: PermissionsView
  lore?: LoreView
  mcp?: MCPView
}

export interface WireEvent {
  type: string
  delta?: string
  id?: string
  name?: string
  args?: unknown
  text?: string
  is_error?: boolean
  content?: WireBlock[]
  usage?: WireUsage
  cumulative?: WireUsage
  message?: WireMessage
  stop?: string
  error?: string
  permission?: PermissionRequest
  ask?: AskRequest
  resolved?: { call_id?: string; ask_id?: string }
  snapshot?: Snapshot
  info?: SessionInfo
  queued?: string[]
  surface_id?: string
  locale?: string
  notice?: Notice
}

// ServerHello is the handshake frame the server sends back (role "server").
// We only read the bits the client acts on today — the active locale, so the
// PWA can select its string catalog to match the daemon.
export interface ServerHello {
  role: string
  protocol?: number
  agent?: string
  version?: string
  groups?: string[]
  features?: string[]
  locale?: string
}

export interface Frame {
  kind: 'hello' | 'cmd' | 'resp' | 'event'
  id?: number
  sess?: string
  hello?: ServerHello
  method?: string
  params?: unknown
  result?: unknown
  error?: { code: string; message: string }
  event?: WireEvent
}

export interface Decision {
  allow: boolean
  reason?: string
  remember_tool?: boolean
  remember_all?: boolean
}

export type Status = 'connecting' | 'open' | 'closed'

const PROTOCOL = 1

function wsURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const token =
    new URLSearchParams(location.search).get('token') ||
    localStorage.getItem('terva_token') ||
    ''
  const q = token ? '?token=' + encodeURIComponent(token) : ''
  return `${proto}//${location.host}/ws${q}`
}

// Client is a reconnecting ctrlproto peer over one WebSocket. Commands return
// promises correlated by id; events are delivered to onEvent by session.
export class Client {
  private ws: WebSocket | null = null
  private nextId = 1
  private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()
  private stopped = false

  onEvent: (sess: string, ev: WireEvent) => void = () => {}
  onStatus: (s: Status) => void = () => {}
  onReady: (hello?: ServerHello) => void = () => {}

  connect() {
    this.onStatus('connecting')
    const ws = new WebSocket(wsURL())
    this.ws = ws
    ws.onopen = () =>
      ws.send(
        JSON.stringify({
          kind: 'hello',
          hello: {
            role: 'client',
            protocol: PROTOCOL,
            agent: 'terva-web',
            version: '1',
            groups: ['conversation', 'session', 'control'],
            // images = inbound attachments on prompt; image-data = outbound
            // image payloads in the transcript (agent-generated images, echoed
            // attachments, tool-result screenshots) render as real pixels.
            features: ['images', 'image-data', 'resolve-events'],
          },
        }),
      )
    ws.onmessage = (e) => this.onFrame(JSON.parse(e.data as string) as Frame)
    ws.onclose = () => {
      this.onStatus('closed')
      this.rejectAll(new Error('connection closed'))
      if (!this.stopped) setTimeout(() => this.connect(), 1500)
    }
  }

  private onFrame(f: Frame) {
    if (f.kind === 'hello') {
      this.onStatus('open')
      this.onReady(f.hello)
      return
    }
    if (f.kind === 'resp' && f.id != null) {
      const p = this.pending.get(f.id)
      if (!p) return
      this.pending.delete(f.id)
      if (f.error) p.reject(new Error(`${f.error.code}: ${f.error.message}`))
      else p.resolve(f.result)
      return
    }
    if (f.kind === 'event' && f.event) this.onEvent(f.sess ?? '', f.event)
  }

  private isOpen(): boolean {
    return this.ws != null && this.ws.readyState === WebSocket.OPEN
  }

  send<T = unknown>(method: string, params: unknown, sess = ''): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      // Reject cleanly rather than letting ws.send() throw InvalidStateError on
      // a CONNECTING/closed socket (e.g. during a reconnect). Callers that care
      // catch this; the reconnect snapshot re-syncs state either way.
      if (!this.isOpen()) {
        reject(new Error('not connected'))
        return
      }
      const id = this.nextId++
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject })
      this.raw({ kind: 'cmd', id, sess, method, params })
    })
  }

  fire(method: string, params: unknown, sess = '') {
    // Fire-and-forget: silently drop if the socket isn't open. Calling send() on
    // a CONNECTING socket throws InvalidStateError, and there's no promise here
    // to reject; a reconnect re-subscribes and re-syncs via the snapshot.
    if (!this.isOpen()) return
    this.raw({ kind: 'cmd', id: this.nextId++, sess, method, params })
  }

  private raw(f: Frame) {
    // Guarded by isOpen() at the call sites, but re-check: send() on a non-OPEN
    // socket throws a DOMException (InvalidStateError).
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(f))
  }

  private rejectAll(e: Error) {
    for (const p of this.pending.values()) p.reject(e)
    this.pending.clear()
  }

  close() {
    this.stopped = true
    this.ws?.close()
  }
}
