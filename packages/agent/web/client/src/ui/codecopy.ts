import { copyToClipboard } from './browser'

// Delegated copy for the code blocks markdown.ts renders.
//
// markdown.ts wraps every fenced block in .code-wrap carrying a .code-copy
// button, so the button's behaviour is not optional decoration — it ships with
// any surface that calls renderMarkdown. It lived as a private useCallback in
// the panel's ConversationTimeline, which meant Stage rendered the same buttons
// with nothing listening: every code block in a Stage transcript had a copy
// button that did nothing.
//
// It sits in ui/ next to the stylesheet that draws the button so the pair
// travels together. Returns true when it handled a copy, letting a caller that
// shares the click target (Stage's bubble also opens the inline editor) fall
// through only when the click was not on a copy button.
export function handleCodeCopyClick(event: MouseEvent): boolean {
  const button = (event.target as HTMLElement)?.closest?.('.code-copy') as HTMLElement | null
  if (!button) return false
  const text = button.parentElement?.querySelector('pre')?.textContent ?? ''
  if (!text) return false
  void copyToClipboard(text).then((ok) => {
    if (!ok) return
    button.classList.add('copied')
    setTimeout(() => button.classList.remove('copied'), 1200)
  })
  return true
}
