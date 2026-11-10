import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive } from 'vue'
import { useTelegramSupportList } from './useTelegramSupportList'
import { useTelegramSupportSession } from './useTelegramSupportSession'
import { useTelegramSupportTask } from './useTelegramSupportTask'
import { supportContentPermission, supportResolvePermission } from '@/api/telegramSupport'
const mocks = vi.hoisted(() => ({ sessions: vi.fn(), session: vi.fn(), messages: vi.fn(), content: vi.fn(), tasks: vi.fn(), task: vi.fn(), resolve: vi.fn(), denied: [] as string[] }))
vi.mock('@/api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }))
vi.mock('@/api/telegramSupport', async original => ({ ...await original<typeof import('@/api/telegramSupport')>(), telegramSupportAPI: mocks }))
const auth = reactive({ isSuper: false, permissions: [] as string[], hasPermission: (key: string) => !mocks.denied.includes(key) })
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => auth }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: vi.fn() }))
const apps: ReturnType<typeof createApp>[] = []
/** Mount lifecycle-aware composables and return their state to the test. */
function model<T>(factory: () => T): T {
  let result!: T
  const app = createApp({ setup() { result = factory(); return () => null } })
  app.mount(document.createElement('div')); apps.push(app); return result
}
const session = { id: 1, bot_id: 2, user_id: '9007199254740993', chat_id: '9007199254740993', state: 'open', version: 1 }
const message = { id: 3, session_id: 1, bot_id: 2, sequence: 1, state: 'ready', retain_until: '2026-11-01T12:00:10Z', created_at: '', updated_at: '' }
const task = { id: 4, bot_id: 2, session_id: 1, state: 'unknown', version: 7, delivery_id: 9, kind: 'reply' }
beforeEach(() => {
  vi.clearAllMocks(); vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-01T12:00:00Z'))
  mocks.denied = []; auth.permissions = []; auth.isSuper = false
  mocks.session.mockResolvedValue({ data: { data: session } })
  mocks.sessions.mockResolvedValue({ data: { data: { items: [session], total: 1 } } })
  mocks.messages.mockResolvedValue({ data: { data: { items: [message], total: 1 } } })
  mocks.content.mockResolvedValue({ data: { data: { available: true, text: '<script>private</script>' } } })
  mocks.task.mockResolvedValue({ data: { data: task } })
  mocks.tasks.mockResolvedValue({ data: { data: { items: [task], total: 1 } } })
  mocks.resolve.mockResolvedValue({ data: { data: { resolved: true } } })
})
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); vi.useRealTimers() })
describe('audited support content', () => {
  it('requests the selected message page size and resets pagination on a new session', async () => {
    const detail = model(useTelegramSupportSession)
    await detail.show(1)
    detail.pageSize.value = 50
    await detail.loadMessages(1)
    expect(mocks.messages).toHaveBeenLastCalledWith(1, { page: 1, page_size: 50 })
    await detail.loadMessages(2)
    expect(mocks.messages).toHaveBeenLastCalledWith(1, { page: 2, page_size: 50 })
    await detail.show(1)
    expect(mocks.messages).toHaveBeenLastCalledWith(1, { page: 1, page_size: 50 })
  })
  it('loads metadata without content and keeps Telegram identifiers as strings', async () => {
    const list = model(() => useTelegramSupportList('sessions')); await list.load()
    const detail = model(useTelegramSupportSession); await detail.show(1)
    expect(detail.session.value?.user_id).toBe('9007199254740993'); expect(mocks.content).not.toHaveBeenCalled()
    auth.permissions = ['GET:/admin/telegram-bot/support/*']; await detail.viewContent(message)
    expect(mocks.content).not.toHaveBeenCalled()
    auth.permissions = [supportContentPermission]; await detail.viewContent(message)
    expect(mocks.content).toHaveBeenCalledExactlyOnceWith(1, 3)
    expect(detail.content.value?.text).toBe('<script>private</script>')
    await vi.advanceTimersByTimeAsync(10000)
    expect(detail.content.value).toEqual({ available: false, reason: 'expired' })
  })
  it('discards late content after closing and clears content on permission revocation', async () => {
    auth.permissions = [supportContentPermission]
    const detail = model(useTelegramSupportSession); await detail.show(1)
    let finish!: (result: unknown) => void
    mocks.content.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const pending = detail.viewContent(message); detail.open.value = false; await nextTick()
    finish({ data: { data: { available: true, text: 'secret' } } }); await pending
    expect(detail.content.value).toBeNull()
    await detail.show(1); await detail.viewContent(message)
    auth.permissions = []; await nextTick(); expect(detail.content.value).toBeNull()
  })
  it('shows cleared content and handles failed audit/read without keeping an old body', async () => {
    auth.isSuper = true
    const detail = model(useTelegramSupportSession); await detail.show(1)
    mocks.content.mockResolvedValueOnce({ data: { data: { available: false, reason: 'cleared' } } })
    await detail.viewContent(message); expect(detail.content.value?.reason).toBe('cleared')
    mocks.content.mockRejectedValueOnce({ response: { status: 500 } })
    await detail.viewContent(message); expect(detail.content.value).toBeNull(); expect(detail.contentError.value).toBe('telegramSupport.failed')
  })
  it('enforces separate list, detail and message permissions', async () => {
    mocks.denied = ['GET:/admin/telegram-bot/support/sessions', 'GET:/admin/telegram-bot/support/sessions/1']
    const list = model(() => useTelegramSupportList('sessions')); await list.load()
    const detail = model(useTelegramSupportSession); await detail.show(1)
    expect(mocks.sessions).not.toHaveBeenCalled(); expect(mocks.session).not.toHaveBeenCalled()
    mocks.denied = ['GET:/admin/telegram-bot/support/sessions/1/messages']; await detail.show(1)
    expect(mocks.messages).not.toHaveBeenCalled()
  })
})
describe('session operational filtering', () => {
  it('searches current inputs repeatedly and resets every filter with one first-page request', async () => {
    const list = model(() => useTelegramSupportList('sessions'))
    list.botFilter.value = '2'; list.stateFilter.value = 'open'; list.unrepliedFilter.value = 'true'
    list.blockedFilter.value = 'false'; list.topicFilter.value = 'true'; list.failureFilter.value = 'upstream'
    await nextTick(); expect(mocks.sessions).not.toHaveBeenCalled()
    await list.load(3); await list.search(); await list.search()
    expect(mocks.sessions).toHaveBeenCalledTimes(3)
    expect(mocks.sessions).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_id: 2, state: 'open', unreplied: true, blocked: false, has_topic: true, failure: 'upstream' })
    list.page.value = 3; await list.resetFilters(); await nextTick()
    expect(mocks.sessions).toHaveBeenCalledTimes(4); expect(list.page.value).toBe(1)
    expect(list.botFilter.value).toBe('')
    for (const filter of [list.stateFilter, list.unrepliedFilter, list.blockedFilter, list.topicFilter, list.failureFilter]) expect(filter.value).toBe('all')
    expect(mocks.sessions).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_id: undefined, state: undefined, unreplied: undefined, blocked: undefined, has_topic: undefined, failure: undefined })
    await list.resetFilters(); expect(mocks.sessions).toHaveBeenCalledTimes(5)
  })
  it.each(['success', 'failure'])('does not let an old session %s overwrite reset', async outcome => {
    const list = model(() => useTelegramSupportList('sessions'))
    let finish!: (value: unknown) => void, fail!: (reason: unknown) => void
    mocks.sessions.mockImplementationOnce(() => new Promise((resolve, reject) => { finish = resolve; fail = reject }))
    list.botFilter.value = '2'; const pending = list.search()
    await list.resetFilters()
    if (outcome === 'success') finish({ data: { data: { items: [], total: 99 } } })
    else fail(new Error('old request failed'))
    await pending
    expect(list.items.value).toEqual([session]); expect(list.total.value).toBe(1); expect(list.page.value).toBe(1)
    expect(list.error.value).toBe(''); expect(list.loading.value).toBe(false)
  })
  it('sends false explicitly, keeps paging server-owned and drops stale filter results', async () => {
    const list = model(() => useTelegramSupportList('sessions'))
    list.botFilter.value = '2'; list.unrepliedFilter.value = 'true'; list.blockedFilter.value = 'false'
    list.topicFilter.value = 'true'; list.failureFilter.value = 'upstream'
    await list.load(2)
    expect(mocks.sessions).toHaveBeenLastCalledWith({ page: 2, page_size: 20, bot_id: 2, state: undefined, unreplied: true, blocked: false, has_topic: true, failure: 'upstream' })
    let finish!: (value: unknown) => void
    mocks.sessions.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const pending = list.load(); list.failureFilter.value = 'none'
    expect(list.items.value).toEqual([]); expect(list.page.value).toBe(1)
    finish({ data: { data: { items: [session], total: 1 } } }); await pending
    expect(list.items.value).toEqual([])
    list.blockedFilter.value = 'invalid'; expect(list.validFilter.value).toBe(false)
    await list.load(); expect(mocks.sessions).toHaveBeenCalledTimes(2)
    expect(mocks.content).not.toHaveBeenCalled(); expect(mocks.resolve).not.toHaveBeenCalled()
  })
})
describe('unknown task reconciliation', () => {
  beforeEach(() => { auth.permissions = [supportResolvePermission] })
  it('rejects wildcard-only write permissions', async () => {
    auth.permissions = ['POST:/admin/telegram-bot/support/*']
    const detail = model(() => useTelegramSupportTask(vi.fn().mockResolvedValue(undefined)))
    await detail.show(4); Object.assign(detail.form, { decision: 'failed', evidence: 'case:review' })
    expect(detail.canResolve.value).toBe(false); await detail.resolve(); expect(mocks.resolve).not.toHaveBeenCalled()
  })
  it('submits four fields and refreshes only after a genuine API success', async () => {
    const reload = vi.fn().mockResolvedValue(undefined), detail = model(() => useTelegramSupportTask(reload))
    await detail.show(4); Object.assign(detail.form, { decision: 'delivered', evidence: 'ticket:review-1' })
    await detail.resolve()
    expect(mocks.resolve).toHaveBeenCalledExactlyOnceWith(4, { expected_version: 7, decision: 'delivered', evidence: 'ticket:review-1', delivery_id: 9 })
    expect(reload).toHaveBeenCalledOnce(); expect(detail.success.value).toBe('telegramSupport.resolved')
  })
  it('refreshes conflict state, clears the decision and never automatically resubmits', async () => {
    const reload = vi.fn().mockResolvedValue(undefined), detail = model(() => useTelegramSupportTask(reload))
    await detail.show(4); Object.assign(detail.form, { decision: 'failed', evidence: 'audit:review-1' })
    mocks.resolve.mockRejectedValueOnce({ response: { status: 409 } })
    mocks.task.mockResolvedValueOnce({ data: { data: { ...task, version: 8 } } })
    await detail.resolve(); expect(detail.task.value?.version).toBe(8); expect(detail.conflict.value).toBe(true)
    expect(detail.form.decision).toBe(''); expect(reload).toHaveBeenCalledOnce()
    await detail.resolve(); expect(mocks.resolve).toHaveBeenCalledOnce()
    detail.acknowledgeConflict(); expect(detail.conflict.value).toBe(false); expect(detail.valid.value).toBe(false)
  })
  it('does not resolve known outcomes or missing permissions and does not claim failed requests succeeded', async () => {
    const detail = model(() => useTelegramSupportTask(vi.fn().mockResolvedValue(undefined)))
    mocks.task.mockResolvedValueOnce({ data: { data: { ...task, state: 'delivered' } } })
    await detail.show(4); Object.assign(detail.form, { decision: 'delivered', evidence: 'ticket:review' }); await detail.resolve()
    expect(mocks.resolve).not.toHaveBeenCalled()
    await detail.show(4); Object.assign(detail.form, { decision: 'failed', evidence: 'case:review' })
    mocks.resolve.mockRejectedValueOnce({ response: { status: 403 } }); await detail.resolve()
    expect(detail.error.value).toBe('telegramSupport.failed'); expect(detail.success.value).toBe('')
  })
})
