import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Component from './MembershipDisposition.vue'
import i18n from '@/i18n'
import { membershipDispositionMessages as messages } from '@/i18n/membershipDisposition'
const mocks = vi.hoisted(() => ({ refunds: vi.fn(), dispose: vi.fn(), tasks: vi.fn(), recover: vi.fn(), auth: null as any }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { refunds: mocks.refunds, disposeRefund: mocks.dispose, lifecycleMembers: async (params: unknown) => {
 const response = await mocks.tasks(params)
 const data = response.data.data
 return { data: { data: { ...data, items: data.items.map((item: any) => item.tasks ? item : { telegram_id: item.telegram_id || item.id, tasks: [{ ...item, telegram_id: item.telegram_id || item.id, chat_id: item.chat_id || `-${item.id}` }] }) } } }
}, recoverLifecycle: mocks.recover } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
/** Mount real themed component. @param locale Translation locale. @param orderId Optional refund scope. @returns Setup state. */
function mount(locale: keyof typeof messages, orderId?: number, extra: { embedded?: boolean; active?: boolean; onUpdated?: () => void } = {}) {
 mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
 root = document.createElement('div'); document.body.append(root)
 app = createApp(Component, { orderId, ...extra })
 app.use(createI18n({ legacy: false, locale, missingWarn: false, fallbackWarn: false, messages: { [locale]: { ...i18n.global.getLocaleMessage(locale), membershipDisposition: messages[locale] } } }))
 return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
/** Drain reactive updates. @returns Completion of pending microtasks. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** Expand fixture members and supply identifiers omitted by legacy task-only safety fixtures.
 * @param state Component state. @returns Completion of rendered task rows.
 */
async function expandTasks(state: any) {
 state.tasks = state.tasks.map((task: any) => ({ ...task, telegram_id: task.telegram_id || task.id, chat_id: task.chat_id || `-${task.id}` }))
 state.expanded = new Set(state.tasks.map((task: any) => task.telegram_id))
 await flush()
}
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })
describe('lifecycle evidence and filtering', () => {
 it('keeps same-cycle confirmed removal visible while preserving latest cleanup failure and original history', async () => {
  const state = mount('zh-CN'); state.bot = '1'
  const scope = { telegram_id: '6277335228', chat_id: '-1002453021421', target_id: '1', bot_id: '1', route_version: '4', entitlement_version: '32', cleanup_cycle_id: '172738', version: '1', state: 'succeeded', ban_blocked: false }
  state.tasks = [
   { ...scope, id: '172752', phase: 'done', review_reason: 'removed', ban_confirmed: true, ban_outcome: 'succeeded', unban_outcome: 'succeeded', member_outcome: 'succeeded', invite_cleanup: 'succeeded' },
   { ...scope, id: '172755', phase: 'not_required', review_reason: '', ban_confirmed: false, ban_outcome: '', unban_outcome: '', member_outcome: 'not_required', observed_at: '0001-01-01T00:00:00Z', invite_cleanup: 'failed' },
  ]
  await expandTasks(state)
  const summaryRow = root.querySelector('[data-testid="lifecycle-member"]')!
  expect(summaryRow.textContent).toContain('已确认移除')
  expect(summaryRow.textContent).toContain('解封成功')
  expect(summaryRow.textContent).toContain(messages['zh-CN'].cleanupStates.failed)
  expect(root.querySelector('[data-task-id="172752"]')?.textContent).toContain('本次移出凭据')
  expect(root.querySelector('[data-task-id="172755"]')?.textContent).toContain('无需移出')
  expect(mocks.recover).not.toHaveBeenCalled()
 })
 it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('separates removal, unban and restored admission in %s', async locale => {
  const state = mount(locale), copy = (i18n.global.getLocaleMessage(locale) as any).membershipLifecycle
  state.bot = '9'
  state.tasks = [
   { id: '1', version: '7', state: 'needs_review', phase: 'unban', ban_confirmed: true, ban_outcome: 'succeeded', unban_outcome: 'failed', review_reason: 'platform_failed', observed_at: '2026-11-04T12:00:00Z', member_status: 'kicked', member_is_member: false },
   { id: '2', state: 'needs_review', review_reason: 'payment_pending', observed_at: '0001-01-01T00:00:00Z' },
   { id: '3', state: 'succeeded', review_reason: 'entitlement_active' },
   { id: '4', state: 'succeeded', review_reason: 'admission_restored', ban_confirmed: true, unban_outcome: 'succeeded' },
  ]
  await expandTasks(state)
  const rows = [...root.querySelectorAll('[data-task-id]')]
  expect(rows[0]?.textContent).toContain(messages[locale].removed)
  expect(rows[0]?.textContent).toContain(copy.unbanStates.failed)
  expect(rows[0]?.textContent).toContain(copy.reasons.platform_failed)
  expect(rows[0]?.textContent).toContain(copy.absent)
  expect(rows[0]?.textContent).toContain('2026')
  expect(rows[0]?.textContent).toContain(copy.actions.retryUnban)
  expect(rows[0]?.textContent).not.toContain(copy.actions.retryRemoval)
  expect(rows[1]?.textContent).toContain(copy.results.payment_pending)
  expect(rows[1]?.textContent).not.toContain('0001')
  expect(rows[2]?.textContent).toContain(copy.results.entitlement_active)
  expect(rows[3]?.textContent).toContain(copy.results.admission_restored)
  const retry = [...rows[0]!.querySelectorAll('button')].find(button => button.textContent === copy.actions.retryUnban)!
  retry.click(); await flush()
  expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain(copy.confirm.retryUnban)
  mocks.recover.mockResolvedValue({}); mocks.tasks.mockResolvedValue({ data: { data: { items: [state.tasks[0]], total: 1 } } })
  await state.submit(); await flush()
  expect(mocks.recover).toHaveBeenCalledWith('9', '1', { expected_version: '7', action: 'retry' })
  expect(root.textContent).toContain(copy.unbanStates.failed)
  expect(root.textContent).not.toContain(copy.unbanStates.succeeded)
 })
 it('filters on the server, resets page and rejects late status, bot and page responses', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  let resolveOld!: (value: any) => void
  mocks.tasks.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
  const oldStatus = state.load(3)
  state.status = 'needs_review'
  expect(state.pagination.page).toBe(1)
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ id: 'new' }], total: 100 } } })
  await state.load()
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 20, status: 'needs_review' })
  resolveOld({ data: { data: { items: [{ id: 'old' }], total: 80 } } }); await oldStatus
  expect(state.tasks[0].id).toBe('new')
  mocks.tasks.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
  const oldPage = state.load(2)
  await state.load(4)
  resolveOld({ data: { data: { items: [{ id: 'old-page' }], total: 80 } } }); await oldPage
  expect(state.pagination.page).toBe(4); expect(state.tasks[0].id).toBe('new')
  await state.resetFilters(); expect(state.status).toBe('all'); expect(state.bot).toBe('9')
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 20, status: '', keyword: undefined })
 })
 it('does not allow unknown or blocked retries even through submit', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  for (const evidence of [{ phase: 'unknown_ban' }, { phase: 'unknown_unban' }, { ban_outcome: 'unknown' }, { unban_outcome: 'unknown' }, { ban_blocked: true }]) {
   const task = { id: '1', state: 'needs_review', ...evidence }
   expect(state.recoveryActions(task)).not.toContain('retry')
   state.pending = { task, action: 'retry' }; await state.submit()
  }
  expect(mocks.recover).not.toHaveBeenCalled()
 })
 it('discards delayed recovery after a context or permission change', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  let resolve!: (value: any) => void
  mocks.recover.mockReturnValue(new Promise(accept => { resolve = accept }))
  state.pending = { task: { id: '1', version: '2', state: 'needs_review' }, action: 'recheck' }
  const pending = state.submit()
  state.status = 'completed'
  resolve({}); await pending
  expect(state.accepted).toBe(false); expect(mocks.tasks).not.toHaveBeenCalled()
  state.pending = { task: { id: '1', state: 'needs_review' }, action: 'recheck' }
  mocks.auth.hasPermission = () => false
  mocks.auth.permissions = ['changed']
  await state.submit(); await flush()
  expect(state.pending).toBeUndefined(); expect(root.querySelector('section')).toBeNull()
  expect(mocks.recover).toHaveBeenCalledTimes(1)
 })
 it('keeps paid-but-unposted retry under review rather than reporting completion', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  state.pending = { task: { id: '1', version: '2', state: 'needs_review', phase: 'ban', ban_outcome: 'failed' }, action: 'retry' }
  mocks.recover.mockResolvedValue({})
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ id: '1', state: 'needs_review', review_reason: 'payment_pending' }], total: 1 } } })
  await state.submit(); await flush()
  expect(mocks.recover).toHaveBeenCalledWith('9', '1', { expected_version: '2', action: 'retry' })
  expect(root.textContent).toContain('已付款，权益处理中，暂缓退群')
  expect(root.textContent).not.toContain(messages['zh-CN'].removed)
  expect(state.tasks[0].state).toBe('needs_review')
 })
 it('keeps recheck read-only and applies read-only permissions to row actions', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  const task = { id: '1', version: '3', state: 'needs_review', phase: 'unknown_unban', ban_confirmed: true, unban_outcome: 'unknown' }
  state.pending = { task, action: 'recheck' }
  mocks.recover.mockResolvedValue({}); mocks.tasks.mockResolvedValue({ data: { data: { items: [task], total: 1 } } })
  await state.submit()
  expect(mocks.recover).toHaveBeenCalledWith('9', '1', { expected_version: '3', action: 'recheck' })
  mocks.auth.hasPermission = (permission: string) => permission.startsWith('GET:')
  expect(state.recoveryActions(task)).toEqual([])
  expect(state.observedDate('invalid')).toBe('未知 / 尚无观测')
  expect(state.observedDate(null)).toBe('未知 / 尚无观测')
  expect(state.phaseLabel('not_required')).toBe('无需移出')
  expect(state.observationLabel({ observed_at: '0001-01-01T00:00:00Z', member_is_member: false })).toBe('未知 / 尚无观测')
  expect(state.state({ ban_confirmed: true, review_reason: 'member_without_entitlement' })).toBe('failed')
 })
 it('has the same lifecycle translation keys in all three locales', () => {
  const source = (i18n.global.getLocaleMessage('zh-CN') as any).membershipLifecycle
  for (const locale of ['zh-TW', 'en-US']) {
   const target = (i18n.global.getLocaleMessage(locale) as any).membershipLifecycle
   expect(Object.keys(target).sort()).toEqual(Object.keys(source).sort())
   for (const key of ['filters', 'results', 'unbanStates', 'actions', 'confirm', 'reasons']) expect(Object.keys(target[key]).sort()).toEqual(Object.keys(source[key]).sort())
  }
 })
})
describe('grouped lifecycle members', () => {
 it('searches IDs and usernames on the server and renders the Telegram username column', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  state.keyword = '  @Seven_Time  '
  mocks.tasks.mockResolvedValue({ data: { data: { items: [
   { telegram_id: '9007199254740993', telegram_username: 'Seven_Time', tasks: [{ id: '91', chat_id: '-1001', state: 'pending' }] },
   { telegram_id: '8', telegram_username: '', tasks: [{ id: '92', chat_id: '-1002', state: 'pending' }] },
  ], total: 2 } } })
  await flush()
  const input = root.querySelector<HTMLInputElement>('input[aria-label="成员 ID / 电报用户名"]')!
  expect(input).not.toBeNull(); expect(mocks.tasks).not.toHaveBeenCalled()
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); await flush()
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', keyword: '@Seven_Time', status: '', page: 1, page_size: 20 })
  expect([...root.querySelectorAll('thead th')].slice(0, 3).map(cell => cell.textContent)).toEqual(['成员 Telegram ID', '电报用户名', '绑定群'])
  expect([...root.querySelectorAll('[data-testid="member-username"]')].map(cell => cell.textContent)).toEqual(['@Seven_Time', '—'])
  expect(root.querySelector('[data-testid="member-username"]')?.getAttribute('title')).toBe('@Seven_Time')
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ telegram_id: '9007199254740993', telegram_username: '', tasks: [{ id: '91', chat_id: '-1001', state: 'pending' }] }], total: 1 } } })
  await state.load(); await flush()
  expect(root.querySelector('[data-testid="member-username"]')?.textContent).toBe('—')
  state.keyword = '9007199254740993'; await state.searchMembers()
  expect(mocks.tasks).toHaveBeenLastCalledWith(expect.objectContaining({ keyword: '9007199254740993', page: 1 }))
  state.resetFilters(); expect(state.keyword).toBe(''); expect(state.usernames).toEqual({})
 })
 it('invalidates pending confirmations and late responses when search changes', async () => {
  const state = mount('zh-CN'); state.bot = '9'; state.keyword = 'oldname'
  let resolve!: (value: any) => void
  mocks.tasks.mockReturnValueOnce(new Promise(accept => { resolve = accept }))
  const old = state.load()
  state.pending = { task: { id: '1', state: 'needs_review' }, action: 'recheck' }
  state.keyword = 'newname'
  expect(state.pending).toBeUndefined()
  mocks.tasks.mockResolvedValue({ data: { data: { items: [], total: 0 } } }); await state.searchMembers()
  resolve({ data: { data: { items: [{ telegram_id: '1', telegram_username: 'oldname', tasks: [{ id: '1', chat_id: '-1' }] }], total: 1 } } })
  await old; expect(state.tasks).toEqual([]); expect(state.usernames).toEqual({})
 })
 it('returns to the last valid member page after recovery shrinks the filter', async () => {
  const state = mount('zh-CN'); state.bot = '9'; state.status = 'needs_review'
  const task = { id: '99', telegram_id: '7', chat_id: '-1001', version: '3', state: 'needs_review' }
  state.pagination = { page: 4, page_size: 20, total: 61 }
  state.pending = { task, action: 'recheck' }
  mocks.recover.mockResolvedValue({})
  mocks.tasks.mockResolvedValueOnce({ data: { data: { items: [], total: 1 } } })
   .mockResolvedValueOnce({ data: { data: { items: [{ telegram_id: '8', tasks: [{ ...task, id: '100', telegram_id: '8' }] }], total: 1 } } })
  await state.submit(); await flush()
  expect(mocks.tasks).toHaveBeenNthCalledWith(1, { bot_id: '9', page: 4, page_size: 20, status: 'needs_review' })
  expect(mocks.tasks).toHaveBeenNthCalledWith(2, { bot_id: '9', page: 1, page_size: 20, status: 'needs_review' })
  expect(state.pagination).toEqual({ page: 1, page_size: 20, total: 1 })
  expect(state.tasks[0].telegram_id).toBe('8'); expect(state.accepted).toBe(true); expect(state.saving).toBe(false)
 })
 it('resets an empty result to page one without another query', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  state.pagination = { page: 4, page_size: 20, total: 61 }
  mocks.tasks.mockResolvedValue({ data: { data: { items: [], total: 0 } } })
  await state.load()
  expect(mocks.tasks).toHaveBeenCalledTimes(1)
  expect(state.pagination).toEqual({ page: 1, page_size: 20, total: 0 })
 })
 it('discards a page correction when the bot context changes', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  let resolve!: (value: any) => void
  mocks.tasks.mockResolvedValueOnce({ data: { data: { items: [], total: 1 } } })
   .mockReturnValueOnce(new Promise(accept => { resolve = accept }))
  const loading = state.load(4); await flush()
  state.bot = '10'
  resolve({ data: { data: { items: [{ id: 'OLD' }], total: 1 } } }); await loading
  expect(state.tasks).toEqual([]); expect(state.pagination.total).toBe(0)
 })
 it('shows one member, distinct group summaries, history, and exact scoped recovery', async () => {
  const state = mount('zh-CN'); state.bot = '9007199254740993'
  const member = '9007199254740994', chat = '-1009007199254740993'
  const tasks = [
   { id: '9007199254740992', telegram_id: member, chat_id: chat, state: 'needs_review', phase: 'unknown_ban', ban_outcome: 'unknown', version: '8', target_id: 'HIDDEN_TARGET' },
   { id: '9007199254740993', telegram_id: member, chat_id: chat, state: 'succeeded', phase: 'done', review_reason: 'removed', unban_outcome: 'succeeded', invite_cleanup: 'succeeded', attempt: 2 },
   { id: '9007199254740995', telegram_id: member, chat_id: '-1002', state: 'needs_review', phase: 'ban', member_outcome: 'failed', unban_outcome: 'failed', invite_cleanup: 'failed', version: '9007199254740996', attempt: 3 },
  ]
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ telegram_id: member, tasks }], total: 41 } } })
  await state.load(2); await flush()
  expect(state.pagination).toEqual({ page: 2, page_size: 20, total: 41 })
  expect(root.querySelectorAll('[data-testid="lifecycle-member"]')).toHaveLength(1)
  const row = root.querySelector('[data-testid="lifecycle-member"]')!
  expect(row.querySelector('td')?.textContent).toBe(member)
  expect(row.textContent).toContain('2 个群'); expect(row.textContent).toContain('1 条历史任务尚未结束')
  expect(row.textContent).toContain(`${messages['zh-CN'].removed} 1`); expect(row.textContent).toContain(`${messages['zh-CN'].failed} 1`)
  expect(root.querySelector('[data-testid="member-groups"]')?.textContent).toBe('-1002')
  expect(root.querySelectorAll('[data-task-id]')).toHaveLength(0)
  row.querySelector('button')!.click(); await flush()
  expect(root.querySelector('[data-testid="member-groups"]')?.textContent).toBe(`-1002, ${chat}`)
  expect(root.querySelectorAll('[data-task-id]')).toHaveLength(3)
  expect(root.textContent).not.toContain('HIDDEN_TARGET')
  const taskRow = root.querySelector('[data-task-id="9007199254740995"]')!
  ;(taskRow.querySelector('button') as HTMLButtonElement).click(); await flush()
  expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain(`成员 ${member} · 群 -1002`)
  mocks.recover.mockResolvedValue({}); await state.submit(); await flush()
  expect(mocks.recover).toHaveBeenCalledWith('9007199254740993', '9007199254740995', { expected_version: '9007199254740996', action: 'recheck' })
  expect(state.expanded.has(member)).toBe(true)
  await state.load(3); expect(state.expanded.size).toBe(0)
 })
 it('ignores errors from superseded pages without replacing current member evidence', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  let rejectOld!: (error: Error) => void
  mocks.tasks.mockReturnValueOnce(new Promise((_resolve, reject) => { rejectOld = reject }))
  const old = state.load(2)
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ telegram_id: '9007199254740993', tasks: [{ id: '9007199254740994', chat_id: '-1001', state: 'pending' }] }], total: 1 } } })
  await state.load(1)
  rejectOld(new Error('old page failed')); await old
  expect(state.error).toBe(''); expect(state.pagination.total).toBe(1)
  expect(state.members[0].telegram_id).toBe('9007199254740993')
  expect(state.loading).toBe(false)
 })
 it('retains legal expansions on refresh, prunes disappeared members and clears changed permissions', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  const response = (ids: string[]) => ({ data: { data: { items: ids.map(id => ({ telegram_id: id, tasks: [{ id, chat_id: `-${id}`, state: 'pending' }] })), total: ids.length } } })
  mocks.tasks.mockResolvedValue(response(['9007199254740993', '9007199254740994']))
  await state.load(); state.toggleMember('9007199254740993'); state.toggleMember('9007199254740994')
  mocks.tasks.mockRejectedValueOnce(new Error('failed refresh')); await state.load()
  expect(state.expanded.size).toBe(2); expect(state.error).toBeTruthy()
  mocks.tasks.mockResolvedValue(response(['9007199254740993'])); await state.load()
  expect([...state.expanded]).toEqual(['9007199254740993'])
  mocks.auth.permissions = ['changed']; await flush()
  expect(state.expanded.size).toBe(0); expect(state.tasks).toEqual([])
 })
})
describe('refund disposition', () => {
 it('embeds without a duplicate title, emits updates and closes hidden confirmations', async () => {
  const updated = vi.fn(), state = mount('zh-CN', undefined, { embedded: true, active: true, onUpdated: updated })
  state.bot = '9'; mocks.tasks.mockResolvedValue({ data: { data: { items: [] } } })
  await state.load(); await flush()
  expect(root.querySelector('h3')).toBeNull(); expect(updated).toHaveBeenCalledTimes(1)
  state.pending = { task: { id: '1' }, action: 'recheck' }; await flush()
  expect(document.body.querySelector('[role="dialog"]')).not.toBeNull()
  ;(app as unknown as { _instance: { props: { active: boolean } } })._instance.props.active = false
  await flush(); expect(state.pending).toBeUndefined(); expect(state.bot).toBe('9')
 })
 it.each([undefined, 10])('uses product table styling with compact refund details for scope %s', async orderId => {
  const state = mount('zh-CN', orderId)
  if (orderId) state.candidates = [{ refund_id: '2', order_item_id: '3', source_ids: [], disposed: false }]
  else state.tasks = [{ id: '1', telegram_id: '7', chat_id: '-8', phase: 'check', state: 'needs_review' }]
  await flush()
  const table = root.querySelector('table')!
  expect(table.closest('.rounded-xl')?.className).toContain('border border-border bg-card overflow-hidden')
  expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
  expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
  expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
  expect(table.querySelector('th')?.className).toContain(orderId ? 'px-4 py-3' : 'px-6 py-3')
  expect(table.querySelector('td')?.className).toContain(orderId ? 'px-4 py-3' : 'px-6 py-4')
  expect(table.querySelector('th:last-child')?.className).toContain('text-right')
  expect(table.querySelector('td:last-child')?.className).toContain('text-right')
  if (orderId) {
   expect(table.querySelector('td:last-child > div')?.className).toBe('flex flex-wrap items-center justify-end gap-2')
   expect(table.querySelector('button')?.className).toContain('border-input')
  } else expect(table.querySelector('button')?.getAttribute('aria-expanded')).toBe('false')
  expect(table.querySelector('button')?.className).toContain('h-8')
  expect(mocks.dispose).not.toHaveBeenCalled(); expect(mocks.recover).not.toHaveBeenCalled()
 })
 it('resets lifecycle query once, retaining bot scope and guarding loading', async () => {
  const state = mount('zh-CN'); state.bot = '9'; state.status = 'needs_review'; state.keyword = 'member'
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ id: '1', phase: 'check', state: 'pending' }], total: 60 } } })
  await state.load(); await state.load(3)
  await state.resetFilters(); await flush()
  expect(state.bot).toBe('9'); expect(state.pagination.page).toBe(1); expect(state.tasks[0].id).toBe('1')
  expect(state.keyword).toBe(''); expect(state.status).toBe('all')
  expect(mocks.tasks).toHaveBeenCalledTimes(3)
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 20, status: '', keyword: undefined })
  await state.resetFilters(); expect(mocks.tasks).toHaveBeenCalledTimes(4)
  state.loading = true; await state.resetFilters(); expect(mocks.tasks).toHaveBeenCalledTimes(4)
  state.loading = false; state.bot = ''; await state.resetFilters(); expect(mocks.tasks).toHaveBeenCalledTimes(4)
  expect(mocks.recover).not.toHaveBeenCalled(); expect(mocks.dispose).not.toHaveBeenCalled()
 })
 it('jumps directly, preserves failed page and size, and keeps zero-data pagination', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ id: '61' }], total: 100 } } })
  await state.load(4); await flush()
  expect(mocks.tasks).toHaveBeenCalledTimes(1)
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 4, page_size: 20, status: '' })
  mocks.tasks.mockRejectedValueOnce(new Error('unavailable'))
  await state.load(1, 50)
  expect(state.pagination).toEqual({ page: 4, page_size: 20, total: 100 }); expect(state.tasks[0].id).toBe('61')
  mocks.tasks.mockResolvedValue({ data: { data: { items: [], total: 0 } } })
  await state.load(1, 50); await flush()
  expect(state.pagination).toEqual({ page: 1, page_size: 50, total: 0 })
  const container = root.querySelector('table')!.closest('.rounded-xl')!
  expect(container.querySelector('input[type="number"]')).not.toBeNull()
  expect(container.querySelector('input[type="number"]')?.closest('.overflow-auto')).toBeNull()
 })
 it('retains the loaded page during loading and discards stale bot responses', async () => {
  const state = mount('zh-CN'); state.bot = '9'
  mocks.tasks.mockResolvedValue({ data: { data: { items: [{ id: '21' }], total: 100 } } })
  await state.load(2)
  let resolve!: (value: any) => void
  mocks.tasks.mockReturnValueOnce(new Promise(accept => { resolve = accept }))
  const pending = state.load(4); await flush()
  expect(state.pagination.page).toBe(2); expect(state.tasks[0].id).toBe('21')
  expect(root.querySelector<HTMLInputElement>('input[type="number"]')?.disabled).toBe(true)
  state.bot = '10'; resolve({ data: { data: { items: [{ id: 'OLD_BOT' }], total: 100 } } })
  await pending; await flush()
  expect(state.tasks).toEqual([]); expect(state.pagination.page).toBe(1)
  expect(root.textContent).not.toContain('OLD_BOT')
 })
 it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('allows unposted purchase withdrawal in %s', async locale => {
  const state = mount(locale, 10)
  const candidate = { refund_id: '2', order_item_id: '3', account_id: '7', expected_version: '1', snapshot_hash: 'hash', source_ids: [], beneficiary_telegram_id: '9007199254740993', disposed: false }
  mocks.refunds.mockResolvedValue({ data: { data: { items: [candidate] } } }); mocks.dispose.mockResolvedValue({})
  await state.load(); await flush()
  expect(root.textContent).toContain(messages[locale].unposted)
  const action = [...root.querySelectorAll('button')].find(button => button.getAttribute('aria-label') === messages[locale].revoke)!
  expect(action.disabled).toBe(false); action.click(); await flush(); await state.submit(); await flush()
  expect(mocks.dispose).toHaveBeenCalledWith(candidate)
  expect(root.textContent).not.toContain(messages[locale].removed)
 })
 it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('keeps per-group outcomes distinct in %s', async locale => {
  const state = mount(locale); state.bot = '9'
  mocks.tasks.mockResolvedValue({ data: { data: { items: [
   { id: '1', phase: 'check', state: 'pending' }, { id: '2', phase: 'ban', state: 'creating' },
   { id: '3', phase: 'done', state: 'succeeded', review_reason: 'removed' },
   { id: '4', phase: 'unknown_ban', state: 'needs_review', ban_outcome: 'unknown' },
   { id: '5', phase: 'check', state: 'needs_review', review_reason: 'administrator_member' },
  ], total: 5, next_cursor: '' } } })
  await state.load(); await expandTasks(state)
  for (const key of ['queued', 'processing', 'removed', 'unknown', 'failed', 'administrator_member'] as const) expect(root.textContent).toContain(messages[locale][key])
  const unknown = [...root.querySelectorAll('[data-task-id]')].find(row => row.textContent?.includes(messages[locale].unknown))!
  expect(unknown.textContent).not.toContain(messages[locale].retry)
  expect([...unknown.querySelectorAll('button')].some(button => button.getAttribute('aria-label') === (i18n.global.getLocaleMessage(locale) as any).membershipLifecycle.actions.recheck)).toBe(true)
 })
 it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('separates removal and cleanup evidence in %s', async locale => {
  const state = mount(locale)
  state.tasks = [
   { id: '1', phase: 'done', state: 'failed', member_outcome: 'succeeded', review_reason: 'removed', invite_cleanup: 'failed', invite_cleanup_total: 2, invite_cleanup_completed: 1, invite_cleanup_failed: 1, invite_cleanup_unknown: 0 },
   { id: '2', phase: 'done', state: 'succeeded', review_reason: 'already_left', invite_cleanup: 'unknown', invite_cleanup_total: 1, invite_cleanup_completed: 0, invite_cleanup_failed: 0, invite_cleanup_unknown: 1 },
   { id: '3', phase: 'done', state: 'succeeded', review_reason: 'removed' },
  ]
  await expandTasks(state)
  const rows = [...root.querySelectorAll('[data-task-id]')]
  expect(rows[0]?.textContent).toContain(messages[locale].removed)
  expect(rows[0]?.textContent).toContain(messages[locale].cleanupStates.failed)
  expect(rows[1]?.textContent).toContain(messages[locale].already_left)
  expect(rows[1]?.textContent).toContain(messages[locale].cleanupStates.unknown)
  expect(rows[1]?.textContent).not.toContain(messages[locale].removed)
  expect(rows[2]?.textContent).toContain(messages[locale].cleanupStates.unavailable)
  expect(root.textContent).not.toContain(messages[locale].cleanupStates.succeeded)
  expect(mocks.recover).not.toHaveBeenCalled()
 })
 it('shows recovery only for server-accepted member evidence, never for cleanup-only failures', async () => {
  const state = mount('zh-CN')
  for (const task of [
   { id: '1', state: 'pending' },
   { id: '2', state: 'creating' },
   { id: '3', state: 'succeeded', member_outcome: 'removed', invite_cleanup: 'failed' },
   { id: '4', state: 'not_required', invite_cleanup: 'unknown' },
   { id: '5', state: 'needs_review', ban_blocked: true },
  ]) expect(state.recoveryActions(task)).toEqual([])
  expect(state.recoveryActions({ id: '6', state: 'needs_review', unban_outcome: 'succeeded' })).toEqual(['recheck', 'block_ban'])
  expect(state.recoveryActions({ id: '7', state: 'needs_review', ban_outcome: 'unknown' })).toEqual(['recheck', 'block_ban'])
  expect(state.recoveryActions({ id: '8', state: 'needs_review', ban_outcome: 'failed' })).toEqual(['recheck', 'retry', 'block_ban'])
  await flush()
 })
 it('sends exact purchase evidence and never marks the accepted command as kicked', async () => {
  const state = mount('en-US', 10)
  const candidate = { refund_id: '2', order_item_id: '3', account_id: '7', expected_version: '1', snapshot_hash: 'hash', source_ids: [5, 6], beneficiary_telegram_id: '9007199254740993', disposed: false }
  mocks.refunds.mockResolvedValue({ data: { data: { items: [candidate] } } }); mocks.dispose.mockResolvedValue({})
  await state.load(); state.pending = { candidate }; await state.submit(); await flush()
  expect(mocks.dispose).toHaveBeenCalledWith(candidate)
  expect(root.textContent).toContain(messages['en-US'].accepted)
  expect(root.textContent).not.toContain(messages['en-US'].removed)
  mocks.auth.token = 'second'; await flush(); expect(state.candidates).toEqual([]); expect(state.accepted).toBe(false)
 })
})
