import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import ManualPaymentConfirmation from './ManualPaymentConfirmation.vue'
import { manualPaymentMessages } from '@/i18n/manualPayment'

const mocks = vi.hoisted(() => ({ review: vi.fn(), confirm: vi.fn(), payments: vi.fn(), auth: null as any }))
vi.mock('@/api/manualPayment', async importOriginal => ({ ...await importOriginal<object>(), manualPaymentAPI: { review: mocks.review, confirm: mocks.confirm } }))
vi.mock('@/api/admin', () => ({ adminAPI: { getPayments: mocks.payments } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
const payment = { id: 91, order_id: 7, payment_no: 'PAY-91', channel_id: 2, provider_type: 'epay', status: 'pending', created_at: '2026-01-01T00:00:00Z' }
const review = { payment_id: 91, order_id: 7, payment_status: 'pending', order_status: 'pending_payment', amount: '10.12', currency: 'USDT', can_confirm: true, requires_timeout_confirmation: false, blocked_reason: '' }
/** Drain Vue/API microtasks. @returns Completion. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount the real dialog against synthetic data. @param override Order overrides. @returns Setup state for guarded action assertions. */
function mount(override = {}, autoLoad = false) {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
  mocks.review.mockResolvedValue({ data: { data: { ...review } } })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(ManualPaymentConfirmation, { autoLoad, order: { id: 7, order_no: 'ORDER-7', payments: [payment], ...override } as any })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { manualPayment: manualPaymentMessages['zh-CN'] } } }))
  const instance = app.mount(root)
  return (instance.$ as unknown as { setupState: any }).setupState
}
/** Supply a valid external receipt without numeric conversion. @param state Component state. @returns Nothing. */
function fill(state: any) { Object.assign(state.draft, { amount: '10.12', currency: 'USDT', paid_at: '2026-01-02T10:30', transaction_id: 'verified-trade', evidence_reference: '', reason: 'Bank statement reviewed' }) }
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.clearAllMocks() })

