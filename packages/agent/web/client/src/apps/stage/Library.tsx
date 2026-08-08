import { useEffect, useState } from 'preact/hooks'
import { THEMES, applyTheme, currentTheme } from './theme'
import { CardSheet } from './CardSheet'
import { PersonaSheet } from './PersonaSheet'
import { PersonaEditor } from './PersonaEditor'
import { CharacterChats } from './CharacterChats'
import { GroupSheet } from './GroupSheet'
import { SessionGroupSheet } from './SessionGroupSheet'
import { CREATOR_PERSONA } from './creator'
import { applyPortraits, portraitsOn } from './portraits'
import { relativeTime } from './format'
import { t, tn, tr } from '../../i18n'
import { panelHref } from '../../ui/navlinks'
import { ConnectionBanner, Placeholder } from '../../ui/Loading'
import { fileToBase64 } from '../../ui/browser'
// Stage's own formatBytes is gone: it is ui/formatting's humanBytes, which took
// this one's translated units with it rather than dropping them.
import { humanBytes } from '../../ui/formatting'
import type { ClientLike } from '../../platform/ctrlproto/client'
import {
  applyGroupFilter,
  bulkMembers,
  bulkState,
  cycleGroup,
  emptyFilter,
  groupState,
  hasFilter,
  originGroups,
  ungroupedGroup,
  worldGroups,
  type GroupFilter,
} from '../../platform/groups'
import { cardQueryTerms, matchesCardQuery } from '../../platform/cardsearch'
import { personaGroupNames, shelvePersonas } from '../../platform/personagroups'
import type {
  Status,
  CardSummary,
  CardView,
  PersonaSummary,
  PersonaView,
  SessionInfo,
  CardsListResult,
  PersonasListResult,
  CreateOpts,
  WorldView,
  WorldsListResult,
  Group,
  CardGroupsResult,
} from '../../platform/ctrlproto/types'

type SessionsResult = { sessions: SessionInfo[] }

// Card sort modes. Each has a NATURAL order (the label reads in that direction) —
// name A→Z, the rest most-recent / most-first — and `reversed` flips it. Favorites
// always float to the top, sorted among themselves by the same order.
// 'updated' is when the character was last CHANGED — a doctor apply, an enrich,
// a hand edit — as opposed to 'added' (when it arrived) or 'used' (when it was
// last played). Distinct from both: the character you have been working on is
// usually neither the newest nor the most recently chatted with.
const CARD_SORT_MODES = ['name', 'added', 'updated', 'used', 'chats'] as const
type CardSortMode = (typeof CARD_SORT_MODES)[number]
type CardSort = { mode: CardSortMode; reversed: boolean }

// The shelf opens on what you last worked on. Alphabetical was the old default
// and it is a filing order, not a working one: it puts the same names at the top
// every day regardless of what you have been doing, so the character you just
// spent an hour on is wherever the alphabet left it.
//
// See the key comment where this is read — the version suffix is load-bearing.
const CARD_SORT_KEY = 'terva_card_sort_2'
const CARD_SORT_DEFAULT: CardSort = { mode: 'updated', reversed: false }

// A row of group chips that doubles as the filter. Tapping a chip cycles its
// state (off → show-only → hide → off); the "⋯" opens a user group for editing.
// System chips (a derived Stage origin) filter but carry no "⋯" — there is
// nothing to edit. Shared by the card grid and the "Your chats" list.
function GroupFilterChips(props: {
  groups: Group[]
  filter: GroupFilter
  onCycle: (id: string) => void
  onManage?: (g: Group) => void
}) {
  const { groups, filter, onCycle, onManage } = props
  if (groups.length === 0) return null
  return (
    <div class="stage-groupchips" role="group" aria-label={t('Filter by group')}>
      {groups.map((g) => {
        const state = groupState(filter, g.id)
        const title =
          state === 'off'
            ? t('Show only “%s”', g.name)
            : state === 'include'
              ? t('Hide “%s”', g.name)
              : t('Clear the filter on “%s”', g.name)
        return (
          <span key={g.id} class="stage-groupchip" data-state={state} data-system={g.system ? '' : undefined}>
            <button class="stage-groupchip__filter" onClick={() => onCycle(g.id)} aria-pressed={state !== 'off'} title={title}>
              {g.color && <span class="stage-groupchip__dot" style={{ background: g.color }} aria-hidden="true" />}
              <span class="stage-groupchip__name">{g.name}</span>
              <span class="stage-groupchip__count">·{g.members.length}</span>
            </button>
            {onManage && !g.system && (
              <button
                class="stage-groupchip__manage"
                onClick={() => onManage(g)}
                title={t('Edit “%s”', g.name)}
                aria-label={t('Edit “%s”', g.name)}
              >
                ⋯
              </button>
            )}
          </span>
        )
      })}
    </div>
  )
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
    const size = file ? ` (${humanBytes(file.size)})` : ''
    return t(
      'Lost the connection to terva while uploading%s. If the file is large this is likely its size — the daemon closes the socket on an oversized upload. Otherwise the daemon may have restarted; it should reconnect on its own.',
      size,
    )
  }
  // "badRequest: import card: ..." → "import card: ...". The wire code is noise
  // to a user; the sentence after it is the actual reason.
  return msg.replace(/^Error:\s*/, '').replace(/^[a-zA-Z]+:\s*/, '')
}

// cardDeleteWarning is the confirm text for cards.delete, asked only when the
// card has no chats on it.
//
// The house style for these (deleteSession, deleteWorld) is to say what SURVIVES
// — "its chats keep their own copies". A card used to be the case where that
// reassurance would be a lie, so this said how many chats the delete would
// break. It no longer can: the daemon REFUSES while chats are bound, because a
// session re-resolves SessionMeta.Card on every materialize and the card's bytes
// are gone for good. So the count moved out of this sentence and into
// cardInUseMessage, and what is left is the plain irreversibility.
// Exported for its own test.
export function cardDeleteWarning(name: string): string {
  return t('Delete “%s”? The card and its avatar are removed for good.', name)
}

// worldDeleteWarning is the confirm text for worlds.delete, shared by the two
// surfaces that offer it: the shelf tile here and the World studio's own button.
//
// Shared rather than written twice because the two are one promise about what
// survives, and a World is the case where that promise is the whole reassurance
// — deleting one does NOT take the chats played in it. A session copies its
// World in at creation and keeps that copy; what it loses is the grouping. Two
// copies of this sentence would eventually disagree about that, and the surface
// that got it wrong would be the one talking someone out of a safe delete or
// into an unsafe one.
export function worldDeleteWarning(name: string): string {
  return t('Delete the World “%s”? Its chats keep their own copies — only the saved World goes.', name)
}

