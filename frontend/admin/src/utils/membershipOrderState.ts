import type { MembershipRightsState, MembershipMemberRemovalState, MembershipInviteCleanupState } from '@/api/telegramMembership'

/** Filter values match the server's aggregate evidence, not raw operation states. */
export const membershipOrderFilters = {
  rights: ['none', 'pending', 'active', 'expired', 'revoked', 'unconfirmed'] satisfies MembershipRightsState[],
  member_removal: ['none', 'removed', 'present', 'failed', 'pending', 'not_required', 'mixed', 'unknown', 'unconfirmed'] satisfies MembershipMemberRemovalState[],
  invite_cleanup: ['none', 'succeeded', 'failed', 'pending', 'not_required', 'mixed', 'unknown', 'unconfirmed'] satisfies MembershipInviteCleanupState[],
}
export type MembershipOrderDimension = keyof typeof membershipOrderFilters
export const membershipDimensionLabels = { rights: 'rights', member_removal: 'memberRemoval', invite_cleanup: 'inviteCleanup' } as const

/** Resolve dimension-specific wording without turning missing evidence into “none”.
 * @param dimension Backend evidence dimension. @param state Server-projected value.
 * @returns Translation key; unknown future states remain explicitly unconfirmed.
 */
export function membershipOrderStateKey(dimension: MembershipOrderDimension, state?: string): string {
  if (dimension === 'invite_cleanup' && state === 'succeeded') return 'membershipOrderState.inviteDestroyed'
  if (dimension === 'rights' && state === 'pending') return 'membershipOrderState.rightsPending'
  if (dimension === 'member_removal' && state === 'not_required') return 'membershipDisposition.memberNotRequired'
  if (dimension === 'member_removal' && state === 'failed') return 'membershipOrderState.removalFailed'
  const known: readonly string[] = membershipOrderFilters[dimension]
  return `membershipOrderState.${state && known.includes(state) ? state : 'unconfirmed'}`
}
