import { Component, createElement, type ComponentType, type VNode } from 'preact'

// memo skips re-rendering a component whose props are shallow-equal to last time.
//
// Preact exports this only from `preact/compat`, which drags in the whole React
// compatibility shim for one thirty-line idea. This is the thirty lines.
//
// It matters because the conversation list re-renders on EVERY token delta — thirty-odd
// times a second — and every row in it re-rendered with it, though only the last one had
// changed. Rows are reference-stable during a turn (applyEvent replaces only the item it
// touches), so shallow equality catches the rest. A turn-end snapshot rebuilds every item
// object and every row re-renders once, which is correct: that is when they may actually
// have changed.
function shallowEqual(a: Record<string, unknown>, b: Record<string, unknown>): boolean {
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length) return false
  for (const k of ka) {
    if (!Object.is(a[k], b[k])) return false
  }
  return true
}

export function memo<P extends Record<string, unknown>>(Inner: ComponentType<P>): ComponentType<P> {
  return class Memo extends Component<P> {
    shouldComponentUpdate(next: P): boolean {
      return !shallowEqual(this.props, next)
    }
    render(): VNode<P> {
      return createElement(Inner, this.props)
    }
  }
}
