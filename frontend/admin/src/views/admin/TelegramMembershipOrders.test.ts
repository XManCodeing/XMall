import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import TelegramMembershipOrders from './TelegramMembershipOrders.vue'
import { telegramMembershipMessages } from '@/i18n/telegramMembership'
const mocks = vi.hoisted(() => ({ copy: vi.fn(), success: vi.fn(), failure: vi.fn(), list: vi.fn(), detail: vi.fn(), payments: vi.fn(), refreshAttention: vi.fn().mockResolvedValue(undefined), attention: null as any, auth: null as any }))
vi.mock('@/composables/useMembershipAttention', () => ({ useMembershipAttention: () => ({
  states: mocks.attention || reactive({ tasks: { count: 0, loading: false, error: false }, reviews: { count: 0, loading: false, error: false }, lifecycle: { count: 0, loading: false, error: false } }),
  refresh: mocks.refreshAttention,
}) }))
vi.mock('@/api/admin', () => ({ adminAPI: { getPayments: mocks.payments } }))
vi.mock('@/components/telegram/TelegramBotSelect.vue', () => ({ default: {
  props: ['modelValue'], emits: ['update:modelValue'],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option value="">Select</option><option value="19">Bot 19</option></select>',
} }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { orders: mocks.list, order: mocks.detail } }))
vi.mock('@/utils/clipboard', () => ({ copyText: mocks.copy }))
vi.mock('@/utils/notify', () => ({ notifySuccess: mocks.success, notifyErrorOnce: mocks.failure }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
/** Drain DOM and API updates. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.clearAllMocks(); mocks.attention = null })
describe('membership order component', () => {
  it('sends all evidence filters to server pagination and ignores late responses after a filter change', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: (permission: string) => permission.endsWith('/orders') })
    mocks.list.mockResolvedValue({ data: { data: [], pagination: { page: 1, page_size: 20, total: 80, total_page: 4 } } })
    root = document.createElement('div'); document.body.append(root); app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
    await flush()
    state.evidenceFilters = { rights: 'pending', member_removal: 'failed', invite_cleanup: 'none' }
    await state.searchOrders(); await state.fetchOrders(3)
    expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ page: 3, rights: 'pending', member_removal: 'failed', invite_cleanup: 'none' }))
    expect(root.querySelectorAll('[role="combobox"][aria-label^="membershipOrderState."]')).toHaveLength(3)
    let resolve!: (value: unknown) => void
    mocks.list.mockReturnValueOnce(new Promise(accept => { resolve = accept }))
    const request = state.fetchOrders(4)
    state.evidenceFilters.rights = 'none'; await flush()
    resolve({ data: { data: [{ id: 99, order_no: 'STALE' }] } }); await request; await flush()
    expect(state.orders).toEqual([]); expect(state.pagination.page).toBe(1)
    await state.resetFilters()
    expect(state.evidenceFilters).toEqual({ rights: 'all', member_removal: 'all', invite_cleanup: 'all' })
    expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1, rights: undefined, member_removal: undefined, invite_cleanup: undefined }))
  })
  it('truncates each user line and exposes full registered and guest identities on hover', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: (permission: string) => permission.endsWith('/orders') })
    const name = 'Long membership customer display name'
    const email = 'long-membership-customer@example.test'
    mocks.list.mockResolvedValue({ data: { data: [
      { id: 3, order_no: 'LONG-MEMBERSHIP-ORDER-123456789', status: 'paid', user_id: 7, user_display_name: name, user_email: email, items: [{ id: 1, title: { 'zh-CN': '超长会员商品名称完整展示测试' }, quantity: 2 }] },
      { id: 4, order_no: 'GUEST', status: 'paid', guest_email: email, items: [] },
    ] } })
    root = document.createElement('div'); document.body.append(root); app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    app.mount(root); await flush()
    const cells = root.querySelectorAll('tbody tr')[0]!.querySelectorAll('td')
    const orderNumber = cells[0]!.querySelector('.truncate')!
    expect(orderNumber.getAttribute('title')).toBe('LONG-MEMBERSHIP-ORDER-123456789')
    expect(cells[0]!.querySelector('button')?.classList.contains('shrink-0')).toBe(true)
    const product = cells[1]!.querySelector('.truncate')!
    expect(product.getAttribute('title')).toBe('超长会员商品名称完整展示测试 x2')
    expect(cells[1]!.classList.contains('max-w-[240px]')).toBe(true)
    const userCell = cells[2]!
    expect(userCell.classList.contains('max-w-[200px]')).toBe(true)
    const lines = userCell.querySelectorAll('.truncate')
    expect([...lines].map(line => line.getAttribute('title'))).toEqual([name, email])
    expect(userCell.querySelector('a')?.getAttribute('href')).toContain('/users/7')
    const guestLine = root.querySelectorAll('tbody tr')[1]!.querySelectorAll('td')[2]!.querySelector('.truncate')!
    expect(guestLine.getAttribute('title')).toContain(email)
    expect(guestLine.textContent).toBe(guestLine.getAttribute('title'))
  })
  it('renders explicit none without badges and keeps missing or unknown evidence unconfirmed', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: (permission: string) => permission.endsWith('/orders') })
    mocks.list.mockResolvedValue({ data: { data: [{ id: 3, order_no: 'TEST', status: 'paid', items: [], membership_groups: [{ item_id: 1, target_id: 2, rights: 'none', member_removal: 'none', invite_cleanup: 'none' }] }] } })
    root = document.createElement('div'); document.body.append(root); app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
    await flush()
    for (const index of [6, 7, 8]) {
      const cell = root.querySelectorAll('tbody tr:first-child td')[index]!
      expect(cell.textContent).toBe('membershipOrderState.none'); expect(cell.querySelector('.bg-success')).toBeNull()
    }
    state.orders[0].membership_groups[0] = { item_id: 1, target_id: 2, rights: 'pending', member_removal: 'unknown', invite_cleanup: '' }
    await flush()
    const cells = root.querySelectorAll('tbody tr:first-child td')
    expect(cells[6]?.textContent).toContain('membershipOrderState.rightsPending')
    for (const index of [7, 8]) {
      expect(cells[index]?.textContent).toContain(index === 7 ? 'membershipOrderState.unknown' : 'membershipOrderState.unconfirmed')
      expect(cells[index]?.querySelector('.bg-success, .bg-destructive')).toBeNull()
    }
  })
  it('renders expiry timestamps only and removes internal target prefixes from all status cells', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: (permission: string) => permission === 'GET:/admin/telegram-bot/membership/orders' })
    const group = { item_id: 1, target_id: 234, beneficiary_telegram_id: '9007199254740993', rights: 'active', member_removal: 'failed', invite_cleanup: 'processing' }
    mocks.list.mockResolvedValue({ data: { data: [{ id: 3, order_no: 'MEMBER-3', status: 'paid', total_amount: 18, items: [], membership_groups: [
      { ...group, expires_at: '2026-11-04T23:04:05Z' },
      { ...group, target_id: 235, permanent: true },
      { ...group, target_id: 236 },
    ] }] } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
    await flush()
    const cells = root.querySelectorAll('tbody tr:first-child td')
    expect(cells[5]?.textContent?.trim()).toBe('2026-11-05 07:04:05')
    for (const index of [5, 6, 7, 8]) {
      expect(cells[index]?.textContent).not.toContain('#')
      expect(cells[index]?.textContent).not.toContain('234')
      expect(cells[index]?.textContent).not.toContain('9007199254740993')
    }
    state.orders[0].membership_groups = []
    await flush()
    expect(root.querySelectorAll('tbody tr:first-child td')[5]?.textContent?.trim()).toBe('')
  })
  it('uses semantic status badges and keeps a loading-safe footer for empty results', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: (permission: string) => permission === 'GET:/admin/telegram-bot/membership/orders' })
    const order = { id: 3, order_no: 'MEMBER-3', status: 'paid', currency: 'CNY', total_amount: 18, items: [], membership_groups: [{ item_id: 1, target_id: 2, rights: 'active', member_removal: 'failed', invite_cleanup: 'processing' }] }
    mocks.list.mockResolvedValue({ data: { data: [order], pagination: { page: 1, page_size: 20, total: 30, total_page: 2 } } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
    await flush()
    expect(root.querySelector('tbody .bg-success')?.textContent).toBe('order.status.paid')
    expect(root.querySelector('tbody .bg-destructive')?.textContent).toBe('membershipOrderState.removalFailed')
    expect(root.querySelector('tbody .text-warning')?.textContent).toBe('membershipOrderState.unconfirmed')
    const next = () => [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === 'admin.common.nextPage')!
    expect(next().disabled).toBe(false)
    state.loading = true; await flush(); expect(next().disabled).toBe(true)
    state.loading = false; state.orders = []; state.pagination.total = 0; state.pagination.total_page = 0
    await flush(); expect(next().disabled).toBe(true)
  })
  it('uses four local tabs with truthful attention badges and preserved filters', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
    mocks.attention = reactive({ tasks: { count: 7, loading: false, error: false }, reviews: { count: null, loading: true, error: false }, lifecycle: { count: 0, loading: false, error: true } })
    mocks.list.mockResolvedValue({ data: { data: [] } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramMembership: telegramMembershipMessages['zh-CN'] } } }))
    const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
    await flush()
    expect(root.querySelector('h1')).toBeNull()
    expect(root.querySelectorAll('[role="tab"]')).toHaveLength(4)
    expect(root.querySelector('[role="tablist"]')?.textContent).toContain('7')
    expect(root.querySelector('[role="tablist"]')?.textContent).toContain('待检查')
    expect(root.querySelector('[role="tablist"]')?.textContent).not.toContain('查询失败')
    expect(root.querySelector('[role="tablist"] [aria-label="查询失败"]')?.textContent).toBe('—')
    state.orderNo = 'keep-draft'; state.currentTab = 'tasks'; await flush()
    const taskInput = root.querySelector('[role="tabpanel"][data-state="active"] select') as HTMLSelectElement
    taskInput.value = '19'; taskInput.dispatchEvent(new Event('change', { bubbles: true })); await flush()
    state.currentTab = 'reviews'; await flush()
    expect((root.querySelector('[role="tabpanel"][data-state="active"] select') as HTMLSelectElement).value).toBe('')
    state.currentTab = 'tasks'; await flush()
    expect((root.querySelector('[role="tabpanel"][data-state="active"] select') as HTMLSelectElement).value).toBe('19')
    document.documentElement.classList.add('dark')
    state.currentTab = 'orders'; await flush(); expect(state.orderNo).toBe('keep-draft')
    await state.searchOrders(); expect(mocks.refreshAttention).toHaveBeenCalled()
    state.currentTab = 'tasks'; await flush()
    mocks.auth.hasPermission = (permission: string) => permission.endsWith('/access/reviews')
    await flush()
    expect(root.querySelectorAll('[role="tab"]')).toHaveLength(1)
    expect(state.currentTab).toBe('reviews')
    expect(root.querySelector('[role="tab"]')?.textContent).toContain('待核对申请')
  })
  it('makes no order request and exposes no tabs without list permissions', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => false })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'en-US', missingWarn: false, fallbackWarn: false, messages: {} }))
    app.mount(root); await flush()
    expect(mocks.list).not.toHaveBeenCalled()
    expect(root.querySelectorAll('[role="tab"]')).toHaveLength(0)
  })
  it('searches explicitly, repeats queries and resets all filters to page one', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
    mocks.list.mockResolvedValue({ data: { data: [] } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
    await flush()
    state.orderNo = 'DJ123'; state.telegramIdentity = '@member'; state.status = 'canceled'; state.pagination.page = 4
    await flush(); expect(mocks.list).toHaveBeenCalledTimes(1)
    await state.searchOrders()
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 20, user_keyword: '@member', order_no: 'DJ123', status: 'canceled' })
    await state.searchOrders(); expect(mocks.list).toHaveBeenCalledTimes(3)
    state.pagination.page = 4
    await state.resetFilters(); await flush()
    expect(state.telegramIdentity).toBe(''); expect(state.orderNo).toBe(''); expect(state.status).toBe('all'); expect(state.pagination.page).toBe(1)
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 20, user_keyword: undefined, order_no: undefined, status: undefined })
    expect(root.textContent).not.toContain('admin.common.refresh')
  })
  it('opens the shared receipt flow from a pending row and refreshes list and detail after confirmation', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
    const order = { id: 3, order_no: 'MEMBER-3', status: 'pending_payment', currency: 'CNY', total_amount: 18, items: [] }
    mocks.list.mockResolvedValue({ data: { data: [order] } })
    mocks.detail.mockResolvedValue({ data: { data: order } })
    mocks.payments.mockResolvedValue({ data: { data: [] } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
    const instance = app.mount(root), state = (instance.$ as unknown as { setupState: any }).setupState
    await flush()
    expect(root.querySelector('thead')?.textContent).not.toContain('admin.orders.table.id')
    const copy = root.querySelector('button[title="admin.common.copy"]') as HTMLButtonElement
    copy.click(); await flush()
    expect(mocks.copy).toHaveBeenCalledWith('MEMBER-3')
    expect(mocks.success).toHaveBeenCalledWith('admin.common.copied')
    mocks.copy.mockRejectedValueOnce(new Error('clipboard denied'))
    copy.click(); await flush(); expect(mocks.failure).toHaveBeenCalled()
    expect(root.querySelector('thead')?.className).toContain('bg-muted/40')
    const receipt = [...root.querySelectorAll('button')].find(button => button.textContent === 'tableAction.receipt')
    expect(receipt).toBeDefined()
    receipt!.click(); await flush()
    expect(state.detailOpen).toBe(false)
    expect(mocks.detail).toHaveBeenCalledWith(3)
    expect(mocks.payments).toHaveBeenCalledWith({ order_id: 3, page: 1, page_size: 20 })
    mocks.list.mockResolvedValue({ data: { data: [{ ...order, status: 'paid' }] } })
    mocks.detail.mockResolvedValue({ data: { data: { ...order, status: 'paid' } } })
    await state.openDetail(3)
    await state.receiptConfirmed(); await flush()
    expect(state.detail.status).toBe('paid')
    expect(state.orders[0].status).toBe('paid')
    mocks.list.mockRejectedValueOnce(new Error('private-provider-diagnostic'))
    await state.refresh(); await flush()
    expect(state.orders[0].status).toBe('paid')
    expect(state.error).toBeTruthy()
    expect(document.body.textContent).not.toContain('private-provider-diagnostic')
  })
  it('clears previous ledger while loading and never displays raw API failures', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
    mocks.list.mockResolvedValue({ data: { data: [] } })
    let rejectRequest!: (reason: Error) => void
    mocks.detail.mockImplementation(() => new Promise((_resolve, reject) => { rejectRequest = reject }))
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramMembership: telegramMembershipMessages['zh-CN'] } } }))
    const instance = app.mount(root), state = (instance.$ as unknown as { setupState: any }).setupState
    await flush()
    const request = state.openDetail(3)
    await flush()
    expect(state.detailLoading).toBe(true); expect(state.detail).toBeUndefined()
    rejectRequest(new Error('synthetic-secret-token'))
    await request; await flush()
    expect(state.detailLoading).toBe(false); expect(state.detailError).toBeTruthy()
    expect(document.body.textContent).not.toContain('synthetic-secret-token')
    expect(state.detail).toBeUndefined()
  })
  it('shows immutable identity/term/targets and isolates another account', async () => {
    mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
    mocks.list.mockResolvedValue({ data: { data: [{ id: 3, order_no: 'snapshot-order', status: 'paid', total_amount: 18 }] } })
    mocks.detail.mockResolvedValue({ data: { data: { id: 3, order_no: 'snapshot-order', status: 'paid', total_amount: 18, items: [{ id: 1, title: { 'zh-CN': '会员' }, total_price: 18,
      membership_snapshot: { beneficiary_telegram_id: '9007199254740993', config_version: 4, kind: 'months', months: 18, targets: [{ target_id: 2, bot_instance_id: 1, route_version: 6, telegram_chat_id: '-1009007199254740993' }] },
    }] } } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(TelegramMembershipOrders)
    app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramMembership: telegramMembershipMessages['zh-CN'] } } }))
    const instance = app.mount(root), state = (instance.$ as unknown as { setupState: any }).setupState
    await flush(); await state.openDetail(3); await flush()
    expect(document.body.textContent).toContain('9007199254740993')
    expect(document.body.textContent).toContain('18 月数')
    expect(document.body.textContent).toContain('不代表权益已到账')
    expect(mocks.detail).toHaveBeenCalledWith(3)
    document.documentElement.classList.add('dark'); await flush()
    expect(state.detail.order_no).toBe('snapshot-order')
    expect(document.body.textContent).toContain('暂无入账来源')
    expect(document.body.textContent).not.toContain('退款操作')
    mocks.auth.token = 'second'; await flush()
    expect(state.detail).toBeUndefined(); expect(state.orders).toEqual([]); expect(state.detailOpen).toBe(false)
  })
})
