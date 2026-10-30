import { afterEach, describe, expect, it } from 'vitest'
import i18n from './index'

const locales = ['zh-CN', 'zh-TW', 'en-US'] as const
const keys = [
  'admin.settings.contact.telegramHint',
  'telegramBot.settings.supportUrlHint',
] as const

afterEach(() => {
  i18n.global.locale.value = 'zh-CN'
})

describe('support contact translations', () => {
  it.each(locales)('renders a literal @ without double escaping in %s', (locale) => {
    i18n.global.locale.value = locale
    for (const key of keys) {
      const translated = i18n.global.t(key)
      expect(translated).not.toBe(key)
      expect(translated).toContain('@')
      expect(translated).not.toContain("{'@'}")
    }
  })
})
