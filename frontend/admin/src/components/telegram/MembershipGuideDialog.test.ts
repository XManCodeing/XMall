import { createApp, defineComponent, h, nextTick } from 'vue'
import { membershipGuideMessages } from '@/i18n/membershipGuide'
import { membershipPageGuideMessages } from '@/i18n/membershipPageGuide'
import i18n from '@/i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import MembershipGuideDialog from './MembershipGuideDialog.vue'

const mocks = vi.hoisted(() => ({ path: '/telegram-bot/membership/orders', fullPath: '/telegram-bot/membership/orders', push: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ currentRoute: { value: mocks }, push: mocks.push }) }))
vi.mock('@/composables/useAdminNavigation', () => ({ useAdminNavigation: () => ({ navGroups: { value: [] } }) }))
vi.mock('./MembershipPageGuide.vue', async () => {
  const { membershipPageSteps } = await import('@/utils/membershipPageGuide')
  return { default: defineComponent({ setup: () => () => h('div', { 'data-page-guide-test': '' }, JSON.stringify(membershipPageSteps(mocks.path))) }) }
})
let app: ReturnType<typeof createApp> | undefined

afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.restoreAllMocks(); mocks.push.mockClear() })

describe('会员群顶栏快捷引导', () => {
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('在全局语言包注册 %s 的全部引导文案', locale => {
    for (const [namespace, messages] of Object.entries({ membershipGuide: membershipGuideMessages, membershipPageGuide: membershipPageGuideMessages })) {
      for (const key of Object.keys(messages[locale])) {
        expect(i18n.global.te(`${namespace}.${key}`, locale)).toBe(true)
      }
    }
  })
  it.each(['access', 'reviews', 'lifecycle'])('在 %s 子页直接启动当前引导，不打开概览或跳转', async tab => {
    document.body.innerHTML = `<section data-guide="orders-${tab}"></section><div id="test-root"></div>`
    const target = document.querySelector('section')!
    vi.spyOn(target, 'getClientRects').mockReturnValue([{}] as unknown as DOMRectList)
    app = createApp(MembershipGuideDialog, { open: false, step: 0 })
    app.use(i18n)
    const instance = app.mount('#test-root') as unknown as { showCurrent: () => Promise<void> }
    await instance.showCurrent(); await nextTick()
    expect(document.querySelector('[data-page-guide-test]')?.textContent).toContain(`orders-${tab}`)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(mocks.push).not.toHaveBeenCalled()
  })
})
