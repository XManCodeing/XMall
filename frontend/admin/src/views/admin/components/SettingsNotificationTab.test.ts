import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'

vi.mock('@/api/admin', () => ({ adminAPI: {
  getNotificationCenterSettings: vi.fn(), updateNotificationCenterSettings: vi.fn(), getProduct: vi.fn(),
} }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn(), notifySuccess: vi.fn() }))

import { adminAPI } from '@/api/admin'
import { notifyError } from '@/utils/notify'
import SettingsNotificationTab from './SettingsNotificationTab.vue'

let app: App | undefined
let root: HTMLDivElement | undefined

/** 创建最小通知表单；无参数，返回挂载组件的状态以验证开关操作。 */
const mountForm = async () => {
  const template = { 'zh-CN': { title: '', body: '' }, 'zh-TW': { title: '', body: '' }, 'en-US': { title: '', body: '' } }
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(SettingsNotificationTab, {
    currentLang: 'zh-CN',
    data: {
      default_locale: 'zh-CN', dedupe_ttl_seconds: 300, inventory_alert_interval_seconds: 1800,
      payment_order_alert_interval_seconds: 1800, payment_order_alert_check_interval_seconds: 86400,
      ignored_product_ids_text: '',
      channels: {
        email: { enabled: false, recipients_text: '' }, telegram: { enabled: false, recipients_text: '' },
        feishu: { enabled: false, app_id: '', app_secret: '', has_app_secret: false, receive_id_type: 'chat_id', recipients_text: '' },
      },
      scenes: { wallet_recharge_success: true, order_paid_success: true, manual_fulfillment_pending: true, exception_alert: true },
      templates: { wallet_recharge_success: template, order_paid_success: template, manual_fulfillment_pending: template, exception_alert: template },
    },
  })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': {} } }))
  const instance = app.mount(root)
  await nextTick()
  return (instance.$ as unknown as { setupState: Record<string, any> }).setupState
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.mocked(adminAPI.getNotificationCenterSettings).mockResolvedValue({ data: { data: { prerequisites: { email: false, telegram: false } } } } as never)
})
afterEach(() => { app?.unmount(); root?.remove(); vi.unstubAllGlobals() })

describe('notification prerequisite switches', () => {
  it.each(['email', 'telegram'])('blocks %s without required configuration', async (channel) => {
    const state = await mountForm()
    await state.toggleChannel(channel, true)
    expect(state.form.channels[channel].enabled).toBe(false)
    expect(notifyError).toHaveBeenCalledTimes(1)
    expect(adminAPI.updateNotificationCenterSettings).not.toHaveBeenCalled()
  })

  it.each(['email', 'telegram'])('allows configured %s and disables without checking', async (channel) => {
    const state = await mountForm()
    vi.mocked(adminAPI.getNotificationCenterSettings).mockResolvedValue({ data: { data: { prerequisites: { [channel]: true } } } } as never)
    await state.toggleChannel(channel, true)
    expect(state.form.channels[channel].enabled).toBe(true)
    await state.toggleChannel(channel, false)
    expect(state.form.channels[channel].enabled).toBe(false)
    expect(adminAPI.getNotificationCenterSettings).toHaveBeenCalledTimes(1)
  })

  it('fails closed on read errors and preserves the recipient draft', async () => {
    const state = await mountForm()
    state.form.channels.telegram.recipients_text = '123456'
    vi.mocked(adminAPI.getNotificationCenterSettings).mockRejectedValueOnce(new Error('read failed'))
    await state.toggleChannel('telegram', true)
    expect(state.form.channels.telegram.enabled).toBe(false)
    expect(state.form.channels.telegram.recipients_text).toBe('123456')
    expect(state.checkingChannel).toBeNull()
    expect(notifyError).toHaveBeenCalledWith('read failed')
  })

  it('accepts a saved Feishu secret but rejects missing credentials', async () => {
    const state = await mountForm()
    await state.toggleChannel('feishu', true)
    expect(state.form.channels.feishu.enabled).toBe(false)
    state.form.channels.feishu.app_id = 'test-app'
    state.form.channels.feishu.has_app_secret = true
    await state.toggleChannel('feishu', true)
    expect(state.form.channels.feishu.enabled).toBe(true)
    expect(adminAPI.getNotificationCenterSettings).not.toHaveBeenCalled()
  })

  it('blocks saving and repeated toggles while prerequisites are loading', async () => {
    const state = await mountForm()
    let complete!: (response: never) => void
    vi.mocked(adminAPI.getNotificationCenterSettings).mockImplementationOnce(() => new Promise((resolve) => { complete = resolve }))
    const pending = state.toggleChannel('telegram', true)
    await state.toggleChannel('email', true)
    await state.save()
    expect(adminAPI.getNotificationCenterSettings).toHaveBeenCalledTimes(1)
    expect(adminAPI.updateNotificationCenterSettings).not.toHaveBeenCalled()
    complete({ data: { data: { prerequisites: { telegram: true } } } } as never)
    await pending
    expect(state.form.channels.telegram.enabled).toBe(true)
  })
})
