import { useState } from 'preact/hooks'

import type { ModelParamSpec, ModelParamsView } from '../../platform/ctrlproto/types'
import { t } from '../../i18n'

// splitList mirrors the daemon's parseList: commas and spaces both separate,
// because a set typed into a box arrives both ways and neither is wrong. The
// daemon still parses what goes back, so this only has to be good enough to
// render.
function splitList(s: string): string[] {
  return s.split(/[\s,]+/).filter(Boolean)
}

// ListField edits a set: a checkbox for each value terva knows, and, when the
// daemon says the set is open, a box for one it does not.
//
// That box is not a convenience. clampEffortToDeclared leaves an effort it does
// not recognize alone, on the reasoning that an unknown effort is the server's
// own extension — so a closed picker here would leave that reachable only by
// hand-editing models.json, which is the whole failure this form exists to end.
function ListField({
  spec,
  value,
  onChange,
}: {
  spec: ModelParamSpec
  value: string
  onChange: (v: string) => void
}) {
  const opts = spec.options ?? []
  const known = new Set(opts)
  const tokens = splitList(value)
  const checked = new Set(tokens.filter((v) => known.has(v)))

  // The free box keeps its own text. Recomposing it on every keystroke would
  // eat the comma the moment it was typed, because splitting and rejoining a
  // half-written list is not the identity.
  const [otherText, setOtherText] = useState(() => tokens.filter((v) => !known.has(v)).join(', '))

  // Emitted in the daemon's own order rather than click order, so the value
  // reads as a scale and two operators who picked the same set save the same
  // string.
  const compose = (next: Set<string>, others: string[]) =>
    onChange([...opts.filter((o) => next.has(o)), ...others].join(', '))

  return (
    <div class="prov-list">
      <div class="prov-list-opts">
        {opts.map((o) => (
          <label key={o} class="prov-check">
            <input
              type="checkbox"
              checked={checked.has(o)}
              onChange={() => {
                const next = new Set(checked)
                if (next.has(o)) next.delete(o)
                else next.add(o)
                compose(next, splitList(otherText))
              }}
            />
            <span>{o}</span>
          </label>
        ))}
      </div>
      {spec.free_values ? (
        <input
          type="text"
          autocomplete="off"
          spellcheck={false}
          placeholder={t('another value this server accepts')}
          value={otherText}
          onInput={(e) => {
            const raw = (e.target as HTMLInputElement).value
            setOtherText(raw)
            compose(checked, splitList(raw))
          }}
        />
      ) : null}
    </div>
  )
}

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
  adding,
  providers,
  onAdd,
}: {
  view: ModelParamsView
  busy: boolean
  error: string
  onSave: (values: Record<string, string>) => void
  onReset: () => void
  onCancel: () => void
  // Turns the edit form into a create form: two required rows on top, no reset
  // affordance, and a save that goes to models.add instead of models.params.set.
  // `view` then describes the CLONE SOURCE rather than the model being made.
  adding?: boolean
  // The reachable set from models.list. Not derived from the model rows: a
  // provider with no models yet has no row, and it is the one an add form is
  // most needed for.
  providers?: string[]
  onAdd?: (provider: string, model: string, values: Record<string, string>) => void
}) {
  const params = view.params ?? []
  // 🪤 Adding seeds from `default`; editing seeds from `value`. The inversion is
  // deliberate and it is the whole reason clone-from works.
  //
  // `value` is only what models.json pins, and a clone source usually pins
  // nothing, so seeding an add form from it hands back empty boxes and creates a
  // model with no context window: no gauge, and auto-condensing never fires.
  // `default` is the effective value resolved off the source, which is what
  // "behaves like the model I cloned" actually means.
  //
  // This is the exact opposite of the placeholder rule below, and for the same
  // reason. Editing must not pre-fill a default, because a box holding one looks
  // like an override and gets saved as one. Adding must, because a synthetic
  // model has no layer underneath to inherit from, so every value has to be
  // explicit or it is zero.
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(params.map((p) => [p.key, (adding ? p.default : p.value) ?? ''])),
  )
  const [newID, setNewID] = useState('')
  const [newProvider, setNewProvider] = useState(view.provider)
  const [addError, setAddError] = useState('')
  const [confirmReset, setConfirmReset] = useState(false)

  // Refused here rather than at the daemon for the rows the daemon cannot see
  // the point of. The daemon still refuses all of these, plus the two this
  // cannot know: a duplicate id, and a provider with no credential.
  const submitAdd = () => {
    const id = newID.trim()
    if (!id) {
      setAddError(t('A model id is required.'))
      return
    }
    const prov = newProvider.trim()
    if (!prov) {
      setAddError(t('A provider is required.'))
      return
    }
    // A synthetic model has nothing underneath it, so a blank window is not
    // "inherit", it is zero. Safe but inert: no gauge, and the session grows
    // until the provider refuses the request.
    const win = (values['contextWindow'] ?? '').trim()
    if (!win || win === '0') {
      setAddError(t('A context window is required, or this model never auto-condenses.'))
      return
    }
    setAddError('')
    onAdd?.(prov, id, values)
  }

  // The default goes in the PLACEHOLDER, never in the box. A box pre-filled with
  // the default looks like an override and would be saved as one — pinning a value
  // the operator never chose, which then stops tracking the catalog when terva
  // learns a model's real window.
  const placeholder = (p: ModelParamSpec) => p.default || t('inherit')

  return (
    <div class="prov-flow">
      <div class="prov-flow-title">
        {adding ? t('Add a model like %s', `${view.provider}/${view.model}`) : `${view.provider}/${view.model}`}
      </div>
      <div class="prov-note">
        {adding
          ? t('Every value is explicit: a new model has no catalog row underneath to inherit from.')
          : t('Empty means inherit. These are saved per model.')}
      </div>

      {/* The two rows a clone cannot supply. The provider defaults to the source
          and stays editable, because a provider you are logged into that has no
          models yet has no row to clone from, and pinning this would put it out
          of reach entirely. */}
      {adding && (
        <>
          <label class="prov-field">
            <span class="prov-label">{t('id')}</span>
            <input
              type="text"
              autocomplete="off"
              spellcheck={false}
              placeholder={t('the id the provider knows this model by')}
              value={newID}
              onInput={(e) => setNewID((e.target as HTMLInputElement).value)}
            />
          </label>
          <label class="prov-field">
            <span class="prov-label">{t('provider')}</span>
            <select
              value={newProvider}
              onChange={(e) => setNewProvider((e.target as HTMLSelectElement).value)}
            >
              {(providers?.length ? providers : [view.provider]).map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </label>
        </>
      )}

      {params.map((p) => (
        <label key={p.key} class="prov-field">
          <span class="prov-label">{p.label}</span>
          {p.kind === 'tristate' ? (
            // Three states, never a checkbox. "Off" is the operator overruling
            // what terva believes about this model, and it keeps outranking
            // discovery; the empty value leaves the catalog and the live layer
            // deciding. A two-state control cannot say the difference, so it
            // would pin every model this form was ever opened on.
            <select
              value={values[p.key] ?? ''}
              onChange={(e) => setValues({ ...values, [p.key]: (e.target as HTMLSelectElement).value })}
            >
              <option value="">{p.default ? t('inherit (%s)', p.default) : t('inherit')}</option>
              <option value="on">{t('on')}</option>
              <option value="off">{t('off')}</option>
            </select>
          ) : p.kind === 'list' ? (
            <ListField
              spec={p}
              value={values[p.key] ?? ''}
              onChange={(v) => setValues({ ...values, [p.key]: v })}
            />
          ) : p.kind === 'enum' && p.options?.length ? (
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
      {addError ? <div class="prov-warn">{addError}</div> : null}

      <div class="prov-actions">
        {adding ? (
          <button class="btn" disabled={busy} onClick={submitAdd}>
            {busy ? t('Adding…') : t('Add model')}
          </button>
        ) : (
          <button class="btn" disabled={busy} onClick={() => onSave(values)}>
            {busy ? t('Saving…') : t('Save')}
          </button>
        )}

        {/* Only when there is something to reset. Offering it against a model with
            no entry would promise an undo for a change nobody made.

            The wording forks on `custom`, because the same button does two
            different things. With a catalog row underneath, dropping the entry
            restores the shipped values. With nothing underneath, the entry IS
            the model, so this removes it from the picker and there is no default
            to come back to. "Reset to defaults" over that second case offers
            something that cannot happen. */}
        {view.has_override && !adding ? (
          confirmReset ? (
            <>
              <span class="prov-help">
                {view.custom
                  ? t('Delete this model? It exists only in models.json, so it leaves the picker.')
                  : t('Drop every override for this model?')}
              </span>
              <button
                class="btn"
                onClick={() => {
                  setConfirmReset(false)
                  onReset()
                }}
              >
                {view.custom ? t('Delete') : t('Reset')}
              </button>
              <button class="btn ghost" onClick={() => setConfirmReset(false)}>
                {view.custom ? t('Keep it') : t('Keep them')}
              </button>
            </>
          ) : (
            <button class="btn ghost" disabled={busy} onClick={() => setConfirmReset(true)}>
              {view.custom ? t('Delete this model') : t('Reset to defaults')}
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
