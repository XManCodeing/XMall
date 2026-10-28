import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'

vi.mock('@/api/admin', () => ({ adminAPI: {
  checkSystemUpdate: vi.fn(), getUpdateCapability: vi.fn(), getUpdateStatus: vi.fn(),
  startSystemUpdate: vi.fn(), restartSystemService: vi.fn(), rollbackSystemUpdate: vi.fn(),
  getPublicConfig: vi.fn(),
} }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }))
vi.mock('@/utils/notify', () => ({ notifySuccess: vi.fn(), notifyError: vi.fn() }))
vi.mock('@/components/ui/dialog', async () => {
  const { defineComponent, h } = await import('vue')
  const passthrough = defineComponent({ setup: (_, { slots }) => () => h('div', slots.default?.()) })
  return Object.fromEntries(['Dialog', 'DialogContent', 'DialogHeader', 'DialogTitle', 'DialogFooter'].map(key => [key, passthrough]))
})
import { adminAPI } from '@/api/admin'
import { notifyError } from '@/utils/notify'
import { confirmAction } from '@/utils/confirm'
import SystemUpdateDialog from './SystemUpdateDialog.vue'
import i18n from '@/i18n'

const response = (data: unknown) => ({ data: { data } })
const state = (restart_required = true, target_version = 'v2.0.0') => ({
  status: 'succeeded', stage: 'done', percent: 100, restart_required, target_version,
})
const capability = (previous_version = 'v1.0.0') => ({
  can_update: true, can_restart: true, has_backup: true, rollback_unsafe: false,
  backup: { previous_version, target_version: 'v2.0.0' },
})
const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const flush = async () => {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }
}
let app: App | undefined
let root: HTMLDivElement
let open = ref(true)
const reload = vi.fn()
async function mount() {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({ setup: () => () => h(SystemUpdateDialog, { open: open.value }) }))
  app.mount(root)
  await flush()
  const original = window
  vi.stubGlobal('window', new Proxy(original, {
    get: (target, key) => key === 'location' ? { reload } : Reflect.get(target, key),
  }))
}
async function click(action: string) {
  const button = Array.from(root.querySelectorAll('button')).find(item => item.textContent?.trim() === `admin.systemUpdate.${action}`)
  expect(button, action).toBeDefined()
  button!.click()
  await flush()
}
async function tick(ms = 2000) { await vi.advanceTimersByTimeAsync(ms); await flush() }
beforeEach(() => {
  vi.useFakeTimers()
  open = ref(true)
  vi.mocked(confirmAction).mockResolvedValue(true)
  vi.mocked(adminAPI.checkSystemUpdate).mockResolvedValue(response({ current_version: 'v1.0.0', latest_version: 'v9.0.0', has_update: true }))
  vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: capability(), state: state() }))
  vi.mocked(adminAPI.getUpdateStatus).mockResolvedValue(response(state(false)))
  vi.mocked(adminAPI.getPublicConfig).mockResolvedValue(response({ app_version: 'v2.0.0' }))
  vi.mocked(adminAPI.restartSystemService).mockResolvedValue(response({}))
  vi.mocked(adminAPI.rollbackSystemUpdate).mockResolvedValue(response(state()))
})
afterEach(() => {
  app?.unmount(); app = undefined
  root?.remove()
  vi.unstubAllGlobals()
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.resetAllMocks()
})

const restartReasons = [
  'systemd_policy_unknown', 'systemd_policy_disabled', 'exec_preflight_failed',
  'unsupported_os', 'container',
]

