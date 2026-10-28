import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { effectScope } from 'vue'
import { botState, useTelegramBotInstances, type BotInstance } from './useTelegramBotInstances'
const mocks = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn(), create: vi.fn(), status: vi.fn(), allowed: true }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBotInstances: mocks.get, patchTelegramBotInstance: mocks.patch, createTelegramBotInstance: mocks.create, getTelegramBotInstanceStatus: mocks.status } }))
const item = { id: 1, mode: 'internal', enabled: true, version: 3, state: 'running', last_seen_at: '2026-10-28T12:00:00Z' } as BotInstance
beforeEach(() => { vi.clearAllMocks(); mocks.allowed = true; mocks.get.mockResolvedValue({ data: { data: { items: [item] } } }); mocks.status.mockResolvedValue({ data: { data: {} } }) })
afterEach(() => { vi.useRealTimers() })
describe('managed bot instances', () => {
  it('marks expired and missing heartbeats stale but stopped bots stopped', () => {
    expect(botState(item, Date.parse('2026-10-28T12:02:00Z'))).toBe('stale')
    expect(botState({ ...item, last_seen_at: undefined })).toBe('stale')
    expect(botState({ ...item, enabled: false })).toBe('stale')
    expect(botState({ ...item, enabled: false }, Date.parse('2026-10-28T12:00:10Z'))).toBe('running')
    expect(botState({ ...item, state: 'stopped', last_seen_at: undefined })).toBe('stopped')
    expect(botState({ ...item, state: 'stopped', enabled: false }, Date.parse('2026-10-29T12:00:00Z'))).toBe('stopped')
    expect(botState(item, Date.parse('2026-10-28T12:00:44.999Z'))).toBe('running')
    expect(botState(item, Date.parse('2026-10-28T12:00:45Z'))).toBe('stale')
    expect(botState({ ...item, mode: 'external' })).toBe('external')
    expect(botState(item, Date.parse('2026-10-28T12:00:10Z'))).toBe('running')
  })
  it('unwraps data.items and sends expected version', async () => {
    const model = useTelegramBotInstances()
    await model.refresh()
    expect(model.instances.value[0]?.id).toBe(1)
    await model.patch(item, { enabled: false })
    expect(mocks.patch).toHaveBeenCalledWith(1, { expected_version: 3, enabled: false })
  })
  it('keeps conflicts visible and does not silently retry', async () => {
    mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    const model = useTelegramBotInstances()
    expect(await model.patch(item, { token: 'secret' })).toBe(false)
    expect(model.error.value).toContain('conflict')
    expect(mocks.patch).toHaveBeenCalledTimes(1)
  })
  it('polls status every ten seconds without overlapping and stops on disposal', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const model = scope.run(() => useTelegramBotInstances())!
    model.startPolling()
    await model.refresh()
    expect(mocks.status).toHaveBeenCalledTimes(1)
    let resolveStatus!: (value: unknown) => void
    mocks.status.mockImplementationOnce(() => new Promise(resolve => { resolveStatus = resolve }))
    await vi.advanceTimersByTimeAsync(10000)
    await vi.advanceTimersByTimeAsync(30000)
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(mocks.status).toHaveBeenCalledTimes(2)
    const previous = model.instances.value
    const pending = model.refresh()
    scope.stop()
    resolveStatus({ data: { data: { state: 'stopped' } } })
    await pending
    await vi.advanceTimersByTimeAsync(30000)
    expect(model.instances.value).toBe(previous)
    expect(model.instances.value[0]?.state).toBe('running')
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })
  it('publishes fresh status on each completed poll', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const model = scope.run(() => useTelegramBotInstances())!
    model.startPolling()
    await model.refresh()
    mocks.status.mockResolvedValueOnce({ data: { data: { state: 'stopped', enabled: false } } })
    await vi.advanceTimersByTimeAsync(10000)
    expect(model.instances.value[0]?.state).toBe('stopped')
    expect(model.instances.value[0]?.enabled).toBe(false)
    expect(mocks.status).toHaveBeenCalledTimes(2)
    scope.stop()
  })
  it('does not start status requests after disposal during list fetch', async () => {
    let resolveList!: (value: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve }))
    const scope = effectScope()
    const model = scope.run(() => useTelegramBotInstances())!
    const pending = model.refresh()
    scope.stop()
    resolveList({ data: { data: { items: [item] } } })
    await pending
    expect(mocks.status).not.toHaveBeenCalled()
    expect(model.instances.value).toEqual([])
  })
  it('keeps an editing snapshot and conflict feedback intact across refreshes', async () => {
    const model = useTelegramBotInstances()
    await model.refresh()
    const editing = model.instances.value[0]!
    mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    await model.patch(editing, { token: 'draft' })
    mocks.status.mockResolvedValueOnce({ data: { data: { version: 4, state: 'stopping' } } })
    await model.refresh()
    expect(editing.version).toBe(3)
    expect(editing.state).toBe('running')
    expect(model.instances.value[0]?.version).toBe(4)
    expect(model.error.value).toContain('conflict')
  })
  it('retains the last snapshot on network failure and recovers on the next poll', async () => {
    const model = useTelegramBotInstances()
    await model.refresh()
    mocks.get.mockRejectedValueOnce(new Error('offline'))
    await model.refresh()
    expect(model.instances.value[0]?.id).toBe(1)
    expect(model.refreshError.value).toContain('failed')
    await model.refresh()
    expect(model.refreshError.value).toBe('')
  })
  it('waits for an in-flight poll then refreshes after mutation', async () => {
    let resolveList!: (value: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve }))
    const model = useTelegramBotInstances()
    const poll = model.refresh()
    const mutation = model.patch(item, { enabled: false })
    expect(mocks.patch).not.toHaveBeenCalled()
    resolveList({ data: { data: { items: [item] } } })
    await poll
    await mutation
    expect(mocks.patch).toHaveBeenCalledTimes(1)
    expect(mocks.get).toHaveBeenCalledTimes(2)
  })
  it('does not call APIs without permission', async () => {
    mocks.allowed = false
    const model = useTelegramBotInstances()
    await model.refresh(); await model.patch(item, { enabled: false }); await model.create('secret')
    expect(mocks.get).not.toHaveBeenCalled(); expect(mocks.patch).not.toHaveBeenCalled(); expect(mocks.create).not.toHaveBeenCalled()
  })
})
