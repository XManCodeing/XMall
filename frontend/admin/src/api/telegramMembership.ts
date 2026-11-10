import { api } from './client'
import type { AdminProduct, AdminOrder, AdminOrderItem } from './types'

export type MembershipTermKind = 'months' | 'permanent'
export interface MembershipTerm { sku_id?: number; sku_code: string; kind: MembershipTermKind; months: number }
export interface MembershipConfig {
  product_id: number
  version: number
  enabled: boolean
  quantity: 1
  target_ids: number[]
  /** 后台列表展示名称，缺失目标仍保留原 ID 作为定位依据。 */
  target_names?: Record<string, string>
  terms: MembershipTerm[]
}
export interface MembershipProduct extends AdminProduct {
  membership: MembershipConfig
  /** 服务端按绑定会员群解析，旧版本服务端缺失时不允许复制。 */
  share_url?: string
  share_unavailable_reason?: string
}
export interface MembershipInput {
  expected_version: number
  enabled: boolean
  quantity: 1
  target_ids: number[]
  terms: MembershipTerm[]
}
export interface MembershipSnapshot {
  order_item_id: number
  beneficiary_telegram_id: string
  account_id: number
  identity_version: number
  config_version: number
  kind: MembershipTermKind
  months: number
  quantity: number
  targets: Array<{ target_id: number; route_version: number; bot_instance_id: number; telegram_chat_id: string }>
}
export interface MembershipLedger {
  item_id: number
  state: 'pending' | 'failed' | 'recorded'
  sources: Array<{ target_id: number; beneficiary_telegram_id: string; kind: MembershipTermKind; months: number; paid_at: string; status: string }>
  effective: Array<{ target_id: number; beneficiary_telegram_id: string; permanent: boolean; expires_at?: string; pending_restore: boolean }>
}
export type MembershipRightsState = 'none' | 'pending' | 'active' | 'expired' | 'revoked' | 'unconfirmed'
export type MembershipMemberRemovalState = 'none' | 'removed' | 'present' | 'failed' | 'pending' | 'not_required' | 'mixed' | 'unknown' | 'unconfirmed'
export type MembershipInviteCleanupState = 'none' | 'succeeded' | 'failed' | 'pending' | 'not_required' | 'mixed' | 'unknown' | 'unconfirmed'
export interface MembershipOrderQuery extends Record<string, unknown> {
  rights?: MembershipRightsState
  member_removal?: MembershipMemberRemovalState
  invite_cleanup?: MembershipInviteCleanupState
}
export interface MembershipAccessReviewQuery extends Record<string, unknown> { keyword?: string; status?: 'pending' | 'reviewed' | 'all' }
export interface MembershipAccessQuery extends Record<string, unknown> { order_no?: string }
export interface MembershipAccessTaskAssociation { order_no?: string; chat_title?: string; buyer_telegram_id?: string }
export interface MembershipOrderGroupState {
  item_id: number
  target_id: number
  beneficiary_telegram_id: string
  rights: string
  permanent: boolean
  expires_at?: string
  member_removal: string
  member_removed: boolean
  invite_cleanup: string
}
export interface MembershipOrder extends AdminOrder {
  children?: MembershipOrder[]
  membership_groups?: MembershipOrderGroupState[]
  items: Array<AdminOrderItem & { membership_snapshot?: MembershipSnapshot; membership_ledger?: MembershipLedger }>
}
export type MembershipLifecycleStatus = '' | 'processing' | 'needs_review' | 'completed'
export interface MembershipLifecycleQuery extends Record<string, unknown> { status?: MembershipLifecycleStatus }
export interface MembershipLifecycleTask {
  id: string
  target_id: string
  chat_id: string
  telegram_id: string
  version: string
  /** 服务端持久化的处置周期与隔离版本；缺失时不得跨任务继承结果。 */
  cleanup_cycle_id?: string
  entitlement_version?: string
  route_version?: string
  bot_id?: string
  state: string
  phase: string
  review_reason: string
  ban_outcome: string
  unban_outcome: string
  ban_blocked: boolean
  ban_confirmed?: boolean
  member_status?: string
  member_is_member?: boolean
  observed_at?: string
  attempt?: number
  last_attempt_at?: string | null
  next_attempt_at?: string
  updated_at?: string
  member_outcome?: string
  invite_cleanup?: string
  invite_cleanup_total?: number
  invite_cleanup_completed?: number
  invite_cleanup_failed?: number
  invite_cleanup_unknown?: number
}
export interface MembershipLifecycleMember { telegram_id: string; telegram_username?: string; tasks: MembershipLifecycleTask[] }
export interface MembershipLifecycleMemberQuery extends MembershipLifecycleQuery { keyword?: string }
export type MembershipProductInput = Record<string, unknown> & { membership: MembershipInput }
export const membershipBase = '/admin/telegram-bot/membership'
/** All product fields and membership configuration are committed in one request/transaction. */
export const telegramMembershipAPI = {
  products: (params: Record<string, unknown>) => api.get(`${membershipBase}/products`, { params }),
  /** 为历史商品幂等生成短码，已存在时返回原分享链接。 */
  share: (id: number) => api.post(`${membershipBase}/products/${id}/share`, {}),
  product: (id: number) => api.get(`${membershipBase}/products/${id}`),
  create: (input: MembershipProductInput) => api.post(`${membershipBase}/products`, input, { timeoutMs: 30000 }),
  update: (id: number, input: MembershipProductInput) => api.put(`${membershipBase}/products/${id}`, input, { timeoutMs: 30000 }),
  /** Set listing state using the last read configuration version and server-side capability checks. */
  setActive: (id: number, isActive: boolean, expectedVersion: number) => api.patch(`${membershipBase}/products/${id}`, { is_active: isActive, expected_version: expectedVersion }),
  /** Delete a membership product only when its configuration version still matches. */
  remove: (id: number, expectedVersion: number) => api.delete(`${membershipBase}/products/${id}`, { params: { expected_version: expectedVersion } }),
  refunds: (order: number) => api.get(`${membershipBase}/refunds`, { params: { order_id: order } }),
  disposeRefund: (input: Record<string, unknown>) => api.post(`${membershipBase}/refunds/dispose`, input),
  /** Read bot-scoped tasks, or cross-bot exception totals with summary: 1 under the same permission. */
  lifecycleTasks: (params: MembershipLifecycleQuery) => api.get(`${membershipBase}/lifecycle/tasks`, { params }),
  /** Read member-numbered pages; status matches members and preserves all their group/history tasks.
   * @param params Existing bot, status and numbered page scope. @returns Grouped member page; total counts members.
   */
  lifecycleMembers: (params: MembershipLifecycleMemberQuery) => api.get(`${membershipBase}/lifecycle/tasks`, { params: { ...params, group_by: 'telegram_id' } }),
  recoverLifecycle: (bot: string, id: string, input: Record<string, unknown>) => api.post(`${membershipBase}/lifecycle/tasks/${id}/recover`, input, { params: { bot_id: bot } }),
  /** Read bot-scoped admission tasks; summary: 1 returns only a cross-bot attention_count. */
  accessTasks: (params: MembershipAccessQuery) => api.get(`${membershipBase}/access/tasks`, { params }),
  /** Read bot-scoped applications; summary: 1 counts only applications still needing review. */
  accessReviews: (params: MembershipAccessReviewQuery) => api.get(`${membershipBase}/access/reviews`, { params }),
  recoverAccess: (bot: string, id: string, input: Record<string, unknown>) => api.post(`${membershipBase}/access/tasks/${id}/recover`, input, { params: { bot_id: bot } }),
  orders: (params: MembershipOrderQuery) => api.get(`${membershipBase}/orders`, { params }),
  order: (id: number) => api.get(`${membershipBase}/orders/${id}`),
}
