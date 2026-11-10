import type { BadgeVariants } from '@/components/ui/badge'

/** Membership/support states share semantic tokens, not fixed palette classes.
 * @param state Server state or explicitly derived boolean state.
 * @returns Existing Badge variant; unknown states remain neutral.
 */
export function membershipStatusVariant(state?: string): BadgeVariants['variant'] {
  if (['active', 'ready', 'succeeded', 'success', 'completed', 'verified', 'delivered', 'paid', 'replied', 'not_blocked', 'removed', 'already_left', 'retained', 'entitlement_active', 'admission_restored', 'open', 'member', 'administrator', 'creator'].includes(state || '')) return 'success'
  if (['failed', 'partial_failed', 'blocked', 'authentication_error', 'inaccessible', 'missing_permissions', 'kicked', 'restricted'].includes(state || '')) return 'destructive'
  if (['pending', 'pending_restore', 'awaiting_confirmation', 'buffered', 'queued', 'waiting_verification', 'needs_review', 'retryable', 'unknown', 'unconfirmed', 'partial', 'unreplied', 'rate_limited', 'cooldown', 'awaiting_member', 'cleanup_pending', 'pending_payment', 'payment_pending', 'partially_delivered', 'partially_refunded'].includes(state || '')) return 'warning'
  if (['running', 'recorded', 'fencing', 'switching', 'recovering', 'claimed', 'collecting', 'challenged', 'processing', 'creating', 'sending', 'checking', 'confirmed', 'previewed', 'refunded', 'fulfilling'].includes(state || '')) return 'info'
  return 'secondary'
}
