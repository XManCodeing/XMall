import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { groupFresh, groupReady, groupState, validChatID, useTelegramGroups } from './useTelegramGroups'
import { useTelegramGroupModules } from './useTelegramGroupModules'
import type { GroupDetail } from '@/api/telegramGroups'
const mocks = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), create: vi.fn(), previewEnable: vi.fn(), patch: vi.fn(), check: vi.fn(), modules: vi.fn(), saveModule: vi.fn(), success: vi.fn(), info: vi.fn(), denied: [] as string[] }))
vi.mock('@/api/telegramGroups', () => ({ telegramGroupsAPI: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (key: string) => !mocks.denied.includes(key) }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/notify', () => ({ notifySuccess: mocks.success, notifyInfo: mocks.info, notifyErrorOnce: vi.fn() }))
const scopes: ReturnType<typeof effectScope>[] = []
function model() { const scope = effectScope(); scopes.push(scope); return scope.run(useTelegramGroups)! }
function moduleModel() { const scope = effectScope(); scopes.push(scope); return scope.run(useTelegramGroupModules)! }
const now = Date.parse('2026-10-30T10:00:00Z')
const item = {
  id: 1, name: 'Group', purpose: 'membership', bot_instance_id: 2, telegram_chat_id: '-9223372036854775808', enabled: false,
  version: 4, route_version: 3, fresh: true, fresh_until: '2026-10-30T10:05:00Z', missing_capabilities: [], affected_functions: [], checking: false,
  observation: { state: 'ready', route_version: 3, error_class: '' }, routes: [], operations: [],
} as unknown as GroupDetail
beforeEach(() => {
  vi.clearAllMocks(); vi.useFakeTimers(); vi.setSystemTime(now); mocks.denied = []
  mocks.list.mockResolvedValue({ data: { data: { items: [item], total: 1, page: 1 } } })
  mocks.get.mockResolvedValue({ data: { data: item } }); mocks.patch.mockResolvedValue({ data: { data: item } }); mocks.create.mockResolvedValue({ data: { data: item } })
  mocks.check.mockResolvedValue({ data: { data: item } })
  mocks.modules.mockResolvedValue({ data: { data: { items: [
    { bot_instance_id: 2, partition: 'support', version: 7, enabled: false, configured_enabled: true, effective_enabled: false, target_id: 0 },
    { bot_instance_id: 2, partition: 'membership', version: 11, enabled: false, configured_enabled: false, effective_enabled: false, target_id: 1 },
  ] } } })
})
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); vi.useRealTimers() })
describe('group CAS and capability UI', () => {
  it('checks new-group toggles without saving and honors final disabled response', async () => {
    const m = model(); m.openEditor()
    await m.toggleEnabled(true); expect(m.enableNotice.value).toBe('telegramBot.groups.enableIncomplete')
    expect(mocks.previewEnable).not.toHaveBeenCalled()
    m.form.value.name = 'Group'; m.form.value.bot_instance_id = 2; m.form.value.telegram_chat_id = '-100123'
    mocks.previewEnable.mockResolvedValueOnce({ data: { data: { ...item, enabled: true } } })
    await m.toggleEnabled(true)
    expect(m.form.value.enabled).toBe(true); expect(mocks.create).not.toHaveBeenCalled()
    mocks.create.mockResolvedValueOnce({ data: { data: { ...item, enable_rejected: true, missing_capabilities: ['can_invite_users'], version: 1 } } })
    await m.save()
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ enabled: true }))
    expect(m.form.value.enabled).toBe(false); expect(m.editing.value?.id).toBe(1)
    expect(m.editorOpen.value).toBe(true); expect(m.enableNotice.value).toContain('telegramBot.groups.savedDisabled')
  })
  it('keeps toggle off for missing permissions or request failure', async () => {
    const m = model(); m.openEditor(item)
    mocks.previewEnable.mockResolvedValueOnce({ data: { data: { ...item, missing_capabilities: ['can_restrict_members'] } } })
    await m.toggleEnabled(true); expect(m.form.value.enabled).toBe(false)
    expect(m.enableNotice.value).toBe('telegramBot.groups.enableDenied')
    mocks.previewEnable.mockRejectedValueOnce(new Error('network'))
    await m.toggleEnabled(true); expect(m.form.value.enabled).toBe(false)
    expect(m.enableNotice.value).toBe('telegramBot.groups.failed')
  })
  it('invalidates pending checks on route change and prevents repeated requests', async () => {
    const m = model(); m.openEditor(item)
    let resolve!: (value: unknown) => void
    mocks.previewEnable.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const pending = m.toggleEnabled(true); await m.toggleEnabled(true)
    expect(mocks.previewEnable).toHaveBeenCalledTimes(1)
    m.form.value.telegram_chat_id = '-100222'
    resolve({ data: { data: { ...item, enabled: true } } }); await pending
    expect(m.form.value.enabled).toBe(false); expect(m.enableChecking.value).toBe(false)
  })
  it('checks explicit preview permission before making a request', async () => {
    const m = model(); m.openEditor(item)
    mocks.denied = ['POST:/admin/telegram-bot/groups/1/enable-preview']
    await m.toggleEnabled(true); expect(mocks.previewEnable).not.toHaveBeenCalled()
    expect(m.enableNotice.value).toBe('telegramBot.groups.forbidden')
  })
  it('searches page one repeatedly and resets all filters with exactly one request', async () => {
    const list = model()
    list.botFilter.value = '2'; list.purposeFilter.value = 'support'
    expect(mocks.list).not.toHaveBeenCalled()
    await list.load(3)
    await list.search(); await list.search()
    expect(mocks.list).toHaveBeenCalledTimes(3)
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_instance_id: 2, purpose: 'support' })
    list.page.value = 3
    await list.resetFilters()
    expect(list.botFilter.value).toBe(''); expect(list.purposeFilter.value).toBe('all'); expect(list.page.value).toBe(1)
    expect(mocks.list).toHaveBeenCalledTimes(4)
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_instance_id: undefined, purpose: undefined })
    await list.resetFilters(); expect(mocks.list).toHaveBeenCalledTimes(5)
  })
  it.each(['success', 'failure'])('ignores stale group search %s after reset', async outcome => {
    const list = model()
    let finish!: (value: unknown) => void, fail!: (reason: unknown) => void
    mocks.list.mockImplementationOnce(() => new Promise((resolve, reject) => { finish = resolve; fail = reject }))
    list.botFilter.value = '2'
    const pending = list.search()
    await list.resetFilters()
    if (outcome === 'success') finish({ data: { data: { items: [], total: 99, page: 9 } } })
    else fail(new Error('old request failed'))
    await pending
    expect(list.items.value).toHaveLength(1); expect(list.total.value).toBe(1)
    expect(list.page.value).toBe(1); expect(list.error.value).toBe(''); expect(list.loading.value).toBe(false)
  })
  it('retains first-page failure state and rejects invalid bot input without HTTP', async () => {
    const list = model(); list.page.value = 4
    mocks.list.mockRejectedValueOnce(new Error('network'))
    await list.resetFilters()
    expect(list.page.value).toBe(1); expect(list.error.value).toBe('telegramBot.groups.failed')
    expect(list.items.value).toEqual([]); expect(list.total.value).toBe(0); expect(list.loading.value).toBe(false)
    list.botFilter.value = '9007199254740993'; await list.search()
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })
  it('keeps signed int64 chat IDs exact and rejects noncanonical values', () => {
    expect(validChatID('-9223372036854775808')).toBe(true)
    for (const id of ['123', '12453021421', '1001245302142', ' 12453021421 ']) expect(validChatID(id)).toBe(true)
    for (const id of ['-9223372036854775809', '9223372036854775808', '-0', '0', '01', '-01', '-1.2', '-1e3', '', '- 2', '@group', 'https://t.me/group']) expect(validChatID(id)).toBe(false)
  })
  it('expires ready observations using frontend time and rejects mismatched routes', async () => {
    expect(groupReady(item, now)).toBe(true)
    expect(groupState(item, now + 300000)).toBe('stale')
    expect(groupFresh({ ...item, route_version: 5 }, now)).toBe(false)
    expect(groupReady({ ...item, observation: { ...item.observation, error_class: 'network' } }, now)).toBe(false)
    const m = model(); m.openEditor(item); expect(m.canEnable.value).toBe(true)
    await vi.advanceTimersByTimeAsync(300000)
    expect(m.canEnable.value).toBe(false)
  })
  it('creates disabled and patches the complete form with both CAS versions', async () => {
    const m = model(); m.openEditor(); Object.assign(m.form.value, { name: 'New', bot_instance_id: 2, telegram_chat_id: item.telegram_chat_id })
    await m.save(); expect(mocks.create).toHaveBeenCalledWith({ name: 'New', purpose: 'membership', bot_instance_id: 2, telegram_chat_id: item.telegram_chat_id, enabled: false, expected_version: 0, expected_route_version: 0 })
    m.openEditor(item); m.form.value.name = 'Renamed'; await m.save()
    expect(mocks.patch).toHaveBeenCalledWith(1, { name: 'Renamed', purpose: 'membership', bot_instance_id: 2, telegram_chat_id: item.telegram_chat_id, enabled: false, expected_version: 4, expected_route_version: 3 })
  })
  it('requires a separate disable before rerouting, then checking before enable', async () => {
    const m = model(); m.openEditor({ ...item, enabled: true }); m.form.value.enabled = false; m.form.value.telegram_chat_id = '-100'
    expect(m.valid.value).toBe(false); await m.save(); expect(mocks.patch).not.toHaveBeenCalled()
    m.openEditor(item); m.form.value.telegram_chat_id = '-100'; expect(m.valid.value).toBe(true)
    m.form.value.enabled = true; expect(m.valid.value).toBe(false)
  })
  it('submits unsigned IDs unchanged for server resolution and preserves failed drafts', async () => {
    const m = model(); m.openEditor()
    m.form.value.name = 'Group'; m.form.value.bot_instance_id = 2; m.form.value.telegram_chat_id = '12453021421'
    expect(m.valid.value).toBe(true)
    mocks.create.mockRejectedValueOnce({ response: { status: 422, data: { error_class: 'chat_id_unresolved' } } })
    await m.save()
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ telegram_chat_id: '12453021421', enabled: false }))
    expect(m.formError.value).toBe('telegramBot.groups.chatUnresolved')
    expect(m.form.value.telegram_chat_id).toBe('12453021421'); expect(m.editorOpen.value).toBe(true)
    mocks.create.mockRejectedValueOnce({ response: { status: 422, data: { error_class: 'chat_id_ambiguous' } } })
    await m.save(); expect(m.formError.value).toBe('telegramBot.groups.chatAmbiguous')
  })
  it('preserves draft and CAS on 409, blocks repeat submission until explicit reload', async () => {
    const m = model(); m.openEditor(item); m.form.value.name = 'My draft'; mocks.patch.mockRejectedValueOnce({ response: { status: 409 } })
    await m.save(); expect(m.form.value.name).toBe('My draft'); expect(m.form.value.expected_version).toBe(4); expect(m.editorOpen.value).toBe(true); expect(m.conflict.value).toBe(true)
    await m.save(); expect(mocks.patch).toHaveBeenCalledTimes(1)
    await m.reloadEditor(); expect(m.form.value.name).toBe('Group'); expect(m.conflict.value).toBe(false)
  })
  it('does not call HTTP 200 failed observation a success and handles 429', async () => {
    const m = model(); mocks.check.mockResolvedValueOnce({ data: { data: { ...item, fresh: false, observation: { ...item.observation, state: 'unknown', error_class: 'network' } } } })
    await m.check(item); expect(mocks.success).not.toHaveBeenCalled(); expect(mocks.info).toHaveBeenCalledWith('telegramBot.groups.checkObserved')
    expect(mocks.check).toHaveBeenCalledWith(1, 3)
    mocks.check.mockRejectedValueOnce({ response: { status: 429, data: { error_class: 'check_busy' } } })
    await m.check(item); expect(m.error.value).toBe('telegramBot.groups.checkBusy'); expect(m.checkingID.value).toBe(null)
  })
  it('keeps anonymous administrator evidence visible and prevents enabling until a clean recheck', async () => {
    const blocked = { ...item, fresh: false, observation: { ...item.observation, state: 'missing_permissions', error_class: 'anonymous_admin' } }
    const m = model(); mocks.list.mockResolvedValueOnce({ data: { data: { items: [blocked], total: 1, page: 1 } } })
    mocks.check.mockResolvedValueOnce({ data: { data: blocked } })
    await m.check(item)
    expect(mocks.success).not.toHaveBeenCalled()
    expect(groupState(blocked, now)).toBe('missing_permissions')
    expect(groupReady(blocked, now)).toBe(false)
    m.openEditor(blocked); expect(m.canEnable.value).toBe(false)
    expect(m.items.value[0]?.observation.error_class).toBe('anonymous_admin')
    await m.check(blocked)
    expect(mocks.success).toHaveBeenCalled()
    m.openEditor(item); expect(m.canEnable.value).toBe(true)
  })
  it('enforces read, write and check permissions independently at action time', async () => {
    const m = model(); m.openEditor(item)
    mocks.denied = ['GET:/admin/telegram-bot/groups', 'GET:/admin/telegram-bot/groups/1', 'PATCH:/admin/telegram-bot/groups/1', 'POST:/admin/telegram-bot/groups/1/check']
    await m.load(); await m.showDetail(1); await m.save(); await m.check(item)
    expect(mocks.list).not.toHaveBeenCalled(); expect(mocks.get).not.toHaveBeenCalled(); expect(mocks.patch).not.toHaveBeenCalled(); expect(mocks.check).not.toHaveBeenCalled()
    mocks.denied = ['POST:/admin/telegram-bot/groups/1/check']; await m.load(); expect(mocks.list).toHaveBeenCalledTimes(1)
  })
  it('ignores stale page responses', async () => {
    const m = model(); let resolve!: (value: unknown) => void
    mocks.list.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const first = m.load(1); await m.load(2)
    resolve({ data: { data: { items: [], total: 0, page: 1 } } }); await first
    expect(m.items.value).toHaveLength(1)
  })
})
describe('independent module partitions', () => {
  it('saves configured true even while effective capability gating is false', async () => {
    const m = moduleModel(); await m.select('2')
    const row = m.rows.value[0]!
    expect(row.configured_enabled).toBe(true); expect(row.effective_enabled).toBe(false); expect(row.enabled).toBe(false)
    mocks.saveModule.mockResolvedValueOnce({ data: { data: { items: [{ ...row, version: 8 }] } } })
    await m.save(row)
    expect(mocks.saveModule).toHaveBeenCalledExactlyOnceWith(2, { partition: 'support', expected_version: 7, enabled: true, target_id: 0 })
  })
  it.each(['success', 'failure'])('ignores an old instance response after switching: %s', async outcome => {
    const m = moduleModel()
    let resolve!: (value: unknown) => void
    let reject!: (reason: unknown) => void
    mocks.modules.mockImplementationOnce(() => new Promise((yes, no) => { resolve = yes; reject = no }))
    const old = m.select('2')
    const newRows = [{ bot_instance_id: 3, partition: 'support', version: 12, enabled: false, configured_enabled: false, effective_enabled: false, target_id: 0 }]
    mocks.modules.mockResolvedValueOnce({ data: { data: { items: newRows } } })
    await m.select('3')
    if (outcome === 'success') resolve({ data: { data: { items: [{ bot_instance_id: 2, partition: 'membership' }] } } })
    else reject(new Error('old request failed'))
    await old
    expect(m.botID.value).toBe('3'); expect(m.rows.value).toEqual(newRows)
    expect(m.error.value).toBe(''); expect(m.loading.value).toBe(false)
  })
  it('makes zero read or write requests when permissions are absent', async () => {
    mocks.denied = ['GET:/admin/telegram-bot/instances/2/modules', 'PATCH:/admin/telegram-bot/instances/2/modules']
    const m = moduleModel(); await m.select('2'); await m.load()
    expect(m.canRead.value).toBe(false); expect(m.rows.value).toEqual([]); expect(m.loading.value).toBe(false)
    await m.save({ bot_instance_id: 2, partition: 'support', version: 1, configured_enabled: true, effective_enabled: false, enabled: false, target_id: 1, updated_by: 0, updated_at: '' })
    expect(mocks.modules).not.toHaveBeenCalled(); expect(mocks.saveModule).not.toHaveBeenCalled()
  })
  it('recovers from a failed read by explicitly reloading', async () => {
    mocks.modules.mockRejectedValueOnce(new Error('network'))
    const m = moduleModel(); await m.select('2')
    expect(m.error.value).toBe('telegramBot.groups.failed'); expect(m.loading.value).toBe(false); expect(m.rows.value).toEqual([])
    await m.load()
    expect(m.error.value).toBe(''); expect(m.loading.value).toBe(false); expect(m.rows.value).toHaveLength(2)
  })
  it('retains a failed write draft and permits a successful retry without changing CAS', async () => {
    const m = moduleModel(); await m.select('2'); const row = m.rows.value[0]!
    row.target_id = 88
    mocks.saveModule.mockRejectedValueOnce(new Error('network'))
    await m.save(row)
    expect(m.busy.value).toBe(null); expect(m.errors.value.support).toBe('telegramBot.groups.failed')
    expect(m.conflicts.value.support).toBe(false); expect(row.target_id).toBe(88); expect(row.version).toBe(7)
    mocks.saveModule.mockResolvedValueOnce({ data: { data: { items: [{ ...row, version: 8 }] } } })
    await m.save(row)
    expect(mocks.saveModule).toHaveBeenCalledTimes(2)
    expect(mocks.saveModule.mock.calls[1]).toEqual(mocks.saveModule.mock.calls[0])
    expect(m.rows.value[0]!.version).toBe(8); expect(m.errors.value.support).toBe(''); expect(m.busy.value).toBe(null)
  })
  it('sends one partition CAS and does not overwrite the other unsaved draft', async () => {
    const m = moduleModel(); await m.select('2'); m.rows.value[1]!.target_id = 99
    mocks.saveModule.mockResolvedValueOnce({ data: { data: { items: [
      { partition: 'support', version: 8, enabled: true, target_id: 3 },
      { partition: 'membership', version: 11, enabled: false, target_id: 1 },
    ] } } })
    const row = m.rows.value[0]!; expect(row.enabled).toBe(false); expect(row.configured_enabled).toBe(true); row.target_id = 3; await m.save(row)
    expect(mocks.saveModule).toHaveBeenCalledWith(2, { partition: 'support', expected_version: 7, enabled: true, target_id: 3 })
    expect(m.rows.value[0]!.version).toBe(8); expect(m.rows.value[1]!.target_id).toBe(99); expect(m.rows.value[1]!.version).toBe(11)
  })
  it('preserves partition input on conflict and blocks writing without permission', async () => {
    const m = moduleModel(); await m.select('2'); const row = m.rows.value[0]!; row.target_id = 88
    mocks.saveModule.mockRejectedValueOnce({ response: { status: 409 } }); await m.save(row)
    expect(row.target_id).toBe(88); expect(row.version).toBe(7); expect(m.conflicts.value.support).toBe(true)
    await m.save(row); expect(mocks.saveModule).toHaveBeenCalledTimes(1)
    mocks.denied = ['PATCH:/admin/telegram-bot/instances/2/modules']; await m.save(m.rows.value[1]!); expect(mocks.saveModule).toHaveBeenCalledTimes(1)
  })
})
