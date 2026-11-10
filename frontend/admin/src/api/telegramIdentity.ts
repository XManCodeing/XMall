import { api } from './client'
import { recoveryBase, type RecoveryDetail, type RecoveryTask } from './telegramRecovery'

export interface IdentityTarget {
  target_id: string; bot_id: string; chat_id: string
  entitlement_version: string; group_version: string; route_version: string
}
export interface IdentityMapping {
  id: string; target_id: string; state: string
  old_telegram_id: string; new_telegram_id: string
}
export interface IdentityDetail extends RecoveryDetail {
  identity_mappings?: IdentityMapping[]
  snapshot: RecoveryDetail['snapshot'] & { identity_targets?: IdentityTarget[] }
}
export interface IdentityPreflightInput {
  account_id: string; old_telegram_id: string; new_telegram_id: string; target_ids: string[]
  request_key: string; reason: string; verification_note: string; human_verified: boolean
}
/** Validate canonical positive decimal IDs without numeric coercion.
 * @param value Untrusted input. @param maximum Inclusive decimal bound. @returns Valid identifier.
 */
export function validIdentityID(value: unknown, maximum = 9223372036854775807n): value is string {
  return typeof value === 'string' && /^[1-9]\d{0,19}$/.test(value) && BigInt(value) <= maximum
}
/** Check critical persisted snapshot fields before enabling any identity mutation.
 * @param value Untrusted response. @returns Valid identity detail, retaining all IDs as strings.
 */
export function isIdentityDetail(value: unknown): value is IdentityDetail {
  const detail = value as IdentityDetail | undefined
  const task = detail?.task, snapshot = detail?.snapshot, targets = snapshot?.identity_targets
  return !!task && task.kind === 'identity_transfer' && validIdentityID(task.id) && validIdentityID(task.version)
    && typeof task.preflight_ref === 'string' && !!task.preflight_ref && typeof task.state === 'string'
    && (task.confirmed_actor_id === '0' || validIdentityID(task.confirmed_actor_id)) && typeof task.expires_at === 'string'
    && !!snapshot && validIdentityID(snapshot.old?.account_id) && snapshot.old.account_id === snapshot.new?.account_id
    && validIdentityID(snapshot.old.telegram_id) && validIdentityID(snapshot.new.telegram_id)
    && snapshot.old.telegram_id !== snapshot.new.telegram_id && validIdentityID(snapshot.expected_old?.binding)
    && Array.isArray(targets) && targets.length > 0 && targets.length <= 100
    && targets.every(target => [target.target_id, target.bot_id, target.entitlement_version, target.group_version, target.route_version].every(value => validIdentityID(value)))
    && (detail.identity_mappings === undefined || (Array.isArray(detail.identity_mappings) && detail.identity_mappings.every(mapping =>
      validIdentityID(mapping.id) && validIdentityID(mapping.target_id) && typeof mapping.state === 'string')))
}
export const telegramIdentityAPI = {
  /** Persist a human-reviewed preflight only. @param input String-safe draft. @returns Persisted task. */
  preflight: (input: IdentityPreflightInput) => api.post(`${recoveryBase}/identity-preflight`, input),
  /** Read authoritative identity task progress. @param id Task ID. @returns Safe detail response. */
  get: (id: string) => api.get(`${recoveryBase}/tasks/${id}`),
  /** Resume one reviewed database-only migration pass. @param task Current persisted task. @returns Updated task. */
  resume: (task: RecoveryTask) => api.post(`${recoveryBase}/tasks/${task.id}/identity-resume`, { expected_version: task.version }),
  /** Confirm exact immutable preview once. @param task Reviewed task. @returns Updated task. */
  confirm: (task: RecoveryTask) => api.post(`${recoveryBase}/tasks/${task.id}/identity-confirm`, {
    expected_version: task.version, preflight_ref: task.preflight_ref,
  }),
}
