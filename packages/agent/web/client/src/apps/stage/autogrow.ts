import { useLayoutEffect, useRef } from 'preact/hooks'

// useAutoGrow returns a ref for a <textarea> that keeps its height fitted to its
// content — up to the element's CSS max-height, past which it scrolls. Pass the
// textarea's current value so it also re-fits on programmatic changes (a draft
// filled by ✨ Suggest, a revert to an earlier draft), not only on keystrokes.
// The element's `rows` attribute acts as the minimum height.
export function useAutoGrow(value: string) {
  const ref = useRef<HTMLTextAreaElement>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }, [value])
  return ref
}
