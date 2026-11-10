import { describe, expect, it } from 'vitest'
import { supportSessionMessages } from './telegramSupportSessions'

/** All supported locales must expose the same independent session label contract. */
describe('support session translations', () => {
  it('keeps Chinese and English key sets aligned', () => {
    const keys = Object.keys(supportSessionMessages['zh-CN']).sort()
    for (const messages of Object.values(supportSessionMessages)) {
      expect(Object.keys(messages).sort()).toEqual(keys)
      expect(Object.values(messages).every(value => value.length > 0)).toBe(true)
    }
  })
})
