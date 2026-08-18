import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { PersonaSummary, PersonaView, PersonaWriteParams } from '../../platform/ctrlproto/types'

// A labeled input/textarea. Same shape as CardEditor's EditField (and the
// read-only Field in PersonaSheet), so authoring and inspecting read alike.
function EditField(props: {
  label: string
  value: string
  hint?: string
  area?: boolean
  rows?: number
  // Existing values to suggest. A datalist, not a select: the point of these is
  // that you may type one that does not exist yet.
  options?: string[]
  listId?: string
  onInput: (v: string) => void
}) {
  return (
    <label class="stage-editfield">
      <span class="stage-editfield__label">
        {props.label}
        {props.hint && (
          <span class="stage-editfield__hint" title={props.hint} aria-label={props.hint}>
            ⓘ
          </span>
        )}
      </span>
      {props.area ? (
        <textarea
          class="stage-editfield__area"
          rows={props.rows ?? 3}
          value={props.value}
          onInput={(e) => props.onInput((e.target as HTMLTextAreaElement).value)}
        />
      ) : (
        <>
          <input
            class="stage-editfield__input"
            value={props.value}
            list={props.options?.length ? props.listId : undefined}
            onInput={(e) => props.onInput((e.target as HTMLInputElement).value)}
          />
          {!!props.options?.length && (
            <datalist id={props.listId}>
              {props.options.map((o) => (
                <option key={o} value={o} />
              ))}
            </datalist>
          )}
        </>
      )}
    </label>
  )
}

// The comma-joined list idiom CardEditor uses for tags — the right weight for
// good-for / avoid-for / skills, which are short phrases rather than prose.
const splitList = (v: string): string[] =>
  v
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

// personaForm is exactly PersonaWriteParams with every field present, because a
// write REPLACES the persona file: a field left out of the payload is erased,
// not preserved. Holding them all in one object is what makes that safe — there
// is no path where a field the editor rendered fails to be re-sent.
export interface PersonaForm {
  name: string
  pronunciation: string
  specialty: string
  summary: string
  emoji: string
  accent_color: string
  group: string
  recommended_skills: string[]
  good_for: string[]
  avoid_for: string[]
  immersive: boolean
  introduction: string
  charter: string
  extends: string
}

const EMPTY: PersonaForm = {
  name: '',
  pronunciation: '',
  specialty: '',
  summary: '',
  emoji: '',
  accent_color: '',
  group: '',
  recommended_skills: [],
  good_for: [],
  avoid_for: [],
  immersive: false,
  introduction: '',
  charter: '',
  extends: '',
}

export function formFromView(v: PersonaView): PersonaForm {
  return {
    name: v.name ?? '',
    pronunciation: v.pronunciation ?? '',
    specialty: v.specialty ?? '',
    summary: v.summary ?? '',
    emoji: v.emoji ?? '',
    accent_color: v.accent_color ?? '',
    group: v.group ?? '',
    recommended_skills: v.recommended_skills ?? [],
    good_for: v.good_for ?? [],
    avoid_for: v.avoid_for ?? [],
    immersive: v.immersive ?? false,
    introduction: v.introduction ?? '',
    charter: v.charter ?? '',
    // Round-tripped verbatim rather than through a checkbox. A checkbox would
    // have to map some strings to on and everything else to off, and saving
    // then rewrites the file with whatever the checkbox meant — erasing a value
    // it could not represent.
    extends: v.extends ?? '',
  }
}

// duplicateName proposes a free name for a copy. The daemon will NOT stop you
// from creating a persona whose name matches a built-in — personas.create only
// checks the USER layer — and the result is either a silent fork of that
// built-in or a second roster entry wearing its name, depending on whether the
// built-in is top-level. Both are what this flow exists to avoid, so the
// collision check is the client's, against the whole roster.
export function duplicateName(base: string, taken: string[]): string {
  const used = new Set(taken.map((n) => n.trim().toLowerCase()))
  const candidate = t('%s (my copy)', base)
  if (!used.has(candidate.toLowerCase())) return candidate
  for (let n = 2; n < 100; n++) {
    const next = t('%s (my copy %d)', base, n)
    if (!used.has(next.toLowerCase())) return next
  }
  return candidate
}

