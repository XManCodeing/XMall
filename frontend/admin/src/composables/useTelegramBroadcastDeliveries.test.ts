import { effectScope, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminTelegramBroadcastDelivery } from '@/api/types'
import { useTelegramBroadcastDeliveries } from './useTelegramBroadcastDeliveries'

const mocks = vi.hoisted(() => ({ list: vi.fn(), retry: vi.fn(), read: true, write: true }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (key: string) => key.startsWith('GET:') ? mocks.read : mocks.write }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBroadcastDeliveries: mocks.list, retryTelegramBroadcastDelivery: mocks.retry } }))
const row = (status = 'failed', retryable = true): AdminTelegramBroadcastDelivery => ({
  id: 7, bot_id: 2, broadcast_id: 1, user_id: 3, telegram_user_id: 4, chat_id: 4,
  status, retryable, attempts: 1, message_id: 0, last_error: 'telegram rejected message',
  available_at: '', created_at: '', updated_at: '',
})
const response = (items = [row()], page = 1) => ({ data: { data: items, pagination: { page, page_size: 20, total: 25, total_page: 2 } } })
const scopes: ReturnType<typeof effectScope>[] = []
function setup(enabled = true) {
  const id = ref(1)
  const active = ref(enabled)
  const scope = effectScope()
  scopes.push(scope)
  return { id, active, model: scope.run(() => useTelegramBroadcastDeliveries(id, active))! }
}
beforeEach(() => {
  vi.resetAllMocks(); mocks.read = true; mocks.write = true
  mocks.list.mockImplementation(async () => response())
  mocks.retry.mockResolvedValue({ data: { data: { accepted: true } } })
})
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()) })

describe('broadcast recipient deliveries', () => {
  it('loads the paginated DTO and requests the selected page', async () => {
    const { model } = setup(); await nextTick()
    expect(mocks.list).toHaveBeenCalledWith(1, { page: 1, page_size: 20 })
    mocks.list.mockResolvedValueOnce(response([], 2))
    await model.fetchPage(2)
    expect(mocks.list).toHaveBeenLastCalledWith(1, { page: 2, page_size: 20 })
    expect(model.pagination.value.page).toBe(2)
    expect(model.items.value).toEqual([])
  })
  it('does not query legacy broadcasts or without read permission', async () => {
    const { model } = setup(false); await model.fetchPage()
    mocks.read = false
    const denied = setup(); await denied.model.fetchPage()
    expect(mocks.list).not.toHaveBeenCalled()
  })
  it.each(['unknown', 'sent', 'pending', 'processing', 'retry'])('never retries %s even if retryable is true', async status => {
    mocks.list.mockResolvedValue(response([row(status)]))
    const { model } = setup(); await nextTick()
    const item = model.items.value[0]!
    expect(model.canRetry(item)).toBe(false)
    await model.retry(item)
    expect(mocks.retry).not.toHaveBeenCalled()
  })
  it('requires explicit backend retryable and POST permission', async () => {
    const { model } = setup(); await nextTick()
    const item = model.items.value[0]!
    mocks.write = false
    await model.retry(item)
    mocks.write = true; item.retryable = false
    await model.retry(item)
    expect(mocks.retry).not.toHaveBeenCalled()
    expect(model.canRetry({ ...item, retryable: true, broadcast_id: 2 })).toBe(false)
  })
  it('accepts a retry once and refreshes the current page without claiming delivery success', async () => {
    const { model } = setup(); await nextTick()
    mocks.list.mockResolvedValue(response([row('pending', false)]))
    const item = model.items.value[0]!
    const first = model.retry(item)
    await model.retry(item)
    expect(await first).toBe(true)
    expect(mocks.retry).toHaveBeenCalledExactlyOnceWith(1, 7)
    expect(model.notice.value).toBe('telegramBot.instances.retryAccepted')
    expect(model.items.value[0]?.status).toBe('pending')
  })
  it('fails closed after ambiguous retry failure until explicit reload', async () => {
    mocks.retry.mockRejectedValue(new Error('offline'))
    const { model } = setup(); await nextTick()
    const item = model.items.value[0]!
    await model.retry(item); await model.retry(item)
    expect(mocks.retry).toHaveBeenCalledTimes(1)
    expect(model.error.value).toBe('telegramBot.instances.retryFailed')
    expect(model.canRetry(item)).toBe(false)
  })
  it('does not treat an unaccepted response as successful', async () => {
    mocks.retry.mockResolvedValue({ data: { data: {} } })
    const { model } = setup(); await nextTick()
    expect(await model.retry(model.items.value[0]!)).toBe(false)
    expect(model.notice.value).toBe('')
  })
  it('clears records on load failure and permits explicit reload', async () => {
    mocks.list.mockRejectedValueOnce(new Error('offline'))
    const { model } = setup(); await nextTick()
    expect(model.error.value).toBe('telegramBot.instances.recipientLoadFailed')
    expect(model.items.value).toEqual([])
    await model.fetchPage()
    expect(model.items.value).toHaveLength(1)
    expect(model.error.value).toBe('')
  })
  it('ignores stale responses after switching broadcasts', async () => {
    let resolve!: (value: ReturnType<typeof response>) => void
    mocks.list.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const { id, model } = setup()
    id.value = 2
    await nextTick()
    resolve(response([row('sent')]))
    await nextTick()
    expect(model.items.value[0]?.status).toBe('failed')
    expect(mocks.list).toHaveBeenLastCalledWith(2, { page: 1, page_size: 20 })
  })
})
