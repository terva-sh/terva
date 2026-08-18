import { errText } from '../../platform/ctrlproto/errors'
import { useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { WorldLoreEntry } from '../../platform/ctrlproto/types'
import { t, tn } from '../../i18n'

// The scene-state card (SD4): one reserved World-lore entry — the pinned,
// always-in-context state note (in-fiction datetime, location, ledger facts,
// active routines). The server owns the invariants (always-on, shared,
// world_note may update it); this component owns the surface the proposal
// asked for — "rendered as a card above the composer rather than buried in
// lore". Collapsed it is one line of the current state; tapped open it shows
// the whole card with edit (world.lore.put) and unpin (world.lore.delete).

// The reserved name — must match Go core.SceneStateName. Reads tolerate any
// casing (an imported bundle may differ); writes send the canonical form and
// the server canonicalizes regardless.
export const SCENE_STATE_NAME = 'Scene state'

export function sceneStateOf(lore?: WorldLoreEntry[]): WorldLoreEntry | undefined {
  return (lore ?? []).find((e) => e.name.trim().toLowerCase() === SCENE_STATE_NAME.toLowerCase())
}

// How much scene may play before the pin is worth flagging (SD6). The card
// claims to outrank disagreeing history and nothing keeps it current on its
// own, so a card left behind quietly wins arguments it should lose — a scene
// carried across a break kept saying a visitor waited outside long after she
// had been let in, and every regenerate re-staged her arrival because the pin
// said so. Below the threshold the drift is normal play, not a problem: a
// badge on every turn is a badge nobody reads.
export const SCENE_PIN_STALE_TURNS = 6

// scenePinDrift is the number to show, or 0 for "say nothing" — a pin that is
// current, absent, or only a beat behind.
export function scenePinDrift(stale?: number): number {
  return (stale ?? 0) >= SCENE_PIN_STALE_TURNS ? (stale ?? 0) : 0
}

export function SceneStateCard(props: { client: ClientLike; sessionId: string; entry: WorldLoreEntry; stale?: number }) {
  const { client, sessionId, entry } = props
  // The drift (SD6). This card is the one the author actually looks at during
  // play, so it is where the warning belongs — the World tab is where they go
  // afterwards to ask what happened.
  const drift = scenePinDrift(props.stale)
  const [open, setOpen] = useState(false)
  // null = viewing; a string = the edit textarea's draft.
  const [draft, setDraft] = useState<string | null>(null)
  const [error, setError] = useState('')
  // View mode reads entry.content straight off the snapshot, so a mid-scene
  // model rewrite (world_note) shows up live; an open edit keeps its draft —
  // the user's typing outranks a background rewrite until save or cancel.

  const save = () => {
    const content = (draft ?? '').trim()
    if (!content) return
    client
      .send('world.lore.put', { entry: { name: SCENE_STATE_NAME, constant: true, content } }, sessionId)
      .then(() => setDraft(null))
      .catch((e: unknown) => setError(errText(e)))
  }
  const unpin = () => {
    if (!window.confirm(t('Unpin the scene state? The card is removed from context; the scene itself is untouched.'))) return
    client.send('world.lore.delete', { name: entry.name }, sessionId).catch((e: unknown) => setError(errText(e)))
  }

  const firstLine = entry.content.split('\n', 1)[0]
  const staleTitle = t('This card outranks the scene when they disagree, and nothing updates it on its own — check it against what has happened since, then doctor this session to refresh it.')
  if (!open) {
    return (
      <button
        class={'stage-scene-state stage-scene-state--closed' + (drift > 0 ? ' stage-scene-state--stale' : '')}
        title={drift > 0 ? staleTitle : t('Scene state — pinned in context every turn. Tap to view or edit.')}
        onClick={() => setOpen(true)}
      >
        <span class="stage-scene-state__pin">📌</span>
        <span class="stage-scene-state__line">{firstLine}</span>
        {drift > 0 && <span class="stage-scene-state__stale">{tn(drift, '%d turn behind', '%d turns behind')}</span>}
      </button>
    )
  }
  return (
    <div class="stage-scene-state stage-scene-state--open">
      <div class="stage-scene-state__head">
        <span class="stage-scene-state__pin">📌</span>
        <span class="stage-scene-state__title">{t('Scene state')}</span>
        {drift > 0 && (
          <span class="stage-scene-state__stale" title={staleTitle}>
            {tn(drift, '%d turn behind', '%d turns behind')}
          </span>
        )}
        {entry.model && (
          <span class="stage-scene-state__model" title={t('Last written by the model during play (world_note)')}>
            📝
          </span>
        )}
        <button class="stage-scene-state__act" title={t('Collapse')} onClick={() => { setOpen(false); setDraft(null) }}>
          ▾
        </button>
      </div>
      {draft === null ? (
        <>
          <pre class="stage-scene-state__content">{entry.content}</pre>
          <div class="stage-scene-state__acts">
            <button onClick={() => setDraft(entry.content)}>{t('✎ Edit')}</button>
            <button class="stage-scene-state__unpin" onClick={unpin}>{t('Unpin')}</button>
          </div>
        </>
      ) : (
        <>
          <textarea
            class="stage-scene-state__editor"
            rows={Math.min(8, Math.max(3, draft.split('\n').length + 1))}
            value={draft}
            onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
          />
          <div class="stage-scene-state__acts">
            <button class="stage-scene-state__save" disabled={!draft.trim()} onClick={save}>
              {t('Save')}
            </button>
            <button onClick={() => setDraft(null)}>{t('Cancel')}</button>
          </div>
        </>
      )}
      {error && <p class="stage-scene-state__error">{error}</p>}
    </div>
  )
}
