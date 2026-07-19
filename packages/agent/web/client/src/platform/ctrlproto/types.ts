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
  // experience tags an immersive (Stage) session — 'chat' | 'play' — empty for a
  // coding session, so a client badges it distinctly. background is the bound
  // scene backdrop id, fetched from /media/backgrounds/<id>. Both persisted in
  // session meta (Phase 0/2).
  experience?: string
  background?: string
  // note is the session's author's note — a live steering string injected into the
  // uncached per-turn tail (set via note.set), '' for none. Immersive only.
  note?: string
  // user_name / user_description are the bound user persona — who the user is in
  // the story (set via user.bind), distinct from persona (who the agent is). The
  // description rides the free per-turn tail; the name is the {{user}} macro baked
  // into the cached prefix, so changing it is a deliberate rebuild. Immersive only.
  user_name?: string
  user_description?: string
  // supports_continue is true when the session's provider can extend a trailing
  // assistant message as a prefill (turn.continue) — the Stage "continue" gate. A
  // client also needs a trailing assistant message and an idle session.
  supports_continue?: boolean
  // card is the character-card ref (library id) the session was created from, so
  // the Stage library groups a character's chats under it.
  card?: string
  // cast is a play session's ensemble (actor name → persona/card ref) the
  // director can bring on stage via actor_spawn; empty for a solo chat.
  cast?: Record<string, string>
  path?: string
  created?: string
  updated?: string
  messages: number
  usage: WireUsage
  current?: boolean
  trusted?: boolean
  context_tokens?: number
  context_window?: number
  // live = materialized in memory (subscribable); busy = a turn in flight.
  // Absent from an old daemon — read as unknown, not idle/cold. The board keys
  // its tile status off these (orchestration frontend stage 4.0).
  live?: boolean
  busy?: boolean
}

// CreateOpts parameterizes sessions.create. Beyond the coding fields
// (title/provider/model/persona/template), the immersive (Stage) fields decide
// what the session IS: experience selects 'chat'/'play'; card is a library id or
// path; greeting picks the opening; cast declares play actors; background binds a
// scene backdrop. All optional — an empty object is an ordinary coding session.
export interface CreateOpts {
  title?: string
  provider?: string
  model?: string
  persona?: string
  template?: string
  experience?: string
  card?: string
  cast?: Record<string, string>
  greeting?: number
  background?: string
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
  // agent names the WORKER (swarm agent) this approval belongs to, when the
  // request is a foreign worker's routed to the dispatching session's human card
  // (workerConfirmer sets it; a session's own tool call leaves it empty). The
  // board correlates it to the swarm-lane tile of the stalled worker.
  agent?: string
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
  // tail describes the swipeable takes of the last response span (turn.swipe/retry),
  // present only when there are 2+ takes. The Stage chat draws swipe arrows from it.
  tail?: TailInfo
  // variant_marks is the superset of tail: every switchable position — the tail
  // span (span=true) plus message-scoped edit variants at any position (Option C).
  // A client draws a `‹n/m›` control at each and switches with turn.swipe{index,variant}.
  variant_marks?: VariantMark[]
}

// TailInfo places the tail span's variants: span_start is the index of the span's
// first message, variants the count of takes, active the selected one.
export interface TailInfo {
  span_start: number
  variants: number
  active: number
}

// VariantMark places one switchable position for the per-position swipe UI: its
// transcript index, take count, active take, and whether it is the tail suffix span
// (span=true) or a message-scoped edit (span=false, one message, shared downstream).
export interface VariantMark {
  index: number
  variants: number
  active: number
  span?: boolean
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
  // lore_fired is the last turn's lore activation trace — which entries fed the
  // ext_bytes ephemeral tail, why (matched keys), and what the budget dropped.
  // The Usage pane's home for the 4c trace (the Stage drawer is the other).
  lore_fired?: ContextLoreEntry[]
}

