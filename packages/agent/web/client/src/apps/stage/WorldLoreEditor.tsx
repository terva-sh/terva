import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { WorldLoreEntry, WorldView } from '../../platform/ctrlproto/types'

// The saved World's lorebook, edited with no session open.
//
// Until now this surface existed only inside a session's steering drawer, which
// meant the lorebook of a World you were not currently playing was unreachable —
// you could read it on the shelf and change nothing. It writes through
// worlds.lore.put / worlds.lore.delete, which take the World's id rather than a
// frame session.
//
// One entry is open at a time. The alternative — every row an always-live form —
// looks more direct and is worse here: a lorebook row is mostly CONTENT, so a
// dozen expanded textareas bury the list you navigate by, and each keystroke
// would have to decide whether it is worth a round trip. Opening one is also what
// makes a rename expressible: `replace` needs the name the entry HAD, and a form
// that edits in place has already lost it.
export function WorldLoreEditor(props: {
  client: ClientLike
  world: WorldView
  onWorld: (w: WorldView) => void
  onError: (e: string) => void
}) {
  const lore = props.world.lore ?? []
  // null = nothing open; '' = composing a new entry; otherwise the name of the
  // entry being edited — which is also the `replace` a rename sends.
  const [editing, setEditing] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [keys, setKeys] = useState('')
  const [constant, setConstant] = useState(false)
  const [audience, setAudience] = useState('')
  const [content, setContent] = useState('')
  const [busy, setBusy] = useState(false)

  const open = (e: WorldLoreEntry | null) => {
    setEditing(e ? e.name : '')
    setName(e?.name ?? '')
    setKeys((e?.keys ?? []).join(', '))
    setConstant(e?.constant ?? false)
    setAudience((e?.audience ?? []).join(', '))
    setContent(e?.content ?? '')
  }

  const list = (s: string) =>
    s
      .split(',')
      .map((x) => x.trim())
      .filter(Boolean)

  const save = async () => {
    setBusy(true)
    try {
      const view = await props.client.send<WorldView>('worlds.lore.put', {
        id: props.world.id,
        // An empty `replace` upserts by name; sending the name the entry was
        // opened under is what turns a changed name into a rename-in-place
        // rather than a second entry standing beside the first.
        replace: editing || undefined,
        entry: { name: name.trim(), keys: list(keys), constant, audience: list(audience), content: content.trim() },
      })
      props.onWorld(view)
      setEditing(null)
    } catch (e) {
      props.onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (entryName: string) => {
    if (!window.confirm(t('Delete the lore entry “%s”?', entryName))) return
    setBusy(true)
    try {
      const view = await props.client.send<WorldView>('worlds.lore.delete', { id: props.world.id, name: entryName })
      props.onWorld(view)
      if (editing === entryName) setEditing(null)
    } catch (e) {
      props.onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  // The engine's activation rule, enforced here so the button explains itself
  // instead of the server refusing after a round trip: an entry needs content,
  // and needs trigger keywords unless it is always-on.
  const complete = name.trim() !== '' && content.trim() !== '' && (constant || list(keys).length > 0)

  return (
    <div class="stage-lorebook">
      <div class="stage-lorebook__head">
        <p class="stage-hint">
          {t('Shared background every character in this World can draw on. An always-on entry is in play every turn; a keyed entry arrives when one of its keywords is mentioned.')}
        </p>
        <button class="stage-lorebook__new" disabled={busy} onClick={() => open(null)}>
          {t('+ New entry')}
        </button>
      </div>

      {lore.length === 0 && editing === null && <p class="stage-empty">{t('This World has no lore yet.')}</p>}

      <ul class="stage-lorebook__list">
        {lore.map((e) => (
          <li key={e.name} class={`stage-lorebook__row ${editing === e.name ? 'stage-lorebook__row--open' : ''}`}>
            <button class="stage-lorebook__open" onClick={() => (editing === e.name ? setEditing(null) : open(e))}>
              <span class="stage-lorebook__name">{e.name}</span>
              <span class="stage-lorebook__badges">
                {e.constant ? (
                  <span class="stage-lorebook__badge" title={t('Always in play')}>
                    {t('always on')}
                  </span>
                ) : (
                  (e.keys ?? []).map((k) => (
                    <span key={k} class="stage-lorebook__key">
                      {k}
                    </span>
                  ))
                )}
                {/* Audience (L2): who in the cast knows this. Empty = everyone. */}
                {(e.audience ?? []).length > 0 && (
                  <span class="stage-lorebook__badge" title={t('Only these characters know this')}>
                    {t('known to %s', (e.audience ?? []).join(', '))}
                  </span>
                )}
                {/* The model authored this one (W4b). A user edit takes
                    ownership and the badge goes. */}
                {e.model && <span class="stage-lorebook__badge" title={t('Written by the narrator during play')}>📝</span>}
              </span>
              <span class="stage-lorebook__snippet">{e.content}</span>
            </button>
            <button class="stage-lorebook__del" disabled={busy} title={t('Delete “%s”', e.name)} onClick={() => void remove(e.name)}>
              ✕
            </button>
          </li>
        ))}
      </ul>

      {editing !== null && (
        <form
          class="stage-lorebook__form"
          onSubmit={(ev) => {
            ev.preventDefault()
            void save()
          }}
        >
          <label class="stage-lorebook__field">
            <span>{t('Name')}</span>
            <input value={name} placeholder={t('The curfew bells')} onInput={(ev) => setName((ev.target as HTMLInputElement).value)} />
          </label>
          <label class="stage-lorebook__field stage-lorebook__field--check">
            <input type="checkbox" checked={constant} onChange={(ev) => setConstant((ev.target as HTMLInputElement).checked)} />
            <span>{t('Always in play (no keywords needed)')}</span>
          </label>
          {!constant && (
            <label class="stage-lorebook__field">
              <span>{t('Trigger keywords')}</span>
              <input value={keys} placeholder={t('curfew, bells, dusk')} onInput={(ev) => setKeys((ev.target as HTMLInputElement).value)} />
            </label>
          )}
          <label class="stage-lorebook__field">
            <span>{t('Known to')}</span>
            <input
              value={audience}
              placeholder={t('everyone — or name characters, separated by commas')}
              onInput={(ev) => setAudience((ev.target as HTMLInputElement).value)}
            />
          </label>
          <label class="stage-lorebook__field">
            <span>{t('Content')}</span>
            <textarea rows={5} value={content} onInput={(ev) => setContent((ev.target as HTMLTextAreaElement).value)} />
          </label>
          <div class="stage-lorebook__actions">
            <button type="submit" class="stage-lorebook__save" disabled={busy || !complete}>
              {t('Save entry')}
            </button>
            <button type="button" class="stage-lorebook__cancel" onClick={() => setEditing(null)}>
              {t('Cancel')}
            </button>
          </div>
        </form>
      )}
    </div>
  )
}
