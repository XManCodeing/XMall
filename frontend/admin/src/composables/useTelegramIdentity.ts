import { computed, onBeforeUnmount, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { recoveryBase, telegramRecoveryAPI } from '@/api/telegramRecovery'
import { isIdentityDetail, telegramIdentityAPI as api, validIdentityID, type IdentityDetail } from '@/api/telegramIdentity'

/** Bound required UTF-8 evidence, not JavaScript code units. @param value Text. @param limit Byte ceiling. @returns Valid text. */
function bounded(value: string, limit: number) { return !!value.trim() && new TextEncoder().encode(value).length <= limit }
/** Select a safe error translation key. @param cause Rejection. @returns Localization key. */
function errorKey(cause: unknown) {
  const status = (cause as { response?: { status?: number } })?.response?.status
  return `telegramRecovery.identity.${status === 403 ? 'forbidden' : status === 409 ? 'conflict' : isDefiniteRejection(cause) ? 'rejected' : 'uncertain'}`
}
/** Treat only definitive client rejections as unwritten; transport/server failures remain unknown.
 * @param cause HTTP rejection. @returns Whether the server definitely rejected this request.
 */
function isDefiniteRejection(cause: unknown) {
  const status = (cause as { response?: { status?: number } })?.response?.status
  return typeof status === 'number' && status >= 400 && status < 500 && status !== 408
}

type IdentityAction = 'confirm' | 'resume' | 'cancel'
type IdentityPermission = (method: string, suffix: string) => boolean

/** Derive mutation candidates from the last validated GET, never from an empty mappings list.
 * @param detail Persisted detail. @param blocked Local write/read protection. @param can Exact route permission. @returns Action candidates and phase.
 */
function identityCapabilities(detail: Ref<IdentityDetail | undefined>, blocked: () => boolean, can: IdentityPermission) {
  /** Check state, capability and route for the current persisted snapshot. @param action Intended action. @returns Candidate, not server authorization. */
  const allowed = (action: IdentityAction) => {
    const current = detail.value
    if (!current || blocked()) return false
    const { task } = current
    if (action === 'cancel') return ['awaiting_confirmation', 'fencing'].includes(task.state) && can('POST', `/tasks/${task.id}/cancel`)
    if (current.execution_available !== true || !can('POST', `/tasks/${task.id}/identity-${action}`)) return false
    return action === 'confirm' ? task.state === 'awaiting_confirmation' && task.confirmed_actor_id === '0'
      : ['fencing', 'recovering'].includes(task.state) && task.confirmed_actor_id !== '0'
  }
  const phase = computed(() => {
    const state = detail.value?.task.state
    if (!state) return ''
    if (state === 'completed') return 'phaseCompleted'
    if (state === 'recovering') return 'phaseSubmitted'
    return ['awaiting_confirmation', 'fencing', 'cancelled'].includes(state) ? 'phaseUnsubmitted' : 'unknown'
  })
  return { canConfirm: computed(() => allowed('confirm')), canResume: computed(() => allowed('resume')), canCancel: computed(() => allowed('cancel')), phase }
}

/** Own a string-safe identity draft independently from group replacement and global theme.
 * @param existingTaskID Existing-task mode, which never permits creation. @returns Draft and explicitly reviewed actions.
 */
export function useTelegramIdentity(existingTaskID: () => string | undefined = () => undefined) {
  const { t } = useI18n(), auth = useAdminAuthStore()
  const draft = reactive({ account: '', oldID: '', newID: '', targets: '', note: '', reason: '', verified: false })
  const open = ref(false), busy = ref(false), error = ref(''), taskID = ref(''), detail = ref<IdentityDetail>()
  const confirmOpen = ref(false), acknowledged = ref(false), uncertain = ref(false), accepted = ref(false)
  const action = ref<IdentityAction>('confirm')
  let epoch = 0, readGeneration = 0
  /** Authorize one exact recovery route. @param method HTTP verb. @param suffix Resource suffix. @returns Permission. */
  const can = (method: string, suffix: string) => auth.hasPermission(`${method}:${recoveryBase}${suffix}`)
  const canPreview = computed(() => can('POST', '/identity-preflight'))
  const targets = computed(() => draft.targets.split(/[\s,，]+/).filter(Boolean))
  const valid = computed(() => [draft.account, draft.oldID, draft.newID].every(value => validIdentityID(value))
    && draft.oldID !== draft.newID && targets.value.length > 0 && targets.value.length <= 100
    && targets.value.every(value => validIdentityID(value)) && new Set(targets.value).size === targets.value.length
    && draft.verified && bounded(draft.note, 1024) && bounded(draft.reason, 512))
  const { canConfirm, canResume, canCancel, phase } = identityCapabilities(detail, () => uncertain.value || accepted.value || !!error.value, can)
  const canStartNext = computed(() => !existingTaskID() && canPreview.value && !busy.value && !error.value
    && !accepted.value && !uncertain.value && !!detail.value && ['completed', 'cancelled'].includes(detail.value.task.state))
  const canAbandon = computed(() => !existingTaskID() && canPreview.value && !taskID.value && uncertain.value && !busy.value)
  const canAct = computed(() => action.value === 'cancel' ? canCancel.value : action.value === 'confirm' ? canConfirm.value : canResume.value)
  /** Map failure without exposing platform payloads. @param cause Rejection. @returns Localized error. */
  const message = (cause: unknown) => t(errorKey(cause))
  /** Open without discarding a same-account draft. @returns Nothing. */
  function show() { if (canPreview.value && !busy.value) open.value = true }
  /** Fetch persisted state once; never replay mutations. @returns Completion promise. */
  async function refresh() {
    const id = taskID.value, account = epoch
    if (!validIdentityID(id) || !can('GET', `/tasks/${id}`) || busy.value) return
    const request = ++readGeneration
    busy.value = true; error.value = ''; confirmOpen.value = false; acknowledged.value = false; detail.value = undefined
    try {
      const response = await api.get(id)
      if (account !== epoch || request !== readGeneration || taskID.value !== id || !can('GET', `/tasks/${id}`)) return
      if (!isIdentityDetail(response.data.data) || response.data.data.task.id !== id) throw new Error('Invalid identity detail')
      detail.value = response.data.data; accepted.value = false; uncertain.value = false
    } catch (cause) { if (account === epoch && request === readGeneration) error.value = message(cause) }
    finally { if (account === epoch && request === readGeneration) busy.value = false }
  }
  /** Persist preview only, requiring server-verified website binding and exact scope.
   * Unknown responses lock this attempt; an explicit read is safe but never auto-submits again. @returns Completion promise.
   */
  async function preview() {
    if (existingTaskID() || !valid.value || !canPreview.value || busy.value || uncertain.value || taskID.value) return
    const account = epoch
    busy.value = true; error.value = ''; detail.value = undefined; accepted.value = false
    try {
      const response = await api.preflight({ account_id: draft.account, old_telegram_id: draft.oldID, new_telegram_id: draft.newID,
        target_ids: [...targets.value], verification_note: draft.note, reason: draft.reason, human_verified: true, request_key: crypto.randomUUID() })
      if (account !== epoch || !canPreview.value) return
      const task = response.data.data
      if (!validIdentityID(task?.id) || task.kind !== 'identity_transfer') throw new Error('Invalid preflight response')
      taskID.value = task.id
    } catch (cause) { if (account === epoch) { error.value = message(cause); uncertain.value = !isDefiniteRejection(cause) } }
    finally { if (account === epoch) busy.value = false }
    if (account === epoch && taskID.value) await refresh()
  }
  /** Open second confirmation for the currently reviewed snapshot. @returns Nothing. */
  function askConfirm(nextAction: IdentityAction = 'confirm') {
    const allowed = nextAction === 'cancel' ? canCancel.value : nextAction === 'confirm' ? canConfirm.value : canResume.value
    if (allowed && !busy.value) {
      action.value = nextAction; acknowledged.value = false; confirmOpen.value = true
    }
  }
  /** Submit once with the exact reviewed CAS; approval is not membership proof. @returns Completion promise. */
  async function confirm() {
    const current = detail.value, account = epoch
    if (!current || !canAct.value || !confirmOpen.value || !acknowledged.value || busy.value) return
    if (action.value === 'confirm' && !(Date.parse(current.task.expires_at) > Date.now())) { error.value = t('telegramRecovery.identity.expired'); confirmOpen.value = false; return }
    busy.value = true; error.value = ''
    try {
      const response = action.value === 'cancel' ? await telegramRecoveryAPI.cancel(current.task.id, current.task.version)
        : action.value === 'confirm' ? await api.confirm(current.task) : await api.resume(current.task)
      if (account !== epoch) return
      const task = response.data.data
      if (!validIdentityID(task?.id) || task.id !== current.task.id || task.kind !== 'identity_transfer') throw new Error('Invalid confirmation response')
      accepted.value = true; confirmOpen.value = false
    } catch (cause) { if (account === epoch) { error.value = message(cause); uncertain.value = !isDefiniteRejection(cause); acknowledged.value = false } }
    finally { if (account === epoch) busy.value = false }
    // A refresh is deliberately manual, preserving an uncertain response warning until the operator reads progress.
  }
  /** Clear account data and prevent callbacks from previous credentials. @returns Nothing. */
  function clear() {
    epoch++; readGeneration++; Object.assign(draft, { account: '', oldID: '', newID: '', targets: '', note: '', reason: '', verified: false })
    open.value = false; busy.value = false; error.value = ''; taskID.value = ''; detail.value = undefined
    confirmOpen.value = false; acknowledged.value = false; uncertain.value = false; accepted.value = false
  }
  /** Start a fresh local draft only after an authoritative terminal read. @returns Nothing. */
  function startNext() { if (canStartNext.value) { clear(); open.value = true } }
  /** Abandon an ID-less local attempt; this does not cancel any server task. @returns Nothing. */
  function abandon() { if (canAbandon.value) { clear(); open.value = true } }
  // Evidence applies to one exact identity and scope, not subsequently edited identifiers.
  watch(() => draft.account, () => { draft.oldID = ''; draft.newID = ''; draft.targets = ''; draft.verified = false }, { flush: 'sync' })
  watch(() => draft.oldID, () => { draft.targets = ''; draft.verified = false }, { flush: 'sync' })
  watch(() => [draft.newID, draft.targets], () => { draft.verified = false }, { flush: 'sync' })
  watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], clear, { flush: 'sync' })
  onBeforeUnmount(clear)
  return { draft, open, busy, error, taskID, detail, confirmOpen, acknowledged, uncertain, accepted,
    can, canPreview, valid, canConfirm, canResume, canCancel, canAct, canStartNext, canAbandon, phase, startNext, abandon, action, show, preview, refresh, askConfirm, confirm }
}
