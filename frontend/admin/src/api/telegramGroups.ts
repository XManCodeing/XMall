import { api, type ApiResponse } from './client'

export type GroupPurpose = 'support' | 'membership'
export interface GroupInput {
  name: string
  purpose: GroupPurpose
  bot_instance_id: number
  telegram_chat_id: string
  enabled: boolean
  expected_version: number
  expected_route_version: number
}
export interface GroupObservation {
  route_version: number
  sequence: number
  bot_telegram_id: string
  generation: number
  chat_type: string
  title: string
  is_member: boolean
  is_forum: boolean
  member_status: string
  can_manage_topics: boolean
  can_invite_users: boolean
  can_restrict_members: boolean
  state: string
  error_class: string
  source: string
  checked_at: string | null
  observed_at: string | null
  last_success_at: string | null
  last_failure_at: string | null
  retry_after_seconds: number
}
export interface TelegramGroup {
  id: number
  name: string
  purpose: GroupPurpose
  bot_instance_id: number
  telegram_chat_id: string
  recovery_task_id?: number
  enabled: boolean
  version: number
  route_version: number
  observation_seq: number
  observation: GroupObservation
  fresh: boolean
  fresh_until: string | null
  missing_capabilities: string[]
  affected_functions: string[]
  checking: boolean
  next_check_at: string
  created_at: string
  updated_at: string
}
export interface GroupDetail extends TelegramGroup {
  enable_rejected?: boolean
  routes: Array<{ id: number; target_id: number; route_version: number; bot_instance_id: number; telegram_chat_id: string; effective_at: string; retired_at: string | null }>
  operations: Array<{ id: number; target_id: number; route_version: number; sequence: number; actor_admin_id: number; source: string; state: string; error_class: string; started_at: string; finished_at: string | null }>
}
export interface GroupModule {
  configured_enabled: boolean
  effective_enabled: boolean
  bot_instance_id: number
  partition: GroupPurpose
  version: number
  enabled: boolean
  target_id: number
  updated_by: number
  updated_at: string
}
export interface ModuleInput {
  partition: GroupPurpose
  expected_version: number
  enabled: boolean
  target_id: number
}
/** Groups handler embeds both rows and offset pagination inside data, not top-level pagination. */
export interface TelegramGroupPage {
  items: TelegramGroup[]
  total: number
  page: number
  page_size: number
}
const base = '/admin/telegram-bot'
/** Omit the editable legacy name from creation requests.
 * @param input Editor draft. @returns Creation payload whose name is resolved by Telegram.
 */
function groupCreationInput(input: GroupInput): Omit<GroupInput, 'name'> {
  const { name: _name, ...creation } = input
  return creation
}
export const telegramGroupsAPI = {
  /** Read a handler-shaped group page. @param params Offset and optional route filters. @returns Nested page envelope. */
  list: (params: { page: number; page_size: number; bot_instance_id?: number | string; purpose?: string }): Promise<{ data: Omit<ApiResponse<TelegramGroupPage>, 'pagination'> }> => api.get(`${base}/groups`, { params }),
  get: (id: number) => api.get(`${base}/groups/${id}`),
  create: (input: GroupInput) => api.post(`${base}/groups`, groupCreationInput(input), { timeoutMs: 30000 }),
  patch: (id: number, input: GroupInput) => api.patch(`${base}/groups/${id}`, input, { timeoutMs: 30000 }),
  /** Probe a draft in its existing write scope without persisting it. @returns Current group capabilities. */
  previewEnable: (input: GroupInput, id?: number) => api.post(`${base}/groups${id ? `/${id}` : ''}/enable-preview`, id ? input : groupCreationInput(input), { timeoutMs: 30000 }),
  check: (id: number, expected_route_version: number) => api.post(`${base}/groups/${id}/check`, { expected_route_version }, { timeoutMs: 15000 }),
  modules: (id: number) => api.get(`${base}/instances/${id}/modules`),
  saveModule: (id: number, input: ModuleInput) => api.patch(`${base}/instances/${id}/modules`, input),
}
