import type { ModelInfo } from '../../platform/ctrlproto/types'

// The web mirror of provider.Model.Label in Go. Both sides answer the same
// question — "what do I call this model on a surface that would otherwise
// print its id" — and both answer it the same way, so the header, the picker,
// and the TUI status bar never disagree about what a model is called.

// renamedTo returns the name the operator gave this model in models.json, or
// undefined when they gave it none. Guarded on `renamed` rather than on
// display_name being present: the daemon sends a display name for EVERY model,
// and catalog ones run longer than the ids they would replace ("Claude Sonnet
// 4.5 (latest)" vs claude-sonnet-4-5), so preferring them would make a list
// harder to scan rather than easier.
export function renamedTo(model: ModelInfo): string | undefined {
  return model.renamed && model.display_name ? model.display_name : undefined
}

// modelLabel is what to show where only one string fits.
export function modelLabel(model: ModelInfo): string {
  return renamedTo(model) ?? model.id
}
