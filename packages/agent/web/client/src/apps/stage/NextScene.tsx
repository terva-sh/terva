import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { NextSceneResult } from '../../platform/ctrlproto/types'
import { t } from '../../i18n'
import { ModelPick } from './ModelPick'

// Start the next scene (SD5): end this scene at a chapter boundary and open a
// fresh one in the same World. Dramaturgi drafts three things — a title, the
// story-so-far recap, and the cold open — and every one of them is editable
// before anything is created. The draft costs a model call; the commit costs
// nothing and is the only step that makes a scene.
//
// What crosses the boundary is structure, not prose: the roster, the World
// lore, the pinned scene-state card, the backdrop, and the player's persona
// are carried by the daemon. The recap lands as always-on lore; the cold open
// stands in for the card greeting.

export function NextSceneSheet(props: {
  client: ClientLike
  sessionId: string
  // The doctor's scene_break proposal seeds the title when it opens this.
  suggestedTitle?: string
  // The session's provider/model, so the picker can name what the draft's
  // default ("Session model") resolves to.
  defaultProvider?: string
  defaultModel?: string
  onOpenSession: (session: string) => void
  onClose: () => void
}) {
  const { client, sessionId } = props
  const [drafting, setDrafting] = useState(true)
  const [committing, setCommitting] = useState(false)
  const [note, setNote] = useState('')
  const [title, setTitle] = useState(props.suggestedTitle ?? '')
  const [summary, setSummary] = useState('')
  const [opening, setOpening] = useState('')
  const [error, setError] = useState('')
  // The World this story is in, reported by the draft. An id means the next
  // scene simply joins it and there is nothing to ask. No id means the two
  // scenes would be grouped by nothing — so we offer to promote one, prefilled
  // with the daemon's suggestion, on by default because a story told across
  // scenes is what a World is FOR.
  const [worldID, setWorldID] = useState('')
  const [worldName, setWorldName] = useState('')
  const [groupWorld, setGroupWorld] = useState(true)
  // Per-generation model override for the draft; empty = the session model (the
  // daemon default). The commit spends no model, so only the draft carries it.
  const [ovProvider, setOvProvider] = useState('')
  const [ovModel, setOvModel] = useState('')

  const draft = () => {
    setDrafting(true)
    setError('')
    client
      .send<NextSceneResult>('sessions.next_scene', { provider: ovProvider, model: ovModel }, sessionId)
      .then((r) => {
        // A re-draft replaces the fields wholesale — the author asked for a
        // different take, so keeping their edits would blend two drafts.
        setTitle(r.title ?? '')
        setSummary(r.summary ?? '')
        setOpening(r.opening ?? '')
        setNote(r.note ?? '')
        setWorldID(r.world_id ?? '')
        setWorldName(r.world_name ?? '')
      })
      .catch((e: unknown) => setError(errText(e)))
      .finally(() => setDrafting(false))
  }
  useEffect(() => {
    draft()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const commit = () => {
    setCommitting(true)
    setError('')
    client
      .send<NextSceneResult>(
        'sessions.next_scene',
        {
          commit: true,
          title: title.trim(),
          summary: summary.trim(),
          opening: opening.trim(),
          // Only meaningful when there is no World yet; the daemon ignores it
          // otherwise, and an unticked box declines the grouping entirely.
          world: !worldID && groupWorld ? worldName.trim() : '',
        },
        sessionId,
      )
      .then((r) => {
        if (r.session?.id) props.onOpenSession(r.session.id)
        props.onClose()
      })
      .catch((e: unknown) => setError(errText(e)))
      .finally(() => setCommitting(false))
  }

  // An unnamed World is refused by the daemon, so gate on it here rather than
  // letting a commit either fail or — worse — quietly skip the grouping the
  // author left ticked.
  const worldReady = worldID !== '' || !groupWorld || worldName.trim() !== ''
  const ready = title.trim() !== '' && summary.trim() !== '' && opening.trim() !== '' && worldReady
  return (
    <div class="stage-sheet-backdrop" onClick={props.onClose}>
      <div class="stage-nextscene" onClick={(e) => e.stopPropagation()}>
        <header class="stage-nextscene__head">
          <h3>{t('Start the next scene')}</h3>
          <button class="stage-drawer__close" onClick={props.onClose}>
            ✕
          </button>
        </header>
        <p class="stage-hint">
          {t('This scene stays as it is. The next one opens in the same world — same cast, same lore, same scene-state card — with the recap below as always-on knowledge and the cold open as its first beat.')}
        </p>
        {drafting ? (
          <p class="stage-hint">{t('Reading the scene…')}</p>
        ) : (
          <>
            {note && <p class="stage-doctor__note">{note}</p>}
            <label class="stage-nextscene__field">
              <span>{t('Title of the next scene')}</span>
              <input value={title} onInput={(e) => setTitle((e.target as HTMLInputElement).value)} />
            </label>
            <label class="stage-nextscene__field">
              <span>{t('The story so far — carried as always-on lore')}</span>
              <textarea rows={6} value={summary} onInput={(e) => setSummary((e.target as HTMLTextAreaElement).value)} />
            </label>
            <label class="stage-nextscene__field">
              <span>{t('Cold open — the first beat of the new scene')}</span>
              <textarea rows={6} value={opening} onInput={(e) => setOpening((e.target as HTMLTextAreaElement).value)} />
            </label>
            {/* A story already in a World needs no question — say where the
                scene is going and move on. A story in none is one commit away
                from being two sessions with nothing tying them together, which
                is the only case worth an offer. */}
            {worldID ? (
              <p class="stage-hint">{t('Both scenes belong to the World “%s”.', worldName)}</p>
            ) : (
              <div class="stage-nextscene__world">
                <label class="stage-nextscene__group">
                  <input type="checkbox" checked={groupWorld} onChange={(e) => setGroupWorld((e.target as HTMLInputElement).checked)} />
                  {t('Keep these scenes together as a World')}
                </label>
                {groupWorld && (
                  <>
                    <input
                      class="stage-nextscene__worldname"
                      value={worldName}
                      placeholder={t('Name this World')}
                      onInput={(e) => setWorldName((e.target as HTMLInputElement).value)}
                    />
                    <p class="stage-hint">
                      {t('This scene and the next one both join it, and its roster, lore, and pins are saved to your World shelf.')}
                    </p>
                  </>
                )}
              </div>
            )}
          </>
        )}
        {/* Which model drafts the scene — the session's by default, or a per-run
            override. Takes effect on the next "Draft again". */}
        <ModelPick
          client={client}
          sessionId={sessionId}
          currentProvider={ovProvider}
          currentModel={ovModel}
          onSelect={(p, m) => {
            setOvProvider(p)
            setOvModel(m)
          }}
          defaultLabel={t('Session model')}
          defaultProvider={props.defaultProvider}
          defaultModel={props.defaultModel}
        />
        <div class="stage-nextscene__acts">
          <button class="stage-nextscene__go" disabled={drafting || committing || !ready} onClick={commit}>
            {committing ? t('Opening…') : t('Start the scene')}
          </button>
          <button disabled={drafting || committing} onClick={draft}>
            {t('Draft again')}
          </button>
          <button disabled={committing} onClick={props.onClose}>
            {t('Cancel')}
          </button>
        </div>
        {error && <p class="stage-doctor__error">{error}</p>}
      </div>
    </div>
  )
}
