import { useState } from 'preact/hooks'

import type { ModelParamSpec, ModelParamsView } from '../../platform/ctrlproto/types'
import { t } from '../../i18n'

// The per-model overrides that live in models.json — context window, max tokens,
// temperature — which until now only the TUI could edit.
//
// It knows none of their names. The daemon describes each setting (a label, a
// kind, the default this model would take, the value currently pinned) and this
// renders whatever it was handed, exactly as AuthStepForm does for a login. A
// provider that gains a new knob costs a daemon change and nothing here.
//
// The one thing it will NOT do is decide what is valid: values go back as strings
// and the daemon parses them through the same code the TUI commits through. Two
// opinions on what a context window may be is one too many.
export function ModelParamsForm({
  view,
  busy,
  error,
  onSave,
  onReset,
  onCancel,
}: {
  view: ModelParamsView
  busy: boolean
  error: string
  onSave: (values: Record<string, string>) => void
  onReset: () => void
  onCancel: () => void
}) {
  const params = view.params ?? []
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(params.map((p) => [p.key, p.value ?? ''])),
  )
  const [confirmReset, setConfirmReset] = useState(false)

  // The default goes in the PLACEHOLDER, never in the box. A box pre-filled with
  // the default looks like an override and would be saved as one — pinning a value
  // the operator never chose, which then stops tracking the catalog when terva
  // learns a model's real window.
  const placeholder = (p: ModelParamSpec) => p.default || t('inherit')

  return (
    <div class="prov-flow">
      <div class="prov-flow-title">{`${view.provider}/${view.model}`}</div>
      <div class="prov-note">{t('Empty means inherit. These are saved per model.')}</div>

      {params.map((p) => (
        <label key={p.key} class="prov-field">
          <span class="prov-label">{p.label}</span>
          {p.kind === 'enum' && p.options?.length ? (
            // A closed set gets a picker, never a text box. The options are the
            // daemon's, per model: a thinking ladder collapses rungs that reach
            // a given model as one wire value, so a list hardcoded here would
            // offer levels the model cannot tell apart.
            <select
              value={values[p.key] ?? ''}
              onChange={(e) => setValues({ ...values, [p.key]: (e.target as HTMLSelectElement).value })}
            >
              {/* The empty entry is "inherit", and it NAMES what would apply —
                  the same job the placeholder does for a text field. An
                  "inherit" that named nothing is what made this setting read
                  as broken in the terminal. */}
              <option value="">{p.default ? t('inherit (%s)', p.default) : t('inherit')}</option>
              {p.options.map((o) => (
                <option key={o} value={o}>
                  {o}
                </option>
              ))}
            </select>
          ) : (
            <input
              type="text"
              inputMode={p.kind === 'int' ? 'numeric' : p.kind === 'float' ? 'decimal' : undefined}
              autocomplete="off"
              spellcheck={false}
              placeholder={placeholder(p)}
              value={values[p.key] ?? ''}
              onInput={(e) => setValues({ ...values, [p.key]: (e.target as HTMLInputElement).value })}
            />
          )}
          {p.help ? <span class="prov-help">{p.help}</span> : null}
        </label>
      ))}

      {error ? <div class="prov-warn">{error}</div> : null}

      <div class="prov-actions">
        <button class="btn" disabled={busy} onClick={() => onSave(values)}>
          {busy ? t('Saving…') : t('Save')}
        </button>

        {/* Only when there is something to reset. Offering it against a model with
            no entry would promise an undo for a change nobody made. */}
        {view.has_override ? (
          confirmReset ? (
            <>
              <span class="prov-help">{t('Drop every override for this model?')}</span>
              <button
                class="btn"
                onClick={() => {
                  setConfirmReset(false)
                  onReset()
                }}
              >
                {t('Reset')}
              </button>
              <button class="btn ghost" onClick={() => setConfirmReset(false)}>
                {t('Keep them')}
              </button>
            </>
          ) : (
            <button class="btn ghost" disabled={busy} onClick={() => setConfirmReset(true)}>
              {t('Reset to defaults')}
            </button>
          )
        ) : null}

        <button class="btn ghost" onClick={onCancel}>
          {t('Cancel')}
        </button>
      </div>
    </div>
  )
}
