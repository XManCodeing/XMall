import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'

vi.mock('@/utils/clipboard', () => ({ copyText: vi.fn() }))
vi.mock('@/utils/notify', () => ({ notifySuccess: vi.fn(), notifyErrorOnce: vi.fn() }))
import { copyText } from '@/utils/clipboard'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
import IdCell from './IdCell.vue'

let app: App | undefined
let root: HTMLDivElement | undefined
afterEach(() => { app?.unmount(); root?.remove(); vi.clearAllMocks() })

describe('ID copy feedback', () => {
  it.each([true, false])('uses toast only for the copy outcome (%s)', async succeeds => {
    const error = new Error('clipboard denied')
    if (succeeds) vi.mocked(copyText).mockResolvedValueOnce()
    else vi.mocked(copyText).mockRejectedValueOnce(error)
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(IdCell, { value: 123 })
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
    app.mount(root)
    const before = root.innerHTML
    root.querySelector('button')!.click()
    await Promise.resolve(); await nextTick()
    expect(copyText).toHaveBeenCalledWith('123')
    if (succeeds) {
      expect(notifySuccess).toHaveBeenCalledExactlyOnceWith('admin.common.copied')
      expect(notifyErrorOnce).not.toHaveBeenCalled()
    } else {
      expect(notifyErrorOnce).toHaveBeenCalledExactlyOnceWith(error, 'admin.common.copyFailed')
      expect(notifySuccess).not.toHaveBeenCalled()
    }
    expect(root.innerHTML).toBe(before)
  })
})
