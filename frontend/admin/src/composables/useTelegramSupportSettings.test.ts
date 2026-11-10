import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { useTelegramSupportSettings, validSupportPolicy } from './useTelegramSupportSettings'

const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), permission: vi.fn(), notify: vi.fn() }))
vi.mock('@/api/telegramSupportSettings', () => ({ getSupportSettings: mocks.get, saveSupportSettings: mocks.save }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: mocks.permission }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/notify', () => ({ notifySuccess: mocks.notify }))
const policy = { max_messages: 10, max_bytes: 65536, max_attempts: 3, retention_seconds: 3600, challenge_ttl_seconds: 300, cooldown_seconds: 60 }
const response = (bot = 1, version = 0) => ({ data: { data: { bot_id: bot, version, using_defaults: version === 0, policy: { ...policy }, defaults: { ...policy }, verification_mode: 'click', production_ready: false } } })
let scope: ReturnType<typeof effectScope>
/** Mount composable lifecycle in a disposable scope without rendering unrelated pages. */
function model() { scope = effectScope(); return scope.run(useTelegramSupportSettings)! }
beforeEach(() => { vi.clearAllMocks(); mocks.permission.mockReturnValue(true); mocks.get.mockResolvedValue(response()); mocks.save.mockResolvedValue(response(1, 1)) })
afterEach(() => scope?.stop())

describe('support policy editor', () => {
  it('validates finite integer bounds and challenge-retention relationship', () => {
    expect(validSupportPolicy(policy)).toBe(true)
    for (const patch of [{ max_attempts: 0 }, { max_bytes: Infinity }, { max_messages: 1.2 }, { cooldown_seconds: 0 }, { retention_seconds: 60 }, { challenge_ttl_seconds: 3601 }]) {
      expect(validSupportPolicy({ ...policy, ...patch })).toBe(false)
    }
  })
  it('saves only the selected Bot and loaded CAS revision', async () => {
    const editor = model(); await editor.select('1')
    expect(editor.snapshot.value?.production_ready).toBe(false)
    editor.draft.value!.max_messages = 12
    await editor.save()
    expect(mocks.save).toHaveBeenCalledWith(1, 0, { ...policy, max_messages: 12 })
    expect(editor.snapshot.value?.version).toBe(1)
    expect(mocks.notify).toHaveBeenCalledOnce()
  })
  it('preserves conflict drafts and disables blind resubmission', async () => {
    const editor = model(); await editor.select('1'); editor.draft.value!.max_messages = 12
    mocks.save.mockRejectedValue({ response: { status: 409 } }); await editor.save()
    expect(editor.draft.value!.max_messages).toBe(12)
    expect(editor.conflict.value).toBe(true); expect(editor.canSave.value).toBe(false)
    await editor.save(); expect(mocks.save).toHaveBeenCalledTimes(1)
    await editor.load(); expect(editor.conflict.value).toBe(false)
  })
  it('does not fetch without permission or save without write permission', async () => {
    mocks.permission.mockReturnValue(false)
    const editor = model(); await editor.select('1'); expect(mocks.get).not.toHaveBeenCalled()
    mocks.permission.mockImplementation((value: string) => value.startsWith('GET:'))
    await editor.select('2'); expect(mocks.get).toHaveBeenCalledWith(2)
    await editor.save(); expect(mocks.save).not.toHaveBeenCalled()
  })
  it('ignores an old Bot response after selection changes', async () => {
    let resolveOld!: (value: ReturnType<typeof response>) => void
    mocks.get.mockImplementation((bot: number) => bot === 1 ? new Promise(resolve => { resolveOld = resolve }) : Promise.resolve(response(2)))
    const editor = model(); const first = editor.select('1'); await editor.select('2')
    resolveOld(response(1)); await first
    expect(editor.botID.value).toBe('2'); expect(editor.snapshot.value?.bot_id).toBe(2)
  })
  it('keeps malformed server responses out of the editor', async () => {
    mocks.get.mockResolvedValue(response(99)); const editor = model(); await editor.select('1')
    expect(editor.snapshot.value).toBeNull(); expect(editor.error.value).toBeTruthy()
  })
})
