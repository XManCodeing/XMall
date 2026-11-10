import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Binding from './SupportGroupBinding.vue'
import { telegramSupportSettingsMessages } from '@/i18n/telegramSupportSettings'
import type { TelegramGroup } from '@/api/telegramGroups'

const mocks = vi.hoisted(() => ({ modules: vi.fn(), saveModule: vi.fn(), denied: [] as string[], saved: vi.fn() }))
vi.mock('@/api/telegramGroups', () => ({ telegramGroupsAPI: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (permission: string) => !mocks.denied.includes(permission) }) }))
vi.mock('@/utils/notify', () => ({ notifySuccess: vi.fn() }))
let app: App, root: HTMLDivElement
const now = Date.now()
/** Drain render and request microtasks. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 12; turn++) { await Promise.resolve(); await nextTick() } }
/** Build saved module intent. @param target Target group. @param enabled Saved intent. @param version CAS version. @returns API response. */
function modules(target = 7, enabled = true, version = 3) { return { data: { data: { items: [
  { partition: 'support', bot_instance_id: 1, version, target_id: target, configured_enabled: enabled, effective_enabled: false },
  { partition: 'membership', bot_instance_id: 1, version: 9, target_id: 99, configured_enabled: true, effective_enabled: true },
] } } } }
/** Mount the row switch. @param overrides Group state overrides. @returns Component state and parent props. */
function mount(overrides: Partial<TelegramGroup> = {}) {
  const props = reactive({ group: {
    id: 7, name: '客服群', bot_instance_id: 1, purpose: 'support', enabled: true, route_version: 1,
    fresh: true, fresh_until: new Date(now + 60000).toISOString(), checking: false, missing_capabilities: [],
    observation: { state: 'ready', route_version: 1, error_class: '' }, ...overrides,
  } as TelegramGroup, now, revision: 0, onSaved: mocks.saved })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(Binding, props)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupportSettings: telegramSupportSettingsMessages['zh-CN'] } } }))
  const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
  return { state, props: (app as unknown as { _instance: { props: typeof props } })._instance.props }
}
beforeEach(() => { mocks.modules.mockResolvedValue(modules()); mocks.denied = [] })
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })
describe('support reception row switch', () => {
  it('uses saved intent, and permits stopping an unavailable group without altering membership', async () => {
    const { state } = mount({ fresh: false, enabled: false }); await flush()
    expect(root.querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true')
    expect(root.textContent).toContain('已开启，当前接待能力不可用')
    mocks.saveModule.mockResolvedValue(modules(7, false, 4))
    await state.toggle(false); await flush()
    expect(mocks.saveModule).not.toHaveBeenCalled()
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('关闭客服接待')
    expect(state.active).toBe(true)
    await state.persist(false); await flush()
    expect(mocks.saveModule).toHaveBeenCalledExactlyOnceWith(1, { partition: 'support', expected_version: 3, enabled: false, target_id: 7 })
    expect(state.rows[1].version).toBe(9)
    expect(mocks.saved).toHaveBeenCalledOnce()
  })
  it('binds an unassigned group only after confirmation', async () => {
    mocks.modules.mockResolvedValue(modules(0, false))
    const { state } = mount(); await flush()
    mocks.saveModule.mockResolvedValue(modules(7, true, 4))
    await state.toggle(true); await flush()
    expect(mocks.saveModule).not.toHaveBeenCalled()
    expect(state.active).toBe(false)
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('开启客服接待')
    await state.persist(true)
    expect(mocks.saveModule).toHaveBeenCalledExactlyOnceWith(1, { partition: 'support', expected_version: 3, enabled: true, target_id: 7 })
  })
  it.each([true, false])('cancels confirmation without saving or changing the switch (enabled=%s)', async enabled => {
    mocks.modules.mockResolvedValue(modules(7, enabled))
    const { state } = mount(); await flush()
    root.querySelector<HTMLButtonElement>('[role="switch"]')!.click(); await flush()
    expect(state.confirmation).toBe(true)
    expect(state.active).toBe(enabled)
    const cancel = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(button => button.textContent === 'admin.common.cancel')!
    cancel.click(); await flush()
    expect(state.confirmation).toBe(false)
    expect(state.active).toBe(enabled)
    await state.persist(!enabled)
    expect(mocks.saveModule).not.toHaveBeenCalled()
  })
  it('requires confirmation for rebinding and preserves intended direction across theme changes', async () => {
    mocks.modules.mockResolvedValue(modules(0, false))
    const { state } = mount(); await flush()
    state.toggle(true); await flush()
    document.documentElement.classList.add('dark'); await flush()
    expect(state.requestedEnabled).toBe(true); expect(state.confirmation).toBe(true)
    document.documentElement.classList.remove('dark')
    await state.persist(false)
    expect(mocks.saveModule).not.toHaveBeenCalled()
  })
  it('requires confirmation for replacement and restores saved state on conflict', async () => {
    const { state } = mount({ id: 8 }); await flush()
    await state.toggle(true); await flush()
    expect(state.confirmation).toBe(true); expect(mocks.saveModule).not.toHaveBeenCalled()
    mocks.saveModule.mockRejectedValue({ response: { status: 409 } })
    await state.persist(true); await flush()
    expect(state.binding.target_id).toBe(7); expect(state.active).toBe(false)
    expect(state.blocked).toBe(true); expect(mocks.saved).not.toHaveBeenCalled()
    await state.reload(); await flush(); expect(state.blocked).toBe(false)
  })
  it.each([{ enabled: false }, { fresh: false }, { recovery_task_id: 7 }, { checking: true }])('blocks opening when group is unavailable: %j', async overrides => {
    mocks.modules.mockResolvedValue(modules(0, false))
    const { state } = mount(overrides); await flush(); await state.toggle(true)
    expect(state.blocked).toBe(true); expect(mocks.saveModule).not.toHaveBeenCalled()
  })
  it('disables all controls during save and restores the switch on request failure', async () => {
    let reject!: (reason: unknown) => void
    mocks.saveModule.mockImplementation(() => new Promise((_resolve, fail) => { reject = fail }))
    const { state } = mount(); await flush()
    state.toggle(false); await flush()
    const pending = state.persist(false); await flush()
    expect(root.querySelector<HTMLButtonElement>('[role="switch"]')?.disabled).toBe(true)
    await state.toggle(true); expect(mocks.saveModule).toHaveBeenCalledTimes(1)
    reject(new Error('network')); await pending; await flush()
    expect(state.active).toBe(true); expect(mocks.saved).not.toHaveBeenCalled()
  })
  it('honors separate read and write permissions', async () => {
    mocks.denied = ['PATCH:/admin/telegram-bot/instances/1/modules']
    const { state } = mount(); await flush(); await state.toggle(false)
    expect(state.blocked).toBe(true); expect(mocks.saveModule).not.toHaveBeenCalled()
  })
  it('does not fetch without read permission', async () => {
    mocks.denied = ['GET:/admin/telegram-bot/instances/1/modules']
    mount(); await flush(); expect(mocks.modules).not.toHaveBeenCalled()
  })
  it('ignores delayed data after switching Bot scope', async () => {
    let resolve!: (value: unknown) => void
    mocks.modules.mockImplementationOnce(() => new Promise(accept => { resolve = accept }))
    const { state, props } = mount(); await flush()
    props.group = { ...props.group, bot_instance_id: 2 }; await flush()
    resolve(modules(88)); await flush()
    expect(state.binding.target_id).toBe(7)
    expect(mocks.modules).toHaveBeenLastCalledWith(2)
  })
  it('reloads other rows after a successful binding change', async () => {
    const { state, props } = mount(); await flush()
    mocks.modules.mockResolvedValue(modules(8, true, 4))
    props.revision++; await flush()
    expect(state.active).toBe(false); expect(state.binding.version).toBe(4)
  })
})
