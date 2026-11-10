import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, ref } from 'vue'
import { useTelegramSupportList } from './useTelegramSupportList'
import { useTelegramSupportTask } from './useTelegramSupportTask'
import { supportBase, supportResolvePermission, type SupportTaskResource } from '@/api/telegramSupport'
const mocks = vi.hoisted(() => ({ tasks: vi.fn(), task: vi.fn(), resolve: vi.fn(), upstreams: vi.fn(), upstream: vi.fn(), resolveUpstream: vi.fn(), topics: vi.fn(), topic: vi.fn(), resolveTopic: vi.fn() }))
vi.mock('@/api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }))
vi.mock('@/api/telegramSupport', async original => ({ ...await original<typeof import('@/api/telegramSupport')>(), telegramSupportAPI: mocks }))
const auth = reactive({ isSuper: false, permissions: [] as string[], denied: [] as string[], hasPermission: (key: string) => !auth.denied.includes(key) })
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => auth }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const apps: ReturnType<typeof createApp>[] = []
/** Mount a lifecycle-aware composable and return its model for assertions. */
function model<T>(factory: () => T): T {
  let result!: T
  const app = createApp({ setup() { result = factory(); return () => null } })
  app.mount(document.createElement('div')); apps.push(app); return result
}
/** Wrap metadata in the real client response shape. */
const response = (data: unknown) => ({ data: { data } })
const row = { id: 4, bot_id: 2, session_id: 1, state: 'unknown', version: 7, delivery_id: 9, kind: 'reply' }
const topic = { id: 4, bot_id: 2, session_id: 1, state: 'unknown', version: 7, action: 'create', target_id: 8, route_version: 3 }
beforeEach(() => {
  vi.resetAllMocks(); auth.permissions = [supportResolvePermission]; auth.denied = []; auth.isSuper = false
  mocks.task.mockResolvedValue(response(row)); mocks.upstream.mockResolvedValue(response(row)); mocks.topic.mockResolvedValue(response(topic))
  for (const fetch of [mocks.tasks, mocks.upstreams, mocks.topics]) fetch.mockResolvedValue(response({ items: [row], total: 23 }))
})
afterEach(() => apps.splice(0).forEach(app => app.unmount()))

