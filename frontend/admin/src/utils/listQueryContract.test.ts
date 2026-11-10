import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App, type Component } from 'vue'
import { createI18n } from 'vue-i18n'
import WalletRecharges from '@/views/admin/WalletRecharges.vue'
import UserLoginLogs from '@/views/admin/UserLoginLogs.vue'
import AffiliateCommissions from '@/views/admin/AffiliateCommissions.vue'
import i18n from '@/i18n'

const api = vi.hoisted(() => ({ getWalletRecharges: vi.fn(), getUserLoginLogs: vi.fn(), getAffiliateCommissions: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('@/utils/notify', () => ({ notifySuccess: vi.fn(), notifyErrorOnce: vi.fn() }))
interface QueryState {
  filters: Record<string, string>
  pagination: { page: number; page_size: number }
  loading: boolean
}
let app: App | undefined
let root: HTMLElement | undefined
/** 清空异步请求和 Vue 更新队列。@returns 完成通知。 */
async function settle() { for (let turn = 0; turn < 12; turn++) { await Promise.resolve(); await nextTick() } }
/** 挂载真实表格和筛选控件。@param component 页面组件。@returns 页面内部筛选状态。 */
async function mount(component: Component) {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(component).use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': i18n.global.getLocaleMessage('zh-CN') } }))
  const state = (app.mount(root).$ as unknown as { setupState: QueryState }).setupState
  await settle()
  return state
}
/** 点击当前筛选栏的真实按钮。@param label 用户可见文案。@returns 更新完成。 */
async function click(label: string) {
  const button = [...root!.querySelectorAll('button')].find(element => element.textContent?.trim() === label)
  expect(button, `缺少${label}按钮`).toBeDefined()
  button!.click(); await settle()
}
beforeEach(() => {
  for (const loader of Object.values(api)) loader.mockImplementation(async (query: { page: number; page_size: number }) => ({ data: { data: [], pagination: { ...query, total: 0, total_page: 1 } } }))
})
afterEach(() => { app?.unmount(); root?.remove(); vi.clearAllMocks() })

describe('table query controls', () => {
  it.each([
    { component: WalletRecharges, method: 'getWalletRecharges' as const, field: 'rechargeNo', parameter: 'recharge_no' },
    { component: UserLoginLogs, method: 'getUserLoginLogs' as const, field: 'email', parameter: 'email' },
    { component: AffiliateCommissions, method: 'getAffiliateCommissions' as const, field: 'orderNo', parameter: 'order_no' },
  ])('searches and resets $method from page one without changing page size', async ({ component, method, field, parameter }) => {
    const state = await mount(component)
    const defaults = { ...state.filters }
    state.pagination.page = 4; state.pagination.page_size = 50
    state.filters[field] = 'query-check'
    const beforeSearch = api[method].mock.calls.length
    await settle()
    expect(api[method]).toHaveBeenCalledTimes(beforeSearch)
    await click('搜索')
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1, page_size: 50, [parameter]: 'query-check' }))
    const searched = api[method].mock.calls.length
    await click('搜索')
    expect(api[method]).toHaveBeenCalledTimes(searched + 1)
    for (const key of Object.keys(defaults)) state.filters[key] = key.toLowerCase().includes('from') || key.toLowerCase().includes('to') ? '2026-11-01T12:00' : 'changed'
    state.pagination.page = 3
    await click('重置')
    expect(state.filters).toEqual(defaults)
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1, page_size: 50 }))
    const resetCalls = api[method].mock.calls.length
    let finish: ((value: unknown) => void) | undefined
    api[method].mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await click('搜索')
    expect(state.loading).toBe(true)
    state.pagination.page = 1
    Object.assign(state.pagination, { total: 100, total_page: 2 })
    await settle()
    const nextPage = [...root!.querySelectorAll('button')].find(element => element.textContent?.trim() === '下一页')!
    expect(nextPage.disabled).toBe(true)
    nextPage.click()
    await click('搜索'); await click('重置')
    expect(api[method]).toHaveBeenCalledTimes(resetCalls + 1)
    finish!({ data: { data: [], pagination: { page: 1, page_size: 50, total: 0, total_page: 1 } } })
    await settle()
    expect(state.loading).toBe(false)
    api[method].mockRejectedValueOnce(new Error('list query unavailable'))
    await click('搜索')
    expect(state.loading).toBe(false)
    await click('重置')
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1, page_size: 50 }))
    const beforeEnter = api[method].mock.calls.length
    const input = root!.querySelector('input')!
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settle()
    expect(api[method]).toHaveBeenCalledTimes(beforeEnter + 1)
  })
})
