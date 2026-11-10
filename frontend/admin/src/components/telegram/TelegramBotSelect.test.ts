import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import TelegramBotSelect from './TelegramBotSelect.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), auth: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBotInstances: mocks.list } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
const selected = vi.fn()
let app: App, root: HTMLDivElement, state: any
/** 等待选项读取及渲染。@returns 完成。 */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** 挂载真实主题下拉组件。@param required 是否必选。@returns 完成。 */
async function mount(required = false) {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(TelegramBotSelect, { modelValue: '', required, 'onUpdate:modelValue': selected })
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
  state = (app.mount(root).$ as unknown as { setupState: any }).setupState
  await flush()
}
beforeEach(() => {
  mocks.auth = reactive({ token: 'first', permissions: [], isSuper: true, allowed: true, hasPermission() { return this.allowed } })
  mocks.list.mockResolvedValue({ data: { data: { items: [{ id: 1, username: 'first_bot' }, { id: '9223372036854775807', username: 'large_bot' }] } } })
})
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('shared Telegram Bot dropdown', () => {
  it('shows options without text entry or status requests and maps all to empty', async () => {
    await mount()
    expect(root.querySelector('input:not([type="hidden"])')).toBeNull()
    expect(root.querySelector('[role="combobox"]')).not.toBeNull()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(state.bots.map((bot: { id: string }) => bot.id)).toEqual(['1', '9223372036854775807'])
    state.select('all'); expect(selected.mock.calls).toEqual([['']])
    state.select('9223372036854775807')
    expect(selected.mock.calls[selected.mock.calls.length - 1]).toEqual(['9223372036854775807'])
    state.select('unknown')
    expect(selected.mock.calls).toHaveLength(2)
  })
  it('does not allow all in required mode and preserves disabled bots as query choices', async () => {
    mocks.list.mockResolvedValue({ data: { data: { items: [{ id: 4, username: 'stopped_bot', enabled: false }] } } })
    await mount(true)
    state.select('all'); expect(selected).not.toHaveBeenCalled()
    state.select('4'); expect(selected.mock.calls).toEqual([['4']])
    expect(root.querySelector('[role="combobox"]')?.getAttribute('aria-required')).toBe('true')
  })
  it('shows permission denial without requests', async () => {
    mocks.auth.allowed = false; await mount()
    expect(mocks.list).not.toHaveBeenCalled()
    expect(root.textContent).toContain('telegramBot.groups.instancePermission')
    expect(root.querySelector<HTMLButtonElement>('[role="combobox"]')?.disabled).toBe(true)
  })
  it('shows empty and failure states and retries the list explicitly', async () => {
    mocks.list.mockRejectedValueOnce(new Error('offline')); await mount(true)
    expect(root.querySelector('[role="alert"]')).not.toBeNull()
    expect(root.querySelector<HTMLButtonElement>('[role="combobox"]')?.disabled).toBe(true)
    mocks.list.mockResolvedValueOnce({ data: { data: { items: [] } } })
    await state.loadBots(); await flush()
    expect(root.querySelector('[role="alert"]')).toBeNull()
    expect(root.textContent).toContain('telegramBot.instances.empty')
  })
  it('invalidates stale account responses and rejects unsafe numeric IDs', async () => {
    let finish!: (value: unknown) => void
    mocks.list.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    await mount()
    mocks.list.mockResolvedValue({ data: { data: { items: [{ id: 9007199254740992, username: 'unsafe' }, { id: 2, username: 'second' }] } } })
    mocks.auth.token = 'second'; await flush()
    finish({ data: { data: { items: [{ id: 1, username: 'stale' }] } } }); await flush()
    expect(state.bots.map((bot: { id: string }) => bot.id)).toEqual(['2'])
    expect(selected.mock.calls).toContainEqual([''])
  })
})
