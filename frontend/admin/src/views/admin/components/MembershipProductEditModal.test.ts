import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { adminUrl } from '@/utils/adminBase'
import MembershipProductEditModal from './MembershipProductEditModal.vue'
import { telegramMembershipMessages } from '@/i18n/telegramMembership'
const mocks = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), create: vi.fn(), update: vi.fn(), groups: vi.fn(), categories: vi.fn(), confirm: vi.fn(), auth: null as any }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { products: mocks.list, product: mocks.get, create: mocks.create, update: mocks.update } }))
vi.mock('@/api/admin', () => ({ adminAPI: { getCategories: mocks.categories, getTelegramBotInstances: async () => ({ data: { data: { items: [{ id: 1, username: 'first_bot' }, { id: 2, username: 'second_bot' }] } } }) } }))
vi.mock('@/api/telegramGroups', () => ({ telegramGroupsAPI: { list: mocks.groups } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/utils/confirm', () => ({ confirmAction: mocks.confirm }))
let app: App, root: HTMLDivElement, state: any
/** Drain rendering and API microtasks for the actual editor component. */
async function flush() { for (let index = 0; index < 10; index++) { await Promise.resolve(); await nextTick() } }
/** Mount using real shared UI components; keep the global theme external to the editor. */
function mountEditor() {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(MembershipProductEditModal)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramMembership: telegramMembershipMessages['zh-CN'] } } }))
  const instance = app.mount(root)
  state = (instance.$ as unknown as { setupState: any }).setupState
}
/** A complete existing DEFAULT fixture retains SKU price and CAS version. */
function existing() { return { id: 7, title: { 'zh-CN': '会员' }, slug: 'member', category_id: 1, description: {}, content: {},
  instructions: { 'zh-CN': '交付后使用说明' }, payment_channel_ids: '[2,5]',
  manual_form_schema: { fields: [{ key: 'account', type: 'text', label: { 'zh-CN': '账号' }, required: true }] },
  skus: [{ id: 9, sku_code: 'DEFAULT', price_amount: 28, cost_price_amount: 0, is_active: true, sort_order: 0, spec_values: {} }],
  membership: { product_id: 7, version: 3, enabled: false, quantity: 1, target_ids: [1], terms: [{ sku_id: 9, sku_code: 'DEFAULT', kind: 'months', months: 5 }] } } }
/** Provide authoritative ready evidence rather than bypassing the availability gate. @param overrides Fixture changes. @returns Group fixture. */
function readyGroup(overrides = {}) { return { id: 1, name: 'Group', purpose: 'membership', bot_instance_id: 1,
  enabled: true, recovery_task_id: 0, fresh: true, fresh_until: new Date(Date.now() + 300000).toISOString(),
  checking: false, route_version: 1, missing_capabilities: [], observation: { state: 'ready', error_class: '', route_version: 1 }, ...overrides } }
beforeEach(() => {
  vi.clearAllMocks()
  mocks.auth = reactive({ token: 'account-one', isSuper: false, permissions: [] as string[], denied: [] as string[], hasPermission(permission: string) { return !this.denied.includes(permission) } })
  mocks.categories.mockResolvedValue({ data: { data: [{ id: 1, name: { 'zh-CN': '会员' }, parent_id: 0 }] } })
  mocks.groups.mockResolvedValue({ data: { data: { items: [readyGroup()], total: 1, page: 1, page_size: 100 } } })
  mocks.get.mockResolvedValue({ data: { data: existing() } }); mocks.confirm.mockResolvedValue(true)
  mocks.create.mockResolvedValue({}); mocks.update.mockResolvedValue({})
  mountEditor()
})
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark') })

