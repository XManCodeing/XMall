import { beforeEach, describe, expect, it, vi } from 'vitest'
import { adminAPI } from './admin'

const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
beforeEach(() => vi.clearAllMocks())

describe('broadcast delivery endpoints', () => {
  it('previews using the same payload as creation and the API client prefix', () => {
    const payload = { bot_id: 2, title: 'Title', message_html: 'Message', recipient_type: 'specific', user_ids: [1, 2], filters: {} }
    adminAPI.previewTelegramBroadcast(payload)
    adminAPI.createTelegramBroadcast(payload)
    expect(mocks.post).toHaveBeenNthCalledWith(1, '/admin/telegram-bot/broadcasts/preview', payload)
    expect(mocks.post).toHaveBeenNthCalledWith(2, '/admin/telegram-bot/broadcasts', payload)
  })
  it('uses the registered deliveries path and pagination query', () => {
    adminAPI.getTelegramBroadcastDeliveries(12, { page: 2, page_size: 20 })
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/broadcasts/12/deliveries', { params: { page: 2, page_size: 20 } })
  })
  it('posts retry for the delivery within its broadcast without payload or bot overrides', () => {
    adminAPI.retryTelegramBroadcastDelivery(12, 45)
    expect(mocks.post).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/broadcasts/12/deliveries/45/retry')
  })
})
