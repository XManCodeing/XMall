import { computed, onBeforeUnmount, ref, watch, type ComputedRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRecoveryBotRoutes } from './useRecoveryBotRoutes'
import { useAdminAuthStore } from '@/stores/auth'
import { useTelegramBotOptions } from './useTelegramBotOptions'
import { telegramGroupsAPI, type TelegramGroup } from '@/api/telegramGroups'
import { notifyErrorOnce } from '@/utils/notify'
import { recoveryBase, telegramRecoveryAPI as api, validRecoveryChatID, validRecoveryBotID, type RecoveryTask, type RecoveryDetail, type RecoveryStep } from '@/api/telegramRecovery'

type Permission = (method: string, path: string) => boolean
type ErrorMessage = (cause: unknown) => string

/** Reject legacy cursor responses before committing numbered state.
 * @param data Server response body. @returns Nothing; throws for missing pagination metadata.
 */
function assertNumberedPage(data: unknown): void {
  const page = data as { items?: unknown; page?: number; page_size?: number; total?: number; total_page?: number } | null
  if (!page || !Array.isArray(page.items) || !Number.isSafeInteger(page.page) || (page.page ?? 0) < 1 || !Number.isSafeInteger(page.page_size) || (page.page_size ?? 0) < 1 || !Number.isSafeInteger(page.total) || (page.total ?? -1) < 0 || !Number.isSafeInteger(page.total_page) || (page.total_page ?? 0) < 1) {
    throw new Error('Invalid numbered pagination response')
  }
}

/** Manage authorized numbered reads, isolating stale responses. @param can Permission predicate. @param message Safe error mapper. @returns Reader state and actions. */
function useRecoveryReader(can: Permission, message: ErrorMessage) {
  const items = ref<RecoveryTask[]>([]), loading = ref(false), error = ref('')
  const page = ref(1), pageSize = ref(20), total = ref(0), totalPage = ref(1), canList = computed(() => can('GET', '/tasks'))
  const detail = ref<RecoveryDetail>(), detailOpen = ref(false), detailLoading = ref(false), detailError = ref('')
  const steps = ref<RecoveryStep[]>([]), stepsLoading = ref(false), stepsError = ref('')
  const stepsPage = ref(1), stepsPageSize = ref(20), stepsTotal = ref(0), stepsTotalPage = ref(1)
  let listGeneration = 0, detailGeneration = 0
  /** Fetch one database page and commit navigation only after success. @param target One-based page. @param size Requested size. @returns Completion promise. */
  async function load(target = page.value, size = pageSize.value) {
    if (!canList.value || loading.value || !Number.isSafeInteger(target) || target < 1 || ![10, 20, 50, 100].includes(size)) return
    const generation = ++listGeneration
    loading.value = true; error.value = ''
    try {
      const response = await api.listPage(target, size)
      if (generation !== listGeneration || !canList.value) return
      const data = response.data.data
      assertNumberedPage(data)
      items.value = data.items; page.value = data.page; pageSize.value = data.page_size; total.value = data.total; totalPage.value = data.total_page
    } catch (cause) {
      if (generation === listGeneration) {
        error.value = message(cause)
        notifyErrorOnce(cause, error.value)
      }
    }
    finally { if (generation === listGeneration) loading.value = false }
  }
  /** Reset to the first page when changing size. @param size Selected size. @returns Completion promise. */
  async function changePageSize(size: number) { await load(1, size) }
  /** Read a checkpoint page directly; errors retain the current rows/page/size. @param target One-based page. @param size Requested size. @returns Completion promise. */
  async function loadSteps(target = 1, size = stepsPageSize.value) {
    const id = detail.value?.task.id, generation = detailGeneration
    if (!id || !can('GET', `/tasks/${id}/steps`) || stepsLoading.value || !Number.isSafeInteger(target) || target < 1 || ![10, 20, 50, 100].includes(size)) return
    stepsLoading.value = true; stepsError.value = ''
    try {
      const response = await api.stepsPage(id, target, size)
      if (generation !== detailGeneration || !can('GET', `/tasks/${id}/steps`)) return
      const data = response.data.data
      assertNumberedPage(data)
      steps.value = data.items; stepsPage.value = data.page; stepsPageSize.value = data.page_size; stepsTotal.value = data.total; stepsTotalPage.value = data.total_page
    } catch (cause) { if (generation === detailGeneration) stepsError.value = message(cause) }
    finally { if (generation === detailGeneration) stepsLoading.value = false }
  }
  /** Reset checkpoints to the first page on size change. @param size Selected size. @returns Completion promise. */
  async function changeStepsPageSize(size: number) { await loadSteps(1, size) }
  /** Read persisted detail, invalidating previous requests. @param id Decimal task ID. @returns Completion promise. */
  async function show(id: string) {
    if (!can('GET', `/tasks/${id}`)) return
    const generation = ++detailGeneration
    detailOpen.value = true; detail.value = undefined; detailLoading.value = true; detailError.value = ''
    steps.value = []; stepsPage.value = 1; stepsTotal.value = 0; stepsTotalPage.value = 1; stepsLoading.value = false; stepsError.value = ''
    try {
      const response = await api.get(id)
      if (generation !== detailGeneration || !can('GET', `/tasks/${id}`)) return
      detail.value = response.data.data; void loadSteps()
    } catch (cause) { if (generation === detailGeneration) detailError.value = message(cause) }
    finally { if (generation === detailGeneration) detailLoading.value = false }
  }
  /** Clear account-scoped reads and invalidate callbacks. @returns Nothing. */
  function clear() {
    listGeneration++; detailGeneration++; items.value = []; detail.value = undefined; steps.value = []
    detailOpen.value = false; loading.value = false; detailLoading.value = false; stepsLoading.value = false
    error.value = ''; detailError.value = ''; stepsError.value = ''; stepsPage.value = 1; stepsPageSize.value = 20; stepsTotal.value = 0; stepsTotalPage.value = 1
    page.value = 1; pageSize.value = 20; total.value = 0; totalPage.value = 1
  }
  watch(detailOpen, open => { if (!open) { detailGeneration++; detail.value = undefined; steps.value = []; stepsLoading.value = false } })
  return { canList, items, loading, error, page, pageSize, total, totalPage, load, changePageSize, detail, detailOpen, detailLoading, detailError, show,
    steps, stepsLoading, stepsError, stepsPage, stepsPageSize, stepsTotal, stepsTotalPage, loadSteps, changeStepsPageSize, clear }
}

