import { useCallback, useState } from 'preact/hooks'
import { t } from '../i18n'
import { copyToClipboard } from './browser'

// CopyButton copies `text` to the clipboard and flashes a check for ~1.2s. Used
// on assistant replies (the raw markdown source, so fenced code round-trips) and
// wherever a one-tap copy helps.
//
// `inline` picks the variant. The default is bubble chrome: pinned to a message
// corner and invisible until the row is hovered, which is right when the copy is
// incidental to reading. Anywhere it is a control in its own right — a header
// bar over a code block, a row holding a command meant to be run — that default
// renders NOTHING a user can see or reach, and being absolutely positioned it
// does not even sit where it was placed. Pass `inline` there.
export function CopyButton({ text, label, inline }: { text: string; label?: string; inline?: boolean }) {
  const [copied, setCopied] = useState(false)
  const onClick = useCallback(() => {
    void copyToClipboard(text).then((ok) => {
      if (!ok) return
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    })
  }, [text])
  const title = copied ? t('Copied') : label || t('Copy')
  return (
    <button
      class={`copy-btn${inline ? ' inline' : ''}${copied ? ' copied' : ''}`}
      title={title}
      aria-label={title}
      onClick={onClick}
    >
      {copied ? (
        <svg
          viewBox="0 0 24 24"
          width="14"
          height="14"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <path d="M20 6 9 17l-5-5" />
        </svg>
      ) : (
        <svg
          viewBox="0 0 24 24"
          width="14"
          height="14"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <rect x="9" y="9" width="11" height="11" rx="2" />
          <path d="M5 15V5a2 2 0 0 1 2-2h10" />
        </svg>
      )}
    </button>
  )
}
