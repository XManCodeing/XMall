import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'

vi.mock('@/api/admin', () => ({ adminAPI: {
  getMemberLevels: vi.fn(), getPaymentChannel: vi.fn(), createPaymentChannel: vi.fn(),
  updatePaymentChannel: vi.fn(), testWechatPayPublicKey: vi.fn(),
} }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/utils/notify', () => {
  const notifyError = vi.fn()
  return { notifySuccess: vi.fn(), notifyError, notifyErrorOnce: vi.fn((error: { __notified?: boolean } | null, message: string) => {
    if (!error?.__notified) notifyError(message)
  }) }
})
vi.mock('@/components/admin/MediaPicker.vue', () => ({ default: { render: () => null } }))
vi.mock('@/components/ui/dialog', async () => {
  const { defineComponent, h } = await import('vue')
  const passthrough = defineComponent({ setup: (_, { slots }) => () => h('div', slots.default?.()) })
  return Object.fromEntries(['Dialog', 'DialogScrollContent', 'DialogHeader', 'DialogTitle'].map(key => [key, passthrough]))
})
import PaymentChannelModal from './PaymentChannelModal.vue'
import { adminAPI } from '@/api/admin'
import { notifyError, notifyErrorOnce, notifySuccess } from '@/utils/notify'

let app: App | undefined
let root: HTMLDivElement
const channelId = ref<number | null>(null)
const success = vi.fn()
const closed = vi.fn()
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
async function mount(edit = false) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({ setup: () => () => h(PaymentChannelModal, { modelValue: true, channelId: channelId.value, onSuccess: success, 'onUpdate:modelValue': closed }) }))
  app.mount(root)
  await flush()
  if (edit) { channelId.value = 7; await flush() }
}
async function submit() {
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flush()
}
async function click(key: string) {
  const button = Array.from(root.querySelectorAll('button')).find(item => item.textContent?.trim() === key)!
  expect(button).toBeDefined()
  button.click()
  await flush()
}
beforeEach(() => {
  channelId.value = null
  vi.mocked(adminAPI.getMemberLevels).mockResolvedValue({ data: { data: [] } })
  vi.mocked(adminAPI.getPaymentChannel).mockResolvedValue({ data: { data: {
    name: 'Wechat', provider_type: 'official', channel_type: 'wechat', interaction_mode: 'qr',
    config_json: { verification_mode: 'wechatpay_public_key' },
  } } })
})
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('payment channel modal notifications', () => {
  it.each([false, true])('notifies successful saves once (edit: %s)', async edit => {
    await mount(edit)
    await submit()
    expect(edit ? adminAPI.updatePaymentChannel : adminAPI.createPaymentChannel).toHaveBeenCalledTimes(1)
    expect(notifySuccess).toHaveBeenCalledExactlyOnceWith('admin.common.operationSuccess')
    expect(success).toHaveBeenCalledTimes(1)
    expect(closed).toHaveBeenCalledWith(false)
  })
  it.each([false, true])('uses a toast-only save fallback (already notified: %s)', async notified => {
    const error = Object.assign(new Error('private failure'), { __notified: notified })
    vi.mocked(adminAPI.createPaymentChannel).mockRejectedValueOnce(error)
    await mount(); await submit()
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.settings.saveFailed')
    expect(notifyError).toHaveBeenCalledTimes(notified ? 0 : 1)
    expect(root.textContent).not.toContain('private failure')
    expect(success).not.toHaveBeenCalled()
    expect(closed).not.toHaveBeenCalled()
  })
  it('keeps invalid JSON validation inline', async () => {
    await mount()
    await click('admin.paymentChannels.modal.advancedShow')
    const input = root.querySelector('textarea[rows="8"]') as HTMLTextAreaElement
    input.value = '{'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush(); await submit()
    expect(root.textContent).toContain('admin.paymentChannels.errors.invalidConfig')
    expect(adminAPI.createPaymentChannel).not.toHaveBeenCalled()
    expect(notifyErrorOnce).not.toHaveBeenCalled()
  })
  it('keeps gateway diagnostics visible', async () => {
    vi.mocked(adminAPI.testWechatPayPublicKey).mockResolvedValueOnce({ data: { data: {
      response_serial: 'serial-123', request_signature_accepted: true, response_signature_valid: true, echo_message_matched: true,
    } } })
    await mount(true); await click('admin.paymentChannels.modal.wechatPublicKeyTest')
    expect(root.textContent).toContain('serial-123')
    expect(root.textContent).toContain('admin.paymentChannels.modal.wechatPublicKeyTestSuccess')
    expect(notifySuccess).not.toHaveBeenCalled()
  })
  it('does not repeat notified gateway request failures inline', async () => {
    const error = Object.assign(new Error('already shown'), { __notified: true })
    vi.mocked(adminAPI.testWechatPayPublicKey).mockRejectedValueOnce(error)
    await mount(true); await click('admin.paymentChannels.modal.wechatPublicKeyTest')
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.paymentChannels.modal.wechatPublicKeyTestFailed')
    expect(notifyError).not.toHaveBeenCalled()
    expect(root.textContent).not.toContain('already shown')
  })
  it('retains loading failures inline', async () => {
    vi.mocked(adminAPI.getPaymentChannel).mockRejectedValueOnce(new Error('load unavailable'))
    await mount(true)
    expect(root.textContent).toContain('load unavailable')
  })
})
