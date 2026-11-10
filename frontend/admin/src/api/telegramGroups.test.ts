import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/i18n'
import { telegramGroupsAPI } from './telegramGroups'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), patch: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
beforeEach(() => vi.clearAllMocks())
describe('telegram groups API contracts', () => {
  it.each([
    ['zh-CN', '关闭', '保持匿名'], ['zh-TW', '關閉', '保持匿名'], ['en-US', 'Turn off', 'Remain Anonymous'],
  ])('provides an actionable anonymous administrator diagnostic in %s', (locale, action, setting) => {
    const message = i18n.global.getLocaleMessage(locale as 'zh-CN' | 'zh-TW' | 'en-US') as unknown as { telegramBot: { groups: { values: Record<string, string> } } }
    const values = message.telegramBot.groups.values
    expect(values.anonymous_admin).toContain(action)
    expect(values.anonymous_admin).toContain(setting)
    expect(values.anonymous_admin).not.toBe(values.resource_inaccessible)
  })
  const input = { name: 'Group', purpose: 'membership' as const, bot_instance_id: 2, telegram_chat_id: '-9223372036854775808', enabled: true, expected_version: 4, expected_route_version: 3 }
  const { name: _name, ...creationInput } = input
  it('uses dedicated non-writing preview endpoints for new and existing groups', () => {
    telegramGroupsAPI.previewEnable(input)
    telegramGroupsAPI.previewEnable(input, 1)
    expect(mocks.post.mock.calls).toEqual([
      ['/admin/telegram-bot/groups/enable-preview', creationInput, { timeoutMs: 30000 }],
      ['/admin/telegram-bot/groups/1/enable-preview', input, { timeoutMs: 30000 }],
    ])
  })
  it('uses all seven registered routes through the shared client', () => {
    telegramGroupsAPI.list({ page: 2, page_size: 20, purpose: 'support', bot_instance_id: 2 })
    telegramGroupsAPI.get(1)
    telegramGroupsAPI.create(input)
    telegramGroupsAPI.patch(1, input)
    telegramGroupsAPI.check(1, 3)
    telegramGroupsAPI.modules(2)
    telegramGroupsAPI.saveModule(2, { partition: 'support', expected_version: 9, enabled: true, target_id: 1 })
    expect(mocks.get.mock.calls).toEqual([
      ['/admin/telegram-bot/groups', { params: { page: 2, page_size: 20, purpose: 'support', bot_instance_id: 2 } }],
      ['/admin/telegram-bot/groups/1'], ['/admin/telegram-bot/instances/2/modules'],
    ])
    expect(mocks.post.mock.calls).toEqual([
      ['/admin/telegram-bot/groups', creationInput, { timeoutMs: 30000 }],
      ['/admin/telegram-bot/groups/1/check', { expected_route_version: 3 }, { timeoutMs: 15000 }],
    ])
    expect(mocks.patch.mock.calls).toEqual([
      ['/admin/telegram-bot/groups/1', input, { timeoutMs: 30000 }],
      ['/admin/telegram-bot/instances/2/modules', { partition: 'support', expected_version: 9, enabled: true, target_id: 1 }],
    ])
    expect(input.enabled).toBe(true)
    expect(mocks.patch.mock.calls[0]?.[1].telegram_chat_id).toBe('-9223372036854775808')
  })
})
