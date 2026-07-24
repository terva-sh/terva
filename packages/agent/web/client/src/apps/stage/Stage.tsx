import { useEffect, useRef, useState } from 'preact/hooks'
import { Client } from '../../platform/ctrlproto/client'
import type { CardSummary, CatalogView, Status } from '../../platform/ctrlproto/types'
import { setLocale, applyServerCatalog, t } from '../../i18n'
import { takeNavParams } from '../../ui/navlinks'
import { Library } from './Library'
import { Chat } from './Chat'
import { CharacterStudio } from './CharacterStudio'
import type { StudioTab } from './CharacterStudio'
import type { ScenePersona } from './YouEditor'

// The Stage app owns one shared platform Client and switches between three screens
// with plain state — no path routing, so nothing depends on the /stage/ path depth
// (the embedded app serves assets relative to /stage/). The library is the
// landing; a chat is opened over it and dismissed back to it.
//
// The one exception is the `?session=` deep link the panel writes, read ONCE at
// boot (see ui/navlinks). It only chooses the initial screen — Stage does not
// keep the URL in sync afterwards, because the two screens are not a history a
// back button should walk.
//
// The character studio is the third: one editor with one lifecycle, which both
// the library and a live scene navigate INTO rather than each mounting their own
// sheet over themselves. It carries `back` because the way out depends on the way
// in — ✎ from a scene returns to that scene, ✎ from the library to the library —
// and `card: null` means a character being created rather than edited.
//
// `tab` is which half of the studio the trip is FOR: ✎ on a character opens the
// character, "playing as …" in a scene header opens you. `scene` is the session
// whose persona the You tab steers, carried down because a cold sessions.list row
// has no user fields — only the live chat knows who you are in it.
type View =
  | { screen: 'library' }
  | { screen: 'chat'; session: string }
  | { screen: 'character'; card: CardSummary | null; tab: StudioTab; scene: ScenePersona | null; back: View }

// Read before first render so the initial view is the linked chat, never a
// flash of the library. takeNavParams also strips the param from the address
// bar, so this is deliberately module-scope: it must run exactly once.
const bootNav = takeNavParams()

export function Stage() {
  const [status, setStatus] = useState<Status>('connecting')
  const [ready, setReady] = useState(false)
  // generation counts CONNECTIONS, not renders. Every hello — the first and every
  // one after a reconnect — bumps it, and the chat's subscription effect depends on
  // it. Server-side subscriptions live and die with the socket, so without this a
  // reconnected Stage held a subscription the daemon no longer had: turns still ran
  // (the socket was open) but nothing came back, so neither the user's message nor
  // the reply ever rendered and only a reload recovered.
  const [generation, setGeneration] = useState(0)
  // i18nEpoch re-renders the tree when the server catalog lands, WITHOUT
  // touching generation — generation counts connections and re-subscribes the
  // chat, which a translation refresh must never do.
  const [, setI18nEpoch] = useState(0)
  const [view, setView] = useState<View>(() => {
    // A ?card= link lands straight in the studio; the way out is the library,
    // because there is no scene behind it to return to.
    if (bootNav.card) {
      return { screen: 'character', card: { id: bootNav.card, name: '', greetings: 0 }, tab: 'character', scene: null, back: { screen: 'library' } }
    }
    return bootNav.session ? { screen: 'chat', session: bootNav.session } : { screen: 'library' }
  })

  const clientRef = useRef<Client | null>(null)
  if (!clientRef.current) clientRef.current = new Client()
  const client = clientRef.current

  useEffect(() => {
    client.onStatus = setStatus
    client.onReady = (hello) => {
      // Match the bundled catalog to the daemon's active language for an
      // instant first paint, then overlay the server's effective catalog
      // (embedded web ⊕ stage ⊕ $TERVA_HOME overlays) — same two-step as the
      // panel. A locale changed mid-run lands on the next reconnect/reload
      // (Stage holds no workspace subscription to hear locale_changed live;
      // the panel does).
      document.documentElement.lang = setLocale(hello?.locale ?? 'en')
      client
        .send<{ catalog: CatalogView }>('i18n.catalog', {}, '')
        .then((r) => {
          if (!r?.catalog) return
          document.documentElement.lang = applyServerCatalog(r.catalog)
          setI18nEpoch((e) => e + 1)
        })
        .catch(() => {
          /* older server / offline — the bundle stands in */
        })
      setReady(true)
      setGeneration((g) => g + 1)
    }
    client.connect()
    return () => client.close()
  }, [client])

  if (view.screen === 'character') {
    const back = view.back
    return (
      <CharacterStudio
        // Key by id so opening a different character resets the form rather
        // than carrying the previous one's unsaved edits into it. A new
        // character keys on 'new', which is stable for the whole creation.
        // NOT keyed by tab: switching tabs must keep both panes' work alive.
        key={view.card?.id ?? 'new'}
        client={client}
        ready={ready}
        card={view.card}
        tab={view.tab}
        onTab={(tab) => setView({ ...view, tab })}
        scene={view.scene}
        backLabel={back.screen === 'chat' ? t('Scene') : t('Library')}
        onBack={() => setView(back)}
        // Adopt the minted id, so a second save edits this character instead of
        // importing another copy of it, and so Close lands somewhere real.
        onCreated={(id) => setView({ ...view, card: { id, name: '', greetings: 0 } })}
      />
    )
  }

  if (view.screen === 'chat') {
    // key by session so a branch (which switches to a new session) remounts the
    // chat with clean local state instead of carrying the old draft/edit over.
    return (
      <Chat
        key={view.session}
        client={client}
        sessionId={view.session}
        generation={generation}
        onBack={() => setView({ screen: 'library' })}
        onOpenSession={(session) => setView({ screen: 'chat', session })}
        onOpenStudio={(o) => setView({ screen: 'character', card: o.card, tab: o.tab, scene: o.scene, back: { screen: 'chat', session: view.session } })}
      />
    )
  }
  return (
    <Library
      client={client}
      ready={ready}
      status={status}
      onOpenChat={(session) => setView({ screen: 'chat', session })}
      onEditCharacter={(card) => setView({ screen: 'character', card, tab: 'character', scene: null, back: { screen: 'library' } })}
      // No scene behind it, so the You tab edits the saved library rather than
      // steering a session.
      onEditYou={() => setView({ screen: 'character', card: null, tab: 'you', scene: null, back: { screen: 'library' } })}
    />
  )
}