/** Manage preview drafts with shared recovery controls. @param canCreate Group permission. @param canGroups Group read permission. @param canBotCreate Bot preview permission. @param canBots Bot read permission. @param message Safe error mapper. @returns Draft state and actions. */
function useRecoveryDraft(canCreate: ComputedRef<boolean>, canGroups: ComputedRef<boolean>, canBotCreate: ComputedRef<boolean>, canBots: ComputedRef<boolean>, message: ErrorMessage) {
  const draftKind = ref<'group' | 'bot'>('group'), sourceBotID = ref(''), destinationBotID = ref('')
  const botRoutes = useRecoveryBotRoutes(sourceBotID, canGroups)
  const { bots, botsLoading, botsError, loadBots, clearBots } = useTelegramBotOptions(canBots, message)
  const editorOpen = ref(false), saving = ref(false), formError = ref(''), conflict = ref(false)
  const groupPurpose = ref<'membership' | 'support'>('membership')
  const groups = ref<TelegramGroup[]>([]), groupsLoading = ref(false), groupsError = ref(''), groupsPage = ref(1), groupsTotal = ref(0)
  const targetID = ref(''), chatID = ref(''), reason = ref(''), requestKey = ref('')
  let epoch = 0, groupsGeneration = 0
  const selected = computed(() => groups.value.find(group => String(group.id) === targetID.value))
  const valid = computed(() => {
    if (draftKind.value === 'bot') return botRoutes.valid.value && canBots.value && canBotCreate.value && !botsLoading.value && !botsError.value
      && [sourceBotID.value, destinationBotID.value].every(id => validRecoveryBotID(id) && bots.value.some(bot => bot.id === id))
      && sourceBotID.value !== destinationBotID.value && !!reason.value.trim() && new TextEncoder().encode(reason.value).length <= 512 && !conflict.value
    const group = selected.value
    return !!group && !groupsLoading.value && !groupsError.value && ['membership', 'support'].includes(groupPurpose.value)
      && group.purpose === groupPurpose.value && [group.id, group.bot_instance_id, group.version, group.route_version].every(value => Number.isSafeInteger(value) && value > 0)
      && validRecoveryChatID(chatID.value) && chatID.value !== group.telegram_chat_id && !!reason.value.trim()
      && new TextEncoder().encode(reason.value).length <= 512 && !conflict.value
  })
  /** Load fresh source versions, clearing stale selection. @param requestedPage One-based page. @returns Completion promise. */
  async function loadGroups(requestedPage = 1) {
    if (!canGroups.value || groupsLoading.value || saving.value || !['membership', 'support'].includes(groupPurpose.value)) return
    const generation = ++groupsGeneration, account = epoch, purpose = groupPurpose.value
    groupsLoading.value = true; groupsError.value = ''; groups.value = []; groupsTotal.value = 0; targetID.value = ''; conflict.value = false
    try {
      const response = await telegramGroupsAPI.list({ page: requestedPage, page_size: 20, purpose })
      if (account !== epoch || generation !== groupsGeneration || purpose !== groupPurpose.value || !canGroups.value) return
      groups.value = response.data.data.items.filter(group => group.purpose === purpose)
      groupsPage.value = response.data.data.page; groupsTotal.value = response.data.data.total
    } catch (cause) { if (account === epoch && generation === groupsGeneration) groupsError.value = message(cause) }
    finally { if (account === epoch && generation === groupsGeneration) groupsLoading.value = false }
  }
  /** Switch only supported purposes and invalidate the previous list before starting a new read.
   * @param purpose Selected purpose, never an authorization grant. @returns Nothing.
   */
  function changeGroupPurpose(purpose: string) {
    if (saving.value || !canCreate.value || !canGroups.value || (purpose !== 'membership' && purpose !== 'support') || purpose === groupPurpose.value) return
    groupsGeneration++; groupsLoading.value = false; groups.value = []; targetID.value = ''; chatID.value = ''
    groupsPage.value = 1; groupsTotal.value = 0; groupsError.value = ''; formError.value = ''; conflict.value = false; requestKey.value = ''
    groupPurpose.value = purpose
    if (editorOpen.value && draftKind.value === 'group') void loadGroups()
  }
  /** Open the shared editor; reopening the same kind retains its draft. @param kind Preview scope. @returns Nothing. */
  function openEditor(kind: 'group' | 'bot' = 'group') {
    if (!(kind === 'bot' ? canBotCreate.value : canCreate.value) || saving.value) return
    if (draftKind.value !== kind) { reason.value = ''; requestKey.value = ''; formError.value = ''; conflict.value = false }
    draftKind.value = kind; editorOpen.value = true
    if (kind === 'bot') { if (!bots.value.length) void loadBots() }
    else if (!groups.value.length) void loadGroups()
  }
  /** Refresh Bot choices after a conflict while retaining reviewed draft fields. @returns Completion promise. */
  async function refreshBots() {
    if (saving.value || botsLoading.value || !canBots.value) return
    await loadBots()
    if (!botsError.value) { conflict.value = false; formError.value = ''; requestKey.value = '' }
  }
  /** Persist preview only; retries keep their key until input changes. @returns Created task or undefined on error/denial. */
  async function persist(): Promise<RecoveryTask | undefined> {
    const group = selected.value, isBot = draftKind.value === 'bot'
    if (!valid.value || saving.value || (isBot ? !canBotCreate.value || !canBots.value : !group || !canCreate.value || !canGroups.value)) return
    const account = epoch
    saving.value = true; formError.value = ''
    try {
      requestKey.value ||= crypto.randomUUID()
      const response = isBot
        ? await api.botPreflight({ source_bot_id: sourceBotID.value, destination_bot_id: destinationBotID.value, request_key: requestKey.value, reason: reason.value, ...botRoutes.payload() })
        : await api.preflight({ target_id: group!.id, expected_version: group!.version, expected_route_version: group!.route_version,
          new_bot_instance_id: group!.bot_instance_id, new_telegram_chat_id: chatID.value, request_key: requestKey.value, reason: reason.value })
      if (account !== epoch || !(isBot ? canBotCreate.value : canCreate.value)) return
      editorOpen.value = false
      return response.data.data
    } catch (cause) {
      if (account === epoch) {
        formError.value = message(cause)
        conflict.value = (cause as { response?: { status?: number } })?.response?.status === 409
      }
    } finally { if (account === epoch) saving.value = false }
  }
  /** Discard drafts and invalidate pending callbacks. @returns Nothing. */
  function clear() {
    epoch++; botRoutes.clear(); clearBots(); groupPurpose.value = 'membership'; sourceBotID.value = ''; destinationBotID.value = ''; draftKind.value = 'group'
    groupsGeneration++; groups.value = []; groupsPage.value = 1; groupsTotal.value = 0; editorOpen.value = false; groupsLoading.value = false; saving.value = false
    formError.value = ''; groupsError.value = ''; targetID.value = ''; chatID.value = ''; reason.value = ''; requestKey.value = ''; conflict.value = false
  }
  watch([targetID, chatID, reason, sourceBotID, destinationBotID], () => { requestKey.value = '' }, { flush: 'sync' })
  watch(botRoutes.routes, () => { requestKey.value = '' }, { deep: true, flush: 'sync' })
  return { botRoutes, groupPurpose, changeGroupPurpose, draftKind, sourceBotID, destinationBotID, bots, botsLoading, botsError, refreshBots, editorOpen, saving, formError, conflict, groups, groupsLoading, groupsError, groupsPage, groupsTotal, loadGroups,
    targetID, chatID, reason, selected, valid, openEditor, persist, clear }
}

