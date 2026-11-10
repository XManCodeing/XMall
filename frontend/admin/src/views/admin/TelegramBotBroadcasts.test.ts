import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, KeepAlive, nextTick, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Broadcasts from './TelegramBotBroadcasts.vue'
import BroadcastDetail from './TelegramBotBroadcastDetail.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), detail: vi.fn(), retry: vi.fn(), deliveries: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBroadcasts: mocks.list, getTelegramBroadcast: mocks.detail } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => true }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ params: { id: '1' } }) }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: vi.fn() }))
vi.mock('@/composables/useTelegramBroadcastDeliveries', () => ({ useTelegramBroadcastDeliveries: () => mocks.deliveries }))
let app: App, root: HTMLDivElement

/** Drain Vue rendering and request promises. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount a broadcast view with real table/button components. @param component Target view. @returns Setup state. */
function mount(component: typeof Broadcasts | typeof BroadcastDetail) {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(component)
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
/** Verify product-list table geometry and theme tokens. @param columns Expected column count. @returns Nothing. */
function expectTableStyle(columns: number) {
  const table = root.querySelector('table')!
  expect(table.closest('.rounded-xl.border.border-border.bg-card.overflow-x-auto')).not.toBeNull()
  expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
  expect(table.querySelectorAll('th')).toHaveLength(columns)
  table.querySelectorAll('th').forEach(cell => expect(cell.className).toContain('px-6 py-3'))
  expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
  expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
  table.querySelectorAll('tbody td').forEach(cell => expect(cell.className).toContain('px-6 py-4'))
  expect(table.querySelector('th:last-child')?.className).toContain('text-right')
  const action = table.querySelector('tbody td:last-child')!
  expect(action.className).toContain('text-right')
  expect(action.firstElementChild?.className).toBe('flex flex-wrap items-center justify-end gap-2')
  expect(action.querySelector('button, a')?.className).toContain('border-input')
}
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.useRealTimers(); vi.restoreAllMocks(); vi.resetAllMocks() })

describe('Telegram broadcast product-style tables', () => {
  it('styles the list and hides request errors without showing a successful empty state', async () => {
    mocks.list.mockResolvedValue({ data: { data: [{ id: 1, title: 'Broadcast', status: 'completed', created_at: '' }] } })
    const state = mount(Broadcasts); await flush()
    expectTableStyle(10)
    document.documentElement.classList.add('dark'); await flush()
    expect(root.textContent).toContain('Broadcast')
    state.items = []; state.loading = true; await flush()
    expect(root.querySelector('td[colspan="10"] svg')).not.toBeNull()
    state.loading = false; await flush()
    expect(root.querySelector('td[colspan="10"]')?.textContent).toContain('telegramBot.broadcasts.empty')
    state.error = 'List failed'; await flush()
    expect(root.querySelector('td[colspan="10"]')).toBeNull()
    expect(root.textContent).not.toContain('List failed')
    expect(root.querySelector('[role="alert"]')).toBeNull()
  })
  it('pauses cached broadcasts and resumes the retained page without duplicate intervals', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    mocks.list.mockResolvedValue({ data: { data: [{ id: 1, title: 'Running', status: 'running' }], pagination: { page: 3, page_size: 20, total: 80, total_page: 4 } } })
    const visible = ref(true)
    root = document.createElement('div')
    app = createApp({ render: () => h(KeepAlive, null, { default: () => visible.value ? h(Broadcasts) : null }) })
    app.component('RouterLink', { template: '<a><slot /></a>' })
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    app.mount(root)
    await flush()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(1)
    visible.value = false
    await flush()
    expect(vi.getTimerCount()).toBe(0)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(24000)
    expect(mocks.list).toHaveBeenCalledTimes(1)
    visible.value = true
    await flush()
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 3, page_size: 20 })
    expect(vi.getTimerCount()).toBe(1)
  })
  it('styles recipient details without changing retry or eight-column states', async () => {
    const recipient = { id: 2, user_id: 3, telegram_user_id: '4', status: 'failed', attempts: 1, updated_at: '' }
    mocks.detail.mockResolvedValue({ data: { data: { id: 1, bot_id: 1, delivery_version: 1, status: 'completed', message_html: '', created_at: '' } } })
    mocks.deliveries = {
      items: ref([recipient]), loading: ref(false), retrying: ref(null), error: ref(''),
      pagination: ref({ page: 1, total_page: 1 }), canList: ref(true), canRetry: () => true,
      fetchPage: vi.fn(), retry: mocks.retry,
    }
    mount(BroadcastDetail); await flush()
    expectTableStyle(8)
    root.querySelector<HTMLButtonElement>('tbody button')!.click()
    expect(mocks.retry).toHaveBeenCalledWith(recipient)
    mocks.deliveries.items.value = []; mocks.deliveries.loading.value = true; await flush()
    expect(root.querySelector('td[colspan="8"] svg')).not.toBeNull()
    mocks.deliveries.loading.value = false; await flush()
    expect(root.querySelector('td[colspan="8"]')?.textContent).toContain('telegramBot.instances.empty')
    mocks.deliveries.error.value = 'Recipients failed'; await flush()
    expect(root.querySelector('td[colspan="8"]')).toBeNull()
    expect(root.textContent).not.toContain('Recipients failed')
    expect(root.querySelector('[role="alert"]')).toBeNull()
  })
})
