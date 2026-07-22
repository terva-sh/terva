import { render } from 'preact'
import { Stage } from './Stage'
import { applyTheme, currentTheme } from './theme'
import { trackSafeArea } from '../../ui/safearea'
// ui/ui.css first: it styles what the shared ui/ components and markdown.ts
// emit, and importing it ahead of the app sheet lets the app override.
import '../../ui/ui.css'
import './stage.css'

// Apply the saved theme before the first paint, so there is no flash of the
// default palette on a re-skinned install.
applyTheme(currentTheme())

// Freeze the notch insets into --safe-* before first paint so a top bar or sheet
// survives a fixed overlay (see ui/safearea.ts).
trackSafeArea()

// The Stage app's composition root — the second installable web UI, mounted at
// /stage/. It shares platform/ (ctrlproto client + types + conversation store)
// and ui/ with the panel, but owns its own shell and theme; it must never import
// the panel's app.tsx (boundaries.test guards this).
const el = document.getElementById('app')
if (el) render(<Stage />, el)
