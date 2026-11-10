import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import SupportPolicyDialog from './SupportPolicyDialog.vue'
import { telegramSupportSettingsMessages } from '@/i18n/telegramSupportSettings'

const mocks = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), close: vi.fn(), denied: [] as string[] }))
vi.mock('@/api/telegramSupportSettings', () => ({ getSupportSettings: mocks.get, saveSupportSettings: mocks.put }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (key: string) => !mocks.denied.includes(key) }) }))
vi.mock('@/utils/notify', () => ({ notifySuccess: vi.fn() }))
let app: App, root: HTMLDivElement
const policy = { max_messages: 10, max_bytes: 65536, max_attempts: 3, retention_seconds: 3600, challenge_ttl_seconds: 300, cooldown_seconds: 60 }
/** Create a Bot-scoped policy response. @param botID Bot identity. @param version Revision. @returns API response. */
function response(botID = 1, version = 2) { return { data: { data: { bot_id: botID, version, policy: { ...policy }, defaults: { ...policy }, production_ready: false, verification_mode: 'click_required' } } } }
/** Drain requests and rendering. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount the existing policy model inside its new dialog. @returns Setup state and reactive props. */
function mount() {
  root = document.createElement('div'); document.body.append(root)
  const props = reactive({ botID: 1, botLabel: '@synthetic_bot', onClose: mocks.close })
  app = createApp(SupportPolicyDialog, props)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupportSettings: telegramSupportSettingsMessages['zh-CN'] } } }))
  const state = (app.mount(root).$ as unknown as { setupState: any }).setupState
  return { state, props: (app as unknown as { _instance: { props: typeof props } })._instance.props }
}
beforeEach(() => { vi.clearAllMocks(); mocks.denied = []; mocks.get.mockResolvedValue(response()); mocks.put.mockResolvedValue(response(1, 3)) })
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark') })
describe('Bot policy dialog in group management', () => {
  it('renders six fields and shared scope, preserves drafts across themes and saves with CAS', async () => {
    const { state } = mount(); await flush()
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith(1)
    const dialog = document.querySelector('[role="dialog"]')!
    expect(dialog.textContent).toContain('@synthetic_bot')
    expect(dialog.textContent).toContain('同一机器人的客服群共用此设置')
    expect(dialog.textContent).toContain('顾客联系机器人客服时的验证和消息限制')
    expect(dialog.textContent).toContain('不会开启客服接待，也不会更换客服群')
    expect(dialog.querySelectorAll('input[type="number"]')).toHaveLength(6)
    expect(dialog.textContent).not.toContain('客服群绑定')
    const input = dialog.querySelector<HTMLInputElement>('#support-max_messages')!
    input.value = '12'; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
    document.documentElement.classList.add('dark'); await flush()
    expect(state.draft.max_messages).toBe(12); expect(input.value).toBe('12')
    document.documentElement.classList.remove('dark'); await state.save()
    expect(mocks.put).toHaveBeenCalledExactlyOnceWith(1, 2, { ...policy, max_messages: 12 })
    expect(state.snapshot.version).toBe(3)
  })
  it('validates bounds, applies defaults, and preserves drafts on conflicts', async () => {
    const { state } = mount(); await flush()
    state.draft.challenge_ttl_seconds = 3601; await flush(); expect(state.canSave).toBe(false)
    await state.save(); expect(mocks.put).not.toHaveBeenCalled()
    state.defaults(); await flush(); expect(state.canSave).toBe(true)
    state.draft.max_messages = 12
    mocks.put.mockRejectedValue({ response: { status: 409 } })
    await state.save(); await flush()
    expect(state.draft.max_messages).toBe(12); expect(state.canSave).toBe(false)
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('配置已被修改')
    await state.load(); expect(state.draft.max_messages).toBe(10)
  })
  it('disables closing and repeated submissions during save', async () => {
    let resolve!: (value: unknown) => void
    mocks.put.mockImplementation(() => new Promise(accept => { resolve = accept }))
    const { state } = mount(); await flush()
    const pending = state.save(); await flush()
    state.changeOpen(false); await state.save()
    expect(mocks.close).not.toHaveBeenCalled(); expect(mocks.put).toHaveBeenCalledTimes(1)
    resolve(response(1, 3)); await pending
    state.changeOpen(false); expect(mocks.close).toHaveBeenCalledOnce()
  })
  it('supports read-only access without granting write access', async () => {
    mocks.denied = ['PUT:/admin/telegram-bot/support/settings/1']
    const { state } = mount(); await flush(); await state.save()
    expect(document.querySelector('fieldset')?.disabled).toBe(true)
    expect(mocks.put).not.toHaveBeenCalled()
  })
  it('does not fetch without policy read permission', async () => {
    mocks.denied = ['GET:/admin/telegram-bot/support/settings/1']
    mount(); await flush(); expect(mocks.get).not.toHaveBeenCalled()
    expect(document.querySelectorAll('input[type="number"]')).toHaveLength(0)
  })
  it('shows load errors and supports an explicit retry', async () => {
    mocks.get.mockRejectedValueOnce(new Error('network'))
    const { state } = mount(); await flush()
    expect(document.querySelector('[role="alert"]')).not.toBeNull()
    expect(state.canSave).toBe(false)
    await state.load(); await flush()
    expect(document.querySelectorAll('input[type="number"]')).toHaveLength(6)
  })
  it('ignores delayed responses from a different Bot', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(accept => { resolve = accept }))
    const { state, props } = mount(); await flush()
    mocks.get.mockResolvedValue(response(2)); props.botID = 2; await flush()
    resolve(response(1)); await flush()
    expect(state.snapshot.bot_id).toBe(2)
  })
})
