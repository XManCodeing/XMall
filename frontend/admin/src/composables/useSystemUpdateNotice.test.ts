import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref } from 'vue'
import { useSystemUpdateNotice } from './useSystemUpdateNotice'

const mocks = vi.hoisted(() => ({ check: vi.fn(), auth: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { checkSystemUpdate: mocks.check } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let scope: ReturnType<typeof effectScope>
const hour = 3600000
/** Drain reactive jobs. @returns Completion promise. */
async function flush() { for (let index = 0; index < 5; index++) { await Promise.resolve(); await nextTick() } }
/** Create a scoped poller. @returns Model and manual-dialog state. */
function mount() {
  scope = effectScope()
  const open = ref(false)
  return { model: scope.run(() => useSystemUpdateNotice(open))!, open }
}
beforeEach(() => {
  vi.useFakeTimers(); vi.clearAllMocks()
  mocks.auth = reactive({ token: 'test-session', allowed: true, hasPermission: () => mocks.auth.allowed })
  mocks.check.mockResolvedValue({ data: { data: { has_update: true, latest_version: 'v2' } } })
})
afterEach(() => { scope?.stop(); vi.useRealTimers(); vi.restoreAllMocks() })
describe('silent hourly update notice', () => {
  it('checks on entry and hourly, then disposes the timer', async () => {
    const { model } = mount(); await flush()
    expect(mocks.check).toHaveBeenCalledExactlyOnceWith(undefined, { silent: true })
    expect(model.hasUpdate.value).toBe(true)
    await vi.advanceTimersByTimeAsync(hour - 1); expect(mocks.check).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(mocks.check).toHaveBeenCalledTimes(2)
    scope.stop(); await vi.advanceTimersByTimeAsync(hour); expect(mocks.check).toHaveBeenCalledTimes(2)
  })
  it('skips manual-dialog checks and accepts its latest result', async () => {
    const { model, open } = mount(); await flush()
    open.value = true
    await vi.advanceTimersByTimeAsync(hour); expect(mocks.check).toHaveBeenCalledTimes(1)
    model.accept({ has_update: false, latest_version: 'v2' }); expect(model.hasUpdate.value).toBe(false)
    open.value = false; await vi.advanceTimersByTimeAsync(hour); expect(mocks.check).toHaveBeenCalledTimes(2)
  })
  it('retains a known update on failure and retries next hour', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { model } = mount(); await flush()
    mocks.check.mockRejectedValueOnce(new Error('offline'))
    await vi.advanceTimersByTimeAsync(hour)
    expect(model.hasUpdate.value).toBe(true)
    await vi.advanceTimersByTimeAsync(hour); expect(mocks.check).toHaveBeenCalledTimes(3)
  })
  it('does not poll without permission or after logout', async () => {
    mocks.auth.allowed = false
    const { model } = mount(); await flush(); expect(mocks.check).not.toHaveBeenCalled()
    mocks.auth.allowed = true; await flush(); expect(mocks.check).toHaveBeenCalledTimes(1)
    mocks.auth.token = ''; await flush(); expect(model.hasUpdate.value).toBe(false)
    await vi.advanceTimersByTimeAsync(hour); expect(mocks.check).toHaveBeenCalledTimes(1)
  })
  it('ignores stale responses when a manual check supersedes them', async () => {
    let resolve!: (value: unknown) => void
    mocks.check.mockImplementationOnce(() => new Promise(accept => { resolve = accept }))
    const { model, open } = mount()
    open.value = true; model.accept({ has_update: false, latest_version: 'v3' }); open.value = false
    resolve({ data: { data: { has_update: true, latest_version: 'v2' } } }); await flush()
    expect(model.hasUpdate.value).toBe(false); expect(model.latestVersion.value).toBe('v3')
  })
  it('does not overlap pending requests and discards results after logout', async () => {
    let resolve!: (value: unknown) => void
    mocks.check.mockImplementationOnce(() => new Promise(accept => { resolve = accept }))
    const { model } = mount(); await vi.advanceTimersByTimeAsync(hour)
    expect(mocks.check).toHaveBeenCalledTimes(1)
    mocks.auth.token = ''; await flush()
    resolve({ data: { data: { has_update: true, latest_version: 'v2' } } }); await flush()
    expect(model.hasUpdate.value).toBe(false)
  })
})
