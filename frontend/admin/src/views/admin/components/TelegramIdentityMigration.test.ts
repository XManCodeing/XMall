import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Component from './TelegramIdentityMigration.vue'
import i18n from '@/i18n'
import { telegramRecoveryMessages } from '@/i18n/telegramRecovery'
import { validIdentityID, isIdentityDetail } from '@/api/telegramIdentity'
const mocks = vi.hoisted(() => ({ preflight: vi.fn(), get: vi.fn(), confirm: vi.fn(), resume: vi.fn(), cancel: vi.fn(), auth: null as any }))
vi.mock('@/api/telegramRecovery', async original => ({ ...await original<typeof import('@/api/telegramRecovery')>(), telegramRecoveryAPI: { cancel: mocks.cancel } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/api/telegramIdentity', async original => ({ ...await original<typeof import('@/api/telegramIdentity')>(), telegramIdentityAPI: mocks }))
const task = { id: '9007199254740993', version: '4', kind: 'identity_transfer', state: 'awaiting_confirmation', confirmed_actor_id: '0', preflight_ref: 'reference', expires_at: '2099-01-01T00:00:00Z', verification_note: 'verified', reason: 'review' }
const detail = { task, execution_available: true, snapshot: { old: { account_id: '7', telegram_id: '9007199254740994' }, new: { account_id: '7', telegram_id: '9223372036854775807' }, expected_old: { binding: '5' }, identity_targets: [{ target_id: '9', bot_id: '2', chat_id: '-1009', entitlement_version: '3', group_version: '4', route_version: '5' }] } }
let app: App, root: HTMLDivElement
/** Drain pending Vue and HTTP microtasks. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount themed real UI with controlled HTTP responses. @param permission Exact grant predicate. @param locale Language. @returns Reactive setup. */
function mount(permission = (_value: string) => true, locale: keyof typeof telegramRecoveryMessages = 'zh-CN', existingTaskID?: string) {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: permission })
  mocks.preflight.mockResolvedValue({ data: { data: task } }); mocks.get.mockResolvedValue({ data: { data: detail } })
  mocks.confirm.mockResolvedValue({ data: { data: { ...task, state: 'fencing' } } })
  mocks.resume.mockResolvedValue({ data: { data: { ...task, state: 'recovering' } } })
  root = document.createElement('div'); document.body.append(root); app = createApp(Component, { existingTaskID })
  app.use(createI18n({ legacy: false, locale, fallbackLocale: false, messages: { [locale]: i18n.global.getLocaleMessage(locale) } }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
/** Populate a reviewed string-safe draft. @param state Mounted component state. @returns Nothing. */
function fill(state: any) { Object.assign(state.draft, { account: '7', oldID: '9007199254740994', newID: '9223372036854775807', targets: '9', note: 'verified', reason: 'review', verified: true }); state.show() }
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.resetAllMocks() })
describe('manual identity migration', () => {
  it('uses compact themed snapshot and mapping tables without changing the reviewed draft', async () => {
    const state = mount(); fill(state)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, identity_mappings: [{ id: '1', target_id: '9', state: 'awaiting_member' }] } } })
    await state.preview(); await flush()
    const tables = [...document.querySelectorAll('[role="dialog"] table')]
    expect(tables).toHaveLength(2)
    for (const table of tables) {
      expect(table.closest('.rounded-xl')?.className).toContain('border border-border bg-card overflow-x-auto')
      expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
      expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
      expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
      for (const cell of table.querySelectorAll('th, td')) expect(cell.className).toContain('px-4 py-3')
    }
    document.documentElement.classList.add('dark'); await flush()
    expect(state.draft.newID).toBe('9223372036854775807'); expect(state.draft.note).toBe('verified')
    expect(mocks.confirm).not.toHaveBeenCalled(); expect(mocks.resume).not.toHaveBeenCalled()
  })
  it('preserves decimal precision and rejects malformed numeric values', () => {
    expect(validIdentityID('9223372036854775807')).toBe(true)
    for (const value of ['0', '01', '1e3', ' 7', '-7', '9223372036854775808', 7]) expect(validIdentityID(value)).toBe(false)
    expect(isIdentityDetail(detail)).toBe(true)
    expect(isIdentityDetail({ ...detail, task: { ...task, id: 9007199254740993 } })).toBe(false)
    expect(isIdentityDetail({ ...detail, task: { ...task, confirmed_actor_id: 'invalid' } })).toBe(false)
  })
  it('requires preview then explicit second acknowledgement and keeps the draft across theme changes', async () => {
    const state = mount(); fill(state); document.documentElement.classList.add('dark'); await flush()
    expect(state.draft.newID).toBe('9223372036854775807'); expect(state.valid).toBe(true)
    await state.preview(); expect(mocks.preflight).toHaveBeenCalledWith(expect.objectContaining({ account_id: '7', target_ids: ['9'], new_telegram_id: '9223372036854775807', human_verified: true }))
    expect(mocks.confirm).not.toHaveBeenCalled(); state.askConfirm(); await state.confirm(); expect(mocks.confirm).not.toHaveBeenCalled()
    state.acknowledged = true; await state.confirm(); expect(mocks.confirm).toHaveBeenCalledTimes(1); expect(state.accepted).toBe(true)
    expect(mocks.resume).not.toHaveBeenCalled(); expect(document.body.textContent).not.toContain('映射已完成')
  })
  it('fails closed for missing capability, permission, expired preview and changed account', async () => {
    const state = mount(value => !value.endsWith('/identity-confirm')); fill(state); await state.preview(); state.askConfirm(); expect(state.confirmOpen).toBe(false)
    mocks.auth.hasPermission = () => true; mocks.get.mockResolvedValue({ data: { data: { ...detail, execution_available: false } } }); await state.refresh(); expect(state.canConfirm).toBe(false)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, expires_at: '2000-01-01' } } } }); await state.refresh(); state.askConfirm(); state.acknowledged = true; await state.confirm(); expect(mocks.confirm).not.toHaveBeenCalled()
    mocks.auth.token = 'second'; await flush(); expect(state.taskID).toBe(''); expect(state.draft.note).toBe(''); expect(state.open).toBe(false)
  })
  it('isolates late preview responses and permanently blocks unknown mutations in the current attempt', async () => {
    const state = mount(); fill(state); let resolve!: (value: any) => void
    mocks.preflight.mockImplementation(() => new Promise(done => { resolve = done }))
    const pending = state.preview(); mocks.auth.token = 'second'; resolve({ data: { data: task } }); await pending; expect(state.taskID).toBe('')
    fill(state); mocks.preflight.mockRejectedValue(new Error('sensitive-token')); await state.preview(); await state.preview()
    expect(state.uncertain).toBe(true); expect(mocks.preflight).toHaveBeenCalledTimes(2); expect(document.body.textContent).not.toContain('sensitive-token')
  })
  it('unlocks unknown outcomes only after a valid same-task read without resubmitting', async () => {
    const state = mount(); fill(state); await state.preview(); state.askConfirm(); state.acknowledged = true
    mocks.confirm.mockRejectedValue(new Error('timeout')); await state.confirm()
    expect(state.uncertain).toBe(true); expect(state.canConfirm).toBe(false)
    await state.refresh()
    expect(state.uncertain).toBe(false); expect(state.canConfirm).toBe(true); expect(state.acknowledged).toBe(false)
    expect(mocks.confirm).toHaveBeenCalledTimes(1); expect(mocks.resume).not.toHaveBeenCalled()
  })
  it('uses a freshly read version for explicit resume after an unknown confirmation', async () => {
    const state = mount(); fill(state); await state.preview(); state.askConfirm(); state.acknowledged = true
    mocks.confirm.mockRejectedValue(new Error('timeout')); await state.confirm()
    const active = { ...task, version: '9007199254740995', state: 'fencing', confirmed_actor_id: '2' }
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: active } } }); await state.refresh()
    expect(state.uncertain).toBe(false); expect(state.canConfirm).toBe(false); expect(state.canResume).toBe(true)
    expect(mocks.resume).not.toHaveBeenCalled(); state.askConfirm('resume'); await state.confirm(); expect(mocks.resume).not.toHaveBeenCalled()
    state.acknowledged = true; await state.confirm(); expect(mocks.resume).toHaveBeenCalledExactlyOnceWith(active)
    expect(mocks.get).toHaveBeenCalledTimes(2); expect(mocks.confirm).toHaveBeenCalledTimes(1)
  })
  it.each(['wrong-id', 'malformed', 'failed', 'needs_review', 'partial_failed'])('keeps execution protected for %s refresh', async outcome => {
    const state = mount(); fill(state); await state.preview(); state.askConfirm(); state.acknowledged = true
    mocks.confirm.mockRejectedValue(new Error('timeout')); await state.confirm()
    if (outcome === 'failed') mocks.get.mockRejectedValue(new Error('offline'))
    else mocks.get.mockResolvedValue({ data: { data: outcome === 'malformed' ? {} : { ...detail, task: { ...task, id: outcome === 'wrong-id' ? '3' : task.id, state: outcome } } } })
    await state.refresh(); state.askConfirm(); await state.confirm()
    expect(state.canConfirm).toBe(false); expect(state.canResume).toBe(false); expect(state.canCancel).toBe(false)
    expect(mocks.confirm).toHaveBeenCalledTimes(1)
  })
  it.each([403, 409])('distinguishes a definite preflight rejection %s from uncertainty', async status => {
    const state = mount(); fill(state); mocks.preflight.mockRejectedValue({ response: { status } }); await state.preview()
    expect(state.uncertain).toBe(false); expect(state.taskID).toBe(''); expect(state.draft.note).toBe('verified')
  })
  it.each(['awaiting_confirmation', 'fencing'])('offers backend-decided cancellation for %s with exact version and second acknowledgement', async status => {
    const state = mount(); fill(state)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: status, confirmed_actor_id: status === 'fencing' ? '2' : '0' }, identity_mappings: [{ id: '1', target_id: '9', state: 'awaiting_member' }] } } })
    await state.preview(); expect(state.canCancel).toBe(true)
    state.askConfirm('cancel'); await state.confirm(); expect(mocks.cancel).not.toHaveBeenCalled()
    mocks.cancel.mockResolvedValue({ data: { data: { ...task, state: 'cancelled' } } }); state.acknowledged = true; await state.confirm()
    expect(mocks.cancel).toHaveBeenCalledExactlyOnceWith(task.id, task.version)
    expect(state.canStartNext).toBe(false); expect(mocks.get).toHaveBeenCalledTimes(1)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: 'cancelled' } } } }); await state.refresh()
    expect(state.canStartNext).toBe(true); state.startNext()
    expect(state.taskID).toBe(''); expect(state.draft.verified).toBe(false); expect(state.acknowledged).toBe(false)
  })
  it.each([403, 409, 'timeout'])('blocks cancellation replay after %s until manual read', async status => {
    const state = mount(); fill(state); await state.preview(); state.askConfirm('cancel'); state.acknowledged = true
    mocks.cancel.mockRejectedValue(status === 'timeout' ? new Error('timeout') : { response: { status } }); await state.confirm(); await state.confirm()
    expect(mocks.cancel).toHaveBeenCalledTimes(1); expect(state.uncertain).toBe(status === 'timeout'); expect(state.canCancel).toBe(false)
    expect(mocks.get).toHaveBeenCalledTimes(1); await state.refresh(); expect(state.canCancel).toBe(true)
  })
  it('rejects a cancellation response for another task and isolates late cancellation after account change', async () => {
    const state = mount(); fill(state); await state.preview(); state.askConfirm('cancel'); state.acknowledged = true
    mocks.cancel.mockResolvedValue({ data: { data: { ...task, id: '8', state: 'cancelled' } } }); await state.confirm()
    expect(state.accepted).toBe(false); expect(state.uncertain).toBe(true); expect(state.canStartNext).toBe(false)
    await state.refresh(); state.askConfirm('cancel'); state.acknowledged = true
    let resolve!: (value: unknown) => void
    mocks.cancel.mockImplementationOnce(() => new Promise(done => { resolve = done })); const pending = state.confirm()
    mocks.auth.token = 'second'; fill(state); resolve({ data: { data: { ...task, state: 'cancelled' } } }); await pending
    expect(state.taskID).toBe(''); expect(state.accepted).toBe(false); expect(state.uncertain).toBe(false); expect(state.draft.verified).toBe(true)
  })
  it('requires cancel permission and blocks noncandidate states regardless of empty mappings', async () => {
    const state = mount(value => !value.endsWith('/cancel')); fill(state); await state.preview()
    state.askConfirm('cancel'); expect(state.confirmOpen).toBe(false); expect(state.canCancel).toBe(false)
    mocks.auth.hasPermission = () => true
    for (const status of ['recovering', 'needs_review', 'partial_failed', 'completed', 'cancelled']) {
      mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: status }, identity_mappings: [] } } }); await state.refresh()
      expect(state.canCancel).toBe(false)
    }
  })
  it('starts a blank next task only after a terminal GET and isolates a late read', async () => {
    const state = mount(); fill(state); await state.preview()
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: 'completed' } } } }); await state.refresh()
    expect(state.canStartNext).toBe(true); state.startNext(); expect(state.draft.account).toBe(''); expect(state.draft.verified).toBe(false)
    fill(state); await state.preview(); let resolve!: (value: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(done => { resolve = done })); const pending = state.refresh()
    mocks.auth.token = 'second'; fill(state); resolve({ data: { data: detail } }); await pending
    expect(state.taskID).toBe(''); expect(state.detail).toBeUndefined(); expect(state.draft.verified).toBe(true)
  })
  it('never turns existing task inspection into creation', async () => {
    const state = mount(undefined, 'zh-CN', task.id)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: 'completed' } } } }); await state.inspect(); await flush()
    state.startNext(); expect(state.taskID).toBe(task.id); expect(state.canStartNext).toBe(false)
    expect(document.body.textContent).not.toContain(telegramRecoveryMessages['zh-CN'].identity.startNext)
  })
  it('abandons only an ID-less unknown draft explicitly without claiming server cancellation', async () => {
    const state = mount(); fill(state); mocks.preflight.mockRejectedValue(new Error('timeout')); await state.preview(); await state.preview()
    expect(state.canAbandon).toBe(true); state.abandon(); expect(state.taskID).toBe(''); expect(state.uncertain).toBe(false)
    expect(state.draft.verified).toBe(false); expect(state.draft.account).toBe(''); expect(mocks.preflight).toHaveBeenCalledTimes(1)
    expect(mocks.cancel).not.toHaveBeenCalled(); expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it.each([403, 409])('preserves evidence but blocks retries after HTTP %s', async status => {
    const state = mount(); fill(state); await state.preview(); state.askConfirm(); state.acknowledged = true
    mocks.confirm.mockRejectedValue({ response: { status }, message: 'sensitive-payload' }); await state.confirm(); await flush()
    expect(state.error).toBe(telegramRecoveryMessages['zh-CN'].identity[status === 403 ? 'forbidden' : 'conflict'])
    expect(state.draft.note).toBe('verified'); expect(state.draft.targets).toBe('9'); expect(state.acknowledged).toBe(false)
    state.open = false; state.show(); expect(state.draft.newID).toBe('9223372036854775807')
    await state.confirm(); expect(mocks.confirm).toHaveBeenCalledOnce(); expect(document.body.textContent).not.toContain('sensitive-payload')
  })
  it('invalidates scope verification when account or identity changes', async () => {
    const state = mount(); fill(state); state.draft.oldID = '44'
    expect(state.draft.targets).toBe(''); expect(state.draft.verified).toBe(false)
    state.draft.account = '8'; expect(state.draft.oldID).toBe(''); expect(state.draft.newID).toBe('')
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('localizes persisted phases and action-specific second confirmations in %s', async locale => {
    const state = mount(undefined, locale); fill(state); await state.preview(); await flush()
    expect(document.body.textContent).toContain(telegramRecoveryMessages[locale].identity.phaseUnsubmitted)
    state.askConfirm('cancel'); await flush()
    expect([...document.querySelectorAll('[role="dialog"] h2')].some(title => title.textContent === telegramRecoveryMessages[locale].cancelTitle)).toBe(true)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: 'recovering', confirmed_actor_id: '2' } } } })
    await state.refresh(); state.askConfirm('resume'); await flush()
    expect(document.body.textContent).toContain(telegramRecoveryMessages[locale].identity.phaseSubmitted)
    expect([...document.querySelectorAll('[role="dialog"] h2')].some(title => title.textContent === telegramRecoveryMessages[locale].resumeTitle)).toBe(true)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: 'completed' } } } }); await state.refresh(); await flush()
    expect(document.body.textContent).toContain(telegramRecoveryMessages[locale].identity.phaseCompleted)
    expect(document.body.textContent).toContain(telegramRecoveryMessages[locale].identity.startNext)
    expect(mocks.confirm).not.toHaveBeenCalled(); expect(mocks.resume).not.toHaveBeenCalled(); expect(mocks.cancel).not.toHaveBeenCalled()
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('separates mapping progress from task state in %s', async locale => {
    const state = mount(undefined, locale); fill(state)
    mocks.get.mockResolvedValue({ data: { data: { ...detail, task: { ...task, state: 'recovering', confirmed_actor_id: '2' }, identity_mappings: ['awaiting_member', 'cleanup_pending', 'completed'].map((value, index) => ({ id: String(index + 1), target_id: '9', state: value })) } } })
    await state.preview(); await flush()
    for (const key of ['awaiting_member', 'cleanup_pending', 'completed'] as const) expect(document.body.textContent).toContain(telegramRecoveryMessages[locale].identity[key])
    expect(mocks.resume).not.toHaveBeenCalled(); state.askConfirm('resume'); state.acknowledged = true; await state.confirm(); expect(mocks.resume).toHaveBeenCalledTimes(1)
  })
})
