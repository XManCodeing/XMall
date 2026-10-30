import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { parse } from 'vue/compiler-sfc'
import { readFileSync } from 'node:fs'

vi.mock('@/api/admin', () => ({ adminAPI: {
  getUser: vi.fn(), getSettings: vi.fn(), getOrders: vi.fn(), getMemberLevels: vi.fn(),
  getUserWallet: vi.fn(), getUserWalletTransactions: vi.fn(), adjustUserWallet: vi.fn(),
  getUsers: vi.fn(), batchUpdateUserStatus: vi.fn(), updateUser: vi.fn(),
} }))
vi.mock('vue-router', () => ({ useRoute: () => ({ params: { id: '1' }, query: {} }) }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: vi.fn(), notifySuccess: vi.fn() }))
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }))
import { adminAPI } from '@/api/admin'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
import { confirmAction } from '@/utils/confirm'
import UserDetail from './UserDetail.vue'
import Users from './Users.vue'

let app: App | undefined
let root: HTMLDivElement | undefined
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
async function mount(component: typeof UserDetail | typeof Users) {
  vi.mocked(confirmAction).mockResolvedValue(true)
  const response = { data: { data: [] } } as any
  vi.mocked(adminAPI.getSettings).mockResolvedValue({ data: { data: { currency: 'CNY' } } } as any)
  vi.mocked(adminAPI.getUser).mockResolvedValue({ data: { data: { id: 1, email: 'a@example.test' } } } as any)
  vi.mocked(adminAPI.getOrders).mockResolvedValue(response)
  vi.mocked(adminAPI.getUsers).mockResolvedValue(response)
  vi.mocked(adminAPI.getMemberLevels).mockResolvedValue(response)
  vi.mocked(adminAPI.getUserWallet).mockResolvedValue({ data: { data: { account: { balance: '10' } } } } as any)
  vi.mocked(adminAPI.getUserWalletTransactions).mockResolvedValue(response)
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(component)
  app.component('router-link', { template: '<a><slot /></a>' })
  app.use(createI18n({ legacy: false, locale: 'en-US', missingWarn: false, fallbackWarn: false, messages: { 'en-US': {} } }))
  const vm = app.mount(root)
  await flush()
  return (vm as any).$.setupState as any
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('user operation feedback', () => {
  it('keeps wallet load errors separate from validation and adjustment failures', async () => {
    const state = await mount(UserDetail)
    vi.mocked(adminAPI.getUserWallet).mockRejectedValueOnce(new Error('wallet load failed'))
    await state.loadWalletData()
    expect(state.walletLoadError).toBe('wallet load failed')
    await state.submitWalletAdjust()
    expect(state.walletValidationError).toBe('admin.userDetail.wallet.errors.invalidAmount')
    expect(adminAPI.adjustUserWallet).not.toHaveBeenCalled()
    state.walletAdjustForm.amount = '12.50'
    state.walletAdjustForm.remark = 'adjustment'
    const failure = Object.assign(new Error('api failed'), { __notified: true })
    vi.mocked(adminAPI.adjustUserWallet).mockRejectedValueOnce(failure)
    await state.submitWalletAdjust()
    expect(notifyErrorOnce).toHaveBeenCalledWith(failure, 'admin.userDetail.wallet.errors.adjustFailed')
    expect(state.walletLoadError).toBe('wallet load failed')
    expect(state.walletValidationError).toBe('')
    expect(state.walletAdjustForm.amount).toBe('12.50')
    expect(state.walletAdjustForm.remark).toBe('adjustment')
  })

  it('does not report a successful adjustment as failed when refreshing transactions fails', async () => {
    const state = await mount(UserDetail)
    state.walletAdjustForm.amount = '12.50'
    state.walletAdjustForm.remark = 'adjustment'
    vi.mocked(adminAPI.adjustUserWallet).mockResolvedValue({ data: { data: { account: { balance: '22.50' } } } } as any)
    vi.mocked(adminAPI.getUserWalletTransactions).mockRejectedValueOnce(new Error('refresh failed'))
    await state.submitWalletAdjust()
    expect(adminAPI.adjustUserWallet).toHaveBeenCalledWith(1, { operation: 'add', amount: '12.50', remark: 'adjustment' })
    expect(notifySuccess).toHaveBeenCalledWith('admin.userDetail.wallet.adjustSuccess')
    expect(notifyErrorOnce).not.toHaveBeenCalled()
    expect(state.walletLoadError).toBe('refresh failed')
  })

  it('reports batch failures immediately without retaining error state for the edit dialog', async () => {
    const state = await mount(Users)
    const failure = new Error('batch failure')
    state.selectedIds = [1]
    vi.mocked(adminAPI.batchUpdateUserStatus).mockRejectedValueOnce(failure)
    await state.batchUpdateStatus('disabled')
    expect(notifyErrorOnce).toHaveBeenCalledWith(failure, 'admin.users.errors.updateFailed')
    expect('error' in state).toBe(false)
    state.openEditModal({ id: 1, email: 'a@example.test', display_name: 'Alice' })
    expect(state.form.email).toBe('a@example.test')
    expect(state.showModal).toBe(true)
  })
})

const source = (name: string) => readFileSync(`${process.cwd()}/src/views/admin/${name}.vue`, 'utf8')
describe('operation feedback templates', () => {
  it.each(['UserDetail', 'Users', 'InviteCodes', 'Coupons', 'Promotions', 'MemberLevels', 'Payments', 'components/OrderDetailDialog', 'components/OrderFulfillmentModal', 'components/OrderRefundsDialog'])('keeps %s valid and routes request outcomes through toast', (name) => {
    const text = source(name)
    expect(parse(text).errors).toEqual([])
    expect(text).toContain('notifyErrorOnce')
    expect(text).not.toMatch(/(?:exportError|paymentFeeUpdateError|paymentFeeUpdateSuccess|oauthError|oauthSuccess|twofaError|twofaSuccess|walletSuccess|fulfillmentSuccess|manualRefundSuccess|refundSuccess)\.value/)
    expect(text).not.toMatch(/(?:generateError|editError|fulfillmentError|refundError|manualRefundError|error)\.value\s*=\s*err\??\.message/)
  })

  it('retains generated codes, refund records and load failure states', () => {
    expect(source('InviteCodes')).toContain('generatedCodes.value = created.map')
    expect(source('components/OrderRefundsDialog')).toContain('v-else-if="detailRefund"')
    expect(source('components/OrderDetailDialog')).toContain('v-else-if="detailError"')
    expect(source('components/OrderFulfillmentModal')).toContain("fulfillmentError.value = t('admin.orders.fulfillmentSubmitRequired')")
    expect(source('Coupons')).toContain("error.value = t('admin.coupons.errors.scopeRequired')")
    expect(source('Promotions')).toContain("error.value = t('admin.promotions.errors.scopeRequired')")
  })
})
