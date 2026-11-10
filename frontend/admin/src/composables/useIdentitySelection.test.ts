import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, reactive, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { useIdentitySelection } from './useIdentitySelection'
import { telegramIdentityMessages } from '@/i18n/telegramIdentity'
const mocks = vi.hoisted(() => ({ users: vi.fn(), orders: vi.fn(), order: vi.fn(), auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/api/admin', () => ({ adminAPI: { getUsers: mocks.users } }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { orders: mocks.orders, order: mocks.order } }))
let app: App, root: HTMLDivElement
/** Mount read-only selection state with synthetic directory APIs.
 * @param permission Exact permission predicate. @returns State and scope references.
 */
function mount(permission = (_key: string) => true) {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: permission })
  const account = ref('7'), oldID = ref('9007199254740993'), locked = ref(false)
  let state!: ReturnType<typeof useIdentitySelection>
  root = document.createElement('div')
  app = createApp({ setup() { state = useIdentitySelection(account, oldID, locked); return () => null } })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': { telegramRecovery: { identity: telegramIdentityMessages['zh-CN'] } } } }))
  app.mount(root)
  mocks.users.mockResolvedValue({ data: { data: [{ id: 7, nickname: 'synthetic' }, { id: 9007199254740992 }], pagination: { total: 21 } } })
  mocks.orders.mockResolvedValue({ data: { data: [{ id: 11, user_id: 7, order_no: 'test' }, { id: 12, user_id: 8 }] } })
  mocks.order.mockResolvedValue({ data: { data: { id: 11, user_id: 7, items: [{ membership_snapshot: { account_id: 7 }, membership_ledger: { state: 'recorded', effective: [
    { target_id: 9, beneficiary_telegram_id: oldID.value }, { target_id: 10, beneficiary_telegram_id: '88' }, { target_id: 9007199254740992, beneficiary_telegram_id: oldID.value },
  ] } }] } } })
  return { state, account, oldID, locked }
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })
describe('identity selection existing API reuse', () => {
  it('uses bounded user and account-scoped membership queries, discarding unsafe IDs and other accounts', async () => {
    const { state } = mount(); state.keyword.value = ' synthetic '
    await state.load('users'); expect(mocks.users).toHaveBeenCalledWith({ keyword: 'synthetic', page: 1, page_size: 20 })
    expect(state.users.value).toEqual([{ id: '7', label: 'synthetic' }]); expect(state.userNext.value).toBe(true)
    await state.load('orders'); expect(mocks.orders).toHaveBeenCalledWith({ user_id: '7', page: 1, page_size: 20 })
    expect(state.orders.value).toEqual([{ id: '11', label: 'test' }])
    await state.selectOrder('11'); expect(mocks.order).toHaveBeenCalledWith(11); expect(state.targets.value).toEqual(['9'])
  })
  it('checks each exact read permission and never reads arbitrary detail IDs', async () => {
    const { state } = mount(key => key === 'GET:/admin/telegram-bot/membership/orders')
    await state.load('users'); await state.load('orders'); await state.selectOrder('11'); await state.selectOrder('99')
    expect(mocks.users).not.toHaveBeenCalled(); expect(mocks.orders).toHaveBeenCalledOnce(); expect(mocks.order).not.toHaveBeenCalled()
  })
  it('invalidates late candidate responses when account, identity, permission or draft lock changes', async () => {
    const { state, account, oldID, locked } = mount()
    let resolve!: (response: any) => void
    mocks.orders.mockImplementation(() => new Promise(done => { resolve = done }))
    const pending = state.load('orders'); account.value = '8'; resolve({ data: { data: [{ id: 11, user_id: 7 }] } }); await pending
    expect(state.orders.value).toEqual([]); expect(state.busy.value).toBe(false)
    state.targets.value = ['9']; oldID.value = '99'; expect(state.targets.value).toEqual([])
    state.users.value = [{ id: '7', label: 'synthetic' }]; mocks.auth.permissions = ['changed']; expect(state.users.value).toEqual([])
    locked.value = true; await state.load('users'); expect(mocks.users).not.toHaveBeenCalled()
  })
  it('fails closed for wrong-account detail and renders only a safe retryable error', async () => {
    const { state } = mount(); await state.load('orders')
    mocks.order.mockResolvedValue({ data: { data: { id: 11, user_id: 8, items: [] } } }); await state.selectOrder('11')
    expect(state.targets.value).toEqual([]); expect(state.error.value).toBe(telegramIdentityMessages['zh-CN'].lookupFailed)
    mocks.users.mockRejectedValue(new Error('sensitive-token')); await state.load('users')
    expect(state.error.value).not.toContain('sensitive-token'); expect(state.busy.value).toBe(false)
    mocks.users.mockResolvedValue({ data: { data: [] } }); await state.load('users'); expect(state.error.value).toBe(''); expect(state.users.value).toEqual([])
  })
  it('searches the first page repeatedly and resets only the user keyword', async () => {
    const { state, account, oldID } = mount()
    state.keyword.value = ' synthetic '
    await state.load('users', 2)
    await state.load('users', 1); await state.load('users', 1)
    expect(mocks.users.mock.calls.slice(-2).map(call => call[0])).toEqual([
      { keyword: 'synthetic', page: 1, page_size: 20 }, { keyword: 'synthetic', page: 1, page_size: 20 },
    ])
    await state.load('orders', 2); await state.selectOrder('11')
    await state.resetUsers()
    expect(mocks.users).toHaveBeenLastCalledWith({ keyword: undefined, page: 1, page_size: 20 })
    expect(state.keyword.value).toBe(''); expect(state.userPage.value).toBe(1)
    expect(account.value).toBe('7'); expect(oldID.value).toBe('9007199254740993')
    expect(state.orderID.value).toBe('11'); expect(state.targets.value).toEqual(['9'])
    await state.load('orders', 1)
    expect(mocks.orders).toHaveBeenLastCalledWith({ user_id: '7', page: 1, page_size: 20 })
  })
  it('does not reset locked, busy or unauthorized filters or read orders without an account', async () => {
    const { state, account, locked } = mount(() => false)
    state.keyword.value = 'retained'; await state.resetUsers()
    expect(state.keyword.value).toBe('retained'); expect(mocks.users).not.toHaveBeenCalled()
    mocks.auth.hasPermission = () => true
    locked.value = true; await state.resetUsers()
    expect(state.keyword.value).toBe('retained'); expect(mocks.users).not.toHaveBeenCalled()
    locked.value = false; state.busy.value = true; await state.resetUsers()
    expect(state.keyword.value).toBe('retained'); expect(mocks.users).not.toHaveBeenCalled()
    state.busy.value = false; account.value = ''; await state.load('orders', 1)
    expect(mocks.orders).not.toHaveBeenCalled()
  })
  it('keeps translation keys synchronized in three languages', () => {
    for (const locale of ['zh-TW', 'en-US'] as const) expect(Object.keys(telegramIdentityMessages[locale]).sort()).toEqual(Object.keys(telegramIdentityMessages['zh-CN']).sort())
  })
})
