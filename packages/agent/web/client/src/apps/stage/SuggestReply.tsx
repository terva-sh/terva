import { useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { SuggestTurn, SuggestResult } from '../../platform/ctrlproto/types'
import { useAutoGrow } from './autogrow'

// SuggestReply is Stage's agentic "suggest a reply" modal — the composer aid the
// user asked for (S9): NOT SillyTavern's one-shot text-box fill, but a dedicated
// back-and-forth. You optionally sketch what you want to say first, the model
// drafts it in YOUR voice (the player's), and you refine it in a conversation
// ("shorter", "mention the door") until it's right — then copy it into the
// composer and send it yourself. The transcript is never touched.
//
// Statelessly driven: the daemon holds no per-request state, so we carry the
// whole (note → draft) history on every suggest.reply and re-send it. `rounds`
// is that history; the last round's draft is the current, editable draft.
export function SuggestReply(props: {
  client: Client
  sessionId: string
  initialNote?: string
  onClose: () => void
  onUse: (text: string) => void
}) {
  const { client, sessionId, onClose, onUse } = props
  const [rounds, setRounds] = useState<SuggestTurn[]>([])
  // Seed the sketch with whatever the user had already typed in the composer, so
  // opening ✨ mid-thought carries it in rather than throwing it away.
  const [note, setNote] = useState(props.initialNote?.trim() ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const last = rounds.length - 1
  const draft = last >= 0 ? rounds[last].draft : ''
  // Both inputs grow with their content (up to their CSS caps). One note box is
  // mounted at a time (sketch or refine), so a single ref serves both.
  const noteRef = useAutoGrow(note)
  const draftRef = useAutoGrow(draft)

  // Run one drafting round: send the history so far plus this guidance, append
  // the returned draft as the new latest round. `history` lets a revert/redo
  // re-run from an earlier point (truncated rounds) rather than the current tail.
  const run = async (guidance: string, history: SuggestTurn[]) => {
    setBusy(true)
    setError('')
    try {
      const res = await client.send<SuggestResult>('suggest.reply', { history, note: guidance }, sessionId)
      setRounds([...history, { note: guidance, draft: res.draft ?? '' }])
      setNote('')
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  // Hand-edits to the current draft feed back into the refinement: the next
  // "refine" call threads the edited text, not the model's original.
  const editDraft = (text: string) => {
    if (last < 0) return
    setRounds((rs) => rs.map((r, i) => (i === last ? { ...r, draft: text } : r)))
  }

  // Revert to an earlier draft: drop everything after round i and make it current
  // again, so a refinement that went the wrong way can be backed out.
  const revertTo = (i: number) => setRounds((rs) => rs.slice(0, i + 1))

  // Redo the latest round with the same guidance (a fresh sample), keeping the
  // history before it intact.
  const regenerate = () => {
    if (last < 0) return
    void run(rounds[last].note ?? '', rounds.slice(0, last))
  }

  const composerHint = (n: string) => n.trim() || 'cold suggestion'

  return (
    <div class="stage-sheet-backdrop" onClick={onClose}>
      <div class="stage-suggest" onClick={(e) => e.stopPropagation()}>
        <header class="stage-suggest__head">
          <h3>✨ Suggest a reply</h3>
          <button class="stage-drawer__close" onClick={onClose}>
            ✕
          </button>
        </header>

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {rounds.length === 0 ? (
          <div class="stage-suggest__sketch">
            <p class="stage-suggest__hint">
              Sketch what you want to say — a few words, the gist, or leave it blank and I'll suggest something in your voice.
            </p>
            <textarea
              ref={noteRef}
              class="stage-suggest__note"
              value={note}
              placeholder="e.g. push back, but stay flirty…"
              rows={3}
              autofocus
              disabled={busy}
              onInput={(e) => setNote((e.target as HTMLTextAreaElement).value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  if (!busy) void run(note, [])
                }
              }}
            />
            <div class="stage-suggest__sketch-actions">
              <button class="stage-suggest__go" disabled={busy} onClick={() => void run(note, [])}>
                {busy ? 'Drafting…' : 'Draft it ✨'}
              </button>
            </div>
          </div>
        ) : (
          <>
            <div class="stage-suggest__thread">
              {rounds.map((r, i) => (
                <div key={i} class="stage-suggest__round">
                  <div class={`stage-suggest__guide ${r.note?.trim() ? '' : 'stage-suggest__guide--cold'}`}>
                    {r.note?.trim() ? `“${r.note.trim()}”` : 'cold suggestion'}
                  </div>
                  {i === last ? (
                    <textarea
                      ref={draftRef}
                      class="stage-suggest__draft"
                      value={draft}
                      rows={4}
                      disabled={busy}
                      onInput={(e) => editDraft((e.target as HTMLTextAreaElement).value)}
                    />
                  ) : (
                    <div class="stage-suggest__draft-past">
                      <div class="stage-suggest__draft-text">{r.draft}</div>
                      <button class="stage-suggest__revert" title="Go back to this draft" onClick={() => revertTo(i)}>
                        ↩ revert
                      </button>
                    </div>
                  )}
                </div>
              ))}
            </div>

            <div class="stage-suggest__refine">
              <textarea
                ref={noteRef}
                class="stage-suggest__note"
                value={note}
                placeholder="Refine it — “shorter”, “angrier”, “mention the locked door”…"
                rows={2}
                disabled={busy}
                onInput={(e) => setNote((e.target as HTMLTextAreaElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault()
                    if (!busy && note.trim()) void run(note, rounds)
                  }
                }}
              />
              <div class="stage-suggest__refine-actions">
                <button
                  class="stage-suggest__refine-go"
                  disabled={busy || !note.trim()}
                  title="Refine the draft with this note"
                  onClick={() => void run(note, rounds)}
                >
                  {busy ? '…' : 'Refine'}
                </button>
                <button class="stage-suggest__regen" disabled={busy} title="Try the last note again" onClick={regenerate}>
                  ↻
                </button>
              </div>
            </div>

            <footer class="stage-suggest__foot">
              <button class="stage-suggest__cancel" onClick={onClose}>
                Cancel
              </button>
              <button
                class="stage-suggest__use"
                disabled={busy || !draft.trim()}
                title={composerHint(note)}
                onClick={() => {
                  onUse(draft)
                  onClose()
                }}
              >
                Use this →
              </button>
            </footer>
          </>
        )}
      </div>
    </div>
  )
}
