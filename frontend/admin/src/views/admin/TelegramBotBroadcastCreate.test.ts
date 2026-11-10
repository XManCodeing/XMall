import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import TelegramBotBroadcastCreate from './TelegramBotBroadcastCreate.vue'

const mocks = vi.hoisted(() => ({ preview: vi.fn(), create: vi.fn(), users: vi.fn(), denied: '' }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  previewTelegramBroadcast: mocks.preview, createTelegramBroadcast: mocks.create,
  getTelegramBroadcastUsers: mocks.users,
} }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn(), notifyErrorOnce: vi.fn(), notifySuccess: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (key: string) => key !== mocks.denied }) }))
vi.mock('@/composables/useTelegramBotInstances', () => ({ useTelegramBotInstances: () => ({
  instances: ref([{ id: 2, mode: 'internal', enabled: true, token_set: true, username: 'test_bot', telegram_id: 22 }]),
  refresh: vi.fn(), loading: ref(false), refreshError: ref(''), canList: ref(true),
}) }))
vi.mock('@/components/admin/MediaPicker.vue', () => ({ default: { template: '<div />' } }))
let app: App | undefined
let root: HTMLDivElement | undefined
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const mount = async () => {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(TelegramBotBroadcastCreate)
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
  app.use(createRouter({ history: createMemoryHistory(), routes: [
    { path: '/', component: { template: '<div />' } },
    { path: '/telegram-bot/broadcasts', component: { template: '<div />' } },
  ] }))
  const instance = app.mount(root)
  await flush()
  const vm = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
  vm.form.title = 'Test'
  vm.form.message_html = 'Message'
  return vm
}
beforeEach(() => {
  vi.clearAllMocks()
  mocks.denied = ''
  mocks.preview.mockResolvedValue({ data: { data: { recipient_count: 2 } } })
  mocks.create.mockResolvedValue({ data: { data: {} } })
  mocks.users.mockResolvedValue({ data: { data: [] } })
})
afterEach(() => { app?.unmount(); root?.remove() })

