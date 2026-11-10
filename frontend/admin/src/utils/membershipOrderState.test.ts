import { describe, expect, it } from 'vitest'
import { membershipOrderFilters, membershipOrderStateKey } from './membershipOrderState'
import { membershipOrderStateMessages } from '@/i18n/membershipOrderState'
import { membershipDispositionMessages } from '@/i18n/membershipDisposition'
import { membershipRecoveryMessages } from '@/i18n/membershipRecovery'

describe('membership evidence vocabulary', () => {
  it('exposes only supported aggregate filter enums, not raw operation states', () => {
    expect(membershipOrderFilters.rights).toEqual(['none', 'pending', 'active', 'expired', 'revoked', 'unconfirmed'])
    expect(membershipOrderFilters.member_removal).toEqual(['none', 'removed', 'present', 'failed', 'pending', 'not_required', 'mixed', 'unknown', 'unconfirmed'])
    expect(membershipOrderFilters.invite_cleanup).toEqual(['none', 'succeeded', 'failed', 'pending', 'not_required', 'mixed', 'unknown', 'unconfirmed'])
    expect(membershipOrderStateKey('member_removal', 'not_required')).toBe('membershipDisposition.memberNotRequired')
    expect(membershipDispositionMessages['zh-CN'].memberNotRequired).toBe('无需移出')
    expect(membershipOrderStateKey('invite_cleanup', 'not_required')).toBe('membershipOrderState.not_required')
  })
  it('labels successful invite cleanup as destroyed without changing generic completion', () => {
    expect(membershipOrderStateKey('invite_cleanup', 'succeeded')).toBe('membershipOrderState.inviteDestroyed')
    expect(membershipOrderStateMessages['zh-CN'].inviteDestroyed).toBe('已销毁')
    expect(membershipOrderStateMessages['zh-CN'].succeeded).toBe('已完成')
  })
  it('does not imply partial success when route outcomes merely differ', () => {
    expect(membershipOrderStateMessages['zh-CN'].mixed).toBe('结果不一致')
    expect(membershipOrderStateMessages['zh-TW'].mixed).toBe('結果不一致')
    expect(membershipOrderStateMessages['en-US'].mixed).toBe('Mixed outcomes')
  })
  it('distinguishes pending accrual, definite removal failure and insufficient evidence', () => {
    expect(membershipOrderStateKey('rights', 'pending')).toBe('membershipOrderState.rightsPending')
    expect(membershipOrderStateKey('member_removal', 'failed')).toBe('membershipOrderState.removalFailed')
    for (const dimension of Object.keys(membershipOrderFilters) as Array<keyof typeof membershipOrderFilters>) {
      expect(membershipOrderStateKey(dimension, 'none')).toBe('membershipOrderState.none')
      for (const value of [undefined, '', 'UNSUPPORTED']) expect(membershipOrderStateKey(dimension, value)).toBe('membershipOrderState.unconfirmed')
    }
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('supplies all evidence and task labels in %s', locale => {
    const messages = membershipOrderStateMessages[locale]
    for (const [dimension, values] of Object.entries(membershipOrderFilters)) {
      for (const value of values) {
        const [namespace, key] = membershipOrderStateKey(dimension as keyof typeof membershipOrderFilters, value).split('.')
        const labels = namespace === 'membershipDisposition' ? membershipDispositionMessages[locale] : messages
        expect((labels as Record<string, string>)[key!]).toBeTruthy()
      }
    }
    expect(Object.keys(messages).sort()).toEqual(Object.keys(membershipOrderStateMessages['zh-CN']).sort())
    expect(Object.keys(membershipRecoveryMessages[locale]).sort()).toEqual(Object.keys(membershipRecoveryMessages['zh-CN']).sort())
  })
})
