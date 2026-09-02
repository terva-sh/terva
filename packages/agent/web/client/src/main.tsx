import { render } from 'preact'
import { App } from './app'
import { applyScheme, currentScheme } from './scheme'
import { trackSafeArea } from './ui/safearea'
// ui/ui.css first: it styles what the shared ui/ components and markdown.ts
// emit, and importing it ahead of the app sheet lets the app override.
import './ui/ui.css'
import './styles.css'

// Freeze the notch insets into --safe-* before first paint so a top bar survives
// a fixed overlay (see ui/safearea.ts).
trackSafeArea()

// Apply the saved light/dark choice before the first render, so an override
// that disagrees with the OS does not paint the wrong palette first.
applyScheme(currentScheme())

const el = document.getElementById('app')
if (el) render(<App />, el)
