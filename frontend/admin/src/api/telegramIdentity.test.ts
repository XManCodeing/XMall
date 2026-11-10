import { afterEach, describe, expect, it, vi } from 'vitest'
import { telegramIdentityAPI } from './telegramIdentity'
import { telegramIdentityMessages } from '@/i18n/telegramIdentity'
import type { RecoveryTask } from './telegramRecovery'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
afterEach(() => vi.clearAllMocks())
describe('identity recovery HTTP contract', () => {
  it('uses dedicated identity mutations and exact string CAS', () => {
    const task = { id: '9007199254740993', version: '9007199254740994', preflight_ref: 'review' } as RecoveryTask
    const input = { account_id: '7', old_telegram_id: '42', new_telegram_id: '9223372036854775807', target_ids: ['9'], request_key: 'key', reason: 'review', verification_note: 'verified', human_verified: true }
    telegramIdentityAPI.preflight(input); telegramIdentityAPI.get(task.id); telegramIdentityAPI.confirm(task); telegramIdentityAPI.resume(task)
    expect(mocks.post.mock.calls).toEqual([
      ['/admin/telegram-bot/recovery/identity-preflight', input],
      [`/admin/telegram-bot/recovery/tasks/${task.id}/identity-confirm`, { expected_version: task.version, preflight_ref: 'review' }],
      [`/admin/telegram-bot/recovery/tasks/${task.id}/identity-resume`, { expected_version: task.version }],
    ])
    expect(mocks.get).toHaveBeenCalledWith(`/admin/telegram-bot/recovery/tasks/${task.id}`)
  })
  it('keeps all three language message keys aligned', () => {
    for (const locale of ['zh-TW', 'en-US'] as const) expect(Object.keys(telegramIdentityMessages[locale])).toEqual(Object.keys(telegramIdentityMessages['zh-CN']))
  })
})
