import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, KeepAlive, nextTick, reactive, ref, type App } from 'vue'
import { useMembershipAttention } from './useMembershipAttention'

const mocks = vi.hoisted(() => ({ tasks: vi.fn(), reviews: vi.fn(), lifecycle: vi.fn(), auth: {} as any }))
vi.mock('@/api/telegramMembership', () => ({
  membershipBase: '/admin/telegram-bot/membership',
  telegramMembershipAPI: { accessTasks: mocks.tasks, accessReviews: mocks.reviews, lifecycleTasks: mocks.lifecycle },
}))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App | undefined
let root: HTMLDivElement | undefined
/** Flush component lifecycle and asynchronous readers. @returns Settled microtasks. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount real composable without unrelated page requests.
 * @param sections Optional page-specific summaries. @returns Attention state and refresh.
 */
function mount(sections?: Parameters<typeof useMembershipAttention>[0]) {
  let attention!: ReturnType<typeof useMembershipAttention>
  root = document.createElement('div'); document.body.append(root)
  app = createApp({ setup() { attention = useMembershipAttention(sections); return () => null } })
  app.mount(root)
  return attention
}
beforeEach(() => {
  vi.useFakeTimers()
  mocks.auth = reactive({ token: 'session', isSuper: true, permissions: [] as string[], hasPermission: () => true })
  mocks.tasks.mockResolvedValue({ data: { data: { attention_count: 8 } } })
  mocks.reviews.mockResolvedValue({ data: { data: { attention_count: 2 } } })
  mocks.lifecycle.mockResolvedValue({ data: { data: { attention_count: 0 } } })
})
afterEach(() => { app?.unmount(); root?.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.resetAllMocks() })

describe('membership tab exception summaries', () => {
  it('only queries sections present on the groups page', async () => {
    const attention = mount(['tasks', 'reviews']); await attention.refresh()
    expect(mocks.tasks).toHaveBeenCalledExactlyOnceWith({ summary: 1 })
    expect(mocks.reviews).toHaveBeenCalledExactlyOnceWith({ summary: 1 })
    expect(mocks.lifecycle).not.toHaveBeenCalled()
    expect(attention.states.lifecycle.count).toBeNull()
  })
  it('loads all totals before opening sections without any bot or page restriction', async () => {
    const attention = mount(); await attention.refresh(); await flush()
    expect(attention.states.tasks.count).toBe(8)
    expect(attention.states.reviews.count).toBe(2)
    expect(attention.states.lifecycle.count).toBe(0)
    for (const reader of [mocks.tasks, mocks.reviews, mocks.lifecycle]) {
      expect(reader).toHaveBeenCalledExactlyOnceWith({ summary: 1 })
    }
  })
  it('isolates permission denial and discards late results after authorization changes', async () => {
    let resolve!: (value: unknown) => void
    mocks.tasks.mockReturnValue(new Promise(done => { resolve = done }))
    const attention = mount(); void attention.refresh(); await flush()
    mocks.auth.hasPermission = () => false
    mocks.auth.permissions = ['revoked']
    resolve({ data: { data: { attention_count: 99 } } }); await flush()
    expect(attention.states.tasks.count).toBeNull()
    expect(attention.states.tasks.loading).toBe(false)
    expect(attention.states.reviews.count).toBeNull()
    expect(mocks.tasks).toHaveBeenCalledTimes(1)
  })
  it('does not request unauthorized summaries', async () => {
    mocks.auth.hasPermission = (permission: string) => permission.endsWith('/access/reviews')
    const attention = mount(); await attention.refresh()
    expect(mocks.tasks).not.toHaveBeenCalled()
    expect(mocks.lifecycle).not.toHaveBeenCalled()
    expect(attention.states.reviews.count).toBe(2)
  })
  it.each([undefined, -1, '4', 1.5, Number.MAX_SAFE_INTEGER + 1])('shows unavailable rather than zero for invalid count %s', async count => {
    mocks.tasks.mockResolvedValue({ data: { data: { attention_count: count } } })
    const attention = mount(); await attention.refresh()
    expect(attention.states.tasks).toEqual({ count: null, loading: false, error: true })
    expect(attention.states.reviews.count).toBe(2)
  })
  it('marks failures unknown and clears stale badges after a successful refresh', async () => {
    const attention = mount(); await attention.refresh()
    mocks.tasks.mockRejectedValueOnce(new Error('offline'))
    await attention.refresh()
    expect(attention.states.tasks.error).toBe(true)
    expect(attention.states.tasks.count).toBeNull()
    mocks.tasks.mockResolvedValue({ data: { data: { attention_count: 0 } } })
    await attention.refresh()
    expect(attention.states.tasks).toEqual({ count: 0, loading: false, error: false })
  })
  it('stops automatic retries after an error until an explicit refresh', async () => {
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    mocks.tasks.mockRejectedValue(new Error('offline'))
    const attention = mount(); await attention.refresh()
    await vi.advanceTimersByTimeAsync(90000)
    expect(mocks.tasks).toHaveBeenCalledTimes(1)
    await attention.refresh()
    expect(mocks.tasks).toHaveBeenCalledTimes(2)
  })
  it('does not read summaries after logout even if permissions have not yet cleared', async () => {
    const attention = mount(); await attention.refresh()
    mocks.auth.token = ''
    await flush(); await attention.refresh()
    expect(mocks.tasks).toHaveBeenCalledTimes(1)
    expect(attention.states.tasks.count).toBeNull()
  })
  it('suspends cached-page summaries and subscriptions until activation', async () => {
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    const visible = ref(true)
    let attention!: ReturnType<typeof useMembershipAttention>
    const Page = defineComponent({ setup() { attention = useMembershipAttention(); return () => null } })
    root = document.createElement('div')
    app = createApp({ render: () => h(KeepAlive, null, { default: () => visible.value ? h(Page) : null }) })
    app.mount(root)
    await attention.refresh()
    expect(mocks.tasks).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(1)
    visible.value = false
    await flush()
    expect(vi.getTimerCount()).toBe(0)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(90000)
    expect(mocks.tasks).toHaveBeenCalledTimes(1)
    expect(attention.states.tasks.count).toBe(8)
    visible.value = true
    await flush()
    expect(mocks.tasks).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(1)
  })
  it('deduplicates refreshes, pauses polling when hidden and disposes its timer', async () => {
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    const attention = mount(); const first = attention.refresh()
    expect(attention.refresh()).toBe(first)
    await first
    hidden.mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(30000)
    expect(mocks.tasks).toHaveBeenCalledTimes(1)
    hidden.mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange')); await flush()
    expect(mocks.tasks).toHaveBeenCalledTimes(2)
    app?.unmount(); app = undefined
    await vi.advanceTimersByTimeAsync(60000)
    expect(mocks.tasks).toHaveBeenCalledTimes(2)
  })
})
