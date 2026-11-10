import { computed, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { getSupportSettings, saveSupportSettings, type SupportPolicy, type SupportSettings } from '@/api/telegramSupportSettings'
import { notifySuccess } from '@/utils/notify'

/** Validate all wire fields before a replace; server remains authoritative. */
export function validSupportPolicy(policy: SupportPolicy): boolean {
  const values = Object.values(policy)
  return values.length === 6 && values.every(Number.isSafeInteger)
    && policy.max_messages >= 1 && policy.max_messages <= 100
    && policy.max_bytes >= 1024 && policy.max_bytes <= 4 * 1024 * 1024
    && policy.max_attempts >= 1 && policy.max_attempts <= 10
    && policy.retention_seconds >= 60 && policy.retention_seconds <= 604800
    && policy.challenge_ttl_seconds >= 60 && policy.challenge_ttl_seconds <= 3600
    && policy.challenge_ttl_seconds <= policy.retention_seconds
    && policy.cooldown_seconds >= 60 && policy.cooldown_seconds <= 3600
}

/** Maintain one revision-bound draft; switching Bot invalidates stale responses. */
export function useTelegramSupportSettings() {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const botID = ref('')
  const snapshot = ref<SupportSettings | null>(null)
  const draft = ref<SupportPolicy | null>(null)
  const loading = ref(false)
  const saving = ref(false)
  const error = ref('')
  const conflict = ref(false)
  let request = 0
  onScopeDispose(() => { ++request })
  const validBot = computed(() => /^[1-9]\d*$/.test(botID.value) && Number.isSafeInteger(Number(botID.value)))
  const canRead = computed(() => validBot.value && auth.hasPermission(`GET:/admin/telegram-bot/support/settings/${botID.value}`))
  const canWrite = computed(() => validBot.value && auth.hasPermission(`PUT:/admin/telegram-bot/support/settings/${botID.value}`))
  const canSave = computed(() => canWrite.value && !!snapshot.value && !!draft.value && validSupportPolicy(draft.value) && !conflict.value && !loading.value && !saving.value)

  /** Map HTTP status to local user text without echoing server internals. */
  function fail(cause: unknown): string {
    const status = (cause as { response?: { status?: number } })?.response?.status
    if (status === 409) { conflict.value = true; return t('telegramSupportSettings.conflict') }
    return t(status === 403 ? 'telegramBot.groups.forbidden' : 'telegramBot.instances.failed')
  }

  /** Load settings; readiness-only refreshes preserve policy drafts and their CAS version.
   * @param preserveDraft Whether to refresh only readiness after a binding change. @returns Request completion.
   */
  async function load(preserveDraft = false) {
    if (saving.value) return
    const current = ++request
    if (preserveDraft && snapshot.value) snapshot.value.production_ready = false
    if (!preserveDraft) { snapshot.value = null; draft.value = null; conflict.value = false }
    error.value = ''
    if (!canRead.value) { loading.value = false; return }
    loading.value = true
    try {
      const response = await getSupportSettings(Number(botID.value))
      if (current !== request) return
      const value = response.data.data as SupportSettings
      if (value.bot_id !== Number(botID.value) || !validSupportPolicy(value.policy)) throw new Error('Invalid policy response')
      if (preserveDraft && snapshot.value) snapshot.value.production_ready = value.production_ready
      else { snapshot.value = value; draft.value = { ...value.policy } }
    } catch (cause) { if (current === request) error.value = fail(cause) }
    finally { if (current === request) loading.value = false }
  }

  /** Select a Bot explicitly; disabled while saving so a response cannot replace another draft. */
  async function select(value: string) {
    if (saving.value) return
    botID.value = value
    await load()
  }

  /** Apply server-advertised defaults locally; saving still requires the existing CAS version. */
  function defaults() {
    if (snapshot.value && !saving.value && !loading.value) draft.value = { ...snapshot.value.defaults }
  }

  /** Save only the loaded revision; conflicts preserve the draft and require explicit reload. */
  async function save() {
    if (!canSave.value || !draft.value || !snapshot.value) return
    const current = request
    saving.value = true; error.value = ''
    try {
      const response = await saveSupportSettings(Number(botID.value), snapshot.value.version, draft.value)
      if (current !== request) return
      const value = response.data.data as SupportSettings
      if (value.bot_id !== Number(botID.value) || !validSupportPolicy(value.policy)) throw new Error('Invalid policy response')
      snapshot.value = value; draft.value = { ...value.policy }
      notifySuccess(t('telegramBot.groups.saved'))
    } catch (cause) { if (current === request) error.value = fail(cause) }
    finally { if (current === request) saving.value = false }
  }
  return { botID, snapshot, draft, loading, saving, error, conflict, canRead, canWrite, canSave, load, select, defaults, save }
}
