import { useLayoutEffect, useReducer } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import { dispatchError } from '../../platform/ctrlproto/errors'

type Draft = { text: string; sending: boolean; error: string; listeners: Set<() => void> }
// Stage remounts Chat when the user changes scenes. Keep each draft with the
// client, so navigation during an acknowledgment cannot lose or resend it.
// This memory ends when the tab closes; it contains no durable session state.
const drafts = new WeakMap<ClientLike, Map<string, Draft>>()

export function useComposerDraft(client: ClientLike, session: string) {
  let sessions = drafts.get(client)
  if (!sessions) drafts.set(client, sessions = new Map())
  let held = sessions.get(session)
  if (!held) sessions.set(session, held = { text: '', sending: false, error: '', listeners: new Set() })
  const draft = held
  const [, render] = useReducer((v: number) => v + 1, 0)
  useLayoutEffect(() => {
    const refresh = () => render(null)
    draft.listeners.add(refresh)
    return () => { draft.listeners.delete(refresh) }
  }, [draft])
  const notify = () => { for (const listener of draft.listeners) listener() }
  const setText = (text: string) => { draft.text = text; notify() }
  const clearError = () => { draft.error = ''; notify() }
  const submit = async (send: (text: string) => Promise<unknown>) => {
    if (!draft.text.trim() || draft.sending) return
    const submitted = draft.text
    draft.sending = true
    draft.error = ''
    notify()
    try {
      await send(submitted.trim())
      if (draft.text === submitted) draft.text = ''
    } catch (err) {
      draft.error = dispatchError(err)
    } finally {
      draft.sending = false
      notify()
    }
  }
  return { text: draft.text, sending: draft.sending, error: draft.error, setText, clearError, submit }
}
