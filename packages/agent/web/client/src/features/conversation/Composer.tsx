import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { SkillInfo, WireFileEntry } from '../../platform/ctrlproto/types'
import type { ImageAttachment } from '../../platform/conversation/images'
import { humanBytes } from '../../ui/formatting'
import { atComplete } from './atcomplete'
import { fileToAttachment, tooLargeToAttach, type FileAttachment } from './attachments'

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
  onUpload,
  canAttachFiles = false,
  maxAttachmentBytes = 0,
  sessionID,
}: {
  busy: boolean
  onSend: (text: string, images: ImageAttachment[], attachments: FileAttachment[]) => boolean
  onToast: (message: string) => void
  commands: SlashCommand[]
  skills: SkillInfo[]
  // onUpload stages one file with the daemon. Injected rather than imported so
  // the composer stays ignorant of which session it is in — and so a test can
  // drive the drop path without a server.
  onUpload?: (f: File) => Promise<FileAttachment | { error: string }>
  // canAttachFiles is the daemon's answer to "can I take a file at all"
  // (ctrlproto FeatureAttachments). False on a carrier with no upload route, and
  // then a dropped non-image is refused out loud rather than vanishing.
  canAttachFiles?: boolean
  maxAttachmentBytes?: number
  // sessionID is the session a staged file belongs to. The composer does not
  // send it anywhere — onUpload closes over it host-side — but it has to KNOW
  // when it changes, because a staged id is only meaningful against the session
  // directory it was staged into. See the effect below.
  sessionID?: string
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
  const [attachments, setAttachments] = useState<FileAttachment[]>([])
  // Names of files whose upload is in flight, so a big drop shows progress
  // rather than nothing until it lands.
  const [uploading, setUploading] = useState<string[]>([])
  const [sel, setSel] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const ref = useRef<HTMLTextAreaElement>(null)
  // The session a staged file was staged INTO, readable from inside an upload
  // that is still in flight. A ref, not the prop, because the callback below
  // closed over the session it started in and needs to compare against the
  // session that is current when it lands.
  const sessionRef = useRef(sessionID)

  // Staged attachments do not survive a session change, because a staged id
  // means nothing outside the session directory it was written to: the daemon
  // resolves ids against the session it is prompted on, so sending these in
  // another session would report every one of them as expired.
  //
  // Only the attachments. Text and images are session-agnostic — a draft is
  // worth carrying across a switch, and an inline image rides the frame itself
  // — which is why this is an effect rather than a `key` on the component, the
  // blunter fix that would throw away a half-written message too.
  useEffect(() => {
    sessionRef.current = sessionID
    setAttachments([])
    setUploading([])
  }, [sessionID])
  // Auto-grow to content (capped at 40% of the viewport), and shrink back when
  // cleared — so multiline is visible and signals there's more context in play.
  //
  // The cap is measured against visualViewport, NOT window.innerHeight: iOS
  // holds innerHeight (and vh/dvh) at the full screen height while the software
  // keyboard covers the bottom half of it, so 40%-of-innerHeight is most of
  // what's actually visible — the growing box swallows the transcript and runs
  // on under the keyboard. visualViewport is the only height that shrinks with
  // the keyboard, and it re-fires on resize, so the cap re-tightens when the
  // keyboard opens under an already-grown box.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const grow = () => {
      const visible = window.visualViewport?.height ?? window.innerHeight
      el.style.height = 'auto'
      el.style.height = Math.min(el.scrollHeight, Math.round(visible * 0.4)) + 'px'
    }
    grow()
    const vv = window.visualViewport
    vv?.addEventListener('resize', grow)
    return () => vv?.removeEventListener('resize', grow)
  }, [text])

  // addFiles takes anything dropped or pasted and routes it by what the file is,
  // never silently discarding one.
  //
  // An allowlisted image under the frame budget rides the prompt inline, because
  // that is the only form vision can see. Everything else — a big image
  // included — is staged with the daemon, which hands back an id the prompt will
  // name. Before this, a non-image was filtered out at the drop handler and the
  // user got no feedback at all.
  const addFiles = async (dropped: File[]) => {
    const inline: ImageAttachment[] = []
    const toStage: File[] = []
    for (const f of dropped) {
      const image = await fileToAttachment(f)
      if (image) inline.push(image)
      else toStage.push(f)
    }
    if (inline.length) setImages((current) => [...current, ...inline])
    if (!toStage.length) return
    if (!canAttachFiles || !onUpload) {
      onToast(t('This daemon cannot take file attachments'))
      return
    }
    const sized = toStage.filter((f) => {
      if (!tooLargeToAttach(f, maxAttachmentBytes)) return true
      onToast(t('%s is too large (max %s)', f.name || t('file'), humanBytes(maxAttachmentBytes)))
      return false
    })
    if (!sized.length) return
    const names = sized.map((f) => f.name)
    // The session these are being staged into. An upload is not instant, so the
    // one that is current when it LANDS may not be the one it was written for.
    const startedOn = sessionRef.current
    setUploading((current) => [...current, ...names])
    await Promise.all(
      sized.map(async (f) => {
        const result = await onUpload(f)
        // Drop this file's own pending entry, matched by name rather than by
        // rebuilding the list, so two uploads finishing at once don't clobber
        // each other's progress.
        setUploading((current) => {
          const at = current.indexOf(f.name)
          return at < 0 ? current : [...current.slice(0, at), ...current.slice(at + 1)]
        })
        // The user moved on while this was uploading. The file is staged under
        // the session they left, so chipping it here would hand THIS session an
        // id it cannot resolve — the effect above clears the chips, and an
        // in-flight upload is the one path that can add one back afterwards. Its
        // error is dropped for the same reason: a failure in a session the user
        // has left is not something they can act on.
        if (sessionRef.current !== startedOn) return
        if ('error' in result) onToast(result.error)
        else setAttachments((current) => [...current, result])
      }),
    )
  }

  const submit = () => {
    if (!text.trim() && images.length === 0 && attachments.length === 0) return
    // Clear only if the send was accepted (a busy send carrying attachments is
    // refused, so they aren't lost).
    if (onSend(text, images, attachments)) {
      setText('')
      setImages([])
      setAttachments([])
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
      onSend('/' + command.name, [], [])
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
        // Every file, not just images: addFiles decides which ride inline and
        // which are staged with the daemon. Filtering here is what used to make
        // a dropped .csv disappear with no explanation.
        const dropped = [...(event.dataTransfer?.files ?? [])]
        if (dropped.length) {
          event.preventDefault()
          void addFiles(dropped)
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
      {(images.length > 0 || attachments.length > 0 || uploading.length > 0) && (
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
          {attachments.map((file) => (
            <div key={file.id} class="composer-chip composer-chip--file" title={file.name}>
              <span class="chip-name">{file.name}</span>
              <span class="chip-size">{humanBytes(file.size)}</span>
              <button
                class="chip-x"
                title={t('Remove')}
                aria-label={t('Remove')}
                onClick={() => setAttachments((current) => current.filter((f) => f.id !== file.id))}
              >
                ×
              </button>
            </div>
          ))}
          {uploading.map((name, index) => (
            <div key={'up' + index} class="composer-chip composer-chip--file is-uploading" title={name}>
              <span class="chip-name">{name}</span>
              <span class="chip-size">{t('uploading…')}</span>
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
          // Any pasted file, same as a drop. Text paste is untouched: only
          // clipboard items of kind "file" are taken, so pasting a CSV's
          // CONTENTS still lands as text, which is usually what was meant.
          const pasted = [...(event.clipboardData?.items ?? [])]
            .filter((item) => item.kind === 'file')
            .map((item) => item.getAsFile())
            .filter((file): file is File => file != null)
          if (pasted.length) {
            event.preventDefault()
            void addFiles(pasted)
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
