import { render } from 'preact'
import { TalkootApp } from './TalkootApp'
import { applyScheme, currentScheme } from '../../scheme'
import { trackSafeArea } from '../../ui/safearea'
// The same sheets as the panel, in the panel's order: the Talkoot app is the
// panel's sibling and wears its theme, its buttons, and its interaction cards.
// talkoot.css comes last, after the panel's sheet, as it does in the panel.
// app.css holds the app's own layout.
import '../../ui/tokens.css'
import '../../ui/ui.css'
import '../../styles.css'
import '../../ui/talkoot/talkoot.css'
import './app.css'

// The panel's light, dark, or automatic scheme, before the first paint.
applyScheme(currentScheme())

// Freeze the notch insets into --safe-* before first paint (see ui/safearea.ts).
trackSafeArea()

// The Talkoot app's composition root, the third installable web UI, mounted at
// /talkoot/. It shares platform/ and ui/ with the panel and must never import
// the panel's app.tsx or anything under features/ (boundaries.test guards this).
const el = document.getElementById('app')
if (el) render(<TalkootApp />, el)
