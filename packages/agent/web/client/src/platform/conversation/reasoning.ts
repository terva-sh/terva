/**
 * Shaping the model's live thinking summary into the single line the panel
 * shows while a turn runs.
 *
 * The Go side does the same thing for the TUI (modes/reasoning_line.go,
 * reasoningLineText). The two must agree: the same wire text renders in both
 * surfaces, and a user watching one and then the other should not see the
 * provider's markup in one of them.
 */

/**
 * reasoningLineText renders accumulated reasoning as one display line, or ''
 * when there is nothing worth showing.
 *
 * Only the CURRENT section survives. Providers separate sections with a blank
 * line and each supersedes the last — the model has moved from "reading the
 * config" to "editing the handler", and showing both would grow a log in a slot
 * one row tall.
 *
 * The result is squashed to a single line on purpose: the field usually carries
 * a short headline, but the same field carries multi-paragraph prose from other
 * providers, and newlines reaching the DOM would push the composer off screen.
 */
export function reasoningLineText(acc: string): string {
  const i = acc.lastIndexOf('\n\n')
  const section = i >= 0 ? acc.slice(i + 2) : acc
  return section
    .replace(/\*\*/g, '') // section headings are bolded by the openai backends
    .replace(/[\r\n]+/g, ' ')
    .trim()
    .replace(/\s+/g, ' ')
}
