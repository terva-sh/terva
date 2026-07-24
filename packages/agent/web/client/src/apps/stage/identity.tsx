import { useState } from 'preact/hooks'
import { t } from '../../i18n'

// Inclusive default option lists for the identity dropdowns. The wire is free-text
// (UserPersonaView.gender/pronouns are plain strings), so "Other…" is just an
// unlisted value — a new option needs no server change, and the lists stay
// inclusive and editable right here.
// i18n-exempt — these ARE the committed wire values (they reach the model's
// prompt verbatim), not display-only labels; translating the label would
// diverge from the stored value. Localizing identity options is a wire-level
// question, not a render-time one.
export const PRONOUN_OPTIONS = ['she/her', 'he/him', 'they/them', 'she/they', 'he/they', 'it/its', 'any', 'ask']
export const GENDER_OPTIONS = ['Woman', 'Man', 'Non-binary', 'Genderfluid', 'Agender', 'Prefer not to say']

// IdentityField is an inclusive dropdown with an "Other…" escape that reveals a
// free-text input. The value it edits is always a plain string. Picking a preset
// commits immediately (with the chosen value passed through, so the parent never
// reads stale state); typing a custom value commits on blur.
//
// Shared by the steering drawer's in-scene You tab and the studio's You screen —
// one definition, so the option lists and the "Other…" escape cannot drift
// between the two places the same persona is edited.
export function IdentityField(props: {
  label: string
  placeholder: string
  options: string[]
  value: string
  onChange: (v: string) => void
  onCommit: (v: string) => void
}) {
  const { label, placeholder, options, value, onChange, onCommit } = props
  const custom = value !== '' && !options.includes(value)
  const [openOther, setOpenOther] = useState(custom)
  const showOther = openOther || custom
  return (
    <label class="stage-identity">
      <span class="stage-identity__label">{label}</span>
      <select
        class="stage-identity__select"
        value={showOther ? '__other__' : value}
        onChange={(e) => {
          const v = (e.target as HTMLSelectElement).value
          if (v === '__other__') {
            setOpenOther(true)
          } else {
            setOpenOther(false)
            onChange(v)
            onCommit(v)
          }
        }}
      >
        <option value="">{t('— unset —')}</option>
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
        <option value="__other__">{t('Other…')}</option>
      </select>
      {showOther && (
        <input
          class="stage-identity__other"
          type="text"
          placeholder={placeholder}
          value={value}
          onInput={(e) => onChange((e.target as HTMLInputElement).value)}
          onBlur={() => onCommit(value)}
        />
      )}
    </label>
  )
}
