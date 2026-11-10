import { describe, expect, it } from 'vitest'
import { badgeVariants } from '@/components/ui/badge'
import { membershipStatusVariant } from './membershipStatus'

describe('membership semantic states', () => {
  it.each([['completed', 'success'], ['failed', 'destructive'], ['needs_review', 'warning'], ['processing', 'info'], ['inactive', 'secondary'], ['payment_pending', 'warning'], ['entitlement_active', 'success'], ['admission_restored', 'success'], ['new_unknown_state', 'secondary']])('maps %s without losing text or assuming success', (state, expected) => {
    expect(membershipStatusVariant(state)).toBe(expected)
    const classes = badgeVariants({ variant: membershipStatusVariant(state) })
    expect(classes).not.toMatch(/(?:red|amber|blue|emerald|slate)-\d/)
  })
})
