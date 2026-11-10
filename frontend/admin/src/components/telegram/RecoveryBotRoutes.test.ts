import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Snapshot from './RecoveryBotRouteSnapshot.vue'
import Editor from './RecoveryBotRoutesEditor.vue'
import { recoveryBotRouteMessages } from '@/i18n/recoveryBotRoutes'
import type { RecoveryBotRoute } from '@/api/telegramRecovery'

let app: App, root: HTMLDivElement
/** Mount an isolated real component. @param component Component. @param props Component props. @returns Nothing. */
function mount(component: typeof Snapshot | typeof Editor, props: Record<string, unknown>) {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(component, props)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', messages: recoveryBotRouteMessages }))
  app.mount(root)
}
/** Check shared compact product table styling. @returns Nothing. */
function expectTableStyle() {
  const table = root.querySelector('table')!
  expect(table.closest('.rounded-xl')?.className).toBe('rounded-xl border border-border bg-card overflow-x-auto')
  expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
  expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
  expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
  for (const cell of table.querySelectorAll('th, td')) expect(cell.className).toContain('px-4 py-3')
}
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark') })

describe('recovery bot route themed tables', () => {
  it('retains immutable destination fallback in the compact snapshot', async () => {
    const routes: RecoveryBotRoute[] = [
      { target_id: '1', chat_id: '-1001', new_chat_id: '0', version: '2', route_version: '3', purpose: 'membership', enabled: true },
      { target_id: '2', chat_id: '-1002', new_chat_id: '-1003', version: '4', route_version: '5', purpose: 'support', enabled: true },
    ]
    mount(Snapshot, { routes })
    expectTableStyle()
    const rows = root.querySelectorAll('tbody tr')
    expect(rows[0]!.querySelectorAll('td')[3]!.textContent).toBe('-1001')
    expect(rows[1]!.querySelectorAll('td')[3]!.textContent).toBe('-1003')
    document.documentElement.classList.add('dark'); await nextTick()
    expectTableStyle()
  })

  it('keeps reload, pagination, editing and removal wired with outline actions', async () => {
    const model = {
      routes: ref([{ target_id: '1', chat_id: '-1003' }]),
      options: ref([{ id: '1', name: 'Membership', chat: '-1001' }]),
      selectedOptions: ref([{ id: '1', name: 'Membership', chat: '-1001' }]),
      loading: ref(false), error: ref(''), page: ref(2), total: ref(60), valid: ref(true),
      load: vi.fn(), add: vi.fn(), remove: vi.fn(), clear: vi.fn(), payload: vi.fn(),
    }
    mount(Editor, { model, allowed: true, source: '9', disabled: false })
    expectTableStyle()
    const messages = recoveryBotRouteMessages['zh-CN']
    for (const label of [messages.load, messages.previous, messages.next]) {
      const button = [...root.querySelectorAll('button')].find(item => item.textContent === label)!
      expect(button.className).toContain('border-input'); button.click()
    }
    expect(model.load.mock.calls).toEqual([[], [1], [3]])
    const action = root.querySelector<HTMLButtonElement>('tbody button')!
    expect(action.parentElement?.className).toBe('flex flex-wrap items-center justify-end gap-2')
    expect(action.className).toContain('border-input'); expect(action.className).toContain('h-8')
    const input = root.querySelector<HTMLInputElement>('tbody input')!
    input.value = '-1009'; input.dispatchEvent(new Event('input', { bubbles: true })); await nextTick()
    document.documentElement.classList.add('dark'); await nextTick()
    expect(model.routes.value[0]!.chat_id).toBe('-1009')
    expect(input.value).toBe('-1009')
    action.click(); expect(model.remove).toHaveBeenCalledWith('1')
    model.loading.value = true; await nextTick(); expect(input.disabled).toBe(true)
  })
})
