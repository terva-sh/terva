import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { SkillInfo, WireFileEntry } from '../../platform/ctrlproto/types'
import type { ImageAttachment } from '../../platform/conversation/images'
import { atComplete } from './atcomplete'
import { fileToAttachment } from './attachments'

// matchFiles ranks the workspace file list against an @-query: substring
// hits first (earlier is better), then subsequence hits (the TUI ranks with
// a fuzzy matcher; a subsequence pass is the same recall for a menu this
// short). Case-insensitive, capped — the menu is a picker, not a listing.
export function matchFiles(files: WireFileEntry[], query: string, cap = 8): WireFileEntry[] {
  if (!query) return files.slice(0, cap)
  const q = query.toLowerCase()
  const subs: { f: WireFileEntry; at: number }[] = []
  const seq: WireFileEntry[] = []
  for (const f of files) {
    const p = f.path.toLowerCase()
    const at = p.indexOf(q)
    if (at >= 0) {
      subs.push({ f, at })
      continue
    }
    let i = 0
    for (const ch of p) {
      if (ch === q[i]) i++
      if (i === q.length) break
    }
    if (i === q.length) seq.push(f)
  }
  subs.sort((a, b) => a.at - b.at || a.f.path.length - b.f.path.length)
  return [...subs.map((s) => s.f), ...seq].slice(0, cap)
}

// extractAtQuery mirrors the TUI picker's trigger: the last "@" at start or
// after a space opens the stage, and the query is everything after it (no
// whitespace). Returns null when no @-token is live at the end of the text.
export function extractAtQuery(text: string): { start: number; query: string } | null {
  const idx = text.lastIndexOf('@')
  if (idx < 0) return null
  if (idx > 0 && text[idx - 1] !== ' ' && text[idx - 1] !== '\n') return null
  const query = text.slice(idx + 1)
  if (/[\s]/.test(query)) return null
  return { start: idx, query }
}

// SlashCommand is one user-driven composer command (/compact, /skill, …). `run`
// receives everything after the command name; `arg` is a display hint for the
// autocomplete and marks whether the command takes an argument.
export interface SlashCommand {
  name: string
  arg?: string
  desc: string
  run: (arg: string) => void
}