describe('independent support task lists', () => {
  it('resets downstream filters and requests page one exactly once', async () => {
    const resource = ref<SupportTaskResource>('downstreams'), list = model(() => useTelegramSupportList(resource))
    list.botFilter.value = '2'; list.stateFilter.value = 'unknown'
    await nextTick(); expect(mocks.tasks).not.toHaveBeenCalled()
    await list.load(3); await list.search(); await list.search()
    expect(mocks.tasks).toHaveBeenCalledTimes(3)
    expect(mocks.tasks).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_id: 2, state: 'unknown' })
    list.page.value = 3; await list.resetFilters(); await nextTick()
    expect(mocks.tasks).toHaveBeenCalledTimes(4)
    expect(mocks.tasks).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_id: undefined, state: undefined })
    expect(list.page.value).toBe(1); expect(list.botFilter.value).toBe(''); expect(list.stateFilter.value).toBe('all')
    await list.resetFilters(); expect(mocks.tasks).toHaveBeenCalledTimes(5)
  })
  it.each(['upstreams', 'topic-operations'] as const)('resets %s without bypassing required bot or changing resource', async kind => {
    const resource = ref<SupportTaskResource>(kind), list = model(() => useTelegramSupportList(resource))
    const fetch = kind === 'upstreams' ? mocks.upstreams : mocks.topics
    list.botFilter.value = '2'; list.stateFilter.value = 'unknown'; await list.load(3)
    await list.resetFilters(); await nextTick()
    expect(resource.value).toBe(kind); expect(list.botFilter.value).toBe(''); expect(list.stateFilter.value).toBe('all')
    expect(list.page.value).toBe(1); expect(list.items.value).toEqual([]); expect(list.validFilter.value).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(1); expect(mocks.tasks).not.toHaveBeenCalled()
    list.botFilter.value = '3'; await list.search()
    expect(fetch).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_id: 3, state: undefined })
  })
  it('keeps failed resets on page one and supports explicit retry', async () => {
    const list = model(() => useTelegramSupportList('downstreams')); await list.load(3)
    mocks.tasks.mockRejectedValueOnce(new Error('network')); await list.resetFilters()
    expect(list.page.value).toBe(1); expect(list.error.value).toBe('telegramSupport.failed')
    expect(list.items.value).toEqual([]); expect(list.loading.value).toBe(false)
    await list.search(); expect(list.error.value).toBe(''); expect(list.items.value).toHaveLength(1)
  })
  it('requires Bot for upstream/topic and sends bounded pagination and finite state', async () => {
    const resource = ref<SupportTaskResource>('upstreams'), list = model(() => useTelegramSupportList(resource))
    await list.load(); expect(mocks.upstreams).not.toHaveBeenCalled(); expect(list.validFilter.value).toBe(false)
    list.botFilter.value = '2'; list.stateFilter.value = 'ready'; await list.load(2)
    expect(mocks.upstreams).toHaveBeenLastCalledWith({ page: 2, page_size: 20, bot_id: 2, state: 'ready' })
    resource.value = 'topic-operations'
    expect(list.stateFilter.value).toBe('all'); expect(list.page.value).toBe(1); expect(list.items.value).toEqual([])
    list.stateFilter.value = 'ready'; await list.load(); expect(mocks.topics).not.toHaveBeenCalled()
    list.stateFilter.value = 'creating'; await list.load()
    expect(mocks.topics).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_id: 2, state: 'creating' })
    list.botFilter.value = '9007199254740993'; expect(list.validFilter.value).toBe(false)
  })
  it('rejects late list results across resource and Bot changes', async () => {
    const resource = ref<SupportTaskResource>('downstreams'), list = model(() => useTelegramSupportList(resource))
    let finish!: (value: unknown) => void
    mocks.tasks.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const pending = list.load(); resource.value = 'upstreams'; list.botFilter.value = '2'; await list.load()
    finish(response({ items: [{ ...row, id: 99 }], total: 99 })); await pending
    expect(list.items.value[0]?.id).toBe(4); expect(list.total.value).toBe(23)
    mocks.upstreams.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const pendingBot = list.load(); list.botFilter.value = '3'
    finish(response({ items: [row], total: 1 })); await pendingBot; expect(list.items.value).toEqual([])
  })
  it.each(['downstreams', 'upstreams', 'topic-operations'] as const)('separates %s list and detail RBAC', async resource => {
    auth.denied = [`GET:${supportBase}/${resource}`, `GET:${supportBase}/${resource}/4`]
    const list = model(() => useTelegramSupportList(resource)); list.botFilter.value = '2'; await list.load()
    const detail = model(() => useTelegramSupportTask(vi.fn(), resource)); await detail.show(4)
    expect(list.items.value).toEqual([]); expect(detail.task.value).toBeNull()
    expect(mocks.tasks).not.toHaveBeenCalled(); expect(mocks.upstreams).not.toHaveBeenCalled(); expect(mocks.topics).not.toHaveBeenCalled()
    expect(mocks.task).not.toHaveBeenCalled(); expect(mocks.upstream).not.toHaveBeenCalled(); expect(mocks.topic).not.toHaveBeenCalled()
  })
})

