import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createI18n } from 'vue-i18n'
import TelegramBotSupportTasks from './TelegramBotSupportTasks.vue'
import { telegramSupportMessages } from '@/i18n/telegramSupport'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ api: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ isSuper: true, permissions: [], hasPermission: () => true }) }))
let app: App | undefined, root: HTMLDivElement | undefined
/** Drain metadata requests and Vue component rendering. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
beforeEach(() => {
  vi.clearAllMocks()
  mocks.get.mockImplementation((url: string) => Promise.resolve({ data: { data: url.endsWith('/4')
    ? { id: 4, bot_id: 2, session_id: 1, version: 7, state: 'unknown', action: 'create', target_id: 8, route_version: 3, updated_at: '' }
    : { items: [], total: 0 } } }))
})
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark') })

describe('support task page', () => {
  it('matches product table spacing and outline actions while preserving detail navigation', async () => {
    const row = { id: 4, bot_id: 2, session_id: 1, version: 7, state: 'unknown', kind: 'text', updated_at: '' }
    mocks.get.mockImplementation((url: string) => Promise.resolve({ data: { data: url.endsWith('/4') ? row : { items: [row], total: 1 } } }))
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramBotSupportTasks)
    app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] }))
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupport: telegramSupportMessages['zh-CN'] } } }))
    app.mount(root); await flush()
    const table = root.querySelector('table')!
    expect(table.parentElement?.classList.contains('overflow-auto')).toBe(true)
    expect(table.parentElement?.parentElement?.className).toBe('rounded-xl border border-border bg-card overflow-hidden')
    expect(table.querySelector('thead')?.className).toContain('bg-muted/40 text-xs uppercase text-muted-foreground')
    for (const header of table.querySelectorAll('th')) expect(header.className).toContain('px-6 py-3')
    for (const cell of table.querySelectorAll('tbody td')) expect(cell.className).toContain('px-6 py-4')
    expect(table.querySelector('tbody tr')?.classList.contains('hover:bg-muted/30')).toBe(true)
    expect(table.querySelector('th:last-child')?.classList.contains('text-right')).toBe(true)
    expect(table.querySelector('td:last-child')?.classList.contains('text-right')).toBe(true)
    expect(table.querySelector('td:last-child > div')?.className).toBe('flex flex-wrap items-center justify-end gap-2')
    const action = table.querySelector<HTMLButtonElement>('td:last-child button')!
    expect(action.classList.contains('border-input')).toBe(true)
    expect(action.classList.contains('h-8')).toBe(true)
    action.click(); await flush()
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/support/downstreams/4')
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    expect(mocks.post).not.toHaveBeenCalled()
  })
  it('queries only on search/reset and preserves resource scope when bot is required', async () => {
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramBotSupportTasks)
    app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] }))
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false,
      messages: { 'zh-CN': { telegramSupport: telegramSupportMessages['zh-CN'], admin: { common: { search: '搜索', reset: '重置' } } } } }))
    const instance = app.mount(root)
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    await flush(); mocks.get.mockClear()
    state.resource = 'upstreams'; state.botFilter = '2'; state.stateFilter = 'unknown'; await flush()
    expect(mocks.get).not.toHaveBeenCalled()
    const search = Array.from(root.querySelectorAll('button')).find(button => button.textContent === '搜索')!
    const reset = Array.from(root.querySelectorAll('button')).find(button => button.textContent === '重置')!
    expect(search.className).toContain('border-input'); expect(reset.className).toContain('border-input')
    search.click(); await flush()
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/support/upstreams', { params: { page: 1, page_size: 20, bot_id: 2, state: 'unknown' } })
    reset.click(); await flush()
    expect(state.resource).toBe('upstreams'); expect(state.botFilter).toBe('2'); expect(state.page).toBe(1)
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/support/upstreams', { params: { page: 1, page_size: 20, bot_id: 2, state: undefined } })
    state.resource = 'topic-operations'; state.stateFilter = 'unknown'; await flush()
    reset.click(); await flush()
    expect(mocks.get).toHaveBeenCalledTimes(3)
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/support/topic-operations', { params: { page: 1, page_size: 20, bot_id: 2, state: undefined } })
    state.resource = 'downstreams'; state.botFilter = '3'; await flush()
    expect(mocks.get).toHaveBeenCalledTimes(3)
    state.page = 3; reset.click(); await flush()
    expect(mocks.get).toHaveBeenCalledTimes(4)
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/support/downstreams', { params: { page: 1, page_size: 20, bot_id: undefined, state: undefined } })
    expect(root.textContent).not.toContain('telegramBot.instances.reload')
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('associates the actual localized description for every task type in %s', async locale => {
    const warnings = vi.spyOn(console, 'warn')
    try {
      root = document.createElement('div'); document.body.append(root)
      app = createApp(TelegramBotSupportTasks)
    app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] }))
      app.use(createI18n({ legacy: false, locale, missingWarn: false, fallbackWarn: false, messages: { [locale]: { telegramSupport: telegramSupportMessages[locale] } } }))
      const instance = app.mount(root)
      const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
      for (const resource of ['downstreams', 'upstreams', 'topic-operations'] as const) {
        state.resource = resource; state.botFilter = '2'; await flush()
        await state.show(4); await flush()
        const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
        expect(dialog).not.toBeNull()
        const descriptionID = dialog.getAttribute('aria-describedby')
        expect(descriptionID).toBeTruthy()
        const description = document.getElementById(descriptionID!)
        expect(description).not.toBeNull()
        expect(dialog.contains(description)).toBe(true)
        expect(description!.textContent).toBe(telegramSupportMessages[locale].identities[resource])
        state.open = false; await flush()
      }
      expect(warnings.mock.calls.flat().join(' ')).not.toMatch(/Missing.*Description|aria-describedby/)
    } finally { warnings.mockRestore() }
  })
  it('keeps a large thread as text across global themes and clears drafts when changing category', async () => {
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramBotSupportTasks)
    app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] }))
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupport: telegramSupportMessages['zh-CN'] } } }))
    const instance = app.mount(root)
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    state.resource = 'topic-operations'; state.botFilter = '2'; await flush(); await state.show(4)
    state.form.decision = 'succeeded'; await flush()
    const input = document.querySelector<HTMLInputElement>('#support-thread')!
    expect(input).not.toBeNull(); expect(input.type).toBe('text')
    input.value = '9223372036854775807'; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
    state.form.evidence = 'case:review'; expect(state.valid).toBe(true)
    document.documentElement.classList.add('dark'); await flush()
    expect(input.value).toBe('9223372036854775807'); expect(state.form.threadID).toBe('9223372036854775807')
    document.documentElement.classList.remove('dark'); await flush(); expect(state.form.evidence).toBe('case:review')
    state.resource = 'upstreams'; await flush()
    expect(state.open).toBe(false); expect(state.task).toBeNull(); expect(state.form.threadID).toBe('')
    expect(mocks.get.mock.calls.some(([url]) => url.includes('/content'))).toBe(false); expect(mocks.post).not.toHaveBeenCalled()
  })
})
