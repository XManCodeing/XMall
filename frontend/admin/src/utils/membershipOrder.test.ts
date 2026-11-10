import { describe, expect, it } from 'vitest'
import type { MembershipOrder } from '@/api/telegramMembership'
import { canOfferMembershipRefund } from './membershipOrder'

/** Build a paid delivered order with server-confirmed permanent rights. @returns Isolated fixture. */
function order(): MembershipOrder {
  return { status: 'delivered', paid_at: '2026-01-01', refunded_amount: 0, membership_groups: [{ rights: 'active', permanent: true, member_removal: 'unconfirmed', member_removed: false }] } as unknown as MembershipOrder
}
describe('membership monetary action eligibility', () => {
  it('accepts actual paid delivered/completed orders, not only status paid', () => {
    for (const status of ['paid', 'delivered', 'completed', 'fulfilling']) { const row = order(); row.status = status; expect(canOfferMembershipRefund(row)).toBe(true) }
  })
  it('rejects unpaid/refunded/expired/revoked/removed/unknown and mixed target evidence', () => {
    const unpaid = order(); unpaid.paid_at = undefined; expect(canOfferMembershipRefund(unpaid)).toBe(false)
    const refunded = order(); refunded.refunded_amount = 1; expect(canOfferMembershipRefund(refunded)).toBe(false)
    for (const rights of ['expired', 'revoked', 'unconfirmed']) { const row = order(); row.membership_groups![0]!.rights = rights; expect(canOfferMembershipRefund(row)).toBe(false) }
    const removed = order(); removed.membership_groups![0]!.member_removed = true; expect(canOfferMembershipRefund(removed)).toBe(false)
    const mixed = order(); mixed.membership_groups!.push({ ...mixed.membership_groups![0]!, rights: 'expired' }); expect(canOfferMembershipRefund(mixed)).toBe(false)
    const expired = order(); expired.membership_groups![0]!.permanent = false; expired.membership_groups![0]!.expires_at = '2000-01-01'; expect(canOfferMembershipRefund(expired)).toBe(false)
    const absent = order(); absent.membership_groups = []; expect(canOfferMembershipRefund(absent)).toBe(false)
  })
})
