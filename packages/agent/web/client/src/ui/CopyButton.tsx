import { useCallback, useState } from 'preact/hooks'
import { t } from '../i18n'
import { copyToClipboard } from './browser'

// CopyButton copies `text` to the clipboard and flashes a check for ~1.2s. Used
// on assistant replies (the raw markdown source, so fenced code round-trips) and
// wherever a one-tap copy helps.
export function CopyButton({ text, label }: { text: string; label?: string }) {
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
    <button class={`copy-btn${copied ? ' copied' : ''}`} title={title} aria-label={title} onClick={onClick}>
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
