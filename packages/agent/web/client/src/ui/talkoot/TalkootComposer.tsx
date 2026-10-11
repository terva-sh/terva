import { useRef, useState } from 'preact/hooks'
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

// mentionAt finds the @name the caret is in: the text from an @ at the start
// of the draft or after a space, up to the caret. It returns where the @
// starts and the part of the name typed so far, or null when the caret is
// not in one.
export function mentionAt(body: string, caret: number): { start: number; prefix: string } | null {
  const m = /(^|\s)@([a-z0-9-]*)$/.exec(body.slice(0, caret))
  if (!m) return null
  return { start: caret - m[2].length - 1, prefix: m[2] }
}

export function TalkootComposer({
  members,
  to,
  onPost,
  complete = false,
}: {
  members: string[]
  // The member whose view is open, or empty in the room.
  to: string
  onPost: (body: string, to: string[]) => Promise<unknown>
  // complete offers member ids when the person types @. The Talkoot app turns
  // it on. The panel's frozen Talkoot view leaves it off.
  complete?: boolean
}) {
  const [body, setBody] = useState('')
  const [sending, setSending] = useState(false)
  const [caret, setCaret] = useState(0)
  const [pick, setPick] = useState(0)
  // closedAt is the @ the person dismissed with Escape, so the list stays shut
  // while the caret is still in that name.
  const [closedAt, setClosedAt] = useState(-1)
  const area = useRef<HTMLTextAreaElement>(null)

  const at = complete ? mentionAt(body, caret) : null
  const options = at && at.start !== closedAt ? members.filter((m) => m.startsWith(at.prefix)) : []
  const open = options.length > 0
  const chosen = open ? Math.min(pick, options.length - 1) : 0

  const insert = (id: string) => {
    if (!at) return
    // A caret inside a name replaces all of it, not just the part before it.
    const end = caret + (/^[a-z0-9-]*/.exec(body.slice(caret))?.[0].length ?? 0)
    const next = body.slice(0, at.start) + '@' + id + ' ' + body.slice(end)
    const pos = at.start + id.length + 2
    // ⚠️ Set the text and the caret on the element now. A caret set on a later
    // frame lands after keys the person typed in between, and pulls the caret
    // back into the middle of them. Preact then finds the value already set.
    const el = area.current
    if (el) {
      el.value = next
      el.focus()
      el.setSelectionRange(pos, pos)
    }
    setBody(next)
    setCaret(pos)
    setPick(0)
  }

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
  const track = (el: HTMLTextAreaElement) => {
    const pos = el.selectionStart ?? el.value.length
    setCaret(pos)
    // An Escape holds only while the caret stays in that name. A name typed
    // again at the same place opens the list again.
    if (closedAt >= 0 && mentionAt(el.value, pos)?.start !== closedAt) setClosedAt(-1)
  }
  return (
    <form
      class="talkoot-composer"
      onSubmit={(e) => {
        e.preventDefault()
        send()
      }}
    >
      {open && (
        <ul class="talkoot-mentions" role="listbox" aria-label={t('Members')}>
          {options.map((m, i) => (
            <li
              key={m}
              role="option"
              aria-selected={i === chosen}
              class={i === chosen ? 'on' : ''}
              onMouseDown={(e) => {
                // Keep the textarea's focus and caret.
                e.preventDefault()
                insert(m)
              }}
            >
              @{m}
            </li>
          ))}
        </ul>
      )}
      <textarea
        ref={area}
        aria-label={t('Message to the team')}
        placeholder={to ? t('Message %s, or name a member with @', to) : t('Message the coordinator, or name a member with @')}
        value={body}
        rows={2}
        onInput={(e) => {
          const el = e.target as HTMLTextAreaElement
          setBody(el.value)
          track(el)
          setPick(0)
        }}
        onKeyUp={(e) => track(e.target as HTMLTextAreaElement)}
        onClick={(e) => track(e.target as HTMLTextAreaElement)}
        onKeyDown={(e) => {
          if (open) {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault()
              const step = e.key === 'ArrowDown' ? 1 : -1
              setPick((chosen + step + options.length) % options.length)
              return
            }
            if (e.key === 'Enter' || e.key === 'Tab') {
              e.preventDefault()
              insert(options[chosen])
              return
            }
            if (e.key === 'Escape') {
              e.preventDefault()
              setClosedAt(at!.start)
              return
            }
          }
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
