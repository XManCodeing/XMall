import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useTelegramBotSettingsRegistration } from './useTelegramBotSettingsRegistration'

const mocks = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn(), allowed: true, error: vi.fn(), success: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/utils/notify', () => ({ notifyErrorOnce: mocks.error, notifySuccess: mocks.success }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBotSettings: mocks.get, updateTelegramBotSettingsSection: mocks.patch } }))

beforeEach(() => {
  vi.clearAllMocks()
  mocks.allowed = true
  mocks.get.mockResolvedValue({ data: { data: { config_version: 3 } } })
  mocks.patch.mockResolvedValue({ data: { data: { config_version: 4 } } })
})

describe('independent bot registration settings', () => {
  it('defaults closed and saves only the global section without profiles', async () => {
    const model = useTelegramBotSettingsRegistration()
    await model.load()
    expect(model.enabled.value).toBe(false)
    model.enabled.value = true
    await model.save()
    expect(mocks.patch).toHaveBeenCalledExactlyOnceWith('registration', 3, { enabled: true })
  })

  it('preserves drafts on conflict and requires explicit reload', async () => {
    const model = useTelegramBotSettingsRegistration()
    await model.load()
    model.enabled.value = true
    mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    await model.save()
    await model.save()
    expect(model.enabled.value).toBe(true)
    expect(model.loaded.value).toBe(false)
    expect(model.error.value).toBe('telegramBot.instances.conflict')
    expect(mocks.patch).toHaveBeenCalledTimes(1)
  })

  it('blocks writes without permission or after load failure', async () => {
    mocks.allowed = false
    const model = useTelegramBotSettingsRegistration()
    await model.load()
    await model.save()
    expect(mocks.get).not.toHaveBeenCalled()
    expect(model.canLoad.value).toBe(false)
    expect(model.error.value).toBe('telegramBot.instances.forbidden')
    expect(mocks.patch).not.toHaveBeenCalled()
    mocks.allowed = true
    mocks.get.mockRejectedValueOnce(new Error('offline'))
    await model.load()
    await model.save()
    expect(model.loaded.value).toBe(false)
    expect(mocks.patch).not.toHaveBeenCalled()
  })
})
