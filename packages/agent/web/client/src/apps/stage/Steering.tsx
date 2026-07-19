import { useEffect, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { SessionInfo, BackgroundView, BackgroundsListResult, LoreView, Surface, UserPersonaView, UserPersonasListResult } from '../../platform/ctrlproto/types'
import { ModelPick } from './ModelPick'

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
export function Steering(props: { client: Client; sessionId: string; info: SessionInfo | null; onClose: () => void }) {
  const { client, sessionId, info, onClose } = props
  const [backgrounds, setBackgrounds] = useState<BackgroundView[]>([])
  const [lore, setLore] = useState<LoreView | null>(null)
  const [error, setError] = useState('')
  // The author's note is edited locally and committed on blur; the snapshot fold
  // re-seeds it (from info.note) on a session switch or an external change.
  const [noteDraft, setNoteDraft] = useState(info?.note ?? '')
  useEffect(() => {
    setNoteDraft(info?.note ?? '')
  }, [sessionId, info?.note])
  // The user persona — name + description — edited locally, committed together on
  // blur, re-seeded from the snapshot the same way as the note.
  const [userName, setUserName] = useState(info?.user_name ?? '')
  const [userDesc, setUserDesc] = useState(info?.user_description ?? '')
  useEffect(() => {
    setUserName(info?.user_name ?? '')
    setUserDesc(info?.user_description ?? '')
  }, [sessionId, info?.user_name, info?.user_description])
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
  // Generate-a-scene: a prompt + a pending flag (generation is a slow backend
  // round-trip). The result auto-binds, so the scene updates via the snapshot.
  const [scenePrompt, setScenePrompt] = useState('')
  const [generating, setGenerating] = useState(false)

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
      setError('Give your persona a name to save it.')
      return
    }
    setError('')
    client
      .send<UserPersonaView>('userpersonas.save', { name, description: userDesc.trim() })
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
  // Both halves ride one verb; the daemon rebuilds the prefix only when the name
  // actually changed, so a description-only edit stays a free tail update.
  const commitUser = () => {
    if (userName === (info?.user_name ?? '') && userDesc === (info?.user_description ?? '')) return
    client.send('user.bind', { name: userName, description: userDesc }, sessionId).catch((e: unknown) => setError(String(e)))
  }
  const castAdd = () => {
    const name = castName.trim()
    const ref = castRef.trim()
    if (!name || !ref) return
    client
      .send('cast.add', { name, ref }, sessionId)
      .then(() => {
        setCastName('')
        setCastRef('')
      })
      .catch((e: unknown) => setError(String(e)))
  }
  const castRemove = (name: string) => client.send('cast.remove', { name }, sessionId).catch((e: unknown) => setError(String(e)))
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
          <h3>Steering</h3>
          <button class="stage-drawer__close" onClick={onClose}>✕</button>
        </header>
        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        <section class="stage-drawer__section">
          <h4>Session</h4>
          <dl class="stage-detail">
            <dt>Character</dt>
            <dd>{info?.title || '—'}</dd>
            <dt>Mode</dt>
            <dd>{info?.experience || 'chat'}</dd>
          </dl>
          <ModelPick client={client} sessionId={sessionId} currentProvider={info?.provider} currentModel={info?.model} />
        </section>

        {info?.experience === 'play' && (
          <section class="stage-drawer__section">
            <h4>Cast</h4>
            <p class="stage-hint">Who the director can bring on stage during the scene. Each is a real agent with its own memory. Changing the cast rebuilds the prompt — the next reply starts uncached.</p>
            <ul class="stage-cast">
              {Object.entries(info.cast ?? {}).map(([name, ref]) => (
                <li key={name} class="stage-cast__member">
                  <span class="stage-cast__name">{name}</span>
                  <span class="stage-cast__ref">{ref}</span>
                  <button class="stage-cast__remove" title={`Remove ${name}`} onClick={() => void castRemove(name)}>
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
                placeholder="Name"
                value={castName}
                onInput={(e) => setCastName((e.target as HTMLInputElement).value)}
              />
              <input
                class="stage-cast-add__ref"
                placeholder="Persona or card"
                value={castRef}
                onInput={(e) => setCastRef((e.target as HTMLInputElement).value)}
              />
              <button type="submit" class="stage-cast-add__go" disabled={!castName.trim() || !castRef.trim()}>
                Add
              </button>
            </form>
          </section>
        )}

        <section class="stage-drawer__section">
          <h4>You (in this story)</h4>
          <p class="stage-hint">Who you are to the character. Changing your name rebuilds the prompt — the next reply starts uncached; the description applies on the next message. Save a persona to reuse it, or set a default that pre-fills every new chat.</p>
          {savedPersonas.length > 0 && (
            <div class="stage-userpersonas">
              {savedPersonas.map((p) => (
                <span key={p.ref} class={`stage-userpersona ${p.default ? 'stage-userpersona--default' : ''}`}>
                  <button class="stage-userpersona__pick" title={p.description || p.name} onClick={() => p.ref && void bindSaved(p.ref)}>
                    {p.default ? '★ ' : ''}
                    {p.name}
                  </button>
                  <button class="stage-userpersona__del" title={`Delete ${p.name}`} onClick={() => p.ref && void deletePersona(p.ref)}>
                    ✕
                  </button>
                </span>
              ))}
            </div>
          )}
          <input
            class="stage-user-name"
            type="text"
            placeholder="Your name (e.g. Kira)"
            value={userName}
            onInput={(e) => setUserName((e.target as HTMLInputElement).value)}
            onBlur={commitUser}
          />
          <textarea
            class="stage-user-desc"
            rows={2}
            placeholder="e.g. A wary courier who trusts no one."
            value={userDesc}
            onInput={(e) => setUserDesc((e.target as HTMLTextAreaElement).value)}
            onBlur={commitUser}
          />
          <div class="stage-userpersona-actions">
            <button class="stage-userpersona-save" disabled={!userName.trim()} onClick={() => savePersona(false)}>
              Save persona
            </button>
            <button
              class="stage-userpersona-save"
              disabled={!userName.trim()}
              title="Save this persona and pre-fill it into every new chat"
              onClick={() => savePersona(true)}
            >
              Save as default
            </button>
          </div>
        </section>

        <section class="stage-drawer__section">
          <h4>Author's note</h4>
          <p class="stage-hint">A steering instruction added to every turn — tone, pacing, what happens next. Applies on the next message.</p>
          <textarea
            class="stage-note"
            rows={3}
            placeholder="e.g. Keep replies short and tense. It is raining."
            value={noteDraft}
            onInput={(e) => setNoteDraft((e.target as HTMLTextAreaElement).value)}
            onBlur={commitNote}
          />
        </section>

        <section class="stage-drawer__section">
          <div class="stage-drawer__head2">
            <h4>Scene</h4>
            <label class="stage-import">
              + Import
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
              None
            </button>
            {backgrounds.map((b) => (
              <button
                key={b.id}
                class={`stage-bg-tile ${info?.background === b.id ? 'stage-bg-tile--on' : ''}`}
                style={{ backgroundImage: `url("${b.url}")` }}
                title={b.id}
                onClick={() => void bind(b.id)}
              />
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
              placeholder="Describe a scene to generate…"
              value={scenePrompt}
              disabled={generating}
              onInput={(e) => setScenePrompt((e.target as HTMLInputElement).value)}
            />
            <button type="submit" class="stage-bg-gen__go" disabled={generating || !scenePrompt.trim()}>
              {generating ? 'Painting…' : 'Generate'}
            </button>
          </form>
        </section>

        <section class="stage-drawer__section">
          <h4>Lorebook</h4>
          {(!lore || lore.entries.length === 0) && <p class="stage-empty">No lorebook for this character.</p>}
          <ul class="stage-lore">
            {lore?.entries.map((e, i) => (
              <li key={i} class={`stage-lore__entry ${e.fired ? 'stage-lore__entry--fired' : ''}`}>
                <div class="stage-lore__keys">
                  {e.constant ? (
                    <span class="stage-lore__const">always on</span>
                  ) : (
                    (e.keys ?? []).map((k) => (
                      <span key={k} class={`stage-lore__key ${(e.matched_keys ?? []).includes(k) ? 'stage-lore__key--hit' : ''}`}>
                        {k}
                      </span>
                    ))
                  )}
                  {e.fired && !e.dropped_for_budget && <span class="stage-lore__badge stage-lore__badge--fired">fired last turn</span>}
                  {e.dropped_for_budget && <span class="stage-lore__badge stage-lore__badge--dropped">budget dropped</span>}
                </div>
                {e.content && <p class="stage-lore__content">{e.content}</p>}
              </li>
            ))}
          </ul>
        </section>
      </aside>
    </div>
  )
}
