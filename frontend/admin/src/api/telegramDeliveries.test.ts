import { beforeEach, describe, expect, it, vi } from 'vitest'
import { adminAPI } from './admin'

const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
beforeEach(() => vi.clearAllMocks())

describe('broadcast delivery endpoints', () => {
  it('uses the registered deliveries path and pagination query', () => {
    adminAPI.getTelegramBroadcastDeliveries(12, { page: 2, page_size: 20 })
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/broadcasts/12/deliveries', { params: { page: 2, page_size: 20 } })
  })
  it('posts retry for the delivery within its broadcast without payload or bot overrides', () => {
    adminAPI.retryTelegramBroadcastDelivery(12, 45)
    expect(mocks.post).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/broadcasts/12/deliveries/45/retry')
  })
})
