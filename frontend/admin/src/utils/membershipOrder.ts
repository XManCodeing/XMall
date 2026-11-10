import type { MembershipOrder } from '@/api/telegramMembership'

/**
 * Show the new monetary action only with paid, unrefunded and fully active purchase evidence.
 * @param order Server projection; absent/unknown rights fail closed, never inferred from trading status.
 * @returns Whether a new refund command may be offered (the server still authorizes submission).
 */
export function canOfferMembershipRefund(order: MembershipOrder): boolean {
  const groups = order.membership_groups || []
  return Boolean(order.paid_at) && Number(order.refunded_amount || 0) === 0
    && !['refunded', 'partially_refunded', 'canceled'].includes(order.status)
    && groups.length > 0 && groups.every(group => group.rights === 'active'
      && !group.member_removed && !['removed', 'mixed'].includes(group.member_removal)
      && (group.permanent || Boolean(group.expires_at && Date.parse(group.expires_at) > Date.now())))
}
