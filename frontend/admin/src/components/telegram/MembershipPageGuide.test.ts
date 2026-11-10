import { createApp, nextTick, reactive } from 'vue'
import i18n from '@/i18n'
import { computed } from 'vue'
import { pageDialogTargetKey } from '@/components/ui/dialog/pageScope'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MembershipPageGuide from './MembershipPageGuide.vue'

const mocks = vi.hoisted(() => ({ route: { path: '/telegram-bot/groups', fullPath: '/telegram-bot/groups' } }))
vi.mock('vue-router', () => ({ useRoute: () => mocks.route }))
let app: ReturnType<typeof createApp>
const close = vi.fn()
const scroll = vi.fn()
const disconnect = vi.fn()

/** 等待 Vue 与焦点锁挂载。@returns 完成 */
async function flush() { await nextTick(); await new Promise(resolve => setTimeout(resolve, 30)); await nextTick() }
/** 挂载真实弹窗与统一本地化。@param host 页面浮层容器 @returns 完成 */
async function mount(host?: HTMLElement) {
  const root = document.createElement('div'); document.body.append(root)
  app = createApp(MembershipPageGuide, { onClose: close })
  app.use(i18n)
  if (host) app.provide(pageDialogTargetKey, computed(() => host))
  app.mount(root)
  await flush()
}
/** 点击引导内按钮。@param text 按钮文案 @returns 渲染完成 */
async function click(text: string) {
  const button = [...document.querySelectorAll<HTMLButtonElement>('.membership-page-guide button')].find(item => item.textContent === text)
  expect(button).toBeTruthy()
  button!.click(); await flush()
}

beforeEach(() => {
  close.mockClear(); scroll.mockClear(); disconnect.mockClear()
  mocks.route = reactive({ path: '/telegram-bot/groups', fullPath: '/telegram-bot/groups' })
  i18n.global.locale.value = 'zh-CN'
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() { disconnect() } })
  vi.spyOn(HTMLElement.prototype, 'getClientRects').mockReturnValue([{}] as unknown as DOMRectList)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ left: 100, top: 100, right: 500, bottom: 160, width: 400, height: 60 } as DOMRect)
  HTMLElement.prototype.scrollIntoView = scroll
  document.body.innerHTML = '<main><button data-guide="groups-filter">Search</button><button data-guide="groups-create">Create</button></main>'
})
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('MembershipPageGuide', () => {
  it('定位区域并前后切换，不点击业务按钮；缺少数据仍能完成', async () => {
    const business = vi.fn()
    document.querySelector('main')!.addEventListener('click', business)
    await mount()
    expect(document.querySelector('[data-guide-highlight]')).not.toBeNull()
    expect(document.querySelector('.membership-page-guide')?.getAttribute('style')).toContain('width: 380px')
    await click('下一步')
    expect(document.querySelector('.membership-page-guide')?.textContent).toContain('新建群')
    await click('上一步')
    expect(document.querySelector('.membership-page-guide')?.textContent).toContain('1 / 4')
    await click('下一步'); await click('下一步')
    expect(document.querySelector('.membership-page-guide')?.textContent).toContain('此区域当前未显示')
    expect(document.querySelector('[data-guide-highlight]')).toBeNull()
    await click('下一步'); await click('完成')
    expect(close).toHaveBeenCalledOnce()
    expect(business).not.toHaveBeenCalled()
    expect(scroll).toHaveBeenCalled()
  })
  it('浮层及高亮留在页面容器，并将视口坐标转换成容器坐标', async () => {
    const host = document.createElement('div')
    document.body.append(host)
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue({ left: 80, top: 60, right: 880, bottom: 660, width: 800, height: 600 } as DOMRect)
    await mount(host)
    const highlight = host.querySelector<HTMLElement>('[data-guide-highlight]')
    expect(highlight?.style.left).toBe('17px')
    expect(highlight?.style.top).toBe('37px')
    expect(highlight?.classList.contains('absolute')).toBe(true)
    expect(host.querySelector('.membership-page-guide')).not.toBeNull()
    expect(host.querySelector('.bg-transparent')?.classList.contains('absolute')).toBe(true)
    expect(document.querySelector('main')?.inert).toBe(true)
    app.unmount()
    expect(host.querySelector('[data-guide-highlight]')).toBeNull()
    expect(document.querySelector('main')?.inert).not.toBe(true)
  })
  it('全局三语切换保持当前步骤，并恢复关闭前入口焦点', async () => {
    const entry = document.querySelector<HTMLButtonElement>('main button')!
    entry.focus()
    await mount()
    await click('下一步')
    for (const locale of ['zh-TW', 'en-US', 'zh-CN'] as const) {
      i18n.global.locale.value = locale
      await flush()
      const guide = document.querySelector('.membership-page-guide')!
      expect(guide.textContent).toContain(i18n.global.t('membershipPageGuide.groupCreate'))
      expect(guide.textContent).toContain('2 / 4')
    }
    app.unmount()
    await flush()
    expect(document.activeElement).toBe(entry)
  })
  it('路由变化关闭引导，卸载释放观察器', async () => {
    await mount()
    mocks.route.fullPath = '/telegram-bot/membership/products'
    await flush()
    expect(close).toHaveBeenCalledOnce()
    app.unmount()
    expect(disconnect).toHaveBeenCalled()
  })
  it('路由切换后保留用户已移至标签栏的焦点', async () => {
    const tab = document.createElement('button')
    const host = document.createElement('div')
    document.body.append(tab, host)
    await mount(host)
    tab.focus()
    mocks.route.fullPath = '/telegram-bot/membership/products'
    await flush()
    app.unmount()
    await flush()
    expect(close).toHaveBeenCalledOnce()
    expect(document.activeElement).toBe(tab)
  })
  it('结束入口可直接退出', async () => {
    await mount(); await click('结束引导')
    expect(close).toHaveBeenCalledOnce()
  })
})
