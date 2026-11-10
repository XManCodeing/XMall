import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Products from './TelegramMembershipProducts.vue'
import Orders from './TelegramMembershipOrders.vue'
import Recovery from './components/MembershipAccessRecovery.vue'
import Disposition from './components/MembershipDisposition.vue'
import { useMembershipAttention } from '@/composables/useMembershipAttention'
import i18n from '@/i18n'

const mocks = vi.hoisted(() => ({
  products: vi.fn(), orders: vi.fn(), tasks: vi.fn(), reviews: vi.fn(), lifecycle: vi.fn(), members: vi.fn(),
  refunds: vi.fn(), remove: vi.fn(), show: vi.fn(), auth: null as any,
}))
vi.mock('@/stores/notice', () => ({ useNoticeStore: () => ({ show: mocks.show }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('@/utils/confirm', () => ({ confirmAction: async () => true }))
vi.mock('./components/MembershipProductEditModal.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/telegram/TelegramBotSelect.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: {
  products: mocks.products, orders: mocks.orders, accessTasks: mocks.tasks, accessReviews: mocks.reviews,
  lifecycleTasks: mocks.lifecycle, lifecycleMembers: mocks.members, refunds: mocks.refunds, remove: mocks.remove,
} }))
let app: App | undefined, root: HTMLDivElement
let clock = Date.now()

/** 等待页面真实渲染与请求结束。@returns 微任务完成。 */
async function flush(): Promise<void> {
  for (let turn = 0; turn < 12; turn++) { await Promise.resolve(); await nextTick() }
}
/** 挂载真实页面与通知函数。@param component 组件。@param props 组件参数。@returns setup 状态。 */
async function mount(component: any, props: Record<string, unknown> = {}): Promise<any> {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(component, props)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false,
    messages: { 'zh-CN': i18n.global.getLocaleMessage('zh-CN') } }))
  const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
  await flush()
  return state
}
/** 模拟 HTTP 拦截器已完成通知的原始错误。@returns 带原标记的错误对象。 */
function apiFailure(): Error & { __notified: boolean } {
  mocks.show('服务端失败', 'error')
  return Object.assign(new Error('服务端失败'), { __notified: true })
}
/** 验证请求失败未重复渲染错误或成功空态。@returns 无。 */
function expectQuietFailure(): void {
  expect(root.querySelector('[role="alert"]')).toBeNull()
  expect(root.textContent).not.toContain('服务端失败')
  expect(root.textContent).not.toContain('暂无数据')
}
beforeEach(() => {
  vi.resetAllMocks()
  // Move past the shared notifier's time window; assertions exercise real once-only behavior.
  clock += 3000
  vi.spyOn(Date, 'now').mockReturnValue(clock)
  mocks.auth = reactive({ token: 'test', isSuper: false, permissions: [],
    hasPermission: (key: string) => key.includes('/products') || key.includes('/orders') })
  mocks.products.mockResolvedValue({ data: { data: [], pagination: { page: 1, page_size: 10, total: 0, total_page: 0 } } })
  mocks.orders.mockResolvedValue({ data: { data: [] } })
  for (const reader of [mocks.tasks, mocks.reviews, mocks.lifecycle]) reader.mockResolvedValue({ data: { data: { attention_count: 0, items: [], total: 0 } } })
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => { app?.unmount(); app = undefined; root?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('membership failure presentation regression', () => {
  it('keeps a product API failure in the global toast and recovers to a real empty state', async () => {
    mocks.products.mockRejectedValueOnce(apiFailure())
    const state = await mount(Products)
    expectQuietFailure(); expect(state.error).toBeTruthy(); expect(mocks.show).toHaveBeenCalledTimes(1)
    await state.fetchProducts(); await flush()
    expect(state.error).toBe(''); expect(root.textContent).toContain('暂无数据')
  })
  it('notifies malformed product lists locally without rendering an error or false empty row', async () => {
    mocks.products.mockResolvedValueOnce({ data: { data: null } })
    const state = await mount(Products)
    expectQuietFailure(); expect(state.error).toBeTruthy(); expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('preserves the original refresh exception and its notification marker, retaining order rows', async () => {
    const state = await mount(Orders)
    state.orders = [{ id: 8, order_no: 'KEEP', status: 'paid', items: [] }]
    const failure = apiFailure(); mocks.orders.mockRejectedValue(failure)
    await expect(state.fetchOrders(1, { preserveRows: true })).rejects.toBe(failure)
    await state.refresh(); await flush()
    expect(state.orders[0].order_no).toBe('KEEP'); expectQuietFailure()
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('notifies a malformed order refresh exactly once and keeps unknown results out of the empty state', async () => {
    const state = await mount(Orders)
    mocks.orders.mockResolvedValue({ data: { data: {} } })
    await state.refresh(); await flush()
    expect(state.error).toBeTruthy(); expectQuietFailure(); expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it.each(['tasks', 'reviews'])('removes duplicate %s tab failures but preserves retry and empty-state recovery', async mode => {
    mocks.auth.hasPermission = () => true
    const reader = mode === 'tasks' ? mocks.tasks : mocks.reviews
    const state = await mount(Recovery, { mode }); state.bot = '9'
    reader.mockRejectedValueOnce(apiFailure())
    await state.load(); await flush()
    expectQuietFailure(); expect(state.error).toBeTruthy(); expect(mocks.show).toHaveBeenCalledTimes(1)
    await state.load(); await flush()
    expect(state.error).toBe(''); expect(root.textContent).toContain('暂无数据')
  })
  it('reports malformed tab pages locally without accepting missing items as empty', async () => {
    mocks.auth.hasPermission = () => true
    const state = await mount(Recovery, { mode: 'tasks' }); state.bot = '9'
    mocks.tasks.mockResolvedValue({ data: { data: { total: 0 } } })
    await state.load(); await flush()
    expectQuietFailure(); expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('cleans lifecycle tab failures without altering refund dialog errors', async () => {
    mocks.auth.hasPermission = () => true
    const state = await mount(Disposition, { embedded: true }); state.bot = '9'
    mocks.members.mockRejectedValueOnce(apiFailure())
    await state.load(); await flush()
    expectQuietFailure(); expect(mocks.show).toHaveBeenCalledTimes(1)
    app?.unmount(); app = undefined; root.remove()
    const refund = await mount(Disposition, { orderId: 8 })
    mocks.refunds.mockRejectedValueOnce(new Error('refund failure'))
    await refund.load(); await flush()
    expect(root.querySelector('[role="alert"]')?.textContent).toContain('请求失败')
  })
  it('rejects malformed lifecycle pages and retains the unknown internal state', async () => {
    mocks.auth.hasPermission = () => true
    const state = await mount(Disposition, { embedded: true }); state.bot = '9'
    mocks.members.mockResolvedValueOnce({ data: { data: { total: 0 } } })
    await state.load(); await flush()
    expectQuietFailure(); expect(state.error).toBeTruthy(); expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('keeps failed summary counts null and uses the original toast rather than reporting zero', async () => {
    mocks.auth.hasPermission = () => true
    mocks.tasks.mockRejectedValueOnce(apiFailure())
    let attention!: ReturnType<typeof useMembershipAttention>
    await mount({ setup() { attention = useMembershipAttention(); return () => null } })
    expect(attention.states.tasks).toEqual({ count: null, loading: false, error: true })
    expect(mocks.show).toHaveBeenCalledTimes(1)
    await attention.refresh()
    expect(attention.states.tasks).toEqual({ count: 0, loading: false, error: false })
  })
  it('notifies an invalid summary locally and never converts it into a zero badge', async () => {
    mocks.auth.hasPermission = () => true
    mocks.tasks.mockResolvedValueOnce({ data: { data: {} } })
    let attention!: ReturnType<typeof useMembershipAttention>
    await mount({ setup() { attention = useMembershipAttention(); return () => null } })
    expect(attention.states.tasks).toEqual({ count: null, loading: false, error: true })
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('keeps an initial order API failure out of both inline errors and successful empty states', async () => {
    mocks.orders.mockRejectedValueOnce(apiFailure())
    const state = await mount(Orders)
    expectQuietFailure(); expect(state.error).toBeTruthy()
    expect(root.textContent).not.toContain(i18n.global.t('admin.orders.empty'))
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('retains unknown write safety notices and disabled replay without repeating the API text', async () => {
    const product = { id: 7, slug: 'vip', title: {}, skus: [], is_active: false,
      membership: { version: 3, enabled: false, target_ids: [], terms: [] } }
    mocks.products.mockResolvedValue({ data: { data: [product], pagination: { page: 1, page_size: 10, total: 1, total_page: 1 } } })
    const state = await mount(Products)
    mocks.remove.mockRejectedValueOnce(apiFailure())
    await state.operations.operate(product, 'remove'); await flush()
    expect(root.textContent).toContain('操作结果未知')
    expect(root.textContent).not.toContain('服务端失败')
    expect(state.operations.blocked(product)).toBe(true)
    await state.operations.operate(product, 'remove')
    expect(mocks.remove).toHaveBeenCalledTimes(1); expect(mocks.show).toHaveBeenCalledTimes(1)
  })
})
