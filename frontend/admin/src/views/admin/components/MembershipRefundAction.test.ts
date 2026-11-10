import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import MembershipRefundAction from './MembershipRefundAction.vue'
import i18n from '@/i18n'
import { membershipOrderStateMessages } from '@/i18n/membershipOrderState'
import { membershipRefundMessages } from '@/i18n/membershipRefund'

const mocks = vi.hoisted(() => ({ preview: vi.fn(), order: vi.fn(), execute: vi.fn(), requests: vi.fn(), auth: null as any }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { order: mocks.order } }))
vi.mock('@/api/membershipRefund', () => ({ membershipRefundAPI: { preview: mocks.preview, execute: mocks.execute, requests: mocks.requests } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
/** Flush asynchronous UI updates. @returns Completion. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount the real themed dialog under a reactive backend permission fixture. @returns Component state. */
function mount(locale = 'zh-CN', recovery = false) {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(MembershipRefundAction, { recovery, order: { id: 7, order_no: 'MEMBER-7', status: 'delivered', paid_at: '2026-11-01', membership_groups: [{ rights: 'active', permanent: true, member_removal: 'unconfirmed' }] } as any })
  app.use(createI18n({ legacy: false, locale, missingWarn: false, fallbackWarn: false, messages: Object.fromEntries(Object.entries(membershipRefundMessages).map(([key, value]) => [key, { ...i18n.global.getLocaleMessage(key), membershipRefund: value, membershipOrderState: membershipOrderStateMessages[key as keyof typeof membershipOrderStateMessages] }])) }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
/** Prepare immutable member-item detail and no prior command. @returns Nothing. */
function ready() {
  mocks.order.mockResolvedValue({ data: { data: { id: 7, total_amount: '100.00', refunded_amount: '0', items: [{ id: 8, title: {}, total_price: '100', membership_snapshot: {} }] } } })
  mocks.preview.mockResolvedValue({ data: { data: { observed_at: '2026-11-04', groups: [{ item_id: 8, target_id: 1, chat_id: '-1001', active: false, permanent: false }] } } })
  mocks.requests.mockResolvedValue({ data: { data: { items: [] } } })
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('combined membership refund', () => {
  it('defaults to a manual full remaining refund without acknowledging payment or selecting items', async () => {
    ready(); const state = mount(); await state.show(); await flush()
    expect(state.method).toBe('manual'); expect(state.amount).toBe('100.00')
    expect(state.remark).toBe('用户主动申请退款'); expect(state.manualConfirmed).toBe(false)
    expect(state.selected).toEqual([]); expect(mocks.execute).not.toHaveBeenCalled()
    state.selected = [8]; await state.submit()
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('此处仅登记已退款事实')
  })
  it('prefills only the remaining refundable amount and preserves edited drafts across method switches', async () => {
    ready(); mocks.order.mockResolvedValue({ data: { data: { total_amount: '100.00', refunded_amount: '30.25', items: [] } } })
    const state = mount(); await state.show()
    expect(state.amount).toBe('69.75')
    state.amount = '20'; state.remark = '协商部分退款'
    state.changeMethod('wallet'); expect(state.method).toBe('manual'); expect(state.walletWarningOpen).toBe(true)
    state.walletWarningOpen = false; expect(state.method).toBe('manual')
    state.changeMethod('wallet'); state.acceptWallet(); expect(state.method).toBe('wallet')
    state.changeMethod('manual')
    expect(state.amount).toBe('20'); expect(state.remark).toBe('协商部分退款')
    expect(state.walletAcknowledged).toBe(false)
  })
  it('shows a wallet warning and blocks even a direct wallet preview until acknowledged', async () => {
    ready(); const state = mount(); await state.show(); state.selected = [8]
    state.changeMethod('wallet'); await flush()
    expect(document.body.textContent).toContain('重复赔付')
    expect(mocks.preview).not.toHaveBeenCalled(); expect(mocks.execute).not.toHaveBeenCalled()
    state.method = 'wallet'; await state.submit(); expect(mocks.preview).not.toHaveBeenCalled()
    state.acceptWallet(); await state.submit()
    expect(mocks.preview).toHaveBeenCalledWith(7, 'wallet', [8])
    expect(mocks.execute).not.toHaveBeenCalled()
  })
  it('requires wallet acknowledgement when manual refunds are not permitted', async () => {
    ready(); const state = mount()
    mocks.auth.hasPermission = (permission: string) => !permission.endsWith('/manual-refund')
    await state.show(); expect(state.method).toBe('wallet'); expect(state.walletWarningOpen).toBe(true)
    state.selected = [8]; state.amount = '10'; await state.submit()
    expect(mocks.preview).not.toHaveBeenCalled()
    mocks.auth.token = 'changed'; await flush(); expect(state.walletWarningOpen).toBe(false)
  })
  it('does not replace a persisted wallet command with manual defaults or a new monetary request', async () => {
    ready()
    const pending = { request_key: 'wallet-persisted-key', method: 'wallet', amount: '10.00', item_ids: [8], remark: '原说明', rights_state: 'pending' }
    mocks.requests.mockResolvedValue({ data: { data: { items: [pending] } } })
    const state = mount(); await state.show()
    expect(state.command).toMatchObject({ request_key: pending.request_key, method: 'wallet', amount: '10.00', item_ids: [8], remark: '原说明' }); expect(state.amount).toBe(''); expect(state.remark).toBe('')
    state.changeMethod('manual'); await state.submit()
    expect(mocks.preview).toHaveBeenCalledWith(7, 'wallet', [8])
    expect(state.draft.request_key).toBe(pending.request_key)
    expect(mocks.execute).not.toHaveBeenCalled()
  })
  it('deduplicates parent summaries and child items by actual item ID while retaining distinct purchases', async () => {
    ready()
    const item = { id: 13, order_id: 25, title: { 'zh-CN': '测试商品' }, total_price: '10', membership_snapshot: {} }
    mocks.order.mockResolvedValue({ data: { data: {
      id: 25, total_amount: '20', refunded_amount: '0', items: [item],
      children: [{ id: 26, items: [{ ...item, order_id: 26 }, { ...item, id: 14, order_id: 26 }, { id: 15, total_price: '0' }] }],
    } } })
    const state = mount(); await state.show(); await flush()
    expect(state.items.map((item: { id: number }) => item.id)).toEqual([13, 14])
    expect(document.querySelectorAll('[role="dialog"] tbody tr')).toHaveLength(2)
    state.toggle(13); state.amount = '10'; state.acceptWallet(); await state.submit()
    expect(mocks.preview).toHaveBeenCalledWith(7, 'wallet', [13])
    expect(mocks.execute).not.toHaveBeenCalled()
  })
  it('renders a single choice when the only membership item is repeated in the parent and child', async () => {
    ready()
    const item = { id: 13, title: {}, total_price: '10', membership_snapshot: {} }
    mocks.order.mockResolvedValue({ data: { data: { id: 7, total_amount: '10', items: [item], children: [{ items: [item] }] } } })
    const state = mount(); await state.show(); await flush()
    expect(state.items).toHaveLength(1)
    expect(document.querySelectorAll('[role="dialog"] tbody tr')).toHaveLength(1)
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'])('uses localized action text in %s', locale => {
    mount(locale)
    expect(root.querySelector('button')?.textContent).toBe(locale === 'en-US' ? 'Refund' : '退款')
    expect(root.querySelector('button')?.getAttribute('aria-label')).toBe(membershipOrderStateMessages[locale as keyof typeof membershipOrderStateMessages].refundedAction)
  })
  it('does not create a fresh refund from the recovery-only entry', async () => {
    ready(); const state = mount('zh-CN', true); await state.show()
    state.selected = [8]; state.amount = '10'; await state.submit()
    expect(mocks.preview).not.toHaveBeenCalled(); expect(mocks.execute).not.toHaveBeenCalled()
    expect(state.error).toBe(membershipOrderStateMessages['zh-CN'].noPendingRefund)
  })
  it('requires both refund and disposition authority', () => {
    const state = mount()
    mocks.auth.hasPermission = (permission: string) => !permission.endsWith('/refunds/dispose')
    expect(state.visible).toBe(false)
  })
  it('requires the generic order permission enforced by HTTP assembly', () => {
    const state = mount()
    mocks.auth.hasPermission = (permission: string) => permission !== 'GET:/admin/orders/7'
    expect(state.visible).toBe(false)
  })
  it('blocks stale submissions when reopening preview fails and preserves the server message', async () => {
    ready(); const state = mount(); await state.show()
    state.selected = [8]; state.amount = '10.00'
    mocks.requests.mockRejectedValueOnce(new Error('会员服务暂不可用'))
    await state.show()
    expect(state.detail).toBeUndefined()
    expect(state.error).toBe('会员服务暂不可用')
    await state.submit()
    expect(mocks.execute).not.toHaveBeenCalled()
  })
  it('retries exactly the same command after an unknown response', async () => {
    ready(); const state = mount(); await state.show()
    state.selected = [8]; state.amount = '10.00'; state.acceptWallet(); mocks.execute.mockRejectedValueOnce(new Error('network'))
    await state.submit(); expect(mocks.execute).not.toHaveBeenCalled(); await state.confirmSubmit(); const first = mocks.execute.mock.calls[0]![1]
    expect(first.item_ids).toEqual([8]); expect(first.method).toBe('wallet')
    mocks.execute.mockResolvedValue({ data: { data: { ...first, refund_id: '3', funds_state: 'completed', rights_state: 'queued' } } })
    await state.submit(); await state.confirmSubmit(); expect(mocks.execute.mock.calls[1]![1]).toEqual(first)
    expect(state.result.rights_state).toBe('queued')
  })
  it('requires offline confirmation and blocks writes when authority preview fails', async () => {
    ready(); const state = mount(); await state.show()
    state.selected = [8]; state.amount = '10'; state.method = 'manual'
    await state.submit(); expect(mocks.preview).not.toHaveBeenCalled()
    state.manualConfirmed = true
    mocks.preview.mockRejectedValueOnce(new Error('preview failed'))
    await state.submit(); await state.confirmSubmit()
    expect(mocks.execute).not.toHaveBeenCalled(); expect(state.confirmOpen).toBe(false)
    await state.submit(); expect(state.confirmOpen).toBe(true)
    await state.confirmSubmit(); expect(mocks.execute.mock.calls[0]![1].manual_confirmed).toBe(true)
  })
  it('clears teleported confirmation when the hidden tab unmounts the action', async () => {
    ready(); const state = mount(); await state.show(); state.selected = [8]; state.amount = '10'; state.manualConfirmed = true
    await state.submit(); expect(state.confirmOpen).toBe(true)
    app.unmount(); await flush()
    expect(state.confirmOpen).toBe(false); expect(state.open).toBe(false)
  })
  it('loads a persisted pending command rather than creating another refund', async () => {
    ready()
    const command = { request_key: 'persisted-key-123456', method: 'manual', amount: '10.00', existing_refund_id: '0', item_ids: [8], remark: '', refund_id: '3', rights_state: 'pending' }
    mocks.requests.mockResolvedValue({ data: { data: { items: [command] } } })
    const state = mount(); await state.show(); expect(state.command.request_key).toBe(command.request_key)
    mocks.auth.token = 'other'; await flush()
    expect(state.command).toBeUndefined(); expect(state.detail).toBeUndefined(); expect(state.open).toBe(false)
  })
})