type RecoveryReader = ReturnType<typeof useRecoveryReader>
type ExecutionAction = 'confirm' | 'resume'

/** Manage reviewed execution independently from editable preflight drafts.
 * @param reader Persisted task reader. @param can Exact permission predicate. @param message Safe error mapper.
 * @returns Capability-gated execution state, confirmation and bounded refresh actions.
 */
function useRecoveryExecution(reader: RecoveryReader, can: Permission, message: ErrorMessage) {
  const { t } = useI18n()
  const executionPending = ref<{ task: RecoveryTask; action: ExecutionAction; snapshot: RecoveryDetail['snapshot'] }>()
  const executing = ref(false), executionError = ref(''), executionAcknowledged = ref(false), executionSuccess = ref('')
  const refreshingProgress = ref(false)
  let epoch = 0
  /** Fail closed for absent capability, unsupported scope or stale detail. @param action Intended mutation. @returns Allowed state. */
  function canExecute(action: ExecutionAction) {
    const current = reader.detail.value, task = current?.task
    if (!current || !task || current.execution_available !== true || reader.detailLoading.value || reader.detailError.value) return false
    const oldBot = current.snapshot.old.bot_id, newBot = current.snapshot.new.bot_id
    const supported = task.kind === 'group_replace' ? oldBot === newBot
      : task.kind === 'bot_replace' && validRecoveryBotID(oldBot) && validRecoveryBotID(newBot) && oldBot !== newBot
    if (!supported) return false
    if (!can('POST', `/tasks/${task.id}/${action}`)) return false
    return action === 'confirm'
      ? task.state === 'awaiting_confirmation' && task.confirmed_actor_id === '0' && !!task.preflight_ref
      : (['fencing', 'switching', 'recovering'].includes(task.state)
        || (task.kind === 'group_replace' && ['needs_review', 'partial_failed'].includes(task.state))) && task.confirmed_actor_id !== '0'
  }
  /** Capture immutable reviewed intent; opening never mutates the server. @param action Intended mutation. @returns Nothing. */
  function askExecute(action: ExecutionAction) {
    const current = reader.detail.value
    if (!current || !canExecute(action) || executing.value || refreshingProgress.value) return
    executionPending.value = { task: { ...current.task }, action, snapshot: current.snapshot }
    executionError.value = ''; executionAcknowledged.value = false; executionSuccess.value = ''
  }
  /** Refresh persisted progress once, without polling or touching draft inputs. @returns Completion promise. */
  async function refreshProgress() {
    const id = reader.detail.value?.task.id, account = epoch
    if (!id || refreshingProgress.value || executing.value) return
    refreshingProgress.value = true
    try { await reader.show(id); if (account === epoch) await reader.load() }
    finally { if (account === epoch) refreshingProgress.value = false }
  }
  /** Submit only after a second explicit acknowledgement, using the reviewed CAS. @returns Completion promise. */
  async function execute() {
    const pending = executionPending.value, current = reader.detail.value, account = epoch
    if (!pending || !current || executing.value || executionError.value || !executionAcknowledged.value || !canExecute(pending.action)) return
    if (pending.task.id !== current.task.id || pending.task.version !== current.task.version || pending.task.preflight_ref !== current.task.preflight_ref) {
      executionError.value = t('telegramRecovery.conflict'); return
    }
    executing.value = true; executionSuccess.value = ''
    try {
      if (pending.action === 'confirm') await api.confirm(pending.task.id, pending.task.preflight_ref, pending.task.version)
      else await api.resume(pending.task.id, pending.task.version)
      if (account !== epoch) return
      executionPending.value = undefined; executionSuccess.value = t('telegramRecovery.executionAccepted')
    } catch (cause) {
      if (account === epoch) { executionError.value = message(cause); executionAcknowledged.value = false }
    } finally {
      if (account === epoch) {
        // Even failures can follow a persisted checkpoint. Reload, but never replay a mutation automatically.
        await reader.load()
        if (account === epoch && reader.detailOpen.value && reader.detail.value?.task.id === pending.task.id) await reader.show(pending.task.id)
        if (account === epoch) executing.value = false
      }
    }
  }
  /** Invalidate callbacks on account changes and unmount. @returns Nothing. */
  function clear() {
    epoch++; executionPending.value = undefined; executing.value = false; executionError.value = ''; executionAcknowledged.value = false
    executionSuccess.value = ''; refreshingProgress.value = false
  }
  return { executionPending, executing, executionError, executionAcknowledged, executionSuccess, refreshingProgress, canExecute, askExecute, execute, refreshProgress, clear }
}

