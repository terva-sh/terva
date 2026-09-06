import type { RefObject } from 'preact'
import { useLayoutEffect, useRef } from 'preact/hooks'

// keyCode 229 covers IMEs that end composition before the confirmation keydown.
export function composingKey(event: KeyboardEvent, composing: boolean): boolean {
  return composing || event.isComposing || event.keyCode === 229
}

export function useComposition(ref: RefObject<HTMLTextAreaElement>) {
  const active = useRef(false)
  useLayoutEffect(() => {
    const input = ref.current
    if (!input) return
    const start = () => { active.current = true }
    const end = () => { active.current = false }
    // Chromium lacks oncompositionstart as a DOM property. Preact's JSX event
    // casing inference therefore needs native listeners for these events.
    input.addEventListener('compositionstart', start)
    input.addEventListener('compositionend', end)
    input.addEventListener('blur', end)
    return () => {
      input.removeEventListener('compositionstart', start)
      input.removeEventListener('compositionend', end)
      input.removeEventListener('blur', end)
      active.current = false
    }
  }, [ref])
  return (event: KeyboardEvent) => composingKey(event, active.current)
}
