import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createI18n } from 'vue-i18n'
import Sessions from './TelegramBotSupportSessions.vue'
import { telegramSupportMessages } from '@/i18n/telegramSupport'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ api: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ isSuper: true, permissions: [], hasPermission: () => true }) }))
let app: App | undefined, root: HTMLDivElement | undefined
/** Flush only microtasks and Vue rendering; this test never starts a real site. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.clearAllMocks() })

describe('read-only support operations page', () => {
  it('renders metadata, filters and trusted links without content or admin writes', async () => {
    const row = { id: 1, bot_id: 2, user_id: '9007199254740993', chat_id: '9007199254740993', state: 'open', version: 1,
      created_at: '', updated_at: '', unreplied: true, unreplied_since: '2026-11-01T12:00:00Z', blocked: true, block_expires_at: null,
      upstream_failed: 2, upstream_unknown: 1, downstream_failed: 3, downstream_unknown: 4,
      topic_url: 'https://t.me/c/1234567890/9007199254740993', topic_chat_id: '-1001234567890', topic_thread_id: '9007199254740990993', topic_state: 'open' }
    mocks.get.mockImplementation((url: string) => Promise.resolve({ data: { data: url.endsWith('/1') ? row : { items: [row], total: 1 } } }))
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Sessions)
    app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] }))
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupport: telegramSupportMessages['zh-CN'] } } }))
    const instance = app.mount(root); await flush()
    expect(root.textContent).toContain('未回复'); expect(root.textContent).toContain('长期有效')
    expect(root.textContent).toContain('上行：失败 2 / 未知 1')
    expect(root.querySelector('a[target="_blank"]')?.getAttribute('href')).toBe(row.topic_url)
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
    for (const action of table.querySelectorAll('td:last-child button, td:last-child a')) {
      expect(action.classList.contains('border-input')).toBe(true)
      expect(action.classList.contains('h-8')).toBe(true)
    }
    expect(root.querySelector('a[target="_blank"]')?.getAttribute('target')).toBe('_blank')
    expect(root.querySelector('a[target="_blank"]')?.getAttribute('rel')).toBe('noopener noreferrer')
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    state.unrepliedFilter = 'true'; state.blockedFilter = 'false'; await state.load(1)
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/support/sessions', expect.objectContaining({ params: expect.objectContaining({ unreplied: true, blocked: false }) }))
    document.documentElement.classList.add('dark'); await flush()
    expect(state.unrepliedFilter).toBe('true'); expect(state.blockedFilter).toBe('false')
    document.documentElement.classList.remove('dark'); await flush()
    expect(state.unrepliedFilter).toBe('true')
    expect(state.topicURL({ ...row, topic_url: 'javascript:alert(1)' })).toBe('')
    expect(mocks.get.mock.calls.some(([url]) => url.includes('/content'))).toBe(false)
    expect(mocks.post).not.toHaveBeenCalled()
    state.session = row
    state.messages = [{ id: 7, sequence: 1, state: 'delivered', retain_until: '' }]
    state.open = true; await flush()
    const messages = document.querySelector('[role="dialog"] table')!
    expect(messages).not.toBeNull()
    expect(messages.parentElement?.parentElement?.className).toBe('rounded-xl border border-border bg-card overflow-hidden')
    expect(messages.querySelector('thead')?.className).toContain('bg-muted/40 text-xs uppercase text-muted-foreground')
    expect(messages.querySelector('tbody tr')?.classList.contains('hover:bg-muted/30')).toBe(true)
    for (const header of messages.querySelectorAll('th')) expect(header.className).toContain('px-6 py-3')
    for (const cell of messages.querySelectorAll('td')) expect(cell.className).toContain('px-6 py-4')
    expect(messages.querySelector('td:last-child > div')?.className).toBe('flex flex-wrap items-center justify-end gap-2')
    const contentButton = messages.querySelector<HTMLButtonElement>('td:last-child button')!
    expect(contentButton.classList.contains('border-input')).toBe(true)
    expect(contentButton.classList.contains('h-8')).toBe(true)
    state.contentLoading = true; await flush()
    expect(contentButton.disabled).toBe(true)
  })
})