// slugPreview mirrors the daemon's cardSlug: the filename a persona will take.
// Worth showing because a name with nothing in [a-z0-9] slugs to "" and the
// write fails with "no usable filename" — an error about a filename, for a form
// that never mentioned files.
export function slugPreview(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '')
    .slice(0, 32)
}

// PersonaEditor is the writable sibling of PersonaSheet — the seam that sheet's
// own comment described. It authors a persona in the USER library, either new
// (personas.create) or an existing one of yours (personas.edit).
//
// It deliberately does NOT offer in-place editing of a built-in. The daemon
// allows it — personas.edit on a built-in writes a user file that shadows it by
// its ref — but the result is a permanent, invisible fork: a later terva release
// that improves that charter would never reach you, and nothing in the UI would
// ever say why. The sheet offers Duplicate for built-ins instead, which lands
// here in create mode under a new name, so the built-in keeps flowing.
export function PersonaEditor(props: {
  client: ClientLike
  // The persona to edit (yours), or the one to duplicate. Absent = a blank new one.
  persona?: PersonaSummary
  // duplicate: seed from `persona` but create under a NEW name rather than
  // overwrite. This is how a built-in is customized.
  duplicate?: boolean
  // Every name already in the roster, for the collision check the daemon does
  // not do (create only checks the user layer, so a built-in's name silently
  // shadows it).
  taken?: string[]
  // The shelves the roster already has, offered as suggestions. Free text, so a
  // new one is always one keystroke away.
  groups?: string[]
  onClose: () => void
  onSaved: () => void
}) {
  const { client, persona, duplicate, onClose, onSaved } = props
  const taken = props.taken ?? []
  const creating = !persona || duplicate
  const [form, setForm] = useState<PersonaForm | null>(persona ? null : { ...EMPTY })
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState(false)

  useEffect(() => {
    if (!persona) return
    client
      .send<PersonaView>('personas.get', { ref: persona.ref })
      .then((v) => {
        const f = formFromView(v)
        // A duplicate keeps the charter and every other field — that is the
        // point of duplicating — but must not keep the NAME, or create would
        // either conflict with your own or shadow the built-in it copied.
        setForm(duplicate ? { ...f, name: duplicateName(f.name, taken) } : f)
      })
      .catch((e: unknown) => setError(errText(e)))
  }, [persona?.ref, duplicate])

  const set = <K extends keyof PersonaForm>(key: K, value: PersonaForm[K]) => {
    setForm((f) => (f ? { ...f, [key]: value } : f))
    setSavedAt(false)
  }

  const nameTaken =
    creating && !!form && taken.some((n) => n.trim().toLowerCase() === form.name.trim().toLowerCase())
  const slug = form ? slugPreview(form.name) : ''
  const nameUnusable = !!form && form.name.trim() !== '' && slug === ''

  const save = async () => {
    if (!form) return
    setSaving(true)
    setError('')
    try {
      // The whole form, always: a write replaces the file, so anything omitted
      // is erased rather than left alone.
      const params: PersonaWriteParams = { ...form }
      // An edit also says WHICH persona it overwrites, and the ref comes from
      // the persona this editor opened rather than from the form — the name in
      // the form is content the user may have just changed, and with two
      // personas sharing one it names the wrong persona. A create sends no ref:
      // there is nothing to overwrite, and duplicating is a create.
      await (creating
        ? client.send<PersonaView>('personas.create', params)
        : client.send<PersonaView>('personas.edit', { ...params, ref: persona!.ref }))
      setSavedAt(true)
      onSaved()
    } catch (e) {
      setError(errText(e))
    } finally {
      setSaving(false)
    }
  }

  const title = creating ? (duplicate ? t('Duplicate %s', persona?.name ?? '') : t('New persona')) : t('Edit %s', persona?.name ?? '')

  return (
    <div class="stage-sheet-backdrop" onClick={onClose}>
      <div class="stage-sheet stage-sheet--detail stage-personaeditor" onClick={(e) => e.stopPropagation()}>
        <header class="stage-personaeditor__head">
          <h3>{title}</h3>
          <button class="stage-drawer__close" onClick={onClose}>
            ✕
          </button>
        </header>

        {duplicate && (
          <p class="stage-hint">
            {t('This makes your own copy under a new name. The original stays as it is, so updates to it keep reaching you.')}
          </p>
        )}

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {!form && !error && <p class="stage-empty">{t('Loading…')}</p>}

        {form && (
          <div class="stage-personaeditor__body">
            <EditField
              label={t('Name')}
              value={form.name}
              hint={t('How you refer to this persona. It also names the file it is saved in.')}
              onInput={(v) => set('name', v)}
            />
            {nameTaken && (
              <p class="stage-personaeditor__warn">
                {t(
                  '“%s” is already in your roster. Saving under a name that matches a built-in would hide the built-in instead of adding to it — pick another.',
                  form.name.trim(),
                )}
              </p>
            )}
            {nameUnusable && (
              <p class="stage-personaeditor__warn">
                {t('This name has no letters or digits to make a filename from. Add at least one.')}
              </p>
            )}
            {creating && !!slug && <p class="stage-personaeditor__slug">{t('Saves as %s.md', slug)}</p>}

            <EditField label={t('Emoji')} value={form.emoji} hint={t('Shown beside the name in the roster.')} onInput={(v) => set('emoji', v)} />
            <EditField label={t('Specialty')} value={form.specialty} hint={t('One line: what this persona is for.')} onInput={(v) => set('specialty', v)} />
            <EditField
              label={t('Group')}
              value={form.group}
              hint={t('The shelf this persona files under in the roster. Pick an existing one or type a new one.')}
              options={props.groups}
              listId="stage-persona-groups"
              onInput={(v) => set('group', v)}
            />
            <EditField label={t('Summary')} value={form.summary} area rows={2} onInput={(v) => set('summary', v)} />
            <EditField
              label={t('Pronunciation')}
              value={form.pronunciation}
              hint={t('Optional, for a name that is not obvious — e.g. SEP-pah.')}
              onInput={(v) => set('pronunciation', v)}
            />
            <EditField label={t('Accent colour')} value={form.accent_color} hint={t('A hex colour, e.g. #e0af68.')} onInput={(v) => set('accent_color', v)} />

            <EditField
              label={t('Charter')}
              value={form.charter}
              area
              rows={12}
              hint={t('The behavioural body — what actually shapes how this persona acts. This is the substance of a persona.')}
              onInput={(v) => set('charter', v)}
            />
            <EditField
              label={t('Extends')}
              value={form.extends}
              hint={t("Optional. Name a persona whose charter this one builds on — today only “mieli”, the default. Its charter comes first, yours after. Leave empty and yours is the only charter in the prompt.")}
              onInput={(v) => set('extends', v)}
            />
            <EditField
              label={t('Introduction')}
              value={form.introduction}
              area
              rows={2}
              hint={t('How the persona introduces itself when it takes over.')}
              onInput={(v) => set('introduction', v)}
            />

            <EditField
              label={t('Good for')}
              value={form.good_for.join(', ')}
              hint={t('Comma-separated. Also what makes this persona dispatchable to sub-agents.')}
              onInput={(v) => set('good_for', splitList(v))}
            />
            <EditField
              label={t('Avoid for')}
              value={form.avoid_for.join(', ')}
              hint={t('Comma-separated.')}
              onInput={(v) => set('avoid_for', splitList(v))}
            />
            <EditField
              label={t('Recommended skills')}
              value={form.recommended_skills.join(', ')}
              hint={t('Comma-separated.')}
              onInput={(v) => set('recommended_skills', splitList(v))}
            />

            <label class="stage-personaeditor__check">
              <input type="checkbox" checked={form.immersive} onChange={(e) => set('immersive', (e.target as HTMLInputElement).checked)} />
              <span>
                {t("Immersive — the charter owns the whole system prompt (roleplay) instead of layering on terva's own identity.")}
              </span>
            </label>
          </div>
        )}

        <div class="stage-personaeditor__bar">
          {savedAt && !saving && <span class="stage-cardeditor__saved">{t('Saved ✓')}</span>}
          <button class="stage-cardeditor__cancel" onClick={onClose}>
            {t('Close')}
          </button>
          <button
            class="stage-cardeditor__save"
            disabled={saving || !form || !form.name.trim() || nameTaken || nameUnusable}
            onClick={() => void save()}
          >
            {saving ? t('Saving…') : creating ? t('Create persona') : t('Save changes')}
          </button>
        </div>
      </div>
    </div>
  )
}
