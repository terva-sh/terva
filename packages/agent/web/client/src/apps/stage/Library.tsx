import { useEffect, useState } from 'preact/hooks'
import { THEMES, applyTheme, currentTheme } from './theme'
import { CardSheet } from './CardSheet'
import { CardEditor } from './CardEditor'
import { PersonaSheet } from './PersonaSheet'
import { PersonaEditor } from './PersonaEditor'
import { CharacterChats } from './CharacterChats'
import { relativeTime } from './format'
import type { Client } from '../../platform/ctrlproto/client'
import type {
  Status,
  CardSummary,
  CardView,
  PersonaSummary,
  SessionInfo,
  CardsListResult,
  PersonasListResult,
  CreateOpts,
  WorldView,
  WorldsListResult,
  WorldExport,
  WorldUpdateParams,
} from '../../platform/ctrlproto/types'

type SessionsResult = { sessions: SessionInfo[] }

// Base64-encode a file's bytes for cards.import (Go decodes []byte from base64).
// Built in chunks: a character card is a PNG whose pixels can run to tens of MB,
// and appending a megabyte of single characters to a string one at a time is
// both slow and a good way to blow the argument limit on String.fromCharCode.
async function fileToBase64(file: File): Promise<string> {
  const buf = new Uint8Array(await file.arrayBuffer())
  const CHUNK = 0x8000
  const parts: string[] = []
  for (let i = 0; i < buf.length; i += CHUNK) {
    parts.push(String.fromCharCode(...buf.subarray(i, i + CHUNK)))
  }
  return btoa(parts.join(''))
}

