import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useTelegramBotSettings } from './useTelegramBotSettings'
const mocks = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn(), allowed: true }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn(), notifySuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBotSettings: mocks.get, updateTelegramBotSettingsSection: mocks.patch } }))
beforeEach(() => {
  vi.clearAllMocks(); mocks.allowed = true
  mocks.get.mockResolvedValue({ data: { data: { config_version: 4, help: { title: { 'en-US': 'Help' } } } } })
  mocks.patch.mockResolvedValue({ data: { data: { config_version: 5 } } })
})
describe('bot content section saves', () => {
  it('saves only help with its loaded CAS version', async () => {
    const model = useTelegramBotSettings(['help'])
    await model.fetchConfig(); await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('help', 4, model.form.value.help)
    expect(mocks.patch).toHaveBeenCalledTimes(1)
  })
  it('preserves draft and reports conflicts without retry', async () => {
    mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    const model = useTelegramBotSettings(['help'])
    await model.fetchConfig(); model.form.value.help.title['en-US'] = 'Draft'; await model.saveConfig()
    expect(model.error.value).toContain('conflict')
    expect(model.form.value.help.title['en-US']).toBe('Draft')
    expect(mocks.patch).toHaveBeenCalledTimes(1)
  })
  it.each([undefined, null, '5', -1, 4, 3, 5.5, Number.MAX_SAFE_INTEGER + 1])('blocks subsequent CAS writes after invalid version %s', async (configVersion) => {
    mocks.patch.mockResolvedValueOnce({ data: { data: { config_version: configVersion } } })
    const model = useTelegramBotSettings()
    await model.fetchConfig()
    model.form.value.basic.display_name = 'Draft'
    await model.saveConfig()
    await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledTimes(1)
    expect(model.loaded.value).toBe(false)
    expect(model.error.value).toBe('telegramBot.settings.saveFailed')
    expect(model.form.value.basic.display_name).toBe('Draft')
  })
  it.each(['web_app', 'command', 'unknown'])('preserves and rejects unsupported menu type %s', async (type) => {
    mocks.get.mockResolvedValueOnce({ data: { data: { config_version: 4, menu: { items: [{ key: 'legacy', action: { type, value: 'shop' } }] } } } })
    const model = useTelegramBotSettings(['menu'])
    await model.fetchConfig()
    expect(model.menuActionTypes).toEqual(['builtin', 'url'])
    expect(model.form.value.menu.items[0]?.action).toEqual({ type, value: 'shop' })
    expect(model.menuValidationError.value).toBe('telegramBot.settings.menuUnsupportedType')
    await model.saveConfig()
    expect(mocks.patch).not.toHaveBeenCalled()
    expect(model.loaded.value).toBe(true)
    model.form.value.menu.items[0]!.action = { type: 'builtin', value: 'help' }
    await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledTimes(1)
  })
  it('validates builtin values without blocking unrelated section saves', async () => {
    const menu = { items: [{ key: 'legacy', action: { type: 'builtin', value: 'gift_card' } }] }
    mocks.get.mockResolvedValue({ data: { data: { config_version: 4, menu } } })
    const model = useTelegramBotSettings(['menu'])
    await model.fetchConfig()
    await model.saveConfig()
    expect(model.menuValidationError.value).toBe('telegramBot.settings.menuUnsupportedBuiltin')
    expect(mocks.patch).not.toHaveBeenCalled()
    const help = useTelegramBotSettings(['help'])
    await help.fetchConfig()
    await help.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('help', 4, help.form.value.help)
  })
  it('refuses writes before loading or without permission', async () => {
    const model = useTelegramBotSettings(['menu'])
    await model.saveConfig(); expect(mocks.patch).not.toHaveBeenCalled()
    mocks.allowed = false; await model.fetchConfig(); await model.saveConfig()
    expect(mocks.patch).not.toHaveBeenCalled()
  })
})
