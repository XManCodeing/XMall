import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createI18n } from 'vue-i18n'
import { Users, Package, ReceiptText, MessagesSquare, ArrowRightLeft } from 'lucide-vue-next'
import SupportNavigation from './SupportNavigation.vue'
import { useAdminNavigation } from '@/composables/useAdminNavigation'
import { telegramSupportMessages } from '@/i18n/telegramSupport'

const access = vi.hoisted(() => ({ permissions: new Set<string>() }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (permission: string) => access.permissions.has(permission) }) }))
let app: App | undefined
let root: HTMLDivElement | undefined
afterEach(() => { app?.unmount(); root?.remove(); access.permissions.clear() })

/** 挂载真实客服子导航并读取侧栏结果。permissions 为现有权限集合，返回路由与菜单快照。 */
async function mount(permissions: string[]) {
  for (const permission of permissions) access.permissions.add(`GET:/admin/telegram-bot/support/${permission}`)
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/telegram-bot/support/sessions', component: { template: '<div />' } },
    { path: '/telegram-bot/support/tasks', component: { template: '<div />' } },
  ] })
  await router.push('/telegram-bot/support/sessions')
  let navigation: ReturnType<typeof useAdminNavigation> | undefined
  app = createApp({ components: { SupportNavigation }, setup() { navigation = useAdminNavigation(); return {} }, template: '<SupportNavigation active="sessions" />' })
  app.use(router)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupport: telegramSupportMessages['zh-CN'] } } }))
  root = document.createElement('div'); document.body.append(root); app.mount(root)
  await nextTick()
  return { router, items: navigation!.navGroups.value.flatMap(group => group.items) }
}

describe('support consolidated navigation', () => {
  it('keeps one sidebar entry and navigates to the unchanged task address', async () => {
    const { router, items } = await mount(['sessions', 'downstreams'])
    expect(items).toHaveLength(1)
    expect(items[0]?.to).toBe('/telegram-bot/support/sessions')
    expect(root!.textContent).toContain('会话列表')
    expect(root!.querySelector('h2, nav, p')).toBeNull()
    expect(root!.firstElementChild?.getAttribute('data-slot')).toBe('tabs')
    const tab = [...root!.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find(element => element.textContent === '投递记录 / 异常处理')!
    expect(tab).toBeTruthy()
    tab.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/telegram-bot/support/tasks'))
  })
  it('orders member menus by workflow with distinct semantic icons', async () => {
    for (const resource of ['groups', 'membership/products', 'membership/orders', 'recovery/tasks']) {
      access.permissions.add(`GET:/admin/telegram-bot/${resource}`)
    }
    const { items } = await mount(['sessions', 'downstreams'])
    expect(items.map(item => item.to)).toEqual([
      '/telegram-bot/groups', '/telegram-bot/membership/products', '/telegram-bot/membership/orders',
      '/telegram-bot/support/sessions', '/telegram-bot/recovery',
    ])
    expect(items.map(item => item.icon)).toEqual([Users, Package, ReceiptText, MessagesSquare, ArrowRightLeft])
  })
  it('preserves the task-only account entry without granting session access', async () => {
    const { items } = await mount(['downstreams'])
    expect(items).toHaveLength(1)
    expect(items[0]?.to).toBe('/telegram-bot/support/tasks')
    expect([...root!.querySelectorAll('[role="tab"]')].map(tab => tab.textContent)).toEqual(['投递记录 / 异常处理'])
  })
  it('hides task navigation from session-only accounts', async () => {
    const { items } = await mount(['sessions'])
    expect(items).toHaveLength(1)
    expect([...root!.querySelectorAll('[role="tab"]')].map(tab => tab.textContent)).toEqual(['会话列表'])
  })
})
