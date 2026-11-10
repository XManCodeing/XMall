import { computed, getCurrentInstance, onActivated, onDeactivated, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramGroupsAPI, type GroupDetail, type GroupInput, type TelegramGroup } from '@/api/telegramGroups'
import { notifySuccess, notifyInfo, notifyErrorOnce } from '@/utils/notify'

export function groupFresh(item: TelegramGroup, now = Date.now()) {
  return item.fresh && !!item.fresh_until && Date.parse(item.fresh_until) > now
    && item.observation.route_version === item.route_version && !item.checking && !item.observation.error_class
}
export function groupReady(item: TelegramGroup, now = Date.now()) {
  return groupFresh(item, now) && item.observation.state === 'ready' && !item.missing_capabilities?.length
}
export function groupState(item: TelegramGroup, now = Date.now()) {
  if (item.checking) return 'checking'
  if (item.observation.state === 'ready' && !groupReady(item, now)) return 'stale'
  return item.observation.state || 'unknown'
}
/** Validate a decimal input without losing precision; the server resolves unsigned group IDs.
 * @param value User-entered ID. @returns Whether its trimmed form fits the accepted integer range.
 */
export function validChatID(value: string) {
  const trimmed = value.trim()
  return /^-?[1-9]\d*$/.test(trimmed) && trimmed.length <= 20
    && BigInt(trimmed) >= -9223372036854775808n && BigInt(trimmed) <= 9223372036854775807n
}
export function groupErrorKey(error: unknown) {
  const response = (error as { response?: { status?: number; data?: { error_class?: string } } })?.response
  if (response?.data?.error_class === 'chat_id_unresolved') return 'chatUnresolved'
  if (response?.data?.error_class === 'chat_id_ambiguous') return 'chatAmbiguous'
  if (response?.data?.error_class === 'capability_unavailable') return 'capabilityUnavailable'
  if (response?.status === 409) return 'conflict'
  if (response?.status === 429 || response?.data?.error_class === 'check_busy') return 'checkBusy'
  return 'failed'
}
export function groupInput(item?: TelegramGroup): GroupInput {
  return {
    name: item?.name ?? '', purpose: item?.purpose ?? 'membership', bot_instance_id: item?.bot_instance_id ?? 0,
    telegram_chat_id: item?.telegram_chat_id ?? '', enabled: item?.enabled ?? false,
    expected_version: item?.version ?? 0, expected_route_version: item?.route_version ?? 0,
  }
}
export function useTelegramGroups() {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const can = (method: string, suffix = '') => auth.hasPermission(`${method}:/admin/telegram-bot/groups${suffix}`)
  const canList = computed(() => can('GET'))
  const items = ref<TelegramGroup[]>([])
  const page = ref(1)
  const pageSize = ref(20)
  const total = ref(0)
  const botFilter = ref('')
  const purposeFilter = ref('all')
  const loading = ref(false)
  const error = ref('')
  const detail = ref<GroupDetail | null>(null)
  const detailOpen = ref(false)
  const detailLoading = ref(false)
  const detailError = ref('')
  const editorOpen = ref(false)
  const editing = ref<TelegramGroup | null>(null)
  const form = ref<GroupInput>(groupInput())
  const formError = ref('')
  const conflict = ref(false)
  const enableChecking = ref(false)
  const enableNotice = ref('')
  const enableProof = ref('')
  let enableRequest = 0
  const saving = ref(false)
  const checkingID = ref<number | null>(null)
  const now = ref(Date.now())
  let listRequest = 0
  let detailRequest = 0
  let editorRequest = 0
  let disposed = false
  let timer: ReturnType<typeof setInterval> | undefined
  /** Refresh freshness labels only while their cached page is active. @returns Nothing. */
  function startClock() {
    if (timer !== undefined) return
    now.value = Date.now()
    timer = setInterval(() => { now.value = Date.now() }, 1000)
  }
  /** Pause the clock without resetting the group editor draft. @returns Nothing. */
  function stopClock() { if (timer !== undefined) clearInterval(timer); timer = undefined }
  startClock()
  if (getCurrentInstance()) { onActivated(startClock); onDeactivated(stopClock) }
  onScopeDispose(() => { disposed = true; ++listRequest; ++detailRequest; ++editorRequest; stopClock() })
  const message = (err: unknown) => t(`telegramBot.groups.${groupErrorKey(err)}`)
  const routeChanged = computed(() => !!editing.value && (
    form.value.bot_instance_id !== editing.value.bot_instance_id || form.value.telegram_chat_id !== editing.value.telegram_chat_id
    || form.value.purpose !== editing.value.purpose
  ))
  const routeKey = computed(() => `${form.value.bot_instance_id}:${form.value.telegram_chat_id}:${form.value.purpose}`)
  const canEnable = computed(() => enableProof.value === routeKey.value || (!!editing.value && !routeChanged.value && (editing.value.enabled || groupReady(editing.value, now.value))))
  watch(routeKey, () => {
    ++enableRequest; enableChecking.value = false; enableProof.value = ''; enableNotice.value = ''
    if (routeChanged.value || !editing.value) form.value.enabled = false
  }, { flush: 'sync' })
  watch(editorOpen, () => { ++enableRequest; enableChecking.value = false; enableNotice.value = ''; enableProof.value = '' }, { flush: 'sync' })
  // Only existing records retain editable names; creation trusts the server's Telegram observation.
  const valid = computed(() => (!editing.value || (!!form.value.name.trim() && Array.from(form.value.name).length <= 120))
    && Number.isSafeInteger(form.value.bot_instance_id) && form.value.bot_instance_id > 0 && validChatID(form.value.telegram_chat_id)
    && !(routeChanged.value && editing.value?.enabled) && (!form.value.enabled || canEnable.value))
  const validFilter = computed(() => (!botFilter.value || (/^[1-9]\d*$/.test(botFilter.value)
    && Number.isSafeInteger(Number(botFilter.value)))) && ['all', 'support', 'membership'].includes(purposeFilter.value))
  /** Invalidate list identity without querying. No parameters or return value. */
  function clearList() {
    ++listRequest; items.value = []; total.value = 0; error.value = ''; loading.value = false; page.value = 1
  }
  watch([botFilter, purposeFilter, canList], clearList, { flush: 'sync' })
  /** Load a validated page; nextPage defaults to the current page. Returns request completion. */
  async function load(nextPage = page.value) {
    const request = ++listRequest
    if (!can('GET') || !validFilter.value || !Number.isSafeInteger(nextPage) || nextPage < 1) {
      items.value = []; total.value = 0; loading.value = false
      return
    }
    loading.value = true; error.value = ''
    try {
      const res = await telegramGroupsAPI.list({ page: nextPage, page_size: pageSize.value,
        bot_instance_id: botFilter.value ? Number(botFilter.value) : undefined,
        purpose: purposeFilter.value === 'all' ? undefined : purposeFilter.value })
      if (request !== listRequest || !can('GET')) return
      if (!Array.isArray(res.data?.data?.items)) throw new Error(t('common.api.responseMissing'))
      items.value = res.data.data.items; total.value = res.data.data.total; page.value = res.data.data.page
    } catch (err) {
      if (request === listRequest) {
        error.value = message(err); items.value = []
        notifyErrorOnce(err, error.value)
      }
    }
    finally { if (request === listRequest) loading.value = false }
  }
  /** Search current inputs on page one. No parameters; returns one list request. */
  function search() { return load(1) }
  /** Clear filters and reload defaults on page one. No parameters; returns one list request. */
  function resetFilters() {
    botFilter.value = ''; purposeFilter.value = 'all'; page.value = 1
    return load(1)
  }
  async function showDetail(id: number) {
    if (!can('GET', `/${id}`)) return
    const request = ++detailRequest
    detailOpen.value = true; detailLoading.value = true; detailError.value = ''; detail.value = null
    try {
      const res = await telegramGroupsAPI.get(id)
      if (request === detailRequest) detail.value = res.data.data
    } catch (err) { if (request === detailRequest) detailError.value = message(err) }
    finally { if (request === detailRequest) detailLoading.value = false }
  }
  function openEditor(item?: TelegramGroup) {
    if (saving.value || !(item ? can('PATCH', `/${item.id}`) : can('POST'))) return
    ++editorRequest
    editing.value = item ? { ...item, observation: { ...item.observation } } : null
    form.value = groupInput(item); formError.value = ''; conflict.value = false; editorOpen.value = true
  }
  // Explicit reload only: a conflict never silently updates CAS or discards a draft.
  async function reloadEditor() {
    if (!editing.value || !can('GET', `/${editing.value.id}`) || saving.value) return
    const request = ++editorRequest
    saving.value = true
    try {
      const res = await telegramGroupsAPI.get(editing.value.id)
      if (request !== editorRequest) return
      editing.value = res.data.data; form.value = groupInput(res.data.data); conflict.value = false; formError.value = ''
    } catch (err) { if (request === editorRequest) formError.value = message(err) }
    finally { if (request === editorRequest) saving.value = false }
  }
  /** Explain failed capabilities using existing localized permission names. @param row Probe result. @returns User-facing diagnosis. */
  function enableFailure(row: TelegramGroup) {
    const reason = row.observation.error_class
      ? t(`telegramBot.groups.values.${row.observation.error_class}`)
      : row.missing_capabilities.map(value => t(`telegramBot.groups.values.${value}`)).join('、') || t('telegramBot.groups.capabilityUnavailable')
    return t('telegramBot.groups.enableDenied', { reason })
  }
  /** Check a draft before switching on; late results cannot enable a different route. @param enabled Requested state. @returns Completion promise. */
  async function toggleEnabled(enabled: boolean) {
    if (saving.value || enableChecking.value) return
    enableNotice.value = ''; formError.value = ''
    if (!enabled) { form.value.enabled = false; enableProof.value = ''; return }
    if (!valid.value || !(editing.value ? can('PATCH', `/${editing.value.id}`) : can('POST'))) {
      enableNotice.value = t('telegramBot.groups.enableIncomplete'); return
    }
    if (!can('POST', editing.value ? `/${editing.value.id}/enable-preview` : '/enable-preview')) {
      enableNotice.value = t('telegramBot.groups.forbidden'); return
    }
    const request = ++enableRequest, key = routeKey.value
    enableChecking.value = true
    try {
      const response = await telegramGroupsAPI.previewEnable({ ...form.value, enabled: true }, editing.value?.id)
      if (disposed || request !== enableRequest || key !== routeKey.value || !editorOpen.value) return
      const row = response.data.data as TelegramGroup
      if (row.enabled && groupReady(row)) {
        enableProof.value = key; form.value.enabled = true; enableNotice.value = t('telegramBot.groups.enableReady')
      } else { form.value.enabled = false; enableNotice.value = enableFailure(row) }
    } catch (err) {
      if (!disposed && request === enableRequest) { form.value.enabled = false; enableNotice.value = message(err) }
    } finally { if (request === enableRequest) enableChecking.value = false }
  }
  async function save() {
    if (enableChecking.value || saving.value || conflict.value || !valid.value || !(editing.value ? can('PATCH', `/${editing.value.id}`) : can('POST'))) return false
    saving.value = true; formError.value = ''
    try {
      const input = { ...form.value }
      const res = editing.value ? await telegramGroupsAPI.patch(editing.value.id, input) : await telegramGroupsAPI.create(input)
      if (disposed) return false
      if (detail.value?.id === res.data.data.id) detail.value = res.data.data
      if (res.data.data.enable_rejected) {
        editing.value = res.data.data; form.value = groupInput(res.data.data); enableProof.value = ''
        enableNotice.value = `${t('telegramBot.groups.savedDisabled')} ${enableFailure(res.data.data)}`
        notifyInfo(enableNotice.value); void load(); return true
      }
      editorOpen.value = false; notifySuccess(t('telegramBot.groups.saved')); void load(); return true
    } catch (err) { if (!disposed) { formError.value = message(err); conflict.value = groupErrorKey(err) === 'conflict' }; return false }
    finally { if (!disposed) saving.value = false }
  }
  async function check(item: TelegramGroup) {
    if (checkingID.value !== null || item.checking || !can('POST', `/${item.id}/check`)) return
    checkingID.value = item.id; detailError.value = ''; error.value = ''
    const selectedDetail = detailRequest
    try {
      const res = await telegramGroupsAPI.check(item.id, item.route_version)
      if (disposed) return
      const result: GroupDetail = res.data.data
      if (selectedDetail === detailRequest && detail.value?.id === item.id) detail.value = result
      if (groupReady(result)) notifySuccess(t('telegramBot.groups.checkReady'))
      else notifyInfo(t('telegramBot.groups.checkObserved'))
      void load()
    } catch (err) {
      if (!disposed) {
        error.value = message(err)
        if (detail.value?.id === item.id) detailError.value = message(err)
        // Detail dialogs keep their own feedback; list actions use the shared notification.
        if (!detailOpen.value) notifyErrorOnce(err, error.value)
      }
    } finally { if (!disposed) checkingID.value = null }
  }
  return { can, canList, items, page, pageSize, total, botFilter, purposeFilter, loading, error, load, validFilter, search, resetFilters,
    detail, detailOpen, detailLoading, detailError, showDetail, editorOpen, editing, form, formError, conflict,
    enableChecking, enableNotice, toggleEnabled,
    saving, checkingID, now, routeChanged, canEnable, valid, openEditor, reloadEditor, save, check }
}
