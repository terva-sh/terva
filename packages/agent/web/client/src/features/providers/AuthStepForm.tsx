import { useState } from 'preact/hooks'

import type { AuthField, AuthFlowStep } from '../../platform/ctrlproto/types'
import { t } from '../../i18n'

// AuthStepForm renders whatever the daemon asked for.
//
// It knows nothing about any provider. It is handed a list of labelled fields and
// gives back a name→value map — which is the entire point of the descriptor: a
// provider that wants a field nobody anticipated costs a daemon change and
// nothing here. The one thing it will not do is decide what is VALID; integer
// fields go back as strings and the daemon parses, so there is exactly one
// authority on what a context window may be rather than three that can disagree.
export function AuthStepForm({
  step,
  onSubmit,
  onCancel,
  busy,
  error,
}: {
  step: AuthFlowStep
  onSubmit: (values: Record<string, string>) => void
  onCancel: () => void
  busy: boolean
  error: string
}) {
  const fields = step.fields ?? []
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(fields.map((f) => [f.name, f.default ?? ''])),
  )
  // A disabled button is not an explanation. Naming the fields still outstanding
  // is the difference between "the sign-in button does nothing" and knowing what
  // to type: an openai-compatible endpoint requires a model, and with that box
  // empty the form used to go silently dead on a perfectly good base URL.
  //
  // required_unless is how a field stops being required once another is filled:
  // naming an endpoint makes the default model pointless, because a named endpoint
  // discovers its own models. The daemon re-checks all of this at submit — this is
  // an affordance, not the authority.
  const filled = (name: string) => !!values[name]?.trim()
  const needed = (f: AuthField) => f.required && !(f.required_unless && filled(f.required_unless))
  const missing = fields.filter((f) => needed(f) && !filled(f.name))
  const incomplete = missing.length > 0

  return (
    <div class="prov-flow">
      {step.title ? <div class="prov-flow-title">{step.title}</div> : null}
      {step.lines?.map((l, i) => (
        <div key={i} class="prov-note">
          {l}
        </div>
      ))}

      {/* Displayed, never opened FOR you: the daemon may be on another machine
          entirely, and the browser that has to visit this is the one you are
          reading it in. */}
      {step.url ? (
        <a class="prov-url" href={step.url} target="_blank" rel="noreferrer noopener">
          {step.url}
        </a>
      ) : null}
      {step.user_code ? <div class="prov-code">{step.user_code}</div> : null}

      {step.kind === 'display' ? (
        <div class="prov-note">{t('Waiting for you to approve it — this updates by itself.')}</div>
      ) : null}

      {fields.map((f) => (
        <label key={f.name} class="prov-field">
          {/* "(optional)" tracks whether the field is required RIGHT NOW, not how it
              was declared: name an endpoint and the default model stops being needed,
              so the label has to stop asking for it. */}
          <span class="prov-label">
            {f.label}
            {needed(f) ? '' : ` ${t('(optional)')}`}
          </span>
          <input
            type={f.type === 'secret' ? 'password' : 'text'}
            inputMode={f.type === 'integer' ? 'numeric' : undefined}
            autocomplete="off"
            spellcheck={false}
            placeholder={f.placeholder}
            value={values[f.name] ?? ''}
            onInput={(e) => setValues({ ...values, [f.name]: (e.target as HTMLInputElement).value })}
          />
          {f.help ? <span class="prov-help">{f.help}</span> : null}
        </label>
      ))}

      {error ? <div class="prov-warn">{error}</div> : null}

      {step.kind === 'form' && incomplete && !busy ? (
        <div class="prov-note">
          {t('Still needed:')} {missing.map((f) => f.label).join(', ')}
        </div>
      ) : null}

      <div class="prov-actions">
        {step.kind === 'form' ? (
          <button class="btn" disabled={busy || incomplete} onClick={() => onSubmit(values)}>
            {busy ? t('Signing in…') : t('Sign in')}
          </button>
        ) : null}
        <button class="btn ghost" onClick={onCancel}>
          {step.kind === 'info' ? t('Close') : t('Cancel')}
        </button>
      </div>
    </div>
  )
}
