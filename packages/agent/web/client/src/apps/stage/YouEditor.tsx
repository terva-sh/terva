import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { UserPersonaView, UserPersonasListResult } from '../../platform/ctrlproto/types'
import { IdentityField, PRONOUN_OPTIONS, GENDER_OPTIONS } from './identity'

// ScenePersona is who the author is in ONE scene, handed down from the chat that
// opened this screen. The daemon only reports the bound persona for a
// materialized session (a cold sessions.list row carries no user fields), so the
// scene passes what it already has rather than this screen going looking for it.
export interface ScenePersona {
  session: string
  name: string
  description: string
  gender: string
  pronouns: string
}

interface Fields {
  name: string
  description: string
  gender: string
  pronouns: string
}

const blank: Fields = { name: '', description: '', gender: '', pronouns: '' }

const fieldsOf = (p: UserPersonaView): Fields => ({
  name: p.name ?? '',
  description: p.description ?? '',
  gender: p.gender ?? '',
  pronouns: p.pronouns ?? '',
})

const same = (a: Fields, b: Fields) =>
  a.name === b.name && a.description === b.description && a.gender === b.gender && a.pronouns === b.pronouns

// YouEditor is the other half of the studio: the character is who you are playing
// WITH, this is who you are playing AS. Both deserved the same room, and until now
// only one had it — your own persona lived in a corner of the steering drawer,
// reachable only from inside a scene, and the saved library was a row of chips you
// could pick or delete but never actually edit.
//
// It runs in two modes, because "who am I" means two different things:
//
//   IN A SCENE (opened from "playing as …" in the chat header) the editor's
//   working copy is THAT SCENE's persona. Every edit commits to the session on
//   blur, exactly as the drawer's You tab does — a scene is live, and steering it
//   should not need a save. The library below is a shelf: pull an identity from
//   it to play as, or push this one back with "Keep in your personas".
//
//   FROM THE LIBRARY (no scene) there is nothing live to steer, so the editor
//   edits a SAVED persona directly and an explicit Save commits it. This is the
//   mode where a rename is possible, which is why userpersonas.save takes the ref
//   of the persona being edited — without it a renamed persona was written under
//   the new name and left behind under the old one.
export function YouEditor(props: { client: ClientLike; ready: boolean; scene: ScenePersona | null }) {
  const { client, ready, scene } = props
  const [personas, setPersonas] = useState<UserPersonaView[]>([])
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  // The persona the library mode is editing: its ref, or '' for one being made.
  // Unused in scene mode, where the scene itself is the thing being edited.
  const [editing, setEditing] = useState('')
  // The name it had when it was opened, so the heading says which persona this
  // is rather than following the field and renaming itself as you type.
  const [editingName, setEditingName] = useState('')
  const [form, setForm] = useState<Fields>(() => (scene ? { ...scene } : blank))
  // What the session last accepted, so a blur with nothing changed does not fire a
  // bind — a bind with a changed NAME rebuilds the cached prompt prefix, so a
  // spurious one is not free.
  const committed = useRef<Fields>(scene ? { ...scene } : blank)

  const load = () =>
    client
      .send<UserPersonasListResult>('userpersonas.list', {})
      .then((r) => setPersonas(r.personas ?? []))
      .catch((e: unknown) => setError(errText(e)))
  useEffect(() => {
    if (!ready) return
    void load()
  }, [ready])

  const set = (patch: Partial<Fields>) => setForm((f) => ({ ...f, ...patch }))
  const flash = (msg: string) => {
    setSaved(msg)
    setTimeout(() => setSaved(''), 1600)
  }

  // Scene mode: commit the working copy to the session. `over` lets a dropdown
  // pass its just-picked value through, since a select's onChange fires before
  // the state settles.
  const commitScene = (over?: Partial<Fields>) => {
    if (!scene) return
    const next = { ...form, ...over }
    if (same(next, committed.current)) return
    committed.current = next
    client
      .send('user.bind', { name: next.name, description: next.description, gender: next.gender, pronouns: next.pronouns }, scene.session)
      .catch((e: unknown) => setError(errText(e)))
  }

  // Play as a saved persona here. The daemon resolves the ref and copies its
  // fields into the session, so the form is re-seeded from the same row rather
  // than re-fetched.
  const playAs = (p: UserPersonaView) => {
    if (!scene || !p.ref) return
    const next = fieldsOf(p)
    setForm(next)
    committed.current = next
    setError('')
    client.send('user.bind', { ref: p.ref }, scene.session).catch((e: unknown) => setError(errText(e)))
  }

  // Save the working copy into the library. In scene mode this is "keep this
  // identity for other stories" and is name-keyed (no ref), so it updates a
  // persona of the same name and otherwise makes a new one. In library mode it
  // carries the ref of the persona being edited, which is what turns a changed
  // name into a rename instead of a second copy.
  // (`editing` is only ever set in library mode, so the scene arm below states
  // the rule rather than enforcing something reachable.)
  const save = (makeDefault: boolean) => {
    const name = form.name.trim()
    if (!name) {
      setError(t('Give your persona a name to save it.'))
      return
    }
    setError('')
    client
      .send<UserPersonaView>('userpersonas.save', {
        ref: scene ? '' : editing,
        name,
        description: form.description.trim(),
        gender: form.gender.trim(),
        pronouns: form.pronouns.trim(),
      })
      .then((p) => {
        // Adopt the ref the save minted (or moved to), so the NEXT save edits this
        // persona rather than making another one — and, after a rename, so it
        // renames from the new name rather than the one that no longer exists.
        if (!scene && p.ref) {
          setEditing(p.ref)
          setEditingName(p.name ?? name)
        }
        return makeDefault && p.ref ? client.send('userpersonas.set_default', { ref: p.ref }) : undefined
      })
      .then(() => load())
      .then(() => flash(makeDefault ? t('Saved as your default.') : t('Saved.')))
      .catch((e: unknown) => setError(errText(e)))
  }

  const setDefault = (p: UserPersonaView) => {
    if (!p.ref) return
    setError('')
    client
      .send('userpersonas.set_default', { ref: p.default ? '' : p.ref })
      .then(() => load())
      .catch((e: unknown) => setError(errText(e)))
  }

  const remove = (p: UserPersonaView) => {
    if (!p.ref) return
    if (!window.confirm(t('Delete the persona “%s”? Scenes you have already played as them are untouched.', p.name))) return
    setError('')
    client
      .send('userpersonas.delete', { ref: p.ref })
      .then(() => {
        // The editor was pointed at the row that just went away.
        if (editing === p.ref) {
          setEditing('')
          setEditingName('')
          setForm(blank)
        }
        return load()
      })
      .catch((e: unknown) => setError(errText(e)))
  }

  // Library mode: open a saved persona in the editor.
  const openPersona = (p: UserPersonaView) => {
    setError('')
    setEditing(p.ref ?? '')
    setEditingName(p.name ?? '')
    setForm(fieldsOf(p))
  }

  const startNew = () => {
    setEditing('')
    setEditingName('')
    setForm(blank)
    setError('')
  }

  // The library row the scene is currently playing as, matched by name — a bind
  // copies fields into the session and keeps no ref, so the name is the only link
  // back. Used to mark the row, never to write anything.
  const playingRef = scene
    ? personas.find((p) => (p.name ?? '').trim().toLowerCase() === form.name.trim().toLowerCase() && form.name.trim() !== '')?.ref ?? ''
    : ''

  return (
    <div class="stage-you">
      <section class="stage-you__editor">
        <h2>{scene ? t('You, in this scene') : editing ? t('Editing %s', editingName) : t('A new persona')}</h2>
        <p class="stage-hint">
          {scene
            ? t('Who you are to the character. Changes apply to this scene as you make them — a new name rebuilds the prompt, so the next reply starts uncached. Keep it in your personas to play as them again elsewhere.')
            : t('The identities you play as. Editing one here changes it for the scenes you start from now on; a scene already running keeps the copy it was given.')}
        </p>
        <label class="stage-editfield">
          <span class="stage-editfield__label">{t('Name')}</span>
          <input
            class="stage-you__name stage-editfield__input"
            placeholder={t('Your name (e.g. Kira)')}
            value={form.name}
            onInput={(e) => set({ name: (e.target as HTMLInputElement).value })}
            onBlur={() => commitScene()}
          />
        </label>
        <label class="stage-editfield">
          <span class="stage-editfield__label">{t('Description')}</span>
          <textarea
            class="stage-you__desc stage-editfield__area"
            rows={4}
            placeholder={t('e.g. A wary courier who trusts no one.')}
            value={form.description}
            onInput={(e) => set({ description: (e.target as HTMLTextAreaElement).value })}
            onBlur={() => commitScene()}
          />
        </label>
        <div class="stage-identity-row">
          <IdentityField
            key={`pronouns-${editing}-${scene?.session ?? ''}`}
            label={t('Pronouns')}
            placeholder={t('e.g. xe/xem')}
            options={PRONOUN_OPTIONS}
            value={form.pronouns}
            onChange={(v) => set({ pronouns: v })}
            onCommit={(v) => commitScene({ pronouns: v })}
          />
          <IdentityField
            key={`gender-${editing}-${scene?.session ?? ''}`}
            label={t('Gender')}
            placeholder={t('Describe it your way')}
            options={GENDER_OPTIONS}
            value={form.gender}
            onChange={(v) => set({ gender: v })}
            onCommit={(v) => commitScene({ gender: v })}
          />
        </div>
        <div class="stage-you__actions">
          <button class="stage-you__save" disabled={!form.name.trim()} onClick={() => save(false)}>
            {scene ? t('Keep in your personas') : t('Save')}
          </button>
          <button
            class="stage-you__save"
            disabled={!form.name.trim()}
            title={t('Save this persona and pre-fill it into every new chat')}
            onClick={() => save(true)}
          >
            {t('Save as default')}
          </button>
          {!scene && (editing || form.name.trim() !== '') && (
            <button class="stage-you__new" onClick={startNew}>
              {t('+ New persona')}
            </button>
          )}
          {saved && <span class="stage-you__saved">{saved}</span>}
        </div>
        {error && <p class="stage-error">{error}</p>}
      </section>

      <section class="stage-you__library">
        <h3>{t('Your personas')}</h3>
        {personas.length === 0 ? (
          <p class="stage-empty">{t('None saved yet. Fill in who you are above and keep it — a saved persona can be played in any story, and the default pre-fills every new chat.')}</p>
        ) : (
          <ul class="stage-you__list">
            {personas.map((p) => (
              <li
                key={p.ref}
                class={`stage-you__row ${p.ref === editing ? 'stage-you__row--editing' : ''} ${p.ref === playingRef ? 'stage-you__row--playing' : ''}`}
              >
                <button
                  class="stage-you__pick"
                  title={scene ? t('Play as %s in this scene', p.name) : t('Edit %s', p.name)}
                  onClick={() => (scene ? playAs(p) : openPersona(p))}
                >
                  <span class="stage-you__rowname">
                    {p.default ? '★ ' : ''}
                    {p.name}
                  </span>
                  {p.description && <span class="stage-you__rowdesc">{p.description}</span>}
                </button>
                {p.ref === playingRef && <span class="stage-you__badge">{t('playing as')}</span>}
                <button
                  class="stage-you__default"
                  title={p.default ? t('Stop pre-filling %s into new chats', p.name) : t('Pre-fill %s into every new chat', p.name)}
                  aria-pressed={!!p.default}
                  onClick={() => setDefault(p)}
                >
                  {p.default ? '★' : '☆'}
                </button>
                <button class="stage-you__del" title={t('Delete %s', p.name)} onClick={() => remove(p)}>
                  ✕
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