describe('membership product editor component', () => {
  it('searches and resets group choices without discarding product selections or submitting', async () => {
    await state.editor.edit(7); await flush(); mocks.groups.mockClear()
    state.botFilter = '2'; state.draft.skus[0].months = 18; await flush()
    expect(mocks.groups).not.toHaveBeenCalled()
    const toolbar = document.querySelector('[aria-labelledby="membership-groups-heading"]')!
    const search = [...toolbar.querySelectorAll('button')].find(button => button.textContent === 'admin.common.search')!
    const reset = [...toolbar.querySelectorAll('button')].find(button => button.textContent === 'admin.common.reset')!
    expect(search.type).toBe('button'); expect(reset.type).toBe('button')
    search.click(); await flush(); search.click(); await flush()
    expect(mocks.groups).toHaveBeenCalledTimes(2)
    reset.click(); await flush()
    expect(mocks.groups).toHaveBeenCalledTimes(3)
    expect(mocks.groups).toHaveBeenLastCalledWith({ page: 1, page_size: 100, purpose: 'membership' })
    expect(state.botFilter).toBe(''); expect(state.draft.target_ids).toEqual([1]); expect(state.draft.skus[0].months).toBe(18)
    state.groupsLoading = true; await flush()
    expect(search.disabled).toBe(true); expect(reset.disabled).toBe(true)
    await state.searchGroups(); await state.resetGroupFilters()
    expect(mocks.groups).toHaveBeenCalledTimes(3)
    expect(mocks.create).not.toHaveBeenCalled(); expect(mocks.update).not.toHaveBeenCalled()
  })
  it('shares table theme while retaining dense SKU fields and aligned outline actions', async () => {
    await state.editor.edit(7); await flush()
    const tables = [...document.querySelectorAll('[role="dialog"] table')]
    expect(tables).toHaveLength(2)
    for (const table of tables) {
      expect(table.closest('.rounded-xl')?.className).toContain('border border-border bg-card overflow-x-auto')
      expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
      expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
      expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
    }
    for (const cell of tables[0]!.querySelectorAll('th, td')) expect(cell.className).toContain('px-4 py-3')
    const skuTable = tables[1]!
    expect(skuTable.querySelector('td')?.className).toContain('p-2')
    expect(skuTable.querySelector('td')?.className).not.toContain('px-6')
    expect(skuTable.querySelector('th:last-child')?.className).toContain('text-right')
    expect(skuTable.querySelector('td:last-child')?.className).toContain('text-right')
    expect(skuTable.querySelector('td:last-child > div')?.className).toBe('flex flex-wrap items-center justify-end gap-2')
    expect(skuTable.querySelector('td:last-child button')?.className).toContain('border-input')
    expect(skuTable.querySelector('td:last-child button')?.className).toContain('h-8')
    expect(mocks.update).not.toHaveBeenCalled(); expect(mocks.create).not.toHaveBeenCalled()
  })
  it('preserves unknown draft through canceled close, reopen and read-only ID checks', async () => {
    await state.editor.edit(7); await flush()
    state.draft.skus[0].months = 18
    mocks.update.mockRejectedValue(Object.assign(new Error('请求超时'), { resultUnknown: true }))
    expect(await state.editor.save()).toBe(false)
    mocks.confirm.mockResolvedValue(false); await state.editor.close(); expect(state.open).toBe(true)
    mocks.confirm.mockResolvedValue(true); await state.editor.close(); expect(state.open).toBe(false)
    await state.editor.edit(); expect(state.open).toBe(true); expect(state.draft.skus[0].months).toBe(18)
    await state.editor.save(); await state.editor.recovery.check()
    expect(state.editor.recovery.candidate.value.id).toBe(7)
    expect(state.editor.recovery.unknown.value).toBe(true)
    expect(mocks.update).toHaveBeenCalledTimes(1); expect(mocks.create).not.toHaveBeenCalled()
    expect(state.draft.skus[0].months).toBe(18)
  })
  it('requires exact slug for create reconciliation and never retries a missing match', async () => {
    await state.editor.edit(); await flush()
    Object.assign(state.draft, { title: { 'zh-CN': '新会员' }, slug: 'exact', category_id: 1, target_ids: [1] })
    state.draft.skus[0].price_amount = 10
    mocks.create.mockRejectedValue(Object.assign(new Error('offline'), { resultUnknown: true }))
    await state.editor.save()
    mocks.list.mockResolvedValue({ data: { data: [{ id: 8, slug: 'exact-other' }] } })
    await state.editor.recovery.check()
    expect(state.editor.recovery.candidate.value).toBeUndefined()
    expect(mocks.list).toHaveBeenCalledWith(expect.objectContaining({ slug: 'exact', search: 'exact' }))
    mocks.list.mockResolvedValue({ data: { data: [{ id: 9, slug: 'exact' }] } })
    await state.editor.recovery.check(); await state.editor.save()
    expect(state.editor.recovery.candidate.value.id).toBe(9)
    expect(state.editor.recovery.notice.value).toContain('不代表本次提交成功')
    expect(mocks.create).toHaveBeenCalledTimes(1)
  })
  it('keeps the original draft when candidate navigation is canceled', async () => {
    await state.editor.edit(7); await flush()
    state.draft.skus[0].months = 18
    mocks.update.mockRejectedValue(Object.assign(new Error('timeout'), { resultUnknown: true }))
    await state.editor.save(); await state.editor.recovery.check()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    mocks.confirm.mockResolvedValue(false); await state.editor.recovery.openCandidate()
    expect(open).not.toHaveBeenCalled(); expect(state.draft.skus[0].months).toBe(18)
    mocks.confirm.mockResolvedValue(true); await state.editor.recovery.openCandidate()
    expect(open).toHaveBeenCalledWith(expect.stringContaining('/telegram-bot/membership/products?product_id=7'), '_blank', 'noopener,noreferrer')
    expect(state.editor.recovery.unknown.value).toBe(true); expect(state.draft.skus[0].months).toBe(18)
    open.mockRestore()
  })
  it('displays definite server validation message without locking as unknown', async () => {
    await state.editor.edit(7); await flush()
    mocks.update.mockRejectedValue(Object.assign(new Error('SKU编码重复'), { resultUnknown: false, response: { status: 422 } }))
    await state.editor.save()
    expect(state.error).toBe('SKU编码重复')
    expect(state.editor.recovery.unknown.value).toBe(false)
  })
  it('preserves DEFAULT, 18-month draft and price across global theme changes', async () => {
    await state.editor.edit(7); await flush()
    expect(state.draft.skus[0].sku_code).toBe('DEFAULT'); expect(state.draft.skus[0].months).toBe(5)
    state.draft.skus[0].months = 18; state.draft.skus[0].price_amount = 99
    document.documentElement.classList.add('dark'); await flush()
    expect(state.draft.skus[0].months).toBe(18)
    document.documentElement.classList.remove('dark'); await flush()
    expect(state.draft.skus[0].price_amount).toBe(99)
    expect(mocks.update).not.toHaveBeenCalled()
  })
  it('saves price and CAS configuration once, with no general product POST', async () => {
    await state.editor.edit(7); await flush()
    expect(await state.editor.save()).toBe(true)
    expect(mocks.update).toHaveBeenCalledTimes(1); expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.update.mock.calls[0]![1].membership.expected_version).toBe(3)
    expect(mocks.update.mock.calls[0]![1].skus[0].price_amount).toBe(28)
    expect(mocks.update.mock.calls[0]![1]).toMatchObject({
      instructions: existing().instructions, payment_channel_ids: [2, 5], manual_form_schema: existing().manual_form_schema,
      membership: { target_ids: [1] },
    })
  })
  it('preserves listed state when editing an already active product', async () => {
    const product = existing()
    mocks.get.mockResolvedValue({ data: { data: { ...product, is_active: true, membership: { ...product.membership, enabled: true } } } })
    await state.editor.edit(7); await flush()
    state.draft.skus[0].price_amount = 39
    expect(await state.editor.save()).toBe(true)
    expect(mocks.update).toHaveBeenCalledExactlyOnceWith(7, expect.objectContaining({
      is_active: true, membership: expect.objectContaining({ expected_version: 3, enabled: true }),
    }))
  })
  it.each(['price', 'term'])('preserves replacement-only product fields when changing only %s', async field => {
    await state.editor.edit(7); await flush()
    if (field === 'price') state.draft.skus[0].price_amount = 39
    else state.draft.skus[0].months = 18
    expect(await state.editor.save()).toBe(true)
    const input = mocks.update.mock.calls[0]![1]
    expect(input).toMatchObject({ instructions: existing().instructions, payment_channel_ids: [2, 5],
      manual_form_schema: existing().manual_form_schema, membership: { target_ids: [1] } })
    expect(input.skus[0].price_amount).toBe(field === 'price' ? 39 : 28)
    expect(input.membership.terms[0].months).toBe(field === 'term' ? 18 : 5)
    expect(input.manual_stock_total).toBeUndefined()
    expect(input.skus[0]).not.toHaveProperty('manual_stock_total')
  })
  it.each([0, -1, 0.004, NaN, Infinity])('blocks saving invalid rounded price %s', async price => {
    await state.editor.edit(7); await flush()
    state.draft.skus[0].price_amount = price; await flush()
    expect(state.valid).toBe(false)
    expect(await state.editor.save()).toBe(false)
    expect(mocks.update).not.toHaveBeenCalled()
    const priceInput = document.querySelector<HTMLInputElement>('input[aria-label="价格"]')
    expect(priceInput?.min).toBe('0.01')
    expect(priceInput?.step).toBe('0.01')
    expect(priceInput?.required).toBe(true)
  })
  it('409 preserves draft and blocks retry until explicit confirmed reload', async () => {
    await state.editor.edit(7); await flush()
    state.draft.skus[0].months = 18
    mocks.update.mockRejectedValue({ response: { status: 409 } })
    expect(await state.editor.save()).toBe(false); await flush()
    expect(state.conflict).toBe(true); expect(state.draft.skus[0].months).toBe(18)
    await state.editor.save(); expect(mocks.update).toHaveBeenCalledTimes(1)
    mocks.confirm.mockResolvedValue(false); await state.editor.reload(); expect(state.draft.skus[0].months).toBe(18)
    mocks.confirm.mockResolvedValue(true); await state.editor.reload(); await flush()
    expect(state.conflict).toBe(false); expect(state.draft.skus[0].months).toBe(5)
  })
  it('does not request unauthorized groups or save an unverifiable configuration', async () => {
    mocks.auth.denied = ['GET:/admin/telegram-bot/groups']
    await state.editor.edit(7); await flush()
    expect(mocks.groups).not.toHaveBeenCalled(); expect(await state.editor.save()).toBe(false)
    expect(mocks.update).not.toHaveBeenCalled()
  })
  it('clears drafts on account changes and rejects stale detail responses', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockReturnValue(new Promise(done => { resolve = done }))
    const pending = state.editor.edit(7)
    mocks.auth.token = 'account-two'; await flush()
    resolve({ data: { data: existing() } }); await pending; await flush()
    expect(state.open).toBe(false); expect(state.product).toBeUndefined(); expect(state.draft.title['zh-CN']).toBe('')
  })
  it('loads all groups using the existing nested pagination contract and rejects mixed Bots', async () => {
    mocks.groups.mockResolvedValueOnce({ data: { data: { items: [readyGroup()], total: 2, page: 1, page_size: 1 } } })
      .mockResolvedValueOnce({ data: { data: { items: [readyGroup({ id: 2, bot_instance_id: 2 })], total: 2, page: 2, page_size: 1 } } })
    await state.editor.edit(7); await flush()
    expect(mocks.groups).toHaveBeenCalledTimes(2)
    state.draft.target_ids = [1, 2]
    expect(await state.editor.save()).toBe(false)
    expect(mocks.update).not.toHaveBeenCalled()
  })
  it.each([
    ['disabled', { enabled: false }], ['expired', { fresh_until: '2000-01-01T00:00:00Z' }],
    ['missing permissions', { observation: { state: 'missing_permissions', error_class: '', route_version: 1 } }],
    ['anonymous', { observation: { state: 'missing_permissions', error_class: 'anonymous_admin', route_version: 1 } }],
    ['recovering', { recovery_task_id: 9 }],
  ])('blocks new %s bindings but permits removing selected invalid groups', async (_name, overrides) => {
    mocks.groups.mockResolvedValue({ data: { data: { items: [readyGroup(overrides)], total: 1, page: 1, page_size: 100 } } })
    await state.editor.edit(7); await flush()
    expect(state.draft.target_ids).toEqual([1]); expect(await state.editor.save()).toBe(false)
    expect(document.querySelector<HTMLButtonElement>('#membership-target-1')?.disabled).toBe(false)
    state.toggleTarget(1, false); expect(state.draft.target_ids).toEqual([])
    state.toggleTarget(1, true); expect(state.draft.target_ids).toEqual([])
    await flush(); expect(document.querySelector<HTMLButtonElement>('#membership-target-1')?.disabled).toBe(true)
    expect(mocks.update).not.toHaveBeenCalled()
  })
  it('keeps selected missing groups visible through filtering, refresh failures and retries', async () => {
    await state.editor.edit(7); state.draft.title['zh-CN'] = '草稿'; state.botFilter = '999'
    await flush(); expect(document.querySelector('#membership-target-1')).not.toBeNull()
    mocks.groups.mockRejectedValueOnce(new Error('offline')); await state.editor.loadGroups(); await flush()
    expect(state.draft.target_ids).toEqual([1]); expect(state.draft.title['zh-CN']).toBe('草稿')
    expect(await state.editor.save()).toBe(false)
    mocks.groups.mockResolvedValueOnce({ data: { data: { items: [], total: 0, page: 1, page_size: 100 } } })
    await state.editor.loadGroups(); await flush()
    expect(document.body.textContent).toContain('已选群缺失'); expect(state.draft.target_ids).toEqual([1])
    state.toggleTarget(1, false); expect(state.draft.target_ids).toEqual([])
  })
  it('expires permissions while open without losing the draft and links management in a new tab', async () => {
    app.unmount(); root.remove(); vi.useFakeTimers(); mountEditor()
    try {
      await state.editor.edit(7); await flush(); expect(state.valid).toBe(true)
      const link = document.querySelector<HTMLAnchorElement>(`a[href="${adminUrl('/telegram-bot/groups')}"]`)
      expect(link?.target).toBe('_blank'); expect(link?.rel).toContain('noopener')
      await vi.advanceTimersByTimeAsync(301000); await flush()
      expect(state.valid).toBe(false); expect(state.draft.target_ids).toEqual([1])
      expect(await state.editor.save()).toBe(false)
    } finally { vi.useRealTimers() }
  })
  it('keeps mixed Bot selections visible with nearby error even when filtered', async () => {
    mocks.groups.mockResolvedValue({ data: { data: { items: [readyGroup(), readyGroup({ id: 2, bot_instance_id: 2 })], total: 2, page: 1, page_size: 100 } } })
    await state.editor.edit(7); state.toggleTarget(2, true); state.botFilter = '99'; await flush()
    expect(state.visibleGroups).toHaveLength(2); expect(state.mixedBots).toBe(true)
    expect(document.querySelector('section [role="alert"]')?.textContent).toContain('同一 Bot')
    expect(await state.editor.save()).toBe(false)
    state.toggleTarget(2, false); expect(state.valid).toBe(true)
  })
  it('checks write permission again when saving', async () => {
    await state.editor.edit(7); await flush()
    mocks.auth.denied = ['PUT:/admin/telegram-bot/membership/products/7']
    expect(await state.editor.save()).toBe(false); expect(mocks.update).not.toHaveBeenCalled()
  })
})
