import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { SkillInfo } from '../../platform/ctrlproto/types'
import type { ImageAttachment } from '../../platform/conversation/images'
import { fileToAttachment } from './attachments'

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
  onCancel,
}: {
  busy: boolean
  onSend: (text: string, images: ImageAttachment[]) => boolean
  onToast: (message: string) => void
  commands: SlashCommand[]
  skills: SkillInfo[]
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

  // Two autocomplete stages: command names while the text is a bare "/partial",
  // then skill names once "/skill " is being argued. Each item knows how to
  // apply itself on select.
  type MenuItem = { key: string; label: string; hint: string; apply: () => void }
  const cmdStage = /^\/(\S*)$/.exec(text)
  const skillStage = /^\/skill\s+(\S*)$/.exec(text)
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
