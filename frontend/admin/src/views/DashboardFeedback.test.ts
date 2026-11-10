import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Dashboard from './Dashboard.vue'
import ResellerOperationsDashboard from './admin/ResellerOperationsDashboard.vue'

const mocks = vi.hoisted(() => ({
  overview: vi.fn(), trends: vi.fn(), rankings: vi.fn(), inventory: vi.fn(),
  reseller: vi.fn(), finance: vi.fn(), notify: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: {
  getDashboardOverview: mocks.overview, getDashboardTrends: mocks.trends,
  getDashboardRankings: mocks.rankings, getDashboardInventoryAlerts: mocks.inventory,
  getResellerOperationsOverview: mocks.reseller, getResellerOperationsFinance: mocks.finance,
} }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: mocks.notify }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => true }) }))
let app: App | undefined
let root: HTMLDivElement | undefined
interface DashboardState {
  dashboardError?: string
  pageError?: string
  filters: { range: string; from: string; to: string }
  loadDashboard?: () => Promise<void>
  loadAll?: () => Promise<void>
}
/** 等待组件异步请求及 DOM 更新。@returns 所有当前微任务处理完成。 */
async function flush(): Promise<void> {
  for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() }
}
/** 挂载真实概览页。@param component 待测页面。@returns 页面查询状态与处理函数。 */
async function mount(component: typeof Dashboard | typeof ResellerOperationsDashboard): Promise<DashboardState> {
  const response = { data: { data: null } }
  for (const request of [mocks.overview, mocks.trends, mocks.rankings, mocks.reseller, mocks.finance]) request.mockResolvedValue(response)
  mocks.inventory.mockResolvedValue({ data: { data: [] } })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(component)
  app.component('router-link', { template: '<a><slot /></a>' })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
  const state = (app.mount(root).$ as unknown as { setupState: DashboardState }).setupState
  await flush()
  mocks.notify.mockClear()
  return state
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })
describe('dashboard request error presentation', () => {
  it('routes dashboard load failures to toast without duplicating them in the page', async () => {
    const state = await mount(Dashboard)
    for (const request of [mocks.overview, mocks.trends, mocks.rankings]) {
      const failure = Object.assign(new Error('dashboard failure marker'), { __notified: true })
      mocks.notify.mockClear(); request.mockRejectedValueOnce(failure)
      await state.loadDashboard!(); await flush()
      expect(mocks.notify).toHaveBeenCalledExactlyOnceWith(failure, 'admin.dashboard.errors.fetchFailed')
      expect(state.dashboardError).toBe('')
      expect(root?.textContent).not.toContain(failure.message)
    }
    state.filters.range = 'custom'; state.filters.from = ''; state.filters.to = ''
    mocks.notify.mockClear()
    await state.loadDashboard!(); await flush()
    expect(state.dashboardError).toBe('admin.dashboard.errors.customRangeRequired')
    expect(mocks.notify).not.toHaveBeenCalled()
  })
  it('keeps reseller query validation inline but not overview or finance request errors', async () => {
    const state = await mount(ResellerOperationsDashboard)
    for (const request of [mocks.reseller, mocks.finance]) {
      const failure = Object.assign(new Error('reseller failure marker'), { __notified: true })
      mocks.notify.mockClear(); request.mockRejectedValueOnce(failure)
      await state.loadAll!(); await flush()
      expect(mocks.notify).toHaveBeenCalledExactlyOnceWith(failure, 'admin.resellerOperations.errors.loadFailed')
      expect(state.pageError).toBe('')
      expect(root?.textContent).not.toContain(failure.message)
    }
    state.filters.range = 'custom'; state.filters.from = ''; state.filters.to = ''
    mocks.notify.mockClear()
    await state.loadAll!(); await flush()
    expect(root?.textContent).toContain('admin.resellerOperations.errors.customRangeRequired')
    expect(mocks.notify).not.toHaveBeenCalled()
  })
})
