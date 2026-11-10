import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import ListPagination from './ListPagination.vue'

let app: App, root: HTMLDivElement
/** Mount real pagination with isolated text. @param overrides Initial props. @returns Reactive props and event spy. */
function mount(overrides: Record<string, unknown> = {}) {
  const onChangePage = vi.fn()
  const props = reactive({ page: 1, total: 45, totalPage: 3, alwaysShow: true, ...overrides, onChangePage })
  root = document.createElement('div'); document.body.append(root)
  app = createApp({ render: () => h(ListPagination, props) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en: { admin: { common: { pageInfo: '{total} / {page} / {totalPage}', pageSize: 'Per page', prevPage: 'Previous', nextPage: 'Next', jumpTo: 'Jump', jumpPlaceholder: 'Page' } }, telegramRecovery: { page: 'Page {page}' } } } }))
  app.mount(root)
  return { props, onChangePage }
}
import { h } from 'vue'
/** Find exact control. @param label Translated control text. @returns Button. */
function button(label: string) { return Array.from(root.querySelectorAll('button')).find(node => node.textContent?.trim() === label)! }
afterEach(() => { app?.unmount(); root?.remove() })

describe('ListPagination', () => {
  it('renders the complete numbered footer with a consistent control order', () => {
    mount({ pageSize: 20, pageSizeOptions: [10, 20, 50, 100] })
    const footer = root.firstElementChild!
    expect(footer.classList.contains('border-t')).toBe(true)
    expect(footer.classList.contains('px-6')).toBe(true)
    expect(footer.classList.contains('py-4')).toBe(true)
    const controls = [...root.querySelectorAll('[role="combobox"], input, button')]
    const selector = root.querySelector('[role="combobox"]')!
    expect(selector).not.toBeNull()
    expect(controls.indexOf(selector)).toBeLessThan(controls.indexOf(root.querySelector('input')!))
    expect(controls.indexOf(button('Jump'))).toBeLessThan(controls.indexOf(button('Previous')))
    expect(controls.indexOf(button('Previous'))).toBeLessThan(controls.indexOf(button('Next')))
  })
  it('keeps the empty footer but disables navigation', () => {
    mount({ total: 0, totalPage: 0 })
    expect(root.textContent).toContain('0 / 1 / 1')
    expect(button('Previous').disabled).toBe(true)
    expect(button('Next').disabled).toBe(true)
  })
  it('never invents totals or exposes arbitrary jumps for cursor pages', () => {
    const { onChangePage } = mount({ mode: 'cursor', page: 2, hasPrevious: true, hasNext: false, total: undefined, totalPage: undefined })
    expect(root.textContent).toContain('Page 2')
    expect(root.querySelector('input')).toBeNull()
    expect(button('Next').disabled).toBe(true)
    button('Previous').click()
    expect(onChangePage).toHaveBeenCalledWith(1)
  })
  it('blocks duplicate navigation while loading and resumes after failure', async () => {
    const { props, onChangePage } = mount({ mode: 'cursor', loading: true, hasNext: true })
    button('Next').click(); expect(onChangePage).not.toHaveBeenCalled()
    Object.assign(props, { loading: false }); await nextTick()
    button('Next').click(); expect(onChangePage).toHaveBeenCalledWith(2)
  })
  it('uses authoritative numbered boundaries and clamps jump input', async () => {
    const { onChangePage } = mount()
    const input = root.querySelector('input')!
    input.value = '99'; input.dispatchEvent(new Event('input')); await nextTick()
    button('Jump').click()
    expect(onChangePage).toHaveBeenCalledWith(3)
  })
})
