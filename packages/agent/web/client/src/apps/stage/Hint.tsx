// An accessible ⓘ hint that works on touch, not only on desktop hover. The native
// `title=` tooltip it replaces never surfaces on a phone — there's no hover — so
// the field explainers were dead weight on mobile. This renders a real button
// whose bubble shows on hover (desktop) AND on focus: a tap focuses the button, so
// :focus-within reveals the bubble and tapping away (blur) hides it. No JS state,
// no library — the CSS in stage.css (.stage-infohint*) does the reveal.
export function Hint(props: { text: string; label?: string }) {
  return (
    <span class="stage-infohint">
      <button type="button" class="stage-infohint__btn" aria-label={props.label ?? props.text}>
        ⓘ
      </button>
      <span class="stage-infohint__bubble" role="tooltip">
        {props.text}
      </span>
    </span>
  )
}
