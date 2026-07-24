import { CardEditor } from './CardEditor'
import { YouEditor } from './YouEditor'
import type { ScenePersona } from './YouEditor'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { CardSummary } from '../../platform/ctrlproto/types'

export type StudioTab = 'character' | 'you'

// The character studio: the one place a character is made and changed — and, on
// its second tab, the one place you are.
//
// Editing used to happen in a sheet thrown over whichever surface asked for it —
// the Library in one shape, a live session in another — and the two hosts did not
// agree on what a save meant. One of them closed the editor on every save, which
// tore down a doctor consultation in progress and looked, from the seat, like the
// page reloading.
//
// A screen fixes that structurally rather than by convention: there is one editor
// with one lifecycle, and both surfaces NAVIGATE here instead of each mounting
// their own copy with their own idea of when it should go away. `back` is where
// the trip returns to, so ✎ from a scene comes back to that scene and ✎ from the
// Library comes back to the Library.
//
// The two tabs are the two halves of a story: who you play WITH and who you play
// AS. Both panes stay MOUNTED and the inactive one is hidden, because a tab reads
// as a lighter act than leaving — unmounting would throw away an unsaved card
// draft, or a doctor consultation mid-negotiation, on a click that promised only
// to look elsewhere.
export function CharacterStudio(props: {
  client: ClientLike
  // The socket. A ?card= deep link renders this screen on the FIRST paint,
  // before the client has connected, and the editor's opening cards.get would
  // land on a closed socket — "not connected" where the character should be.
  // Every other Stage screen already waits; this one has to as well.
  ready: boolean
  // The character being edited, or null to create one.
  card: CardSummary | null
  tab: StudioTab
  onTab: (tab: StudioTab) => void
  // The scene this was opened from, if any — with one, the You tab steers that
  // session's persona live; without one it edits the saved library.
  scene: ScenePersona | null
  backLabel: string
  onBack: () => void
  // Fired when a new character's first save mints an id, so the host can adopt
  // it — otherwise a second save would import a second copy.
  onCreated?: (id: string) => void
  onSaved?: () => void
}) {
  const characterTab = props.card?.name || (props.card ? t('Character') : t('New character'))
  return (
    <div class="stage stage-studio">
      <header class="stage-topbar">
        <button class="stage-back" onClick={props.onBack}>
          {t('‹ %s', props.backLabel)}
        </button>
        {/* i18n-exempt — the terva Stage wordmark, a product name */}
        <h1 class="stage-brand">terva Stage</h1>
      </header>
      <nav class="stage-studio__tabs">
        <button
          class={`stage-studio__tab ${props.tab === 'character' ? 'stage-studio__tab--on' : ''}`}
          onClick={() => props.onTab('character')}
        >
          {characterTab}
        </button>
        <button class={`stage-studio__tab ${props.tab === 'you' ? 'stage-studio__tab--on' : ''}`} onClick={() => props.onTab('you')}>
          {t('You')}
        </button>
      </nav>
      <div class="stage-studio__body">
        <div class="stage-studio__pane" hidden={props.tab !== 'character'}>
          {!props.ready && props.card ? (
            <p class="stage-studio__waiting">{t('connecting…')}</p>
          ) : (
            <CardEditor
              client={props.client}
              card={props.card}
              onClose={props.onBack}
              onCreated={props.onCreated}
              onSaved={() => props.onSaved?.()}
            />
          )}
        </div>
        <div class="stage-studio__pane" hidden={props.tab !== 'you'}>
          <YouEditor client={props.client} ready={props.ready} scene={props.scene} />
        </div>
      </div>
    </div>
  )
}
