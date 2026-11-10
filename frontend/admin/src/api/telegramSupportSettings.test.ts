import { describe, expect, it, vi } from 'vitest'
import { getSupportSettings, saveSupportSettings } from './telegramSupportSettings'

const mocks = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))

describe('support policy API', () => {
  it('uses a separate Bot-scoped GET/PUT contract without target or enable flags', () => {
    const policy = { max_messages: 10, max_bytes: 65536, max_attempts: 3, retention_seconds: 3600, challenge_ttl_seconds: 300, cooldown_seconds: 60 }
    getSupportSettings(3)
    saveSupportSettings(3, 4, { ...policy, enabled: true, target_id: 99 } as typeof policy)
    expect(mocks.get).toHaveBeenCalledWith('/admin/telegram-bot/support/settings/3')
    expect(mocks.put).toHaveBeenCalledWith('/admin/telegram-bot/support/settings/3', { expected_version: 4, policy })
  })
})
