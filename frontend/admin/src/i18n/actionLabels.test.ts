import { describe, expect, it } from 'vitest'
import i18n from './index'

const sources = import.meta.glob('../**/*.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const locales = ['zh-CN', 'zh-TW', 'en-US'] as const

/** Collect literal translation keys displayed by table buttons, excluding tooltips and confirmations.
 * @returns Unique keys from visible button content and standalone row-action components.
 */
function rowActionKeys(): string[] {
  const keys = new Set<string>()
  for (const [path, source] of Object.entries(sources)) {
    // Editable field grids are forms, not list operation columns.
    if (['HttpIntegrationEditor.vue', 'MembershipProductEditModal.vue', 'ManualPaymentConfirmation.vue'].some(name => path.endsWith(name))) continue
    const cells = path.endsWith('ManualPaymentAction.vue') || path.endsWith('MembershipRefundAction.vue')
      ? [source.slice(source.indexOf('<template>'), source.indexOf('<Dialog', source.indexOf('<template>')))]
      : [...source.matchAll(/<(?:TableCell|td)\b[^>]*>([\s\S]*?)<\/(?:TableCell|td)>/g)].map(match => match[1] || '')
    for (const cell of cells) {
      for (const button of cell.matchAll(/<(?:Button|button|RouterLink|router-link)\b[^>]*>([\s\S]*?)<\/(?:Button|button|RouterLink|router-link)>/g)) {
        for (const expression of (button[1] || '').matchAll(/\{\{([\s\S]*?)\}\}/g)) {
          for (const key of (expression[1] || '').matchAll(/['"]([\w]+\.[\w.]+)['"]/g)) keys.add(key[1]!)
        }
      }
    }
  }
  return [...keys].sort()
}

describe('admin table action labels', () => {
  it('keeps short-action keys identical across all three locales, including dynamic recovery actions', () => {
    const expected = Object.keys(i18n.global.getLocaleMessage('zh-CN').tableAction as object).sort()
    for (const locale of locales) {
      const actions = i18n.global.getLocaleMessage(locale).tableAction as Record<string, string>
      expect(Object.keys(actions).sort()).toEqual(expected)
      for (const label of Object.values(actions)) {
        expect(label.trim().length).toBeGreaterThan(0)
        if (locale !== 'en-US') expect([...label].length).toBeLessThanOrEqual(2)
      }
    }
    for (const action of ['reconcile', 'refresh_member', 'retry', 'rebuild', 'review', 'review_join']) expect(expected).toContain(action)
    expect(rowActionKeys().length).toBeGreaterThan(60)
  })
  it.each(locales)('keeps visible actions translated and compact in %s', locale => {
    const invalid: string[] = []
    for (const key of rowActionKeys()) {
      expect(i18n.global.te(key, locale), `${locale}: ${key}`).toBe(true)
      const label = i18n.global.t(key, {}, { locale })
      // Explicitly named support settings distinguish this action from editing the group.
      const maxLength = key === 'telegramSupportSettings.settingsEntry' ? 4 : 2
      if (locale !== 'en-US' && [...label].length > maxLength) invalid.push(`${key}: ${label}`)
    }
    expect(invalid).toEqual([])
  })
})