describe('task reconciliation boundaries', () => {
  it.each(['downstreams', 'upstreams', 'topic-operations'] as const)('requires %s POST and unified exact resolve authority', async resource => {
    const detail = model(() => useTelegramSupportTask(vi.fn(), resource)); await detail.show(4)
    expect(detail.canResolve.value).toBe(true)
    auth.denied = [`POST:${supportBase}/${resource}/4/resolve`]; expect(detail.canResolve.value).toBe(false)
    auth.denied = []; auth.permissions = [`POST:${supportBase}/${resource}/:id/resolve`].filter(permission => permission !== supportResolvePermission)
    expect(detail.canResolve.value).toBe(false)
    auth.permissions = [`POST:${supportBase}/*`]; expect(detail.canResolve.value).toBe(false)
    auth.permissions = [supportResolvePermission]; expect(detail.canResolve.value).toBe(true)
    auth.denied = [`GET:${supportBase}/${resource}/4`]; expect(detail.task.value).toBeNull()
  })
  it('blocks known outcomes, version zero, missing delivery proof and failed requests', async () => {
    const detail = model(() => useTelegramSupportTask(vi.fn(), 'upstreams'))
    for (const change of [{ version: 0 }, { state: 'delivered' }, { delivery_id: 0 }]) {
      mocks.upstream.mockResolvedValueOnce(response({ ...row, ...change })); await detail.show(4)
      Object.assign(detail.form, { decision: 'delivered', evidence: 'audit:review' }); await detail.resolve()
      expect(detail.valid.value).toBe(false); expect(mocks.resolveUpstream).not.toHaveBeenCalled()
    }
    await detail.show(4); Object.assign(detail.form, { decision: 'delivered', evidence: 'audit:review' })
    mocks.resolveUpstream.mockRejectedValueOnce({ response: { status: 403 } }); await detail.resolve()
    expect(detail.success.value).toBe(''); expect(detail.error.value).toBe('telegramSupport.failed')
  })
  it('submits a topic int64 string and resets no-effect confirmation on a decision change', async () => {
    const detail = model(() => useTelegramSupportTask(vi.fn().mockResolvedValue(undefined), 'topic-operations'))
    await detail.show(4); Object.assign(detail.form, { decision: 'failed', evidence: 'case:review' })
    expect(detail.valid.value).toBe(false); detail.form.noEffect = true; expect(detail.valid.value).toBe(true)
    detail.form.threadID = '1'; expect(detail.valid.value).toBe(false)
    detail.form.decision = 'succeeded'; expect(detail.form.noEffect).toBe(false); expect(detail.form.threadID).toBe('')
    detail.form.threadID = '9223372036854775807'; expect(detail.valid.value).toBe(true); await detail.resolve()
    expect(mocks.resolveTopic).toHaveBeenCalledExactlyOnceWith(4, { expected_version: 7, decision: 'succeeded', evidence: 'case:review', thread_id: '9223372036854775807', no_effect: false })
    expect(mocks.resolve).not.toHaveBeenCalled(); expect(mocks.resolveUpstream).not.toHaveBeenCalled()
  })
  it.each(['upstreams', 'topic-operations'] as const)('refreshes only %s on 409 and clears all conclusions', async resource => {
    const reload = vi.fn().mockResolvedValue(undefined), detail = model(() => useTelegramSupportTask(reload, resource))
    const fetch = resource === 'upstreams' ? mocks.upstream : mocks.topic
    const resolve = resource === 'upstreams' ? mocks.resolveUpstream : mocks.resolveTopic
    await detail.show(4); Object.assign(detail.form, { decision: 'failed', evidence: 'case:review', noEffect: true })
    resolve.mockRejectedValueOnce({ response: { status: 409 } }); fetch.mockResolvedValueOnce(response({ ...(resource === 'upstreams' ? row : topic), version: 8 }))
    await detail.resolve()
    expect(detail.task.value?.version).toBe(8); expect(detail.conflict.value).toBe(true)
    expect(detail.form.decision).toBe(''); expect(detail.form.evidence).toBe(''); expect(detail.form.noEffect).toBe(false); expect(detail.form.threadID).toBe('')
    expect(reload).toHaveBeenCalledOnce(); expect(mocks.task).not.toHaveBeenCalled()
    await detail.resolve(); expect(resolve).toHaveBeenCalledOnce()
    detail.acknowledgeConflict(); expect(detail.valid.value).toBe(false)
  })
  it('clears old detail/drafts synchronously and ignores late reads and writes on type/Bot switch', async () => {
    const resource = ref<SupportTaskResource>('downstreams'), bot = ref('2'), reload = vi.fn()
    const detail = model(() => useTelegramSupportTask(reload, resource, bot))
    let finish!: (value: unknown) => void
    mocks.task.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const pending = detail.show(4); resource.value = 'topic-operations'; await detail.show(4)
    finish(response(row)); await pending; expect(detail.task.value).toEqual(topic)
    Object.assign(detail.form, { decision: 'succeeded', evidence: 'audit:review', threadID: '9007199254740993' })
    mocks.resolveTopic.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const writing = detail.resolve(); resource.value = 'upstreams'
    expect(detail.task.value).toBeNull(); expect(detail.form.evidence).toBe(''); expect(detail.saving.value).toBe(false)
    await detail.show(4); finish(response({ resolved: true })); await writing
    expect(detail.task.value).toEqual(row); expect(detail.success.value).toBe(''); expect(reload).not.toHaveBeenCalled()
    mocks.upstream.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const botPending = detail.show(4); bot.value = '3'; finish(response(row)); await botPending
    expect(detail.task.value).toBeNull(); expect(detail.open.value).toBe(false)
    await nextTick(); expect(detail.form.decision).toBe('')
  })
})
