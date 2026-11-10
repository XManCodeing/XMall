import { createApp, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isMembershipGuideRoute, useMembershipGuide } from './useMembershipGuide'

const mocks = vi.hoisted(() => ({ route: { path: '/telegram-bot/groups' }, auth: { adminId: 0 } }))
vi.mock('vue-router', () => ({ useRoute: () => mocks.route }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: ReturnType<typeof createApp> | undefined
let root: HTMLElement

/** 挂载引导监听器并在卸载时自动释放。@returns 引导状态 */
function mountGuide() {
  let guide!: ReturnType<typeof useMembershipGuide>
  root = document.createElement('div')
  app = createApp({ setup() { guide = useMembershipGuide(); return () => null } })
  app.mount(root)
  return guide
}

beforeEach(() => {
  localStorage.clear()
  mocks.route = reactive({ path: '/telegram-bot/groups' })
  mocks.auth = reactive({ adminId: 12 })
})
afterEach(() => { app?.unmount(); vi.restoreAllMocks() })

describe('membership guide', () => {
  it('只覆盖会员群页面和子路由', () => {
    for (const path of ['/telegram-bot/groups', '/telegram-bot/groups/1', '/telegram-bot/membership/orders', '/telegram-bot/support/tasks', '/telegram-bot/recovery']) expect(isMembershipGuideRoute(path)).toBe(true)
    for (const path of ['/', '/telegram-bot/settings', '/telegram-bot/groups-other', '/orders']) expect(isMembershipGuideRoute(path)).toBe(false)
  })
  it('首次显示，关闭后不再自动弹出，手动从头打开', async () => {
    const guide = mountGuide()
    expect(guide.open.value).toBe(true)
    expect(localStorage.getItem('admin_membership_guide_v1:12')).toBe('seen')
    guide.open.value = false
    guide.step.value = 3
    mocks.route.path = '/orders'; await nextTick()
    mocks.route.path = '/telegram-bot/support/tasks'; await nextTick()
    expect(guide.open.value).toBe(false)
    guide.show()
    expect(guide.open.value).toBe(true)
    expect(guide.step.value).toBe(0)
  })
  it('已看过时不弹出，切换管理员隔离记录', async () => {
    localStorage.setItem('admin_membership_guide_v1:12', 'seen')
    const guide = mountGuide()
    expect(guide.open.value).toBe(false)
    mocks.auth.adminId = 13; await nextTick()
    expect(guide.open.value).toBe(true)
  })
  it('等待管理员身份就绪，其他页面不触发', async () => {
    mocks.auth.adminId = 0
    const guide = mountGuide()
    expect(guide.open.value).toBe(false)
    mocks.route.path = '/'; mocks.auth.adminId = 12; await nextTick()
    expect(guide.open.value).toBe(false)
    mocks.route.path = '/telegram-bot/groups'; await nextTick()
    expect(guide.open.value).toBe(true)
  })
  it('存储不可用仍可使用且不在本次会话反复弹出', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
    const warning = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const guide = mountGuide()
    expect(guide.open.value).toBe(true)
    mocks.route.path = '/'; await nextTick()
    mocks.route.path = '/telegram-bot/groups'; await nextTick()
    expect(guide.open.value).toBe(false)
    expect(warning).toHaveBeenCalledTimes(2)
  })
})
