import { computed, onUnmounted, reactive, ref, watch, type Ref } from 'vue'
import { useAdminAuthStore } from '@/stores/auth'
import type { RecoveryTask } from '@/api/telegramRecovery'
import { supportRecordedReceipt, supportDecisions, supportEntriesPermission, supportOperationsPermission, supportResolutionPermission, supportResumePermission,
  supportEntry, supportOperation, supportPage, supportID, validRecoverySupportResolution, telegramRecoverySupportAPI as api,
  type RecoverySupportEntry, type RecoverySupportOperation, type RecoverySupportResolution, type RecoverySupportDecision } from '@/api/telegramRecoverySupport'

/** Task-owned review controller. All late responses are invalidated by identity, permission and request epochs.
 * @param task Current parent metadata. @returns Bounded independent readers and a non-replaying audited writer.
 */
export function useRecoverySupportReview(task: Ref<RecoveryTask>) {
  const auth = useAdminAuthStore()
  /** Match the backend's persisted exact grant, not a wildcard. @param permission Method/template key. @returns Whether granted. */
  const exact = (permission: string) => auth.isSuper || auth.permissions.includes(permission)
  const canEntries = computed(() => exact(supportEntriesPermission)), canOperations = computed(() => exact(supportOperationsPermission))
  const canResolve = computed(() => exact(supportResolutionPermission) && exact(supportResumePermission))
  const entries = ref<RecoverySupportEntry[]>([]), operations = ref<RecoverySupportOperation[]>([])
  const entry = ref<RecoverySupportEntry>(), operation = ref<RecoverySupportOperation>()
  const entryPage = ref(0), operationPage = ref(0), entryNext = ref('0'), operationNext = ref('0')
  const entryCursors = ['0'], operationCursors = ['0'], entryLoading = ref(false), operationLoading = ref(false), saving = ref(false)
  const entryError = ref(''), operationError = ref(''), error = ref(''), notice = ref('')
  const taskVersion = ref(''), operationsVersion = ref(''), blocked = ref(false), reviewOpen = ref(false), needsEntryRefresh = ref(false)
  const form = reactive({ decision: '' as RecoverySupportDecision | '', evidence: '', threadID: '', acknowledged: false })
  let entryRequest = 0, operationRequest = 0, writeRequest = 0
  const recordedReceipt = computed(() => !!entry.value && !!operation.value && supportRecordedReceipt(entry.value, operation.value))
  const decisions = computed(() => entry.value && operation.value ? supportDecisions(entry.value, operation.value) : [])
  const input = computed<RecoverySupportResolution>(() => ({ task_version: taskVersion.value, revision: entry.value?.revision || '', operation_id: operation.value?.id || '',
    operation_version: operation.value?.version || '', decision: form.decision as RecoverySupportDecision, evidence: form.evidence, thread_id: form.decision === 'succeeded' ? form.threadID : '0' }))
  const valid = computed(() => canEntries.value && canOperations.value && canResolve.value && !blocked.value && !needsEntryRefresh.value && !entryLoading.value && !operationLoading.value
    && ['recovering', 'needs_review', 'partial_failed'].includes(task.value.state) && taskVersion.value === task.value.version
    && operationsVersion.value === taskVersion.value && decisions.value.includes(form.decision as RecoverySupportDecision)
    && form.acknowledged && (form.decision === 'succeeded' || form.threadID === '') && validRecoverySupportResolution(input.value))
  /** Invalidate a draft explicitly; a new identity never inherits another conclusion. @param message Notice key. @returns Nothing. */
  function clearDraft(message = '') {
    writeRequest++; saving.value = false; operation.value = undefined; reviewOpen.value = false; blocked.value = false; error.value = ''; notice.value = message
    Object.assign(form, { decision: '', evidence: '', threadID: '', acknowledged: false })
  }
  /** Clear nested operation scope before changing an entry. @returns Nothing. */
  function clearOperations() {
    operationRequest++; operationLoading.value = false; operations.value = []; operationNext.value = '0'; operationsVersion.value = ''
    operationPage.value = 0; operationCursors.splice(0, operationCursors.length, '0'); operationError.value = ''; clearDraft('draftInvalidated')
  }
  /** Reset every descendant on parent/auth changes or unmount. @returns Nothing. */
  function reset() {
    entryRequest++; entryLoading.value = false; entries.value = []; entry.value = undefined; taskVersion.value = ''; entryError.value = ''
    entryPage.value = 0; entryNext.value = '0'; entryCursors.splice(0, entryCursors.length, '0'); clearOperations()
  }
  /** Fetch a bounded entry page; refreshing explicitly invalidates the previous draft. @param page Known cursor page. @returns Completion promise. */
  async function loadEntries(page = 0) {
    if (saving.value || !canEntries.value || !supportID(task.value.id) || !entryCursors[page]) return
    const current = ++entryRequest, id = task.value.id, after = entryCursors[page]!
    entry.value = undefined; entries.value = []; taskVersion.value = ''; clearOperations(); entryLoading.value = true; entryError.value = ''; entryNext.value = '0'
    try {
      const response = await api.entries(id, after)
      if (current !== entryRequest || !canEntries.value) return
      const data: unknown = response.data.data
      if (!supportPage(data, after, supportEntry)) { entryError.value = 'invalidResponse'; return }
      entries.value = data.items; needsEntryRefresh.value = false; taskVersion.value = data.task_version; entryPage.value = page; entryNext.value = data.next_after_id
      entryCursors.splice(page + 1, entryCursors.length, data.next_after_id === '0' ? '' : data.next_after_id)
      if (data.task_version !== task.value.version) entryError.value = 'parentChanged'
    } catch (failure) { if (current === entryRequest) entryError.value = failureKey(failure) }
    finally { if (current === entryRequest) entryLoading.value = false }
  }
  /** Select only a current-page owned entry. @param row Entry projection. @returns Completion promise. */
  async function selectEntry(row: RecoverySupportEntry) {
    if (saving.value || !entries.value.includes(row) || !canOperations.value) return
    clearOperations(); entry.value = row; await loadOperations()
  }
  /** Fetch a bounded operation page under the selected task and entry. @param page Known cursor page. @returns Completion promise. */
  async function loadOperations(page = 0) {
    if (saving.value || !entry.value || !canEntries.value || !canOperations.value || !operationCursors[page]) return
    const current = ++operationRequest, id = task.value.id, entryID = entry.value.id, after = operationCursors[page]!
    clearDraft('draftInvalidated'); operations.value = []; operationsVersion.value = ''; operationNext.value = '0'; operationLoading.value = true; operationError.value = ''
    try {
      const response = await api.operations(id, entryID, after)
      if (current !== operationRequest || !canOperations.value) return
      const data: unknown = response.data.data
      if (!supportPage(data, after, supportOperation)) { operationError.value = 'invalidResponse'; return }
      operations.value = data.items; operationsVersion.value = data.task_version; operationPage.value = page; operationNext.value = data.next_after_id
      operationCursors.splice(page + 1, operationCursors.length, data.next_after_id === '0' ? '' : data.next_after_id)
      if (data.task_version !== taskVersion.value) operationError.value = 'parentChanged'
    } catch (failure) { if (current === operationRequest) operationError.value = failureKey(failure) }
    finally { if (current === operationRequest) operationLoading.value = false }
  }
  /** Open only a server-listed operation; reopening the same blocked draft cannot replay it. @param row Owned operation. @returns Nothing. */
  function review(row: RecoverySupportOperation) {
    if (saving.value || !operations.value.includes(row) || !canResolve.value) return
    if (operation.value?.id !== row.id) { clearDraft(); operation.value = row }
    if (needsEntryRefresh.value) notice.value = 'refreshRequired'
    reviewOpen.value = true
  }
  /** Save once. Any error preserves the draft but locks CAS until explicit metadata refresh. @returns Completion promise. */
  async function resolve() {
    if (!valid.value || saving.value || !entry.value) return
    const current = ++writeRequest, id = task.value.id, entryID = entry.value.id
    saving.value = true; error.value = ''; notice.value = ''; blocked.value = true; needsEntryRefresh.value = true
    try {
      const response = await api.resolve(id, entryID, { ...input.value })
      if (current !== writeRequest) return
      if (response.data.data?.resolved !== true) { error.value = 'uncertain'; return }
      notice.value = recordedReceipt.value && form.decision !== 'retry' ? 'receiptSaved' : 'saved'
    } catch (failure) { if (current === writeRequest) error.value = failureKey(failure, true) }
    finally { if (current === writeRequest) saving.value = false }
  }
  watch(() => [task.value.id, task.value.version, task.value.state, auth.token, auth.isSuper, ...auth.permissions], reset, { flush: 'sync' })
  watch(() => form.decision, () => { form.threadID = ''; form.acknowledged = false }, { flush: 'sync' })
  onUnmounted(reset)
  return { canEntries, canOperations, canResolve, entries, operations, entry, operation, entryPage, operationPage, entryNext, operationNext, entryLoading, operationLoading,
    saving, entryError, operationError, error, notice, blocked, reviewOpen, form, decisions, recordedReceipt, valid, loadEntries, selectEntry, loadOperations, review, resolve }
}

/** Map transport failures without leaking response details. @param failure Transport error. @param write Whether outcome may be ambiguous. @returns Localized message key. */
function failureKey(failure: unknown, write = false): string {
  const status = (failure as { response?: { status?: number } })?.response?.status
  return status === 403 ? 'forbiddenDraft' : status === 409 ? 'conflict' : write ? 'uncertain' : 'loadFailed'
}
