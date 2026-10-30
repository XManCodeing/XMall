import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, type App } from 'vue'

vi.mock('@/api/admin', () => ({ adminAPI: { upload: vi.fn(), getMedia: vi.fn() } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/notify', () => {
  const notifyError = vi.fn()
  return { notifyError, notifyErrorOnce: vi.fn((error: { __notified?: boolean } | null, message: string) => {
    if (!error?.__notified) notifyError(message)
  }) }
})
vi.mock('@/components/ui/dialog', async () => {
  const { defineComponent, h } = await import('vue')
  const passthrough = defineComponent({ setup: (_, { slots }) => () => h('div', slots.default?.()) })
  return Object.fromEntries(['Dialog', 'DialogScrollContent', 'DialogHeader', 'DialogTitle'].map(key => [key, passthrough]))
})
import MediaPicker from './MediaPicker.vue'
import { adminAPI } from '@/api/admin'
import { notifyError, notifyErrorOnce } from '@/utils/notify'
import { DEFAULT_UPLOAD_MAX_SIZE_BYTES } from '@/utils/upload'

let app: App | undefined
let root: HTMLDivElement
const changed = vi.fn()
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
async function mount() {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({ setup: () => () => h(MediaPicker, { multiple: true, modelValue: ['/old.png'], 'onUpdate:modelValue': changed }) }))
  app.mount(root)
  await flush()
}
async function upload(files: File[], dialog: boolean) {
  if (dialog) {
    const tab = Array.from(root.querySelectorAll('button')).find(button => button.textContent?.trim() === 'admin.mediaPicker.tabUpload')!
    tab.click()
    await flush()
  }
  const input = root.querySelectorAll<HTMLInputElement>('input[type="file"]')[dialog ? 1 : 0]!
  Object.defineProperty(input, 'files', { configurable: true, value: files })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await flush()
}
beforeEach(() => {
  vi.mocked(adminAPI.getMedia).mockResolvedValue({ data: { data: { items: [], total: 0 } } })
})
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('media picker upload notification ownership', () => {
  it.each([false, true])('keeps successful files without an aggregate toast (dialog: %s)', async dialog => {
    const error = Object.assign(new Error('already shown'), { __notified: true })
    vi.mocked(adminAPI.upload).mockResolvedValueOnce({ data: { data: { url: '/new.png' } } }).mockRejectedValueOnce(error)
    await mount()
    await upload([new File(['ok'], 'ok.png'), new File(['bad'], 'bad.png')], dialog)
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.common.uploadFailed')
    expect(notifyError).not.toHaveBeenCalled()
    expect(root.querySelector('[role="alert"]')).toBeNull()
    if (dialog) {
      const confirm = Array.from(root.querySelectorAll('button')).find(button => button.textContent?.trim() === 'admin.mediaPicker.confirm')!
      confirm.click(); await flush()
      expect(changed).toHaveBeenCalledWith(['/new.png'])
    } else {
      expect(changed).toHaveBeenCalledWith(['/old.png', '/new.png'])
    }
  })

  it.each([false, true])('shows an explicit fallback for unnotified upload errors (dialog: %s)', async dialog => {
    vi.mocked(adminAPI.upload).mockRejectedValueOnce(new Error('private failure'))
    await mount()
    await upload([new File(['bad'], 'bad.png')], dialog)
    expect(notifyError).toHaveBeenCalledExactlyOnceWith('admin.common.uploadFailed')
    expect(changed).not.toHaveBeenCalled()
    expect(root.textContent).not.toContain('private failure')
  })

  it.each([false, true])('preserves file-size validation and skips rejected files (dialog: %s)', async dialog => {
    const large = new File(['large'], 'large.png')
    Object.defineProperty(large, 'size', { value: DEFAULT_UPLOAD_MAX_SIZE_BYTES + 1 })
    await mount()
    await upload([large], dialog)
    expect(adminAPI.upload).not.toHaveBeenCalled()
    expect(notifyError).toHaveBeenCalledExactlyOnceWith('admin.media.errors.fileTooLarge')
  })
})