// ContextLoreEntry is one entry of ContextBreakdown.lore_fired.
export interface ContextLoreEntry {
  name: string
  source?: string
  constant?: boolean // always-on (baked into the prompt, no trigger keys)
  keys?: string[] // the trigger keys that matched (empty for a constant entry)
  dropped?: boolean // fired but cut for token budget
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
  cost_usd?: number // worker's cumulative spend so far (absent/0 when unknown)
  tail?: string
  // backend = which engine drives the child ("claude", "terva", …; absent = a
  // native terva swarm agent). Present only when an external-agent-workers
  // daemon serves it; the tile hides it otherwise, so the lane renders native
  // children today and lights up workers with no board change.
  backend?: string
}

export interface TaskList {
  tasks: TaskInfo[]
  // backends a human may spawn a worker against (worker.Names() — the same set
  // the model's swarm_spawn enum uses). Native (empty backend) is always
  // available and NOT listed. workers_enabled mirrors the daemon's
  // external_workers knob: when false the backends still list (so the picker can
  // show them greyed, with a hint), but a foreign spawn is gated and refused.
  backends?: string[]
  workers_enabled?: boolean
}

// Managed git worktrees (kind=worktrees): the built-in worktree engine's list
// plus the merge-back (collect) overview, both riding one fetch. Read-only, and
// not Live — no push event exists for worktree changes, so the pane fetches on
// open and offers a manual refresh.
export interface WorktreeViewItem {
  name: string
  path: string
  branch?: string
  base_commit?: string
  base_ref?: string
  head_commit?: string
  status: string // available | claimed
  claimed_by?: string // "self" = claimed by this session
  stale_reason?: string
  dirty?: boolean
  unmanaged?: boolean
}

export interface WorktreeCollectItem {
  name: string
  branch?: string
  base_ref?: string
  base_commit?: string
  head_commit?: string
  ahead: number
  commits?: string[]
  dirty?: boolean
  unpushed?: boolean
}

export interface WorktreeView {
  repo_key: string
  cwd_worktree?: string
  items?: WorktreeViewItem[]
  collect?: WorktreeCollectItem[]
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
  // The last turn's activation trace, for triggered entries (constant lore is
  // always-on/baked, never here). fired = it fired last turn, matched_keys = why
  // (the trigger keys that hit), dropped_for_budget = fired but cut for budget.
  fired?: boolean
  matched_keys?: string[]
  dropped_for_budget?: boolean
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
  characters?: CharactersView
  worktrees?: WorktreeView
}

// The content library (Stage). Cards, personas, and scene backgrounds — the nouns
// the immersive stack is made of — surfaced over ctrlproto so any controller
// inspects and picks from the same store (cards.*/personas.*/backgrounds.* verbs;
// the 'characters' surface renders both stores as a workspace pane).

// CardSummary is the at-a-glance library entry + inventory a grid needs.
export interface CardSummary {
  id: string
  name: string
  creator?: string
  character_version?: string
  spec_version?: string
  tags?: string[]
  // avatar_url is the media route for the card's portrait (/media/cards/<id>), or
  // absent if the card was a JSON import with no image.
  avatar_url?: string
  greetings: number
  book_entries?: number
  has_phi?: boolean
}

// CardView is one card in full: its summary plus the normalized card JSON, so an
// editor round-trips fields it doesn't render (unknown extensions).
export interface CardView extends CardSummary {
  raw: unknown
}

// CardLintFinding is one deterministic card-lint result (Go card.Finding): a
// static, model-free check the character sheet renders as a badge and the card
// doctor reads first. severity is 'warn' (a problem) or 'info' (a fact); field
// names the offending card field; detail carries the offending snippet.
export interface CardLintFinding {
  rule: string
  severity: string
  field?: string
  message: string
  detail?: string
}

export interface CardLintResult {
  findings: CardLintFinding[]
}

