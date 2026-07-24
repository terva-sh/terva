import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { Group, ModelInfo, PersonaSummary, PersonaView, SessionInfo } from '../../platform/ctrlproto/types'
import type { GroupFilter } from '../../platform/groups'
import { stageHref } from '../../ui/navlinks'
import { SessionsBoard } from '../board/SessionsBoard'

// The session-grid props are threaded straight through to SessionsBoard; app.tsx
// still owns the session list, subscriptions, and verbs (web layering rules).
type BoardPassthrough = {
  sessions: SessionInfo[]
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
  const [detail, setDetail] = useState<PersonaSummary | null>(null)
  const [newOpen, setNewOpen] = useState(false)

  useEffect(() => {
    if (!client) return
    // personas.list is optional (served only by a PersonasController); a daemon
    // without it just leaves the roster empty rather than erroring.
    client
      .send<{ personas: PersonaSummary[] }>('personas.list', null, '')
      .then((r) => setPersonas(r.personas ?? []))
      .catch(() => setPersonas([]))
  }, [client])

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
        <span class={`dot ${status}`} title={status} />
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
          {personas.length === 0 ? (
            <p class="landing-empty">{t('No personas available.')}</p>
          ) : (
            <ul class="landing-persona-list">
              {personas.map((p) => (
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
            {props.personas.map((p) => (
              <option key={p.ref} value={p.ref}>
                {(p.emoji ? p.emoji + ' ' : '') + p.name}
              </option>
            ))}
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
      .send<PersonaView>('personas.get', { name: persona.ref }, '')
      .then(setView)
      .catch((e: unknown) => setError(String(e)))
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
