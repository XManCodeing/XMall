import { api } from './client'

export const recoveryBase = '/admin/telegram-bot/recovery'
export interface RecoveryTask {
  id: string
  version: string
  actor_id: string
  confirmed_actor_id: string
  kind: string
  state: string
  request_key: string
  preflight_ref: string
  reason: string
  verification_note: string
  expires_at: string
  created_at: string
  updated_at: string
}
export interface RecoveryEndpoint {
  target_id: string
  bot_id: string
  chat_id: string
  account_id: string
  telegram_id: string
}
export interface RecoveryVersions {
  config: string
  route: string
  generation: string
  binding: string
  entitlement: string
}
export interface RecoveryBotRoute { target_id: string; chat_id: string; new_chat_id?: string; version: string; route_version: string; purpose: string; enabled: boolean }
export interface BotRouteDestination { target_id: string; chat_id: string }
export interface RecoverySnapshot {
  bot_routes?: RecoveryBotRoute[]
  old: RecoveryEndpoint
  new: RecoveryEndpoint
  expected_old: RecoveryVersions
  expected_new: RecoveryVersions
  scope: { target_ids: string[]; chat_ids: string[]; bot_ids: string[]; account_ids: string[]; telegram_ids: string[] }
}
export interface RecoveryDetail { task: RecoveryTask; snapshot: RecoverySnapshot; execution_available?: boolean; preparation_available?: boolean; blocker?: string }
export interface BotPreflightInput {
  routes?: BotRouteDestination[]
  source_bot_id: string
  destination_bot_id: string
  request_key: string
  reason: string
}
export interface RecoveryStep {
  id: string
  task_id: string
  business_key: string
  operation: string
  state: string
  version: string
  result_code: string
  created_at: string
  updated_at: string
}
export interface RecoveryPage<T> { items: T[]; next_after_id: string }
export interface RecoveryNumberedPage<T> { items: T[]; page: number; page_size: number; total: number; total_page: number }
export interface GroupPreflightInput {
  target_id: number
  expected_version: number
  expected_route_version: number
  new_bot_instance_id: number
  new_telegram_chat_id: string
  request_key: string
  reason: string
}

/** Validate canonical negative int64 without converting Telegram IDs to Number.
 * @param value Raw chat identifier. @returns Whether the identifier is a nonzero negative int64.
 */
export function validRecoveryChatID(value: string): boolean {
  return /^-[1-9]\d{0,18}$/.test(value) && BigInt(value) >= -9223372036854775808n
}

/** Validate canonical positive uint64 IDs without a Number conversion.
 * @param value Raw identifier. @returns Whether the ID is a nonzero uint64.
 */
export function validRecoveryBotID(value: string): boolean {
  return /^[1-9]\d{0,19}$/.test(value) && BigInt(value) <= 18446744073709551615n
}

export const telegramRecoveryAPI = {
  /** Persist a cross-Bot preview only. @param input String IDs and reviewed intent. @returns Awaiting-confirmation task. */
  botPreflight: (input: BotPreflightInput) => api.post(`${recoveryBase}/bot-preflight`, input),
  /** Read persisted tasks only. @param after Cursor string. @returns API response with one bounded page. */
  list: (after = '0') => api.get(`${recoveryBase}/tasks`, { params: { after_id: after, limit: 20 } }),
  /** Read a numbered task page directly. @param page Page number. @param pageSize Bounded size. @returns Counted page response. */
  listPage: (page = 1, pageSize = 20) => api.get(`${recoveryBase}/tasks`, { params: { page, page_size: pageSize } }),
  /** Read a numbered checkpoint page directly. @param id Task ID. @param page Page number. @param pageSize Bounded size. @returns Counted page response. */
  stepsPage: (id: string, page = 1, pageSize = 20) => api.get(`${recoveryBase}/tasks/${id}/steps`, { params: { page, page_size: pageSize } }),
  /** Read safe immutable snapshot. @param id Decimal task ID. @returns Task and snapshot response. */
  get: (id: string) => api.get(`${recoveryBase}/tasks/${id}`),
  /** Read persisted checkpoints only. @param id Task ID. @param after Cursor. @returns Bounded step page. */
  steps: (id: string, after = '0') => api.get(`${recoveryBase}/tasks/${id}/steps`, { params: { after_id: after, limit: 20 } }),
  /** Persist a read-only same-Bot preview. @param input Versioned intent. @returns Awaiting-confirmation task. */
  preflight: (input: GroupPreflightInput) => api.post(`${recoveryBase}/group-preflight`, input),
  /** Cancel unconfirmed preview using exact CAS version. @param id Task ID. @param version String version. @returns Updated task response. */
  cancel: (id: string, version: string) => api.post(`${recoveryBase}/tasks/${id}/cancel`, { expected_version: version }),
  /** Confirm the reviewed preflight. @param id Task ID. @param reference Preflight reference. @param version Exact string version. @returns Updated task response. */
  confirm: (id: string, reference: string, version: string) => api.post(`${recoveryBase}/tasks/${id}/confirm`, { preflight_ref: reference, expected_version: version }),
  /** Request one safe recovery pass. @param id Task ID. @param version Exact string version. @returns Updated task response. */
  resume: (id: string, version: string) => api.post(`${recoveryBase}/tasks/${id}/resume`, { expected_version: version }),
}
