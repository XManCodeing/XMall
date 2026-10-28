import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyError, notifySuccess } from '@/utils/notify'

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

const emptyLocalized = (): LocalizedText => ({
  'zh-CN': '',
  'zh-TW': '',
  'en-US': '',
})

const createMenuItem = (): MenuItem => ({
  key: '',
  enabled: true,
  order: 0,
  label: emptyLocalized(),
  action: { type: 'builtin', value: '' },
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
  const item = createMenuItem()
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
  const canSave = computed(() => sections.every(section => auth.hasPermission(`PATCH:/admin/settings/telegram-bot/${section}`)))
  const version = ref(0)
  const error = ref('')
  const loaded = ref(false)

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

  const fetchConfig = async () => {
    if (loading.value || saving.value) return
    if (!auth.hasPermission('GET:/admin/settings/telegram-bot')) {
      loaded.value = false
      error.value = t('telegramBot.instances.forbidden')
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

      form.value = createTelegramBotSettingsForm()
      version.value = Number(data.config_version)
      loaded.value = true
      error.value = ''
      form.value.enabled = false
      form.value.default_locale = (data.default_locale as SupportedLanguage) ?? 'zh-CN'

      const basic = data.basic as Record<string, unknown> | undefined
      if (basic) {
        form.value.basic.display_name = (basic.display_name as string) ?? ''
        form.value.basic.description = parseLocalized(basic.description)
        form.value.basic.support_url = (basic.support_url as string) ?? ''
        form.value.basic.cover_url = (basic.cover_url as string) ?? ''
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
        form.value.menu.items = menu.items.map(parseMenuItem)
      }
    } catch {
      loaded.value = false
      error.value = t('telegramBot.settings.loadFailed')
      notifyError(error.value)
    } finally {
      loading.value = false
    }
  }

  const getMenuItemError = (item: MenuItem) => {
    if (!menuActionTypes.includes(item.action.type)) {
      return t('telegramBot.settings.menuUnsupportedType', { type: item.action.type })
    }
    if (item.action.type === 'builtin' && !builtinMenuValues.includes(item.action.value.trim())) {
      return t('telegramBot.settings.menuUnsupportedBuiltin')
    }
    return ''
  }
  const menuValidationError = computed(() => form.value.menu.items.map(getMenuItemError).find(Boolean) || '')

  const saveConfig = async () => {
    if (!loaded.value || !canSave.value || saving.value || loading.value) return
    if (sections.includes('menu') && menuValidationError.value) {
      error.value = menuValidationError.value
      notifyError(error.value)
      return
    }
    saving.value = true
    error.value = ''
    // Freeze the payload across sequential CAS writes; edits made while saving remain local.
    const snapshot = JSON.parse(JSON.stringify(form.value)) as TelegramBotSettingsForm
    try {
      for (const section of sections) {
        const data = section === 'basic' ? { basic: snapshot.basic, default_locale: snapshot.default_locale } : snapshot[section]
        const res = await adminAPI.updateTelegramBotSettingsSection(section, version.value, data)
        const nextVersion = res.data?.data?.config_version
        if (!Number.isSafeInteger(nextVersion) || nextVersion <= version.value) {
          throw new Error('Invalid configuration version response')
        }
        version.value = nextVersion
      }
      notifySuccess(t('telegramBot.settings.saveSuccess'))
    } catch (err: any) {
      // A conflict or an ambiguous transport failure requires an explicit reload.
      loaded.value = false
      error.value = t(err?.response?.status === 409 ? 'telegramBot.instances.conflict' : 'telegramBot.settings.saveFailed')
      notifyError(error.value)
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
    } catch {
      notifyError(t('telegramBot.settings.uploadFailed'))
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
    form.value.menu.items.push(createMenuItem())
  }

  const removeMenuItem = (index: number) => {
    form.value.menu.items.splice(index, 1)
  }

  const moveMenuItem = (index: number, direction: 'up' | 'down') => {
    const items = form.value.menu.items
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= items.length) return
    ;[items[index], items[targetIndex]] = [items[targetIndex]!, items[index]!]
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
  }

  const moveHelpItem = (index: number, direction: 'up' | 'down') => {
    const items = form.value.help.items
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= items.length) return
    ;[items[index], items[targetIndex]] = [items[targetIndex]!, items[index]!]
  }

  const getMenuActionValuePlaceholder = (type: MenuActionType) => menuActionTypes.includes(type)
    ? t(`telegramBot.settings.menuActionValuePlaceholder_${type}`)
    : t('telegramBot.settings.menuActionValuePlaceholder')

  const getMenuActionValueHint = (type: MenuActionType) => menuActionTypes.includes(type)
    ? t(`telegramBot.settings.menuActionValueHint_${type}`)
    : t('telegramBot.settings.menuUnsupportedType', { type })

  return {
    canSave,
    error,
    loaded,
    coverFileInput,
    currentLang,
    fetchConfig,
    form,
    handleUploadCover,
    addHelpItem,
    addMenuItem,
    getMenuActionValueHint,
    getMenuActionValuePlaceholder,
    languages,
    loading,
    menuActionTypes,
    getMenuItemError,
    menuValidationError,
    moveHelpItem,
    moveMenuItem,
    removeHelpItem,
    removeMenuItem,
    saveConfig,
    saving,
    uploadingCover,
  }
}
