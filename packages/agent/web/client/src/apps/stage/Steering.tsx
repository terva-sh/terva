import { useEffect, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import { copyToClipboard, downloadExport } from '../../ui/browser'
import { panelHref } from '../../ui/navlinks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { SessionInfo, BackgroundView, BackgroundsListResult, CardView, DoctorProposal, DoctorResult, LoreView, SessionExport, Surface, UserPersonaView, UserPersonasListResult } from '../../platform/ctrlproto/types'
import { ModelPick } from './ModelPick'
import { SessionDoctor } from './SessionDoctor'
import { SCENE_STATE_NAME, sceneStateOf, scenePinDrift } from './SceneState'
import { IdentityField, PRONOUN_OPTIONS, GENDER_OPTIONS } from './identity'

async function fileToBase64(file: File): Promise<string> {
  const buf = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  for (let i = 0; i < buf.length; i++) binary += String.fromCharCode(buf[i])
  return btoa(binary)
}

// The steering drawer — the progressive-disclosure power surface. This pass
// carries what exists over Phase-2 primitives: session details, a live scene
// picker (backgrounds.list/import/bind), and the character's lorebook (the
// existing lore surface — ST's opacity complaint, answered by showing exactly
// which entries drive the character and when). Author's note, user-persona
// binding, and the activation trace are Phase-4 steering depth.
// onNextScene opens the scene-break flow (SD5). It lives in Chat, which owns
// session switching — the drawer only asks for it, optionally seeding a title
// the doctor proposed.
export function Steering(props: {
  client: ClientLike
  sessionId: string
  info: SessionInfo | null
  onClose: () => void
  onNextScene?: (suggestedTitle: string) => void
}) {
  const { client, sessionId, info, onClose } = props
  const [backgrounds, setBackgrounds] = useState<BackgroundView[]>([])
  const [lore, setLore] = useState<LoreView | null>(null)
  const [error, setError] = useState('')
  // The drawer is tabbed so it stops being one long scroll: World (the shared
  // container — roster + World lore, the reserved left flank), Session (title/
  // mode/model + the play cast), You (your persona), Scene (author's note,
  // backgrounds, lorebook). Session stays the landing tab — a World of one is
  // invisible until you reach for it.
  const [tab, setTab] = useState<'world' | 'session' | 'you' | 'scene'>('session')
  // The author's note is edited locally and committed on blur; the snapshot fold
  // re-seeds it (from info.note) on a session switch or an external change.
  const [noteDraft, setNoteDraft] = useState(info?.note ?? '')
  // Which diagnostics row last copied, so the button can confirm it landed —
  // a clipboard write is otherwise completely silent.
  const [copied, setCopied] = useState<'id' | 'path' | ''>('')
  // Export is a round trip to the daemon on a long transcript, so the button
  // says so rather than looking inert.
  const [exporting, setExporting] = useState(false)
  const exportStory = () => {
    setError('')
    setExporting(true)
    client
      .send<SessionExport>('sessions.export', { format: 'markdown' }, sessionId)
      .then(downloadExport)
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setExporting(false))
  }
  const copyRow = (which: 'id' | 'path', text: string) => {
    void copyToClipboard(text).then((ok) => {
      if (!ok) return
      setCopied(which)
      setTimeout(() => setCopied(''), 1200)
    })
  }
  useEffect(() => {
    setNoteDraft(info?.note ?? '')
  }, [sessionId, info?.note])
  // The user persona — name + description — edited locally, committed together on
  // blur, re-seeded from the snapshot the same way as the note.
  const [userName, setUserName] = useState(info?.user_name ?? '')
  const [userDesc, setUserDesc] = useState(info?.user_description ?? '')
  const [userGender, setUserGender] = useState(info?.user_gender ?? '')
  const [userPronouns, setUserPronouns] = useState(info?.user_pronouns ?? '')
  useEffect(() => {
    setUserName(info?.user_name ?? '')
    setUserDesc(info?.user_description ?? '')
    setUserGender(info?.user_gender ?? '')
    setUserPronouns(info?.user_pronouns ?? '')
  }, [sessionId, info?.user_name, info?.user_description, info?.user_gender, info?.user_pronouns])
  // The saved user personas (userpersonas.*) — a small library of reusable "who I
  // am" identities the user can swap between or mark as the default new chats
  // pre-fill. Loaded once; re-fetched after a save/delete/default change.
  const [savedPersonas, setSavedPersonas] = useState<UserPersonaView[]>([])
  const loadUserPersonas = () =>
    client
      .send<UserPersonasListResult>('userpersonas.list', {})
      .then((r) => setSavedPersonas(r.personas ?? []))
      .catch(() => {})
  // The cast add form (play sessions only). The roster itself re-renders from the
  // snapshot fold after each cast.add/remove, so there is no local roster state.
  const [castName, setCastName] = useState('')
  const [castRef, setCastRef] = useState('')
  // An optional per-actor model pin for the new cast member (Phase 7): empty = the
  // actor inherits the session/host model.
  const [castProvider, setCastProvider] = useState('')
  const [castModel, setCastModel] = useState('')
  // Generate-a-scene: a prompt + a pending flag (generation is a slow backend
  // round-trip). The result auto-binds, so the scene updates via the snapshot.
  const [scenePrompt, setScenePrompt] = useState('')
  const [generating, setGenerating] = useState(false)
  // The World lore editor (Worlds L1). The entry LIST re-renders from the
  // snapshot fold (info.world_lore) after each put/delete — only the form is
  // local. loreEditing holds the original name while editing (world.lore.put's
  // replace), '' when composing a new entry.
  const [loreName, setLoreName] = useState('')
  const [loreKeys, setLoreKeys] = useState('')
  const [loreAlways, setLoreAlways] = useState(false)
  const [loreContent, setLoreContent] = useState('')
  const [loreEditing, setLoreEditing] = useState('')
  // Who knows this entry (L2): empty = everyone on stage. Toggled by chips —
  // the bound character + the roster.
  const [loreAudience, setLoreAudience] = useState<string[]>([])
  const resetLoreForm = () => {
    setLoreName('')
    setLoreKeys('')
    setLoreAlways(false)
    setLoreContent('')
    setLoreEditing('')
    setLoreAudience([])
  }
  useEffect(() => {
    resetLoreForm()
  }, [sessionId])
  // The bound character's name, for the audience chips (the roster names come
  // from info.cast; the session's own character needs its card resolved once).
  const [boundName, setBoundName] = useState('')
  useEffect(() => {
    setBoundName('')
    if (!info?.card) return
    client
      .send<{ name?: string }>('cards.get', { id: info.card })
      .then((c) => setBoundName(c.name ?? ''))
      .catch(() => {})
  }, [sessionId, info?.card])
  const audienceChoices = [...(boundName ? [boundName] : []), ...Object.keys(info?.cast ?? {})].filter((n, i, a) => a.indexOf(n) === i)
  const toggleAudience = (name: string) =>
    setLoreAudience((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]))
  // The scene-state pin renders above the composer (SD4), not in this list.
  const worldLoreListed = (info?.world_lore ?? []).filter((e) => e.name.trim().toLowerCase() !== SCENE_STATE_NAME.toLowerCase())

  const loadBackgrounds = () =>
    client
      .send<BackgroundsListResult>('backgrounds.list', {})
      .then((r) => setBackgrounds(r.backgrounds ?? []))
      .catch(() => {})

  useEffect(() => {
    void loadBackgrounds()
    void loadUserPersonas()
    client
      .send<Surface>('surface.get', { id: 'lore' }, sessionId)
      .then((s) => setLore(s.lore ?? null))
      .catch(() => setLore(null))
  }, [sessionId])

  // Bind a saved persona to this session by ref; the daemon resolves its name +
  // description, and the snapshot fold re-seeds the fields.
  const bindSaved = (ref: string) => client.send('user.bind', { ref }, sessionId).catch((e: unknown) => setError(String(e)))
  // Save the current name + description as a reusable persona; makeDefault also
  // marks it the identity new chats pre-fill.
  const savePersona = (makeDefault: boolean) => {
    const name = userName.trim()
    if (!name) {
      setError(t('Give your persona a name to save it.'))
      return
    }
    setError('')
    client
      .send<UserPersonaView>('userpersonas.save', { name, description: userDesc.trim(), gender: userGender.trim(), pronouns: userPronouns.trim() })
      .then((saved) => (makeDefault && saved.ref ? client.send('userpersonas.set_default', { ref: saved.ref }) : undefined))
      .then(() => loadUserPersonas())
      .catch((e: unknown) => setError(String(e)))
  }
  const deletePersona = (ref: string) =>
    client
      .send('userpersonas.delete', { ref })
      .then(() => loadUserPersonas())
      .catch((e: unknown) => setError(String(e)))

  const commitNote = () => {
    if (noteDraft === (info?.note ?? '')) return // no change since the last committed value
    client.send('note.set', { text: noteDraft }, sessionId).catch((e: unknown) => setError(String(e)))
  }
  // Rename this chat, or ✨-regenerate its title from the conversation — the same
  // two verbs the panel's session drawer uses. Both broadcast session_updated, so
  // info.title and the chat header refresh live; no manual refetch here.
  const renameSession = () => {
    const next = window.prompt(t('Rename this chat'), info?.title || '')
    if (next == null) return
    client.send('sessions.rename', { title: next.trim() }, sessionId).catch((e: unknown) => setError(String(e)))
  }
  const regenerateTitle = () => {
    setError('')
    client.send('sessions.generate_title', null, sessionId).catch((e: unknown) => setError(String(e)))
  }
  // All four fields ride one verb; the daemon rebuilds the prefix only when the
  // name actually changed, so a description/gender/pronouns edit stays a free tail
  // update. `over` lets a dropdown pass its just-picked value through so the commit
  // never reads stale state (a select's onChange fires before the state settles).
  const commitUser = (over?: { gender?: string; pronouns?: string }) => {
    const gender = over?.gender ?? userGender
    const pronouns = over?.pronouns ?? userPronouns
    if (
      userName === (info?.user_name ?? '') &&
      userDesc === (info?.user_description ?? '') &&
      gender === (info?.user_gender ?? '') &&
      pronouns === (info?.user_pronouns ?? '')
    )
      return // no change since the last committed value
    client.send('user.bind', { name: userName, description: userDesc, gender, pronouns }, sessionId).catch((e: unknown) => setError(String(e)))
  }
  const castAdd = () => {
    const name = castName.trim()
    const ref = castRef.trim()
    if (!name || !ref) return
    client
      .send('cast.add', { name, ref, provider: castProvider, model: castModel }, sessionId)
      .then(() => {
        setCastName('')
        setCastRef('')
        setCastProvider('')
        setCastModel('')
      })
      .catch((e: unknown) => setError(String(e)))
  }
  const castRemove = (name: string) => client.send('cast.remove', { name }, sessionId).catch((e: unknown) => setError(String(e)))
  // World lore verbs. Save clears the form only on success; the list itself
  // arrives back through the snapshot.
  const parsedLoreKeys = loreKeys
    .split(',')
    .map((k) => k.trim())
    .filter(Boolean)
  const worldLoreSave = () => {
    const name = loreName.trim()
    const content = loreContent.trim()
    if (!name || !content || (!loreAlways && parsedLoreKeys.length === 0)) return
    setError('')
    client
      .send(
        'world.lore.put',
        {
          entry: {
            name,
            keys: loreAlways ? undefined : parsedLoreKeys,
            constant: loreAlways,
            content,
            audience: loreAudience.length > 0 ? loreAudience : undefined,
          },
          replace: loreEditing || undefined,
        },
        sessionId,
      )
      .then(resetLoreForm)
      .catch((e: unknown) => setError(String(e)))
  }
  const worldLoreEdit = (e: { name: string; keys?: string[]; constant?: boolean; content: string; audience?: string[] }) => {
    setLoreName(e.name)
    setLoreKeys((e.keys ?? []).join(', '))
    setLoreAlways(!!e.constant)
    setLoreContent(e.content)
    setLoreEditing(e.name)
    setLoreAudience(e.audience ?? [])
  }
  const worldLoreDelete = (name: string) => {
    if (loreEditing === name) resetLoreForm()
    client.send('world.lore.delete', { name }, sessionId).catch((e: unknown) => setError(String(e)))
  }
  // The meta-narrator setting (W3): who answers a normal turn. Committed on
  // pick; the snapshot fold re-renders the current value (info.coordination).
  const setCoordination = (mode: string) => client.send('world.set', { coordination: mode }, sessionId).catch((e: unknown) => setError(String(e)))
  // Saving the World (W5): promote the session's embedded World into the
  // library, or write changes back to the one it belongs to. Explicit only —
  // play never syncs live.
  const [worldName, setWorldName] = useState('')
  const [worldSaving, setWorldSaving] = useState(false)
  const [worldSavedNote, setWorldSavedNote] = useState('')
  useEffect(() => {
    setWorldName('')
    setWorldSavedNote('')
  }, [sessionId])
  const saveWorld = () => {
    const name = worldName.trim()
    if (!info?.world && !name) return
    setWorldSaving(true)
    setError('')
    client
      .send<{ name: string }>('worlds.save', info?.world ? {} : { name }, sessionId)
      .then((v) => {
        setWorldName('')
        setWorldSavedNote(info?.world ? t('Saved to “%s”.', v.name) : t('“%s” is now a saved World — new chats can start inside it from the Library.', v.name))
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setWorldSaving(false))
  }
  // The editor (W4): ✏️ enrich a character's card from THIS scene — the
  // doctor's negotiation loop grounded in the session (cards.doctor {session}).
  const [enrich, setEnrich] = useState<{ ref: string; name: string } | null>(null)
  const [enrichProposals, setEnrichProposals] = useState<DoctorProposal[] | null>(null)
  const [enrichNote, setEnrichNote] = useState('')
  const [enrichRunning, setEnrichRunning] = useState(false)
  const [enrichApplying, setEnrichApplying] = useState(false)
  // Per-proposal verdicts: unset = undecided, true = accept, false = decline
  // (with an optional reason the editor weighs on a revise round).
  const [enrichVerdicts, setEnrichVerdicts] = useState<Record<string, boolean>>({})
  const [enrichReasons, setEnrichReasons] = useState<Record<string, string>>({})
  // Per-generation model override for the editor run; empty = the session model
  // (the daemon's default now that editor mode resolves against the session).
  const [enrichProvider, setEnrichProvider] = useState('')
  const [enrichModel, setEnrichModel] = useState('')
  // The author's standing instruction for this enrichment — "keep her terse",
  // "fold in what she learned about the ledger". Rides every round, the same
  // steer the studio's card doctor takes.
  const [enrichSteer, setEnrichSteer] = useState('')
  const resetEnrich = () => {
    setEnrich(null)
    setEnrichProposals(null)
    setEnrichNote('')
    setEnrichVerdicts({})
    setEnrichReasons({})
    setEnrichProvider('')
    setEnrichModel('')
    setEnrichSteer('')
  }
  useEffect(() => {
    resetEnrich()
  }, [sessionId])
  // ✏️ EXPANDS the editor for a character without running it — the model picker
  // and a dedicated "Read the scene" button live in the panel, so the run always
  // uses the model you chose. (It used to fire cards.doctor the instant ✏️ was
  // clicked, before the picker was even visible, so every run went to the default
  // model no matter what you set the picker to afterwards.)
  const openEnrich = (target: { ref: string; name: string }) => {
    setEnrich(target)
    setEnrichProposals(null)
    setEnrichNote('')
    setEnrichVerdicts({})
    setEnrichReasons({})
    setEnrichProvider('')
    setEnrichModel('')
    setEnrichSteer('')
    setError('')
  }
  const runEnrich = (target: { ref: string; name: string }, decisions?: unknown[]) => {
    setEnrich(target)
    setEnrichRunning(true)
    setError('')
    client
      .send<DoctorResult>('cards.doctor', { id: target.ref, session: sessionId, decisions, steer: enrichSteer, provider: enrichProvider, model: enrichModel }, sessionId)
      .then((r) => {
        setEnrichProposals(r.proposals ?? [])
        setEnrichNote(r.note ?? '')
        setEnrichVerdicts({})
        setEnrichReasons({})
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setEnrichRunning(false))
  }
  const enrichRevise = () => {
    if (!enrich || !enrichProposals) return
    const decisions = enrichProposals
      .filter((p) => p.id in enrichVerdicts)
      .map((p) => ({ proposal_id: p.id, field: p.field, rationale: p.rationale, accepted: enrichVerdicts[p.id], reason: enrichVerdicts[p.id] ? undefined : enrichReasons[p.id] || undefined }))
    runEnrich(enrich, decisions)
  }
  // Apply the accepted proposals: read the card's raw document, replace the
  // accepted fields wholesale (the contract: `after` is the complete value —
  // empty for a removal, which is the point of it), save through the ordinary
  // cards.edit.
  //
  // The fields of a CCv2 document live under `data`, and cards.edit RE-PARSES
  // what it is given: a field written at the top level of a v2 wrapper is parsed
  // off and lost, so every accepted edit here used to be silently discarded
  // while the panel closed as though it had landed. Mirror the parser's own
  // branch — nested for a spec'd document, flat for a bare v1 one — rather than
  // assuming either shape.
  const enrichApply = async () => {
    if (!enrich || !enrichProposals) return
    const accepted = enrichProposals.filter((p) => enrichVerdicts[p.id])
    if (accepted.length === 0) return
    setEnrichApplying(true)
    setError('')
    try {
      const view = await client.send<CardView>('cards.get', { id: enrich.ref })
      const raw = { ...((view.raw ?? {}) as Record<string, unknown>) }
      const spec = typeof raw.spec === 'string' ? raw.spec : ''
      const nested = spec.startsWith('chara_card_v') && typeof raw.data === 'object' && raw.data !== null
      const fields = nested ? { ...(raw.data as Record<string, unknown>) } : raw
      for (const p of accepted) fields[p.field] = p.after
      if (nested) raw.data = fields
      await client.send('cards.edit', { id: enrich.ref, card: raw })
      resetEnrich()
    } catch (e) {
      setError(String(e))
    } finally {
      setEnrichApplying(false)
    }
  }
  const bind = (id: string) => client.send('backgrounds.bind', { background: id }, sessionId).catch((e: unknown) => setError(String(e)))
  // Paint a scene from a prompt; the daemon generates, stores, and binds it, so
  // the new backdrop arrives via the snapshot. Refresh the tiles so it joins them.
  const generateScene = () => {
    const prompt = scenePrompt.trim()
    if (!prompt) return
    setGenerating(true)
    setError('')
    client
      .send('backgrounds.generate', { prompt }, sessionId)
      .then(() => {
        setScenePrompt('')
        void loadBackgrounds()
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setGenerating(false))
  }
  // Remove a backdrop from the workspace store (backgrounds.delete).
  //
  // Two things the daemon does NOT do, both of which have to happen here. It
  // does not check whether anything is using the backdrop — the store is global
  // but a binding is per-session (SessionMeta.Background), so other chats may be
  // on it and this drawer cannot see them; hence the warning. And it does not
  // clear the binding, so deleting the one this chat is on would leave a dangling
  // id whose /media URL 404s — a CSS background-image that fails silently, i.e. a
  // scene that just goes bare with nothing to explain it. Unbind FIRST, so a
  // failed delete leaves us merely unbound rather than bound to nothing.
  const deleteBg = async (id: string) => {
    if (!window.confirm(t('Delete this backdrop? It is removed from the library for good, and any other chat using it loses its scene.'))) return
    try {
      if (info?.background === id) await bind('')
      await client.send('backgrounds.delete', { id })
      void loadBackgrounds()
    } catch (e) {
      setError(String(e))
    }
  }
  const importBg = async (files: FileList) => {
    for (const f of Array.from(files)) {
      try {
        await client.send('backgrounds.import', { bytes: await fileToBase64(f) })
      } catch (e) {
        setError(String(e))
      }
    }
    void loadBackgrounds()
  }

  return (
    <div class="stage-drawer-backdrop" onClick={onClose}>
      <aside class="stage-drawer" onClick={(e) => e.stopPropagation()}>
        <header class="stage-drawer__head">
          <h3>{t('Steering')}</h3>
          <button class="stage-drawer__close" onClick={onClose}>✕</button>
        </header>
        <nav class="stage-drawer__tabs">
          <button class={`stage-drawer__tab ${tab === 'world' ? 'stage-drawer__tab--on' : ''}`} onClick={() => setTab('world')}>{t('World')}</button>
          <button class={`stage-drawer__tab ${tab === 'session' ? 'stage-drawer__tab--on' : ''}`} onClick={() => setTab('session')}>{t('Session')}</button>
          <button class={`stage-drawer__tab ${tab === 'you' ? 'stage-drawer__tab--on' : ''}`} onClick={() => setTab('you')}>{t('You')}</button>
          <button class={`stage-drawer__tab ${tab === 'scene' ? 'stage-drawer__tab--on' : ''}`} onClick={() => setTab('scene')}>{t('Scene')}</button>
        </nav>
        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {/* World tab: the shared container this session lives in (Worlds W2) —
            who is on stage, and the lore they all share. A World of one is just
            an empty tab; it grows without changing anything else. */}
        {tab === 'world' && (
        <>
        <section class="stage-drawer__section">
          <h4>{t('This World')}</h4>
          {info?.world ? (
            <>
              <p class="stage-hint">{t('This session belongs to a saved World. Its roster and lore here are a working copy — save your changes back when the World itself should remember them.')}</p>
              <button class="stage-worldsave__go" disabled={worldSaving} onClick={saveWorld}>
                {worldSaving ? t('Saving…') : t('Save changes to World')}
              </button>
            </>
          ) : (
            <>
              <p class="stage-hint">{t("Save this session's World — the roster, its lore and secrets, who replies — as a named World you can start new chats inside.")}</p>
              <form
                class="stage-worldsave"
                onSubmit={(e) => {
                  e.preventDefault()
                  saveWorld()
                }}
              >
                <input
                  class="stage-worldsave__name"
                  placeholder={t('World name (e.g. Lowtown)')}
                  value={worldName}
                  onInput={(e) => setWorldName((e.target as HTMLInputElement).value)}
                />
                <button type="submit" class="stage-worldsave__go" disabled={worldSaving || !worldName.trim()}>
                  {worldSaving ? t('Saving…') : t('Save as World')}
                </button>
              </form>
            </>
          )}
          {worldSavedNote && <p class="stage-hint">{worldSavedNote}</p>}
        </section>

        <section class="stage-drawer__section">
          <h4>{t('On stage')}</h4>
          {info?.experience === 'play' ? (
            <p class="stage-hint">{t('The play cast — with per-actor model pins — lives in the Session tab.')}</p>
          ) : (
            <>
              <p class="stage-hint">{t("Characters kept on stage. Voice one from the ✨ suggest menu (Character → pick from the roster); they share this World's lore. ✏️ enriches a character's card from what this scene established.")}</p>
              {Object.keys(info?.cast ?? {}).length === 0 && <p class="stage-empty">{t('No one yet — pick a character in the ✨ suggest menu and keep them on stage.')}</p>}
              <ul class="stage-cast">
                {boundName && info?.card && (
                  <li key="__bound" class="stage-cast__member">
                    <span class="stage-cast__name">{boundName}</span>
                    <span class="stage-cast__ref">{t('main character')}</span>
                    <button class="stage-worldlore__act" title={t("Enrich %s's card from this scene", boundName)} onClick={() => openEnrich({ ref: info.card!, name: boundName })}>
                      ✏️
                    </button>
                  </li>
                )}
                {Object.entries(info?.cast ?? {})
                  .filter(([, ref]) => ref !== info?.card)
                  .map(([name, ref]) => (
                    <li key={name} class="stage-cast__member">
                      <span class="stage-cast__name">{name}</span>
                      <span class="stage-cast__ref">{ref}</span>
                      <button class="stage-worldlore__act" title={t("Enrich %s's card from this scene", name)} onClick={() => openEnrich({ ref, name })}>
                        ✏️
                      </button>
                      <button class="stage-cast__remove" title={t('Remove %s', name)} onClick={() => void castRemove(name)}>
                        ✕
                      </button>
                    </li>
                  ))}
              </ul>
              {enrich && (
                <div class="stage-enrich">
                  <div class="stage-enrich__head">
                    <h5 class="stage-enrich__title">{t('✏️ Enrich %s from this scene', enrich.name)}</h5>
                    <button class="stage-worldlore__act" title={t('Close')} onClick={resetEnrich}>✕</button>
                  </div>
                  {/* Which model the editor runs on — the session's by default, or a
                      per-run override. Pick it before starting: it takes effect on
                      "Read the scene" below and on each "Revise with feedback" round. */}
                  <ModelPick
                    client={client}
                    sessionId={sessionId}
                    currentProvider={enrichProvider}
                    currentModel={enrichModel}
                    onSelect={(p, m) => {
                      setEnrichProvider(p)
                      setEnrichModel(m)
                    }}
                    defaultLabel={t('Session model')}
                    defaultProvider={info?.provider}
                    defaultModel={info?.model}
                  />
                  {/* Optional direction for the run, the same steer the studio's
                      card doctor takes. Stays put between rounds so "Revise with
                      feedback" carries it too. */}
                  <textarea
                    class="stage-enrich__steer"
                    rows={2}
                    placeholder={t('Optional: what should change? e.g. keep her terse, fold in what she learned')}
                    value={enrichSteer}
                    onInput={(e) => setEnrichSteer((e.target as HTMLTextAreaElement).value)}
                  />
                  {/* Not run yet: the picker above is live, so start the read with
                      a dedicated button rather than firing on open. */}
                  {!enrichRunning && enrichProposals === null && (
                    <div class="stage-enrich__start">
                      <p class="stage-hint">{t('Pick the model above, then read the scene to draft edits for %s’s card.', enrich.name)}</p>
                      <button class="stage-worldlore-form__save" onClick={() => runEnrich(enrich)}>
                        {t('✨ Read the scene')}
                      </button>
                    </div>
                  )}
                  {enrichRunning && <p class="stage-hint">{t('The editor is reading the scene…')}</p>}
                  {!enrichRunning && enrichNote && <p class="stage-hint">{enrichNote}</p>}
                  {!enrichRunning && enrichProposals && enrichProposals.length === 0 && !enrichNote && (
                    <p class="stage-empty">{t('Nothing to fold back yet — play a little more first.')}</p>
                  )}
                  {!enrichRunning &&
                    (enrichProposals ?? []).map((p) => (
                      <div key={p.id} class={`stage-enrich__proposal ${enrichVerdicts[p.id] === false ? 'stage-enrich__proposal--declined' : ''}`}>
                        <div class="stage-enrich__meta">
                          <span class="stage-enrich__field">{p.field}</span>
                          <span class="stage-enrich__why">{p.rationale}</span>
                          <button
                            class={`stage-enrich__verdict ${enrichVerdicts[p.id] === true ? 'stage-enrich__verdict--on' : ''}`}
                            title={t('Accept this edit')}
                            onClick={() => setEnrichVerdicts((v) => ({ ...v, [p.id]: true }))}
                          >
                            ✓
                          </button>
                          <button
                            class={`stage-enrich__verdict ${enrichVerdicts[p.id] === false ? 'stage-enrich__verdict--off' : ''}`}
                            title={t('Decline this edit')}
                            onClick={() => setEnrichVerdicts((v) => ({ ...v, [p.id]: false }))}
                          >
                            ✗
                          </button>
                        </div>
                        {p.before && <pre class="stage-enrich__text stage-enrich__text--before">{p.before}</pre>}
                        {/* A removal's `after` is empty on purpose — say so, or
                            the proposal renders as a blank box. */}
                        <pre class="stage-enrich__text stage-enrich__text--after">
                          {p.remove ? t('(this field is cleared)') : p.after}
                        </pre>
                        {enrichVerdicts[p.id] === false && (
                          <input
                            class="stage-enrich__reason"
                            placeholder={t('Why not? (the editor revises with this)')}
                            value={enrichReasons[p.id] ?? ''}
                            onInput={(e) => setEnrichReasons((r) => ({ ...r, [p.id]: (e.target as HTMLInputElement).value }))}
                          />
                        )}
                      </div>
                    ))}
                  {!enrichRunning && (enrichProposals?.length ?? 0) > 0 && (
                    <div class="stage-enrich__actions">
                      <button
                        class="stage-worldlore-form__save"
                        disabled={enrichApplying || Object.values(enrichVerdicts).filter(Boolean).length === 0}
                        onClick={() => void enrichApply()}
                      >
                        {enrichApplying ? t('Applying…') : tn(Object.values(enrichVerdicts).filter(Boolean).length, 'Apply %d accepted', 'Apply %d accepted')}
                      </button>
                      <button class="stage-worldlore-form__cancel" disabled={Object.keys(enrichVerdicts).length === 0} onClick={enrichRevise}>
                        {t('Revise with feedback')}
                      </button>
                    </div>
                  )}
                </div>
              )}
              {Object.entries(info?.cast ?? {}).filter(([, ref]) => ref !== info?.card).length > 0 && (
                <label class="stage-coordination">
                  <span class="stage-coordination__label">{t('Who replies')}</span>
                  <select
                    class="stage-coordination__select"
                    value={info?.coordination ?? ''}
                    onChange={(e) => void setCoordination((e.target as HTMLSelectElement).value)}
                  >
                    <option value="">{t('Auto — the meta-narrator picks')}</option>
                    <option value="off">{t('Only the main character')}</option>
                    {Object.entries(info?.cast ?? {})
                      .filter(([, ref]) => ref !== info?.card)
                      .map(([name]) => (
                        <option key={name} value={`focus:${name}`}>
                          {t('Always %s', name)}
                        </option>
                      ))}
                  </select>
                  <span class="stage-hint">{t('Auto lets the story decide who answers each message — the main character, someone from the roster, or a narrator beat. Applies on your next message.')}</span>
                </label>
              )}
            </>
          )}
        </section>

        <section class="stage-drawer__section">
          <h4>{t('World lore')}</h4>
          <p class="stage-hint">{t('Shared facts about this world — everyone on stage sees them. A keyed entry injects when a keyword appears in recent messages; an always-on entry injects every turn. Edits apply on the next message.')}</p>
          {/* The scene-state pin (SD4) is EDITED above the composer, not in this
              list — but it must still be ACCOUNTED FOR here, because this is
              where someone comes looking for "what is in this world's context".
              Offering to pin one when there is none and then going silent once
              there is meant the tab denied knowing about the card at exactly the
              moment it existed — and the doctor's accept happens behind the
              drawer's own backdrop, so that silence was the only thing the
              author saw. Read-only on purpose: one editor for one card. */}
          {sceneStateOf(info?.world_lore) ? (
            <div class="stage-worldlore__pinned">
              <div class="stage-worldlore__head">
                <span class="stage-worldlore__name">📌 {t('Scene state')}</span>
                <span class="stage-lore__keys">
                  <span class="stage-lore__const">{t('always on')}</span>
                  {scenePinDrift(info?.scene_pin_stale) > 0 && (
                    <span
                      class="stage-lore__badge stage-lore__badge--dropped"
                      title={t('This card outranks the scene when they disagree, and nothing updates it on its own — check it against what has happened since, then doctor this session to refresh it.')}
                    >
                      {tn(scenePinDrift(info?.scene_pin_stale), 'pinned %d turn ago', 'pinned %d turns ago')}
                    </span>
                  )}
                </span>
              </div>
              <p class="stage-lore__content">{sceneStateOf(info?.world_lore)?.content}</p>
              <p class="stage-hint">{t('Pinned above the composer — open the card there to edit or unpin it.')}</p>
            </div>
          ) : (
            <button
              type="button"
              class="stage-worldlore__pinbtn"
              title={t('Pin a scene-state card — the in-fiction clock, location, and ledger, always in context, shown above the composer')}
              onClick={() => {
                setLoreEditing('')
                setLoreName(SCENE_STATE_NAME)
                setLoreAlways(true)
                setLoreKeys('')
                setLoreAudience([])
              }}
            >
              {t('📌 Pin a scene-state card')}
            </button>
          )}
          {worldLoreListed.length === 0 && <p class="stage-empty">{t('No World lore yet.')}</p>}
          <ul class="stage-worldlore">
            {worldLoreListed.map((e) => (
              <li key={e.name} class="stage-worldlore__entry">
                <div class="stage-worldlore__head">
                  <span class="stage-worldlore__name">{e.name}</span>
                  <span class="stage-lore__keys">
                    {e.constant ? (
                      <span class="stage-lore__const">{t('always on')}</span>
                    ) : (
                      (e.keys ?? []).map((k) => (
                        <span key={k} class="stage-lore__key">
                          {k}
                        </span>
                      ))
                    )}
                    {(e.audience ?? []).length > 0 && (
                      <span
                        class="stage-worldlore__audience"
                        title={t(
                          'Only these characters know this: %s',
                          (e.audience ?? []).map((n) => (e.learned?.[n] ? t('%s (learned mid-scene)', n) : n)).join(', '),
                        )}
                      >
                        🔒 {(e.audience ?? []).join(', ')}
                      </span>
                    )}
                    {e.model && (
                      <span class="stage-worldlore__model" title={t('Noted by the model during play (world_note)')}>
                        📝
                      </span>
                    )}
                  </span>
                  <button class="stage-worldlore__act" title={t('Edit %s', e.name)} onClick={() => worldLoreEdit(e)}>
                    ✎
                  </button>
                  <button class="stage-worldlore__act" title={t('Delete %s', e.name)} onClick={() => void worldLoreDelete(e.name)}>
                    ✕
                  </button>
                </div>
                <p class="stage-lore__content">{e.content}</p>
              </li>
            ))}
          </ul>
          <form
            class="stage-worldlore-form"
            onSubmit={(e) => {
              e.preventDefault()
              worldLoreSave()
            }}
          >
            <input
              class="stage-worldlore-form__name"
              placeholder={t('Name (e.g. The Accord)')}
              value={loreName}
              onInput={(e) => setLoreName((e.target as HTMLInputElement).value)}
            />
            <label class="stage-worldlore-form__always">
              <input type="checkbox" checked={loreAlways} onChange={(e) => setLoreAlways((e.target as HTMLInputElement).checked)} />
              {t('always on')}
            </label>
            {!loreAlways && (
              <input
                class="stage-worldlore-form__keys"
                placeholder={t('Trigger keywords, comma-separated (e.g. accord, council)')}
                value={loreKeys}
                onInput={(e) => setLoreKeys((e.target as HTMLInputElement).value)}
              />
            )}
            <textarea
              class="stage-worldlore-form__content"
              rows={3}
              placeholder={t('What everyone on stage should know when this fires.')}
              value={loreContent}
              onInput={(e) => setLoreContent((e.target as HTMLTextAreaElement).value)}
            />
            {audienceChoices.length > 0 && (
              <div class="stage-worldlore-form__audience">
                <span class="stage-worldlore-form__audience-label">{loreAudience.length === 0 ? t('Who knows this — everyone') : t('Who knows this')}</span>
                <div class="stage-worldlore-form__audience-chips">
                  {audienceChoices.map((name) => (
                    <button
                      key={name}
                      type="button"
                      class={`stage-worldlore-form__chip ${loreAudience.includes(name) ? 'stage-worldlore-form__chip--on' : ''}`}
                      title={loreAudience.includes(name) ? t('%s knows this', name) : t('Limit this to %s', name)}
                      onClick={() => toggleAudience(name)}
                    >
                      {loreAudience.includes(name) ? '🔒 ' : ''}
                      {name}
                    </button>
                  ))}
                </div>
                <span class="stage-hint">{t('No selection = everyone on stage. Select characters to make this a secret only they know — the others never see it.')}</span>
              </div>
            )}
            <div class="stage-worldlore-form__actions">
              <button type="submit" class="stage-worldlore-form__save" disabled={!loreName.trim() || !loreContent.trim() || (!loreAlways && parsedLoreKeys.length === 0)}>
                {loreEditing ? t('Save entry') : t('Add entry')}
              </button>
              {loreEditing && (
                <button type="button" class="stage-worldlore-form__cancel" onClick={resetLoreForm}>
                  {t('Cancel')}
                </button>
              )}
            </div>
          </form>
        </section>
        <SessionDoctor
          client={client}
          sessionId={sessionId}
          defaultProvider={info?.provider}
          defaultModel={info?.model}
          onSceneBreak={props.onNextScene ? (title) => { onClose(); props.onNextScene?.(title) } : undefined}
        />

        {props.onNextScene && (
          <section class="stage-drawer__section">
            <h4>{t('Next scene')}</h4>
            <p class="stage-hint">
              {t('End this scene at a chapter boundary and open the next one in the same world. The cast, the lore, and the scene-state card carry over; a recap and a cold open are drafted for you to edit. This chat stays exactly as it is.')}
            </p>
            <button
              class="stage-nextscene__open"
              onClick={() => {
                onClose()
                props.onNextScene?.('')
              }}
            >
              {t('🎬 Start the next scene…')}
            </button>
          </section>
        )}
        </>
        )}

        {/* Session tab: this chat's title/mode/model, plus the play cast. */}
        {tab === 'session' && (
        <>
        <section class="stage-drawer__section">
          <h4>{t('Session')}</h4>
          <dl class="stage-detail">
            <dt>{t('Title')}</dt>
            <dd class="stage-detail__title">
              <span>{info?.title || '—'}</span>
              <button class="stage-title-btn" title={t('Rename this chat')} onClick={renameSession}>✎</button>
              <button class="stage-title-btn" title={t('Regenerate the title from the conversation')} onClick={regenerateTitle}>✨</button>
            </dd>
            <dt>{t('Mode')}</dt>
            {/* i18n-exempt — experience is the wire mode token ('chat'/'play'), shown verbatim */}
            <dd>{info?.experience || 'chat'}</dd>
          </dl>
          <ModelPick client={client} sessionId={sessionId} currentProvider={info?.provider} currentModel={info?.model} />
          {/* The scene as something you can read elsewhere. Rendered by the
              daemon, not here: the client cannot tell a card greeting from a
              reply (the wire drops that source), and attribution already has
              three implementations without adding a fourth. */}
          <button class="stage-export" disabled={exporting} onClick={exportStory}>
            {exporting ? t('Exporting…') : t('⬇ Export as story (.md)')}
          </button>
        </section>

        {/* Diagnostics: the id and transcript path, so a dogfooding session can
            be found on disk or named in a bug report without a round trip
            through the panel to look them up. Both ride SessionInfo already. */}
        <section class="stage-drawer__section">
          <h4>{t('Diagnostics')}</h4>
          <dl class="stage-detail">
            <dt>{t('Session')}</dt>
            <dd class="stage-copyrow">
              <code>{sessionId}</code>
              <button
                class="stage-title-btn"
                title={t('Copy the session id')}
                aria-label={t('Copy the session id')}
                onClick={() => copyRow('id', sessionId)}
              >
                {copied === 'id' ? '✓' : '⧉'}
              </button>
            </dd>
            {info?.path && (
              <>
                <dt>{t('Path')}</dt>
                <dd class="stage-copyrow">
                  <code title={info.path}>{info.path}</code>
                  <button
                    class="stage-title-btn"
                    title={t('Copy the transcript path')}
                    aria-label={t('Copy the transcript path')}
                    onClick={() => copyRow('path', info.path!)}
                  >
                    {copied === 'path' ? '✓' : '⧉'}
                  </button>
                </dd>
              </>
            )}
          </dl>
          {/* Context lives in the panel, deliberately: Stage stays a reading
              surface, and the panel already renders the whole breakdown. */}
          <a class="stage-nav-link stage-inspect" href={panelHref(sessionId, 'context')}>
            {t('🔍 Inspect context in the panel')}
          </a>
        </section>

        {info?.experience === 'play' && (
          <section class="stage-drawer__section">
            <h4>{t('Cast')}</h4>
            <p class="stage-hint">{t('Who the director can bring on stage during the scene. Each is a real agent with its own memory. Changing the cast rebuilds the prompt — the next reply starts uncached.')}</p>
            <ul class="stage-cast">
              {Object.entries(info.cast ?? {}).map(([name, ref]) => (
                <li key={name} class="stage-cast__member">
                  <span class="stage-cast__name">{name}</span>
                  <span class="stage-cast__ref">{ref}</span>
                  {info.cast_models?.[name]?.model && (
                    <span
                      class="stage-cast__model"
                      title={
                        info?.model
                          ? t('Pinned model — this actor runs on %s instead of the session model (%s)', info.cast_models[name].model, info.model)
                          : t('Pinned model — this actor runs on %s instead of the session model', info.cast_models[name].model)
                      }
                    >
                      ⚙ {info.cast_models[name].model}
                    </span>
                  )}
                  <button class="stage-cast__remove" title={t('Remove %s', name)} onClick={() => void castRemove(name)}>
                    ✕
                  </button>
                </li>
              ))}
            </ul>
            <form
              class="stage-cast-add"
              onSubmit={(e) => {
                e.preventDefault()
                castAdd()
              }}
            >
              <input
                class="stage-cast-add__name"
                placeholder={t('Name')}
                value={castName}
                onInput={(e) => setCastName((e.target as HTMLInputElement).value)}
              />
              <input
                class="stage-cast-add__ref"
                placeholder={t('Persona or card')}
                value={castRef}
                onInput={(e) => setCastRef((e.target as HTMLInputElement).value)}
              />
              <div class="stage-cast-add__model">
                <ModelPick
                  client={client}
                  sessionId={sessionId}
                  currentProvider={castProvider}
                  currentModel={castModel}
                  onSelect={(p, m) => {
                    setCastProvider(p)
                    setCastModel(m)
                  }}
                  defaultLabel={t('Session model')}
                  defaultProvider={info?.provider}
                  defaultModel={info?.model}
                />
              </div>
              <button type="submit" class="stage-cast-add__go" disabled={!castName.trim() || !castRef.trim()}>
                {t('Add')}
              </button>
            </form>
          </section>
        )}
        </>
        )}

        {/* You tab: the persona you play in this story. */}
        {tab === 'you' && (
        <section class="stage-drawer__section">
          <h4>{t('You (in this story)')}</h4>
          <p class="stage-hint">{t('Who you are to the character. Changing your name rebuilds the prompt — the next reply starts uncached; the description applies on the next message. Save a persona to reuse it, or set a default that pre-fills every new chat.')}</p>
          {savedPersonas.length > 0 && (
            <div class="stage-userpersonas">
              {savedPersonas.map((p) => (
                <span key={p.ref} class={`stage-userpersona ${p.default ? 'stage-userpersona--default' : ''}`}>
                  <button class="stage-userpersona__pick" title={p.description || p.name} onClick={() => p.ref && void bindSaved(p.ref)}>
                    {p.default ? '★ ' : ''}
                    {p.name}
                  </button>
                  <button class="stage-userpersona__del" title={t('Delete %s', p.name)} onClick={() => p.ref && void deletePersona(p.ref)}>
                    ✕
                  </button>
                </span>
              ))}
            </div>
          )}
          <input
            class="stage-user-name"
            type="text"
            placeholder={t('Your name (e.g. Kira)')}
            value={userName}
            onInput={(e) => setUserName((e.target as HTMLInputElement).value)}
            onBlur={() => commitUser()}
          />
          <textarea
            class="stage-user-desc"
            rows={2}
            placeholder={t('e.g. A wary courier who trusts no one.')}
            value={userDesc}
            onInput={(e) => setUserDesc((e.target as HTMLTextAreaElement).value)}
            onBlur={() => commitUser()}
          />
          <div class="stage-identity-row">
            <IdentityField
              key={`pronouns-${sessionId}`}
              label={t('Pronouns')}
              placeholder={t('e.g. xe/xem')}
              options={PRONOUN_OPTIONS}
              value={userPronouns}
              onChange={setUserPronouns}
              onCommit={(v) => commitUser({ pronouns: v })}
            />
            <IdentityField
              key={`gender-${sessionId}`}
              label={t('Gender')}
              placeholder={t('Describe it your way')}
              options={GENDER_OPTIONS}
              value={userGender}
              onChange={setUserGender}
              onCommit={(v) => commitUser({ gender: v })}
            />
          </div>
          <div class="stage-userpersona-actions">
            <button class="stage-userpersona-save" disabled={!userName.trim()} onClick={() => savePersona(false)}>
              {t('Save persona')}
            </button>
            <button
              class="stage-userpersona-save"
              disabled={!userName.trim()}
              title={t('Save this persona and pre-fill it into every new chat')}
              onClick={() => savePersona(true)}
            >
              {t('Save as default')}
            </button>
          </div>
        </section>
        )}

        {/* Scene tab: the setting — author's note, backgrounds, lorebook. */}
        {tab === 'scene' && (
        <>
        <section class="stage-drawer__section">
          <h4>{t("Author's note")}</h4>
          <p class="stage-hint">{t('A steering instruction added to every turn — tone, pacing, what happens next. Applies on the next message.')}</p>
          <textarea
            class="stage-note"
            rows={3}
            placeholder={t('e.g. Keep replies short and tense. It is raining.')}
            value={noteDraft}
            onInput={(e) => setNoteDraft((e.target as HTMLTextAreaElement).value)}
            onBlur={commitNote}
          />
        </section>

        <section class="stage-drawer__section">
          <div class="stage-drawer__head2">
            <h4>{t('Scene')}</h4>
            <label class="stage-import">
              {t('+ Import')}
              <input
                type="file"
                accept="image/*"
                hidden
                onChange={(e) => {
                  const f = (e.target as HTMLInputElement).files
                  if (f) void importBg(f)
                }}
              />
            </label>
          </div>
          <div class="stage-bg-grid">
            <button class={`stage-bg-tile stage-bg-tile--none ${!info?.background ? 'stage-bg-tile--on' : ''}`} onClick={() => void bind('')}>
              {t('None')}
            </button>
            {/* Each tile is wrapped so the remove control has somewhere to sit:
                the tile itself is a bare button whose image is a CSS background,
                with no child slot. The ✕ is a sibling, not a nested button — a
                button inside a button is invalid and the inner click would not
                reliably beat the bind. */}
            {backgrounds.map((b) => (
              <div key={b.id} class="stage-bg-cell">
                <button
                  class={`stage-bg-tile ${info?.background === b.id ? 'stage-bg-tile--on' : ''}`}
                  style={{ backgroundImage: `url("${b.url}")` }}
                  title={b.id}
                  onClick={() => void bind(b.id)}
                />
                <button
                  class="stage-bg-cell__del"
                  title={t('Delete this backdrop')}
                  aria-label={t('Delete this backdrop')}
                  onClick={() => void deleteBg(b.id)}
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
          <form
            class="stage-bg-gen"
            onSubmit={(e) => {
              e.preventDefault()
              generateScene()
            }}
          >
            <input
              class="stage-bg-gen__prompt"
              placeholder={t('Describe a scene to generate…')}
              value={scenePrompt}
              disabled={generating}
              onInput={(e) => setScenePrompt((e.target as HTMLInputElement).value)}
            />
            <button type="submit" class="stage-bg-gen__go" disabled={generating || !scenePrompt.trim()}>
              {generating ? t('Painting…') : t('Generate')}
            </button>
          </form>
        </section>

        <section class="stage-drawer__section">
          <h4>{t('Lorebook')}</h4>
          {(!lore || lore.entries.length === 0) && <p class="stage-empty">{t('No lorebook for this character.')}</p>}
          <ul class="stage-lore">
            {lore?.entries.map((e, i) => (
              <li key={i} class={`stage-lore__entry ${e.fired ? 'stage-lore__entry--fired' : ''}`}>
                <div class="stage-lore__keys">
                  {e.constant ? (
                    <span class="stage-lore__const">{t('always on')}</span>
                  ) : (
                    (e.keys ?? []).map((k) => (
                      <span key={k} class={`stage-lore__key ${(e.matched_keys ?? []).includes(k) ? 'stage-lore__key--hit' : ''}`}>
                        {k}
                      </span>
                    ))
                  )}
                  {e.fired && !e.dropped_for_budget && <span class="stage-lore__badge stage-lore__badge--fired">{t('fired last turn')}</span>}
                  {e.dropped_for_budget && <span class="stage-lore__badge stage-lore__badge--dropped">{t('budget dropped')}</span>}
                </div>
                {e.content && <p class="stage-lore__content">{e.content}</p>}
              </li>
            ))}
          </ul>
        </section>
        </>
        )}
      </aside>
    </div>
  )
}
