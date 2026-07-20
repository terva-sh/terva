import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { PersonaSummary, PersonaView } from '../../platform/ctrlproto/types'

// A labeled long-text block; renders nothing when the value is empty (unlike the
// card sheet's "—", a persona simply omits fields it doesn't carry).
function Field(props: { label: string; value?: string }) {
  if (!props.value?.trim()) return null
  return (
    <div class="stage-personasheet__field">
      <div class="stage-personasheet__label">{props.label}</div>
      <p class="stage-personasheet__value">{props.value}</p>
    </div>
  )
}

// A labeled chip list (good-for / avoid-for / skills); omitted when empty.
function ListField(props: { label: string; items?: string[]; tone?: 'good' | 'avoid' }) {
  if (!props.items?.length) return null
  return (
    <div class="stage-personasheet__field">
      <div class="stage-personasheet__label">{props.label}</div>
      <div class="stage-personasheet__chips">
        {props.items.map((it) => (
          <span key={it} class={`stage-personasheet__chip ${props.tone ? `stage-personasheet__chip--${props.tone}` : ''}`}>
            {it}
          </span>
        ))}
      </div>
    </div>
  )
}

// PersonaSheet is the persona detail sheet (rough-edge #9): the Library's persona
// roster was display-only; tapping one now opens its full PersonaView
// (personas.get) — specialty, summary, introduction, the charter that shapes its
// identity, and the good-for/avoid-for guidance.
//
// The editor seam this comment used to promise is now wired, and provenance
// decides which actions it offers. A persona of YOURS edits and deletes in
// place. A built-in or extension persona does NEITHER: it offers Duplicate.
//
// That asymmetry is deliberate and is not what the daemon would enforce on its
// own — personas.edit on a built-in succeeds, writing a user file that shadows
// it by slug. The shadow is permanent and invisible: a later terva release that
// improves that charter would never reach you, and no screen would ever explain
// why. Duplicating under a new name gets the same customization while leaving
// the built-in live.
export function PersonaSheet(props: {
  client: ClientLike
  persona: PersonaSummary
  onClose: () => void
  onEdit?: () => void
  onDuplicate?: () => void
  onDelete?: () => void
}) {
  const { client, persona, onClose, onEdit, onDuplicate, onDelete } = props
  // `editable` is the daemon's own verdict: true only for a user on-disk file.
  const mine = persona.editable === true
  const [view, setView] = useState<PersonaView | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    // Name accepts a bare name or a namespace:name ref — pass the ref so a
    // namespaced persona resolves unambiguously.
    client
      .send<PersonaView>('personas.get', { name: persona.ref })
      .then(setView)
      .catch((e: unknown) => setError(String(e)))
  }, [persona.ref])

  const v = view ?? persona

  return (
    <div class="stage-sheet-backdrop" onClick={onClose}>
      <div class="stage-sheet stage-personasheet" onClick={(e) => e.stopPropagation()}>
        <header class="stage-personasheet__head">
          <span class="stage-personasheet__emoji" aria-hidden="true">
            {persona.emoji || '🎭'}
          </span>
          <div class="stage-personasheet__id">
            <h3>{persona.name}</h3>
            <div class="stage-personasheet__meta">
              {v.specialty && <span>{v.specialty}</span>}
              <span>{persona.origin}</span>
              {persona.namespace && <span>{persona.namespace}</span>}
              {persona.immersive && <span class="stage-personasheet__immersive">{t('immersive')}</span>}
            </div>
          </div>
          {mine && onEdit && (
            <button class="stage-cardsheet__edit" title={t('Edit this persona')} onClick={onEdit}>
              {t('✎ Edit')}
            </button>
          )}
          {!mine && onDuplicate && (
            <button
              class="stage-cardsheet__edit"
              title={t('Make your own copy of %s under a new name — the %s one stays as it is', persona.name, persona.origin)}
              onClick={onDuplicate}
            >
              {t('⧉ Duplicate')}
            </button>
          )}
          <button class="stage-drawer__close" onClick={onClose}>
            ✕
          </button>
        </header>

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}
        {!view && !error && <p class="stage-empty">{t('Loading…')}</p>}

        {view && (
          <div class="stage-personasheet__body">
            {view.summary && <p class="stage-personasheet__summary">{view.summary}</p>}
            <Field label={t('Introduction')} value={view.introduction} />
            <Field label={t('Charter')} value={view.charter} />
            <ListField label={t('Good for')} items={view.good_for} tone="good" />
            <ListField label={t('Avoid for')} items={view.avoid_for} tone="avoid" />
            <ListField label={t('Recommended skills')} items={view.recommended_skills} />
            <Field label={t('Pronunciation')} value={view.pronunciation} />
          </div>
        )}

        {mine && onDelete && (
          <button class="stage-sheet__delete" onClick={onDelete}>
            {t('Delete persona')}
          </button>
        )}
      </div>
    </div>
  )
}
