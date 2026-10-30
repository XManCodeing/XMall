import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { effectScope } from 'vue'
import { botState, botStateVariant, useTelegramBotInstances, type BotInstance } from './useTelegramBotInstances'
const mocks = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn(), create: vi.fn(), status: vi.fn(), test: vi.fn(), success: vi.fn(), info: vi.fn(), error: vi.fn(), allowed: true }))
vi.mock('@/utils/notify', () => ({
  notifySuccess: mocks.success, notifyInfo: mocks.info,
  notifyErrorOnce: (error: { __notified?: boolean } | undefined, message: string) => {
    if (!error?.__notified) mocks.error(message)
  },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: { bot: string }) => params ? `${key}: ${params.bot}` : key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBotInstances: mocks.get, patchTelegramBotInstance: mocks.patch, createTelegramBotInstance: mocks.create, testTelegramBotInstance: mocks.test, getTelegramBotInstanceStatus: mocks.status } }))
const item = { id: 1, telegram_id: 123, username: 'example_bot', mode: 'internal', enabled: true, version: 3, state: 'running', last_seen_at: '2026-10-28T12:00:00Z' } as BotInstance
beforeEach(() => { vi.clearAllMocks(); mocks.allowed = true; mocks.get.mockResolvedValue({ data: { data: { items: [item] } } }); mocks.status.mockResolvedValue({ data: { data: {} } }) })
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
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
  it('colors actual runtime health rather than desired enabled state', () => {
    const now = Date.parse('2026-10-28T12:00:10Z')
    expect(botStateVariant(item, now)).toBe('success')
    expect(botStateVariant({ ...item, enabled: false }, now)).toBe('success')
    for (const state of ['stopped', 'error']) {
      expect(botStateVariant({ ...item, state }, now)).toBe('destructive')
    }
    expect(botStateVariant(item, now + 60000)).toBe('destructive')
    for (const state of ['starting', 'stopping', 'unknown']) {
      expect(botStateVariant({ ...item, state }, now)).toBe('secondary')
    }
    expect(botStateVariant({ ...item, mode: 'external' }, now)).toBe('secondary')
  })
  it('unwraps data.items and sends expected version', async () => {
    const model = useTelegramBotInstances()
    await model.refresh()
    expect(model.instances.value[0]?.id).toBe(1)
    await model.patch(item, { enabled: false })
    expect(mocks.patch).toHaveBeenCalledWith(1, { expected_version: 3, enabled: false })
    expect(mocks.success).toHaveBeenCalledWith('telegramBot.instances.success')
  })
  it('keeps conflicts visible and does not silently retry', async () => {
    mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    const model = useTelegramBotInstances()
    expect(await model.patch(item, { token: 'secret' })).toBe(false)
    expect(mocks.error).toHaveBeenCalledExactlyOnceWith('telegramBot.instances.conflict')
    expect(model).not.toHaveProperty('error')
    expect(model).not.toHaveProperty('notice')
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
  it.each([{ items: [item] }, { items: [] }])('keeps loaded snapshots stable during background and manual refreshes: $items', async ({ items }) => {
    vi.useFakeTimers()
    mocks.get.mockResolvedValue({ data: { data: { items } } })
    const scope = effectScope()
    const model = scope.run(() => useTelegramBotInstances())!
    try {
      model.startPolling()
      expect(model.loading.value).toBe(true)
      await model.refresh()
      expect(model.loading.value).toBe(false)
      const snapshot = model.instances.value
      let resolveList!: (value: unknown) => void
      mocks.get.mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve }))
      await vi.advanceTimersByTimeAsync(10000)
      expect(model.loading.value).toBe(false)
      expect(model.instances.value).toBe(snapshot)
      const pending = model.refresh()
      expect(model.loading.value).toBe(false)
      expect(mocks.get).toHaveBeenCalledTimes(2)
      resolveList({ data: { data: { items } } })
      await pending
      expect(model.loading.value).toBe(false)
    } finally { scope.stop() }
  })
  it('pauses hidden-page polling and refreshes on return, removing its listener on disposal', async () => {
    vi.useFakeTimers()
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    const scope = effectScope()
    const model = scope.run(() => useTelegramBotInstances())!
    try {
      model.startPolling()
      model.startPolling()
      await model.refresh()
      hidden.mockReturnValue(true)
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(30000)
      expect(mocks.get).toHaveBeenCalledTimes(1)
      hidden.mockReturnValue(false)
      document.dispatchEvent(new Event('visibilitychange'))
      await model.refresh()
      expect(mocks.get).toHaveBeenCalledTimes(2)
    } finally { scope.stop() }
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(10000)
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })
  it('retains initial loading feedback when retrying a failed first request', async () => {
    mocks.get.mockRejectedValueOnce(new Error('offline'))
    const model = useTelegramBotInstances()
    await model.refresh()
    expect(model.loading.value).toBe(false)
    const retry = model.refresh()
    expect(model.loading.value).toBe(true)
    await retry
    expect(model.loading.value).toBe(false)
    expect(model.refreshError.value).toBe('')
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
    expect(mocks.error).toHaveBeenCalledExactlyOnceWith('telegramBot.instances.conflict')
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
  it('shows testing state, prevents duplicate clicks and reports the returned bot without reloading', async () => {
    let resolveTest!: (value: unknown) => void
    mocks.test.mockImplementationOnce(() => new Promise(resolve => { resolveTest = resolve }))
    const model = useTelegramBotInstances()
    const pending = model.test(item)
    expect(model.testingId.value).toBe(item.id)
    expect(model.busy.value).toBe(true)
    expect(await model.test(item)).toBe(false)
    expect(mocks.test).toHaveBeenCalledTimes(1)
    resolveTest({ data: { data: { ok: true, telegram_id: 123, username: 'verified_bot', webhook_present: false } } })
    expect(await pending).toBe(true)
    expect(model.testingId.value).toBeNull()
    expect(model.busy.value).toBe(false)
    expect(mocks.success).toHaveBeenCalledExactlyOnceWith('telegramBot.instances.testSuccess: @verified_bot')
    expect(mocks.get).not.toHaveBeenCalled()
    await model.refresh()
    expect(mocks.success).toHaveBeenCalledTimes(1)
  })
  it('warns about configured webhooks instead of a plain success notification', async () => {
    mocks.test.mockResolvedValueOnce({ data: { data: { ok: true, telegram_id: 123, username: '', webhook_present: true } } })
    const model = useTelegramBotInstances()
    expect(await model.test({ ...item, username: '' })).toBe(true)
    expect(mocks.info).toHaveBeenCalledExactlyOnceWith('telegramBot.instances.testSuccess: 123 telegramBot.instances.testWebhook')
    expect(mocks.success).not.toHaveBeenCalled()
  })
  it.each([{}, { ok: false }, { ok: true, telegram_id: 999, webhook_present: false }])('rejects invalid test results: %j', async result => {
    mocks.test.mockResolvedValueOnce({ data: { data: result } })
    const model = useTelegramBotInstances()
    expect(await model.test(item)).toBe(false)
    expect(mocks.success).not.toHaveBeenCalled()
    expect(mocks.error).toHaveBeenCalledExactlyOnceWith('telegramBot.instances.testFailed: @example_bot')
  })
  it.each([false, true])('keeps failure feedback without duplicating API notifications: %s', async notified => {
    mocks.test.mockRejectedValueOnce({ __notified: notified, message: 'sensitive upstream details' })
    const model = useTelegramBotInstances()
    expect(await model.test(item)).toBe(false)
    if (!notified) expect(mocks.error).toHaveBeenCalledWith('telegramBot.instances.testFailed: @example_bot')
    expect(model).not.toHaveProperty('error')
    expect(model).not.toHaveProperty('notice')
    expect(model.testingId.value).toBeNull()
    expect(mocks.error).toHaveBeenCalledTimes(notified ? 0 : 1)
  })
  it('does not publish test feedback after leaving the page', async () => {
    let resolveTest!: (value: unknown) => void
    mocks.test.mockImplementationOnce(() => new Promise(resolve => { resolveTest = resolve }))
    const scope = effectScope()
    const model = scope.run(() => useTelegramBotInstances())!
    const pending = model.test(item)
    scope.stop()
    resolveTest({ data: { data: { ok: true, telegram_id: 123, username: 'example_bot', webhook_present: false } } })
    expect(await pending).toBe(false)
    expect(mocks.success).not.toHaveBeenCalled()
    expect(mocks.info).not.toHaveBeenCalled()
    expect(model).not.toHaveProperty('notice')
  })
  it('does not call APIs without permission', async () => {
    mocks.allowed = false
    const model = useTelegramBotInstances()
    await model.refresh(); await model.patch(item, { enabled: false }); await model.create('secret'); await model.test(item)
    expect(mocks.test).not.toHaveBeenCalled()
    expect(mocks.get).not.toHaveBeenCalled(); expect(mocks.patch).not.toHaveBeenCalled(); expect(mocks.create).not.toHaveBeenCalled()
  })
})
