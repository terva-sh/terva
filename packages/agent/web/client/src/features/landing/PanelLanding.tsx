import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { Group, ModelInfo, PersonaSummary, PersonaView, SessionInfo } from '../../platform/ctrlproto/types'
import type { GroupFilter } from '../../platform/groups'
import { shelvePersonas } from '../../platform/personagroups'
import { Placeholder } from '../../ui/Loading'
import { stageHref } from '../../ui/navlinks'
import { SessionsBoard } from '../board/SessionsBoard'

// The session-grid props are threaded straight through to SessionsBoard; app.tsx
// still owns the session list, subscriptions, and verbs (web layering rules).
type BoardPassthrough = {
  sessions: SessionInfo[]
  // Passed straight through: whether sessions.list has answered, so the grid
  // shows a placeholder rather than claiming an empty workspace during connect.
  loaded?: boolean
  current: string
  liveBusy?: Record<string, boolean>
  onSelect: (id: string) => void
  onRename: (s: SessionInfo) => void
  onDelete: (s: SessionInfo) => void
  // Archiving: the session leaves the list without leaving the disk. Optional,
  // so a surface served by a daemon that does not offer it renders no control.
  onArchive?: (s: SessionInfo) => void
  groups?: Group[]
  filterGroups?: Group[]
  filter?: GroupFilter
  onCycleGroup?: (id: string) => void
  onToggleGroup?: (s: SessionInfo, groupId: string) => void
  onCreateGroup?: (s: SessionInfo) => void
}

type NewSessionOpts = { persona?: string; model?: string; provider?: string }

// newSessionOpts builds the CreateOpts subset from the sheet's choices: a chosen
// persona and/or a specific model (provider-qualified). Both are optional, so an
// untouched sheet yields {} — a bare session on the workspace default.
export function newSessionOpts(persona: string, model?: ModelInfo): NewSessionOpts {
  const opts: NewSessionOpts = {}
  if (persona) opts.persona = persona
  if (model) {
    opts.provider = model.provider
    opts.model = model.id
  }
  return opts
}

