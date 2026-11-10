import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Component from './TelegramRecovery.vue'
import { telegramRecoveryMessages } from '@/i18n/telegramRecovery'
const mocks = vi.hoisted(() => ({ listPage: vi.fn(), get: vi.fn(), stepsPage: vi.fn(), preflight: vi.fn(), botPreflight: vi.fn(), bots: vi.fn(), cancel: vi.fn(), confirm: vi.fn(), resume: vi.fn(), groups: vi.fn(), auth: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { getTelegramBotInstances: mocks.bots } }))
vi.mock('@/api/telegramGroups', () => ({ telegramGroupsAPI: { list: mocks.groups } }))
vi.mock('@/api/telegramRecovery', async importOriginal => ({ ...await importOriginal<typeof import('@/api/telegramRecovery')>(), telegramRecoveryAPI: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
const task = { id: '9007199254740993', version: '9007199254740995', actor_id: '1', confirmed_actor_id: '0', kind: 'group_replace', state: 'awaiting_confirmation', reason: 'replace', request_key: 'test', preflight_ref: 'ref', verification_note: '', expires_at: '', created_at: '', updated_at: '' }
const endpoint = { target_id: '1', bot_id: '2', chat_id: '-1009007199254740993', account_id: '0', telegram_id: '0' }
const snapshot = { old: endpoint, new: { ...endpoint, chat_id: '-1009007199254740994' }, expected_old: { config: '3', route: '4' }, expected_new: {}, scope: { target_ids: ['1'], bot_ids: ['2'] } }
/** Match groups/transport/http.Handler.List: pagination belongs inside data.
 * @param items Returned group rows. @param page Server page. @param total Unfiltered server total. @returns HTTP client envelope.
 */
function groupPage(items: unknown[], page = 1, total = items.length) {
  return { data: { status_code: 0, msg: 'success', data: { items, page, page_size: 20, total } } }
}
/** Build a counted recovery response. @param items Rows. @param page Page. @param pageSize Size. @param total Count. @returns API envelope. */
function numberedPage(items: unknown[], page = 1, pageSize = 20, total = items.length) {
  return { data: { data: { items, page, page_size: pageSize, total, total_page: Math.max(1, Math.ceil(total / pageSize)) } } }
}
/** Drain scheduled DOM/API work. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 10; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount real themed UI and mocked service boundary. @param permitted Permission predicate. @param locale UI locale. @returns Setup state for focused actions. */
function mount(permitted = (_permission: string) => true, locale: keyof typeof telegramRecoveryMessages = 'zh-CN') {
  mocks.auth = reactive({ token: 'first', permissions: [], isSuper: false, hasPermission: permitted })
  mocks.listPage.mockImplementation((page = 1, pageSize = 20) => Promise.resolve(numberedPage([task], page, pageSize, 85)))
  mocks.get.mockResolvedValue({ data: { data: { task, snapshot } } })
  mocks.stepsPage.mockResolvedValue(numberedPage([]))
  mocks.bots.mockResolvedValue({ data: { data: { items: [{ id: '9007199254740993', username: 'old' }, { id: '9007199254740994', username: 'new' }] } } })
  mocks.groups.mockResolvedValue(groupPage([{ id: 1, name: 'member group', purpose: 'membership', bot_instance_id: 2, version: 3, route_version: 4, telegram_chat_id: '-1009007199254740993' }]))
  root = document.createElement('div'); document.body.append(root)
  app = createApp(Component)
  app.use(createI18n({ legacy: false, locale, missingWarn: false, fallbackWarn: false, messages: Object.fromEntries(Object.entries(telegramRecoveryMessages).map(([key, value]) => [key, { telegramRecovery: value }])) }))
  return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.resetAllMocks() })

describe('B7 preview-only recovery page', () => {
  it('rejects old cursor responses without blanking page metadata', async () => {
    const state = mount(); await flush()
    mocks.listPage.mockResolvedValueOnce({ data: { data: { items: [], next_after_id: '0' } } })
    await state.load(2)
    expect(state.page).toBe(1); expect(state.pageSize).toBe(20); expect(state.total).toBe(85)
    expect(state.items).toEqual([task]); expect(state.error).toBeTruthy()
    await state.show(task.id); await flush()
    mocks.stepsPage.mockResolvedValueOnce({ data: { data: { items: [], next_after_id: '0' } } })
    await state.loadSteps(2)
    expect(state.stepsPage).toBe(1); expect(state.stepsPageSize).toBe(20)
    expect(state.stepsError).toBeTruthy()
  })
  it('jumps directly across checkpoint pages and retains rows/page/size on failure', async () => {
    const state = mount(); await flush()
    mocks.stepsPage.mockResolvedValueOnce(numberedPage([{ id: '1', state: 'pending' }], 1, 20, 85))
    await state.show(task.id); await flush()
    mocks.stepsPage.mockRejectedValueOnce(new Error('offline'))
    await state.loadSteps(4)
    expect(state.stepsPage).toBe(1); expect(state.steps[0].id).toBe('1')
    mocks.stepsPage.mockResolvedValueOnce(numberedPage([{ id: '61', state: 'completed' }], 4, 20, 85))
    await state.loadSteps(4)
    expect(state.stepsPage).toBe(4); expect(state.stepsTotal).toBe(85)
    expect(mocks.stepsPage).toHaveBeenLastCalledWith(task.id, 4, 20)
    expect(mocks.stepsPage).toHaveBeenCalledTimes(3)
    mocks.stepsPage.mockRejectedValueOnce(new Error('offline')); await state.changeStepsPageSize(50)
    expect(state.stepsPage).toBe(4); expect(state.stepsPageSize).toBe(20)
    mocks.stepsPage.mockResolvedValueOnce(numberedPage([], 1, 50, 85)); await state.changeStepsPageSize(50)
    expect(state.stepsPage).toBe(1); expect(state.stepsPageSize).toBe(50)
    expect(mocks.stepsPage).toHaveBeenLastCalledWith(task.id, 1, 50)
  })
  it('uses product table styling for tasks, endpoint snapshots and steps', async () => {
    const state = mount(); await flush()
    await state.show(task.id); await flush()
    const tables = [...document.querySelectorAll('table')]
    expect(tables).toHaveLength(3)
    for (const table of tables) {
      expect(table.closest('.rounded-xl.border.border-border.bg-card')).not.toBeNull()
      expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
      expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
      table.querySelectorAll('th').forEach(cell => expect(cell.className).toContain('px-6 py-3'))
      table.querySelectorAll('td:not([colspan])').forEach(cell => expect(cell.className).toContain('px-6 py-4'))
      table.querySelectorAll('tbody tr').forEach(row => {
        if (row.querySelector('td:not([colspan])')) expect(row.className).toContain('hover:bg-muted/30')
      })
    }
    const action = root.querySelector('tbody td:last-child')!
    expect(root.querySelector('th:last-child')?.className).toContain('text-right')
    expect(action.className).toContain('text-right')
    expect(action.firstElementChild?.className).toBe('flex flex-wrap items-center justify-end gap-2')
    action.querySelectorAll('button').forEach(button => expect(button.className).toContain('border-input'))
    expect(document.querySelector('td[colspan="4"]')?.className).toContain('px-6 py-8 text-center')
    state.items = []; await flush()
    expect(root.querySelector('td[colspan="6"]')?.className).toContain('px-6 py-8 text-center')
  })
  it('refreshes the first numbered page on every click without adding a reset', async () => {
    const state = mount(); await flush()
    await state.load(4); await flush()
    expect(state.page).toBe(4); expect(mocks.listPage).toHaveBeenLastCalledWith(4, 20)
    const refresh = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === telegramRecoveryMessages['zh-CN'].refresh)!
    refresh.click(); await flush()
    expect(state.page).toBe(1); expect(mocks.listPage).toHaveBeenLastCalledWith(1, 20)
    const count = mocks.listPage.mock.calls.length
    refresh.click(); await flush()
    expect(mocks.listPage).toHaveBeenCalledTimes(count + 1); expect(mocks.listPage).toHaveBeenLastCalledWith(1, 20)
    expect(root.textContent).not.toContain('admin.common.reset')
  })
  it('reads empty and paginated group envelopes without enabling stale selections on failure', async () => {
    const state = mount(); await flush()
    mocks.groups.mockResolvedValueOnce(groupPage([]))
    state.openEditor(); await flush()
    expect(state.groups).toEqual([]); expect(state.groupsTotal).toBe(0); expect(state.groupsError).toBe('')
    expect(state.groupsLoading).toBe(false); expect(state.valid).toBe(false)
    const member = { id: 21, purpose: 'membership', bot_instance_id: 2, version: 3, route_version: 4, telegram_chat_id: '-21' }
    mocks.groups.mockResolvedValueOnce(groupPage([member], 2, 41))
    await state.loadGroups(2)
    expect(mocks.groups).toHaveBeenLastCalledWith({ page: 2, page_size: 20, purpose: 'membership' })
    expect(state.groups).toEqual([member]); expect(state.groupsPage).toBe(2); expect(state.groupsTotal).toBe(41)
    state.targetID = '21'; state.chatID = '-22'; state.reason = 'retain draft'; expect(state.valid).toBe(true)
    mocks.groups.mockRejectedValueOnce({ response: { status: 500 } }); await state.loadGroups(3)
    expect(state.groups).toEqual([]); expect(state.groupsTotal).toBe(0); expect(state.targetID).toBe('')
    expect(state.groupsError).not.toBe(''); expect(state.groupsLoading).toBe(false); expect(state.valid).toBe(false)
    expect(state.chatID).toBe('-22'); expect(state.reason).toBe('retain draft')
    mocks.groups.mockResolvedValueOnce(groupPage([], 3, 41)); await state.loadGroups(3)
    expect(state.groupsPage).toBe(3); expect(state.groupsTotal).toBe(41); expect(state.groupsError).toBe('')
    mocks.auth.token = 'second'; expect(state.groupsPage).toBe(1); expect(state.groupsTotal).toBe(0)
    expect(mocks.preflight).not.toHaveBeenCalled(); expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it('ignores a pending paginated group response after unmount', async () => {
    const state = mount(); await flush()
    let resolve!: (value: unknown) => void
    mocks.groups.mockImplementation(() => new Promise(done => { resolve = done }))
    const pending = state.loadGroups(2); app.unmount()
    resolve(groupPage([{ id: 21, purpose: 'membership' }], 2, 41)); await pending
    expect(state.groups).toEqual([]); expect(state.groupsTotal).toBe(0); expect(state.groupsPage).toBe(1)
    expect(state.groupsLoading).toBe(false)
  })
  it('submits optional membership chat intent only and displays immutable mapped and fallback chats', async () => {
    const state = mount(); await flush(); state.openEditor('bot'); await flush()
    state.sourceBotID = '9007199254740993'; state.destinationBotID = '9007199254740994'; state.reason = 'mapped'
    mocks.groups.mockResolvedValue(groupPage([{ id: '9007199254740997', name: 'member', purpose: 'membership', bot_instance_id: state.sourceBotID, telegram_chat_id: '-1001' }]))
    await state.botRoutes.load(); state.botRoutes.add('9007199254740997'); state.botRoutes.routes.value[0].chat_id = '-9007199254740999'
    mocks.botPreflight.mockRejectedValue(new Error('not saved')); await state.submit()
    expect(mocks.botPreflight.mock.lastCall?.[0].routes).toEqual([{ target_id: '9007199254740997', chat_id: '-9007199254740999' }])
    const key = mocks.botPreflight.mock.lastCall?.[0].request_key
    state.botRoutes.routes.value[0].chat_id = '-9007199254740998'; await state.submit(); expect(mocks.botPreflight.mock.lastCall?.[0].request_key).not.toBe(key)
    state.sourceBotID = '9007199254740994'; expect(state.botRoutes.routes.value).toEqual([])
    mocks.get.mockResolvedValue({ data: { data: { task: { ...task, kind: 'bot_replace' }, snapshot: { ...snapshot, bot_routes: [
      { target_id: '7', chat_id: '-1001', new_chat_id: '-9007199254740999', version: '2', route_version: '3', purpose: 'membership', enabled: true },
      { target_id: '8', chat_id: '-1008', version: '2', route_version: '4', purpose: 'support', enabled: true },
    ] } } } }); await state.show(task.id); await flush()
    expect(document.body.textContent).toContain('-9007199254740999'); expect(document.body.textContent).toContain('-1008')
  })
  it('submits same Bot and source versions; retries reuse key and edits replace key', async () => {
    const state = mount(); await flush(); state.openEditor(); await flush()
    state.targetID = '1'; state.chatID = '-1009007199254740994'; state.reason = 'replace'
    mocks.preflight.mockRejectedValue(new Error('synthetic-sensitive-payload'))
    await state.submit(); const first = mocks.preflight.mock.calls[0]?.[0]
    expect(first).toMatchObject({ target_id: 1, expected_version: 3, expected_route_version: 4, new_bot_instance_id: 2, new_telegram_chat_id: '-1009007199254740994', reason: 'replace' })
    expect(first.request_key).toBeTruthy()
    await state.submit(); expect(mocks.preflight.mock.calls[1]?.[0].request_key).toBe(first.request_key)
    state.reason = 'updated'; await state.submit(); expect(mocks.preflight.mock.calls[2]?.[0].request_key).not.toBe(first.request_key)
    expect(document.body.textContent).not.toContain('synthetic-sensitive-payload')
    document.documentElement.classList.add('dark'); await flush(); expect(state.reason).toBe('updated')
    mocks.preflight.mockResolvedValue({ data: { data: task } }); await state.submit(); await flush()
    expect(state.editorOpen).toBe(false); expect(state.success).toContain('尚未切群')
    expect(document.body.textContent).toContain('-1009007199254740994')
    expect(Array.from(document.querySelectorAll('button')).some(button => button.textContent === '确认执行')).toBe(false); expect(document.body.textContent).not.toContain('一键迁移')
  })
  it('blocks invalid chat IDs, same source, and stale source until refreshed', async () => {
    const state = mount(); await flush(); state.openEditor(); await flush(); state.targetID = '1'; state.reason = 'replace'
    for (const value of ['-0', '123', '-01', '-1e4', '-1009007199254740993']) { state.chatID = value; expect(state.valid).toBe(false); await state.submit() }
    expect(mocks.preflight).not.toHaveBeenCalled()
    state.chatID = '-9'; mocks.preflight.mockRejectedValue({ response: { status: 409 } }); await state.submit()
    expect(state.conflict).toBe(true); state.chatID = '-10'; await state.submit(); expect(mocks.preflight).toHaveBeenCalledTimes(1)
    await state.loadGroups(); expect(state.targetID).toBe(''); expect(state.conflict).toBe(false)
  })
  it('defaults to membership and submits support source versions through the same preflight contract', async () => {
    const state = mount(); await flush(); state.openEditor(); await flush()
    expect(state.groupPurpose).toBe('membership'); expect(mocks.groups).toHaveBeenLastCalledWith({ page: 1, page_size: 20, purpose: 'membership' })
    state.targetID = '1'; state.chatID = '-9'; state.reason = 'support change'
    const support = { id: 12, name: 'support group', purpose: 'support', bot_instance_id: 2, version: 7, route_version: 8, telegram_chat_id: '-10012' }
    mocks.groups.mockResolvedValue(groupPage([support, { ...support, id: 13, purpose: 'membership' }, { ...support, id: 14, purpose: 'other' }]))
    state.changeGroupPurpose('support'); expect(state.targetID).toBe(''); expect(state.selected).toBeUndefined(); expect(state.chatID).toBe('')
    await flush(); expect(mocks.groups).toHaveBeenLastCalledWith({ page: 1, page_size: 20, purpose: 'support' }); expect(state.groups).toEqual([support])
    state.targetID = '13'; state.chatID = '-1009007199254740994'; await state.submit(); expect(mocks.preflight).not.toHaveBeenCalled()
    state.targetID = '12'; mocks.preflight.mockRejectedValue({ response: { status: 409 } }); await state.submit()
    expect(mocks.preflight).toHaveBeenCalledWith({ target_id: 12, expected_version: 7, expected_route_version: 8, new_bot_instance_id: 2,
      new_telegram_chat_id: '-1009007199254740994', request_key: expect.any(String), reason: 'support change' })
    expect(state.conflict).toBe(true); await state.submit(); expect(mocks.preflight).toHaveBeenCalledTimes(1)
    document.documentElement.classList.add('dark'); state.editorOpen = false; state.openEditor(); await flush()
    expect(state.groupPurpose).toBe('support'); expect(state.reason).toBe('support change'); expect(state.chatID).toBe('-1009007199254740994')
    mocks.groups.mockResolvedValue(groupPage([{ ...support, version: 9, route_version: 10 }])); await state.loadGroups()
    expect(state.targetID).toBe(''); expect(state.conflict).toBe(false); state.targetID = '12'
    mocks.preflight.mockResolvedValue({ data: { data: task } }); await state.submit()
    expect(mocks.preflight.mock.lastCall?.[0]).toMatchObject({ expected_version: 9, expected_route_version: 10 })
    expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it.each(['success', 'failure'])('isolates late %s responses across membership-support-membership switches', async result => {
    const state = mount(); await flush()
    const requests: Array<{ resolve: (value: unknown) => void; reject: (cause: unknown) => void }> = []
    mocks.groups.mockImplementation(() => new Promise((resolve, reject) => requests.push({ resolve, reject })))
    state.openEditor(); state.changeGroupPurpose('support'); state.changeGroupPurpose('membership')
    expect(requests).toHaveLength(3); expect(state.groupsLoading).toBe(true); expect(state.groupsPage).toBe(1)
    requests[1]!.resolve(groupPage([{ id: 2, purpose: 'support' }], 1, 22)); await flush()
    expect(state.groups).toEqual([]); expect(state.groupsLoading).toBe(true); expect(state.groupsTotal).toBe(0)
    const member = { id: 1, purpose: 'membership', bot_instance_id: 2, version: 3, route_version: 4, telegram_chat_id: '-1' }
    requests[2]!.resolve(groupPage([member])); await flush(); state.targetID = '1'
    if (result === 'success') requests[0]!.resolve(groupPage([{ ...member, version: 99 }], 1, 99))
    else requests[0]!.reject({ response: { status: 403 } })
    await flush(); expect(state.groups).toEqual([member]); expect(state.selected.version).toBe(3); expect(state.groupsTotal).toBe(1)
    expect(state.groupsError).toBe(''); expect(state.groupsLoading).toBe(false)
    state.changeGroupPurpose('other'); expect(state.groupPurpose).toBe('membership'); expect(requests).toHaveLength(3)
  })
  it.each(['read-only', 'preview-only', 'both'])('retains exact group RBAC for support preview with %s grants', async role => {
    const allowed = role === 'read-only' ? ['GET:/admin/telegram-bot/groups'] : role === 'preview-only' ? ['POST:/admin/telegram-bot/recovery/group-preflight']
      : ['GET:/admin/telegram-bot/groups', 'POST:/admin/telegram-bot/recovery/group-preflight']
    const state = mount(permission => allowed.includes(permission)); await flush(); state.openEditor(); await flush()
    mocks.groups.mockResolvedValue(groupPage([{ id: 12, purpose: 'support', bot_instance_id: 2, version: 7, route_version: 8, telegram_chat_id: '-12' }]))
    state.changeGroupPurpose('support'); await flush(); state.targetID = '12'; state.chatID = '-13'; state.reason = 'support'
    mocks.preflight.mockRejectedValue({ response: { status: 403 } }); await state.submit()
    expect(mocks.preflight).toHaveBeenCalledTimes(role === 'both' ? 1 : 0)
    if (role === 'both') { expect(state.formError).toContain('无权'); expect(state.reason).toBe('support'); expect(state.editorOpen).toBe(true) }
    else expect(mocks.groups).not.toHaveBeenCalled()
    expect(mocks.confirm).not.toHaveBeenCalled(); expect(state.canExecute('confirm')).toBe(false)
  })
  it('prevents purpose switches during support submission and isolates revoked list results', async () => {
    const state = mount(); await flush(); state.openEditor(); await flush()
    mocks.groups.mockResolvedValue(groupPage([{ id: 12, purpose: 'support', bot_instance_id: 2, version: 7, route_version: 8, telegram_chat_id: '-12' }]))
    state.changeGroupPurpose('support'); await flush(); state.targetID = '12'; state.chatID = '-13'; state.reason = 'support'
    let reject!: (cause: unknown) => void
    mocks.preflight.mockImplementation(() => new Promise((_resolve, fail) => { reject = fail })); const pending = state.submit()
    state.changeGroupPurpose('membership'); expect(state.groupPurpose).toBe('support'); expect(state.targetID).toBe('12')
    reject({ response: { status: 409 } }); await pending
    let resolve!: (value: unknown) => void
    mocks.groups.mockImplementation(() => new Promise(done => { resolve = done })); const read = state.loadGroups()
    mocks.auth.permissions = ['revoked']; resolve(groupPage([{ id: 12, purpose: 'support' }])); await read
    expect(state.groups).toEqual([]); expect(state.groupPurpose).toBe('membership'); expect(state.selected).toBeUndefined()
  })
  it('uses exact cancellation version and disallows confirmed or other states', async () => {
    const state = mount(); await flush(); mocks.cancel.mockResolvedValue({ data: { data: { ...task, state: 'cancelled' } } })
    expect(state.canCancel({ ...task, confirmed_actor_id: '2' })).toBe(false)
    expect(state.canCancel({ ...task, state: 'switching' })).toBe(false)
    state.askCancel(task); await state.cancel()
    expect(mocks.cancel).toHaveBeenCalledWith(task.id, task.version); expect(state.success).toContain('已取消')
    state.askCancel(task); mocks.cancel.mockRejectedValue({ response: { status: 409 } }); await state.cancel(); expect(state.cancelError).toContain('重新核验')
  })
  it('uses counted task pages, blocks pending navigation and resets size only on success', async () => {
    const state = mount(); await flush(); await state.load(4)
    expect(mocks.listPage).toHaveBeenCalledTimes(2); expect(state.total).toBe(85); expect(state.totalPage).toBe(5)
    mocks.listPage.mockRejectedValueOnce(new Error('offline')); await state.changePageSize(50)
    expect(state.page).toBe(4); expect(state.pageSize).toBe(20); expect(state.items[0].id).toBe(task.id)
    let resolve!: (value: unknown) => void
    mocks.listPage.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const pending = state.changePageSize(50); await flush()
    const count = mocks.listPage.mock.calls.length
    await state.load(2); await state.changePageSize(100)
    expect(mocks.listPage).toHaveBeenCalledTimes(count)
    const footer = root.querySelector('table')!.closest('.rounded-xl')!.lastElementChild!
    expect(footer.closest('.overflow-x-auto')).toBeNull()
    expect([...footer.querySelectorAll('button')].every(button => button.disabled)).toBe(true)
    resolve(numberedPage([task], 1, 50, 85)); await pending
    expect(state.page).toBe(1); expect(state.pageSize).toBe(50); expect(state.totalPage).toBe(2)
  })
  it('keeps empty pagination visible and ignores late checkpoint responses after detail switches', async () => {
    const state = mount(); await flush()
    mocks.listPage.mockResolvedValueOnce(numberedPage([])); await state.load(); await flush()
    const footer = root.querySelector('table')!.closest('.rounded-xl')!.lastElementChild!
    expect(footer.querySelector('input[type="number"]')).not.toBeNull()
    expect(footer.querySelector('[role="combobox"]')).not.toBeNull()
    expect(footer.closest('.overflow-auto')).toBeNull()
    await state.show(task.id); await flush()
    let resolve!: (value: unknown) => void
    mocks.stepsPage.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const pending = state.loadSteps(3)
    mocks.get.mockResolvedValueOnce({ data: { data: { task: { ...task, id: '42' }, snapshot } } })
    await state.show('42'); await flush()
    resolve(numberedPage([{ id: 'stale' }], 3, 20, 99)); await pending
    expect(state.steps).toEqual([]); expect(state.stepsTotal).toBe(0); expect(state.stepsPage).toBe(1)
    expect(mocks.stepsPage).toHaveBeenLastCalledWith('42', 1, 20)
  })
  it('never calls forbidden APIs and clears data on account changes', async () => {
    const state = mount(permission => permission === 'GET:/admin/telegram-bot/recovery/tasks'); await flush()
    await state.show(task.id); state.openEditor(); state.askCancel(task)
    expect(mocks.get).not.toHaveBeenCalled(); expect(mocks.groups).not.toHaveBeenCalled(); expect(state.cancelPending).toBeUndefined()
    let resolveList!: (value: unknown) => void
    mocks.listPage.mockImplementation(() => new Promise(resolve => { resolveList = resolve })); const pending = state.load()
    mocks.auth.token = 'second'; await flush(); resolveList({ data: { data: { items: [task], next_after_id: '0' } } }); await pending
    expect(state.items).toEqual([]); expect(state.detail).toBeUndefined()
  })
  it('fails closed without server capability and exact POST execution permissions', async () => {
    const state = mount(permission => !permission.endsWith('/confirm')); await flush(); await state.show(task.id)
    expect(state.canExecute('confirm')).toBe(false); state.askExecute('confirm'); expect(state.executionPending).toBeUndefined()
    mocks.get.mockResolvedValue({ data: { data: { task, snapshot, execution_available: true } } }); await state.show(task.id)
    expect(state.canExecute('confirm')).toBe(false)
    mocks.auth.hasPermission = () => true
    expect(state.canExecute('confirm')).toBe(true)
    for (const current of [{ ...task, kind: 'identity_transfer' }, { ...task, state: 'completed' }, { ...task, confirmed_actor_id: '2' }]) {
      mocks.get.mockResolvedValue({ data: { data: { task: current, snapshot, execution_available: true } } }); await state.show(task.id)
      expect(state.canExecute('confirm')).toBe(false)
    }
    mocks.get.mockResolvedValue({ data: { data: { task, snapshot: { ...snapshot, new: { ...snapshot.new, bot_id: '3' } }, execution_available: true } } })
    await state.show(task.id); expect(state.canExecute('confirm')).toBe(false)
    expect(mocks.confirm).not.toHaveBeenCalled(); expect(mocks.resume).not.toHaveBeenCalled()
  })
  it('requires second acknowledgement and exact string CAS then reads persisted progress once', async () => {
    const state = mount(); await flush()
    mocks.get.mockResolvedValue({ data: { data: { task, snapshot, execution_available: true } } }); await state.show(task.id)
    state.askExecute('confirm'); await state.execute(); expect(mocks.confirm).not.toHaveBeenCalled()
    state.executionAcknowledged = true
    mocks.confirm.mockResolvedValue({ data: { data: { ...task, state: 'fencing', confirmed_actor_id: '2' } } })
    const reads = mocks.get.mock.calls.length
    await state.execute(); await flush()
    expect(mocks.confirm).toHaveBeenCalledWith(task.id, task.preflight_ref, task.version)
    expect(mocks.get).toHaveBeenCalledTimes(reads + 1); expect(state.executionPending).toBeUndefined()
    expect(state.executionSuccess).toContain('不代表迁移已完成')
  })
  it('reloads 409 without changing draft or reviewed intent and requires reopening confirmation', async () => {
    const state = mount(); await flush(); state.openEditor(); await flush()
    state.targetID = '1'; state.chatID = '-9'; state.reason = 'preserve draft'
    mocks.get.mockResolvedValue({ data: { data: { task, snapshot, execution_available: true } } }); await state.show(task.id)
    state.askExecute('confirm'); state.executionAcknowledged = true
    mocks.confirm.mockRejectedValue({ response: { status: 409 } })
    mocks.get.mockResolvedValue({ data: { data: { task: { ...task, version: '9007199254740996' }, snapshot, execution_available: true } } })
    await state.execute(); await flush()
    expect(state.executionError).toContain('重新核验'); expect(state.detail.task.version).toBe('9007199254740996')
    expect(state.executionPending.task.version).toBe(task.version); expect(state.executionAcknowledged).toBe(false)
    expect(state.chatID).toBe('-9'); expect(state.reason).toBe('preserve draft'); expect(state.targetID).toBe('1')
    state.executionAcknowledged = true; await state.execute(); expect(mocks.confirm).toHaveBeenCalledTimes(1)
    state.askExecute('confirm'); expect(state.executionPending.task.version).toBe('9007199254740996'); expect(state.executionAcknowledged).toBe(false)
  })
  it('resumes only safe active states, protects unknown outcomes and refreshes without polling', async () => {
    const state = mount(); await flush()
    const active = { ...task, confirmed_actor_id: '2', state: 'recovering' }
    mocks.get.mockResolvedValue({ data: { data: { task: active, snapshot, execution_available: true } } }); await state.show(task.id)
    expect(state.canExecute('resume')).toBe(true); expect(state.canExecute('confirm')).toBe(false)
    state.askExecute('resume'); state.executionAcknowledged = true
    mocks.resume.mockResolvedValue({ data: { data: active } }); await state.execute()
    expect(mocks.resume).toHaveBeenCalledWith(task.id, task.version)
    const reads = mocks.get.mock.calls.length; await state.refreshProgress(); await flush(); expect(mocks.get).toHaveBeenCalledTimes(reads + 1)
    for (const status of ['completed', 'cancelled', 'unknown']) {
      mocks.get.mockResolvedValue({ data: { data: { task: { ...active, state: status }, snapshot, execution_available: true } } })
      await state.show(task.id); expect(state.canExecute('resume')).toBe(false)
    }
    expect(mocks.resume).toHaveBeenCalledTimes(1)
  })
  it.each(['needs_review', 'partial_failed'])('allows explicit reviewed resume only for failed group replacement: %s', async status => {
    const state = mount(); await flush()
    for (const kind of ['group_replace', 'bot_replace', 'identity_transfer']) {
      mocks.get.mockResolvedValue({ data: { data: { task: { ...task, kind, state: status, confirmed_actor_id: '2' }, snapshot, execution_available: true } } })
      await state.show(task.id); expect(state.canExecute('resume')).toBe(kind === 'group_replace')
      if (kind === 'group_replace') {
        await flush(); expect(document.body.textContent).toContain(telegramRecoveryMessages['zh-CN'].reviewAndResume)
        state.askExecute('resume'); expect(mocks.resume).not.toHaveBeenCalled(); state.executionAcknowledged = true
        mocks.resume.mockResolvedValue({ data: { data: task } }); await state.execute()
        expect(mocks.resume).toHaveBeenCalledWith(task.id, task.version); mocks.resume.mockClear()
      }
    }
  })
  it('ignores execution responses after account revocation and clears confirmation state', async () => {
    const state = mount(); await flush()
    mocks.get.mockResolvedValue({ data: { data: { task, snapshot, execution_available: true } } }); await state.show(task.id)
    state.askExecute('confirm'); state.executionAcknowledged = true
    let resolve!: (value: unknown) => void
    mocks.confirm.mockImplementation(() => new Promise(done => { resolve = done })); const pending = state.execute()
    mocks.auth.token = 'second'; await flush(); resolve({ data: { data: task } }); await pending
    expect(state.executionPending).toBeUndefined(); expect(state.detail).toBeUndefined(); expect(state.executionSuccess).toBe('')
  })
  it('uses shared Bot preview controls, preserves drafts and blocks conflict replay', async () => {
    const state = mount(); await flush()
    const button = Array.from(root.querySelectorAll('button')).find(item => item.textContent === telegramRecoveryMessages['zh-CN'].createBot)
    expect(button).toBeTruthy(); button!.click(); await flush()
    expect(mocks.bots).toHaveBeenCalledTimes(1); expect(mocks.groups).not.toHaveBeenCalled()
    state.sourceBotID = '9007199254740993'; state.destinationBotID = state.sourceBotID; state.reason = 'Bot draft'
    await state.submit(); expect(mocks.botPreflight).not.toHaveBeenCalled()
    state.destinationBotID = '9007199254740994'
    mocks.botPreflight.mockRejectedValue({ response: { status: 409 } }); await state.submit()
    expect(mocks.botPreflight).toHaveBeenCalledWith({ source_bot_id: '9007199254740993', destination_bot_id: '9007199254740994', request_key: expect.any(String), reason: 'Bot draft' })
    expect(state.conflict).toBe(true); await state.submit(); expect(mocks.botPreflight).toHaveBeenCalledTimes(1)
    document.documentElement.classList.add('dark'); state.editorOpen = false; state.openEditor('bot'); await flush()
    expect(state.sourceBotID).toBe('9007199254740993'); expect(state.destinationBotID).toBe('9007199254740994'); expect(state.reason).toBe('Bot draft')
    await state.refreshBots(); expect(state.conflict).toBe(false); expect(state.reason).toBe('Bot draft')
    mocks.botPreflight.mockResolvedValue({ data: { data: { ...task, kind: 'bot_replace' } } }); await state.submit()
    expect(state.editorOpen).toBe(false); expect(state.success).toContain('尚未切群'); expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it.each(['auditor', 'list-only', 'preview-only', 'system-admin'])('gates Bot preview by exact permissions for %s', async role => {
    const grants: Record<string, string[]> = {
      auditor: ['GET:/admin/telegram-bot/recovery/tasks'],
      'list-only': ['GET:/admin/telegram-bot/instances'],
      'preview-only': ['POST:/admin/telegram-bot/recovery/bot-preflight'],
      'system-admin': ['GET:/admin/telegram-bot/instances', 'POST:/admin/telegram-bot/recovery/bot-preflight'],
    }
    const state = mount(permission => grants[role]!.includes(permission)); await flush(); state.openEditor('bot'); await flush()
    expect(state.editorOpen).toBe(['preview-only', 'system-admin'].includes(role))
    expect(mocks.bots).toHaveBeenCalledTimes(role === 'system-admin' ? 1 : 0)
    state.sourceBotID = '9007199254740993'; state.destinationBotID = '9007199254740994'; state.reason = 'reason'
    mocks.botPreflight.mockResolvedValue({ data: { data: task } }); await state.submit()
    expect(mocks.botPreflight).toHaveBeenCalledTimes(role === 'system-admin' ? 1 : 0)
    expect(mocks.confirm).not.toHaveBeenCalled()
  })
  it('requires execution capability rather than preparation and handles Bot CAS conflicts without losing drafts', async () => {
    const state = mount(); await flush(); state.openEditor('bot'); await flush()
    state.sourceBotID = '9007199254740993'; state.destinationBotID = '9007199254740994'; state.reason = 'preserve Bot draft'
    const botTask = { ...task, kind: 'bot_replace' }
    const botSnapshot = { ...snapshot, old: { ...endpoint, bot_id: state.sourceBotID }, new: { ...endpoint, bot_id: state.destinationBotID } }
    mocks.get.mockResolvedValue({ data: { data: { task: botTask, snapshot: botSnapshot, preparation_available: true } } }); await state.show(task.id)
    expect(state.canExecute('confirm')).toBe(false)
    mocks.get.mockResolvedValue({ data: { data: { task: botTask, snapshot: botSnapshot, execution_available: true } } }); await state.show(task.id)
    expect(state.canExecute('confirm')).toBe(true)
    state.askExecute('confirm'); await flush()
    expect(document.body.textContent).toContain(telegramRecoveryMessages['zh-CN'].botExecutionHint)
    await state.execute(); expect(mocks.confirm).not.toHaveBeenCalled(); state.executionAcknowledged = true
    mocks.confirm.mockRejectedValue({ response: { status: 409 } })
    mocks.get.mockResolvedValue({ data: { data: { task: { ...botTask, version: '9007199254740996' }, snapshot: botSnapshot, execution_available: true } } })
    await state.execute(); expect(state.executionError).toContain('重新核验'); expect(state.reason).toBe('preserve Bot draft')
    expect(state.sourceBotID).toBe('9007199254740993'); expect(state.destinationBotID).toBe('9007199254740994')
    state.executionAcknowledged = true; await state.execute(); expect(mocks.confirm).toHaveBeenCalledTimes(1)
    expect(mocks.confirm).toHaveBeenCalledWith(task.id, task.preflight_ref, task.version)
    mocks.auth.hasPermission = (permission: string) => !permission.endsWith('/confirm')
    expect(state.canExecute('confirm')).toBe(false)
    mocks.get.mockResolvedValue({ data: { data: { task: { ...botTask, confirmed_actor_id: '2', state: 'recovering' }, snapshot: botSnapshot, execution_available: true } } })
    await state.show(task.id); expect(state.canExecute('resume')).toBe(true)
    state.askExecute('resume'); state.executionAcknowledged = true; mocks.resume.mockResolvedValue({ data: { data: botTask } }); await state.execute()
    expect(mocks.resume).toHaveBeenCalledWith(task.id, task.version)
    mocks.auth.hasPermission = (permission: string) => !permission.endsWith('/resume'); expect(state.canExecute('resume')).toBe(false)
  })
  it('rejects unsafe numeric Bot IDs and ignores list results after account changes', async () => {
    const state = mount(); await flush()
    mocks.bots.mockResolvedValue({ data: { data: { items: [{ id: 9007199254740992, username: 'rounded' }, { id: 1, username: 'safe' }] } } })
    state.openEditor('bot'); await flush(); expect(state.bots).toEqual([{ id: '1', username: 'safe' }])
    let resolve!: (value: unknown) => void
    mocks.bots.mockImplementation(() => new Promise(done => { resolve = done })); const pending = state.refreshBots()
    mocks.auth.token = 'revoked'; resolve({ data: { data: { items: [{ id: '2', username: 'stale' }] } } }); await pending
    expect(state.bots).toEqual([]); expect(state.editorOpen).toBe(false); expect(state.sourceBotID).toBe('')
  })
  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('renders preview-only copy in %s and empty/error states', async locale => {
    const state = mount(undefined, locale); await flush()
    expect(root.querySelector('h2')?.textContent).toBe(telegramRecoveryMessages[locale].title)
    expect(root.querySelector('nav')).toBeNull()
    expect(root.textContent).not.toContain(telegramRecoveryMessages[locale].phase)
    expect(root.textContent).not.toContain(telegramRecoveryMessages[locale].hint)
    state.openEditor(); await flush()
    expect(document.body.textContent).toContain(telegramRecoveryMessages[locale].hint)
    state.editorOpen = false; await flush()
    mocks.listPage.mockResolvedValue(numberedPage([])); await state.load(); await flush()
    expect(root.textContent).toContain(telegramRecoveryMessages[locale].empty)
    mocks.listPage.mockRejectedValue({ response: { status: 403 } }); await state.load(); await flush()
    expect(root.textContent).not.toContain(telegramRecoveryMessages[locale].forbidden)
    expect(root.textContent).not.toContain(telegramRecoveryMessages[locale].empty)
    expect(root.querySelector('[role="alert"]')).toBeNull()
  })
})
