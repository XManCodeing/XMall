import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Broadcasts from './TelegramBotBroadcasts.vue'
import Status from './TelegramBotStatus.vue'

const mocks = vi.hoisted(() => ({ broadcasts: vi.fn(), bots: vi.fn(), deliveries: vi.fn(), allowed: true }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  getTelegramBroadcasts: mocks.broadcasts,
  getTelegramBotInstances: mocks.bots,
  getTelegramBotInstanceDeliveries: mocks.deliveries,
} }))
let app: App, root: HTMLDivElement
/** Drain Vue updates and mocked requests. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount the real list with isolated service mocks. @param component Target list. @returns Setup state. */
function mount(component: typeof Broadcasts | typeof Status) {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(component)
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks(); mocks.allowed = true })
describe('Telegram unfiltered list searches', () => {
  it('aligns bot headers and row actions with the existing product table', async () => {
    mocks.bots.mockResolvedValue({ data: { data: { items: [
      { id: 7, username: 'style_bot', enabled: true, token_set: true, version: 1, applied_version: 1 },
    ] } } })
    mount(Status); await flush()
    expect(root.querySelector('thead')?.className).toContain('bg-muted/40')
    expect(root.querySelector('th:last-child')?.className).toContain('text-right')
    const actions = root.querySelector('tbody tr td:last-child')!
    expect(actions.className).toContain('px-6 py-4 text-right')
    expect(actions.firstElementChild?.className).toContain('flex-wrap items-center justify-end gap-2')
    const buttons = [...actions.querySelectorAll('button')]
    expect(buttons).toHaveLength(4)
    for (const button of buttons) expect(button.className).toContain('h-8')
    expect(buttons[0]?.className).toContain('bg-destructive')
    for (const button of buttons.slice(1)) expect(button.className).toContain('border-input')
  })
  it('searches broadcasts from page one on repeated clicks, without a reset', async () => {
    mocks.broadcasts.mockImplementation(async ({ page }: { page: number }) => ({
      data: { data: [], pagination: { page, page_size: 20, total: 40, total_page: 2 } },
    }))
    const state = mount(Broadcasts); await flush()
    await state.fetchBroadcasts(2); await flush()
    expect(state.pagination.page).toBe(2)
    const search = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === 'admin.common.search')!
    search.click(); await flush(); search.click(); await flush()
    expect(mocks.broadcasts.mock.calls.slice(-2)).toEqual([
      [{ page: 1, page_size: 20 }], [{ page: 1, page_size: 20 }],
    ])
    expect(state.pagination.page).toBe(1)
    expect(root.textContent).not.toContain('admin.common.reset')
    expect(root.textContent).not.toContain('telegramBot.instances.reload')
  })
  it('searches the current bot delivery scope from page one and retains read guards', async () => {
    mocks.bots.mockResolvedValue({ data: { data: { items: [] } } })
    mocks.deliveries.mockResolvedValue({ data: { data: { items: [], total: 40 } } })
    const state = mount(Status); await flush()
    await state.loadDeliveries({ id: 7 }, 2); await flush()
    expect(state.page).toBe(2)
    const dialog = document.querySelector('[role="dialog"]')!
    const search = [...dialog.querySelectorAll('button')].find(button => button.textContent?.trim() === 'admin.common.search')!
    search.click(); await flush(); search.click(); await flush()
    expect(mocks.deliveries.mock.calls.slice(-2)).toEqual([
      [7, { page: 1, page_size: 20 }], [7, { page: 1, page_size: 20 }],
    ])
    expect(state.page).toBe(1)
    expect(dialog.textContent).not.toContain('admin.common.reset')
    mocks.allowed = false
    await state.loadDeliveries({ id: 8 }, 1)
    expect(mocks.deliveries).toHaveBeenCalledTimes(3)
  })
})
