import { api } from './client'

/** Safe review DTO from payment/contract/manual_confirmation.go; never exposes gateway payloads. */
export interface ManualPaymentReview {
  payment_id: number
  order_id: number
  order_no: string
  channel_id: number
  provider_type: string
  payment_status: string
  order_status: string
  amount: string
  currency: string
  order_amount: string
  wallet_paid_amount: string
  paid_at: string | null
  created_at: string
  expires_at: string | null
  transaction_id: string
  exception_code: string
  can_confirm: boolean
  requires_timeout_confirmation: boolean
  blocked_reason: string
  confirmation: { request_id: string; admin_id: number; created_at: string; paid_at: string } | null
  replayed: boolean
}

/** External receipt evidence only; actor identity is injected by the backend. */
export interface ManualPaymentInput {
  request_id: string
  expected_status: string
  confirm_timeout?: boolean
  amount: string
  currency: string
  paid_at: string
  transaction_id: string
  evidence_reference: string
  reason: string
}

/** Resolve the exact finance endpoint. @param id Actual payment ID. @returns API path. */
export function manualPaymentPath(id: number) { return `/admin/payments/${id}/manual-confirmation` }

export const manualPaymentAPI = {
  /** Read persisted review state without replaying a mutation. @param id Payment ID. @returns Review response. */
  review: (id: number) => api.get(manualPaymentPath(id)),
  /** Record a human-verified receipt, not a charge. @param id Payment ID. @param input Evidence. @returns Review response. */
  confirm: (id: number, input: ManualPaymentInput) => api.post(manualPaymentPath(id), input),
}

/** Validate review identity and essential fields at the network boundary. @param value Untrusted DTO. @param paymentID Selected record. @param orderID Payment root. @returns Type guard. */
export function isManualPaymentReview(value: unknown, paymentID: number, orderID: number): value is ManualPaymentReview {
  if (!value || typeof value !== 'object') return false
  const row = value as ManualPaymentReview
  return row.payment_id === paymentID && row.order_id === orderID
    && typeof row.payment_status === 'string' && typeof row.order_status === 'string'
    && typeof row.amount === 'string' && typeof row.currency === 'string'
    && typeof row.can_confirm === 'boolean' && typeof row.blocked_reason === 'string'
    && typeof row.requires_timeout_confirmation === 'boolean'
}

/** Check bounded human evidence without converting money to floating point. @param input Draft. @returns Valid boundary input. */
export function validManualPaymentInput(input: Omit<ManualPaymentInput, 'request_id' | 'expected_status'>): boolean {
  const validText = (text: string, limit: number) => Array.from(text).length <= limit && !/\p{Cc}/u.test(text)
  return /^\d{1,18}(?:\.\d{1,2})?$/.test(input.amount) && /[1-9]/.test(input.amount)
    && /^[A-Z0-9]{3,12}$/.test(input.currency)
    && Number.isFinite(Date.parse(input.paid_at)) && Date.parse(input.paid_at) <= Date.now()
    && !!(input.transaction_id.trim() || input.evidence_reference.trim())
    && validText(input.transaction_id, 160) && validText(input.evidence_reference, 240)
    && !!input.reason.trim() && validText(input.reason, 1000)
}
