import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, ref, type App } from 'vue'
import { useRecoverySupportReview } from './useRecoverySupportReview'
import { supportEntriesPermission, supportOperationsPermission, supportResolutionPermission, supportResumePermission, type RecoverySupportEntry, type RecoverySupportOperation } from '@/api/telegramRecoverySupport'
import type { RecoveryTask } from '@/api/telegramRecovery'
const mocks = vi.hoisted(() => ({ entries: vi.fn(), operations: vi.fn(), resolve: vi.fn(), auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/api/telegramRecoverySupport', async original => ({ ...await original<typeof import('@/api/telegramRecoverySupport')>(), telegramRecoverySupportAPI: mocks }))
const entry: RecoverySupportEntry = { id: '9007199254740993', target_id: '2', revision: '4', state: 'unknown', legacy: '1', operation_id: '9007199254740995' }
const operation: RecoverySupportOperation = { id: entry.operation_id, version: '9007199254740997', state: 'unknown', retired: false, target_id: '2', route_version: '3', chat_id: '-1002', action: 'create' }
const permissions = [supportEntriesPermission, supportOperationsPermission, supportResolutionPermission, supportResumePermission]
let app: App, root: HTMLDivElement
/** Build a safe page response. @param items Projections. @param next Cursor. @returns HTTP-shaped result. */
function page(items: unknown[], next = '0') { return { data: { data: { task_version: '8', items, next_after_id: next } } } }
/** Mount lifecycle-aware controller. @param grants Exact grants. @returns Controller and parent ref. */
function setup(grants = permissions) {
  mocks.auth = reactive({ token: 'session1', isSuper: false, permissions: [...grants], hasPermission: () => true })
  mocks.entries.mockResolvedValue(page([entry])); mocks.operations.mockResolvedValue(page([operation])); mocks.resolve.mockResolvedValue({ data: { data: { resolved: true } } })
  const task = ref({ id: '9', version: '8', kind: 'group_replace', state: 'needs_review' } as RecoveryTask)
  let state!: ReturnType<typeof useRecoverySupportReview>
  root = document.createElement('div'); app = createApp({ setup() { state = useRecoverySupportReview(task); return () => null } }); app.mount(root)
  return { state, task }
}
/** Read both scopes and prepare explicit no-effect evidence. @param state Controller. @returns Completion promise. */
async function draft(state: ReturnType<typeof useRecoverySupportReview>) {
  await state.loadEntries(); await state.selectEntry(state.entries.value[0]!); state.review(state.operations.value[0]!)
  state.form.decision = 'failed'; state.form.evidence = 'case:verified'; state.form.acknowledged = true
}
/** Flush reactive watchers. @returns Completion promise. */
async function flush() { await Promise.resolve(); await nextTick(); await Promise.resolve() }
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('support recovery review isolation', () => {
  it.each(['succeeded', 'failed'] as const)('submits exact recorded %s receipt without route fields or business-state mutation', async result => {
    const { state } = setup()
    const recordedEntry = { ...entry, state: result === 'succeeded' ? 'restored' : 'failed' }, recordedOperation = { ...operation, state: result }
    mocks.entries.mockResolvedValue(page([recordedEntry])); mocks.operations.mockResolvedValue(page([recordedOperation]))
    await draft(state); state.form.decision = result; state.form.acknowledged = true
    expect(state.recordedReceipt.value).toBe(true)
    if (result === 'succeeded') {
      expect(state.valid.value).toBe(false); await state.resolve(); expect(mocks.resolve).not.toHaveBeenCalled()
      state.form.threadID = '9007199254740999'
    }
    expect(state.valid.value).toBe(true); await state.resolve(); await state.resolve()
    expect(mocks.resolve).toHaveBeenCalledTimes(1)
    expect(mocks.resolve).toHaveBeenCalledWith('9', entry.id, { task_version: '8', revision: '4', operation_id: operation.id, operation_version: operation.version,
      decision: result, evidence: 'case:verified', thread_id: result === 'succeeded' ? '9007199254740999' : '0' })
    expect(state.notice.value).toBe('receiptSaved'); expect(state.entry.value?.state).toBe(recordedEntry.state); expect(state.operation.value?.state).toBe(result)
  })
  it.each([403, 409])('locks recorded-result draft on server binding/receipt rejection %s without replay', async status => {
    const { state } = setup(); mocks.entries.mockResolvedValue(page([{ ...entry, state: 'restored' }])); mocks.operations.mockResolvedValue(page([{ ...operation, state: 'succeeded' }]))
    await draft(state); state.form.decision = 'succeeded'; state.form.threadID = '9007199254740999'; state.form.acknowledged = true
    mocks.resolve.mockRejectedValue({ response: { status } }); await state.resolve()
    state.reviewOpen.value = false; state.review(state.operations.value[0]!); await state.resolve()
    expect(mocks.resolve).toHaveBeenCalledTimes(1); expect(state.form.threadID).toBe('9007199254740999'); expect(state.form.evidence).toBe('case:verified')
    expect(state.valid.value).toBe(false); expect(state.notice.value).not.toBe('receiptSaved')
  })
  it('keeps failure receipt and retry distinct, resetting acknowledgement and forbidding success', async () => {
    const { state } = setup(); mocks.entries.mockResolvedValue(page([{ ...entry, state: 'failed' }])); mocks.operations.mockResolvedValue(page([{ ...operation, state: 'failed' }]))
    await draft(state); expect(state.decisions.value).toEqual(['failed', 'retry'])
    state.form.decision = 'retry'; expect(state.form.acknowledged).toBe(false); expect(state.valid.value).toBe(false)
    state.form.acknowledged = true; expect(state.valid.value).toBe(true)
    state.form.decision = 'succeeded'; state.form.threadID = '9'; state.form.acknowledged = true
    await state.resolve(); expect(mocks.resolve).not.toHaveBeenCalled()
  })
  it('separates exact GET and POST, including resume permission', async () => {
    const { state } = setup([supportEntriesPermission, supportOperationsPermission])
    await draft(state); expect(state.canResolve.value).toBe(false); await state.resolve(); expect(mocks.resolve).not.toHaveBeenCalled()
    mocks.auth.permissions.push(supportResolutionPermission); expect(state.canResolve.value).toBe(false)
    mocks.auth.permissions = ['GET:/admin/*', 'POST:/admin/*']; await state.loadEntries(); expect(mocks.entries).toHaveBeenCalledTimes(1)
  })
  it('sends exact owned CAS once and locks after success without executing recovery', async () => {
    const { state } = setup(); await draft(state); expect(state.valid.value).toBe(true); await state.resolve(); await state.resolve()
    expect(mocks.resolve).toHaveBeenCalledTimes(1)
    expect(mocks.resolve).toHaveBeenCalledWith('9', entry.id, { task_version: '8', revision: '4', operation_id: operation.id, operation_version: operation.version, decision: 'failed', evidence: 'case:verified', thread_id: '0' })
    expect(state.notice.value).toBe('saved'); expect(state.form.evidence).toBe('case:verified'); expect(state.blocked.value).toBe(true)
  })
  it.each([403, 409, 500])('preserves but locks draft on %s, including close/reopen', async status => {
    const { state } = setup(); await draft(state); mocks.resolve.mockRejectedValue({ response: { status } }); await state.resolve()
    state.reviewOpen.value = false; state.review(state.operations.value[0]!); await state.resolve()
    expect(mocks.resolve).toHaveBeenCalledTimes(1); expect(state.form.evidence).toBe('case:verified'); expect(state.valid.value).toBe(false)
    await state.loadOperations(); state.review(state.operations.value[0]!); state.form.decision = 'failed'; state.form.evidence = 'case:again'; state.form.acknowledged = true
    await state.resolve(); expect(mocks.resolve).toHaveBeenCalledTimes(1)
    await state.loadEntries(); expect(state.form.evidence).toBe(''); expect(state.notice.value).toBe('draftInvalidated')
  })
  it('rejects task A → B → A late entry responses and failures', async () => {
    const { state, task } = setup()
    let resolveOld!: (response: unknown) => void
    mocks.entries.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const old = state.loadEntries(); task.value = { ...task.value, id: '10' }; task.value = { ...task.value, id: '9' }
    await state.loadEntries(); resolveOld(page([{ ...entry, id: '99' }])); await old
    expect(state.entries.value[0]?.id).toBe(entry.id); expect(state.entryLoading.value).toBe(false)
  })
  it('rejects entry A → B → A late operation success and failure', async () => {
    const { state } = setup(); mocks.entries.mockResolvedValue(page([entry, { ...entry, id: '9007199254740994' }]))
    await state.loadEntries(); const first = state.entries.value[0]!, second = state.entries.value[1]!
    let rejectOld!: (error: unknown) => void
    mocks.operations.mockImplementationOnce(() => new Promise((_, reject) => { rejectOld = reject }))
    const old = state.selectEntry(first); await state.selectEntry(second); await state.selectEntry(first)
    rejectOld({ response: { status: 403 } }); await old
    expect(state.entry.value?.id).toBe(first.id); expect(state.operationError.value).toBe(''); expect(state.operations.value[0]?.id).toBe(operation.id)
  })
  it('ignores late writes after task switch and invalidates permission changes', async () => {
    const { state, task } = setup(); await draft(state)
    let resolveOld!: (response: unknown) => void
    mocks.resolve.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const old = state.resolve(); task.value = { ...task.value, id: '10' }; resolveOld({ data: { data: { resolved: true } } }); await old
    expect(state.notice.value).toBe('draftInvalidated'); expect(state.operation.value).toBeUndefined(); expect(state.saving.value).toBe(false)
    await state.loadEntries(); mocks.auth.permissions = []; await flush(); expect(state.entries.value).toEqual([]); expect(state.form.evidence).toBe('')
  })
  it('uses bounded cursors without appending and clears descendant scopes', async () => {
    const { state } = setup(); mocks.entries.mockResolvedValueOnce(page([entry], entry.id)); await state.loadEntries()
    await state.selectEntry(state.entries.value[0]!); state.review(state.operations.value[0]!); state.form.evidence = 'case:old'
    mocks.entries.mockResolvedValueOnce(page([{ ...entry, id: '9007199254740999' }]))
    await state.loadEntries(1); expect(mocks.entries).toHaveBeenLastCalledWith('9', entry.id); expect(state.entries.value).toHaveLength(1)
    expect(state.entry.value).toBeUndefined(); expect(state.operations.value).toEqual([]); expect(state.form.evidence).toBe('')
    mocks.operations.mockResolvedValueOnce(page([operation], operation.id)); await state.selectEntry(state.entries.value[0]!)
    mocks.operations.mockResolvedValueOnce(page([{ ...operation, id: '9007199254740999', retired: true }]))
    await state.loadOperations(1); expect(mocks.operations).toHaveBeenLastCalledWith('9', '9007199254740999', operation.id); expect(state.operations.value).toHaveLength(1)
  })
  it('blocks mismatched parent versions, foreign selections and unknown retries', async () => {
    const { state } = setup(); await draft(state); state.form.decision = 'retry'; state.form.acknowledged = true; expect(state.valid.value).toBe(false)
    state.review({ ...operation, id: '33' }); expect(state.operation.value?.id).toBe(operation.id)
    mocks.operations.mockResolvedValue({ data: { data: { task_version: '9', items: [operation], next_after_id: '0' } } })
    await state.loadOperations(); state.review(state.operations.value[0]!); state.form.decision = 'failed'; state.form.evidence = 'case:ok'; state.form.acknowledged = true
    expect(state.valid.value).toBe(false); expect(state.operationError.value).toBe('parentChanged'); await state.resolve(); expect(mocks.resolve).not.toHaveBeenCalled()
  })
})
