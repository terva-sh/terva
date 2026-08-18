import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import { ModelPick } from './ModelPick'
import { WorldLoreEditor } from './WorldLoreEditor'
import { worldDeleteWarning } from './Library'
import { WorldDoctor } from './WorldDoctor'
import { relativeTime } from './format'
import { t } from '../../i18n'
import { downloadExport, fileToBase64 } from '../../ui/browser'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type {
  CardSummary,
  CardsListResult,
  CreateOpts,
  DefaultForResult,
  ParamsFor,
  SessionInfo,
  Verb,
  WorldExport,
  WorldView,
  WorldsListResult,
} from '../../platform/ctrlproto/types'

export type WorldTab = 'roster' | 'lore' | 'scenes' | 'doctor'

type SessionsResult = { sessions: SessionInfo[] }

// The World studio: one screen for everything a World IS.
//
// A World used to be a drawer thrown over the Library — a read-mostly sheet with
// a roster you could not change, a lorebook you could not reach, and a settings
// knob that only existed inside a session. That shape said a World is a property
// of the shelf. It is not: it is an authoring subject like a character, with its
// own cast, its own background, and its own history of scenes.
//
// So it gets what the character studio has, and for the same reasons
// (CharacterStudio.tsx): one screen, one lifecycle, `back` remembering the way
// in, and every pane staying MOUNTED with the inactive one hidden — switching
// tabs reads as a lighter act than leaving, and unmounting would throw away a
// half-typed lore entry on a click that promised only to look elsewhere.
//
// Everything here writes through the sessionless `worlds.*` verbs, so nothing on
// this screen needs a scene open. A member session keeps its own working copy
// untouched — edits here seed the NEXT session started in the World, which is
// the same explicit never-live sync the store has always documented.
export function WorldStudio(props: {
  client: ClientLike
  ready: boolean
  id: string
  tab: WorldTab
  onTab: (tab: WorldTab) => void
  backLabel: string
  onBack: () => void
  onOpenChat: (session: string) => void
  onEditCharacter: (card: CardSummary) => void
}) {
  const [world, setWorld] = useState<WorldView | null>(null)
  const [cards, setCards] = useState<CardSummary[]>([])
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Metadata editing (name/description), the one part lifted verbatim from the
  // old drawer — it already wrote through the sessionless worlds.update.
  const [editMeta, setEditMeta] = useState(false)
  const [metaName, setMetaName] = useState('')
  const [metaDesc, setMetaDesc] = useState('')
  // The add-a-character form: which library card, and under what roster name.
  const [addRef, setAddRef] = useState('')
  const [addName, setAddName] = useState('')
  // What an unpinned pick on this screen actually RESOLVES to, keyed by roster
  // card ref ('' for the World's own row). Every picker here offers an inherit
  // option, and "Inherit" that will not say from what is the least useful thing
  // a settings row can print — especially now that inheritance has three rungs
  // (card → world → workspace) rather than one. Asked of models.default_for,
  // the daemon's single authority, so this screen never re-derives the ladder
  // and cannot disagree with the session it starts. A daemon too old to serve
  // the verb leaves the map empty and the labels fall back to bare "Inherit".
  const [inherits, setInherits] = useState<Record<string, DefaultForResult>>({})

  useEffect(() => {
    if (!props.ready) return
    // There is no worlds.get — the list already resolves member-session counts
    // for the shelf, and a World library is small enough that finding one in it
    // costs less than a verb whose only caller would be this screen.
    props.client
      .send<WorldsListResult>('worlds.list', {})
      .then((r) => setWorld((r.worlds ?? []).find((w) => w.id === props.id) ?? null))
      .catch((e: unknown) => setError(errText(e)))
      .finally(() => setLoaded(true))
    props.client
      .send<CardsListResult>('cards.list', {})
      .then((r) => setCards(r.cards ?? []))
      .catch(() => {})
    props.client
      .send<SessionsResult>('sessions.list', {})
      .then((r) => setSessions(r.sessions ?? []))
      .catch(() => {})
  }, [props.client, props.id, props.ready])

  const cardFor = (ref: string) => cards.find((c) => c.id === ref)
  const roster = Object.entries(world?.characters ?? {})
  const scenes = sessions.filter((s) => s.world === props.id)

  // Re-resolve the inherit labels whenever the World changes — a write answers
  // with the stored view, so setting the World default here must move every
  // roster row's "Inherit" in the same beat. The World's OWN row asks with
  // neither card nor world: it wants the rung BELOW itself, and asking with the
  // world id would just hand back the pin it is already showing.
  const rosterRefs = roster.map(([, ref]) => ref)
  useEffect(() => {
    if (!props.ready || !world) return
    let live = true
    const ask = (key: string, params: { card?: string; world?: string }) =>
      props.client
        .send<DefaultForResult>('models.default_for', params)
        .then((r) => {
          if (live) setInherits((prev) => ({ ...prev, [key]: r }))
        })
        .catch(() => {})
    void ask('', {})
    // ':world' is the doctor's rung: a sessionless run resolves the World's own
    // default when it has one and the workspace's otherwise, which is a
    // different answer from either the World row's or a character's. The key is
    // punctuated so it can never collide with a card ref.
    void ask(':world', { world: world.id })
    for (const ref of new Set(rosterRefs)) void ask(ref, { card: ref, world: world.id })
    return () => {
      live = false
    }
  }, [props.client, props.ready, world])

  // Every sessionless world verb answers with the stored view, so a write
  // re-renders from its own result rather than a re-list that could race it.
  // Typed through Verb/ParamsFor so a wrong params shape is a compile error
  // here rather than a runtime bad-request.
  const write = async <M extends Verb>(verb: M, params: ParamsFor<M>) => {
    setBusy(true)
    setError('')
    try {
      setWorld(await props.client.send<WorldView, M>(verb, params))
    } catch (e) {
      setError(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const startIn = async (experience: 'chat' | 'play', cardRef?: string) => {
    setBusy(true)
    setError('')
    try {
      const res = await props.client.send<{ session: SessionInfo }>('sessions.create', {
        experience,
        world: props.id,
        ...(cardRef ? { card: cardRef } : {}),
      } as CreateOpts)
      props.onOpenChat(res.session.id)
    } catch (e) {
      setError(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (!loaded || !props.ready) {
    return (
      <div class="stage stage-studio">
        <header class="stage-topbar">
          <button class="stage-back" onClick={props.onBack}>
            {t('‹ %s', props.backLabel)}
          </button>
          {/* i18n-exempt — the terva Stage wordmark, a product name */}
          <h1 class="stage-brand">terva Stage</h1>
        </header>
        <p class="stage-studio__waiting">{t('connecting…')}</p>
      </div>
    )
  }

  if (!world) {
    return (
      <div class="stage stage-studio">
        <header class="stage-topbar">
          <button class="stage-back" onClick={props.onBack}>
            {t('‹ %s', props.backLabel)}
          </button>
          {/* i18n-exempt — the terva Stage wordmark, a product name */}
          <h1 class="stage-brand">terva Stage</h1>
        </header>
        <p class="stage-empty">{t('This World is no longer in your library.')}</p>
      </div>
    )
  }

  return (
    <div class="stage stage-studio stage-worldstudio">
      <header class="stage-topbar">
        <button class="stage-back" onClick={props.onBack}>
          {t('‹ %s', props.backLabel)}
        </button>
        {/* i18n-exempt — the terva Stage wordmark, a product name */}
        <h1 class="stage-brand">terva Stage</h1>
      </header>

      {error && <p class="stage-error">{error}</p>}

      <section class="stage-worldstudio__head">
        {world.cover_url && <img class="stage-worldstudio__cover" src={world.cover_url} alt="" />}
        {editMeta ? (
          <form
            class="stage-worldedit"
            onSubmit={(e) => {
              e.preventDefault()
              void write('worlds.update', { id: world.id, name: metaName.trim(), description: metaDesc.trim() }).then(() => setEditMeta(false))
            }}
          >
            <input
              class="stage-worldedit__name"
              placeholder={t('World name')}
              value={metaName}
              onInput={(e) => setMetaName((e.target as HTMLInputElement).value)}
            />
            <textarea
              class="stage-worldedit__desc"
              placeholder={t('What is this World? (shown on the shelf)')}
              value={metaDesc}
              onInput={(e) => setMetaDesc((e.target as HTMLTextAreaElement).value)}
            />
            <div class="stage-worldedit__actions">
              <button type="submit" class="stage-worldedit__save" disabled={!metaName.trim()}>
                {t('Save')}
              </button>
              <button type="button" class="stage-worldedit__cancel" onClick={() => setEditMeta(false)}>
                {t('Cancel')}
              </button>
            </div>
          </form>
        ) : (
          <div class="stage-worldstudio__title">
            <h2>
              🌍 {world.name}
              <button
                class="stage-worldlore__act"
                title={t('Rename or describe this World')}
                onClick={() => {
                  setMetaName(world.name)
                  setMetaDesc(world.description ?? '')
                  setEditMeta(true)
                }}
              >
                ✎
              </button>
            </h2>
            {world.description && <p class="stage-hint">{world.description}</p>}
          </div>
        )}
      </section>

      <nav class="stage-studio__tabs">
        <button class={`stage-studio__tab ${props.tab === 'roster' ? 'stage-studio__tab--on' : ''}`} onClick={() => props.onTab('roster')}>
          {t('Cast (%d)', roster.length)}
        </button>
        <button class={`stage-studio__tab ${props.tab === 'lore' ? 'stage-studio__tab--on' : ''}`} onClick={() => props.onTab('lore')}>
          {t('Lore (%d)', (world.lore ?? []).length)}
        </button>
        <button class={`stage-studio__tab ${props.tab === 'scenes' ? 'stage-studio__tab--on' : ''}`} onClick={() => props.onTab('scenes')}>
          {t('Scenes (%d)', scenes.length)}
        </button>
        <button class={`stage-studio__tab ${props.tab === 'doctor' ? 'stage-studio__tab--on' : ''}`} onClick={() => props.onTab('doctor')}>
          {t('🩺 Doctor')}
        </button>
      </nav>

      <div class="stage-studio__body">
        <div class="stage-studio__pane" hidden={props.tab !== 'roster'}>
          {roster.length === 0 && <p class="stage-empty">{t('No characters in this World yet.')}</p>}
          <ul class="stage-worldroster">
            {roster.map(([name, ref]) => {
              const card = cardFor(ref)
              return (
                <li key={name} class="stage-worldroster__row">
                  {card?.avatar_url ? (
                    <img class="stage-worldroster__face" src={card.avatar_url} alt="" />
                  ) : (
                    <span class="stage-worldroster__face stage-worldroster__face--none" />
                  )}
                  <span class="stage-worldroster__who">
                    <span class="stage-worldroster__name">{name}</span>
                    {/* A roster entry whose card has left the library is a part
                        that cannot be cast. Say so here rather than letting the
                        next session fail to build. */}
                    {!card && <span class="stage-worldroster__missing">{t('card missing from your library')}</span>}
                    {/* This World's own version of the character — a fork made
                        so an edit here did not rewrite them everywhere else.
                        Said plainly, because the author needs to know which
                        copy they are editing. */}
                    {card?.variant_of === world.id && (
                      <span class="stage-worldroster__variant" title={t('Edits here stay in this World')}>
                        {t('this World’s own version')}
                      </span>
                    )}
                  </span>
                  <ModelPick
                    client={props.client}
                    sessionId=""
                    currentProvider={world.character_models?.[name]?.provider}
                    currentModel={world.character_models?.[name]?.model}
                    onSelect={(p, m) => void write('worlds.set_character_model', { id: world.id, character: name, provider: p, model: m })}
                    defaultLabel={t('Inherit')}
                    defaultProvider={inherits[ref]?.provider}
                    defaultModel={inherits[ref]?.model}
                  />
                  {card && (
                    <button class="stage-worldroster__act" title={t('Edit %s', name)} onClick={() => props.onEditCharacter(card)}>
                      ✎
                    </button>
                  )}
                  <button class="stage-worldroster__act" disabled={busy} title={t('New chat with %s', name)} onClick={() => void startIn('chat', ref)}>
                    {t('Chat')}
                  </button>
                  <button
                    class="stage-worldroster__act stage-worldroster__act--drop"
                    disabled={busy}
                    title={t('Take %s out of this World', name)}
                    onClick={() => {
                      if (window.confirm(t('Take %s off this World’s cast? Their card stays in your library.', name))) {
                        void write('worlds.remove_character', { id: world.id, name })
                      }
                    }}
                  >
                    ✕
                  </button>
                </li>
              )
            })}
          </ul>

          <form
            class="stage-worldroster__add"
            onSubmit={(e) => {
              e.preventDefault()
              void write('worlds.add_character', { id: world.id, name: addName.trim(), ref: addRef }).then(() => {
                setAddRef('')
                setAddName('')
              })
            }}
          >
            <select
              value={addRef}
              onChange={(e) => {
                const ref = (e.target as HTMLSelectElement).value
                setAddRef(ref)
                // The roster name defaults to the card's, and stays editable —
                // a World can cast the same card as "the innkeeper".
                if (!addName.trim()) setAddName(cardFor(ref)?.name ?? '')
              }}
            >
              <option value="">{t('Add a character…')}</option>
              {cards.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
            <input
              placeholder={t('Their name in this World')}
              value={addName}
              onInput={(e) => setAddName((e.target as HTMLInputElement).value)}
            />
            <button type="submit" disabled={busy || !addRef || !addName.trim()}>
              {t('Add')}
            </button>
          </form>
        </div>

        <div class="stage-studio__pane" hidden={props.tab !== 'lore'}>
          <WorldLoreEditor client={props.client} world={world} onWorld={setWorld} onError={setError} />
        </div>

        <div class="stage-studio__pane" hidden={props.tab !== 'scenes'}>
          {scenes.length === 0 && <p class="stage-empty">{t('No scenes have been played in this World yet.')}</p>}
          <ul class="stage-yourchats">
            {scenes.map((s) => (
              <li key={s.id} class="stage-yourchats__row">
                <button class="stage-yourchats__item" onClick={() => props.onOpenChat(s.id)}>
                  <span class="stage-yourchats__text">
                    <span class="stage-yourchats__title">{s.title || t('Untitled')}</span>
                    <span class="stage-yourchats__sub">{(s.messages ?? 0) > 0 && <span>{t('%d msg', s.messages ?? 0)}</span>}</span>
                  </span>
                  {relativeTime(s.updated) && <span class="stage-yourchats__when">{relativeTime(s.updated)}</span>}
                </button>
              </li>
            ))}
          </ul>
        </div>

        {/* The doctor stays MOUNTED like every other pane: a consultation
            mid-negotiation must survive a click that only promised to look at
            the cast. */}
        <div class="stage-studio__pane" hidden={props.tab !== 'doctor'}>
          <WorldDoctor
            client={props.client}
            world={world}
            scenes={scenes}
            onWorld={setWorld}
            defaultProvider={inherits[':world']?.provider}
            defaultModel={inherits[':world']?.model}
          />
        </div>
      </div>

      <section class="stage-worldstudio__foot">
        {/* The World's own default model — the middle rung of card → world →
            workspace. It sits beside coordination because it answers the same
            kind of question: not "what happens in this scene" but "what does
            every scene started here begin from". A character with a pin of
            their own still overrides it; the Default row clears back to the
            workspace model, which the label names outright. */}
        <div class="stage-worldstudio__coord stage-worldstudio__model">
          <span>{t('Model for this World')}</span>
          <ModelPick
            client={props.client}
            sessionId=""
            currentProvider={world.model?.provider}
            currentModel={world.model?.model}
            onSelect={(p, m) => void write('worlds.set_model', { id: world.id, provider: p, model: m })}
            defaultLabel={t('Workspace default')}
            defaultProvider={inherits['']?.provider}
            defaultModel={inherits['']?.model}
          />
        </div>
        {/* Coordination (W3) is a World setting, not a session knob: it decides
            who answers a normal turn in every scene started here. */}
        <label class="stage-worldstudio__coord">
          <span>{t('Who answers a turn')}</span>
          <select value={world.coordination ?? ''} disabled={busy} onChange={(e) => void write('worlds.set', { id: world.id, coordination: (e.target as HTMLSelectElement).value })}>
            <option value="">{t('Whoever fits the moment')}</option>
            <option value="off">{t('The character you are talking to')}</option>
            {roster.map(([name]) => (
              <option key={name} value={`focus:${name}`}>
                {t('Always %s', name)}
              </option>
            ))}
          </select>
        </label>
        <div class="stage-worldsheet__actions">
          {roster.length > 0 && (
            <button class="stage-worldsheet__play" disabled={busy} title={t('Run a play session here — the whole cast on stage, you direct')} onClick={() => void startIn('play')}>
              {t('🎭 Play here')}
            </button>
          )}
          <button
            class="stage-worldsheet__export"
            title={t("Download this World as a bundle — its characters' cards ride along")}
            onClick={() => {
              props.client
                .send<WorldExport>('worlds.export', { id: world.id })
                .then(downloadExport)
                .catch((e: unknown) => setError(errText(e)))
            }}
          >
            {t('⬇ Export bundle')}
          </button>
          <label class="stage-worldsheet__coverbtn">
            🖼 {world.cover_url ? t('Change cover') : t('Set cover')}
            <input
              type="file"
              accept="image/*"
              hidden
              onChange={(e) => {
                const f = (e.target as HTMLInputElement).files?.[0]
                if (!f) return
                void fileToBase64(f).then((b64) => write('worlds.update', { id: world.id, description: world.description ?? '', cover: b64 }))
              }}
            />
          </label>
          {world.cover_url && (
            <button
              class="stage-worldsheet__coverbtn"
              onClick={() => void write('worlds.update', { id: world.id, description: world.description ?? '', remove_cover: true })}
            >
              {t('Remove cover')}
            </button>
          )}
        </div>
        <button
          class="stage-worldsheet__delete"
          onClick={() => {
            if (!window.confirm(worldDeleteWarning(world.name))) return
            props.client
              .send('worlds.delete', { id: world.id })
              .then(() => props.onBack())
              .catch((e: unknown) => setError(errText(e)))
          }}
        >
          {t('Delete this World')}
        </button>
      </section>
    </div>
  )
}