describe('manual receipt confirmation', () => {
  it('requires a separate timeout dialog before posting and preserves evidence when canceled', async () => {
    const state = mount({ status: 'canceled' })
    mocks.review.mockResolvedValue({ data: { data: { ...review, order_status: 'canceled', payment_status: 'expired', requires_timeout_confirmation: true } } })
    await state.show(payment); fill(state); state.askConfirm(); state.acknowledged = true
    await state.submit(); await flush()
    expect(state.timeoutOpen).toBe(true)
    expect(document.body.textContent).toContain('确认超时补单')
    expect(mocks.confirm).not.toHaveBeenCalled()
    state.timeoutOpen = false
    expect(state.draft.amount).toBe('10.12')
    await state.submit(true)
    expect(mocks.confirm).not.toHaveBeenCalled()
    mocks.confirm.mockResolvedValue({ data: { data: { ...review, payment_status: 'success', can_confirm: false } } })
    document.documentElement.classList.add('dark'); await flush()
    expect(state.draft.reason).toBe('Bank statement reviewed')
    await state.submit(true); await flush()
    expect(mocks.confirm).toHaveBeenCalledWith(91, expect.objectContaining({ confirm_timeout: true, expected_status: 'expired' }))
    expect(state.timeoutOpen).toBe(false)
    await state.submit(true)
    expect(mocks.confirm).toHaveBeenCalledTimes(1)
  })
  it('invalidates timeout consent on account replacement and rejects unknown timeout flags', async () => {
    const state = mount()
    mocks.review.mockResolvedValue({ data: { data: { ...review, requires_timeout_confirmation: undefined } } })
    await state.show(payment)
    expect(state.review).toBeUndefined()
    mocks.review.mockResolvedValue({ data: { data: { ...review, requires_timeout_confirmation: true } } })
    await state.show(payment); fill(state); state.askConfirm(); state.acknowledged = true
    await state.submit(); expect(state.timeoutOpen).toBe(true)
    mocks.auth.token = 'replacement'; await flush()
    expect(state.timeoutOpen).toBe(false)
    await state.submit(true); expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it('auto-loads real payments for the membership shortcut without selecting or confirming', async () => {
    mocks.payments.mockResolvedValue({ data: { data: [payment] } })
    const state = mount({ payments: undefined }, true)
    await flush()
    expect(mocks.payments).toHaveBeenCalledWith({ order_id: 7, page: 1, page_size: 20 })
    expect(state.availableRows).toHaveLength(1)
    expect(state.payment).toBeUndefined()
    expect(mocks.review).not.toHaveBeenCalled()
    expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it('requires real payment selection, exact POST permission, and rejects child orders', async () => {
    const state = mount({ parent_id: 3 })
    await state.show(payment)
    expect(mocks.review).not.toHaveBeenCalled()
    expect(root.textContent).toContain('子订单不能单独确认收款')
  })
  it('does not treat order update or GET permission as finance authority', async () => {
    const state = mount()
    mocks.auth.hasPermission = (key: string) => !key.startsWith('POST:/admin/payments/')
    await state.show(payment); await state.submit()
    expect(mocks.review).not.toHaveBeenCalled(); expect(mocks.confirm).not.toHaveBeenCalled()
    await state.show({ ...payment, order_id: 8 }); expect(mocks.review).not.toHaveBeenCalled()
  })
  it('requires two stages and acknowledgement; preserves precise external amount and trusted payment status', async () => {
    const state = mount(); await state.show(payment); fill(state)
    await state.submit(); expect(mocks.confirm).not.toHaveBeenCalled()
    state.askConfirm(); await state.submit(); expect(mocks.confirm).not.toHaveBeenCalled()
    state.acknowledged = true
    mocks.confirm.mockResolvedValue({ data: { data: { ...review, payment_status: 'success', can_confirm: false } } })
    await state.submit(); await flush()
    expect(mocks.confirm).toHaveBeenCalledWith(91, expect.objectContaining({ confirm_timeout: false, amount: '10.12', currency: 'USDT', expected_status: 'pending', request_id: expect.any(String), paid_at: '2026-01-02T02:30:00.000Z' }))
    expect(state.accepted).toBe(true)
    expect(document.body.textContent).toContain('收款已确认')
    expect(document.body.textContent).toContain('付款确认不等于权益入账')
    await state.submit(); expect(mocks.confirm).toHaveBeenCalledTimes(1)
  })
  it.each([403, 409, undefined])('keeps evidence and blocks blind replay after %s', async status => {
    const state = mount(); await state.show(payment); fill(state); state.askConfirm(); state.acknowledged = true
    mocks.confirm.mockRejectedValue({ response: status ? { status } : undefined, message: 'secret-provider-token' })
    await state.submit(); await flush()
    expect(state.draft.reason).toBe('Bank statement reviewed'); expect(state.locked).toBe(true)
    expect(document.body.textContent).not.toContain('secret-provider-token')
    await state.refreshReview(); state.askConfirm(); state.acknowledged = true; await state.submit()
    expect(mocks.confirm).toHaveBeenCalledTimes(1)
    state.changeOpen(false); await state.show(payment)
    expect(state.draft.amount).toBe('10.12'); expect(state.locked).toBe(true)
  })
  it('blocks duplicate clicks and dialog closing while busy and clears evidence on account changes', async () => {
    const state = mount(); await state.show(payment); fill(state); state.askConfirm(); state.acknowledged = true
    let resolve!: (value: unknown) => void
    mocks.confirm.mockImplementation(() => new Promise(done => { resolve = done }))
    const request = state.submit(); await state.submit(); state.changeOpen(false)
    expect(mocks.confirm).toHaveBeenCalledTimes(1); expect(state.open).toBe(true)
    document.documentElement.classList.add('dark'); await flush(); expect(state.draft.reason).toBe('Bank statement reviewed')
    mocks.auth.token = 'second'; await flush()
    expect(state.draft.reason).toBe(''); expect(state.open).toBe(false)
    resolve({ data: { data: { ...review, payment_status: 'success' } } }); await request
    expect(state.accepted).toBe(false); expect(state.review).toBeUndefined()
  })
  it('fails closed for review identity mismatch and backend blocked state', async () => {
    const state = mount()
    mocks.review.mockResolvedValueOnce({ data: { data: { ...review, payment_id: 7 } } })
    await state.show(payment); fill(state); state.askConfirm()
    expect(state.review).toBeUndefined(); expect(state.step).toBe('edit')
    mocks.review.mockResolvedValueOnce({ data: { data: { ...review, can_confirm: false } } })
    await state.refreshReview(); state.askConfirm(); expect(state.step).toBe('edit')
  })
  it('loads real order-filtered records without selecting a latest payment implicitly', async () => {
    const state = mount({ payments: undefined })
    mocks.payments.mockResolvedValue({ data: { data: [payment, { ...payment, id: 92, order_id: 8 }], pagination: { total_page: 2 } } })
    await state.loadPayments()
    expect(mocks.payments).toHaveBeenCalledWith({ order_id: 7, page: 1, page_size: 20 })
    expect(state.availableRows).toHaveLength(1); expect(state.payment).toBeUndefined()
    expect(mocks.review).not.toHaveBeenCalled()
  })
})
