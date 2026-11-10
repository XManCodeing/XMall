import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'

/** Manages the global registration section without instance/profile dependencies.
 * No parameters. Returns draft, loading/permission state and load/save actions.
 */
export function useTelegramBotSettingsRegistration() {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const enabled = ref(false)
  const version = ref(0)
  const loaded = ref(false)
  const busy = ref(false)
  const error = ref('')
  const canLoad = computed(() => auth.hasPermission('GET:/admin/settings/telegram-bot'))
  const canSave = computed(() => loaded.value && !busy.value && auth.hasPermission('PATCH:/admin/settings/telegram-bot/registration'))

  /** Loads the latest global policy; returns a promise and never loads profiles. */
  async function load() {
    if (busy.value) return
    loaded.value = false
    if (!canLoad.value) {
      error.value = t('telegramBot.instances.forbidden')
      return
    }
    busy.value = true
    try {
      const response = await adminAPI.getTelegramBotSettings()
      const config = response.data?.data
      if (!config || !Number.isSafeInteger(config.config_version) || config.config_version < 0) throw new Error('Invalid configuration version')
      version.value = config.config_version
      enabled.value = config.registration?.enabled === true
      error.value = ''
      loaded.value = true
    } catch (cause) {
      error.value = t('telegramBot.settings.loadFailed')
      notifyErrorOnce(cause, error.value)
    } finally {
      busy.value = false
    }
  }

  /** Saves only registration with CAS; preserves the draft on failure. */
  async function save() {
    if (!canSave.value) return
    busy.value = true
    try {
      const response = await adminAPI.updateTelegramBotSettingsSection('registration', version.value, { enabled: enabled.value })
      const nextVersion = response.data?.data?.config_version
      if (!Number.isSafeInteger(nextVersion) || nextVersion <= version.value) throw new Error('Invalid configuration version')
      version.value = nextVersion
      error.value = ''
      notifySuccess(t('telegramBot.settings.saveSuccess'))
    } catch (cause) {
      loaded.value = false
      const status = (cause as { response?: { status?: number } })?.response?.status
      error.value = t(status === 409 ? 'telegramBot.instances.conflict' : 'telegramBot.settings.saveFailed')
      notifyErrorOnce(cause, error.value)
    } finally {
      busy.value = false
    }
  }

  return { enabled, loaded, busy, error, canLoad, canSave, load, save }
}
