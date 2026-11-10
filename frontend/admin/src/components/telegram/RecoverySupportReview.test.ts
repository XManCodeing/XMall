import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Component from './RecoverySupportReview.vue'
import i18n from '@/i18n'
import { recoverySupportMessages } from '@/i18n/recoverySupport'
import type { RecoveryTask } from '@/api/telegramRecovery'
const mocks = vi.hoisted(() => ({ entries: vi.fn(), operations: vi.fn(), resolve: vi.fn(), auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/api/telegramRecoverySupport', async original => ({ ...await original<typeof import('@/api/telegramRecoverySupport')>(), telegramRecoverySupportAPI: mocks }))
let app: App, root: HTMLDivElement
/** Flush rendered component and pending readers. @returns Completion promise. */
async function flush() { for (let index = 0; index < 8; index++) { await Promise.resolve(); await nextTick() } }
/** Mount actual themed components without any network side effects. @param locale Locale key. @returns Unwrapped setup state. */
function mount(locale: keyof typeof recoverySupportMessages) {
  mocks.auth = reactive({ isSuper: true, token: 's', permissions: [] })
  mocks.entries.mockResolvedValue({ data: { data: { task_version: '8', next_after_id: '0', items: [{ id: '12', target_id: '5', revision: '4', state: 'unknown', legacy: '1', operation_id: '14' }] } } })
  mocks.operations.mockResolvedValue({ data: { data: { task_version: '8', next_after_id: '0', items: [{ id: '14', version: '5', state: 'unknown', retired: false, target_id: '5', route_version: '3', chat_id: '-1005', action: 'create' }] } } })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(Component, { task: { id: '9', version: '8', state: 'needs_review', kind: 'group_replace' } as RecoveryTask })
  app.use(createI18n({ legacy: false, locale, messages: { [locale]: { ...i18n.global.getLocaleMessage(locale), ...recoverySupportMessages[locale] } } }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.resetAllMocks() })

describe('support review themed UI', () => {
  it('uses product table styling and keeps row actions operational', async () => {
    const state = mount('zh-CN')
    await state.loadEntries(); await flush()
    root.querySelector<HTMLButtonElement>('tbody button')!.click(); await flush()
    const tables = root.querySelectorAll('table')
    expect(tables).toHaveLength(2)
    for (const table of tables) {
      expect(table.closest('.rounded-xl')?.className).toBe('rounded-xl border border-border bg-card overflow-x-auto')
      expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
      expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
      expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
      expect(table.querySelector('th:last-child')?.className).toContain('text-right')
      const action = table.querySelector<HTMLButtonElement>('tbody button')!
      expect(action.parentElement?.className).toBe('flex flex-wrap items-center justify-end gap-2')
      expect(action.className).toContain('border-input')
      expect(action.className).toContain('h-8')
    }
    tables[1]!.querySelector<HTMLButtonElement>('tbody button')!.click(); await flush()
    expect(state.reviewOpen).toBe(true)
    expect(mocks.operations).toHaveBeenCalledTimes(1)
    expect(mocks.resolve).not.toHaveBeenCalled()
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('renders %s review and keeps draft across theme and close/reopen', async locale => {
    const state = mount(locale); await state.loadEntries(); await state.selectEntry(state.entries[0]); state.review(state.operations[0]); await flush()
    expect(document.body.textContent).toContain(recoverySupportMessages[locale].title)
    expect(document.body.textContent).toContain(recoverySupportMessages[locale].routeVersion)
    expect(document.body.textContent).toContain('-1005')
    expect(document.body.textContent).toContain(recoverySupportMessages[locale].rules)
    expect(document.body.textContent).toContain(recoverySupportMessages[locale].evidenceHint)
    expect(state.decisions).toEqual(['failed', 'succeeded'])
    state.form.decision = 'succeeded'; state.form.evidence = 'audit:123'; state.form.threadID = '9007199254740993'; state.form.acknowledged = true
    document.documentElement.classList.add('dark'); await flush()
    expect(state.form.threadID).toBe('9007199254740993'); expect(state.valid).toBe(true)
    state.reviewOpen = false; state.review(state.operations[0]); await flush(); expect(state.form.evidence).toBe('audit:123')
    expect(document.querySelector('table')).not.toBeNull(); expect(document.querySelector('#support-review-thread')).not.toBeNull()
    expect(mocks.resolve).not.toHaveBeenCalled()
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('distinguishes recorded results, checkpoint-only action and retry in %s', async locale => {
    const state = mount(locale), messages = recoverySupportMessages[locale]
    mocks.entries.mockResolvedValue({ data: { data: { task_version: '8', next_after_id: '0', items: [{ id: '12', target_id: '5', revision: '4', state: 'restored', legacy: '0', operation_id: '14' }] } } })
    mocks.operations.mockResolvedValue({ data: { data: { task_version: '8', next_after_id: '0', items: [{ id: '14', version: '5', state: 'succeeded', retired: false, target_id: '5', route_version: '3', chat_id: '-1005', action: 'create' }] } } })
    await state.loadEntries(); await state.selectEntry(state.entries[0]); state.review(state.operations[0]); await flush()
    expect(document.body.textContent).toContain(messages.receiptTitle); expect(document.body.textContent).toContain(messages.receiptHint)
    expect(state.decisionLabel('succeeded')).toBe(messages.receiptSucceeded); expect(state.decisions).toEqual(['succeeded'])
    state.form.decision = 'succeeded'; await flush(); expect(document.body.textContent).toContain(messages.receiptThreadHint)
    expect(document.body.textContent).toContain(messages.receiptAcknowledge); expect(document.body.textContent).toContain(messages.receiptSave)
    state.form.evidence = 'audit:receipt'; state.form.threadID = '9007199254740999'; state.form.acknowledged = true
    document.documentElement.classList.add('dark'); await flush(); expect(state.valid).toBe(true); expect(state.form.threadID).toBe('9007199254740999')
    state.entry.state = 'failed'; state.operation.state = 'failed'; state.form.decision = 'failed'; await flush()
    expect(state.decisions).toEqual(['failed', 'retry']); expect(state.decisionLabel('failed')).toBe(messages.receiptFailed)
    expect(document.body.textContent).toContain(messages.receiptRetryHint); expect(document.querySelector('#support-review-thread')).toBeNull()
    expect(state.form.threadID).toBe(''); expect(state.form.acknowledged).toBe(false)
    state.form.decision = 'retry'; await flush(); expect(state.decisionLabel('retry')).toBe(messages.retry)
    expect(document.body.textContent).toContain(messages.acknowledge); expect(mocks.resolve).not.toHaveBeenCalled()
  })
  it('keeps all translated message keys aligned', () => {
    expect(Object.keys(recoverySupportMessages['zh-TW']).sort()).toEqual(Object.keys(recoverySupportMessages['zh-CN']).sort())
    expect(Object.keys(recoverySupportMessages['en-US']).sort()).toEqual(Object.keys(recoverySupportMessages['zh-CN']).sort())
  })
})