export function formatBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KB`
  return `${n} bytes`
}

// importError turns a failed cards.import into something a user can act on.
//
// The daemon's own rejections already read well ("import card: no character
// metadata (`chara` chunk) in PNG"), so those pass through with their transport
// prefix stripped. The case worth translating is a dropped socket: an upload over
// the carrier's frame limit is not answered with an error, it closes the
// connection, so the request rejects with a generic "connection closed" (or
// "not connected", if the user retries during the reconnect window) that gives no
// hint the size was the problem. A pre-flight check catches that before we send,
// but a stale limit or a differently-bounded proxy can still get us here.
export function importError(e: unknown, file?: File): string {
  const msg = e instanceof Error ? e.message : String(e)
  if (/not connected|connection closed/i.test(msg)) {
    const size = file ? ` (${formatBytes(file.size)})` : ''
    return `Lost the connection to terva while uploading${size}. If the file is large this is likely its size — the daemon closes the socket on an oversized upload. Otherwise the daemon may have restarted; it should reconnect on its own.`
  }
  // "badRequest: import card: ..." → "import card: ...". The wire code is noise
  // to a user; the sentence after it is the actual reason.
  return msg.replace(/^Error:\s*/, '').replace(/^[a-zA-Z]+:\s*/, '')
}

// cardDeleteWarning is the confirm text for cards.delete.
//
// The house style for these (deleteSession, deleteWorld) is to say what SURVIVES
// — "its chats keep their own copies". A card is the case where that reassurance
// would be a lie: `cards.delete` is an `os.RemoveAll` on the card's directory
// with no in-use check, and a session re-resolves `SessionMeta.Card` on every
// materialize, so every chat bound to this card stops reopening once it is
// evicted. Nothing warns you later and nothing brings it back, so the count is
// the whole message. Exported for its own test.
export function cardDeleteWarning(name: string, chats: number): string {
  const head = `Delete “${name}”? The card and its avatar are removed for good.`
  if (chats <= 0) return head
  const s = chats === 1 ? `${chats} chat` : `${chats} chats`
  return `${head}\n\n${s} with ${name} will no longer open — a chat needs its card to resume. Export the card first if you might want it back.`
}

// The library screen: an avatar-forward character grid (tap a card → start
// talking with its default greeting; the ⋯ opens options), card import by drag
// or file-pick, a recent-chats strip, and the persona roster.
export function Library(props: {
  client: Client
  ready: boolean
  status: Status
  onOpenChat: (session: string) => void
}) {
  const { client, ready, status, onOpenChat } = props
  const [cards, setCards] = useState<CardSummary[]>([])
  const [personas, setPersonas] = useState<PersonaSummary[]>([])
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [sheet, setSheet] = useState<CardSummary | null>(null)
  const [editor, setEditor] = useState<CardSummary | null>(null)
  const [personaSheet, setPersonaSheet] = useState<PersonaSummary | null>(null)
  // The persona editor's three entries: a blank new one, editing one of yours,
  // or duplicating a built-in under a new name. One state rather than three
  // booleans, so the modes cannot both be open.
  const [personaEditor, setPersonaEditor] = useState<{ mode: 'new' | 'edit' | 'duplicate'; persona?: PersonaSummary } | null>(null)
  const [charSheet, setCharSheet] = useState<CardSummary | null>(null)
  const [showAllChats, setShowAllChats] = useState(false)
  const [worlds, setWorlds] = useState<WorldView[]>([])
  const [worldSheet, setWorldSheet] = useState<WorldView | null>(null)
  // The daemon serves the Worlds verbs (an older one answers "unsupported" and
  // the whole shelf, import included, stays absent).
  const [worldsSupported, setWorldsSupported] = useState(false)
  const [worldEdit, setWorldEdit] = useState(false)
  const [worldEditName, setWorldEditName] = useState('')
  const [worldEditDesc, setWorldEditDesc] = useState('')
  const [busy, setBusy] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [error, setError] = useState('')
  // Non-fatal import notes (a downscaled portrait), kept apart from error so a
  // card that imported fine never renders as a failure.
  const [notes, setNotes] = useState<string[]>([])
  const [importUrl, setImportUrl] = useState('')
  const [theme, setTheme] = useState(currentTheme())

  const load = () => {
    client.send<CardsListResult>('cards.list', {}).then((r) => setCards(r.cards ?? [])).catch((e: unknown) => setError(String(e)))
    client.send<PersonasListResult>('personas.list', {}).then((r) => setPersonas(r.personas ?? [])).catch(() => {})
    client.send<SessionsResult>('sessions.list', {}).then((r) => setSessions(r.sessions ?? [])).catch(() => {})
    // Saved Worlds (W5) — an older daemon answers "unsupported"; the shelf just
    // stays absent.
    client
      .send<WorldsListResult>('worlds.list', {})
      .then((r) => {
        setWorlds(r.worlds ?? [])
        setWorldsSupported(true)
      })
      .catch(() => {})
  }
  useEffect(() => {
    if (ready) load()
  }, [ready])

  const startChat = async (card: CardSummary, greetingIdx: number) => {
    setBusy(true)
    setError('')
    try {
      const opts: CreateOpts = { experience: 'chat', card: card.id, greeting: greetingIdx }
      const res = await client.send<{ session: SessionInfo }>('sessions.create', opts)
      setSheet(null)
      setCharSheet(null)
      onOpenChat(res.session.id)
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  // Start a chat inside a saved World (W5): the World's roster, lore, and
  // coordination seed the new session; the picked character is who you talk to.
  const startChatInWorld = async (world: WorldView, cardRef: string) => {
    setBusy(true)
    setError('')
    try {
      const res = await client.send<{ session: SessionInfo }>('sessions.create', { experience: 'chat', card: cardRef, world: world.id } as CreateOpts)
      setWorldSheet(null)
      onOpenChat(res.session.id)
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }
  // Start a PLAY session inside the World (W6): the roster warms as the
  // director's actors, the lore travels — the ensemble half of chat-in-World.
  const startPlayInWorld = async (world: WorldView) => {
    setBusy(true)
    setError('')
    try {
      const res = await client.send<{ session: SessionInfo }>('sessions.create', { experience: 'play', world: world.id } as CreateOpts)
      setWorldSheet(null)
      onOpenChat(res.session.id)
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }
  const deleteWorld = async (world: WorldView) => {
    if (!window.confirm(`Delete the World “${world.name}”? Its chats keep their own copies — only the saved World goes.`)) return
    try {
      await client.send('worlds.delete', { id: world.id })
      setWorldSheet(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Import a World bundle (W5b): one JSON file carrying the WorldDoc plus its
  // characters' cards. The cards land in the card library (deduped by
  // content); the World always gets a fresh id, so importing never overwrites.
  const importWorldBundle = async (files: FileList) => {
    setError('')
    setBusy(true)
    try {
      for (const file of Array.from(files)) {
        const limit = client.maxUploadBytes
        if (limit > 0 && file.size > limit) {
          throw new Error(`${file.name} is ${formatBytes(file.size)}, over the ${formatBytes(limit)} upload limit.`)
        }
        await client.send<WorldView>('worlds.import', { bytes: await fileToBase64(file) })
      }
      load()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  // Download a World as a bundle — the CardSheet export pattern.
  const exportWorld = async (world: WorldView) => {
    setError('')
    try {
      const res = await client.send<WorldExport>('worlds.export', { id: world.id })
      const bin = atob(res.bytes)
      const arr = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i)
      const url = URL.createObjectURL(new Blob([arr], { type: res.mime_type }))
      const a = document.createElement('a')
      a.href = url
      a.download = res.filename
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e) {
      setError(String(e))
    }
  }

  // Metadata edits from the sheet (worlds.update): rename, description, cover.
  // Description rides VERBATIM — callers pass the current text when leaving it
  // unchanged.
  const updateWorld = async (p: WorldUpdateParams): Promise<boolean> => {
    try {
      const view = await client.send<WorldView>('worlds.update', p)
      setWorldSheet(view)
      load()
      return true
    } catch (e) {
      setError(String(e))
      return false
    }
  }

  const importFiles = async (files: FileList) => {
    setError('')
    setNotes([])
    const problems: string[] = []
    const imported: string[] = []
    for (const file of Array.from(files)) {
      // Pre-flight the size. Posting a frame over the carrier's limit does not
      // fail the request — it kills the socket — so checking here is the only
      // way the user gets told what actually went wrong.
      const limit = client.maxUploadBytes
      if (limit > 0 && file.size > limit) {
        problems.push(
          `${file.name} is ${formatBytes(file.size)}, over the ${formatBytes(limit)} upload limit. ` +
            `Character cards carry their portrait as the image itself, so an oversized one is usually a wallpaper-sized picture — re-export it at a smaller resolution, or import it by URL instead.`,
        )
        continue
      }
      try {
        const view = await client.send<CardView>('cards.import', { bytes: await fileToBase64(file) })
        imported.push(...(view?.warnings ?? []))
      } catch (e) {
        problems.push(`${file.name}: ${importError(e, file)}`)
      }
    }
    setError(problems.join('\n'))
    setNotes(imported)
    load()
  }

  // Import a card from a remote URL (e.g. a chub.ai download link). The daemon
  // fetches it through the SSRF-guarded egress client — the client just posts
  // the URL.
  const importFromUrl = async () => {
    const url = importUrl.trim()
    if (!url) return
    setError('')
    setNotes([])
    setBusy(true)
    try {
      const view = await client.send<CardView>('cards.import', { url })
      setNotes(view?.warnings ?? [])
      setImportUrl('')
      load()
    } catch (e) {
      setError(importError(e))
    } finally {
      setBusy(false)
    }
  }

  const cardById = new Map(cards.map((c) => [c.id, c]))
  const chatsForCard = (cardId: string) => sessions.filter((s) => s.card === cardId)
  const chatsFor = (cardId: string) => chatsForCard(cardId).length
  // Every immersive session, most-recent first (the daemon lists by mtime), for
  // the "Your chats" resume list (#3). Coding sessions are excluded.
  const chats = sessions.filter((s) => s.experience === 'chat' || s.experience === 'play')
  const CHATS_SHOWN = 6
  const visibleChats = showAllChats ? chats : chats.slice(0, CHATS_SHOWN)

  // Delete a card and its avatar (cards.delete). The daemon does NOT check
  // whether anything is using it, and a session re-resolves SessionMeta.Card on
  // every materialize — so a chat bound to a deleted card fails to reopen once
  // it is evicted or the daemon restarts. That is invisible from here and
  // permanent, so the count goes in the confirm.
  const deleteCard = async (card: CardSummary) => {
    if (!window.confirm(cardDeleteWarning(card.name, chatsFor(card.id)))) return
    try {
      await client.send('cards.delete', { id: card.id })
      setSheet(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Delete a persona of yours (personas.delete). Built-ins are not offered this
  // — the sheet gates it on `editable` — and the daemon refuses them anyway.
  //
  // The confirm distinguishes the two cases, because they are genuinely
  // different acts: deleting a plain persona of yours removes it, while deleting
  // one that SHADOWS a built-in of the same name un-shadows it, so the built-in
  // comes back rather than the entry disappearing.
  const deletePersona = async (p: PersonaSummary) => {
    const shadows = personas.some((o) => o.ref !== p.ref && o.name.trim().toLowerCase() === p.name.trim().toLowerCase())
    const msg = shadows
      ? `Delete your “${p.name}”? The built-in of the same name comes back in its place.`
      : `Delete the persona “${p.name}”? This can't be undone.`
    if (!window.confirm(msg)) return
    try {
      await client.send('personas.delete', { name: p.ref })
      setPersonaSheet(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Tapping a character: with no chats yet, drop straight into a fresh one so a
  // first encounter has no friction; otherwise surface its existing chats to
  // resume or start anew (#2).
  const openCharacter = (card: CardSummary) => {
    if (chatsFor(card.id) === 0) void startChat(card, 0)
    else setCharSheet(card)
  }

  // Delete a session for good (sessions.delete). Confirms first — it is
  // irreversible — then re-lists; the daemon closes any live handle and
  // broadcasts the change. The label helps you tell one untitled chat from the
  // next in the confirm.
  const deleteSession = async (s: SessionInfo) => {
    const label = s.title || (s.card ? cardById.get(s.card)?.name : '') || 'this chat'
    if (!window.confirm(`Delete “${label}”? This can't be undone.`)) return
    try {
      await client.send('sessions.delete', null, s.id)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  return (
    <div class="stage">
      <header class="stage-topbar">
        <h1 class="stage-brand">terva Stage</h1>
        <div class="stage-topbar__right">
          <a class="stage-nav-link" href="/" title="Open the control panel">⌂ Panel</a>
          <select
            class="stage-theme-pick"
            title="Theme"
            value={theme}
            onChange={(e) => {
              const id = (e.target as HTMLSelectElement).value
              setTheme(id)
              applyTheme(id)
            }}
          >
            {THEMES.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
          <span class={`stage-status stage-status--${status}`}>{status}</span>
        </div>
      </header>

      <main
        class={`stage-library ${dragging ? 'stage-library--drag' : ''}`}
        onDragOver={(e) => {
          e.preventDefault()
          setDragging(true)
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault()
          setDragging(false)
          if (e.dataTransfer?.files?.length) void importFiles(e.dataTransfer.files)
        }}
      >
        <div class="stage-section-head">
          <h2>Characters</h2>
          <label class="stage-import">
            + Import
            <input
              type="file"
              accept="image/png,application/json"
              multiple
              hidden
              onChange={(e) => {
                const f = (e.target as HTMLInputElement).files
                if (f) void importFiles(f)
              }}
            />
          </label>
        </div>
        <form
          class="stage-import-url"
          onSubmit={(e) => {
            e.preventDefault()
            void importFromUrl()
          }}
        >
          <input
            type="url"
            class="stage-import-url__input"
            placeholder="Paste a card URL (chub.ai, …)"
            value={importUrl}
            disabled={busy}
            onInput={(e) => setImportUrl((e.target as HTMLInputElement).value)}
          />
          <button type="submit" class="stage-import-url__go" disabled={busy || !importUrl.trim()}>
            Import URL
          </button>
        </form>
        {error && <p class="stage-error">{error}</p>}
        {/* Import succeeded, but the library changed something about the card —
            a downscaled or dropped portrait. Not an error; a stated reason for a
            surprise the user would otherwise read as a bug. */}
        {notes.map((n) => (
          <p key={n} class="stage-note">{n}</p>
        ))}
        {cards.length === 0 && ready && <p class="stage-empty">No characters yet — drop a card PNG here, paste a URL, or use Import.</p>}

        <ul class="stage-grid">
          {cards.map((card) => (
            <li key={card.id} class="stage-grid__cell">
              <button
                class="stage-card"
                disabled={busy}
                title={chatsFor(card.id) > 0 ? `${chatsFor(card.id)} chat${chatsFor(card.id) === 1 ? '' : 's'} with ${card.name}` : `Chat with ${card.name}`}
                onClick={() => openCharacter(card)}
              >
                {card.avatar_url ? (
                  <img class="stage-card__avatar" src={card.avatar_url} alt="" />
                ) : (
                  <div class="stage-card__avatar stage-card__avatar--blank" aria-hidden="true" />
                )}
                <span class="stage-card__name">{card.name}</span>
                <span class="stage-card__meta">
                  {card.creator && <span class="stage-card__creator">{card.creator}</span>}
                  {chatsFor(card.id) > 0 && <span class="stage-card__count">·{chatsFor(card.id)}</span>}
                </span>
              </button>
              <button class="stage-card__more" title="Details" onClick={() => setSheet(card)}>
                ⋯
              </button>
            </li>
          ))}
        </ul>

        {worldsSupported && (
          <>
            <div class="stage-section-head">
              <h2>Worlds</h2>
              <label class="stage-import">
                + Import
                <input
                  type="file"
                  accept="application/json,.json"
                  multiple
                  hidden
                  onChange={(e) => {
                    const f = (e.target as HTMLInputElement).files
                    if (f?.length) void importWorldBundle(f)
                  }}
                />
              </label>
            </div>
            {worlds.length === 0 && (
              <p class="stage-empty">No Worlds yet — save a chat as one (Steering → World tab) or import a World bundle.</p>
            )}
            <ul class="stage-worlds">
              {worlds.map((wld) => (
                <li key={wld.id}>
                  <button class="stage-world" disabled={busy} title={wld.description || wld.name} onClick={() => setWorldSheet(wld)}>
                    {wld.cover_url ? (
                      <img class="stage-world__cover" src={wld.cover_url} alt="" />
                    ) : (
                      <span class="stage-world__icon" aria-hidden="true">🌍</span>
                    )}
                    <span class="stage-world__text">
                      <span class="stage-world__name">{wld.name}</span>
                      <span class="stage-world__meta">
                        {Object.keys(wld.characters ?? {}).length} character{Object.keys(wld.characters ?? {}).length === 1 ? '' : 's'}
                        {(wld.sessions ?? 0) > 0 && <span> · {wld.sessions} chat{wld.sessions === 1 ? '' : 's'}</span>}
                      </span>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}

        {chats.length > 0 && (
          <>
            <h2>Your chats</h2>
            <ul class="stage-yourchats">
              {visibleChats.map((s) => {
                const c = s.card ? cardById.get(s.card) : undefined
                const when = relativeTime(s.updated)
                return (
                  <li key={s.id} class="stage-yourchats__row">
                    <button class="stage-yourchats__item" onClick={() => onOpenChat(s.id)}>
                      {c?.avatar_url ? (
                        <img class="stage-yourchats__avatar" src={c.avatar_url} alt="" />
                      ) : (
                        <div class="stage-yourchats__avatar stage-yourchats__avatar--blank" aria-hidden="true" />
                      )}
                      <span class="stage-yourchats__text">
                        <span class="stage-yourchats__title">{s.title || c?.name || 'Untitled'}</span>
                        <span class="stage-yourchats__sub">
                          {c && <span class="stage-yourchats__char">{c.name}</span>}
                          {s.experience === 'play' && <span class="stage-yourchats__exp">play</span>}
                          {(s.messages ?? 0) > 0 && <span>· {s.messages} msg</span>}
                        </span>
                      </span>
                      {when && <span class="stage-yourchats__when">{when}</span>}
                    </button>
                    <button class="stage-yourchats__del" title="Delete this chat" aria-label="Delete this chat" onClick={() => void deleteSession(s)}>
                      🗑
                    </button>
                  </li>
                )
              })}
            </ul>
            {chats.length > CHATS_SHOWN && (
              <button class="stage-yourchats__more" onClick={() => setShowAllChats((v) => !v)}>
                {showAllChats ? 'Show fewer' : `Show all ${chats.length} chats`}
              </button>
            )}
          </>
        )}

        {/* No longer gated on personas.length: the roster always carries the
            embedded crew, but "+ New" has to be reachable even if it somehow
            does not — a create affordance that only appears once something
            exists is the one case where it is useless. */}
        <>
            <div class="stage-drawer__head2">
              <h2>Personas</h2>
              <button class="stage-import" onClick={() => setPersonaEditor({ mode: 'new' })}>
                + New
              </button>
            </div>
            <ul class="stage-personas">
              {personas.map((p) => (
                <li key={p.ref}>
                  <button class="stage-persona" title={`About ${p.name}`} onClick={() => setPersonaSheet(p)}>
                    <span class="stage-persona__emoji" aria-hidden="true">
                      {p.emoji || '🎭'}
                    </span>
                    <span class="stage-persona__name">{p.name}</span>
                    <span class="stage-persona__origin">{p.origin}</span>
                  </button>
                </li>
              ))}
            </ul>
        </>
      </main>

      {charSheet && (
        <CharacterChats
          card={charSheet}
          chats={chatsForCard(charSheet.id)}
          busy={busy}
          onClose={() => setCharSheet(null)}
          onOpen={(id) => onOpenChat(id)}
          onNew={() => void startChat(charSheet, 0)}
          onDetails={() => {
            setSheet(charSheet)
            setCharSheet(null)
          }}
          onDelete={(s) => void deleteSession(s)}
        />
      )}

      {sheet && (
        <CardSheet
          client={client}
          card={sheet}
          busy={busy}
          onClose={() => setSheet(null)}
          onStart={(g) => void startChat(sheet, g)}
          onEdit={() => {
            setEditor(sheet)
            setSheet(null)
          }}
          onDelete={() => void deleteCard(sheet)}
        />
      )}

      {editor && <CardEditor client={client} card={editor} onClose={() => setEditor(null)} onSaved={load} />}

      {personaSheet && (
        <PersonaSheet
          client={client}
          persona={personaSheet}
          onClose={() => setPersonaSheet(null)}
          onEdit={() => {
            setPersonaEditor({ mode: 'edit', persona: personaSheet })
            setPersonaSheet(null)
          }}
          onDuplicate={() => {
            setPersonaEditor({ mode: 'duplicate', persona: personaSheet })
            setPersonaSheet(null)
          }}
          onDelete={() => void deletePersona(personaSheet)}
        />
      )}

      {personaEditor && (
        <PersonaEditor
          client={client}
          persona={personaEditor.persona}
          duplicate={personaEditor.mode === 'duplicate'}
          taken={personas.map((p) => p.name)}
          onClose={() => setPersonaEditor(null)}
          onSaved={load}
        />
      )}

      {/* The World sheet (W5): who lives here, the chats inside it, and a way
          in — start a new chat as any of its characters. */}
      {worldSheet && (
        <div class="stage-drawer-backdrop" onClick={() => setWorldSheet(null)}>
          <aside class="stage-drawer stage-worldsheet" onClick={(e) => e.stopPropagation()}>
            <header class="stage-drawer__head">
              <h3>🌍 {worldSheet.name}</h3>
              <button
                class="stage-worldlore__act"
                title="Rename or describe this World"
                onClick={() => {
                  setWorldEditName(worldSheet.name)
                  setWorldEditDesc(worldSheet.description ?? '')
                  setWorldEdit(true)
                }}
              >
                ✎
              </button>
              <button class="stage-drawer__close" onClick={() => setWorldSheet(null)}>✕</button>
            </header>
            {worldSheet.cover_url && <img class="stage-worldsheet__cover" src={worldSheet.cover_url} alt="" />}
            {worldEdit ? (
              <form
                class="stage-worldedit"
                onSubmit={(e) => {
                  e.preventDefault()
                  void updateWorld({ id: worldSheet.id, name: worldEditName.trim(), description: worldEditDesc.trim() }).then((ok) => {
                    if (ok) setWorldEdit(false)
                  })
                }}
              >
                <input
                  class="stage-worldedit__name"
                  placeholder="World name"
                  value={worldEditName}
                  onInput={(e) => setWorldEditName((e.target as HTMLInputElement).value)}
                />
                <textarea
                  class="stage-worldedit__desc"
                  placeholder="What is this World? (shown on the shelf)"
                  value={worldEditDesc}
                  onInput={(e) => setWorldEditDesc((e.target as HTMLTextAreaElement).value)}
                />
                <div class="stage-worldedit__actions">
                  <button type="submit" class="stage-worldedit__save" disabled={!worldEditName.trim()}>
                    Save
                  </button>
                  <button type="button" class="stage-worldedit__cancel" onClick={() => setWorldEdit(false)}>
                    Cancel
                  </button>
                </div>
              </form>
            ) : (
              worldSheet.description && <p class="stage-hint">{worldSheet.description}</p>
            )}
            <section class="stage-drawer__section">
              <h4>Characters</h4>
              {Object.keys(worldSheet.characters ?? {}).length === 0 && <p class="stage-empty">No characters saved in this World.</p>}
              <ul class="stage-cast">
                {Object.entries(worldSheet.characters ?? {}).map(([name, ref]) => (
                  <li key={name} class="stage-cast__member">
                    <span class="stage-cast__name">{name}</span>
                    <button class="stage-worldsheet__chat" disabled={busy} title={`New chat with ${name} in ${worldSheet.name}`} onClick={() => void startChatInWorld(worldSheet, ref)}>
                      Chat
                    </button>
                  </li>
                ))}
              </ul>
            </section>
            <section class="stage-drawer__section">
              <h4>Chats in this World</h4>
              {sessions.filter((s) => s.world === worldSheet.id).length === 0 && <p class="stage-empty">None yet.</p>}
              <ul class="stage-yourchats">
                {sessions
                  .filter((s) => s.world === worldSheet.id)
                  .map((s) => (
                    <li key={s.id} class="stage-yourchats__row">
                      <button class="stage-yourchats__item" onClick={() => onOpenChat(s.id)}>
                        <span class="stage-yourchats__text">
                          <span class="stage-yourchats__title">{s.title || 'Untitled'}</span>
                          <span class="stage-yourchats__sub">{(s.messages ?? 0) > 0 && <span>{s.messages} msg</span>}</span>
                        </span>
                        {relativeTime(s.updated) && <span class="stage-yourchats__when">{relativeTime(s.updated)}</span>}
                      </button>
                    </li>
                  ))}
              </ul>
            </section>
            <div class="stage-worldsheet__actions">
              {Object.keys(worldSheet.characters ?? {}).length > 0 && (
                <button
                  class="stage-worldsheet__play"
                  disabled={busy}
                  title={`Run a play session in ${worldSheet.name} — the whole roster on stage, you direct`}
                  onClick={() => void startPlayInWorld(worldSheet)}
                >
                  🎭 Play here
                </button>
              )}
              <button class="stage-worldsheet__export" title="Download this World as a bundle — its characters' cards ride along" onClick={() => void exportWorld(worldSheet)}>
                ⬇ Export bundle
              </button>
              <label class="stage-worldsheet__coverbtn">
                🖼 {worldSheet.cover_url ? 'Change cover' : 'Set cover'}
                <input
                  type="file"
                  accept="image/*"
                  hidden
                  onChange={(e) => {
                    const f = (e.target as HTMLInputElement).files?.[0]
                    if (!f) return
                    void fileToBase64(f).then((b64) =>
                      updateWorld({ id: worldSheet.id, description: worldSheet.description ?? '', cover: b64 }),
                    )
                  }}
                />
              </label>
              {worldSheet.cover_url && (
                <button
                  class="stage-worldsheet__coverbtn"
                  onClick={() => void updateWorld({ id: worldSheet.id, description: worldSheet.description ?? '', remove_cover: true })}
                >
                  Remove cover
                </button>
              )}
            </div>
            <button class="stage-worldsheet__delete" onClick={() => void deleteWorld(worldSheet)}>
              Delete this World
            </button>
          </aside>
        </div>
      )}
    </div>
  )
}
