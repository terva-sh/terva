import { useState } from 'preact/hooks'
import { t } from '../../i18n'

// mentions returns the member ids a post names with @id, in order and once
// each. A name that is not a member is plain text.
export function mentions(body: string, members: string[]): string[] {
  const out: string[] = []
  for (const m of body.matchAll(/(^|\s)@([a-z][a-z0-9-]*)/g)) {
    const id = m[2]
    if (members.includes(id) && !out.includes(id)) out.push(id)
  }
  return out
}

// recipients decides who a post reaches. An @member in the text wins. In a
// member's own view the post goes to that member. Otherwise it goes to no one
// by name, and the router hands it to the coordinator.
export function recipients(body: string, members: string[], view: string): string[] {
  const named = mentions(body, members)
  if (named.length > 0) return named
  return view ? [view] : []
}

export function TalkootComposer({
  members,
  to,
  onPost,
}: {
  members: string[]
  // The member whose view is open, or empty in the room.
  to: string
  onPost: (body: string, to: string[]) => Promise<unknown>
}) {
  const [body, setBody] = useState('')
  const [sending, setSending] = useState(false)
  const send = () => {
    const text = body.trim()
    if (!text || sending) return
    setSending(true)
    onPost(text, recipients(text, members, to)).then(
      () => {
        // Text typed while the post was in flight is a new draft. Keep it.
        setBody((b) => (b.trim() === text ? '' : b))
        setSending(false)
      },
      () => setSending(false),
    )
  }
  const target = recipients(body, members, to)
  return (
    <form
      class="talkoot-composer"
      onSubmit={(e) => {
        e.preventDefault()
        send()
      }}
    >
      <textarea
        aria-label={t('Message to the team')}
        placeholder={to ? t('Message %s, or name a member with @', to) : t('Message the coordinator, or name a member with @')}
        value={body}
        rows={2}
        onInput={(e) => setBody((e.target as HTMLTextAreaElement).value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            send()
          }
        }}
      />
      <div class="talkoot-composer-foot">
        <span class="talkoot-note">{target.length ? t('To: %s', target.join(', ')) : t('To: the coordinator')}</span>
        <button class="btn primary" type="submit" disabled={!body.trim() || sending}>
          {t('Send')}
        </button>
      </div>
    </form>
  )
}
