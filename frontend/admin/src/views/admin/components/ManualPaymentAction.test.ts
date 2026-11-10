import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import ManualPaymentAction from './ManualPaymentAction.vue'
import i18n from '@/i18n'
import { manualPaymentMessages } from '@/i18n/manualPayment'

const mocks = vi.hoisted(() => ({ order: vi.fn(), membershipOrder: vi.fn(), payments: vi.fn(), auth: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { getOrder: mocks.order, getPayments: mocks.payments } }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { order: mocks.membershipOrder } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
/** Drain API and Vue microtasks. @returns Completion. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount one shared list action. @param membership Route selection. @param override Order overrides. @returns Component state. */
function mount(membership: boolean, override = {}) {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(ManualPaymentAction, { membership, order: { id: 7, order_no: 'ORDER-7', status: 'canceled', ...override } as any })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { ...i18n.global.getLocaleMessage('zh-CN'), manualPayment: manualPaymentMessages['zh-CN'] } } }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('shared ordinary and membership list receipt action', () => {
  it.each([false, true])('uses identical selection dialog for membership=%s including canceled orders', async membership => {
    const response = { data: { data: { id: 7, order_no: 'ORDER-7', status: 'canceled', payments: [{ id: 91, order_id: 7, payment_no: 'PAY-91', status: 'expired' }] } } }
    mocks.order.mockResolvedValue(response); mocks.membershipOrder.mockResolvedValue(response)
    const state = mount(membership)
    expect(root.querySelector('button')?.textContent).toBe('补单')
    expect(root.querySelector('button')?.getAttribute('aria-label')).toBe('补单 / 人工确认收款')
    root.querySelector('button')!.click(); await flush()
    expect((membership ? mocks.membershipOrder : mocks.order)).toHaveBeenCalledWith(7)
    expect((membership ? mocks.order : mocks.membershipOrder)).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('选择收款记录')
    expect(document.body.textContent).toContain('PAY-91')
    state.receiptBusy = true; state.changeOpen(false)
    expect(state.open).toBe(true)
    state.receiptBusy = false; state.changeOpen(false); await flush()
    expect(state.detail).toBeUndefined()
  })
  it.each([false, true])('hides paid/child order actions and enforces detail authorization for membership=%s', async membership => {
    const state = mount(membership, { parent_id: 1 })
    expect(state.visible).toBe(false)
    await state.show(); expect(mocks.order).not.toHaveBeenCalled(); expect(mocks.membershipOrder).not.toHaveBeenCalled()
  })
  it.each([false, true])('hides paid order action for membership=%s', async membership => {
    const state = mount(membership, { status: 'paid' })
    expect(state.visible).toBe(false)
    await state.show()
    expect(mocks.order).not.toHaveBeenCalled(); expect(mocks.membershipOrder).not.toHaveBeenCalled()
  })
  it.each([false, true])('clears pending reads after permission changes for membership=%s', async membership => {
    let resolve!: (response: unknown) => void
    const pending = new Promise(done => { resolve = done })
    mocks.order.mockReturnValue(pending); mocks.membershipOrder.mockReturnValue(pending)
    const state = mount(membership)
    const request = state.show()
    mocks.auth.token = 'second'
    resolve({ data: { data: { id: 7, order_no: 'ORDER-7' } } })
    await request; await flush()
    expect(state.detail).toBeUndefined(); expect(state.open).toBe(false)
    mocks.auth.hasPermission = () => false
    expect(state.visible).toBe(false)
  })
})
