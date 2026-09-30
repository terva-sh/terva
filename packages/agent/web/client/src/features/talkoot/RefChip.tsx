import { createContext } from 'preact'
import { useContext, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { TalkootRefText } from '../../platform/ctrlproto/types'

// OpenRef reads the file a path: or note: reference names. The team view
// provides it, so every line that shows a reference can open one without the
// client passing through each component between.
export type OpenRef = (ref: string) => Promise<TalkootRefText>

export const OpenRefContext = createContext<OpenRef | null>(null)

// opens reports whether a reference names a file the daemon can read.
export function opens(ref: string): boolean {
  return ref.startsWith('path:') || ref.startsWith('note:')
}

// RefChip shows one reference. A path: or note: opens in place below it, and
// any other kind stays a label.
export function RefChip({ refText }: { refText: string }) {
  const open = useContext(OpenRefContext)
  const [shown, setShown] = useState(false)
  const [result, setResult] = useState<TalkootRefText | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  if (!open || !opens(refText)) return <code>{refText}</code>

  const toggle = () => {
    if (shown) {
      setShown(false)
      return
    }
    setShown(true)
    if (result || loading) return
    setLoading(true)
    setError('')
    open(refText).then(
      (r) => {
        setResult(r)
        setLoading(false)
      },
      (e: unknown) => {
        setError(e instanceof Error ? e.message : String(e))
        setLoading(false)
      },
    )
  }

  return (
    <span class="talkoot-ref">
      <button class="talkoot-ref-chip" aria-expanded={shown} title={shown ? t('Hide the file') : t('Open the file here')} onClick={toggle}>
        <code>{refText}</code>
      </button>
      {shown && (
        <div class="talkoot-ref-body">
          {loading && <span class="talkoot-note">{t('Reading…')}</span>}
          {error && <span class="talkoot-error">{error}</span>}
          {result?.binary && <span class="talkoot-note">{t('This file is not text, so it is not shown.')}</span>}
          {result && !result.binary && <pre>{result.text}</pre>}
          {result?.truncated && !result.binary && (
            <span class="talkoot-note">{t('Showing the first %s of %s bytes.', String(utf8Bytes(result.text)), String(result.size))}</span>
          )}
        </div>
      )}
    </span>
  )
}

// utf8Bytes counts the bytes the daemon sent. A string's length counts UTF-16
// code units, which is not what `size` measures.
function utf8Bytes(text: string): number {
  return new TextEncoder().encode(text).length
}
