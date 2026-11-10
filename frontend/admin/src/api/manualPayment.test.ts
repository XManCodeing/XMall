import { describe, expect, it, vi } from 'vitest'
import { manualPaymentAPI, isManualPaymentReview, validManualPaymentInput } from './manualPayment'
import { manualPaymentMessages } from '@/i18n/manualPayment'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
const evidence = { amount: '10.12', currency: 'USDT', paid_at: '2026-01-01T01:00:00Z', transaction_id: 'trade-reference', evidence_reference: '', reason: 'Verified actual receipt' }
describe('manual payment contract', () => {
  it('uses independent payment GET/POST endpoints without actor or order-state writes', () => {
    const input = { ...evidence, request_id: 'request-1', expected_status: 'pending' }
    manualPaymentAPI.review(91); manualPaymentAPI.confirm(91, input)
    expect(mocks.get).toHaveBeenCalledWith('/admin/payments/91/manual-confirmation')
    expect(mocks.post).toHaveBeenCalledWith('/admin/payments/91/manual-confirmation', input)
  })
  it('keeps decimal strings and requires receipt proof, reason and a nonfuture timestamp', () => {
    expect(validManualPaymentInput(evidence)).toBe(true)
    for (const amount of ['0', '-1', '1e2', '1,000', 'NaN']) expect(validManualPaymentInput({ ...evidence, amount })).toBe(false)
    expect(validManualPaymentInput({ ...evidence, transaction_id: '' })).toBe(false)
    expect(validManualPaymentInput({ ...evidence, transaction_id: '', evidence_reference: 'ledger/statement/12' })).toBe(true)
    expect(validManualPaymentInput({ ...evidence, reason: ' ' })).toBe(false)
    expect(validManualPaymentInput({ ...evidence, paid_at: '2099-01-01T00:00:00Z' })).toBe(false)
    expect(validManualPaymentInput({ ...evidence, reason: '证'.repeat(1001) })).toBe(false)
    expect(validManualPaymentInput({ ...evidence, amount: '10.12345678' })).toBe(false)
    expect(validManualPaymentInput({ ...evidence, transaction_id: 'line\nfeed' })).toBe(false)
  })
  it('fails closed for missing or cross-record response data', () => {
    expect(isManualPaymentReview(null, 91, 7)).toBe(false)
    expect(isManualPaymentReview({ payment_id: 91, order_id: 8 }, 91, 7)).toBe(false)
  })
  it('rejects missing timeout review flags instead of silently treating expired receipts as normal', () => {
    const review = { payment_id: 91, order_id: 7, payment_status: 'pending', order_status: 'pending_payment', amount: '10.12', currency: 'USDT', can_confirm: true, blocked_reason: '' }
    expect(isManualPaymentReview(review, 91, 7)).toBe(false)
    expect(isManualPaymentReview({ ...review, requires_timeout_confirmation: 'true' }, 91, 7)).toBe(false)
    expect(isManualPaymentReview({ ...review, requires_timeout_confirmation: true }, 91, 7)).toBe(true)
    expect(isManualPaymentReview({ ...review, requires_timeout_confirmation: false }, 91, 7)).toBe(true)
  })
  it('provides identical message keys in all three locales', () => {
    expect(Object.keys(manualPaymentMessages['zh-TW']).sort()).toEqual(Object.keys(manualPaymentMessages['zh-CN']).sort())
    expect(Object.keys(manualPaymentMessages['en-US']).sort()).toEqual(Object.keys(manualPaymentMessages['zh-CN']).sort())
  })
})
