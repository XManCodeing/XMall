import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Component from './IdentitySelection.vue'
import i18n from '@/i18n'
import { telegramRecoveryMessages } from '@/i18n/telegramRecovery'
const mocks = vi.hoisted(() => ({ users: vi.fn(), orders: vi.fn(), order: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ token: 'test', permissions: [], isSuper: false, hasPermission: () => true }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { getUsers: mocks.users } }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { orders: mocks.orders, order: mocks.order } }))
let app: App, root: HTMLDivElement
/** Mount the real selector in the requested locale. @param locale Locale under test. @returns Draft and component state. */
function mount(locale: keyof typeof telegramRecoveryMessages) {
  const draft = reactive({ account: '7', oldID: '44', newID: '55', targets: '', verified: true })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(Component, { draft, locked: false })
  app.use(createI18n({ legacy: false, locale, fallbackLocale: false, messages: { [locale]: i18n.global.getLocaleMessage(locale) } }))
  const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
  return { draft, state }
}
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.resetAllMocks() })
describe('identity selector UI', () => {
  it.each([
    { locale: 'zh-CN', search: '搜索', reset: '重置', usersEmpty: '暂无用户', ordersEmpty: '暂无订单' },
    { locale: 'zh-TW', search: '搜尋', reset: '重設', usersEmpty: '暫無用戶', ordersEmpty: '暫無訂單' },
    { locale: 'en-US', search: 'Search', reset: 'Reset', usersEmpty: 'No users', ordersEmpty: 'No orders' },
  ] as const)('renders accurate search, reset and empty-list labels in $locale', async ({ locale, search, reset, usersEmpty, ordersEmpty }) => {
    const { state } = mount(locale)
    await nextTick()
    const buttons = [...root.querySelectorAll('button')].filter(button => button.textContent?.trim() === search)
    expect(buttons).toHaveLength(2)
    expect([...root.querySelectorAll('button')].filter(button => button.textContent?.trim() === reset)).toHaveLength(1)
    const emptyMessages = [...root.querySelectorAll('p')].map(paragraph => paragraph.textContent)
    expect(emptyMessages).toContain(usersEmpty)
    expect(emptyMessages).toContain(ordersEmpty)
    expect(root.textContent).not.toContain(telegramRecoveryMessages[locale].refreshSource)
    expect(root.textContent).not.toContain(telegramRecoveryMessages[locale].empty)
    expect(root.textContent).not.toMatch(/(?:admin|telegramRecovery)\./)
    state.users = [{ id: '7', label: 'synthetic user' }]
    state.orders = [{ id: '9', label: 'synthetic order' }]
    await nextTick()
    expect(root.textContent).not.toContain(usersEmpty)
    expect(root.textContent).not.toContain(ordersEmpty)
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('localizes candidate choices and preserves the draft across themes in %s', async locale => {
    const { draft, state } = mount(locale)
    state.targets = ['9', '10']; await nextTick()
    expect(root.textContent).toContain(telegramRecoveryMessages[locale].identity.selectionHint)
    state.toggle('9', true); expect(draft.targets).toBe('9'); expect(draft.verified).toBe(false)
    document.documentElement.classList.add('dark'); await nextTick(); expect(draft.targets).toBe('9')
    state.toggle('10', true); state.toggle('9', false); expect(draft.targets).toBe('10')
    expect(mocks.users).not.toHaveBeenCalled(); expect(mocks.order).not.toHaveBeenCalled()
    document.documentElement.classList.remove('dark'); await nextTick(); expect(draft.newID).toBe('55')
    expect(root.textContent).not.toContain('telegramRecovery.')
  })
  it('wires search and reset to first-page reads while retaining the migration draft', async () => {
    mocks.users.mockResolvedValue({ data: { data: [{ id: 7, nickname: 'synthetic' }], pagination: { total: 21 } } })
    mocks.orders.mockResolvedValue({ data: { data: [{ id: 9, user_id: 7 }] } })
    const { draft, state } = mount('zh-CN')
    state.keyword = 'candidate'; state.userPage = 2; state.orderPage = 2; draft.targets = '9'
    await nextTick()
    const buttons = [...root.querySelectorAll('button')]
    const searches = buttons.filter(button => button.textContent?.trim() === '搜索')
    searches[0]!.click(); await nextTick(); await nextTick()
    expect(mocks.users).toHaveBeenLastCalledWith({ keyword: 'candidate', page: 1, page_size: 20 })
    searches[0]!.click(); await nextTick(); await nextTick()
    expect(mocks.users).toHaveBeenCalledTimes(2)
    buttons.find(button => button.textContent?.trim() === '重置')!.click(); await nextTick(); await nextTick()
    expect(mocks.users).toHaveBeenLastCalledWith({ keyword: undefined, page: 1, page_size: 20 })
    expect(draft).toEqual({ account: '7', oldID: '44', newID: '55', targets: '9', verified: true })
    searches[1]!.click(); await nextTick(); await nextTick()
    expect(mocks.orders).toHaveBeenLastCalledWith({ user_id: '7', page: 1, page_size: 20 })
  })
  it('requires an actual user option and clears cross-user identity approval', () => {
    const { draft, state } = mount('zh-CN'); state.users = [{ id: '8', label: 'synthetic' }]
    state.selectUser('9'); expect(draft.account).toBe('7')
    state.selectUser('8'); expect(draft).toEqual({ account: '8', oldID: '', newID: '', targets: '', verified: false })
  })
})
