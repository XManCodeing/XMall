import { api } from './client'
import { membershipBase } from './telegramMembership'

export interface MembershipRefundInput {
  request_key: string
  method: 'wallet' | 'manual' | 'existing'
  amount: string
  existing_refund_id: string
  item_ids: number[]
  remark: string
  manual_confirmed?: boolean
}
export interface MembershipRefundResult extends MembershipRefundInput {
  refund_id: string
  funds_state: 'completed'
  rights_state: 'pending' | 'queued'
}
export interface MembershipRefundPreview {
  observed_at: string
  groups: Array<{ item_id: number; target_id: number; chat_id: string; beneficiary_telegram_id: string; active: boolean; permanent: boolean; expires_at?: string; removal_state: string; cleanup_state: string }>
}
/** Combined server workflow persists money and intent together; retries retain the exact request key. */
export const membershipRefundAPI = {
  execute: (orderID: number, input: MembershipRefundInput) => api.post(`${membershipBase}/orders/${orderID}/refund-and-terminate`, input),
  preview: (orderID: number, method: string, items: number[]) => api.get(`${membershipBase}/orders/${orderID}/refund-and-terminate`, { params: { preview: '1', method, item_ids: items.join(',') } }),
  requests: (orderID: number) => api.get(`${membershipBase}/orders/${orderID}/refund-and-terminate`),
}
