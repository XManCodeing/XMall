import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyError, notifyErrorOnce, notifySuccess } from '@/utils/notify'

const supportedLanguages = ['zh-CN', 'zh-TW', 'en-US'] as const
type SupportedLanguage = (typeof supportedLanguages)[number]
type LocalizedText = Record<SupportedLanguage, string>
type MenuActionType = string

interface MenuItem {
  key: string
  enabled: boolean
  order: number
  label: LocalizedText
  action: {
    type: MenuActionType
    value: string
  }
}

interface HelpItem {
  key: string
  enabled: boolean
  order: number
  summary: LocalizedText
  title: LocalizedText
  content: LocalizedText
  show_support_link: boolean
}

interface TelegramBotSettingsForm {
  enabled: boolean
  default_locale: SupportedLanguage
  basic: {
    display_name: string
    description: LocalizedText
    support_url: string
    cover_url: string
  }
  welcome: {
    enabled: boolean
    message: LocalizedText
  }
  help: {
    enabled: boolean
    title: LocalizedText
    intro: LocalizedText
    center_hint: LocalizedText
    support_hint: LocalizedText
    items: HelpItem[]
  }
  menu: {
    items: MenuItem[]
  }
}

const menuActionTypes: MenuActionType[] = ['builtin', 'url']
const builtinMenuValues = ['shop', 'orders', 'wallet', 'language', 'help']
const menuItemsMaxCount = 20
const helpItemsMaxCount = 12

const isEditableMenuItem = (item: MenuItem) => {
  if (item.action.type === 'url') return true
  return item.action.type === 'builtin' && builtinMenuValues.includes(item.action.value.trim())
}

const emptyLocalized = (): LocalizedText => ({
  'zh-CN': '',
  'zh-TW': '',
  'en-US': '',
})

const createMenuItem = (key: string, order: number): MenuItem => ({
  key,
  enabled: true,
  order,
  label: emptyLocalized(),
  // New rows are external links; builtins are fixed seeds and should not be duplicated.
  action: { type: 'url', value: '' },
})

const createHelpItem = (): HelpItem => ({
  key: '',
  enabled: true,
  order: 0,
  summary: emptyLocalized(),
  title: emptyLocalized(),
  content: emptyLocalized(),
  show_support_link: false,
})

const createTelegramBotSettingsForm = (): TelegramBotSettingsForm => ({
  enabled: false,
  default_locale: 'zh-CN',
  basic: {
    display_name: '',
    description: emptyLocalized(),
    support_url: '',
    cover_url: '',
  },
  welcome: {
    enabled: false,
    message: emptyLocalized(),
  },
  help: {
    enabled: true,
    title: emptyLocalized(),
    intro: emptyLocalized(),
    center_hint: emptyLocalized(),
    support_hint: emptyLocalized(),
    items: [],
  },
  menu: {
    items: [],
  },
})

const parseLocalized = (raw: unknown): LocalizedText => {
  const result = emptyLocalized()
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
    const obj = raw as Record<string, unknown>
    for (const lang of supportedLanguages) {
      if (typeof obj[lang] === 'string') {
        result[lang] = obj[lang]
      }
    }
  }
  return result
}

const parseMenuItem = (raw: unknown): MenuItem => {
  const item = createMenuItem('', 0)
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return item

  const obj = raw as Record<string, unknown>
  if (typeof obj.key === 'string') item.key = obj.key
  if (typeof obj.enabled === 'boolean') item.enabled = obj.enabled
  if (typeof obj.order === 'number') item.order = obj.order
  item.label = parseLocalized(obj.label)

  if (obj.action && typeof obj.action === 'object' && !Array.isArray(obj.action)) {
    const action = obj.action as Record<string, unknown>
    if (typeof action.type === 'string') {
      item.action.type = action.type
    }
    if (typeof action.value === 'string') {
      item.action.value = action.value
    }
  }

  return item
}

