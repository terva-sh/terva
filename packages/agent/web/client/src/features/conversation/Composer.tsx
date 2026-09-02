import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ComposerDraft, SkillInfo, WireFileEntry } from '../../platform/ctrlproto/types'
import type { ImageAttachment } from '../../platform/conversation/images'
import { humanBytes } from '../../ui/formatting'
import { atComplete } from './atcomplete'
import { fileToAttachment, tooLargeToAttach, type FileAttachment } from './attachments'

// draftDebounceMs is how long the composer must sit unchanged before its draft
// is written. The same wait the TUI uses (interactive_draft.go): long enough
// that ordinary typing produces one write rather than one per keystroke, short
// enough that a crash costs a word rather than a thought.
const draftDebounceMs = 800

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
  onLoadDraft,
  onSaveDraft,
  suggestion,
  onAcceptSuggestion,
  onDismissSuggestion,
  onEmptyChange,
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
  // The persisted draft, injected in the same style as onUpload so the composer
  // stays ignorant of the client. onLoadDraft resolves to the session's stored
  // draft (null when it has none); onSaveDraft writes one, and rejects when the
  // daemon refuses it.
  onLoadDraft?: (sess: string) => Promise<ComposerDraft | null>
  onSaveDraft?: (sess: string, text: string) => Promise<void>
  // files is the daemon's workspace listing for the @-stage: null while the
  // daemon doesn't serve files.list (or nothing is fetched yet). The stage
  // calls onFilesNeeded when it activates, so the fetch is lazy — nothing
  // rides the wire until someone types "@".
  files?: WireFileEntry[] | null
  onFilesNeeded?: () => void
  onCancel: () => void
  // A suggested next line (suggest.next_step), offered as a strip above the
  // composer. Null or blank means nothing is on offer: an empty line is an
  // ordinary answer from the daemon rather than a failure, so it must render
  // nothing at all rather than an empty strip.
  suggestion?: string | null
  onAcceptSuggestion?: () => void
  onDismissSuggestion?: () => void
  // Reports whether the composer is empty. The host owns the idle trigger but
  // cannot see this text — it is local state here — and an unbidden offer must
  // not arrive over a composer the user has already started writing in. Pass a
  // stable callback: this fires from an effect keyed on the text.
  onEmptyChange?: (empty: boolean) => void
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

  // Emptiness is reported upward because the host drives the idle trigger and
  // cannot see this text. Trimmed: a composer holding only whitespace is empty
  // for the purpose of "the user has not started writing".
  useEffect(() => {
    onEmptyChange?.(text.trim() === '')
  }, [text, onEmptyChange])
  // The session a staged file was staged INTO, readable from inside an upload
  // that is still in flight. A ref, not the prop, because the callback below
  // closed over the session it started in and needs to compare against the
  // session that is current when it lands.
  const sessionRef = useRef(sessionID)

  // --- the persisted draft: stage 6 of docs/proposals/session-state-sidecar.md
  //
  // What you typed and did not send belongs to the SESSION rather than to this
  // tab. The daemon keeps it beside the transcript, so this panel and the TUI
  // read one draft instead of each holding a private copy the other never
  // learns about.

  // Mirrors of the live state for the savers that run outside render — the
  // session-change effect and the pagehide handler, neither of which can read
  // the state variables of the render that queued them. Assigned during render
  // so they are never a frame behind.
  const textRef = useRef(text)
  textRef.current = text
  const imagesRef = useRef(images)
  imagesRef.current = images
  const attachRef = useRef(attachments)
  attachRef.current = attachments
  // The session whose draft has been read back. Until this equals sessionID,
  // nothing may be SAVED: the composer is empty because the read has not landed
  // yet, and writing that empty string would delete the very draft being
  // fetched. State rather than a ref, so the save below re-runs when it lands.
  const [restoredSess, setRestoredSess] = useState<string | undefined>(undefined)
  // What the daemon already holds, so an unchanged composer costs no writes.
  const savedText = useRef('')
  // The stored draft is an unaccepted SUGGESTION, which only the TUI can have
  // left: this panel has no ghost affordance and never makes one. So it neither
  // shows it nor CLEARS it — wiping an offer this front end cannot even display
  // would destroy something on the user's behalf without ever telling them it
  // was there.
  const storedIsSuggestion = useRef(false)
  // At most one notice per session for each of these, because a debounced saver
  // that toasts on every write is a stutter rather than a warning.
  const warnedAttachments = useRef(false)
  const warnedFailure = useRef(false)

  // saveDraftNow writes immediately, for the paths with no debounce left to
  // wait for. sess is passed rather than read, so the session-change path can
  // name the session being LEFT.
  const saveDraftNow = (sess: string | undefined, value: string) => {
    if (!sess || !onSaveDraft) return
    if (value === savedText.current) return
    if (!value.trim() && storedIsSuggestion.current) return
    savedText.current = value
    // Best-effort by design: both callers run while the user is leaving, and
    // there is nowhere left to show an error they would still be looking at.
    void onSaveDraft(sess, value).catch(() => {})
  }

  // Staged attachments do not survive a session change, because a staged id
  // means nothing outside the session directory it was written to: the daemon
  // resolves ids against the session it is prompted on, so sending these in
  // another session would report every one of them as expired.
  //
  // An effect rather than a `key` on the component, which would throw away far
  // more than it fixed. Images stay: an inline image rides the frame itself.
  //
  // TEXT no longer travels either, and that is a deliberate change. It used to
  // follow the user across a switch, on the reasoning that a half-written
  // message is worth keeping. It still is — but it is kept in the session it
  // was written FOR now, rather than dragged into the next one. A draft that
  // travelled would be saved into the slot of a session it was not written for,
  // overwriting that session's own unsent message with a stranger's.
  useEffect(() => {
    const prev = sessionRef.current
    sessionRef.current = sessionID
    setAttachments([])
    setUploading([])
    if (prev !== undefined && prev !== sessionID) {
      saveDraftNow(prev, textRef.current)
      setText('')
    }
    savedText.current = ''
    storedIsSuggestion.current = false
    warnedAttachments.current = false
    warnedFailure.current = false
    setRestoredSess(undefined)
    if (!sessionID || !onLoadDraft) {
      // Nothing to read, but the composer must still become savable.
      setRestoredSess(sessionID)
      return
    }
    let live = true
    void onLoadDraft(sessionID)
      .then((draft) => {
        if (!live) return
        if (draft && draft.text.trim()) {
          if (draft.source === 'suggestion') {
            storedIsSuggestion.current = true
          } else if (!textRef.current.trim()) {
            // Only into an EMPTY composer. The read took a round trip, and
            // anything typed since outranks what was typed before — overwriting
            // live keystrokes is the one way this could cost writing rather
            // than save it.
            setText(draft.text)
            savedText.current = draft.text
          }
        }
        setRestoredSess(sessionID)
      })
      .catch(() => {
        // A draft that cannot be read is not worth a banner: nothing the user
        // did is waiting on it. But the composer must still become savable, or
        // one failed read would silently stop this session persisting anything.
        if (live) setRestoredSess(sessionID)
      })
    return () => {
      live = false
    }
  }, [sessionID])

  // The debounced save. A write per keystroke would be a round trip per
  // keystroke; 800ms is what the TUI waits (interactive_draft.go), so a crash
  // costs a word rather than a thought.
  useEffect(() => {
    if (!sessionID || !onSaveDraft) return
    if (restoredSess !== sessionID) return
    if (text === savedText.current) return
    if (!text.trim() && storedIsSuggestion.current) return
    const timer = setTimeout(() => {
      if (!warnedAttachments.current && (imagesRef.current.length || attachRef.current.length)) {
        warnedAttachments.current = true
        onToast(t('the draft is kept for this session; attachments are not'))
      }
      // Claim the write BEFORE it goes out, so a slow round trip cannot be
      // raced by the next one, and a failure cannot retry itself every 800ms
      // for as long as it keeps failing. A later keystroke changes the text and
      // tries again on its own, which is the retry that costs nothing.
      savedText.current = text
      // Writing makes the slot this panel's; any suggestion that was in it is
      // gone, and pretending otherwise would suppress every later save.
      storedIsSuggestion.current = false
      void onSaveDraft(sessionID, text).catch(() => {
        if (warnedFailure.current) return
        warnedFailure.current = true
        onToast(t('this draft was not kept — it will not survive a reload'))
      })
    }, draftDebounceMs)
    return () => clearTimeout(timer)
  }, [text, sessionID, restoredSess, onSaveDraft, onToast])

  // The last chance a tab gets. Best-effort in a way the TUI's flush is not: a
  // browser may cut the connection before this lands, which is why the debounce
  // above is the real guarantee and this is only the tail of it.
  useEffect(() => {
    const flush = () => saveDraftNow(sessionID, textRef.current)
    addEventListener('pagehide', flush)
    return () => removeEventListener('pagehide', flush)
  }, [sessionID, onSaveDraft])
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
      // Drop the offer rather than hide it. A suggestion computed against a
      // conversation that has since moved is worse than no suggestion, because
      // it still looks current.
      onDismissSuggestion?.()
    }
  }

  // The offered line, or '' when there is nothing to show.
  const offer = (suggestion ?? '').trim()

  // Accept INSERTS at the cursor instead of replacing the text. A suggestion is
  // the machine's idea and never displaces the user's own words — the same
  // precedence the terminal follows, where a suggestion loses to a withdrawn
  // prompt and to a stashed draft alike. On an empty composer, which is the
  // common case, inserting is indistinguishable from setting.
  const acceptSuggestion = () => {
    if (!offer) return
    const node = ref.current
    const at = node ? (node.selectionStart ?? text.length) : text.length
    setText(text.slice(0, at) + offer + text.slice(at))
    setSel(0)
    setDismissed(false)
    onAcceptSuggestion?.()
    // Focus and caret after the render that applied the text, so the caret is
    // not clobbered by the value update.
    queueMicrotask(() => {
      const el = ref.current
      if (!el) return
      el.focus()
      const caret = at + offer.length
      el.setSelectionRange(caret, caret)
    })
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
      {offer && (
        <div class="composer-offer">
          <span class="composer-offer__label">{t('Suggested next step')}</span>
          <button
            class="composer-offer__line"
            title={t('Use this line (Tab)')}
            onClick={acceptSuggestion}
          >
            {offer}
          </button>
          <button
            class="composer-offer__x"
            title={t('Dismiss (Esc)')}
            aria-label={t('Dismiss suggestion')}
            onClick={() => onDismissSuggestion?.()}
          >
            ×
          </button>
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
          // The offer claims Tab and Escape only once the @-stage and the
          // slash menu above have declined them, so it never steals a key that
          // already meant something. Both keys are no-ops when nothing is
          // offered, which is all but a few seconds of the composer's life.
          if (offer) {
            if (event.key === 'Tab') {
              event.preventDefault()
              acceptSuggestion()
              return
            }
            if (event.key === 'Escape') {
              event.preventDefault()
              onDismissSuggestion?.()
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
