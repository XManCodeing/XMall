import { describe, expect, it, vi } from 'vitest'
import { telegramRecoveryAPI, validRecoveryChatID, validRecoveryBotID } from './telegramRecovery'
import { telegramRecoveryMessages } from '@/i18n/telegramRecovery'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))

describe('recovery preview-only API', () => {
  it('preserves string identifiers, versions and bounded cursors on actual routes', () => {
    const id = '9007199254740993', version = '9007199254740995'
    telegramRecoveryAPI.list(id); telegramRecoveryAPI.get(id); telegramRecoveryAPI.steps(id, '42'); telegramRecoveryAPI.cancel(id, version)
    expect(mocks.get.mock.calls).toEqual([
      ['/admin/telegram-bot/recovery/tasks', { params: { after_id: id, limit: 20 } }],
      [`/admin/telegram-bot/recovery/tasks/${id}`],
      [`/admin/telegram-bot/recovery/tasks/${id}/steps`, { params: { after_id: '42', limit: 20 } }],
    ])
    expect(mocks.post).toHaveBeenCalledWith(`/admin/telegram-bot/recovery/tasks/${id}/cancel`, { expected_version: version })
    telegramRecoveryAPI.confirm(id, 'reviewed-reference', version); telegramRecoveryAPI.resume(id, version)
    expect(mocks.post).toHaveBeenCalledWith(`/admin/telegram-bot/recovery/tasks/${id}/confirm`, { preflight_ref: 'reviewed-reference', expected_version: version })
    expect(mocks.post).toHaveBeenCalledWith(`/admin/telegram-bot/recovery/tasks/${id}/resume`, { expected_version: version })
    expect(Object.keys(telegramRecoveryAPI)).toEqual(['botPreflight', 'list', 'listPage', 'stepsPage', 'get', 'steps', 'preflight', 'cancel', 'confirm', 'resume'])
  })
  it('requests numbered pages directly without cursor traversal or unbounded sizes', () => {
    telegramRecoveryAPI.listPage(4, 50)
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/recovery/tasks', { params: { page: 4, page_size: 50 } })
    telegramRecoveryAPI.stepsPage('9007199254740993', 7, 10)
    expect(mocks.get).toHaveBeenLastCalledWith('/admin/telegram-bot/recovery/tasks/9007199254740993/steps', { params: { page: 7, page_size: 10 } })
  })
  it('posts the same-Bot support preview DTO without trusting a client purpose field', () => {
    const input = { target_id: 12, expected_version: 7, expected_route_version: 8, new_bot_instance_id: 2,
      new_telegram_chat_id: '-1009007199254740994', request_key: 'support-preview', reason: 'replace support group' }
    telegramRecoveryAPI.preflight(input)
    expect(mocks.post).toHaveBeenLastCalledWith('/admin/telegram-bot/recovery/group-preflight', input)
    expect(mocks.post.mock.lastCall?.[1]).not.toHaveProperty('purpose')
  })
  it('posts only the cross-Bot DTO with exact string IDs', () => {
    const input = { source_bot_id: '9007199254740993', destination_bot_id: '18446744073709551615', request_key: 'bot-preview', reason: 'replace' }
    telegramRecoveryAPI.botPreflight(input)
    expect(mocks.post).toHaveBeenLastCalledWith('/admin/telegram-bot/recovery/bot-preflight', input)
    for (const id of ['1', input.source_bot_id, input.destination_bot_id]) expect(validRecoveryBotID(id)).toBe(true)
    for (const id of ['0', '-1', '01', '1e3', ' 1', '18446744073709551616']) expect(validRecoveryBotID(id)).toBe(false)
  })
  it('accepts canonical negative int64 strings only', () => {
    for (const value of ['-1009007199254740993', '-9223372036854775808', '-1']) expect(validRecoveryChatID(value)).toBe(true)
    for (const value of ['0', '-0', '1', '-01', '-1.1', '-1e9', ' -1', '-1 ', '-9223372036854775809', '-100000000000000000000']) expect(validRecoveryChatID(value)).toBe(false)
  })
  it('has matching standalone three-language keys and explicit preview-only copy', () => {
    const messages = telegramRecoveryMessages
    for (const locale of ['zh-TW', 'en-US'] as const) {
      expect(Object.keys(messages[locale])).toEqual(Object.keys(messages['zh-CN']))
      expect(Object.keys(messages[locale].values)).toEqual(Object.keys(messages['zh-CN'].values))
    }
    expect(messages['zh-CN'].hint).toContain('服务端开放执行能力')
    expect(messages['en-US'].hint).toContain('server capability')
  })
})
