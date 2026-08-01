// Typed ctrlproto client. Mirrors packages/agent/ctrlproto's wire shapes.
//
// How much of that mirror is actually checked, precisely — the previous version
// of this header claimed the Go golden-frame tests pinned these interfaces, and
// nothing in this client had ever read a Go file:
//
//   - the VERB VOCABULARY is checked, both directions, against methods.go
//     (verbs.test.ts). A verb added, renamed or removed in Go fails here.
//   - PARAMS are checked for the verbs VerbParams maps, because send() keys off
//     it. Everything else still passes `unknown` — those interfaces are
//     documentation, and can drift. Adding a VerbParams entry is what promotes
//     one from documentation to contract.
//   - RESULT and EVENT shapes are unchecked. Responses are cast, not validated.

export interface WireUsage {
  // input is the UNCACHED remainder of the prompt, never the whole prompt —
  // every decoder normalizes to that. So the prompt is input + cache_read +
  // cache_write, and the same sum is the context gauge.
  input: number
  output: number
  cache_read: number
  cache_write: number
  cost_usd: number
  // What the prompt cache was worth on these tokens, priced per response by
  // whichever model answered it. Signed: negative means cache writes outran the
  // reads they bought. Absent from a server that predates it.
  cache_saved_usd?: number
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
  // routed marks a line the meta-narrator routed to a character on a normal
  // turn (Worlds W3) — model-produced, in that character's voice (actor).
  routed?: boolean
  // A line the user AUTHORED into the scene via directed authorship (Phase 6) —
  // a character's or the narrator's turn they drafted and posted — rather than a
  // model turn. `actor` names the speaking character; empty means the narrator.
  // Render with 🎭 attribution, not as an ordinary assistant bubble.
  directed?: boolean
  actor?: string
  // Files the user attached to this message and that still resolved when it was
  // sent.
  //
  // Inert by design: no path, no id, nothing to click. A staged file is swept
  // on a TTL, so a message reopened later still says what was attached and
  // simply cannot offer it.
  attachments?: WireAttachment[]
  // How many attachments this message named that had already been swept. Shown
  // so a send whose files all lapsed does not render as one that carried none.
  attachments_missing?: number
  // Content[0] is the host's preamble, not the user's words — the block naming
  // the staging paths, which the model needs and a human reads as nine wrapped
  // lines of noise. Drop it and render the two fields above instead; see
  // blockText.
  //
  // Its own flag, not inferred from `attachments`: a message can carry the
  // preamble with nothing left to label, and inferring it meant the prose
  // surfaced in exactly that case.
  preamble?: boolean
  // Files tool calls in this message published FOR the user. Present only on a
  // tool-role message; each names the call it came from. The replay half of the
  // pair whose live half is WireEvent.shared.
  shared?: SharedFile[]
}

// WireAttachment describes one file a user attached: enough to label it, and
// nothing more. Mirrors core.WireAttachment.
export interface WireAttachment {
  name: string
  kind: string // image | audio | video | document
  mime?: string
  size?: number
}

// SharedFile is one file the AGENT published for the user, via share_file.
// Mirrors core.SharedFile.
//
// The outbound counterpart to WireAttachment, and unlike it a HANDLE: `id`
// resolves against the daemon's share store, and GET /shared/<sess>/<id> serves
// the bytes. Hence a card you can view, play, or download, where an inbound
// attachment is an inert label.
//
// call_id is stamped by the agent loop, so one tool-role message carrying
// several calls' results still says which row each card belongs to.
export interface SharedFile {
  id: string
  call_id?: string
  name: string
  kind: string // image | audio | video | document
  mime?: string
  size?: number
  caption?: string
  // RFC3339. When the daemon's sweeper may remove the bytes, so the card can
  // stop offering a download it knows will fail — for a document and a clip as
  // well as an image, which is the point: only an image has an element whose
  // failure could otherwise tell us anything.
  //
  // An upper bound (cap eviction can be earlier), and ABSENT means unknown, not
  // expired — a daemon older than the field sends nothing.
  expires_at?: string
}

// CastRoute is one cast member's pinned provider+model (Phase 7); empty fields
// mean the actor inherits the session/host model.
export interface CastRoute {
  provider?: string
  model?: string
}