// DoctorProposal is one LLM card-doctor edit (Go ctrlproto.DoctorProposal):
// replace `field`'s current value (`before`) with `after`, for `rationale`.
// severity is 'warn' | 'info' | 'suggestion'.
export interface DoctorProposal {
  id: string
  field: string
  severity: string
  rationale: string
  before: string
  after: string
}

// DoctorResult is the payload of cards.doctor: the structured proposals plus an
// optional overall note.
export interface DoctorResult {
  proposals: DoctorProposal[]
  note?: string
}

// DoctorDecision is the user's verdict on a prior proposal, fed back on a
// follow-up cards.doctor call so the doctor can revise (the S7.3 negotiation).
export interface DoctorDecision {
  proposal_id: string
  field?: string
  rationale?: string
  accepted: boolean
  reason?: string
}

export interface CardsListResult {
  cards: CardSummary[]
}

// SuggestTurn is one completed round of the "suggest a reply" back-and-forth (Go
// ctrlproto.SuggestTurn): the guidance the user gave and the draft the model
// returned. Carried on every suggest.reply so the daemon stays stateless.
export interface SuggestTurn {
  note?: string
  draft: string
}

// SuggestParams drives suggest.reply: the refinement history so far and the new
// guidance to draft against (empty note = a cold suggestion). Session rides the
// frame's sess.
export interface SuggestParams {
  history?: SuggestTurn[]
  note?: string
}

// SuggestResult is the payload of suggest.reply: the drafted next user message,
// ready to drop into the composer (the user still sends it).
export interface SuggestResult {
  draft: string
}

// CardExport is a card serialized for download: a CCv2 PNG or JSON. bytes is
// base64 on the wire (Go []byte).
export interface CardExport {
  filename: string
  mime_type: string
  bytes: string
}

// PersonaSummary is a roster entry with provenance. origin is
// 'built-in' | 'extension' | 'user'; editable is false for built-in/extension
// (those copy-to-edit into the user library).
export interface PersonaSummary {
  name: string
  ref: string
  namespace?: string
  specialty?: string
  summary?: string
  emoji?: string
  accent_color?: string
  immersive?: boolean
  origin: string
  editable?: boolean
}

// PersonaView adds the fields an editor renders.
export interface PersonaView extends PersonaSummary {
  pronunciation?: string
  recommended_skills?: string[]
  good_for?: string[]
  avoid_for?: string[]
  introduction?: string
  charter?: string
}

export interface PersonasListResult {
  personas: PersonaSummary[]
}

// UserPersonaView is one saved user persona (build.UserPersona): a reusable "who
// I am in the story" identity (name + description), distinct from an agent
// persona. `default` marks the one a new immersive session pre-fills.
export interface UserPersonaView {
  ref?: string
  name: string
  description?: string
  default?: boolean
}

export interface UserPersonasListResult {
  personas: UserPersonaView[]
}

// BackgroundView is one stored backdrop and the media route to fetch it.
export interface BackgroundView {
  id: string
  url: string
}

export interface BackgroundsListResult {
  backgrounds: BackgroundView[]
}

// CharactersView is the 'characters' surface: the workspace's cards and personas
// side by side, reusing the verbs' own summaries.
export interface CharactersView {
  cards: CardSummary[]
  personas: PersonaSummary[]
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

// WireStall / WireEscalation are the payloads of the stuck-loop hatch's live
// events (Go core.WireStall / WireEscalation). A `stall` is a detector nudge
// (rung 1); an `escalation` is a model swap resolving (rung 3), disposition ∈
// switched | declined | stopped | failed. Both are informational — shown
// in-stream, never joining the transcript.
export interface WireStall {
  axis?: string // spin (same call) | churn (same failure)
  tool?: string
  detail?: string
}

export interface WireEscalation {
  reason?: string
  tool?: string
  from_model?: string
  to_provider?: string
  to_model?: string
  auto?: boolean
  disposition?: string
  detail?: string // failure cause, on a failed swap
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
  stall?: WireStall
  escalation?: WireEscalation
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
