import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createI18n } from 'vue-i18n'
import TelegramBotSupportTasks from './TelegramBotSupportTasks.vue'
import { telegramSupportMessages } from '@/i18n/telegramSupport'
import { topicDeletionPermission, validTopicDeletion } from '@/api/telegramSupportTopic'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), allowed: true }))
vi.mock('@/api/client', () => ({ api: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ isSuper: false, get permissions() { return mocks.allowed ? [topicDeletionPermission] : [] }, hasPermission: () => true }) }))
let app: App | undefined, root: HTMLDivElement | undefined
const mapping = { id: '9', version: '5', chat_id: '-9007199254740993', thread_id: '9007199254740993', route_version: '3', state: 'open' }
/** Drain Vue updates and requests without timers or real HTTP. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount the real task dialog and select a trusted topic snapshot. */
async function mount() {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(TelegramBotSupportTasks)
  app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] }))
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: { 'zh-CN': { telegramSupport: telegramSupportMessages['zh-CN'] } } }))
  const instance = app.mount(root)
  const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
  state.resource = 'topic-operations'; state.botFilter = '2'; await flush(); await state.show(4); await flush()
  return state
}
/** Enter evidence and explicit consent using rendered controls. */
async function confirm() {
  const input = document.querySelector<HTMLInputElement>('#topic-delete-evidence')!
  input.value = 'ticket:verified'; input.dispatchEvent(new Event('input', { bubbles: true }))
  document.querySelector<HTMLButtonElement>('#topic-delete-confirm')!.click(); await flush()
  document.querySelector<HTMLElement>('[data-testid="topic-deletion"] button:last-child')!.click(); await flush()
}
beforeEach(() => {
  vi.clearAllMocks(); mocks.allowed = true
  mocks.get.mockImplementation((url: string) => Promise.resolve({ data: { data: url.endsWith('/4') ? { id: 4, bot_id: 2, session_id: 1, version: 7, operation_version: '7', state: 'succeeded', action: 'create', target_id: 8, route_version: 3, updated_at: '', mapping: { ...mapping } } : { items: [], total: 0 } } }))
  mocks.post.mockResolvedValue({ data: { data: { confirmed: true } } })
})
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark') })

describe('topic deletion dialog', () => {
  it('uses exact permission and sends lossless physical snapshot, never a Telegram delete', async () => {
    await mount(); expect(document.body.textContent).toContain('不会调用 Telegram 删除话题')
    expect(document.body.textContent).toContain(mapping.thread_id)
    document.documentElement.classList.add('dark'); await flush()
    await confirm()
    expect(mocks.post).toHaveBeenCalledWith('/admin/telegram-bot/support/topic-operations/4/confirm-deleted', { expected_version: '7', mapping_id: '9', mapping_version: '5', chat_id: mapping.chat_id, thread_id: mapping.thread_id, evidence: 'ticket:verified', confirmed: true })
  })
  it('clears draft and reloads on 409, requiring review again', async () => {
    const state = await mount(); mocks.post.mockRejectedValue({ response: { status: 409 } })
    await confirm()
    expect(state.conflict).toBe(true)
    expect(document.querySelector<HTMLInputElement>('#topic-delete-evidence')!.value).toBe('')
    expect(document.querySelector<HTMLFieldSetElement>('[data-testid="topic-deletion"] fieldset')!.disabled).toBe(true)
  })
  it('isolates late writes after a bot/category switch', async () => {
    const state = await mount(); let finish!: () => void
    mocks.post.mockImplementation(() => new Promise<void>(resolve => { finish = resolve }))
    await confirm(); state.resource = 'upstreams'; state.botFilter = '3'; await flush()
    const calls = mocks.get.mock.calls.length; finish(); await flush()
    expect(state.open).toBe(false); expect(state.task).toBeNull(); expect(mocks.get.mock.calls.length).toBe(calls)
  })
  it('denies unknown results and missing exact POST permission', async () => {
    mocks.allowed = false; const state = await mount()
    expect(document.querySelector('#topic-delete-evidence')).toBeNull()
    state.task = { ...state.task, state: 'unknown' }; await flush()
    expect(document.body.textContent).toContain('未知结果不能作为删除证据')
    expect(mocks.post).not.toHaveBeenCalled()
  })
  it('validates decimal and evidence boundaries without Number coercion', () => {
    const input = { expected_version: '7', mapping_id: '9', mapping_version: '5', chat_id: mapping.chat_id, thread_id: mapping.thread_id, evidence: 'case:verified', confirmed: true }
    expect(validTopicDeletion(input)).toBe(true)
    for (const patch of [{ thread_id: '9223372036854775808' }, { chat_id: '-9223372036854775809' }, { evidence: 'https://raw' }, { confirmed: false }]) expect(validTopicDeletion({ ...input, ...patch })).toBe(false)
  })
})
