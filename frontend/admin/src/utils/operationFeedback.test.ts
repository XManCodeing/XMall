import { describe, expect, it } from 'vitest'
import i18n from '@/i18n'

const sources = import.meta.glob<string>([
  '../views/**/*.vue',
  '../components/**/*.vue',
  '../composables/*.ts',
  '!../composables/*.test.ts',
], { query: '?raw', import: 'default', eager: true })

describe('admin operation feedback contract', () => {
  it('does not reintroduce removed inline operation-result state', () => {
    const removed = /\b(?:batchActionSuccess|batchActionError|twofaSuccess|twofaError|oauthSuccess|oauthError|walletSuccess|refundSuccess|manualRefundSuccess|fulfillmentSuccess|paymentFeeUpdateSuccess|paymentFeeUpdateError|successMessage|resultMessage)\s*(?:=\s*ref|\.value)/
    for (const [path, source] of Object.entries(sources)) {
      expect(source.match(removed)?.[0], path).toBeUndefined()
    }
  })

  it('provides explicit notification fallbacks in all three locales', () => {
    for (const [path, source] of Object.entries(sources)) {
      const keys = [...source.matchAll(/notify(?:ErrorOnce|Error|Success|Info)\([^\n]*?\bt\('([^']+)'/g)].map(match => match[1]!)
      for (const key of keys) {
        for (const locale of ['zh-CN', 'zh-TW', 'en-US'] as const) {
          expect(i18n.global.te(key, locale), `${path}: ${locale} ${key}`).toBe(true)
        }
      }
    }
  })
})
