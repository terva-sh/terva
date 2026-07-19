import { useEffect, useState } from 'preact/hooks'
import { THEMES, applyTheme, currentTheme } from './theme'
import { CardSheet } from './CardSheet'
import { CardEditor } from './CardEditor'
import { PersonaSheet } from './PersonaSheet'
import { CharacterChats } from './CharacterChats'
import { relativeTime } from './format'
import type { Client } from '../../platform/ctrlproto/client'
import type {
  Status,
  CardSummary,
  PersonaSummary,
  SessionInfo,
  CardsListResult,
  PersonasListResult,
  CreateOpts,
} from '../../platform/ctrlproto/types'

type SessionsResult = { sessions: SessionInfo[] }

// Base64-encode a file's bytes for cards.import (Go decodes []byte from base64).
async function fileToBase64(file: File): Promise<string> {
  const buf = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  for (let i = 0; i < buf.length; i++) binary += String.fromCharCode(buf[i])
  return btoa(binary)
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
  const [charSheet, setCharSheet] = useState<CardSummary | null>(null)
  const [showAllChats, setShowAllChats] = useState(false)
  const [busy, setBusy] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [error, setError] = useState('')
  const [importUrl, setImportUrl] = useState('')
  const [theme, setTheme] = useState(currentTheme())

  const load = () => {
    client.send<CardsListResult>('cards.list', {}).then((r) => setCards(r.cards ?? [])).catch((e: unknown) => setError(String(e)))
    client.send<PersonasListResult>('personas.list', {}).then((r) => setPersonas(r.personas ?? [])).catch(() => {})
    client.send<SessionsResult>('sessions.list', {}).then((r) => setSessions(r.sessions ?? [])).catch(() => {})
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

  const importFiles = async (files: FileList) => {
    setError('')
    for (const file of Array.from(files)) {
      try {
        await client.send('cards.import', { bytes: await fileToBase64(file) })
      } catch (e) {
        setError(String(e))
      }
    }
    load()
  }

  // Import a card from a remote URL (e.g. a chub.ai download link). The daemon
  // fetches it through the SSRF-guarded egress client — the client just posts
  // the URL.
  const importFromUrl = async () => {
    const url = importUrl.trim()
    if (!url) return
    setError('')
    setBusy(true)
    try {
      await client.send('cards.import', { url })
      setImportUrl('')
      load()
    } catch (e) {
      setError(String(e))
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

        {personas.length > 0 && (
          <>
            <h2>Personas</h2>
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
        )}
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
        />
      )}

      {editor && <CardEditor client={client} card={editor} onClose={() => setEditor(null)} onSaved={load} />}

      {personaSheet && <PersonaSheet client={client} persona={personaSheet} onClose={() => setPersonaSheet(null)} />}
    </div>
  )
}