// copyName proposes a free name for a duplicated card, counting upward past any
// copy already made.
//
// The daemon REFUSES a copy that would land on a card already in the library,
// and it is right to: an id is a stem of the name plus a hash of the contents,
// so a copy under an unchanged name is not a second card — it is the first one,
// and writing it would report success while creating nothing. That refusal is a
// backstop, though, not a workflow. Proposing a free name is what keeps an
// author from meeting it for no reason on the very first click.
//
// Distinct from the persona flow's duplicateName, which says "(my copy)" because
// a persona copy exists to fork a BUILT-IN and the wording marks whose it now
// is. A card copy is just a copy, so it says so.
// Exported for its own test.
export function copyName(base: string, taken: string[]): string {
  const used = new Set(taken.map((n) => n.trim().toLowerCase()))
  const candidate = t('%s (copy)', base)
  if (!used.has(candidate.trim().toLowerCase())) return candidate
  for (let n = 2; n < 100; n++) {
    const next = t('%s (copy %d)', base, n)
    if (!used.has(next.trim().toLowerCase())) return next
  }
  return candidate
}

// cardInUseMessage explains a delete that will not happen, and what to do about
// it. Shown INSTEAD of the confirm — asking "are you sure?" about something the
// daemon is going to refuse teaches a user that yes means no.
//
// Archiving is offered alongside deleting because it genuinely releases the
// card: an archived transcript is not scanned. That trade (the restored chat
// finds its character gone) is the user's to make deliberately, which is the
// whole difference from the one this refusal prevents.
export function cardInUseMessage(name: string, chats: number): string {
  const head = tn(
    Math.max(chats, 1),
    '“%s” still has %d chat.',
    '“%s” still has %d chats.',
    name,
    Math.max(chats, 1),
  )
  return `${head}\n\n${t('A chat cannot open without its card, so the card stays until those chats are deleted or archived.')}`
}

// personaDeleteWarning is the confirm text for personas.delete.
//
// The counterpart to cardDeleteWarning above, and deliberately a gentler
// sentence, because the two failures are not the same. A chat whose CARD is
// evicted stops opening — the card is the character. A chat whose PERSONA is
// deleted still opens: the daemon falls back to the workspace default and says
// so on load. So this follows the house style of naming what survives, and the
// count is here to say what CHANGES rather than what breaks.
//
// A persona that shadows a built-in is the case with nothing to warn about at
// all: deleting the copy un-shadows the original, so the name those chats
// replay still resolves — to the built-in it was copied from. Exported for its
// own test.
export function personaDeleteWarning(name: string, chats: number, shadowsBuiltin: boolean): string {
  if (shadowsBuiltin) {
    return t('Delete your “%s”? The built-in of the same name comes back in its place.', name)
  }
  const head = t("Delete the persona “%s”? This can't be undone.", name)
  if (chats <= 0) return head
  const tail = tn(
    chats,
    '%d chat was created with it. It still opens — in your default persona’s voice, not this one.',
    '%d chats were created with it. They still open — in your default persona’s voice, not this one.',
    chats,
  )
  return `${head}\n\n${tail}`
}

