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
  // The summary a compaction checkpoint left in place of the turns it folded
  // away, and its estimate of what those turns cost. Its role is 'user' — render
  // the pair as a divider, not as a user bubble, or the transcript shows the raw
  // "## Context Summary" markdown as if the user had typed it.
  compaction?: boolean
  tokens_before?: number
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
  // default marks the model NEW sessions start on — not the one this session is
  // on (that is `current`, and the two are deliberately allowed to differ).
  default?: boolean
  default_scope?: 'global' | 'project'
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

// WireFileEntry is one workspace file/directory from files.list (Go
// ctrlproto.FileEntry): path relative to the workspace cwd, "/"-separated.
export interface WireFileEntry {
  path: string
  dir?: boolean
}

// FilesListResult is the files.list response (Go ctrlproto.FilesListResult).
export interface FilesListResult {
  files: WireFileEntry[]
  truncated?: boolean
}

export interface Snapshot {
  session: SessionInfo
  // messages is a WINDOW — the tail of the transcript — because we negotiate
  // 'history-window'. epoch/base/total place it: base is the index of messages[0], so
  // base > 0 means there is more above (fetch it with conversation.history), and epoch
  // identifies the transcript itself. epoch changes ONLY when the transcript is
  // wholesale replaced (compacted, cleared) — the signal to rebuild rather than merge.
  epoch: number
  base: number
  total: number
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

// ResetInfo is one consumable usage-reset credit (codex banked reset), mirroring
// ctrlproto.ResetInfo. Times are RFC 3339 strings.
export interface ResetInfo {
  id: string
  kind?: string
  title?: string
  description?: string
  status: string // available | pending | redeemed | expired
  granted_at?: string
  expires_at?: string
  redeemed_at?: string
}

export interface ResetsListResult {
  supported: boolean
  resets?: ResetInfo[]
}

export interface ResetConsumeResult {
  reset: ResetInfo
  windows_reset?: number
}

export interface ContextBreakdown {
  provider?: string
  model?: string
  window: number
  system_bytes: number
  ext_guidance_bytes: number
  tool_bytes: number // advertised tool defs (what the model receives); excludes lazy-inactive groups
  tool_count: number
  tool_bytes_installed?: number // full installed-registry weight, set only when lazy visibility hides some tools
  tool_count_installed?: number
  ext_bytes: number // ephemeral tail: ext cards + the lazy-tool capability note
  lazy_note_bytes?: number // the inactive-tool-groups note's share of ext_bytes (names of hidden groups)
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

// The raati deliberation board (kind=raati): three panelist blocks plus
// the tallied verdict. An idle view (no units, no decision) means the
// client renders the convene form.
export interface RaatiUnit {
  name: string
  accent?: string
  binding?: string // this seat's provider/model
  status: string // deliberating | voted | absent
  verdict?: string
  confidence?: number
  rationale?: string
  why?: string
  blind?: string
}

export interface RaatiTally {
  approve: number
  reject: number
  abstain: number
  absent: number
}

export interface RaatiVoice {
  unit: string
  rationale?: string
}

export interface RaatiHistoryItem {
  id: string
  when?: string
  question: string
  class?: string
  decision: string
  degraded?: boolean
  tally?: RaatiTally
  minority?: string[] // dissenting unit names
}

export interface RaatiInquiry {
  unit: string
  question: string
  answer?: string
  source?: string // record | convener | unanswered
  round?: number
}

export interface RaatiProfileInfo {
  name: string
  description?: string
}

export interface RaatiView {
  running: boolean
  question?: string
  class?: string
  round?: number
  seat_order?: string // fixed | convene | turn — how the pool was dealt
  phase?: string // "briefing" while the clerk summarizes the conversation
  archived?: boolean
  when?: string
  history?: RaatiHistoryItem[]
  binding?: string
  units?: RaatiUnit[]
  decision?: string // approved | rejected | escalated
  degraded?: boolean
  tally?: RaatiTally
  minority?: RaatiVoice[]
  inquiries?: RaatiInquiry[]
  profiles?: RaatiProfileInfo[] // configured convening profiles (names + purpose)
  error?: string
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
  raati?: RaatiView
  chat?: ChatView
  providers?: ProvidersView
}

// Providers pane (kind=providers): the daemon's MODEL-PROVIDER credentials.
//
// Not this panel's own login. The bearer token you typed to get in here answers
// "may you talk to this daemon"; this answers "may this daemon talk to Anthropic".
// The pane is called Providers, never Login, precisely so the two do not blur.
//
// Nothing here is a secret: no key, no token, not a prefix of either.
export interface ProvidersView {
  providers: ProviderInfo[]
  // Whether the daemon will serve a login. False today — the pane reports, and
  // points you at the terminal, rather than showing a control that does nothing.
  can_login?: boolean
}

// AuthFlowStep is what the daemon asks us to render for a login. One shape for
// every provider — the client does not know, and must not need to know, which
// fields Anthropic wants versus an OpenAI-compatible endpoint. Add a provider
// that needs a field nobody anticipated, and this file does not change.
export interface AuthFlowStep {
  // Opaque handle for this attempt; required on submit. A submit against a
  // superseded handle is refused rather than exchanged against the wrong flow.
  flow: string
  // form: collect fields, then submit.
  // display: show url (+ user_code) and wait — the daemon is polling, and the
  //          result arrives as an auth_state event. Nothing to submit.
  // info: read-only prose. Nothing to submit, ever.
  kind: 'form' | 'display' | 'info'
  title?: string
  lines?: string[]
  // DISPLAYED, never auto-opened: the daemon's browser is not ours.
  url?: string
  user_code?: string
  fields?: AuthField[]
}

export interface AuthField {
  name: string
  label: string
  // A rendering hint. Everything goes back as a string, including integer — the
  // daemon parses, so there is exactly one place that decides what is valid.
  type: 'text' | 'secret' | 'integer'
  required?: boolean
  // Names another field that relaxes this one: required only while THAT field is
  // empty. openai-compatible's default model needs it — mandatory for the single
  // shared slot, pointless for a named endpoint, which discovers its own models.
  // Without it the form could not tell the operator what is still outstanding,
  // and a form that goes quietly dead is the bug this pane exists to prevent.
  required_unless?: string
  default?: string
  placeholder?: string
  help?: string
}

// auth_state event payload: a login flow moved. Arrives on the WORKSPACE address,
// because a credential belongs to the daemon and the flow may finish while no
// session is in focus — you authorized in a browser on another device.
export interface AuthState {
  kind: 'started' | 'success' | 'error' | 'canceled'
  flow?: string
  provider?: string
  method?: string
  url?: string
  user_code?: string
  message?: string
}

export interface ProviderInfo {
  // The LOGIN id: "openai" (a platform API key) and "openai-codex" (a ChatGPT
  // subscription) are separate logins that share one slot on disk.
  id: string
  label: string
  method?: string // apikey | oauth | "" (not logged in)
  expiry?: string // RFC 3339, oauth only
  expired?: boolean
  offers?: string[] // apikey | oauth | env
  base_url?: string
  model?: string
  // A server the OPERATOR named (config.json "endpoints"): its own provider, with
  // its own /v1/models discovery. A definition, not a credential — so it is
  // removed by its own verb, never by a logout. Signing out forgets a secret you
  // can re-enter; forgetting this deletes terva's only record of a machine.
  endpoint?: boolean
  // Setup guidance for a provider terva stores no credential for at all — for
  // these, "logging in" means setting environment variables.
  note?: string[]
}

// Chat-connector pane (kind=chat): the registered services and the live bridge.
// Workspace-scoped — the bridge belongs to the workspace and is bound to one of
// its sessions, and does not follow whichever session this tab is showing.
export interface ChatServiceInfo {
  name: string
  kind?: string // "" | "extension"
  dev?: boolean
  configured: boolean
  paired: boolean
}

export interface ChatBridgeState {
  state: string // idle | connecting | connected | error
  connector?: string
  username?: string
  paired_id?: string
  session?: string
  error?: string
}

export interface ChatView {
  services?: ChatServiceInfo[]
  bridge: ChatBridgeState
  // A `terva bot` daemon already polling this service; connect is refused.
  daemon_pid?: number
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
  auth?: AuthState
}

// ServerHello is the handshake frame the server sends back (role "server").
// We only read the bits the client acts on today — the active locale (so the
// PWA can select its string catalog to match the daemon), the workspace cwd,
// and the feature list.
// ADDR_WORKSPACE is the reserved address for facts that are true of the daemon
// rather than of any session: a workspace surface changing, the locale changing,
// a notice — and, once the auth group lands, a provider login.
//
// It is an address, not a session: never pass it to a conversation verb, and
// never render it as a session id. The empty string is NOT the same thing — on a
// command that means "the default session", and the daemon will materialize one.
export const ADDR_WORKSPACE = '#workspace'

export interface ServerHello {
  role: string
  protocol?: number
  agent?: string
  version?: string
  groups?: string[]
  features?: string[]
  locale?: string
  // The daemon's workspace working directory (Go Hello.CWD): names the tree
  // the panel controls.
  cwd?: string
  // The workspace sandbox lock (Go Hello.Jailed).
  jailed?: boolean
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

// The per-model overrides in models.json — context window, max tokens,
// temperature. Described by the daemon, rendered blind by the client: nothing
// here names a setting, so a provider that gains a knob costs no client change.
export interface ModelParamSpec {
  key: string
  label: string
  // A rendering hint. Everything goes back as a string and the DAEMON parses —
  // bounds live in packages/provider, and a second opinion on the wire is a
  // second thing to disagree.
  kind: 'text' | 'int' | 'float'
  // What this model takes with NO override. Belongs in the placeholder, never in
  // the box: a pre-filled default reads as an override and would be saved as one.
  default?: string
  // The override currently pinned in models.json, or "" for none.
  value?: string
  min?: number
  max?: number
  help?: string
}

export interface ModelParamsView {
  provider: string
  model: string
  // Whether models.json holds an entry — i.e. whether a reset would do anything.
  has_override?: boolean
  params: ModelParamSpec[]
}