// ArchivedSessionInfo is one row in the archive: a session whose transcript has
// been compressed into a subdirectory no listing walks. It carries the same
// descriptive fields a live row does, because an archive of opaque ids is one
// nobody restores from, plus the sizes that only exist once archived.
export interface ArchivedSessionInfo {
  id: string
  title?: string
  provider?: string
  model?: string
  message_count?: number
  total_cost?: number
  preview?: string
  started?: string
  archived_at?: string
  bytes?: number
  original?: number
  experience?: string
  card?: string
  world?: string
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
  // user_gender / user_pronouns are the persona's stated identity (free-form; the
  // UI offers an inclusive dropdown with an "Other" text escape). Immersive only.
  user_gender?: string
  user_pronouns?: string
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
  // cast_models pins actors to a specific provider+model (Phase 7); actor name →
  // route, absent = inherit the session/host model. Play sessions only.
  cast_models?: Record<string, CastRoute>
  // world_lore is the session's World lorebook (Worlds L1) — shared entries every
  // character on stage sees, edited via world.lore.put / world.lore.delete. Like
  // the note it rides the uncached per-turn tail: edits apply next turn, no rebuild.
  world_lore?: WorldLoreEntry[]
  // scene_pin_stale is how many messages have played since the pinned
  // scene-state card was last written (SD6); 0 when current, absent when there
  // is no pin. The pin is the one entry whose frame tells the model to trust it
  // over disagreeing history, and nothing keeps it current on its own — so the
  // drift is surfaced rather than left for the author to discover in the prose.
  scene_pin_stale?: number
  // coordination is the World's meta-narrator mode (W3, set via world.set):
  // '' auto (the router picks who answers), 'off' (the bound character always
  // answers), or 'focus:<roster name>'. Chat Worlds with a roster only.
  coordination?: string
  // world is the saved World this session belongs to (a worlds-library id,
  // W5) — '' while its World is still session-embedded.
  world?: string
  path?: string
  created?: string
  updated?: string
  messages: number
  usage: WireUsage
  current?: boolean
  trusted?: boolean
  context_tokens?: number
  context_window?: number
  // subscription = the session's credential is a subscription (OAuth) login
  // rather than a metered API key — tag the cost display "(sub)". Resolved
  // daemon-side, where the credential lives.
  subscription?: boolean
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
  // world creates the session inside a saved World (W5): its roster, pins,
  // lore, and coordination seed the session's working copy.
  world?: string
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
  // auth is how this model's PROVIDER authenticates: 'oauth' spends a
  // subscription plan, 'apikey' bills per token. Absent for keyless backends
  // (ollama, named endpoints) — that means unknown, so render nothing.
  auth?: 'oauth' | 'apikey'
  // display_name is the model's human label; renamed says the operator chose
  // it in models.json rather than it coming from the catalog. Only swap it in
  // for the id when renamed — catalog names are LONGER than the ids they
  // would replace, so preferring them makes the list harder to scan.
  display_name?: string
  renamed?: boolean
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
  // Derived narrow-grant options (bash only today): the daemon parsed the
  // command and offers "always allow <display>"; a client that takes it
  // echoes the patterns back in Decision.persist_scopes. Never derive from
  // the preview — it is truncated.
  scopes?: GrantScope[]
}

// One derived narrow-grant option: display is shown to the user
// ("git status"), pattern is the RE2 a scoped grant persists.
export interface GrantScope {
  display: string
  pattern: string
}

export interface AskQuestion {
  question: string
  options?: string[]
  allow_custom?: boolean
}

// One mid-turn ask: a set of questions posed together and answered in one
// submit. `questions` is the whole set; the singular fields mirror
// questions[0] so a daemon built before question sets is still readable.
export interface AskRequest {
  ask_id: string
  question: string
  options?: string[]
  allow_custom?: boolean
  questions?: AskQuestion[]
}

