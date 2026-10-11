import { t } from '../../i18n'
import type { TalkootMember } from '../../platform/ctrlproto/types'

// Only the daemon can name the resolved provider. A worker's roster model is
// a request to that backend, not evidence of the model it actually runs.
export function memberModelSummary(member: TalkootMember): string {
  if (member.resolved_model) {
    const model = [member.resolved_provider, member.resolved_model].filter(Boolean).join(' / ')
    return member.model_source === 'session' ? t('%s (live session)', model) : model
  }
  if (member.model_problem) return t('Model unavailable: %s', member.model_problem)
  if (member.driver && member.driver !== 'native') {
    if (member.model) return t('%s: requested model %s', member.driver, member.model)
    if (member.tier) return t('%s: requested tier %s', member.driver, member.tier)
    return t('%s: backend default', member.driver)
  }
  if (member.model) return t('Model: %s', member.model)
  if (member.tier) return t('Tier: %s (model not reported)', member.tier)
  return t('Model not reported')
}