describe('system update restart block reasons', () => {
  it.each(restartReasons)('shows the localized %s hint without a restart action', async reason => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({
      capability: { ...capability(), can_restart: false, restart_block_reason: reason }, state: state(),
    }))
    await mount()
    expect(root.textContent).toContain(`admin.systemUpdate.restartBlockReason.${reason}`)
    expect(root.textContent).not.toContain('admin.systemUpdate.manualRestartHint')
    expect(root.textContent).not.toContain('admin.systemUpdate.restartNow')
    expect(adminAPI.restartSystemService).not.toHaveBeenCalled()
  })

  it.each([undefined, null, ''])('keeps the legacy generic hint for %s', async reason => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({
      capability: { ...capability(), can_restart: false, ...(reason === undefined ? {} : { restart_block_reason: reason }) }, state: state(),
    }))
    await mount()
    expect(root.textContent).toContain('admin.systemUpdate.manualRestartHint')
    expect(root.textContent).not.toContain('admin.systemUpdate.restartNow')
  })

  it.each(['future_reason', '__proto__', 'constructor', '<script>alert(1)</script>'])('safely falls back for %s', async reason => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({
      capability: { ...capability(), can_restart: false, restart_block_reason: reason }, state: state(),
    }))
    await mount()
    expect(root.textContent).toContain('admin.systemUpdate.restartBlockReason.unknown')
    expect(root.textContent).not.toContain(reason)
    expect(root.textContent).not.toContain('admin.systemUpdate.restartNow')
    expect(root.querySelector('script')).toBeNull()
  })

  it('ignores a stale reason when the backend allows restart', async () => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({
      capability: { ...capability(), restart_block_reason: 'systemd_policy_unknown' }, state: state(),
    }))
    await mount()
    expect(root.textContent).toContain('admin.systemUpdate.restartNow')
    expect(root.textContent).not.toContain('admin.systemUpdate.restartBlockReason')
  })

  it('does not show restart guidance when no restart is required', async () => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({
      capability: { ...capability(), can_restart: false, restart_block_reason: 'unsupported_os' }, state: state(false),
    }))
    await mount()
    expect(root.textContent).not.toContain('admin.systemUpdate.restartBlockReason')
  })

  it('has matching, nonempty translations in every supported locale', () => {
    for (const locale of i18n.global.availableLocales) {
      const messages = i18n.global.getLocaleMessage(locale)
      const reasons = messages.admin.systemUpdate.restartBlockReason
      expect(Object.keys(reasons).sort()).toEqual([...restartReasons, 'unknown'].sort())
      for (const reason of [...restartReasons, 'unknown']) {
        const key = `admin.systemUpdate.restartBlockReason.${reason}`
        expect(i18n.global.te(key, locale)).toBe(true)
        const text = i18n.global.t(key, {}, { locale })
        expect(text.trim()).not.toBe('')
        expect(text).not.toBe(key)
      }
    }
  })
})