/** Coordinate task reads, drafts and separately confirmed mutations with account isolation. @returns UI state and actions. */
export function useTelegramRecovery() {
  const { t } = useI18n(), auth = useAdminAuthStore()
  const success = ref(''), cancelPending = ref<RecoveryTask>(), cancelling = ref(false), cancelError = ref('')
  let epoch = 0
  /** Check actual server permission. @param method HTTP verb. @param path Recovery suffix. @returns Permission result. */
  const can: Permission = (method, path) => auth.hasPermission(`${method}:${recoveryBase}${path}`)
  const canCreate = computed(() => can('POST', '/group-preflight'))
  const canBotCreate = computed(() => can('POST', '/bot-preflight'))
  const canBots = computed(() => auth.hasPermission('GET:/admin/telegram-bot/instances'))
  const canGroups = computed(() => auth.hasPermission('GET:/admin/telegram-bot/groups'))
  /** Map safe errors without exposing platform payloads. @param cause Rejected API error. @returns Localized message. */
  function message(cause: unknown) {
    const status = (cause as { response?: { status?: number } })?.response?.status
    return t(`telegramRecovery.${status === 403 ? 'forbidden' : status === 409 ? 'conflict' : 'failed'}`)
  }
  const reader = useRecoveryReader(can, message), draft = useRecoveryDraft(canCreate, canGroups, canBotCreate, canBots, message)
  const execution = useRecoveryExecution(reader, can, message)
  /** Persist and display the same task, without executing any migration. @returns Completion promise. */
  async function submit() {
    const account = epoch
    success.value = ''
    const task = await draft.persist()
    if (!task || account !== epoch) return
    success.value = t('telegramRecovery.created'); await reader.load(1)
    if (account === epoch) await reader.show(task.id)
  }
  /** Restrict cancellation to unconfirmed preflights. @param task Persisted task. @returns Whether cancellation is allowed. */
  function canCancel(task: RecoveryTask) {
    return !execution.executing.value && task.state === 'awaiting_confirmation' && task.confirmed_actor_id === '0' && can('POST', `/tasks/${task.id}/cancel`)
  }
  /** Open explicit cancellation confirmation. @param task Reviewed task/version. @returns Nothing. */
  function askCancel(task: RecoveryTask) {
    if (!canCancel(task) || cancelling.value) return
    cancelPending.value = { ...task }; cancelError.value = ''
  }
  /** Cancel with the reviewed version and reload persisted state. @returns Completion promise. */
  async function cancel() {
    const task = cancelPending.value, account = epoch
    if (!task || !canCancel(task) || cancelling.value || cancelError.value) return
    cancelling.value = true; cancelError.value = ''; success.value = ''
    try {
      await api.cancel(task.id, task.version)
      if (account !== epoch) return
      cancelPending.value = undefined; success.value = t('telegramRecovery.cancelled')
      await reader.load()
      if (account === epoch && reader.detail.value?.task.id === task.id) await reader.show(task.id)
    } catch (cause) {
      if (account === epoch) {
        cancelError.value = message(cause)
        if ((cause as { response?: { status?: number } })?.response?.status === 409) {
          await reader.load()
          if (account === epoch && reader.detail.value?.task.id === task.id) await reader.show(task.id)
        }
      }
    }
    finally { if (account === epoch) cancelling.value = false }
  }
  /** Clear all account-scoped state and pending callbacks. @returns Nothing. */
  function clear() {
    epoch++; reader.clear(); draft.clear(); execution.clear(); cancelPending.value = undefined; cancelling.value = false; cancelError.value = ''; success.value = ''
  }
  watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], clear, { flush: 'sync' })
  onBeforeUnmount(clear)
  return { ...reader, ...draft, ...execution, can, canCreate, canGroups, canBotCreate, canBots, success, submit, canCancel, askCancel, cancelPending, cancelling, cancelError, cancel }
}