// askQuestions normalises the two shapes into the list to render.
export function askQuestions(r: AskRequest): AskQuestion[] {
  if (r.questions?.length) return r.questions
  return [{ question: r.question, options: r.options, allow_custom: r.allow_custom }]
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

// CreditsInfo is a provider's credit balance, mirroring ctrlproto.CreditsInfo.
// `unlimited` and `has_credits` give `balance` its meaning: some providers
// report what is left, some what has been spent, some neither. Nothing renders
// it yet — ContextBreakdown carries no credits field, so an OpenRouter/DeepSeek
// balance still has no home on the web.
export interface CreditsInfo {
  unlimited?: boolean
  has_credits?: boolean
  balance?: number
  used?: number
}

// UsageInfo is the provider's subscription picture for one session — the result
// of usage.snapshot, mirroring ctrlproto.UsageInfo. Unlike
// ContextBreakdown.usage_windows, which is the status picture, `windows` here
// carries EVERY window the provider reports, rate-limit ones included;
// filtering is the client's business. `has_data` false is a normal answer ("no
// agent yet", or "this provider reports no usage"), not an error, and
// `refreshable` marks a provider whose usage comes from an endpoint rather than
// from response headers.
export interface UsageInfo {
  provider?: string
  has_data?: boolean
  windows?: UsageWindow[]
  credits?: CreditsInfo
  captured_at?: string
  refreshable?: boolean
}

// UsageSnapshotParams is the payload of usage.snapshot. refresh=true pulls from
// the provider's usage endpoint and BLOCKS server-side on that fetch, so it is
// for a deliberate act (opening the pane, a new turn's usage), not for idle
// polling; the daemon TTL-caches the result.
export interface UsageSnapshotParams {
  refresh?: boolean
}

export interface UsageSnapshotResult {
  usage: UsageInfo
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
  // cache is the prompt-cache reading. Absent from a server that predates it —
  // which is why the panel keys off presence, not off zeroes.
  cache?: ContextCache
}

// ContextCache is what the provider's prompt cache did for this session.
//
// Counterweight to everything else on the panel: the byte fields are terva
// estimating what it is about to send, these are TOKENS the provider counted
// and billed. A large transcript with a high hit rate costs almost nothing,
// which the byte total on its own would never say.
export interface ContextCache {
  // false means the provider reported no cache activity all session — no prefix
  // cache, or prompts under its cacheable minimum. Distinct from a 0% hit rate,
  // which means a cache exists and is missing.
  supported: boolean
  last_request: WireUsage
  session: WireUsage
  // Per-request tail, oldest first, capped server-side. Empty until this agent
  // has run a request — including on a resumed session, where the totals above
  // carry the history instead.
  recent?: CacheSample[]
}

// CacheSample is one request reduced to what a strip draws.
export interface CacheSample {
  hit_rate: number // cache_read / prompt, in [0,1]
  prompt_tokens: number // input + cache_read + cache_write
  write_tokens?: number
  saved_usd?: number // signed; negative when a write went unread
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
  dir?: string // agent working directory
  // deliverable is the schema-validated structured report for a spawn that
  // carried a deliverable schema; deliverable_error says why the contract was
  // NOT met. Both absent for schema-less spawns and old daemons.
  deliverable?: unknown
  deliverable_error?: string
  // lines is the full in-memory transcript (capped daemon-side at 2000 lines
  // per agent) — what a transcript viewer scrolls. The list pane only needs
  // tail; a carrier pays for lines only when a viewer asks.
  lines?: string[]
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

// The model's own task board (kind=taskboard) — what the agent is tracking via
// the built-in task_* tools. Distinct from TaskList above, which is the swarm of
// background sub-agents (the pane titled "Agents"). Read-only: the model owns
// this list, and editing it from a client would create a state it cannot see it
// lost.
export interface TaskBoardItem {
  id: string
  status: string // pending | active | blocked | done | cancelled
  title: string
  active_form?: string
  evidence?: string
  note?: string
}

export interface TaskBoardView {
  tasks: TaskBoardItem[]
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
  // The per-scope toggle detail behind the enabled rollup, so a client can
  // offer the same global (manifest) vs project (config) toggles the TUI does —
  // status alone can't say WHICH scope disabled an extension.
  global_enabled?: boolean
  project_disabled?: boolean
  user_config_disabled?: boolean
  has_config?: boolean
  // has_log: a log file exists — offer the log viewer key.
  has_log?: boolean
  // config is the declared schema with the user's saved values folded in — the
  // whole form, ready to render. It travels because the browser has no
  // filesystem to read a manifest from, so `has_config` used to advertise a
  // capability with nothing behind it.
  config?: ExtensionConfigField[]
}

// One row of an extension's config form. A SECRET field never carries `saved`,
// only `has_saved` — the stored value stays on the host, and submitting a blank
// secret means "leave it alone", never "clear it". Do not add a code path that
// expects a secret's value to be here; there is deliberately none.
export interface ExtensionConfigField {
  key: string
  label?: string
  type?: string // string | bool | int | select | secret
  description?: string
  required?: boolean
  secret?: boolean
  options?: string[]
  saved?: string
  default?: string
  has_saved?: boolean
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

// Durable-memory pane (kind=memory): the agent's curated facts in both scopes,
// with each scope's budget so a reader can see how close it is to refusing the
// next write rather than discovering it when the model reports a refusal.
export interface MemoryScope {
  label: string
  // The ACTIVE tier: facts in the model's cached system prefix on every request.
  entries?: string[]
  // The SERIALIZED size the cap is measured against — not the sum of entry
  // lengths, which understates it by the file header and the bullets. Same
  // number the store refuses on, so the pane and the refusal cannot disagree.
  bytes: number
  max_bytes: number
  max_count: number

  // The ARCHIVED tier: out of that prefix entirely, injected into the per-turn
  // tail only when the conversation matches an entry's triggers. Separate from
  // entries because the budgets are separate and the sizes differ by two orders
  // of magnitude — folding archived bytes into the fraction above would say
  // archiving brings a scope closer to refusing the next write, which is the
  // opposite of what it does.
  archived?: MemoryArchivedEntry[]
  archived_bytes?: number
  archived_max_bytes?: number
  // Archive files that are present but unreadable. Carried because such an entry
  // is inert — it occupies the budget, never fires, and has no other symptom at
  // all — so a pane that does not show it is the only place a user could have
  // found out.
  problems?: string[]
}

// One archived memory. Keys ride along because the archive's failure mode is
// silent: an entry keyed on the answer's vocabulary rather than the question's
// simply never fires and produces nothing to notice, and seeing the triggers
// beside the entry is the only cheap way anyone catches it.
export interface MemoryArchivedEntry {
  // The scope-qualified id ("project:the-id") — the name every surface shows and
  // the one an action must send back.
  ref: string
  title?: string
  keys?: string[]
  secondary_keys?: string[]
  bytes?: number
  text?: string
  // Last turn's activation trace, as LoreEntry carries it and for the same
  // reason. dropped_for_budget separates "fired and was cut" from "never fired",
  // which look identical from outside and need opposite fixes.
  fired?: boolean
  matched_keys?: string[]
  dropped_for_budget?: boolean
}

export interface MemoryView {
  user: MemoryScope
  project: MemoryScope
  // False when the session has no resolvable project: project memory is then
  // in-memory only, and the pane says so rather than showing a list that will
  // never persist.
  project_bound?: boolean
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
  // The per-scope toggle detail behind the enabled/status rollup (status
  // collapses user- and project-disabled into one "disabled"), plus the live
  // connection ground truth a masked status can't carry.
  user_disabled?: boolean
  project_disabled?: boolean
  connected?: boolean
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
  // Workspace Trust for this directory — whether project-supplied extensions,
  // skills, and context files load. The panel surfaces the toggle in the
  // workspace drawer and (like every client) in the settings pane; these ride
  // the permissions view so an inspector can state the whole posture in one
  // place. cwd is empty against a daemon predating the field.
  cwd?: string
  trusted?: boolean
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
  task_board?: TaskBoardView
  settings?: SettingsView
  panel?: PanelView
  widgets?: Widget[]
  commands?: CommandsView
  extensions?: ExtensionsView
  permissions?: PermissionsView
  lore?: LoreView
  memory?: MemoryView
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
  // added is when the card entered the library (RFC 3339, its directory mtime) —
  // the "recently added" sort key. favorite is whether it is favorited: the
  // library highlights it and pins it to the top. Toggled by cards.favorite.
  added?: string
  favorite?: boolean
}

// CardView is one card in full: its summary plus the normalized card JSON, so an
// editor round-trips fields it doesn't render (unknown extensions).
export interface CardView extends CardSummary {
  raw: unknown
  // Non-fatal notes from an import (Go CardView.Warnings) — a portrait that was
  // downscaled or dropped. The card imported fine; these say what was done to it.
  warnings?: string[]
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
  // remove marks a proposal that CLEARS the field: `after` is empty and that is
  // the point, so a surface must say "cleared" rather than render a blank box
  // that reads as a broken proposal.
  remove?: boolean
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

// SessionProposal is one typed session-doctor proposal (Go
// ctrlproto.SessionProposal). kind decides the payload and the apply verb:
// lore_entry/open_thread → world.lore.put, cast_promotion → cards.import +
// cast.add (via a prefilled editor, never a silent import), scene_state (SD4)
// → world.lore.put on the reserved pin name (name/content arrive server-
// forced: canonical name, no keys/audience — the accept path needs no
// special-casing beyond the label).
export interface SessionProposal {
  id: string
  // lore_retire (SD6) carries name only and applies through world.lore.delete —
  // the one kind whose acceptance REMOVES something, so its accept button says
  // so rather than reading as another "Accept".
  kind: 'lore_entry' | 'open_thread' | 'cast_promotion' | 'scene_state' | 'scene_break' | 'lore_retire'
  rationale: string
  // lore_entry / open_thread; scene_state uses name+content only
  name?: string
  content?: string
  keys?: string[]
  audience?: string[]
  // cast_promotion
  character?: string
  description?: string
  personality?: string
  first_mes?: string
}

// SessionDoctorParams drives sessions.doctor (session rides the frame); the
// decisions loop and per-generation model override match the card doctor.
export interface SessionDoctorParams {
  decisions?: DoctorDecision[]
  provider?: string
  model?: string
  // The narrowed asks (SD2/SD3), mutually exclusive: focus marks one message
  // index to draft lore from; promote names a walk-on to seed a card for.
  focus?: number
  promote?: string
}

export interface SessionDoctorResult {
  proposals: SessionProposal[]
  note?: string
}

// NextSceneParams drives sessions.next_scene (SD5), a two-phase verb: without
// commit it drafts (one booked model call, creates nothing); with commit it
// creates the scene from the fields as the author edited them.
export interface NextSceneParams {
  commit?: boolean
  title?: string
  summary?: string
  opening?: string
  provider?: string
  model?: string
  // The World the new scene joins (#297/#298). Go has carried this since the
  // scene-break work landed and NextScene.tsx has always sent it; this mirror
  // simply never grew the field, and `params: unknown` meant nothing noticed.
  world?: string
}

export interface NextSceneResult {
  title?: string
  summary?: string
  opening?: string
  note?: string
  // The created scene; absent on a propose.
  session?: SessionInfo
  // The saved World this story already belongs to, absent when it belongs to
  // none — which is what tells the sheet whether to OFFER a promotion.
  world_id?: string
  // That World's name, or (with no world_id) a suggested name to prefill the
  // offer with.
  world_name?: string
}

// realize (creator C3 — docs/plans/creator-realize.md): turn a cartographer
// conversation into a playable world. Two-phase, like next_scene — propose
// extracts the structure (creates nothing), commit seeds a play session from the
// author-edited proposal (spends nothing).
export interface RealizeParams {
  commit?: boolean
  // The author-edited structure, sent on a commit.
  proposal?: RealizeProposal
  // Per-generation model override for the propose call; the default is the
  // session's own. Ignored on a commit.
  provider?: string
  model?: string
}

export interface RealizeCharacter {
  name: string
  role?: string
  description?: string
  personality?: string
  first_mes?: string
  // The protagonist's play constraint (e.g. a language/ability quirk).
  notes?: string
}

export interface RealizeLore {
  name: string
  keys?: string[]
  always_on?: boolean
  content: string
}

export interface RealizeProposal {
  world: { name: string; description?: string }
  // Who the AUTHOR plays; becomes the bound user persona, not a roster card.
  protagonist: RealizeCharacter
  // The NPCs the model voices; each becomes an imported card + a cast member.
  roster?: RealizeCharacter[]
  lore?: RealizeLore[]
  cold_open?: string
  // The roster name who delivers the cold open, or empty for narration.
  cold_open_actor?: string
  coordination?: string
  // The cartographer's attribution ledger, shown so the author sees what to
  // overrule. Propose only.
  given_by_author?: string[]
  invented_by_you?: string[]
  // An optional remark — e.g. that the conversation has not converged yet.
  note?: string
}

export interface RealizeResult {
  // The extracted structure; absent on a commit.
  proposal?: RealizeProposal
  // The created play session; absent on a propose.
  session?: SessionInfo
}

export interface CardsListResult {
  cards: CardSummary[]
}

// Group is a membership bucket the library browses by (Go ctrlproto.GroupView —
// named GroupView there because Group is already that package's method-group
// type), shared by the card-group and session-group namespaces. members are the LIVE
// ids only (a card/session deleted since it was filed is filtered out), so a
// count is just members.length. Distinct from a card's embedded CCv2 tags.
export interface Group {
  id: string
  name: string
  color?: string
  members: string[]
  // system marks a client-SYNTHESIZED group derived from session metadata (the
  // `stage` bucket, a per-card/World origin) rather than a stored, user-curated
  // one. Never sent to the daemon; it only gates the UI — a system chip filters
  // but cannot be renamed, recoloured, deleted, or hand-filed. See platform/groups.ts.
  system?: boolean
}

export interface CardGroupsResult {
  groups: Group[]
}

// CardGroupSaveParams creates (no id) or renames/recolours (id) a card group;
// members are untouched here and ride cardgroups.set_members.
export interface CardGroupSaveParams {
  id?: string
  name: string
  color?: string
}

export interface CardGroupDeleteParams {
  id: string
}

// CardGroupSetMembersParams replaces a group's members with the given card refs
// (the sole membership mutation — send the group's whole new list).
export interface CardGroupSetMembersParams {
  id: string
  members: string[]
}

// Session groups: the session-id twin of the card-group verbs, over the same
// Group wire type. Shown on both the Stage library and the control panel.
export interface SessionGroupsResult {
  groups: Group[]
}

export interface SessionGroupSaveParams {
  id?: string
  name: string
  color?: string
}

export interface SessionGroupDeleteParams {
  id: string
}

export interface SessionGroupSetMembersParams {
  id: string
  members: string[]
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
  // A per-generation model override (Phase 7); empty = the session's own model.
  provider?: string
  model?: string
  // Whose line to draft (Phase 6 directed authorship): '' | 'user' drafts the
  // player's reply (fills the composer); 'actor' drafts target_name's line in
  // their voice; 'narrator' drafts a narrative beat. target_voice is a short
  // description of a walk-on actor. An actor/narrator draft is posted with
  // post.line, not typed as the player.
  target?: 'user' | 'actor' | 'narrator'
  target_name?: string
  target_voice?: string
  // Voice a LIBRARY CARD instead of a typed walk-on (Worlds W1): a card ref the
  // daemon resolves to the character's full voice. Actor target only; supersedes
  // target_voice.
  target_card?: string
}

// SuggestResult is the payload of suggest.reply: the drafted next user message,
// ready to drop into the composer (the user still sends it).
export interface SuggestResult {
  draft: string
}

// PostLineParams drives post.line (Phase 6 directed authorship): commit an
// approved character/narrator line INTO the transcript as an attributed
// assistant message. actor names the speaking character; empty posts a narrator
// beat. Session rides the frame's sess.
export interface PostLineParams {
  actor?: string
  text: string
}

// DirectTurnParams drives direct.turn (Phase 6b): run one turn steered by an
// out-of-character direction. text is what should happen next; the daemon wraps
// it in the [Direction] marker and the model writes the beat. Session rides the
// frame's sess.
export interface DirectTurnParams {
  text: string
}

// WorldLoreEntry is one World lore entry (Worlds L1): session-scoped keyed
// context. keys trigger injection when one appears in recent messages;
// constant injects every turn (keys ignored). audience (L2) names the
// characters who know the entry — empty = everyone on stage; a named
// character's generation only sees entries they are cleared for.
export interface WorldLoreEntry {
  name: string
  keys?: string[]
  constant?: boolean
  content: string
  audience?: string[]
  // model marks an entry the play director's world_note tool authored (📝
  // badge); learned is the L3 ledger: character → when they learned it via
  // world_reveal. Both read-only on the wire.
  model?: boolean
  learned?: Record<string, string>
}

// WorldLorePutParams drives world.lore.put: add or update one entry (upsert by
// entry.name); replace names the superseded entry on a rename. Session rides
// the frame's sess.
export interface WorldLorePutParams {
  entry: WorldLoreEntry
  replace?: string
}

// WorldLoreDeleteParams drives world.lore.delete. Session rides the frame's sess.
export interface WorldLoreDeleteParams {
  name: string
}

// WorldSetParams drives world.set: the meta-narrator coordination mode — ''
// auto, 'off', or 'focus:<roster name>'. Session rides the frame's sess.
export interface WorldSetParams {
  coordination: string
}

// WorldView is one saved World (W5): the roster, its lore, and how many
// sessions belong to it.
export interface WorldView {
  id: string
  name: string
  description?: string
  characters?: Record<string, string>
  // character_models is the per-character default model (name → provider+model)
  // a new session in this World seeds its cast from; empty = that actor inherits
  // the session model. Edited via worlds.set_character_model.
  character_models?: Record<string, CastRoute>
  lore?: WorldLoreEntry[]
  sessions?: number
  created?: string
  updated?: string
  // cover_url is the media route for the World's cover image (W5b), same
  // contract as CardSummary.avatar_url.
  cover_url?: string
}

// WorldsListResult is the payload of worlds.list.
export interface WorldsListResult {
  worlds: WorldView[]
}

// WorldSaveParams drives worlds.save: promote the session's World (name
// required on first save) or update its saved World in place (name renames).
// Session rides the frame's sess.
export interface WorldSaveParams {
  name?: string
  description?: string
}

// WorldUpdateParams drives worlds.update (W5b): sessionless metadata edits on
// a saved World. name '' keeps the current name; description is applied
// VERBATIM (send the current text back when leaving it unchanged); cover sets
// a new cover image (base64), remove_cover deletes it — never both.
export interface WorldUpdateParams {
  id: string
  name?: string
  description: string
  cover?: string
  remove_cover?: boolean
}

// WorldSetCharacterModelParams pins (or clears) one roster character's
// World-scoped default model. Empty provider AND model clears the pin so the
// character inherits the session model again.
export interface WorldSetCharacterModelParams {
  id: string
  character: string
  provider?: string
  model?: string
}

// WorldExport is a World bundle serialized for download (worlds.export) — the
// same download shape as CardExport. bytes is base64 on the wire.
export interface WorldExport {
  filename: string
  mime_type: string
  bytes: string
}

// WorldImportParams drives worlds.import: a World bundle upload (base64).
export interface WorldImportParams {
  // bytes (base64) or path (a file on the daemon's disk) — one must be present.
  bytes?: string
  path?: string
}

// CardExport is a card serialized for download: a CCv2 PNG or JSON. bytes is
// base64 on the wire (Go []byte).
export interface CardExport {
  filename: string
  mime_type: string
  bytes: string
}

// SessionExport is a session serialized for download — markdown (the readable
// story) or the raw .tervasession round-trip. Same triple as CardExport, so the
// one download helper serves all three.
export interface SessionExport {
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
  // The shelf a roster files this persona under — its declared `group`, else the
  // namespace it came from. Purely organisational: it is NOT part of `ref`, so
  // never key anything on it. Absent from a daemon that predates persona groups.
  group?: string
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
  extends?: string
  // How many sessions — across every project — were created with this persona
  // and replay it whenever they open. Reported by personas.get only; the list
  // omits it because deriving it reads every project's transcripts.
  //
  // Deleting the persona does not break those sessions (the daemon falls back to
  // the default persona and says so on load), but they lose the voice they were
  // written in, so the delete confirm says how many.
  sessions_using?: number
}

// PersonaWriteParams is the editable form — what personas.create/edit accept.
// Deliberately NOT PersonaView: ref/namespace/origin/editable are server-derived
// and sending them back is noise the binder ignores.
//
// ⚠️ A write REPLACES the whole file. Every field must be re-sent on an edit or
// it is ERASED, not preserved — there is no partial update.
export interface PersonaWriteParams {
  name: string
  group?: string
  pronunciation?: string
  specialty?: string
  summary?: string
  emoji?: string
  accent_color?: string
  recommended_skills?: string[]
  good_for?: string[]
  avoid_for?: string[]
  immersive?: boolean
  introduction?: string
  charter?: string
  // A persona whose charter this one builds on. Subject to the same warning as
  // every other field above: an edit that does not send it back DELETES it.
  extends?: string
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
  gender?: string
  pronouns?: string
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

// ReplayState is a replay session's transport (play/pause/seek/speed), carried
// on 'replay_state' events so every client's scrubber converges. The web has no
// replay surface today — mirrored because it is on the wire, not because
// anything here reads it yet.
export interface ReplayState {
  playing: boolean
  position: number // index of the next frame to emit
  total: number // frames in the scene
  speed: number // delay multiplier (2 = twice as fast)
  mode?: string // effective | raw
}

export interface WireEvent {
  type: string
  step?: number // turn_start
  delta?: string
  id?: string
  name?: string
  args?: unknown
  text?: string
  is_error?: boolean
  content?: WireBlock[]
  // tool_result line-change counts (the status bar's Δ segment)
  lines_added?: number
  lines_removed?: number
  // tool_result: files this call published for the user. The live half of the
  // pair whose replay half is WireMessage.shared.
  shared?: SharedFile[]
  usage?: WireUsage
  cumulative?: WireUsage
  message?: WireMessage
  stop?: string
  error?: string
  // user_message_rejected: the refused prompt in full (text carries the
  // guard's reason). Quoted truncated today; whole so a restore-to-composer
  // client needs no wire change.
  rejected?: string
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
  replay?: ReplayState
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
  // The largest single file the carrier accepts in one frame (Go
  // Hello.MaxUploadBytes), or absent when unbounded. Check a file against this
  // BEFORE sending it: an oversized frame does not come back as an error, it
  // closes the socket, and the in-flight request then fails with a generic
  // dead-socket message that explains nothing.
  max_upload_bytes?: number
  // The largest file the ATTACHMENT route will stage (Go
  // Hello.MaxAttachmentBytes), or absent when this carrier has no such route.
  // Separate from max_upload_bytes, and much larger: an attachment never enters
  // a wire frame — it is POSTed to /upload and only its id rides the prompt — so
  // checking one against the frame bound would refuse files the daemon accepts.
  max_attachment_bytes?: number
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
  // Auto-allow this tool for the rest of the SESSION.
  remember_tool?: boolean
  // Auto-allow every tool for the rest of the session.
  remember_all?: boolean
  // Save a permanent allow rule for this tool to the user's config — outlives
  // the session. Implies remember_tool for the current one.
  persist_tool?: boolean
  // Narrow a persist_tool grant to these args patterns (echo the pattern
  // fields of the request's scopes): one saved rule per pattern instead of
  // a blanket tool rule, and no session-wide grant rides along.
  persist_scopes?: string[]
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

// --- the verb vocabulary -----------------------------------------------------
//
// Verb is every method ctrlproto serves. It exists so client.send() can take a
// verb literal instead of a bare string: before this, `send(method: string,
// params: unknown)` type-checked nothing, so a typo'd verb and a params object
// that disagreed with Go both compiled and failed at runtime with an error
// nobody was watching for.
//
// This list is a mirror of packages/agent/ctrlproto/methods.go, and mirrors
// drift — so verbs.test.ts parses that file and fails if the two disagree in
// either direction. Do not edit this list by hand without running the tests.
export type Verb =
  | 'answer'
  | 'approve'
  | 'auth.endpoint.remove'
  | 'auth.login.cancel'
  | 'auth.login.start'
  | 'auth.login.submit'
  | 'auth.logout'
  | 'auth.providers'
  | 'backgrounds.bind'
  | 'backgrounds.delete'
  | 'backgrounds.generate'
  | 'backgrounds.import'
  | 'backgrounds.list'
  | 'cancel'
  | 'cardgroups.delete'
  | 'cardgroups.list'
  | 'cardgroups.save'
  | 'cardgroups.set_members'
  | 'cardmodel.set'
  | 'cards.delete'
  | 'cards.doctor'
  | 'cards.edit'
  | 'cards.export'
  | 'cards.favorite'
  | 'cards.get'
  | 'cards.history'
  | 'cards.import'
  | 'cards.lint'
  | 'cards.list'
  | 'cards.restore'
  | 'cards.revision'
  | 'cast.add'
  | 'cast.remove'
  | 'cast.speak'
  | 'clear'
  | 'compact'
  | 'context.get'
  | 'context.node'
  | 'control.restart'
  | 'control.trust'
  | 'control.untrust'
  | 'conversation.history'
  | 'conversation.reveal'
  | 'direct.turn'
  | 'files.list'
  | 'i18n.catalog'
  | 'message.delete'
  | 'message.edit'
  | 'models.default_for'
  | 'models.favorite'
  | 'models.list'
  | 'models.params'
  | 'models.params.reset'
  | 'models.params.set'
  | 'models.set_default'
  | 'models.switch'
  | 'note.set'
  | 'personas.create'
  | 'personas.delete'
  | 'personas.edit'
  | 'personas.get'
  | 'personas.list'
  | 'post.line'
  | 'prompt'
  | 'queue'
  | 'queue.set'
  | 'replay.control'
  | 'replay.state'
  | 'sessiongroups.delete'
  | 'sessiongroups.list'
  | 'sessiongroups.save'
  | 'sessiongroups.set_members'
  | 'sessions.archive'
  | 'sessions.archived'
  | 'sessions.create'
  | 'sessions.delete'
  | 'sessions.discard_draft'
  | 'sessions.doctor'
  | 'sessions.export'
  | 'sessions.fork'
  | 'sessions.generate_title'
  | 'sessions.list'
  | 'sessions.next_scene'
  | 'sessions.realize'
  | 'sessions.rename'
  | 'sessions.restore'
  | 'sessions.resume'
  | 'sidechat.ask'
  | 'sidechat.close'
  | 'sidechat.open'
  | 'subscribe'
  | 'suggest.reply'
  | 'surface.action'
  | 'surface.get'
  | 'surfaces.list'
  | 'turn.advance'
  | 'turn.continue'
  | 'turn.retry'
  | 'turn.swipe'
  | 'unsubscribe'
  | 'usage.get'
  | 'usage.resets.consume'
  | 'usage.resets.list'
  | 'usage.snapshot'
  | 'user.bind'
  | 'userpersonas.delete'
  | 'userpersonas.list'
  | 'userpersonas.save'
  | 'userpersonas.set_default'
  | 'variants.drop'
  | 'variants.prune'
  | 'world.lore.delete'
  | 'world.lore.put'
  | 'world.set'
  | 'worlds.delete'
  | 'worlds.export'
  | 'worlds.import'
  | 'worlds.list'
  | 'worlds.save'
  | 'worlds.set_character_model'
  | 'worlds.update'
  | 'workflows.get'
  | 'workflows.list'

// --- the workflow dashboard (ctrlproto/workflows.go) ---

// WorkflowRunInfo is one row of workflows.list.
//
// `status` distinguishes a live run from a dead one: a running process restamps
// a heartbeat on its record, so `running` and `crashed` are earned rather than
// guessed. `incomplete` remains the honest "cannot tell" — a run recorded before
// heartbeats, or one that died before its first tick. `completed` of `agents`
// is the number an operator actually reads, because it says how much finished
// work is sitting on disk waiting to be replayed rather than paid for a second
// time; `running` alongside it separates a run to wait for from one to resume.
export interface WorkflowRunInfo {
  id: string
  name?: string
  status: 'incomplete' | 'running' | 'crashed' | 'done' | 'failed'
  started?: string
  ended?: string
  completed: number
  agents?: number
  cached?: number
  cost_usd?: number
  cwd?: string
  script_at?: string
  error?: string
  running?: number
  resumable?: boolean
}

export interface WorkflowRunsResult {
  runs: WorkflowRunInfo[]
}

export interface WorkflowGetParams {
  id: string
}

// WorkflowRunResult is one completed agent call. `label` is the script's name
// for the slice — the generated agent id is unreadable for a fan-out that shares
// a prompt preamble, which is what made these reports unfindable in the first
// place. `bytes` ships alongside the body so a large report can be collapsed
// behind its size instead of pasted into the page.
export interface WorkflowRunResult {
  label?: string
  agent_id?: string
  bytes?: number
  result?: unknown
}

// WorkflowRunView is one run, opened. `script` is the source AS IT RAN, recorded
// at launch — not read back from `script_at`, which may have moved, been edited,
// or belong to another machine.
export interface WorkflowRunView {
  run: WorkflowRunInfo
  script?: string
  args?: unknown
  results?: WorkflowRunResult[]
  in_flight?: WorkflowAgent[]
}

// WorkflowAgent is one agent with no result yet — a slice the run is still
// working on, or was working on when it stopped. Thin on purpose: a workflow
// agent journals once, at the end, so there is no per-agent progress to report
// and knowing WHICH slices are outstanding is the whole signal.
export interface WorkflowAgent {
  label?: string
  agent_id?: string
}

// VerbParams pins the params shape for the verbs whose Go struct this file
// mirrors. It is deliberately PARTIAL: a verb absent here still type-checks its
// name, and its params fall back to `unknown` exactly as before. Adding an entry
// tightens one verb without touching any other call site — so this map is meant
// to grow as the mirrored interfaces do, and every entry added is one more place
// a Go-side field rename stops compiling here instead of failing silently.
export interface VerbParams {
  'sessions.doctor': SessionDoctorParams
  'sessions.next_scene': NextSceneParams
  'sessions.realize': RealizeParams
  'suggest.reply': SuggestParams
  'post.line': PostLineParams
  'direct.turn': DirectTurnParams
  'world.lore.put': WorldLorePutParams
  'world.lore.delete': WorldLoreDeleteParams
  'world.set': WorldSetParams
  'worlds.save': WorldSaveParams
  'worlds.update': WorldUpdateParams
  'worlds.set_character_model': WorldSetCharacterModelParams
  'worlds.import': WorldImportParams
  'cardgroups.save': CardGroupSaveParams
  'cardgroups.delete': CardGroupDeleteParams
  'cardgroups.set_members': CardGroupSetMembersParams
  'cardmodel.set': CardModelSetParams
  'models.default_for': DefaultForParams
  'sessiongroups.save': SessionGroupSaveParams
  'sessiongroups.delete': SessionGroupDeleteParams
  'sessiongroups.set_members': SessionGroupSetMembersParams
  'personas.create': PersonaWriteParams
  'personas.edit': PersonaWriteParams
  'cards.favorite': CardFavoriteParams
  'cards.history': CardHistoryParams
  'cards.restore': CardRestoreParams
  'cards.revision': CardRevisionParams
  'usage.snapshot': UsageSnapshotParams
  'workflows.get': WorkflowGetParams
}

// Card revision history. Every write to a card goes through cards.edit — the
// editor, the doctor's apply, enrich — and the server snapshots the OUTGOING
// card there, so a pass that rewrote fields the user never typed is
// recoverable. A card keeps its original plus its most recent changes.

export interface CardHistoryParams {
  id: string
}

// CardRevision is one retained earlier revision. ref is opaque — the only thing
// to do with it is hand it back to cards.restore. name is the card's name AT
// that revision, so a list still reads correctly after a rename. saved is
// RFC 3339; bytes is the revision's size.
export interface CardRevision {
  ref: string
  saved: string
  bytes: number
  name?: string
  // The CCv2 field names this revision differs from the card in — what
  // restoring it would change. Computed against the SAVED card, so an editor
  // holding unsaved edits is being compared to the store, not to its own draft.
  // Absent/empty means the revision matches the card as it stands.
  fields?: string[]
  // Whether restoring this revision would also change the card's picture. The
  // portrait lives outside the card document, so it is reported separately —
  // without it a revision that replaced ONLY the image would read as identical
  // to the card it plainly differs from.
  portrait?: boolean
}

// CardRevisionParams reads one revision in full. Split from cards.history so a
// list stays small: a card with a large lorebook would otherwise ship ten
// copies of it just to render the list.
export interface CardRevisionParams {
  id: string
  ref: string
}

// CardRevisionView is one revision's stored document. raw has the same shape
// cards.get returns, so a client compares two documents of one shape.
export interface CardRevisionView {
  ref: string
  saved: string
  name?: string
  fields?: string[]
  portrait?: boolean
  raw: unknown
}

// CardHistoryResult is the payload of cards.history, newest first. An empty
// list means the card has never been edited — a normal state, not an error.
export interface CardHistoryResult {
  revisions: CardRevision[]
}

// CardRestoreParams puts a retained revision back. The restore is an ordinary
// edit under the skin, so the state it replaces is itself recorded: restoring
// the wrong revision is undoable.
export interface CardRestoreParams {
  id: string
  ref: string
}

// CardFavoriteParams toggles a card's favorite flag (highlight + pin to top).
export interface CardFavoriteParams {
  id: string
  favorite: boolean
}

// CardModelSetParams sets or clears a card's default model. Empty provider AND
// model clears it (drops the sidecar entry → back to the workspace default).
export interface CardModelSetParams {
  card: string
  provider?: string
  model?: string
}

// DefaultForParams asks the daemon to resolve the effective default model for a
// context, walking Card → World → Workspace. Both fields optional; world has no
// rung yet (reserved).
export interface DefaultForParams {
  card?: string
  world?: string
}

// DefaultForResult is the resolved effective default. `source` names the rung
// that supplied it — 'card' means this card carries its own default, so a picker
// shows it as an active pick rather than an inherited one.
export interface DefaultForResult {
  provider: string
  model: string
  source: 'card' | 'world' | 'workspace'
}

// ParamsFor is VerbParams[V] where the verb is mapped, and `unknown` otherwise.
export type ParamsFor<V extends Verb> = V extends keyof VerbParams ? VerbParams[V] : unknown
