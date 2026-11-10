import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import RichEditor from './RichEditor.vue'
import i18n from '@/i18n'

vi.mock('@/components/admin/MediaPicker.vue', () => ({ default: { render: () => null } }))
let app: App | undefined
let root: HTMLElement | undefined
/** 等待编辑器及弹窗渲染。@returns 完成通知 */
async function settle() { await nextTick(); await new Promise(resolve => setTimeout(resolve, 40)); await nextTick() }
afterEach(() => { app?.unmount(); root?.remove(); document.body.innerHTML = '' })

describe('rich editor page link dialog', () => {
  it('opens a shared dialog and cancels without changing content or calling native prompt', async () => {
    const updated = vi.fn()
    const nativePrompt = vi.spyOn(window, 'prompt')
    root = document.createElement('div'); document.body.append(root)
    app = createApp(RichEditor, { modelValue: '<p>draft text</p>', 'onUpdate:modelValue': updated })
    app.use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': i18n.global.getLocaleMessage('zh-CN') } }))
    app.mount(root); await settle()
    root.querySelector<HTMLButtonElement>('button[title="插入链接"]')!.click(); await settle()
    const dialog = document.querySelector('[role="dialog"]')!
    expect(dialog).not.toBeNull()
    expect(dialog.querySelector('input')).not.toBeNull()
    const cancel = [...dialog.querySelectorAll('button')].find(button => button.textContent?.trim() === '取消')!
    cancel.click(); await settle()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(updated).not.toHaveBeenCalled()
    expect(nativePrompt).not.toHaveBeenCalled()
    nativePrompt.mockRestore()
  })
})
