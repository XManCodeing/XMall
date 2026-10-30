import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useTelegramBotSettings } from './useTelegramBotSettings'
const mocks = vi.hoisted(() => ({
  get: vi.fn(), patch: vi.fn(), upload: vi.fn(), list: vi.fn(), getProfile: vi.fn(), putProfile: vi.fn(),
  error: vi.fn(), success: vi.fn(), allowed: true,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: () => mocks.allowed }) }))
vi.mock('@/utils/notify', () => ({
  notifyError: mocks.error, notifySuccess: mocks.success,
  notifyErrorOnce: (error: { __notified?: boolean } | undefined, message: string) => {
    if (!error?.__notified) mocks.error(message)
  },
}))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    getTelegramBotSettings: mocks.get,
    updateTelegramBotSettingsSection: mocks.patch,
    upload: mocks.upload,
    getTelegramBotInstances: mocks.list,
    getTelegramBotInstanceProfile: mocks.getProfile,
    updateTelegramBotInstanceProfile: mocks.putProfile,
  },
}))
beforeEach(() => {
  vi.clearAllMocks(); mocks.allowed = true
  mocks.get.mockResolvedValue({ data: { data: { config_version: 4, help: { title: { 'en-US': 'Help' } }, basic: { support_url: '' } } } })
  mocks.patch.mockResolvedValue({ data: { data: { config_version: 5 } } })
  mocks.list.mockResolvedValue({ data: { data: { items: [{ id: 7, username: 'shop', telegram_id: 100 }] } } })
  mocks.getProfile.mockResolvedValue({ data: { data: { instance_id: 7, display_name: '', description: {}, cover_url: '', profile_version: 1 } } })
  mocks.putProfile.mockResolvedValue({ data: { data: { instance_id: 7, display_name: 'Draft', description: {}, cover_url: '', profile_version: 2 } } })
})
describe('bot content section saves', () => {
  it('saves only help with its loaded CAS version', async () => {
    const model = useTelegramBotSettings(['help'])
    await model.fetchConfig(); await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('help', 4, model.form.value.help)
    expect(mocks.patch).toHaveBeenCalledTimes(1)
    expect(mocks.putProfile).not.toHaveBeenCalled()
  })
  it('saves selected instance profile then global sections', async () => {
    const model = useTelegramBotSettings()
    await model.fetchConfig()
    model.form.value.basic.display_name = 'Draft'
    await model.saveConfig()
    expect(mocks.putProfile).toHaveBeenCalledWith(7, {
      expected_version: 1,
      display_name: 'Draft',
      description: { 'zh-CN': '', 'zh-TW': '', 'en-US': '' },
      cover_url: '',
    })
    expect(mocks.patch).toHaveBeenCalled()
  })
  it('preserves draft and reports conflicts without retry', async () => {
    mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    const model = useTelegramBotSettings(['help'])
    await model.fetchConfig(); model.form.value.help.title['en-US'] = 'Draft'; await model.saveConfig()
    expect(model.needsReload.value).toBe(true)
    expect(model.loadError.value).toBe('')
    expect(mocks.error).toHaveBeenCalledExactlyOnceWith('telegramBot.instances.conflict')
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
    expect(mocks.putProfile).toHaveBeenCalledTimes(1)
    expect(mocks.patch).toHaveBeenCalledTimes(1)
    expect(model.loaded.value).toBe(false)
    expect(model.needsReload.value).toBe(true)
    expect(model.loadError.value).toBe('')
    expect(mocks.error).toHaveBeenCalledExactlyOnceWith('telegramBot.settings.saveFailed')
    expect(model.form.value.basic.display_name).toBe('Draft')
  })
  it.each(['web_app', 'command', 'unknown'])('drops unsupported menu type %s so toggles can save', async (type) => {
    mocks.get.mockResolvedValueOnce({
      data: {
        data: {
          config_version: 4,
          menu: {
            items: [
              { key: 'legacy', action: { type, value: 'shop' } },
              { key: 'help', enabled: true, action: { type: 'builtin', value: 'help' } },
            ],
          },
        },
      },
    })
    const model = useTelegramBotSettings(['menu'])
    await model.fetchConfig()
    expect(model.menuActionTypes).toEqual(['builtin', 'url'])
    expect(model.menuDroppedUnsupported.value).toBe(true)
    expect(model.form.value.menu.items).toEqual([
      expect.objectContaining({ key: 'help', action: { type: 'builtin', value: 'help' } }),
    ])
    expect(model.menuValidationError.value).toBe('')
    await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('menu', 4, {
      items: [expect.objectContaining({ key: 'help', action: { type: 'builtin', value: 'help' } })],
    })
  })
  it('drops unsupported builtins without blocking menu or unrelated section saves', async () => {
    const menu = {
      items: [
        { key: 'legacy', action: { type: 'builtin', value: 'gift_card' } },
        { key: 'shop', enabled: true, action: { type: 'builtin', value: 'shop' } },
      ],
    }
    mocks.get.mockResolvedValue({ data: { data: { config_version: 4, menu } } })
    const model = useTelegramBotSettings(['menu'])
    await model.fetchConfig()
    expect(model.menuDroppedUnsupported.value).toBe(true)
    expect(model.menuValidationError.value).toBe('')
    await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('menu', 4, {
      items: [expect.objectContaining({ key: 'shop', action: { type: 'builtin', value: 'shop' } })],
    })
    const help = useTelegramBotSettings(['help'])
    await help.fetchConfig()
    await help.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('help', 4, help.form.value.help)
  })
  it.each([false, true])('keeps recovery state and drafts without duplicate save errors: %s', async notified => {
    const model = useTelegramBotSettings(['help'])
    await model.fetchConfig()
    model.form.value.help.title['en-US'] = 'Draft'
    mocks.patch.mockRejectedValueOnce({ __notified: notified, message: 'secret upstream details' })
    await model.saveConfig()
    await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledTimes(1)
    expect(mocks.error).toHaveBeenCalledTimes(notified ? 0 : 1)
    if (!notified) expect(mocks.error).toHaveBeenCalledWith('telegramBot.settings.saveFailed')
    expect(model.loaded.value).toBe(false)
    expect(model.needsReload.value).toBe(true)
    expect(model.loadError.value).toBe('')
    expect(model).not.toHaveProperty('error')
    expect(model.form.value.help.title['en-US']).toBe('Draft')
    mocks.get.mockRejectedValueOnce({ __notified: true })
    await model.fetchConfig()
    expect(model.needsReload.value).toBe(true)
    expect(model.loadError.value).toBe('telegramBot.settings.loadFailed')
    expect(model.form.value.help.title['en-US']).toBe('Draft')
    await model.fetchConfig()
    expect(model.needsReload.value).toBe(false)
    expect(model.loadError.value).toBe('')
    expect(model.loaded.value).toBe(true)
  })
  it.each([false, true])('deduplicates cover upload errors with a safe fallback: %s', async notified => {
    const model = useTelegramBotSettings()
    model.form.value.basic.cover_url = '/draft.png'
    mocks.upload.mockRejectedValueOnce({ __notified: notified, message: 'secret upstream details' })
    await model.handleUploadCover({ target: { files: [new File(['image'], 'cover.png')] } } as unknown as Event)
    expect(model.form.value.basic.cover_url).toBe('/draft.png')
    expect(mocks.error).toHaveBeenCalledTimes(notified ? 0 : 1)
    if (!notified) expect(mocks.error).toHaveBeenCalledWith('telegramBot.settings.uploadFailed')
  })
  it('refuses writes before loading or without permission', async () => {
    const model = useTelegramBotSettings(['menu'])
    await model.saveConfig(); expect(mocks.patch).not.toHaveBeenCalled()
    mocks.allowed = false; await model.fetchConfig(); await model.saveConfig()
    expect(mocks.patch).not.toHaveBeenCalled()
  })
  it('adds external URL rows that become savable after label and https url', async () => {
    mocks.get.mockResolvedValueOnce({
      data: {
        data: {
          config_version: 4,
          menu: {
            items: [{
              key: 'shop',
              enabled: true,
              label: { 'zh-CN': '购物', 'zh-TW': '', 'en-US': '' },
              action: { type: 'builtin', value: 'shop' },
            }],
          },
        },
      },
    })
    const model = useTelegramBotSettings(['menu'])
    await model.fetchConfig()
    model.addMenuItem()
    const added = model.form.value.menu.items[model.form.value.menu.items.length - 1]!
    expect(added).toMatchObject({ key: 'link_1', action: { type: 'url', value: '' } })
    expect(model.menuValidationError.value).toBe('telegramBot.settings.menuUrlRequired')
    added.action.value = 'https://example.com/help'
    expect(model.menuValidationError.value).toBe('telegramBot.settings.menuLabelRequired')
    added.label['zh-CN'] = '客服'
    expect(model.menuValidationError.value).toBe('')
    await model.saveConfig()
    expect(mocks.patch).toHaveBeenCalledWith('menu', 4, {
      items: expect.arrayContaining([
        expect.objectContaining({ key: 'shop' }),
        expect.objectContaining({ key: 'link_1', action: { type: 'url', value: 'https://example.com/help' } }),
      ]),
    })
  })
})
