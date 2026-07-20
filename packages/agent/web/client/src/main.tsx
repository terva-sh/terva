import { render } from 'preact'
import { App } from './app'
// ui/ui.css first: it styles what the shared ui/ components and markdown.ts
// emit, and importing it ahead of the app sheet lets the app override.
import './ui/ui.css'
import './styles.css'

const el = document.getElementById('app')
if (el) render(<App />, el)