describe('broadcast create preview confirmation', () => {
  it('searches every recipient input on Enter and resets only query fields with one request', async () => {
    const state = await mount()
    state.form.recipient_type = 'specific'; await flush(); mocks.users.mockClear()
    state.selectedUserIds = [12]
    Object.assign(state.filters, { keyword: 'member', display_name: 'Name', telegram_username: 'user', telegram_user_id: '12', created_from: '2026-11-01T12:00', created_to: '2026-11-02T12:00' })
    await flush(); expect(mocks.users).not.toHaveBeenCalled()
    const input = root!.querySelector<HTMLInputElement>('input[placeholder="telegramBot.broadcasts.filterKeyword"]')!
    const fields = input.parentElement!.querySelectorAll('input')
    expect(fields).toHaveLength(6)
    for (const field of fields) {
      state.pagination.page = 4
      field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); await flush()
      expect(mocks.users).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1, bot_id: 2, keyword: 'member' }))
      expect(state.pagination.page).toBe(1)
    }
    const reset = [...root!.querySelectorAll('button')].find(button => button.textContent?.trim() === 'telegramBot.broadcasts.resetFilters')!
    reset.click(); await flush()
    expect(mocks.users).toHaveBeenCalledTimes(7)
    expect(Object.values(state.filters)).toEqual(['', '', '', '', '', ''])
    expect(state.botID).toBe('2'); expect(state.selectedUserIds).toEqual([12]); expect(state.form.message_html).toBe('Message')
    state.loadingUsers = true; await flush(); expect(reset.disabled).toBe(true)
    await state.searchUsers(); await state.resetFilters()
    expect(mocks.users).toHaveBeenCalledTimes(7)
    expect(mocks.preview).not.toHaveBeenCalled(); expect(mocks.create).not.toHaveBeenCalled()
  })
  it('uses the product table theme and spacing without changing recipient selection', async () => {
    mocks.users.mockResolvedValue({ data: { data: [{ user_id: 12, display_name: 'Recipient', telegram_user_id: '12345' }] } })
    const state = await mount()
    state.form.recipient_type = 'specific'
    await state.fetchUsers(1); await flush()
    const table = root!.querySelector('table')!
    expect(table.querySelector('thead')?.className).toContain('bg-muted/40')
    expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
    expect(table.querySelector('th:nth-child(2)')?.className).toContain('px-6 py-3')
    expect(table.querySelector('td:nth-child(2)')?.className).toContain('px-6 py-4')
    expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
    table.querySelector<HTMLButtonElement>('tbody [role="checkbox"]')!.click()
    await flush()
    expect(state.selectedUserIds).toEqual([12])
    expect(mocks.create).not.toHaveBeenCalled()
  })
  it.each(['all', 'specific'])('previews %s before creating and shows the selected bot', async type => {
    const vm = await mount()
    vm.form.recipient_type = type
    if (type === 'specific') vm.selectedUserIds = [1, 2, 3]
    await vm.handleSubmit()
    await flush()
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.preview).toHaveBeenCalledWith(expect.objectContaining({ bot_id: 2, recipient_type: type, user_ids: type === 'specific' ? [1, 2, 3] : [] }))
    expect(vm.recipientCount).toBe(2)
    expect(document.body.textContent).toContain('@test_bot')
    expect(vm.canConfirm).toBe(true)
    await vm.confirmSubmit()
    await vm.confirmSubmit()
    expect(mocks.create).toHaveBeenCalledExactlyOnceWith(mocks.preview.mock.calls[0]![0])
  })
  it('never creates while loading, after zero, or on failed preview', async () => {
    const vm = await mount()
    let resolve!: (value: unknown) => void
    mocks.preview.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const pending = vm.handleSubmit()
    await vm.confirmSubmit()
    expect(vm.previewLoading).toBe(true)
    expect(mocks.create).not.toHaveBeenCalled()
    resolve({ data: { data: { recipient_count: 0 } } })
    await pending
    await vm.confirmSubmit()
    expect(vm.recipientCount).toBe(0)
    expect(mocks.create).not.toHaveBeenCalled()
    mocks.preview.mockRejectedValueOnce(new Error('offline'))
    await vm.handleSubmit()
    await vm.confirmSubmit()
    expect(vm.previewState).toBe('failed')
    expect(mocks.create).not.toHaveBeenCalled()
  })
  it.each(['bot', 'type', 'selection', 'filters', 'message'])('requires a fresh preview when %s changes', async change => {
    const vm = await mount()
    await vm.handleSubmit()
    if (change === 'bot') vm.botID = '3'
    if (change === 'type') vm.form.recipient_type = 'specific'
    if (change === 'selection') vm.selectedUserIds = [3]
    if (change === 'filters') vm.filters.keyword = 'changed'
    if (change === 'message') vm.form.message_html = 'Edited'
    await vm.confirmSubmit()
    expect(vm.recipientCount).toBeNull()
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.preview).toHaveBeenCalledTimes(1)
  })
  it('requires preview permission and retains the draft after creation failure', async () => {
    mocks.denied = 'POST:/admin/telegram-bot/broadcasts/preview'
    let vm = await mount()
    await vm.handleSubmit()
    expect(mocks.preview).not.toHaveBeenCalled()
    app?.unmount(); root?.remove()
    mocks.denied = ''
    vm = await mount()
    await vm.handleSubmit()
    mocks.create.mockRejectedValueOnce(new Error('revalidation failed'))
    await vm.confirmSubmit()
    expect(vm.form.message_html).toBe('Message')
    expect(vm.canConfirm).toBe(false)
    await vm.confirmSubmit()
    expect(mocks.create).toHaveBeenCalledTimes(1)
    await vm.handleSubmit()
    expect(mocks.preview).toHaveBeenCalledTimes(2)
  })
})
