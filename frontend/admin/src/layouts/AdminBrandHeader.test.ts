import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import Header from './AdminBrandHeader.vue'
import layoutSource from './AdminLayout.vue?raw'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
let app: App, root: HTMLDivElement
/** Render brand actions. @param collapsed Sidebar state. @returns Event spies. */
function mount(collapsed = false) {
  const update = vi.fn(), theme = vi.fn()
  root = document.createElement('div'); document.body.append(root)
  app = createApp(Header, { collapsed, dark: false, canCheck: true, hasUpdate: true, latestVersion: 'v2', onUpdate: update, onTheme: theme })
  app.mount(root)
  return { update, theme }
}
afterEach(() => { app?.unmount(); root?.remove() })
describe('brand toolbar', () => {
  it('keeps XMall and both actions spaced together with an update dot', async () => {
    const { update, theme } = mount(); await nextTick()
    expect(root.textContent).toBe('XMall')
    expect(root.querySelector('[data-update-dot]')).not.toBeNull()
    root.querySelector<HTMLButtonElement>('[data-update-check]')!.click()
    root.querySelector<HTMLButtonElement>('[data-theme-toggle]')!.click()
    expect(update).toHaveBeenCalledOnce(); expect(theme).toHaveBeenCalledOnce()
    expect(root.querySelector('[data-brand-header]')?.className).toContain('gap-3')
  })
  it('retains actions in a vertical arrangement when collapsed', () => {
    mount(true)
    expect(root.textContent).not.toContain('XMall')
    expect(root.querySelector('[data-brand-header]')?.className).toContain('flex-col')
    expect(root.querySelectorAll('button')).toHaveLength(2)
  })
  it('removes redundant headings and moves actions out of the tab bar', () => {
    expect(layoutSource).not.toContain("t('admin.brand')")
    expect(layoutSource).not.toContain("t('admin.layout.controlRoom')")
    const header = layoutSource.split('<header')[1]!.split('</header>')[0]!
    expect(header).not.toContain('updateCheckOpen')
    expect(header).not.toContain('toggleTheme')
    expect(layoutSource).toContain('@checked="acceptUpdateCheck"')
  })
})