// PanelLanding is the panel's session-focused landing — the state a fresh tab
// boots into (no session adopted). It mirrors Stage's Library shape (a "start
// something new" hero over the things you already have) but keeps sessions at
// the centre, and surfaces personas so the harness opens in-character. Panel-
// native (styled in styles.css via the shared --ui-* tokens) rather than
// reusing Stage's components, whose CSS is Stage-scoped.
export function PanelLanding(
  props: BoardPassthrough & {
    client: ClientLike | null
    status: string
    stageEnabled: boolean
    models: ModelInfo[]
    onNewSession: (opts?: NewSessionOpts) => void
  },
) {
  const { client, status, stageEnabled, models, onNewSession, ...board } = props
  const [personas, setPersonas] = useState<PersonaSummary[]>([])
  // Whether personas.list has ANSWERED — an empty roster means "this daemon
  // serves none", and until the effect below has run it means nothing at all.
  // Conflating the two is what put "No personas available." on screen during
  // every connect, under a roster the client had provably not asked for yet.
  const [personasLoaded, setPersonasLoaded] = useState(false)
  const [detail, setDetail] = useState<PersonaSummary | null>(null)
  const [newOpen, setNewOpen] = useState(false)
  const shelves = shelvePersonas(personas)

  // ⚠️ Gated on the socket being OPEN, not merely on having a client.
  //
  // app.tsx hands this the client out of a ref, which is populated in its mount
  // effect — so the first render this sees a client at all is typically one
  // where the socket is still CONNECTING. Client.send rejects "not connected"
  // there, the catch below turns that into an empty roster, and since the only
  // other dependency is the client identity (which never changes again) nothing
  // ever retried: the Personas section sat on "No personas available." for the
  // life of the tab. Whether you saw a roster came down to which side of that
  // race your machine landed on. Stage's library gates on `ready` for the same
  // reason. Re-running on status also refills the roster after a reconnect.
  useEffect(() => {
    if (!client || status !== 'open') return
    // personas.list is optional (served only by a PersonasController); a daemon
    // without it just leaves the roster empty rather than erroring.
    client
      .send<{ personas: PersonaSummary[] }>('personas.list', null, '')
      .then((r) => {
        setPersonas(r.personas ?? [])
        setPersonasLoaded(true)
      })
      // A refusal is still an answer — this daemon serves no roster — so the
      // empty state is the honest thing to show. Only the state BEFORE the ask
      // is the one that must not claim anything.
      .catch(() => {
        setPersonas([])
        setPersonasLoaded(true)
      })
  }, [client, status])

  return (
    <div class="landing">
      <header class="landing-top">
        {/* i18n-exempt — the terva wordmark, a product name */}
        <span class="landing-brand">terva</span>
        <span class="landing-top__spacer" />
        {stageEnabled && (
          <a class="landing-top__link" href={stageHref('')} title={t('Open Stage')}>
            🎭 {t('Stage')}
          </a>
        )}
        {/* Same indicator as the app's top bar, and it carried the same flaw:
            colour with no text and no label. The visible wording is the
            connection banner app.tsx renders above this screen. */}
        <span class={`dot ${status}`} role="img" title={status} aria-label={t('connection: %s', status)} />
      </header>

      <div class="landing-body">
        <button class="landing-hero" onClick={() => setNewOpen(true)}>
          <span class="landing-hero__icon" aria-hidden="true">
            ＋
          </span>
          <span class="landing-hero__text">
            <span class="landing-hero__title">{t('Start a new session')}</span>
            <span class="landing-hero__sub">{t('Choose a persona and model — or just start.')}</span>
          </span>
        </button>

        <SessionsBoard {...board} onNew={() => setNewOpen(true)} />

        <section class="landing-personas">
          <div class="landing-section-head">
            <h2>{t('Personas')}</h2>
          </div>
          {personas.length === 0 && !personasLoaded ? (
            <Placeholder label={t('Loading personas…')} rows={2} />
          ) : personas.length === 0 ? (
            <p class="landing-empty">{t('No personas available.')}</p>
          ) : (
            shelves.map((shelf) => (
              <div key={shelf.name} class="landing-shelf">
                {/* A lone shelf is the whole roster (one group, or a daemon that
                    serves none) and goes unlabelled, so it reads as the plain
                    list it has always been. Among several, the nameless one is
                    the leftovers and says so. */}
                {shelves.length > 1 && <h3 class="landing-shelf__name">{shelf.name || t('Other')}</h3>}
                <ul class="landing-persona-list">
                  {shelf.personas.map((p) => (
                    <li key={p.ref}>
                      <button class="landing-persona" title={t('About %s', p.name)} onClick={() => setDetail(p)}>
                        <span class="landing-persona__emoji" aria-hidden="true">
                          {p.emoji || '🎭'}
                        </span>
                        <span class="landing-persona__text">
                          <span class="landing-persona__name">{p.name}</span>
                          {p.specialty && <span class="landing-persona__spec">{p.specialty}</span>}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ))
          )}
        </section>
      </div>

      {newOpen && (
        <NewSessionSheet
          personas={personas}
          models={models}
          onCreate={(opts) => {
            setNewOpen(false)
            onNewSession(opts)
          }}
          onClose={() => setNewOpen(false)}
        />
      )}

      {detail && (
        <PersonaDetail
          client={client}
          persona={detail}
          stageEnabled={stageEnabled}
          onStart={() => {
            const ref = detail.ref
            setDetail(null)
            onNewSession({ persona: ref })
          }}
          onClose={() => setDetail(null)}
        />
      )}
    </div>
  )
}

// NewSessionSheet collects an optional persona + model before creating. Both
// default to "none/workspace default", so the fast path (just hit Start) still
// opens a bare session.
function NewSessionSheet(props: {
  personas: PersonaSummary[]
  models: ModelInfo[]
  onCreate: (opts: NewSessionOpts) => void
  onClose: () => void
}) {
  const [persona, setPersona] = useState('')
  const [modelIdx, setModelIdx] = useState(-1)

  const start = () => props.onCreate(newSessionOpts(persona, modelIdx >= 0 ? props.models[modelIdx] : undefined))

  return (
    <div class="landing-scrim" onClick={props.onClose}>
      <div class="landing-sheet" onClick={(e) => e.stopPropagation()}>
        <h3>{t('New session')}</h3>
        <label class="landing-field">
          <span>{t('Persona')}</span>
          <select value={persona} onChange={(e) => setPersona((e.target as HTMLSelectElement).value)}>
            <option value="">{t('No persona')}</option>
            {/* Sixteen flat options is a scroll; the same shelves make it a
                menu. A lone unnamed shelf yields no optgroup at all. */}
            {shelvePersonas(props.personas).map((shelf) =>
              shelf.name ? (
                <optgroup key={shelf.name} label={shelf.name}>
                  {shelf.personas.map(personaOption)}
                </optgroup>
              ) : (
                shelf.personas.map(personaOption)
              ),
            )}
          </select>
        </label>
        <label class="landing-field">
          <span>{t('Model')}</span>
          <select value={String(modelIdx)} onChange={(e) => setModelIdx(Number((e.target as HTMLSelectElement).value))}>
            <option value="-1">{t('Workspace default')}</option>
            {props.models.map((m, i) => (
              <option key={m.provider + '/' + m.id} value={String(i)}>
                {m.id} · {m.provider}
              </option>
            ))}
          </select>
        </label>
        <div class="landing-sheet__actions">
          <button class="btn" onClick={props.onClose}>
            {t('Cancel')}
          </button>
          <button class="btn primary" onClick={start}>
            {t('Start')}
          </button>
        </div>
      </div>
    </div>
  )
}

// personaOption is one entry of the persona picker, shared by the shelved and
// the flat rendering so they cannot drift apart.
function personaOption(p: PersonaSummary) {
  return (
    <option key={p.ref} value={p.ref}>
      {(p.emoji ? p.emoji + ' ' : '') + p.name}
    </option>
  )
}

// PersonaDetail loads the full PersonaView (personas.get) and offers to start a
// session as that persona. Editing lives in Stage (which owns the full editor),
// one link away — the panel does not duplicate it.
function PersonaDetail(props: {
  client: ClientLike | null
  persona: PersonaSummary
  stageEnabled: boolean
  onStart: () => void
  onClose: () => void
}) {
  const { client, persona } = props
  const [view, setView] = useState<PersonaView | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!client) return
    client
      .send<PersonaView>('personas.get', { ref: persona.ref }, '')
      .then(setView)
      .catch((e: unknown) => setError(errText(e)))
  }, [client, persona.ref])

  const v = view ?? persona

  return (
    <div class="landing-scrim" onClick={props.onClose}>
      <div class="landing-sheet landing-sheet--detail" onClick={(e) => e.stopPropagation()}>
        <header class="landing-persona-detail__head">
          <span class="landing-persona-detail__emoji" aria-hidden="true">
            {persona.emoji || '🎭'}
          </span>
          <div class="landing-persona-detail__id">
            <h3>{persona.name}</h3>
            <div class="landing-persona-detail__meta">
              {v.specialty && <span>{v.specialty}</span>}
              <span>{persona.origin}</span>
              {v.group && <span>{v.group}</span>}
            </div>
          </div>
          <button class="landing-close" onClick={props.onClose} aria-label={t('Close')}>
            ✕
          </button>
        </header>

        {error && (
          <p class="landing-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {view && (
          <div class="landing-persona-detail__body">
            {view.summary && <p class="landing-persona-detail__summary">{view.summary}</p>}
            {view.charter && (
              <div class="landing-persona-detail__field">
                <div class="landing-persona-detail__label">{t('Charter')}</div>
                <p>{view.charter}</p>
              </div>
            )}
          </div>
        )}

        <div class="landing-sheet__actions">
          {props.stageEnabled && (
            <a class="btn" href={stageHref('')} title={t('Manage personas in Stage')}>
              {t('Edit in Stage')}
            </a>
          )}
          <button class="btn primary" onClick={props.onStart}>
            {t('Start a session as %s', persona.name)}
          </button>
        </div>
      </div>
    </div>
  )
}
