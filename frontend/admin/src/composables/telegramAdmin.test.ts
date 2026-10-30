import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  allowed: true, error: vi.fn(),
  getSettings: vi.fn(), patchSettings: vi.fn(), list: vi.fn(), status: vi.fn(), create: vi.fn(), patch: vi.fn(),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn(), notifyErrorOnce: mocks.error, notifySuccess: vi.fn(), notifyInfo: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  getTelegramBotSettings: mocks.getSettings,
  updateTelegramBotSettingsSection: mocks.patchSettings,
  getTelegramBotInstances: mocks.list,
  getTelegramBotInstanceStatus: mocks.status,
  createTelegramBotInstance: mocks.create,
  patchTelegramBotInstance: mocks.patch,
} }))
import { useTelegramBotSettings } from './useTelegramBotSettings'
import { botState, useTelegramBotInstances, type BotInstance } from './useTelegramBotInstances'

const instance: BotInstance = { id: 1, telegram_id: 10, username: 'test', token_set: true, enabled: true, mode: 'internal', version: 3, applied_version: 2, state: 'running' }
const config = (version = 4) => ({ data: { data: { config_version: version, basic: { display_name: 'Original' } } } })
beforeEach(() => {
  vi.resetAllMocks()
  mocks.allowed = true
  mocks.getSettings.mockResolvedValue(config())
  mocks.list.mockResolvedValue({ data: { data: { items: [] } } })
})

describe('Telegram section CAS', () => {
  it('writes only selected sections, chaining the returned version', async () => {
    mocks.patchSettings.mockResolvedValueOnce(config(5)).mockResolvedValueOnce(config(6))
    const state = useTelegramBotSettings()
    await state.fetchConfig()
    state.form.value.basic.display_name = 'Draft'
    await state.saveConfig()
    expect(mocks.patchSettings.mock.calls.map(args => [args[0], args[1]])).toEqual([['basic', 4], ['welcome', 5]])
    expect(mocks.patchSettings.mock.calls[0]?.[2]).not.toHaveProperty('enabled')
    expect(mocks.patchSettings.mock.calls[0]?.[2]).not.toHaveProperty('help')
  })
  it('retains drafts and blocks further writes after a conflict', async () => {
    mocks.patchSettings.mockRejectedValue({ response: { status: 409 } })
    const state = useTelegramBotSettings(['menu'])
    await state.fetchConfig()
    state.form.value.basic.display_name = 'Unsaved draft'
    await state.saveConfig()
    await state.saveConfig()
    expect(state.form.value.basic.display_name).toBe('Unsaved draft')
    expect(state.loaded.value).toBe(false)
    expect(state.needsReload.value).toBe(true)
    expect(state.loadError.value).toBe('')
    expect(mocks.error).toHaveBeenCalledWith({ response: { status: 409 } }, 'telegramBot.instances.conflict')
    expect(mocks.patchSettings).toHaveBeenCalledTimes(1)
  })
  it('does not erase drafts or leave save enabled when reload fails', async () => {
    const state = useTelegramBotSettings()
    await state.fetchConfig()
    state.form.value.basic.display_name = 'Draft'
    mocks.getSettings.mockRejectedValue(new Error('offline'))
    await state.fetchConfig()
    expect(state.form.value.basic.display_name).toBe('Draft')
    expect(state.loaded.value).toBe(false)
  })
  it('fails closed for malformed version responses and missing permission', async () => {
    mocks.getSettings.mockResolvedValue({ data: { data: {} } })
    const state = useTelegramBotSettings()
    await state.fetchConfig()
    await state.saveConfig()
    expect(state.loaded.value).toBe(false)
    mocks.allowed = false
    await state.fetchConfig()
    expect(mocks.getSettings).toHaveBeenCalledTimes(1)
    expect(mocks.patchSettings).not.toHaveBeenCalled()
  })
})

describe('Telegram instances', () => {
  it('does not infer healthy state from a stale or absent heartbeat', () => {
    expect(botState(instance)).toBe('stale')
    expect(botState({ ...instance, last_seen_at: new Date(0).toISOString() }, 90001)).toBe('stale')
    expect(botState({ ...instance, last_seen_at: new Date(0).toISOString() }, 100)).toBe('running')
    expect(botState({ ...instance, enabled: false })).toBe('stale')
    expect(botState({ ...instance, state: 'stopped' })).toBe('stopped')
    expect(botState({ ...instance, mode: 'external' })).toBe('external')
  })
  it('uses instance version and never attaches legacy channels at creation', async () => {
    const state = useTelegramBotInstances()
    await state.create(' test-only-token ')
    expect(mocks.create).toHaveBeenCalledWith({ token: 'test-only-token' })
    await state.patch(instance, { enabled: false })
    expect(mocks.patch).toHaveBeenCalledWith(1, { expected_version: 3, enabled: false })
  })
  it('does not invoke APIs without permissions', async () => {
    mocks.allowed = false
    const state = useTelegramBotInstances()
    await state.refresh()
    await state.create('test-only-token')
    await state.patch(instance, { enabled: false })
    expect(mocks.list).not.toHaveBeenCalled()
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.patch).not.toHaveBeenCalled()
  })
})
