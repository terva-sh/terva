import { useEffect, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { PersonaSummary, PersonaView, PersonaWriteParams } from '../../platform/ctrlproto/types'

// A labeled input/textarea. Same shape as CardEditor's EditField (and the
// read-only Field in PersonaSheet), so authoring and inspecting read alike.
function EditField(props: {
  label: string
  value: string
  hint?: string
  area?: boolean
  rows?: number
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
        <input class="stage-editfield__input" value={props.value} onInput={(e) => props.onInput((e.target as HTMLInputElement).value)} />
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
  recommended_skills: string[]
  good_for: string[]
  avoid_for: string[]
  immersive: boolean
  introduction: string
  charter: string
}

const EMPTY: PersonaForm = {
  name: '',
  pronunciation: '',
  specialty: '',
  summary: '',
  emoji: '',
  accent_color: '',
  recommended_skills: [],
  good_for: [],
  avoid_for: [],
  immersive: false,
  introduction: '',
  charter: '',
}

export function formFromView(v: PersonaView): PersonaForm {
  return {
    name: v.name ?? '',
    pronunciation: v.pronunciation ?? '',
    specialty: v.specialty ?? '',
    summary: v.summary ?? '',
    emoji: v.emoji ?? '',
    accent_color: v.accent_color ?? '',
    recommended_skills: v.recommended_skills ?? [],
    good_for: v.good_for ?? [],
    avoid_for: v.avoid_for ?? [],
    immersive: v.immersive ?? false,
    introduction: v.introduction ?? '',
    charter: v.charter ?? '',
  }
}

// duplicateName proposes a free name for a copy. The daemon will NOT stop you
// from creating a persona whose name matches a built-in — personas.create only
// checks the USER layer — and such a persona silently shadows the built-in by
// slug, which is exactly the fork this flow exists to avoid. So the collision
// check is the client's, against the whole roster.
export function duplicateName(base: string, taken: string[]): string {
  const used = new Set(taken.map((n) => n.trim().toLowerCase()))
  const candidate = `${base} (my copy)`
  if (!used.has(candidate.toLowerCase())) return candidate
  for (let n = 2; n < 100; n++) {
    const next = `${base} (my copy ${n})`
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
// slug — but the result is a permanent, invisible fork: a later terva release
// that improves that charter would never reach you, and nothing in the UI would
// ever say why. The sheet offers Duplicate for built-ins instead, which lands
// here in create mode under a new name, so the built-in keeps flowing.
export function PersonaEditor(props: {
  client: Client
  // The persona to edit (yours), or the one to duplicate. Absent = a blank new one.
  persona?: PersonaSummary
  // duplicate: seed from `persona` but create under a NEW name rather than
  // overwrite. This is how a built-in is customized.
  duplicate?: boolean
  // Every name already in the roster, for the collision check the daemon does
  // not do (create only checks the user layer, so a built-in's name silently
  // shadows it).
  taken?: string[]
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
      .send<PersonaView>('personas.get', { name: persona.ref })
      .then((v) => {
        const f = formFromView(v)
        // A duplicate keeps the charter and every other field — that is the
        // point of duplicating — but must not keep the NAME, or create would
        // either conflict with your own or shadow the built-in it copied.
        setForm(duplicate ? { ...f, name: duplicateName(f.name, taken) } : f)
      })
      .catch((e: unknown) => setError(String(e)))
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
      await client.send<PersonaView>(creating ? 'personas.create' : 'personas.edit', params)
      setSavedAt(true)
      onSaved()
    } catch (e) {
      setError(String(e))
    } finally {
      setSaving(false)
    }
  }

  const title = creating ? (duplicate ? `Duplicate ${persona?.name ?? ''}` : 'New persona') : `Edit ${persona?.name ?? ''}`

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
            This makes your own copy under a new name. The original stays as it is, so updates to it keep reaching you.
          </p>
        )}

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {!form && !error && <p class="stage-empty">Loading…</p>}

        {form && (
          <div class="stage-personaeditor__body">
            <EditField
              label="Name"
              value={form.name}
              hint="How you refer to this persona. It also names the file it is saved in."
              onInput={(v) => set('name', v)}
            />
            {nameTaken && (
              <p class="stage-personaeditor__warn">
                “{form.name.trim()}” is already in your roster. Saving under a name that matches a built-in would hide the
                built-in instead of adding to it — pick another.
              </p>
            )}
            {nameUnusable && (
              <p class="stage-personaeditor__warn">
                This name has no letters or digits to make a filename from. Add at least one.
              </p>
            )}
            {creating && !!slug && <p class="stage-personaeditor__slug">Saves as {slug}.md</p>}

            <EditField label="Emoji" value={form.emoji} hint="Shown beside the name in the roster." onInput={(v) => set('emoji', v)} />
            <EditField label="Specialty" value={form.specialty} hint="One line: what this persona is for." onInput={(v) => set('specialty', v)} />
            <EditField label="Summary" value={form.summary} area rows={2} onInput={(v) => set('summary', v)} />
            <EditField
              label="Pronunciation"
              value={form.pronunciation}
              hint="Optional, for a name that is not obvious — e.g. SEP-pah."
              onInput={(v) => set('pronunciation', v)}
            />
            <EditField label="Accent colour" value={form.accent_color} hint="A hex colour, e.g. #e0af68." onInput={(v) => set('accent_color', v)} />

            <EditField
              label="Charter"
              value={form.charter}
              area
              rows={12}
              hint="The behavioural body — what actually shapes how this persona acts. This is the substance of a persona."
              onInput={(v) => set('charter', v)}
            />
            <EditField
              label="Introduction"
              value={form.introduction}
              area
              rows={2}
              hint="How the persona introduces itself when it takes over."
              onInput={(v) => set('introduction', v)}
            />

            <EditField
              label="Good for"
              value={form.good_for.join(', ')}
              hint="Comma-separated. Also what makes this persona dispatchable to sub-agents."
              onInput={(v) => set('good_for', splitList(v))}
            />
            <EditField
              label="Avoid for"
              value={form.avoid_for.join(', ')}
              hint="Comma-separated."
              onInput={(v) => set('avoid_for', splitList(v))}
            />
            <EditField
              label="Recommended skills"
              value={form.recommended_skills.join(', ')}
              hint="Comma-separated."
              onInput={(v) => set('recommended_skills', splitList(v))}
            />

            <label class="stage-personaeditor__check">
              <input type="checkbox" checked={form.immersive} onChange={(e) => set('immersive', (e.target as HTMLInputElement).checked)} />
              <span>
                Immersive — the charter owns the whole system prompt (roleplay) instead of layering on terva's own identity.
              </span>
            </label>
          </div>
        )}

        <div class="stage-personaeditor__bar">
          {savedAt && !saving && <span class="stage-cardeditor__saved">Saved ✓</span>}
          <button class="stage-cardeditor__cancel" onClick={onClose}>
            Close
          </button>
          <button
            class="stage-cardeditor__save"
            disabled={saving || !form || !form.name.trim() || nameTaken || nameUnusable}
            onClick={() => void save()}
          >
            {saving ? 'Saving…' : creating ? 'Create persona' : 'Save changes'}
          </button>
        </div>
      </div>
    </div>
  )
}