describe('system update restart readiness', () => {
  it('does not reload while the old process still responds and requires restart', async () => {
    vi.mocked(adminAPI.getUpdateStatus).mockResolvedValue(response(state(true)))
    await mount(); await click('restartNow'); await tick(6000)
    expect(adminAPI.getUpdateStatus).toHaveBeenCalledTimes(3)
    expect(adminAPI.getPublicConfig).not.toHaveBeenCalled()
    expect(reload).not.toHaveBeenCalled()
  })

  it('requires the actual running target version, not latest or an old version', async () => {
    vi.mocked(adminAPI.getPublicConfig)
      .mockResolvedValueOnce(response({ app_version: 'v1.0.0' }))
      .mockResolvedValueOnce(response({ app_version: 'v9.0.0' }))
    await mount(); await click('restartNow')
    await tick(); expect(reload).not.toHaveBeenCalled()
    await tick(); expect(reload).not.toHaveBeenCalled()
    await tick(); expect(reload).toHaveBeenCalledTimes(1)
    await tick(6000); expect(reload).toHaveBeenCalledTimes(1)
    expect(adminAPI.checkSystemUpdate).toHaveBeenCalledTimes(1)
  })

  it('retries network failures and missing readiness fields', async () => {
    vi.mocked(adminAPI.getUpdateStatus)
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce(response({}))
    vi.mocked(adminAPI.getPublicConfig).mockResolvedValueOnce(response({}))
    await mount(); await click('restartNow'); await tick(6000)
    expect(reload).not.toHaveBeenCalled()
    await tick(); expect(reload).toHaveBeenCalledTimes(1)
  })

  it('captures the started update target instead of the displayed release', async () => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: capability(), state: { ...state(false, ''), status: 'idle' } }))
    vi.mocked(adminAPI.startSystemUpdate).mockResolvedValue(response({ ...state(false, 'v3.0.0'), status: 'running' }))
    vi.mocked(adminAPI.getUpdateStatus).mockResolvedValueOnce(response(state(true, 'v3.0.0'))).mockResolvedValue(response(state(false, '')))
    vi.mocked(adminAPI.getPublicConfig).mockResolvedValue(response({ app_version: 'v3.0.0' }))
    await mount(); await click('upgradeNow')
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: capability(), state: state(true, 'v3.0.0') }))
    await tick(1500); await click('restartNow'); await tick()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('captures rollback previous_version before the backup disappears, ignoring stale target_version', async () => {
    await mount()
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: { can_restart: true, has_backup: false }, state: state() }))
    await click('rollback')
    // Closing/reopening the same dialog must not discard the rollback target.
    open.value = false; await flush(); open.value = true; await flush()
    await click('restartNow'); await tick()
    expect(reload).not.toHaveBeenCalled()
    vi.mocked(adminAPI.getPublicConfig).mockResolvedValue(response({ app_version: 'v1.0.0' }))
    await tick(); expect(reload).toHaveBeenCalledTimes(1)
  })

  it('retains a successful rollback target when its response arrives while closed', async () => {
    const pending = deferred<ReturnType<typeof response>>()
    vi.mocked(adminAPI.rollbackSystemUpdate).mockReturnValueOnce(pending.promise)
    await mount(); await click('rollback')
    open.value = false; await flush()
    pending.resolve(response(state())); await flush()
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: { can_restart: true, has_backup: false }, state: state() }))
    vi.mocked(adminAPI.getPublicConfig).mockResolvedValue(response({ app_version: 'v1.0.0' }))
    open.value = true; await flush()
    await click('restartNow'); await tick()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('fails closed when the rollback version is unknown', async () => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: capability(''), state: state() }))
    await mount(); await click('rollback'); await click('restartNow'); await tick(120_000)
    expect(reload).not.toHaveBeenCalled()
    expect(notifyError).toHaveBeenCalledWith('admin.systemUpdate.restartTimeout')
  })

  it('times out without reloading when a reachable service keeps the wrong version', async () => {
    vi.mocked(adminAPI.getPublicConfig).mockResolvedValue(response({ app_version: 'v1.0.0' }))
    await mount(); await click('restartNow'); await tick(120_000)
    expect(reload).not.toHaveBeenCalled()
    expect(notifyError).toHaveBeenCalledWith('admin.systemUpdate.restartTimeout')
    const calls = vi.mocked(adminAPI.getUpdateStatus).mock.calls.length
    await tick(6000)
    expect(adminAPI.getUpdateStatus).toHaveBeenCalledTimes(calls)
  })

  it('reports restart failure without starting probes', async () => {
    vi.mocked(adminAPI.restartSystemService).mockRejectedValue(new Error('restart rejected'))
    await mount(); await click('restartNow'); await tick(6000)
    expect(notifyError).toHaveBeenCalledWith('restart rejected')
    expect(adminAPI.getUpdateStatus).not.toHaveBeenCalled()
    expect(root.textContent).toContain('admin.systemUpdate.restartNow')
  })

  it('times out even when a probe hangs and ignores its late success', async () => {
    const pending = deferred<ReturnType<typeof response>>()
    vi.mocked(adminAPI.getPublicConfig).mockReturnValueOnce(pending.promise)
    await mount(); await click('restartNow'); await tick(120_000)
    expect(adminAPI.getUpdateStatus).toHaveBeenCalledTimes(1)
    expect(notifyError).toHaveBeenCalledTimes(1)
    expect(notifyError).toHaveBeenCalledWith('admin.systemUpdate.restartTimeout')
    pending.resolve(response({ app_version: 'v2.0.0' })); await flush()
    expect(reload).not.toHaveBeenCalled()
    expect(root.textContent).toContain('admin.systemUpdate.restartNow')
  })

  it.each(['close', 'unmount'])('ignores pending probes after %s', async action => {
    const pending = deferred<ReturnType<typeof response>>()
    vi.mocked(adminAPI.getPublicConfig).mockReturnValueOnce(pending.promise)
    await mount(); await click('restartNow'); await tick()
    if (action === 'close') { open.value = false; await flush() }
    else { app!.unmount(); app = undefined }
    pending.resolve(response({ app_version: 'v2.0.0' })); await flush(); await tick(120_000)
    expect(reload).not.toHaveBeenCalled()
    expect(notifyError).not.toHaveBeenCalled()
  })

  it('does not start probes when the restart response arrives after unmount', async () => {
    const pending = deferred<ReturnType<typeof response>>()
    vi.mocked(adminAPI.restartSystemService).mockReturnValueOnce(pending.promise)
    await mount(); await click('restartNow')
    app!.unmount(); app = undefined
    pending.resolve(response({})); await flush(); await tick(120_000)
    expect(adminAPI.getUpdateStatus).not.toHaveBeenCalled()
    expect(reload).not.toHaveBeenCalled()
  })

  it('serializes progress polling and ignores responses after unmount', async () => {
    vi.mocked(adminAPI.getUpdateCapability).mockResolvedValue(response({ capability: capability(), state: { ...state(), status: 'running' } }))
    const pending = deferred<ReturnType<typeof response>>()
    vi.mocked(adminAPI.getUpdateStatus).mockReturnValueOnce(pending.promise)
    await mount(); await tick(6000)
    expect(adminAPI.getUpdateStatus).toHaveBeenCalledTimes(1)
    app!.unmount(); app = undefined
    pending.resolve(response(state())); await flush(); await tick(6000)
    expect(adminAPI.getUpdateCapability).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('ignores initial capability requests that resolve after unmount', async () => {
    const pending = deferred<ReturnType<typeof response>>()
    vi.mocked(adminAPI.getUpdateCapability).mockReturnValueOnce(pending.promise)
    await mount(); app!.unmount(); app = undefined
    pending.resolve(response({ capability: capability(), state: { ...state(), status: 'running' } }))
    await flush(); await tick(6000)
    expect(adminAPI.getUpdateStatus).not.toHaveBeenCalled()
  })
})
