import { describe, expect, it, vi } from 'vitest'
import { telegramMembershipMessages } from '@/i18n/telegramMembership'
import { membershipDraft, membershipPayload, validMembershipDraft } from '@/utils/membershipProduct'
import { telegramMembershipAPI, type MembershipProduct, type MembershipLifecycleStatus } from './telegramMembership'
import { parsePaymentChannelIDs } from '@/utils/paymentChannelDisplay'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))

describe('membership paginated evidence contract', () => {
  it.each(['9007199254740993', '@Seven_Time', 'seven_time'])('forwards review search %s without changing identity precision', keyword => {
    mocks.get.mockClear()
    const params = { bot_id: '9', keyword, page: 2, page_size: 20 }
    telegramMembershipAPI.accessReviews(params)
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/access/reviews', { params })
    mocks.get.mockClear()
  })
  it('forwards order evidence and task order number without client-side inference', () => {
    mocks.get.mockClear()
    const filters = { page: 3, page_size: 20, rights: 'pending' as const, member_removal: 'none' as const, invite_cleanup: 'failed' as const }
    telegramMembershipAPI.orders(filters)
    telegramMembershipAPI.accessTasks({ bot_id: '9', page: 2, page_size: 50, order_no: 'ORDER-1' })
    expect(mocks.get.mock.calls).toEqual([
      ['/admin/telegram-bot/membership/orders', { params: filters }],
      ['/admin/telegram-bot/membership/access/tasks', { params: { bot_id: '9', page: 2, page_size: 50, order_no: 'ORDER-1' } }],
    ])
    mocks.get.mockClear()
  })
})
describe('membership lifecycle contract', () => {
  it.each(['9007199254740993', '@Seven_Time', 'seven_time'])('passes member keyword %s without numeric conversion', keyword => {
    mocks.get.mockClear()
    telegramMembershipAPI.lifecycleMembers({ bot_id: '9', keyword, page: 1, page_size: 20 })
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/lifecycle/tasks', { params: { bot_id: '9', keyword, page: 1, page_size: 20, group_by: 'telegram_id' } })
    mocks.get.mockClear()
  })
  it.each(['', 'processing', 'needs_review', 'completed'] satisfies MembershipLifecycleStatus[])('groups members through the existing route for status %s', status => {
    mocks.get.mockClear()
    const params = { bot_id: '9007199254740993', page: 3, page_size: 20, status }
    telegramMembershipAPI.lifecycleMembers(params)
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/lifecycle/tasks', { params: { ...params, group_by: 'telegram_id' } })
    expect(params).not.toHaveProperty('group_by')
    mocks.get.mockClear()
  })
  it.each(['', 'processing', 'needs_review', 'completed'] satisfies MembershipLifecycleStatus[])('passes numbered pagination and status %s without client-side filtering', status => {
    mocks.get.mockClear()
    const params = { bot_id: '9007199254740993', page: 3, page_size: 20, status }
    telegramMembershipAPI.lifecycleTasks(params)
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/lifecycle/tasks', { params })
    mocks.get.mockClear()
  })
  it.each(['recheck', 'retry', 'block_ban'])('keeps the existing recover action %s and version evidence', action => {
    mocks.post.mockClear()
    telegramMembershipAPI.recoverLifecycle('9', '17', { action, expected_version: '9007199254740993' })
    expect(mocks.post).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/lifecycle/tasks/17/recover', { action, expected_version: '9007199254740993' }, { params: { bot_id: '9' } })
    mocks.post.mockClear()
  })
})
describe('membership product contract', () => {
  it('generates a persistent share code through the dedicated POST, never a read', () => {
    mocks.post.mockClear()
    telegramMembershipAPI.share(7)
    expect(mocks.post).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/products/7/share', {})
    mocks.post.mockClear()
  })
  it.each([2, 5, 18])('supports custom %i months on DEFAULT with one atomic POST', months => {
    mocks.post.mockClear()
    const draft = membershipDraft()
    Object.assign(draft, { title: { 'zh-CN': '套餐' }, slug: 'membership', category_id: 1, target_ids: [1, 2] })
    draft.skus[0]!.months = months
    draft.skus[0]!.price_amount = 12.34
    expect(validMembershipDraft(draft)).toBe(true)
    const input = membershipPayload(draft)
    telegramMembershipAPI.create(input)
    expect(mocks.post).toHaveBeenCalledTimes(1)
    expect(mocks.post).toHaveBeenCalledWith('/admin/telegram-bot/membership/products', input, { timeoutMs: 30000 })
    expect(input.membership).toEqual({ expected_version: 0, enabled: false, quantity: 1, target_ids: [1, 2], terms: [{ sku_code: 'DEFAULT', kind: 'months', months }] })
    expect((input.skus as any[])[0].price_amount).toBe(12.34)
    expect(input.membership).not.toHaveProperty('bot_instance_id')
  })
  it('permanent uses zero months and no synthetic expiry', () => {
    const draft = membershipDraft(); draft.skus[0]!.kind = 'permanent'; draft.skus[0]!.months = 18
    expect(membershipPayload(draft).membership.terms[0]).toEqual({ sku_code: 'DEFAULT', kind: 'permanent', months: 0 })
  })
  it.each([0, -1, 1.5, 3559, NaN, Infinity])('rejects invalid month value %s', months => {
    const draft = membershipDraft(); Object.assign(draft, { title: { 'en-US': 'Plan' }, slug: 'plan', category_id: 1, target_ids: [1] })
    draft.skus[0]!.months = months
    draft.skus[0]!.price_amount = 1
    expect(validMembershipDraft(draft)).toBe(false)
  })
  it.each([
    [0, false], [-1, false], [0.004, false], [0.004999999999, false],
    [0.005, true], [0.009, true], [0.01, true], [12.34, true], [NaN, false], [Infinity, false],
  ])('validates price %s against backend two-decimal positive rounding', (price, valid) => {
    const draft = membershipDraft()
    Object.assign(draft, { title: { 'en-US': 'Plan' }, slug: 'plan', category_id: 1, target_ids: [1] })
    draft.skus[0]!.price_amount = price as number
    expect(validMembershipDraft(draft)).toBe(valid)
  })
  it('copies target_ids from the real DTO without mutating the response', () => {
    const product = { membership: { version: 3, target_ids: [2, 5], terms: [] } } as unknown as MembershipProduct
    const draft = membershipDraft(product)
    expect(draft.target_ids).toEqual([2, 5])
    draft.target_ids.push(7)
    expect(product.membership.target_ids).toEqual([2, 5])
  })
  it.each([
    ['[2,5]', [2, 5]], [[2, 5], [2, 5]], ['[2,"5",0,-1,null]', [2]],
    ['[]', []], ['', []], [null, []], [undefined, []], ['invalid', []], ['{}', []],
    [[2, Infinity, NaN, -1], [2]],
  ])('normalizes existing product payment channels %s', (raw, expected) => {
    expect(parsePaymentChannelIDs(raw as string | number[] | null | undefined)).toEqual(expected)
  })
  it('sets explicit empty preserved fields for new products', () => {
    expect(membershipPayload(membershipDraft())).toMatchObject({ instructions: {}, payment_channel_ids: [], manual_form_schema: {} })
  })
  it.each([true, false])('preserves the authoritative listing and config flags when editing (%s)', active => {
    const product = { is_active: active, membership: { version: 4, enabled: active, target_ids: [1], terms: [] } } as unknown as MembershipProduct
    const input = membershipPayload(membershipDraft(product), product)
    expect(input.is_active).toBe(active)
    expect(input.membership.enabled).toBe(active)
    expect(input.membership.expected_version).toBe(4)
  })
  it('uses only scoped endpoints', () => {
    telegramMembershipAPI.products({ page: 2 }); telegramMembershipAPI.product(7)
    telegramMembershipAPI.update(7, membershipPayload(membershipDraft()))
    telegramMembershipAPI.orders({ page: 1 }); telegramMembershipAPI.order(9)
    expect(mocks.get.mock.calls).toEqual([
      ['/admin/telegram-bot/membership/products', { params: { page: 2 } }], ['/admin/telegram-bot/membership/products/7'],
      ['/admin/telegram-bot/membership/orders', { params: { page: 1 } }], ['/admin/telegram-bot/membership/orders/9'],
    ])
    expect(mocks.put.mock.calls[0]?.[2]).toEqual({ timeoutMs: 30000 })
    expect(mocks.put.mock.calls[0]?.[0]).toBe('/admin/telegram-bot/membership/products/7')
  })
  it.each([true, false])('passes listing state %s and expected version to the dedicated PATCH endpoint', active => {
    mocks.patch.mockClear()
    telegramMembershipAPI.setActive(7, active, 3)
    expect(mocks.patch).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/products/7', { is_active: active, expected_version: 3 })
  })
  it('passes expected version to the dedicated DELETE endpoint', () => {
    telegramMembershipAPI.remove(7, 3)
    expect(mocks.delete).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/membership/products/7', { params: { expected_version: 3 } })
  })
  it('has complete independent translations and correct titles', () => {
    const keys = Object.keys(telegramMembershipMessages['zh-CN']).sort()
    for (const locale of ['zh-TW', 'en-US'] as const) expect(Object.keys(telegramMembershipMessages[locale]).sort()).toEqual(keys)
    expect(telegramMembershipMessages['zh-CN'].products).toBe('会员群_商品管理')
    expect(telegramMembershipMessages['zh-CN'].orders).toBe('会员群_订单管理')
  })
})