// The library screen: an avatar-forward character grid (tap a card → start
// talking with its default greeting; the ⋯ opens options), card import by drag
// or file-pick, a recent-chats strip, and the persona roster.
export function Library(props: {
  client: ClientLike
  ready: boolean
  status: Status
  onOpenChat: (session: string) => void
  // ✎ hands off to the character studio rather than opening a sheet here. A null
  // card means "make a new one" — the studio's create mode.
  onEditCharacter: (card: CardSummary | null) => void
  // The studio's other tab: the identities you play AS. Reachable from here and
  // not only from inside a scene, because they outlive any one scene.
  onEditYou: () => void
  // A World opens its own screen. It used to open a drawer over this one, which
  // made it a property of the shelf; it has a cast, a lorebook, and a history of
  // scenes, so it navigates like a character does.
  onOpenWorld: (id: string) => void
}) {
  const { client, ready, status, onOpenChat, onEditCharacter } = props
  const [cards, setCards] = useState<CardSummary[]>([])
  const [personas, setPersonas] = useState<PersonaSummary[]>([])
  const personaShelves = shelvePersonas(personas)
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  // Which of the library's lists have ANSWERED.
  //
  // `ready` is not this. It says the hello landed, which is when load() is
  // allowed to start — every verb it fires is a further round trip, so the whole
  // window between the hello and each answer had "No characters yet" and "No
  // Worlds yet" on screen off a useState default. One flag per shelf, because
  // they answer independently and a slow verb must not hold up a fast one's
  // rows. Set on rejection too: a refusal is an answer (this daemon serves
  // none); only the state BEFORE the ask must claim nothing.
  //
  // Worlds is deliberately not here — that whole shelf is gated on
  // `worldsSupported`, which only the answer sets, so an unanswered worlds.list
  // renders nothing rather than an empty shelf.
  const [loaded, setLoaded] = useState({ cards: false, sessions: false, personas: false })
  const didLoad = (key: 'cards' | 'sessions' | 'personas') => setLoaded((l) => ({ ...l, [key]: true }))
  const [sheet, setSheet] = useState<CardSummary | null>(null)
  const [personaSheet, setPersonaSheet] = useState<PersonaSummary | null>(null)
  // The persona editor's three entries: a blank new one, editing one of yours,
  // or duplicating a built-in under a new name. One state rather than three
  // booleans, so the modes cannot both be open.
  const [personaEditor, setPersonaEditor] = useState<{ mode: 'new' | 'edit' | 'duplicate'; persona?: PersonaSummary } | null>(null)
  const [charSheet, setCharSheet] = useState<CardSummary | null>(null)
  const [showAllChats, setShowAllChats] = useState(false)
  const [worlds, setWorlds] = useState<WorldView[]>([])
  // The daemon serves the Worlds verbs (an older one answers "unsupported" and
  // the whole shelf, import included, stays absent).
  const [worldsSupported, setWorldsSupported] = useState(false)
  // Card groups (a library-organizing membership bucket, distinct from a card's
  // embedded tags). An older daemon answers "unsupported" and the whole shelf
  // stays absent.
  const [cardGroups, setCardGroups] = useState<Group[]>([])
  // Bulk edit: a selection of cards plus the one bar that acts on all of them.
  // Off by default — the library's ordinary click must stay "open this
  // character", which is what all but one visit is for.
  const [selecting, setSelecting] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [cardGroupsSupported, setCardGroupsSupported] = useState(false)
  // The grid filter: include/exclude by group (empty = the whole library). Tap a
  // chip to cycle show-only → hide → off; see platform/groups.
  const [cardFilter, setCardFilter] = useState<GroupFilter>(emptyFilter)
  const [cardQuery, setCardQuery] = useState('')
  // Portraits on/off, mirrored into state so the toggle re-renders; the document
  // root (stamped at boot by main.tsx) is what actually drives the CSS.
  const [portraits, setPortraits] = useState(portraitsOn())
  // The grid sort, persisted across reloads. Defaults to what you last CHANGED,
  // and a malformed stored value falls back to the default.
  //
  // The key is versioned, and it has to be. The previous default was
  // alphabetical and the previous implementation persisted from an effect that
  // ran on MOUNT — so every library that has ever been opened already has
  // {"mode":"name"} on disk, written on the user's behalf before they expressed
  // any preference. Changing the default alone would therefore change nothing
  // for anybody who has used Stage: correct in the diff, invisible in the
  // browser. A new key is the only honest way to hand out a new default once.
  const [cardSort, setCardSort] = useState<CardSort>(() => {
    try {
      const p = JSON.parse(localStorage.getItem(CARD_SORT_KEY) || '')
      if (CARD_SORT_MODES.includes(p?.mode) && typeof p?.reversed === 'boolean') return p
    } catch {
      /* fall through */
    }
    return { ...CARD_SORT_DEFAULT }
  })
  // Persisted on an explicit change ONLY — never on mount. Absence now means
  // "this reader has no preference", which is what lets a future default reach
  // them instead of being pinned by a value they never chose. This is the bug
  // the key bump above exists to undo; do not reintroduce it by moving the write
  // back into an effect keyed on cardSort.
  const changeCardSort = (next: CardSort) => {
    setCardSort(next)
    try {
      localStorage.setItem(CARD_SORT_KEY, JSON.stringify(next))
    } catch {
      // A storage-blocked browser still honours the choice for this session.
    }
  }
  // A group opened for its contents (members + rename/recolour/delete).
  const [groupSheet, setGroupSheet] = useState<Group | null>(null)
  // Session groups — the same buckets over chats/plays. Same absence rule.
  const [sessionGroups, setSessionGroups] = useState<Group[]>([])
  const [sessionGroupsSupported, setSessionGroupsSupported] = useState(false)
  const [chatFilter, setChatFilter] = useState<GroupFilter>(emptyFilter)
  const [sessionGroupSheet, setSessionGroupSheet] = useState<Group | null>(null)
  const [busy, setBusy] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [error, setError] = useState('')
  // Non-fatal import notes (a downscaled portrait), kept apart from error so a
  // card that imported fine never renders as a failure.
  const [notes, setNotes] = useState<string[]>([])
  const [importUrl, setImportUrl] = useState('')
  const [theme, setTheme] = useState(currentTheme())

  const load = () => {
    client
      .send<CardsListResult>('cards.list', {})
      .then((r) => setCards(r.cards ?? []))
      .catch((e: unknown) => setError(String(e)))
      .finally(() => didLoad('cards'))
    client
      .send<PersonasListResult>('personas.list', {})
      .then((r) => setPersonas(r.personas ?? []))
      .catch(() => {})
      .finally(() => didLoad('personas'))
    client
      .send<SessionsResult>('sessions.list', {})
      .then((r) => setSessions(r.sessions ?? []))
      .catch(() => {})
      .finally(() => didLoad('sessions'))
    // Saved Worlds (W5) — an older daemon answers "unsupported"; the shelf just
    // stays absent.
    client
      .send<WorldsListResult>('worlds.list', {})
      .then((r) => {
        setWorlds(r.worlds ?? [])
        setWorldsSupported(true)
      })
      .catch(() => {})
    // Card groups — an older daemon answers "unsupported"; the shelf stays absent.
    client
      .send<CardGroupsResult>('cardgroups.list', {})
      .then((r) => {
        setCardGroups(r.groups ?? [])
        setCardGroupsSupported(true)
      })
      .catch(() => {})
    client
      .send<{ groups: Group[] }>('sessiongroups.list', {})
      .then((r) => {
        setSessionGroups(r.groups ?? [])
        setSessionGroupsSupported(true)
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

  // Start something from nothing (creator C1): a card-less chat bound to the
  // cartographer (Kartoittaja). Every other Library entry OPENS something you
  // already have — a card, a World, a past chat — so making something you don't
  // is its own door, not a tile in the character grid. The empty state the chat
  // lands on does the grounding; the creator never speaks first.
  const startCreator = async () => {
    setBusy(true)
    setError('')
    try {
      const opts: CreateOpts = { experience: 'chat', persona: CREATOR_PERSONA }
      const res = await client.send<{ session: SessionInfo }>('sessions.create', opts)
      onOpenChat(res.session.id)
    } catch (e) {
      setError(String(e))
      setBusy(false)
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
          throw new Error(t('%s is %s, over the %s upload limit.', file.name, humanBytes(file.size), humanBytes(limit)))
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
          t(
            '%s is %s, over the %s upload limit. Character cards carry their portrait as the image itself, so an oversized one is usually a wallpaper-sized picture — re-export it at a smaller resolution, or import it by URL instead.',
            file.name,
            humanBytes(file.size),
            humanBytes(limit),
          ),
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
  // Origin buckets derived from the chats themselves (Worlds W5 / seeding card):
  // a system group per World or card that actually has a chat, so you can narrow
  // "Your chats" to one story without ever filing anything. Prepended to the
  // user's own session groups; both filter through the same include/exclude model.
  const originChatGroups = originGroups(chats, (kind, id) =>
    kind === 'world' ? worlds.find((w) => w.id === id)?.name : cardById.get(id)?.name,
  )
  const chatGroups = [...originChatGroups, ...sessionGroups]
  const filteredChats = applyGroupFilter(chats, chatGroups, chatFilter, (s) => s.id)
  const CHATS_SHOWN = 6
  const visibleChats = showAllChats ? filteredChats : filteredChats.slice(0, CHATS_SHOWN)

  // Card-group filtering shares the include/exclude model, plus one derived
  // chip: "Ungrouped", everything no user group holds yet.
  //
  // It is the affordance a freshly imported library needs. Sorting and
  // searching both help you FIND a card; neither tells you which ones you have
  // already filed, so a flat import of hundreds has no visible progress and no
  // end. Filtering to this chip makes the pile a queue — file a batch and they
  // leave the view, and the chip's own count is how much is left.
  //
  // It must be in the list handed to applyGroupFilter as well as to the chips:
  // the filter drops any id it cannot find a group for, so a chip that filtered
  // through a list it was not in would silently do nothing.
  // World-scoped variants are off the shelf.
  //
  // A variant is a fork worlds.edit_character made so an edit inside one World
  // would not rewrite the character every other World is playing. It is a real
  // card, but it is that World's business — on the shelf it would read as a
  // near-duplicate of a character you already have, with no way to tell which is
  // which. The World's own studio lists it, under the character it plays.
  //
  // Filtered HERE rather than at load: `cards` still has to be complete, because
  // a chat seeded by a variant resolves its name through the same list and would
  // otherwise render as an unknown card.
  const shelfCards = cards.filter((c) => !c.variant_of)
  const hiddenVariants = cards.length - shelfCards.length
  // The badge resolves the World id CLIENT-SIDE. The summary carries the id
  // rather than the name so a rename cannot leave a stale caption on a card, and
  // this shelf already lists Worlds — there is nothing to fetch.
  const worldName = (id?: string) => (id ? (worlds.find((w) => w.id === id)?.name ?? '') : '')
  // World chips sit between the user's groups and Ungrouped: derived, so they
  // cannot drift out of step with the rosters they describe, and filterable with
  // the machinery the stored groups already use.
  const worldChips = worldGroups(
    shelfCards,
    (c) => c.id,
    (c) => c.world_of,
    (id) => worldName(id),
  )
  const cardChipGroups = [...cardGroups, ...worldChips, ungroupedGroup(shelfCards, cardGroups, t('Ungrouped'), (c) => c.id)]

  // Free text narrows on top of the groups, over name / creator / tags. Tags
  // are the ones that earn their place: an imported library arrives tagged,
  // which is how it was organized where it came from, so a query can pull a
  // cluster out of a flat import in one go — search, select all, file.
  const cardTerms = cardQueryTerms(cardQuery)
  const visibleCards = applyGroupFilter(shelfCards, cardChipGroups, cardFilter, (c) => c.id).filter((c) =>
    matchesCardQuery(c, cardTerms),
  )
  // Whether the grid is narrowed AT ALL — either half can be the reason it is
  // empty, so the "nothing matches" affordance has to ask about both.
  const cardsNarrowed = hasFilter(cardFilter) || cardTerms.length > 0

  // Sort the filtered grid. Each mode's comparator yields its NATURAL order
  // (name A→Z; added/used newest-first; chats most-first); `reversed` flips it;
  // a name tiebreak keeps it stable. Favorites are partitioned to the top and
  // sorted among themselves by the same order — "highlight and keep at the top".
  const lastUsed = (id: string) =>
    chatsForCard(id).reduce<string>((m, s) => (s.updated && s.updated > m ? s.updated : m), '')
  const cardCmp = (a: CardSummary, b: CardSummary) => {
    let d = 0
    switch (cardSort.mode) {
      case 'name':
        break
      case 'added':
        d = (b.added ?? '').localeCompare(a.added ?? '')
        break
      case 'updated':
        // The daemon falls back to `added` for a never-edited card, so this is
        // already total; the ?? '' is only for a daemon too old to send it.
        d = (b.updated ?? '').localeCompare(a.updated ?? '')
        break
      case 'used':
        d = lastUsed(b.id).localeCompare(lastUsed(a.id))
        break
      case 'chats':
        d = chatsFor(b.id) - chatsFor(a.id)
        break
    }
    if (d === 0) d = a.name.localeCompare(b.name)
    return cardSort.reversed ? -d : d
  }
  const sortedCards = [
    ...visibleCards.filter((c) => c.favorite).sort(cardCmp),
    ...visibleCards.filter((c) => !c.favorite).sort(cardCmp),
  ]

  // Create a group (cardgroups.save with no id). Named up front, filled later —
  // the whole point of an empty group is a bucket you pour cards into.
  const createCardGroup = async () => {
    const name = window.prompt(t('Name the new group:'), '')
    if (name == null) return
    const trimmed = name.trim()
    if (!trimmed) return
    try {
      const g = await client.send<Group>('cardgroups.save', { name: trimmed })
      setGroupSheet(g)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  // Rename or recolour a group (metadata only; members are untouched).
  const saveCardGroup = async (id: string, name: string, color: string) => {
    try {
      const g = await client.send<Group>('cardgroups.save', { id, name, color })
      setGroupSheet(g)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  const deleteCardGroup = async (g: Group) => {
    if (!window.confirm(t('Delete the group “%s”? The cards in it are not deleted — only the grouping is.', g.name))) return
    try {
      await client.send('cardgroups.delete', { id: g.id })
      setCardFilter((f) => ({ include: f.include.filter((x) => x !== g.id), exclude: f.exclude.filter((x) => x !== g.id) }))
      setGroupSheet(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  // Add or remove one card from a group. The sole membership mutation is the
  // group's whole new member list, so a toggle sends members ± this card.
  const toggleCardInGroup = async (cardId: string, g: Group) => {
    const members = g.members.includes(cardId) ? g.members.filter((m) => m !== cardId) : [...g.members, cardId]
    try {
      const saved = await client.send<Group>('cardgroups.set_members', { id: g.id, members })
      if (groupSheet?.id === g.id) setGroupSheet(saved)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  // Add or remove the WHOLE selection to/from a group in one request. The
  // membership verb replaces the member list, so bulk is not a loop over cards:
  // it is a single write per group, and it either lands or it does not.
  const bulkGroup = async (g: Group, add: boolean) => {
    const members = bulkMembers(g.members, [...selected], add)
    if (members.length === g.members.length && add) return // already all in
    try {
      const saved = await client.send<Group>('cardgroups.set_members', { id: g.id, members })
      if (groupSheet?.id === g.id) setGroupSheet(saved)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Favorite / unfavorite the whole selection. Per-card, unlike groups — the
  // favourite verb takes one card — so this is the loop the group path avoids.
  const bulkFavorite = async (favorite: boolean) => {
    const ids = [...selected]
    setCards((cs) => cs.map((c) => (selected.has(c.id) ? { ...c, favorite } : c)))
    try {
      await Promise.all(ids.map((id) => client.send('cards.favorite', { id, favorite })))
    } catch (e) {
      setError(String(e))
    }
    load()
  }

  const toggleSelected = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  // Leaving selection mode drops the selection: a hidden selection that acts on
  // your next bulk click is the kind of surprise this bar exists to avoid.
  const endSelecting = () => {
    setSelecting(false)
    setSelected(new Set())
  }

  // Favorite / unfavorite a card. Optimistic: flip local state now (the star and
  // the pin-to-top respond instantly), then persist; the daemon broadcasts a
  // library change, so a failed write self-corrects on the next load().
  const toggleFavorite = async (card: CardSummary) => {
    const favorite = !card.favorite
    setCards((cs) => cs.map((c) => (c.id === card.id ? { ...c, favorite } : c)))
    try {
      await client.send('cards.favorite', { id: card.id, favorite })
    } catch (e) {
      setError(String(e))
      load()
    }
  }

  // Session groups — the same four operations over sessiongroups.*.
  const createSessionGroup = async () => {
    const name = window.prompt(t('Name the new group:'), '')
    if (name == null) return
    const trimmed = name.trim()
    if (!trimmed) return
    try {
      const g = await client.send<Group>('sessiongroups.save', { name: trimmed })
      setSessionGroupSheet(g)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  const saveSessionGroup = async (id: string, name: string, color: string) => {
    try {
      const g = await client.send<Group>('sessiongroups.save', { id, name, color })
      setSessionGroupSheet(g)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  const deleteSessionGroup = async (g: Group) => {
    if (!window.confirm(t('Delete the group “%s”? The chats in it are not deleted — only the grouping is.', g.name))) return
    try {
      await client.send('sessiongroups.delete', { id: g.id })
      setChatFilter((f) => ({ include: f.include.filter((x) => x !== g.id), exclude: f.exclude.filter((x) => x !== g.id) }))
      setSessionGroupSheet(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }
  const toggleSessionInGroup = async (sessionId: string, g: Group) => {
    const members = g.members.includes(sessionId) ? g.members.filter((m) => m !== sessionId) : [...g.members, sessionId]
    try {
      const saved = await client.send<Group>('sessiongroups.set_members', { id: g.id, members })
      if (sessionGroupSheet?.id === g.id) setSessionGroupSheet(saved)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Delete a card and its avatar (cards.delete). The daemon does NOT check
  // whether anything is using it, and a session re-resolves SessionMeta.Card on
  // every materialize — so a chat bound to a deleted card fails to reopen once
  // it is evicted or the daemon restarts. That is invisible from here and
  // permanent, so the count goes in the confirm.
  const deleteCard = async (card: CardSummary) => {
    // The daemon is the authority here — it counts chats across EVERY project,
    // while this list is only the current one — but when the answer is already
    // visible locally, say so instead of asking a question whose yes is refused.
    const bound = chatsFor(card.id)
    if (bound > 0) {
      setError(cardInUseMessage(card.name, bound))
      return
    }
    if (!window.confirm(cardDeleteWarning(card.name))) return
    try {
      await client.send('cards.delete', { id: card.id })
      setSheet(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Delete a saved World from the shelf (worlds.delete).
  //
  // Unlike deleteCard there is no in-use check, and that is not an omission: a
  // session copies its World in at creation and keeps that copy, so a chat
  // played in a deleted World still opens and still plays. What it loses is the
  // grouping. That is exactly what the confirm says, which is why the sentence
  // is shared with the studio rather than retyped here.
  const deleteWorld = async (wld: WorldView) => {
    if (!window.confirm(worldDeleteWarning(wld.name))) return
    try {
      await client.send('worlds.delete', { id: wld.id })
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Copy a card so a variant can be worked on without overwriting the version
  // that already works (cards.duplicate).
  //
  // This is a daemon verb rather than a get-rename-import right here because the
  // portrait lives OUTSIDE the card document: a copy assembled client-side would
  // mint a real new card and arrive faceless. (Re-importing an export is worse —
  // ids are content-addressed, so it returns the card you already had and looks
  // like it worked.)
  //
  // Lands in the editor, because a copy exists in order to be changed; the
  // studio's back button returns here. The prompt matches the Library's dialog
  // idiom (saveAsWorld), pre-filled with a name that is already free.
  const duplicateCard = async (card: CardSummary) => {
    const proposed = copyName(
      card.name,
      cards.map((c) => c.name),
    )
    // Dismissing the prompt and clearing it both mean "not this time", so they
    // take ONE path. Written as `?? ''` rather than a null guard followed by a
    // trim because the two-step version hides a null dereference behind the
    // guard: drop the guard and the crash produces the same "nothing happened"
    // the guard did, which no test can tell apart.
    const trimmed = (window.prompt(t('Name the copy of “%s”:', card.name), proposed) ?? '').trim()
    if (!trimmed) return
    try {
      const copy = await client.send<CardView>('cards.duplicate', { id: card.id, name: trimmed })
      setSheet(null)
      load()
      onEditCharacter(copy)
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
    // Ask what else depends on the name before offering to remove it. The count
    // comes from personas.get rather than the loaded session list because that
    // list is this project's, and a persona is every project's — and because
    // SessionInfo.persona is filled only for a MATERIALIZED session, so counting
    // client-side would miss every chat that has not been opened this run.
    //
    // Best-effort: a daemon that cannot answer must not block the delete, so a
    // failure here falls through to the plain confirm.
    let used = 0
    if (!shadows) {
      try {
        const view = (await client.send('personas.get', { name: p.ref })) as PersonaView
        used = view?.sessions_using ?? 0
      } catch {
        // leave the count at zero and ask the plain question
      }
    }
    if (!window.confirm(personaDeleteWarning(p.name, used, shadows))) return
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
    const label = s.title || (s.card ? cardById.get(s.card)?.name : '') || t('this chat')
    if (!window.confirm(t("Delete “%s”? This can't be undone.", label))) return
    try {
      await client.send('sessions.delete', null, s.id)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Archive a chat: it leaves the hub without leaving the disk, compressed into
  // a subdirectory nothing lists. No confirm, unlike delete — archiving is
  // reversible, and confirming a reversible act only teaches people to click
  // through the confirm that matters.
  const archiveSession = async (s: SessionInfo) => {
    try {
      await client.send('sessions.archive', null, s.id)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Turn a chat into a reusable World (worlds.save) — its roster, lore, and
  // coordination become a named World you can start new chats inside, no scene
  // break needed. The same primitive the Steering drawer's "Save as World" uses,
  // surfaced here in the hub so it is findable from a chat you are not inside.
  // Prompting for the name matches the Library's dialog idiom (window.confirm);
  // only offered on chats not already in a World.
  const saveAsWorld = async (s: SessionInfo) => {
    const label = s.title || (s.card ? cardById.get(s.card)?.name : '') || t('this chat')
    const name = window.prompt(t('Save “%s” as a World named:', label), '')
    if (name == null) return
    const trimmed = name.trim()
    if (!trimmed) return
    try {
      await client.send<{ name: string }>('worlds.save', { name: trimmed }, s.id)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  return (
    <div class="stage">
      <header class="stage-topbar">
        {/* i18n-exempt — the terva Stage wordmark, a product name */}
        <h1 class="stage-brand">terva Stage</h1>
        <div class="stage-topbar__right">
          {/* The identities you play as, out of the drawer: they belong to the
              library, not to whichever scene happens to be open. */}
          <button class="stage-nav-link stage-you-link" onClick={props.onEditYou} title={t('The identities you play as')}>
            {t('🎭 You')}
          </button>
          <a class="stage-nav-link" href={panelHref()} title={t('Open the control panel')}>{t('⌂ Panel')}</a>
          <select
            class="stage-theme-pick"
            title={t('Theme')}
            value={theme}
            onChange={(e) => {
              const id = (e.target as HTMLSelectElement).value
              setTheme(id)
              applyTheme(id)
            }}
          >
            {THEMES.map((th) => (
              <option key={th.id} value={th.id}>
                {tr(th.name)}
              </option>
            ))}
          </select>
          <span class={`stage-status stage-status--${status}`}>{status}</span>
        </div>
      </header>

      {/* The chip above says the word, but at 0.75rem in the corner of a header
          it is not what you notice when the library looks empty. This is. */}
      <ConnectionBanner status={status} />

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
        {/* The sixth entry point (creator C1): make something you don't have yet.
            Its own door above the "pick something you have" library, because the
            two are different acts. */}
        <button class="stage-creator-door" disabled={busy} onClick={() => void startCreator()}>
          <span class="stage-creator-door__icon" aria-hidden="true">🧭</span>
          <span class="stage-creator-door__text">
            <span class="stage-creator-door__title">{t('Start something new')}</span>
            <span class="stage-creator-door__sub">{t('Talk it through with the cartographer — from a spark, a paragraph, or nothing yet.')}</span>
          </span>
        </button>
        {/* Card groups (a browse-by bucket, distinct from a card's tags): tap a
            chip to show only that group, tap again to hide it, again to clear;
            "⋯" opens a group to edit. Absent on an older daemon lacking the verbs. */}
        {cardGroupsSupported && (cardGroups.length > 0 || cards.length > 0) && (
          <div class="stage-cardgroups">
            <div class="stage-section-head">
              <h2>{t('Groups')}</h2>
              <button class="stage-import" onClick={() => void createCardGroup()}>
                {t('+ New')}
              </button>
            </div>
            {cardGroups.length === 0 ? (
              <p class="stage-empty">
                {t('Group cards to organize your library — freshly imported, in progress, ready to play, or however suits you.')}
              </p>
            ) : (
              <GroupFilterChips
                groups={cardChipGroups}
                filter={cardFilter}
                onCycle={(id) => setCardFilter((f) => cycleGroup(f, id))}
                onManage={(g) => setGroupSheet(g)}
              />
            )}
          </div>
        )}
        <div class="stage-section-head">
          <h2>{t('Characters')}</h2>
          {/* Narrowing by name, creator or tag. First in the row because it is
              the fastest way through a big library, and because pairing it with
              the group chips above is the whole organizing loop: narrow to a
              cluster, select it, file it. Type=search so a phone offers the
              right keyboard and a clear affordance. */}
          {cards.length > 1 && (
            <input
              class="stage-cardsearch"
              type="search"
              value={cardQuery}
              onInput={(e) => setCardQuery((e.target as HTMLInputElement).value)}
              placeholder={t('Search characters')}
              aria-label={t('Search characters by name, creator or tag')}
            />
          )}
          {cards.length > 1 && (
            <div class="stage-cardsort">
              <select
                class="stage-cardsort__mode"
                value={cardSort.mode}
                onChange={(e) => changeCardSort({ ...cardSort, mode: (e.target as HTMLSelectElement).value as CardSortMode })}
                title={t('Sort characters')}
              >
                <option value="name">{t('Alphabetical')}</option>
                <option value="added">{t('Recently added')}</option>
                <option value="updated">{t('Recently updated')}</option>
                <option value="used">{t('Recently used')}</option>
                <option value="chats">{t('Most chats')}</option>
              </select>
              <button
                class="stage-cardsort__dir"
                onClick={() => changeCardSort({ ...cardSort, reversed: !cardSort.reversed })}
                title={t('Reverse order')}
                aria-pressed={cardSort.reversed}
              >
                {cardSort.reversed ? '↑' : '↓'}
              </button>
            </div>
          )}
          {/* Portraits on/off. Beside the sort control because it is the same
              kind of thing — how you want to READ the shelf, not what is on it.
              Persisted and applied at the document root, so the studio roster,
              the cast strip, and the sheets follow without being told. */}
          <button
            class="stage-portraits-toggle"
            aria-pressed={!portraits}
            title={portraits ? t('Hide character portraits everywhere') : t('Show character portraits again')}
            onClick={() => {
              const next = !portraits
              setPortraits(next)
              applyPortraits(next)
            }}
          >
            {portraits ? t('🖼 Images on') : t('🖼 Images off')}
          </button>
          {cards.length > 1 && (
            <button
              class="stage-import"
              aria-pressed={selecting}
              onClick={() => (selecting ? endSelecting() : setSelecting(true))}
            >
              {selecting ? t('Done') : t('Select')}
            </button>
          )}
          {/* Making a character from nothing, which the Library could not do:
              cards only ever arrived by file or URL. It opens the studio in
              create mode — nothing is stored until the first save, so backing
              out leaves no half-made card behind. */}
          <button class="stage-import" onClick={() => onEditCharacter(null)}>
            {t('+ New character')}
          </button>
          <label class="stage-import">
            {t('+ Import')}
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
            placeholder={t('Paste a card URL (chub.ai, …)')}
            value={importUrl}
            disabled={busy}
            onInput={(e) => setImportUrl((e.target as HTMLInputElement).value)}
          />
          <button type="submit" class="stage-import-url__go" disabled={busy || !importUrl.trim()}>
            {t('Import URL')}
          </button>
        </form>
        {error && <p class="stage-error">{error}</p>}
        {/* Import succeeded, but the library changed something about the card —
            a downscaled or dropped portrait. Not an error; a stated reason for a
            surprise the user would otherwise read as a bug. */}
        {notes.map((n) => (
          <p key={n} class="stage-note">{n}</p>
        ))}
        {/* `ready` alone was not enough here: it only means the hello landed, and
            cards.list is a further round trip, so this still asserted an empty
            library for the length of that call. */}
        {cards.length === 0 && !loaded.cards && <Placeholder label={t('Loading your characters…')} rows={2} />}
        {cards.length === 0 && loaded.cards && (
          <p class="stage-empty">{t('No characters yet — drop a card PNG here, paste a URL, or use Import.')}</p>
        )}
        {/* Either half can be the reason nothing shows, so the message names
            the one that is actually on rather than sending the user to clear a
            chip when it was the search that emptied the grid. */}
        {/* Say that variants exist rather than hiding them silently: a fork the
            author accepted in a World is a card they may go looking for, and a
            shelf that simply does not contain it reads as a lost edit. */}
        {hiddenVariants > 0 && (
          <p class="stage-hint">
            {tn(
              hiddenVariants,
              '%d character is a World’s own version — open that World to see it.',
              '%d characters are a World’s own version — open that World to see them.',
              hiddenVariants,
            )}
          </p>
        )}
        {shelfCards.length > 0 && cardsNarrowed && visibleCards.length === 0 && (
          <p class="stage-empty">
            {cardTerms.length > 0 && hasFilter(cardFilter)
              ? t('No characters match the search and the group filter.')
              : cardTerms.length > 0
                ? t('No characters match “%s”.', cardQuery.trim())
                : t('No characters match the group filter — tap a highlighted chip to clear it.')}
          </p>
        )}

        {/* The bulk bar. It sits above the grid rather than floating over it:
            the grid is the thing you are picking from, and a bar that covers
            its last row while you work is the bug the suggest sheet already
            taught us. */}
        {selecting && (
          <div class="stage-bulkbar">
            <span class="stage-bulkbar__count">
              {selected.size === 0 ? t('Pick characters to act on') : tn(selected.size, '%d selected', '%d selected')}
            </span>
            <button
              class="stage-bulkbar__pick"
              onClick={() =>
                setSelected((prev) =>
                  prev.size === sortedCards.length ? new Set() : new Set(sortedCards.map((c) => c.id)),
                )
              }
            >
              {selected.size === sortedCards.length ? t('Select none') : t('Select all')}
            </button>
            {selected.size > 0 && (
              <>
                {cardGroupsSupported && cardGroups.length > 0 && (
                  <div class="stage-bulkbar__groups">
                    {cardGroups.map((g) => {
                      const state = bulkState(g, selected)
                      // One control, two meanings: a group that already holds
                      // ALL of the selection removes it; anything else adds.
                      const add = state !== 'all'
                      return (
                        <button
                          key={g.id}
                          class={`stage-bulkbar__group is-${state}`}
                          title={add ? t('Add the selected characters to %s', g.name) : t('Remove the selected characters from %s', g.name)}
                          onClick={() => void bulkGroup(g, add)}
                        >
                          {g.color && <span class="stage-bulkbar__dot" style={{ background: g.color }} />}
                          <span>{g.name}</span>
                          <span class="stage-bulkbar__mark">{state === 'all' ? '−' : state === 'some' ? '±' : '+'}</span>
                        </button>
                      )
                    })}
                  </div>
                )}
                <button class="stage-bulkbar__act" title={t('Favorite the selected characters')} onClick={() => void bulkFavorite(true)}>
                  ★
                </button>
                <button class="stage-bulkbar__act" title={t('Unfavorite the selected characters')} onClick={() => void bulkFavorite(false)}>
                  ☆
                </button>
              </>
            )}
          </div>
        )}
        <ul class={`stage-grid${selecting ? ' is-selecting' : ''}`}>
          {sortedCards.map((card) => (
            <li
              key={card.id}
              class={`stage-grid__cell${selecting && selected.has(card.id) ? ' is-picked' : ''}`}
              data-favorite={card.favorite ? '' : undefined}
            >
              <button
                class="stage-card"
                disabled={busy && !selecting}
                aria-pressed={selecting ? selected.has(card.id) : undefined}
                title={
                  selecting
                    ? selected.has(card.id)
                      ? t('Deselect %s', card.name)
                      : t('Select %s', card.name)
                    : chatsFor(card.id) > 0
                      ? tn(chatsFor(card.id), '%d chat with %s', '%d chats with %s', chatsFor(card.id), card.name)
                      : t('Chat with %s', card.name)
                }
                onClick={() => (selecting ? toggleSelected(card.id) : openCharacter(card))}
              >
                {selecting && <span class="stage-card__pick" aria-hidden="true">{selected.has(card.id) ? '✓' : ''}</span>}
                {card.avatar_url ? (
                  // Lazy: the grid is every character you own, and without this
                  // the whole library's portraits are requested at once on the
                  // first visit — the browser's connection limit then serialises
                  // them, so the ones you are actually looking at arrive behind
                  // the ones you are not.
                  <img class="stage-card__avatar" src={card.avatar_url} alt="" loading="lazy" />
                ) : (
                  <div class="stage-card__avatar stage-card__avatar--blank" aria-hidden="true" />
                )}
                <span class="stage-card__name">{card.name}</span>
                {/* Which World this character came from, said on the tile so the
                    shelf is legible at a glance. Its OWN line rather than inside
                    the meta row below, which is a space-between pair and would
                    put the creator in the middle with a third child.

                    NOT folded into the name: card.name is the {{char}} macro and
                    an input to the card's content-addressed id, so "Kira
                    (Bellhaven)" would be what the model calls her in dialogue. */}
                {worldName(card.world_of) && (
                  <span class="stage-card__world" title={t('Created in the World “%s”', worldName(card.world_of))}>
                    🌍 {worldName(card.world_of)}
                  </span>
                )}
                <span class="stage-card__meta">
                  {card.creator && <span class="stage-card__creator">{card.creator}</span>}
                  {chatsFor(card.id) > 0 && <span class="stage-card__count">·{chatsFor(card.id)}</span>}
                </span>
              </button>
              <button
                class="stage-card__fav"
                title={card.favorite ? t('Unfavorite %s', card.name) : t('Favorite %s', card.name)}
                aria-pressed={!!card.favorite}
                onClick={() => void toggleFavorite(card)}
              >
                {card.favorite ? '★' : '☆'}
              </button>
              <button class="stage-card__more" title={t('Details')} onClick={() => setSheet(card)}>
                ⋯
              </button>
            </li>
          ))}
        </ul>

        {worldsSupported && (
          <>
            <div class="stage-section-head">
              <h2>{t('Worlds')}</h2>
              <label class="stage-import">
                {t('+ Import')}
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
            {/* No placeholder needed: the whole shelf is gated on
                `worldsSupported`, which only the answer sets — so an
                unanswered worlds.list renders nothing at all rather than
                claiming you have no Worlds. */}
            {worlds.length === 0 && (
              <p class="stage-empty">{t('No Worlds yet — save a chat as one (Steering → World tab) or import a World bundle.')}</p>
            )}
            <ul class="stage-worlds">
              {worlds.map((wld) => (
                // The tile and its delete are SIBLINGS, not nested: the tile is
                // a <button> and a button inside a button is invalid markup that
                // browsers resolve by dropping one of them.
                <li key={wld.id} class="stage-world__row">
                  <button class="stage-world" disabled={busy} title={wld.description || wld.name} onClick={() => props.onOpenWorld(wld.id)}>
                    {wld.cover_url ? (
                      <img class="stage-world__cover" src={wld.cover_url} alt="" />
                    ) : (
                      <span class="stage-world__icon" aria-hidden="true">🌍</span>
                    )}
                    <span class="stage-world__text">
                      <span class="stage-world__name">{wld.name}</span>
                      <span class="stage-world__meta">
                        {tn(Object.keys(wld.characters ?? {}).length, '%d character', '%d characters')}
                        {(wld.sessions ?? 0) > 0 && <span> · {tn(wld.sessions ?? 0, '%d chat', '%d chats')}</span>}
                      </span>
                    </span>
                  </button>
                  {/* Deleting a World is a SHELF act — you do it while looking at
                      the ones you want gone, usually several in a row. It moved
                      into the studio when the World drawer became a screen, which
                      turned "clear out four test Worlds" into four navigations,
                      four scrolls past the cast and the lorebook, and four trips
                      back. Nothing on the shelf said the operation still existed.

                      Always visible rather than revealed on hover: Stage is used
                      on phones, where there is no hover and a hidden control is
                      an absent one. Quiet enough not to compete with the tile,
                      and the confirm carries the weight. */}
                  <button
                    class="stage-world__del"
                    disabled={busy}
                    title={t('Delete the World “%s”', wld.name)}
                    aria-label={t('Delete the World “%s”', wld.name)}
                    onClick={() => void deleteWorld(wld)}
                  >
                    ✕
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}

        {/* Your chats never claimed emptiness — the whole section is gated on
            having some — but it said nothing either, so a library still fetching
            them looked like a library with none. Name the wait under its own
            heading, so the section that is about to appear has a place already. */}
        {chats.length === 0 && !loaded.sessions && (
          <>
            <div class="stage-section-head">
              <h2>{t('Your chats')}</h2>
            </div>
            <Placeholder label={t('Loading your chats…')} rows={2} />
          </>
        )}

        {chats.length > 0 && (
          <>
            {/* Chat groups: the derived origin chips (a World / card the chats came
                from) come first, then your own session groups. Tap to show-only →
                hide → clear; "⋯" edits a user group (origin chips have none). */}
            {(sessionGroupsSupported || originChatGroups.length > 0) && (
              <div class="stage-cardgroups">
                <div class="stage-section-head">
                  <h2>{t('Chat groups')}</h2>
                  {sessionGroupsSupported && (
                    <button class="stage-import" onClick={() => void createSessionGroup()}>
                      {t('+ New')}
                    </button>
                  )}
                </div>
                {chatGroups.length === 0 ? (
                  <p class="stage-empty">{t('Group your chats — playtests, ongoing stories, whatever helps you find them again.')}</p>
                ) : (
                  <GroupFilterChips
                    groups={chatGroups}
                    filter={chatFilter}
                    onCycle={(id) => setChatFilter((f) => cycleGroup(f, id))}
                    onManage={sessionGroupsSupported ? (g) => setSessionGroupSheet(g) : undefined}
                  />
                )}
              </div>
            )}
            <div class="stage-section-head">
              <h2>{t('Your chats')}</h2>
            </div>
            {hasFilter(chatFilter) && filteredChats.length === 0 && (
              <p class="stage-empty">{t('No chats match the group filter — tap a highlighted chip to clear it.')}</p>
            )}
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
                        <span class="stage-yourchats__title">{s.title || c?.name || t('Untitled')}</span>
                        <span class="stage-yourchats__sub">
                          {c && <span class="stage-yourchats__char">{c.name}</span>}
                          {s.experience === 'play' && <span class="stage-yourchats__exp">{t('play')}</span>}
                          {(s.messages ?? 0) > 0 && <span>· {t('%d msg', s.messages ?? 0)}</span>}
                        </span>
                      </span>
                      {when && <span class="stage-yourchats__when">{when}</span>}
                    </button>
                    {worldsSupported && !s.world && (
                      <button class="stage-yourchats__world" title={t('Save this chat as a World')} aria-label={t('Save this chat as a World')} onClick={() => void saveAsWorld(s)}>
                        🗺
                      </button>
                    )}
                    <button class="stage-yourchats__archive" title={t('Archive this chat')} aria-label={t('Archive this chat')} onClick={() => void archiveSession(s)}>
                      📥
                    </button>
                    <button class="stage-yourchats__del" title={t('Delete this chat')} aria-label={t('Delete this chat')} onClick={() => void deleteSession(s)}>
                      🗑
                    </button>
                  </li>
                )
              })}
            </ul>
            {filteredChats.length > CHATS_SHOWN && (
              <button class="stage-yourchats__more" onClick={() => setShowAllChats((v) => !v)}>
                {showAllChats ? t('Show fewer') : t('Show all %d chats', filteredChats.length)}
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
              <h2>{t('Personas')}</h2>
              <button class="stage-import" onClick={() => setPersonaEditor({ mode: 'new' })}>
                {t('+ New')}
              </button>
            </div>
            {/* Unlike the sections above, an unanswered roster here rendered no
                sentence — just a bare empty list under a heading, which reads as
                "this workspace has no personas" just as convincingly. */}
            {personas.length === 0 && !loaded.personas && <Placeholder label={t('Loading personas…')} rows={2} />}
            {/* Shelved by group. A lone shelf goes unlabelled, so a roster with
                one group — or a daemon too old to serve any — reads as the plain
                list it always was. */}
            {personaShelves.map((shelf) => (
              <div key={shelf.name} class="stage-personashelf">
                {personaShelves.length > 1 && (
                  <div class="stage-personashelf__name">{shelf.name || t('Other')}</div>
                )}
                <ul class="stage-personas">
                  {shelf.personas.map((p) => (
                    <li key={p.ref}>
                      <button class="stage-persona" title={t('About %s', p.name)} onClick={() => setPersonaSheet(p)}>
                        <span class="stage-persona__emoji" aria-hidden="true">
                          {p.emoji || '🎭'}
                        </span>
                        <span class="stage-persona__name">{p.name}</span>
                        <span class="stage-persona__origin">{p.origin}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
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
          groups={cardGroupsSupported ? cardGroups : undefined}
          onToggleGroup={(id) => {
            const g = cardGroups.find((x) => x.id === id)
            if (g) void toggleCardInGroup(sheet.id, g)
          }}
          onClose={() => setSheet(null)}
          onStart={(g) => void startChat(sheet, g)}
          onEdit={() => {
            setSheet(null)
            onEditCharacter(sheet)
          }}
          onDuplicate={() => void duplicateCard(sheet)}
          onDelete={() => void deleteCard(sheet)}
        />
      )}

      {groupSheet && (
        <GroupSheet
          group={groupSheet}
          cards={groupSheet.members.map((id) => cardById.get(id)).filter((c): c is CardSummary => !!c)}
          onOpenCard={(card) => {
            setGroupSheet(null)
            setSheet(card)
          }}
          onRemoveCard={(cardId) => void toggleCardInGroup(cardId, groupSheet)}
          onSave={(name, color) => void saveCardGroup(groupSheet.id, name, color)}
          onDelete={() => void deleteCardGroup(groupSheet)}
          onFilter={() => {
            setCardFilter({ include: [groupSheet.id], exclude: [] })
            setGroupSheet(null)
          }}
          onClose={() => setGroupSheet(null)}
        />
      )}

      {sessionGroupSheet && (
        <SessionGroupSheet
          group={sessionGroupSheet}
          members={sessionGroupSheet.members.map((id) => sessions.find((s) => s.id === id)).filter((s): s is SessionInfo => !!s)}
          candidates={chats.filter((s) => !sessionGroupSheet.members.includes(s.id))}
          cardById={cardById}
          onOpenChat={onOpenChat}
          onToggle={(sessionId) => void toggleSessionInGroup(sessionId, sessionGroupSheet)}
          onSave={(name, color) => void saveSessionGroup(sessionGroupSheet.id, name, color)}
          onDelete={() => void deleteSessionGroup(sessionGroupSheet)}
          onFilter={() => {
            setChatFilter({ include: [sessionGroupSheet.id], exclude: [] })
            setSessionGroupSheet(null)
          }}
          onClose={() => setSessionGroupSheet(null)}
        />
      )}

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
          groups={personaGroupNames(personas)}
          onClose={() => setPersonaEditor(null)}
          onSaved={load}
        />
      )}

    </div>
  )
}
