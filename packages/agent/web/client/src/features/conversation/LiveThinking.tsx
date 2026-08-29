import { t } from '../../i18n'
import { Markdown } from '../../ui/Markdown'
import { usePinnedTail } from '../../ui/pinnedtail'

// LiveThinking shows the thinking of the turn IN FLIGHT, as a block in the
// transcript rather than a row above the composer.
//
// It replaced a single clipped line (`.reasoning-line`) that kept only the
// current section and threw the rest away. That was the right shape for a slot
// one row tall, and the wrong shape for the only window in which thinking is
// new information: the model's reasoning scrolled past, unreadable, and was
// then dropped when the turn ended.
//
// 🔑 It is NOT transcript. It renders from SessionState.reasoning, which the
// store clears at assistant_start and turn_end. Once the turn lands, the
// recorded block (ReasoningDisclosure) takes over — and only if "Record
// thinking" is on, which is why thinking can be visible while a turn runs and
// legitimately absent from the message afterwards.
//
// The height cap lives in CSS (.reasoning-summary--live), not here: the same
// field carries a short headline from one provider and multi-paragraph prose
// from another, and uncapped growth would push the composer off screen — the
// concern the one-line row existed to solve, kept without the truncation.
export function LiveThinking({ text }: { text: string }) {
  // Pin to the newest thought: the block is capped and scrolls, so unpinned it
  // would sit at the top showing thinking the model has already moved past.
  //
  // 🪤 Through the shared hook, never a bare `scrollTop = scrollHeight` — which
  // is what this first did, and what ui/pinnedtail.test.tsx's census exists to
  // catch. A raw assignment re-pins on EVERY delta, so a reader who scrolls up
  // inside the block to re-read a step is yanked back to the bottom several
  // times a second. The hook re-pins only while they are already at the end.
  const { ref, onScroll } = usePinnedTail<HTMLDivElement>([text])

  if (!text.trim()) return null

  return (
    <div class="reasoning reasoning--live">
      {/* Not a button: there is nothing to toggle while it streams, and a
          disabled-looking control that does nothing reads as broken. */}
      <div class="reasoning-toggle reasoning-toggle--static">
        <span class="reasoning-chev">▾</span>
        {t('thinking')}
      </div>
      <div class="reasoning-summary reasoning-summary--live md" ref={ref} onScroll={onScroll}>
        <Markdown text={text} />
      </div>
    </div>
  )
}