export function Composer({
  busy,
  onSend,
  onToast,
  commands,
  skills,
  files,
  onFilesNeeded,
  onCancel,
}: {
  busy: boolean
  onSend: (text: string, images: ImageAttachment[]) => boolean
  onToast: (message: string) => void
  commands: SlashCommand[]
  skills: SkillInfo[]
  // files is the daemon's workspace listing for the @-stage: null while the
  // daemon doesn't serve files.list (or nothing is fetched yet). The stage
  // calls onFilesNeeded when it activates, so the fetch is lazy — nothing
  // rides the wire until someone types "@".
  files?: WireFileEntry[] | null
  onFilesNeeded?: () => void
  onCancel: () => void
}) {
  const [text, setText] = useState('')
  const [images, setImages] = useState<ImageAttachment[]>([])
  const [sel, setSel] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const ref = useRef<HTMLTextAreaElement>(null)
  // Auto-grow to content (capped at 40vh), and shrink back when cleared — so
  // multiline is visible and signals there's more context in play.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, Math.round(window.innerHeight * 0.4)) + 'px'
  }, [text])

  // addFiles reads image files (from paste or drop) into attachments, toasting
  // any that are the wrong type or too big rather than silently dropping them.
  const addFiles = async (files: File[]) => {
    const results = await Promise.all(files.map(fileToAttachment))
    const ok: ImageAttachment[] = []
    for (const result of results) {
      if (result && 'error' in result) onToast(result.error)
      else if (result) ok.push(result)
    }
    if (ok.length) setImages((current) => [...current, ...ok])
  }

  const submit = () => {
    if (!text.trim() && images.length === 0) return
    // Clear only if the send was accepted (a busy image send is refused so the
    // attachments aren't lost).
    if (onSend(text, images)) {
      setText('')
      setImages([])
      setDismissed(false)
    }
  }
  // Choose a command from the menu: argless commands run immediately; commands
  // that take an argument prime "/name " and keep focus for the argument.
  const chooseCmd = (command: SlashCommand) => {
    if (command.arg) {
      setText('/' + command.name + ' ')
      ref.current?.focus()
    } else {
      onSend('/' + command.name, [])
      setText('')
    }
    setDismissed(false)
  }

  // Three autocomplete stages: command names while the text is a bare
  // "/partial", skill names once "/skill " is being argued, and workspace
  // files behind a live "@"-token (daemon-listed; see files prop). Each item
  // knows how to apply itself on select.
  type MenuItem = { key: string; label: string; hint: string; apply: () => void }
  const cmdStage = /^\/(\S*)$/.exec(text)
  const skillStage = /^\/skill\s+(\S*)$/.exec(text)
  const atStage = onFilesNeeded ? extractAtQuery(text) : null
  // Lazy-load the listing the first time an @-stage goes live (and refresh
  // per the parent's TTL policy). An effect, not render-time work: the fetch
  // sets parent state.
  useEffect(() => {
    if (atStage) onFilesNeeded?.()
  }, [atStage != null])
  let menu: MenuItem[] = []
  if (cmdStage) {
    const query = cmdStage[1].toLowerCase()
    menu = commands
      .filter((command) => command.name.startsWith(query))
      .map((command) => ({
        key: command.name,
        label: '/' + command.name + (command.arg ? ' ' + command.arg : ''),
        hint: command.desc,
        apply: () => chooseCmd(command),
      }))
  } else if (skillStage) {
    const query = skillStage[1].toLowerCase()
    menu = skills
      .filter((skill) => skill.name.toLowerCase().startsWith(query))
      .map((skill) => ({
        key: skill.name,
        label: skill.name,
        hint: skill.description ?? '',
        apply: () => {
          setText('/skill ' + skill.name + ' ')
          ref.current?.focus()
          setDismissed(false)
        },
      }))
  } else if (atStage && files && files.length) {
    menu = matchFiles(files, atStage.query).map((file) => ({
      key: file.path,
      label: file.path + (file.dir ? '/' : ''),
      hint: '',
      // Selecting a file replaces the live @-token with the path (relative
      // to the workspace cwd — the daemon's tools resolve it there, and
      // it's what a user would have typed by hand) plus a space. Selecting
      // a directory keeps the @ and appends "/", so the stage stays live
      // and the query narrows into that directory.
      apply: () => {
        const head = text.slice(0, atStage.start)
        setText(file.dir ? head + '@' + file.path + '/' : head + file.path + ' ')
        ref.current?.focus()
        setDismissed(false)
        setSel(0)
      },
    }))
  }
  const menuOpen = menu.length > 0 && !dismissed
  const clampedSel = Math.min(sel, Math.max(0, menu.length - 1))

  return (
    <footer
      class="composer"
      onDragOver={(event) => event.preventDefault()}
      onDrop={(event) => {
        const files = [...(event.dataTransfer?.files ?? [])].filter((file) => file.type.startsWith('image/'))
        if (files.length) {
          event.preventDefault()
          void addFiles(files)
        }
      }}
    >
      {menuOpen && (
        <div class="slash-menu" role="listbox">
          {menu.map((item, index) => (
            <button
              key={item.key}
              class={`slash-item${index === clampedSel ? ' sel' : ''}`}
              role="option"
              aria-selected={index === clampedSel}
              onMouseDown={(event) => {
                event.preventDefault()
                item.apply()
              }}
            >
              <span class="slash-name">{item.label}</span>
              {item.hint && <span class="slash-desc">{item.hint}</span>}
            </button>
          ))}
        </div>
      )}
      {images.length > 0 && (
        <div class="composer-chips">
          {images.map((image, index) => (
            <div key={index} class="composer-chip">
              <img src={`data:${image.mime};base64,${image.data}`} alt={t('attached image')} />
              <button
                class="chip-x"
                title={t('Remove')}
                aria-label={t('Remove')}
                onClick={() => setImages((current) => current.filter((_, itemIndex) => itemIndex !== index))}
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}
      <textarea
        ref={ref}
        rows={1}
        value={text}
        placeholder={t('Message terva…')}
        onPaste={(event) => {
          const files = [...(event.clipboardData?.items ?? [])]
            .filter((item) => item.kind === 'file' && item.type.startsWith('image/'))
            .map((item) => item.getAsFile())
            .filter((file): file is File => file != null)
          if (files.length) {
            event.preventDefault()
            void addFiles(files)
          }
        }}
        onInput={(event) => {
          setText((event.target as HTMLTextAreaElement).value)
          setSel(0)
          setDismissed(false)
        }}
        onKeyDown={(event) => {
          // Tab on a live @-token is shell-style completion (atComplete —
          // the TUI runs the same fixture-pinned semantics): extend to the
          // unique candidate or the longest common prefix, never commit.
          // Enter still applies the highlighted row. Even a no-op consumes
          // the keystroke, like a shell — and works after Escape dismissed
          // the menu, as completion doesn't need the popup.
          if (event.key === 'Tab' && atStage && files && files.length) {
            event.preventDefault()
            const [extended] = atComplete(files, atStage.query)
            if (extended !== atStage.query) {
              setText(text.slice(0, atStage.start + 1) + extended)
              setSel(0)
              setDismissed(false)
            }
            return
          }
          if (menuOpen) {
            if (event.key === 'ArrowDown') {
              event.preventDefault()
              setSel((selected) => Math.min(selected + 1, menu.length - 1))
              return
            }
            if (event.key === 'ArrowUp') {
              event.preventDefault()
              setSel((selected) => Math.max(selected - 1, 0))
              return
            }
            if (event.key === 'Escape') {
              event.preventDefault()
              setDismissed(true)
              return
            }
            if (event.key === 'Tab' || (event.key === 'Enter' && !event.shiftKey)) {
              event.preventDefault()
              menu[clampedSel].apply()
              return
            }
          }
          if (event.key === 'Enter' && !event.shiftKey) {
            event.preventDefault()
            submit()
          }
        }}
      />
      {busy ? (
        <button class="btn danger" onClick={onCancel} title={t('Stop')}>
          {t('Stop')}
        </button>
      ) : (
        <button class="btn primary" onClick={submit}>
          {t('Send')}
        </button>
      )}
    </footer>
  )
}
