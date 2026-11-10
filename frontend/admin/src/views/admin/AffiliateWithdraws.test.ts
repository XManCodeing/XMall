import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import AffiliateWithdraws from './AffiliateWithdraws.vue'
import i18n from '@/i18n'
import { AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW } from '@/constants/affiliate'

const mocks = vi.hoisted(() => ({ list: vi.fn(), reject: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { getAffiliateWithdraws: mocks.list, rejectAffiliateWithdraw: mocks.reject } }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: vi.fn(), notifySuccess: vi.fn() }))
let app: App | undefined
let root: HTMLElement | undefined
/** 等待异步列表与浮层挂载。@returns 完成通知 */
async function settle() { await nextTick(); await new Promise(resolve => setTimeout(resolve, 40)); await nextTick() }
afterEach(() => { app?.unmount(); root?.remove(); document.body.innerHTML = '' })

describe('affiliate withdrawal rejection dialog', () => {
  it('cancels without rejecting and submits the trimmed reason only after explicit confirmation', async () => {
    mocks.list.mockResolvedValue({ data: { data: [{ id: 7, status: AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW }], pagination: { page: 1, page_size: 20, total: 1, total_page: 1 } } })
    mocks.reject.mockResolvedValue({})
    root = document.createElement('div'); document.body.append(root)
    app = createApp(AffiliateWithdraws).use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': i18n.global.getLocaleMessage('zh-CN') } }))
    app.mount(root); await settle()
    const reject = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === i18n.global.t('admin.affiliatesWithdraws.actions.reject'))!
    reject.click(); await settle()
    let dialog = document.querySelector('[role="dialog"]')!
    const cancel = [...dialog.querySelectorAll('button')].find(button => button.textContent?.trim() === '取消')!
    cancel.click(); await settle()
    expect(mocks.reject).not.toHaveBeenCalled()
    reject.click(); await settle()
    dialog = document.querySelector('[role="dialog"]')!
    const input = dialog.querySelector('input')!
    input.value = '  test reason  '; input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    dialog.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(mocks.reject).toHaveBeenCalledExactlyOnceWith(7, { reason: 'test reason' })
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })
})
