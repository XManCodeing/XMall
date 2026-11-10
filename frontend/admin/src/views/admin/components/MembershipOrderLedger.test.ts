import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import MembershipOrderLedger from './MembershipOrderLedger.vue'
import { telegramMembershipMessages } from '@/i18n/telegramMembership'
import type { MembershipLedger } from '@/api/telegramMembership'

/** Render the read-only section with real locale messages, without a browser or remote API. */
async function render(ledger?: MembershipLedger, locale: keyof typeof telegramMembershipMessages = 'zh-CN'): Promise<string> {
  const app = createSSRApp(MembershipOrderLedger, { ledger })
  app.use(createI18n({ legacy: false, locale, messages: { [locale]: { telegramMembership: telegramMembershipMessages[locale] } } }))
  return renderToString(app)
}

const ledger: MembershipLedger = {
  item_id: 11, state: 'recorded',
  sources: [
    { target_id: 21, beneficiary_telegram_id: '9007199254740993', kind: 'permanent', months: 0, paid_at: '2026-01-01T00:00:00Z', status: 'active' },
    { target_id: 22, beneficiary_telegram_id: '9007199254740993', kind: 'months', months: 18, paid_at: '2026-02-01T00:00:00Z', status: 'revoked' },
  ],
  effective: [{ target_id: 21, beneficiary_telegram_id: '9007199254740993', permanent: true, pending_restore: true }],
}

describe('admin membership ledger SSR', () => {
  it('uses semantic badges for recording, source and restoration without changing labels', async () => {
    const root = document.createElement('div')
    root.innerHTML = await render(ledger)
    expect(root.querySelector('.text-info')?.textContent).toBe(telegramMembershipMessages['zh-CN'].ledger.recorded)
    expect(root.querySelector('.bg-success')?.textContent).toBe(telegramMembershipMessages['zh-CN'].ledger.active)
    expect(root.querySelector('.text-warning')?.textContent).toBe(telegramMembershipMessages['zh-CN'].ledger.pending_restore)
    expect(root.querySelector('.bg-secondary')?.textContent).toBe(telegramMembershipMessages['zh-CN'].ledger.revoked)
    expect(root.innerHTML).not.toMatch(/(?:emerald|amber|blue|slate)-\d/)
  })
  it('keeps both embedded tables compact with shared product table theme tokens', async () => {
    const root = document.createElement('div')
    root.innerHTML = await render(ledger)
    const tables = [...root.querySelectorAll('table')]
    expect(tables).toHaveLength(2)
    for (const table of tables) {
      expect(table.closest('.rounded-xl')?.className).toContain('border border-border bg-card overflow-x-auto')
      expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
      expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
      for (const row of table.querySelectorAll('tbody tr')) expect(row.className).toContain('hover:bg-muted/30')
      for (const cell of table.querySelectorAll('th, td')) expect(cell.className).toContain('px-4 py-3')
    }
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('renders sources, permanent without a date and pending restoration in %s', async locale => {
    const html = await render(ledger, locale)
    const messages = telegramMembershipMessages[locale]
    expect(html).toContain(messages.ledger.recorded)
    expect(html).toContain(messages.permanent)
    expect(html).toContain(messages.ledger.pending_restore)
    expect(html).toContain(messages.ledger.revoked)
    expect(html).toContain('9007199254740993')
    expect(html).not.toContain('Invalid Date')
    expect(html).not.toContain('telegramMembership.')
    expect(html).not.toContain('style=')
  })
  it('distinguishes empty, pending and failed without rendering raw diagnostics', async () => {
    expect(await render()).toContain('暂无入账来源')
    expect(await render({ item_id: 11, state: 'pending', sources: [], effective: [] })).toContain('待处理')
    expect(await render({ item_id: 11, state: 'failed', sources: [], effective: [] })).toContain('入账失败，待处理')
  })
  it('formats a finite expiration and does not expose a stale date for permanent entitlement', async () => {
    const html = await render({ ...ledger, effective: [{ ...ledger.effective[0]!, expires_at: '2099-12-31T00:00:00Z' }] })
    expect(html).not.toContain('2099')
    expect(await render({ ...ledger, effective: [{ ...ledger.effective[0]!, permanent: false, expires_at: '2099-12-31T00:00:00Z' }] })).toContain('2099')
  })
})