const parseHelpItem = (raw: unknown): HelpItem => {
  const item = createHelpItem()
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return item

  const obj = raw as Record<string, unknown>
  if (typeof obj.key === 'string') item.key = obj.key
  if (typeof obj.enabled === 'boolean') item.enabled = obj.enabled
  if (typeof obj.order === 'number') item.order = obj.order
  if (typeof obj.show_support_link === 'boolean') item.show_support_link = obj.show_support_link
  item.summary = parseLocalized(obj.summary)
  item.title = parseLocalized(obj.title)
  item.content = parseLocalized(obj.content)

  return item
}

export function useTelegramBotSettings(sections: Array<'basic' | 'welcome' | 'help' | 'menu'> = ['basic', 'welcome']) {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const wantsProfile = computed(() => sections.includes('basic'))
  const canSaveContent = computed(() => sections.every(section => auth.hasPermission(`PATCH:/admin/settings/telegram-bot/${section}`)))
  const canListInstances = computed(() => auth.hasPermission('GET:/admin/telegram-bot/instances'))
  const canSaveProfile = computed(() => {
    if (!wantsProfile.value || !selectedInstanceId.value) return false
    return auth.hasPermission(`PUT:/admin/telegram-bot/instances/${selectedInstanceId.value}/profile`)
  })
  const canSave = computed(() => {
    if (!canSaveContent.value) return false
    if (!wantsProfile.value) return true
    // Profile requires a selected instance; global welcome/support can still save alone only when profile is not requested.
    return !!selectedInstanceId.value && canSaveProfile.value && profileLoaded.value
  })
  const version = ref(0)
  const profileVersion = ref(0)
  const loadError = ref('')
  const needsReload = ref(false)
  const loaded = ref(false)
  const menuDroppedUnsupported = ref(false)
  const profileLoaded = ref(false)
  const selectedInstanceId = ref('')
  const instances = ref<Array<{ id: number; username: string; telegram_id: number }>>([])
  const profileSyncError = ref('')

  const currentLang = ref<SupportedLanguage>('zh-CN')
  const loading = ref(false)
  const saving = ref(false)
  const uploadingCover = ref(false)
  const coverFileInput = ref<HTMLInputElement>()
  const form = ref<TelegramBotSettingsForm>(createTelegramBotSettingsForm())

  const languages = computed(() => [
    { code: 'zh-CN' as SupportedLanguage, name: t('admin.common.lang.zhCN') },
    { code: 'zh-TW' as SupportedLanguage, name: t('admin.common.lang.zhTW') },
    { code: 'en-US' as SupportedLanguage, name: t('admin.common.lang.enUS') },
  ])

  const resetProfileFields = () => {
    form.value.basic.display_name = ''
    form.value.basic.description = emptyLocalized()
    form.value.basic.cover_url = ''
    profileVersion.value = 0
    profileLoaded.value = false
    profileSyncError.value = ''
  }

  const fetchProfile = async (id: number) => {
    if (!auth.hasPermission(`GET:/admin/telegram-bot/instances/${id}/profile`)) {
      resetProfileFields()
      loadError.value = t('telegramBot.instances.forbidden')
      return
    }
    const res = await adminAPI.getTelegramBotInstanceProfile(id)
    const data = res.data?.data as Record<string, unknown> | undefined
    if (!data || !Number.isSafeInteger(data.profile_version) || Number(data.profile_version) < 1) {
      throw new Error('Invalid profile response')
    }
    form.value.basic.display_name = (data.display_name as string) ?? ''
    form.value.basic.description = parseLocalized(data.description)
    form.value.basic.cover_url = (data.cover_url as string) ?? ''
    profileVersion.value = Number(data.profile_version)
    profileSyncError.value = typeof data.profile_sync_error === 'string' ? data.profile_sync_error : ''
    profileLoaded.value = true
  }

  const fetchInstances = async () => {
    if (!wantsProfile.value || !canListInstances.value) {
      instances.value = []
      selectedInstanceId.value = ''
      resetProfileFields()
      return
    }
    const res = await adminAPI.getTelegramBotInstances()
    const items = (res.data?.data?.items ?? []) as Array<{ id: number; username: string; telegram_id: number }>
    instances.value = items.map(item => ({ id: item.id, username: item.username, telegram_id: item.telegram_id }))
    if (!instances.value.some(item => String(item.id) === selectedInstanceId.value)) {
      selectedInstanceId.value = instances.value[0] ? String(instances.value[0].id) : ''
    }
    if (selectedInstanceId.value) {
      await fetchProfile(Number(selectedInstanceId.value))
    } else {
      resetProfileFields()
    }
  }

  const selectInstance = async (id: string) => {
    if (loading.value || saving.value) return
    selectedInstanceId.value = id
    if (!id) {
      resetProfileFields()
      return
    }
    loading.value = true
    try {
      await fetchProfile(Number(id))
      loadError.value = ''
    } catch (err) {
      resetProfileFields()
      loadError.value = t('telegramBot.settings.loadFailed')
      notifyErrorOnce(err, loadError.value)
    } finally {
      loading.value = false
    }
  }

  const fetchConfig = async () => {
    if (loading.value || saving.value) return
    if (!auth.hasPermission('GET:/admin/settings/telegram-bot')) {
      loaded.value = false
      loadError.value = t('telegramBot.instances.forbidden')
      return
    }
    loading.value = true
    loaded.value = false
    try {
      const res = await adminAPI.getTelegramBotSettings()
      const data = res.data?.data as Record<string, unknown> | undefined
      if (!data || !Number.isSafeInteger(data.config_version) || Number(data.config_version) < 0) {
        throw new Error('Invalid configuration response')
      }

      const previousSupport = form.value.basic.support_url
      form.value = createTelegramBotSettingsForm()
      version.value = Number(data.config_version)
      loaded.value = true
      needsReload.value = false
      loadError.value = ''
      form.value.enabled = false
      form.value.default_locale = (data.default_locale as SupportedLanguage) ?? 'zh-CN'

      const basic = data.basic as Record<string, unknown> | undefined
      if (basic) {
        // support_url remains global shop content; display_name/cover live on the instance profile.
        form.value.basic.support_url = (basic.support_url as string) ?? previousSupport ?? ''
      }

      const welcome = data.welcome as Record<string, unknown> | undefined
      if (welcome) {
        form.value.welcome.enabled = (welcome.enabled as boolean) ?? false
        form.value.welcome.message = parseLocalized(welcome.message)
      }

      const help = data.help as Record<string, unknown> | undefined
      if (help) {
        form.value.help.enabled = (help.enabled as boolean) ?? true
        form.value.help.title = parseLocalized(help.title)
        form.value.help.intro = parseLocalized(help.intro)
        form.value.help.center_hint = parseLocalized(help.center_hint)
        form.value.help.support_hint = parseLocalized(help.support_hint)
        if (Array.isArray(help.items)) {
          form.value.help.items = help.items.map(parseHelpItem)
        }
      }

      const menu = data.menu as Record<string, unknown> | undefined
      if (menu && Array.isArray(menu.items)) {
        const parsed = menu.items.map(parseMenuItem)
        // Internal admin only edits builtin/url. Legacy external actions stay in storage
        // until a menu save replaces the section; hide them here so toggles can be saved.
        const editable = parsed.filter(isEditableMenuItem)
        menuDroppedUnsupported.value = editable.length !== parsed.length
        form.value.menu.items = editable
      } else {
        menuDroppedUnsupported.value = false
      }

      await fetchInstances()
    } catch (err) {
      loaded.value = false
      loadError.value = t('telegramBot.settings.loadFailed')
      notifyErrorOnce(err, loadError.value)
    } finally {
      loading.value = false
    }
  }

  const getMenuItemError = (item: MenuItem) => {
    if (!item.key.trim()) {
      return t('telegramBot.settings.menuKeyRequired')
    }
    if (!menuActionTypes.includes(item.action.type)) {
      return t('telegramBot.settings.menuUnsupportedType', { type: item.action.type })
    }
    if (item.action.type === 'builtin' && !builtinMenuValues.includes(item.action.value.trim())) {
      return t('telegramBot.settings.menuUnsupportedBuiltin')
    }
    if (item.action.type === 'url') {
      const value = item.action.value.trim()
      if (!value) {
        return t('telegramBot.settings.menuUrlRequired')
      }
      try {
        const parsed = new URL(value)
        if (parsed.protocol !== 'https:' || !parsed.host) {
          return t('telegramBot.settings.menuUrlInvalid')
        }
      } catch {
        return t('telegramBot.settings.menuUrlInvalid')
      }
      // Builtin labels can fall back to i18n; URL buttons have nothing else to show.
      if (item.enabled && !Object.values(item.label).some(value => value.trim())) {
        return t('telegramBot.settings.menuLabelRequired')
      }
    }
    return ''
  }
  const menuValidationError = computed(() => {
    const itemError = form.value.menu.items.map(getMenuItemError).find(Boolean)
    if (itemError) return itemError
    const seen = new Set<string>()
    for (const item of form.value.menu.items) {
      if (item.action.type !== 'builtin') continue
      const value = item.action.value.trim()
      if (!value) continue
      if (seen.has(value)) {
        return t('telegramBot.settings.menuDuplicateBuiltin')
      }
      seen.add(value)
    }
    return ''
  })
  const isBuiltinMenuItem = (item: MenuItem) =>
    item.action.type === 'builtin' && builtinMenuValues.includes(item.action.value.trim())

  const saveConfig = async () => {
    if (!loaded.value || !canSave.value || saving.value || loading.value) return
    if (sections.includes('menu') && menuValidationError.value) {
      notifyError(menuValidationError.value)
      return
    }
    saving.value = true
    // Freeze the payload across sequential CAS writes; edits made while saving remain local.
    const snapshot = JSON.parse(JSON.stringify(form.value)) as TelegramBotSettingsForm
    const instanceID = Number(selectedInstanceId.value)
    try {
      if (wantsProfile.value) {
        if (!profileLoaded.value || !Number.isSafeInteger(instanceID) || instanceID <= 0) {
          throw new Error('Profile not loaded')
        }
        const res = await adminAPI.updateTelegramBotInstanceProfile(instanceID, {
          expected_version: profileVersion.value,
          display_name: snapshot.basic.display_name,
          description: snapshot.basic.description,
          cover_url: snapshot.basic.cover_url,
        })
        const next = res.data?.data?.profile_version
        if (!Number.isSafeInteger(next) || next <= profileVersion.value) {
          throw new Error('Invalid profile version response')
        }
        profileVersion.value = Number(next)
        profileSyncError.value = typeof res.data?.data?.profile_sync_error === 'string' ? res.data.data.profile_sync_error : ''
      }
      for (const section of sections) {
        let data: unknown
        if (section === 'basic') {
          data = { basic: { support_url: snapshot.basic.support_url, display_name: '', description: emptyLocalized(), cover_url: '' }, default_locale: snapshot.default_locale }
        } else if (section === 'menu') {
          data = { items: snapshot.menu.items.filter(isEditableMenuItem) }
        } else {
          data = snapshot[section]
        }
        const res = await adminAPI.updateTelegramBotSettingsSection(section, version.value, data)
        const nextVersion = res.data?.data?.config_version
        if (!Number.isSafeInteger(nextVersion) || nextVersion <= version.value) {
          throw new Error('Invalid configuration version response')
        }
        version.value = nextVersion
      }
      notifySuccess(t('telegramBot.settings.saveSuccess'))
    } catch (err) {
      // A conflict or an ambiguous transport failure requires an explicit reload.
      loaded.value = false
      profileLoaded.value = false
      needsReload.value = true
      const status = (err as { response?: { status?: number } } | null)?.response?.status
      notifyErrorOnce(err, t(status === 409 ? 'telegramBot.instances.conflict' : status === 502 ? 'telegramBot.settings.profileSyncFailed' : 'telegramBot.settings.saveFailed'))
    } finally {
      saving.value = false
    }
  }

  const handleUploadCover = async (event: Event) => {
    const file = (event.target as HTMLInputElement).files?.[0]
    if (!file) return

    uploadingCover.value = true
    try {
      const formData = new FormData()
      formData.append('file', file)
      const res = await adminAPI.upload(formData, 'telegram')
      const url = (res.data.data as Record<string, string>)?.url || ''
      form.value.basic.cover_url = url
    } catch (err) {
      notifyErrorOnce(err, t('telegramBot.settings.uploadFailed'))
    } finally {
      uploadingCover.value = false
      if (coverFileInput.value) {
        coverFileInput.value.value = ''
      }
    }
  }

  const addMenuItem = () => {
    if (form.value.menu.items.length >= menuItemsMaxCount) {
      notifyError(t('telegramBot.settings.menuMaxHint', { max: menuItemsMaxCount }))
      return
    }
    const used = new Set(form.value.menu.items.map(item => item.key))
    let n = 1
    while (used.has(`link_${n}`)) n += 1
    form.value.menu.items.push(createMenuItem(`link_${n}`, form.value.menu.items.length + 1))
  }

  const reindexOrders = <T extends { order: number }>(items: T[]) => {
    items.forEach((item, index) => {
      item.order = index + 1
    })
  }

  const removeMenuItem = (index: number) => {
    const item = form.value.menu.items[index]
    if (!item || isBuiltinMenuItem(item)) return
    form.value.menu.items.splice(index, 1)
    reindexOrders(form.value.menu.items)
  }

  const moveMenuItem = (index: number, direction: 'up' | 'down') => {
    const items = form.value.menu.items
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= items.length) return
    ;[items[index], items[targetIndex]] = [items[targetIndex]!, items[index]!]
    reindexOrders(items)
  }

  const addHelpItem = () => {
    if (form.value.help.items.length >= helpItemsMaxCount) {
      notifyError(t('telegramBot.settings.helpMaxHint', { max: helpItemsMaxCount }))
      return
    }
    form.value.help.items.push(createHelpItem())
  }

  const removeHelpItem = (index: number) => {
    form.value.help.items.splice(index, 1)
    reindexOrders(form.value.help.items)
  }

  const moveHelpItem = (index: number, direction: 'up' | 'down') => {
    const items = form.value.help.items
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= items.length) return
    ;[items[index], items[targetIndex]] = [items[targetIndex]!, items[index]!]
    reindexOrders(items)
  }

  const getMenuActionValuePlaceholder = (type: MenuActionType) => menuActionTypes.includes(type)
    ? t(`telegramBot.settings.menuActionValuePlaceholder_${type}`)
    : t('telegramBot.settings.menuActionValuePlaceholder')

  const getMenuActionValueHint = (type: MenuActionType) => menuActionTypes.includes(type)
    ? t(`telegramBot.settings.menuActionValueHint_${type}`)
    : t('telegramBot.settings.menuUnsupportedType', { type })

  return {
    canSave,
    canListInstances,
    loadError,
    needsReload,
    loaded,
    menuDroppedUnsupported,
    profileLoaded,
    profileSyncError,
    coverFileInput,
    currentLang,
    fetchConfig,
    form,
    handleUploadCover,
    addHelpItem,
    addMenuItem,
    getMenuActionValueHint,
    getMenuActionValuePlaceholder,
    instances,
    languages,
    loading,
    menuActionTypes,
    getMenuItemError,
    menuValidationError,
    isBuiltinMenuItem,
    moveHelpItem,
    moveMenuItem,
    removeHelpItem,
    removeMenuItem,
    saveConfig,
    saving,
    selectedInstanceId,
    selectInstance,
    uploadingCover,
    wantsProfile,
  }
}
