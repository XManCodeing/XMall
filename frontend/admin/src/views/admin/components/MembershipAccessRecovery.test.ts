import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Recovery from './MembershipAccessRecovery.vue'
import i18n from '@/i18n'
import { membershipRecoveryMessages } from '@/i18n/membershipRecovery'
import { formatDate } from '@/utils/format'
const mocks = vi.hoisted(() => ({ tasks: vi.fn(), reviews: vi.fn(), recover: vi.fn(), auth: null as any }))
vi.mock('@/api/telegramMembership', () => ({ membershipBase: '/admin/telegram-bot/membership', telegramMembershipAPI: { accessTasks: mocks.tasks, accessReviews: mocks.reviews, recoverAccess: mocks.recover } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
let app: App, root: HTMLDivElement
/** Drain Vue updates and mocked requests, returning no value. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** Mount the real table and theme-aware controls. @param locale UI locale. @returns Reactive setup state. */
function mountRecovery(locale: 'zh-CN' | 'zh-TW' | 'en-US' = 'en-US', props: { mode?: 'tasks' | 'reviews' | 'all'; active?: boolean; onUpdated?: () => void } = {}) {
 root = document.createElement('div'); document.body.append(root)
 app = createApp(Recovery, props)
 app.use(createI18n({ legacy: false, locale, missingWarn: false, fallbackWarn: false, messages: { [locale]: { ...i18n.global.getLocaleMessage(locale), membershipRecovery: membershipRecoveryMessages[locale] } } }))
 return (app.mount(root).$ as unknown as { setupState: any }).setupState
}
/** Wrap mock page data. @param items Rows. @param next Next cursor. @returns API response. */
function page(items: object[] = [], next = '') { return { data: { data: { items, total: 50, next_cursor: next } } } }
/** Create a controllable request. @returns Promise with explicit settlement functions. */
function deferred() {
 let resolve!: (value: any) => void, reject!: (reason: Error) => void
 const promise = new Promise<any>((accept, fail) => { resolve = accept; reject = fail })
 return { promise, resolve, reject }
}
beforeEach(() => { vi.stubGlobal('ResizeObserver', class { observe = vi.fn(); unobserve = vi.fn(); disconnect = vi.fn() }) })
afterEach(() => { app?.unmount(); root?.remove(); document.documentElement.classList.remove('dark'); vi.resetAllMocks(); vi.unstubAllGlobals() })
describe('membership admission recovery', () => {
 it('defaults to pending, combines status with keywords, discards stale reads and corrects shrinking pages', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  const state = mountRecovery('zh-CN', { mode: 'reviews' }); state.bot = '9'
  expect(state.reviewStatus).toBe('pending')
  mocks.reviews.mockResolvedValue(page([{ id: '1' }]))
  await state.load(1)
  expect(mocks.reviews).toHaveBeenLastCalledWith(expect.objectContaining({ status: 'pending' }))
  const request = deferred(); mocks.reviews.mockReturnValueOnce(request.promise)
  const loading = state.load(2, 'reviews')
  state.reviewStatus = 'reviewed'; state.reviewKeyword = '@alice'; await flush()
  request.resolve(page([{ id: 'stale' }])); await loading
  expect(state.reviews).toEqual([]); expect(state.reviewPagination.page).toBe(1)
  await state.load(1)
  expect(mocks.reviews).toHaveBeenLastCalledWith(expect.objectContaining({ status: 'reviewed', keyword: '@alice' }))
  state.reviewStatus = 'all'; await state.load(1)
  expect(mocks.reviews).toHaveBeenLastCalledWith(expect.objectContaining({ status: 'all' }))
  state.reviewStatus = 'pending'; state.reviewPagination = { page: 2, page_size: 20, total: 21 }
  mocks.reviews.mockResolvedValueOnce({ data: { data: { items: [], total: 20 } } }).mockResolvedValueOnce({ data: { data: { items: [{ id: 'remaining' }], total: 20 } } })
  await state.load()
  expect(state.reviewPagination.page).toBe(1); expect(state.reviews[0].id).toBe('remaining')
  state.reviewStatus = 'reviewed'; state.resetFilters()
  expect(state.reviewStatus).toBe('pending')
 })

 for (const locale of ['zh-CN', 'zh-TW', 'en-US'] as const) {
  it(`explains review-only acknowledgement without implying platform actions in ${locale}`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
   const state = mountRecovery(locale, { mode: 'reviews' }), messages = membershipRecoveryMessages[locale]
   state.bot = '9'
   state.reviews = [{ id: '8', state: 'needs_review' }]
   await flush()
   expect(root.querySelector('tbody button')?.textContent).toBe(messages.reviewEntry)
   root.querySelector<HTMLButtonElement>('tbody button')!.click(); await flush()
   const dialog = document.body.querySelector('[role="dialog"]')!
   expect(dialog.textContent).toContain(messages.reviewDescription)
   expect(dialog.textContent).toContain(messages.reviewNotice)
   expect(dialog.textContent).toContain(messages.reviewConfirm)
   expect(dialog.textContent).not.toContain(messages.confirm)
   expect(dialog.textContent).not.toContain(messages.manualOnly)
   mocks.recover.mockResolvedValue({}); mocks.reviews.mockResolvedValue(page([{ id: '8', reviewed_at: '2026-11-05T09:00:00Z' }]))
   await state.recover(); await flush()
   expect(root.textContent).toContain(messages.reviewAccepted)
   expect(root.textContent).not.toContain(messages.accepted)
   expect(mocks.recover).toHaveBeenCalledWith('9', '8', expect.objectContaining({ action: 'review_join' }))
  })
 }

 it('shows current usernames and searches server-side with stale-response protection', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  const state = mountRecovery('zh-CN', { mode: 'reviews' })
  state.bot = '9'; state.reviewKeyword = ' @Seven_Time '
  mocks.reviews.mockResolvedValue(page([{ id: '1', telegram_username: 'Seven_Time', telegram_id: '42' }, { id: '2' }]))
  await state.load(1); await flush()
  expect(root.querySelector('[data-testid="review-username"]')?.textContent).toBe('@Seven_Time')
  expect(root.querySelector('[data-testid="review-username"]')?.getAttribute('title')).toBe('@Seven_Time')
  expect(root.querySelectorAll('[data-testid="review-username"]')[1]?.textContent).toBe('—')
  document.documentElement.classList.add('dark'); await flush()
  expect(state.reviewKeyword).toBe(' @Seven_Time ')
  expect(root.querySelector('[data-testid="review-username"]')?.textContent).toBe('@Seven_Time')
  await state.load(2, 'reviews', 10)
  expect(mocks.reviews).toHaveBeenLastCalledWith({ bot_id: '9', keyword: '@Seven_Time', status: 'pending', page: 2, page_size: 10 })
  const request = deferred(); mocks.reviews.mockReturnValueOnce(request.promise)
  const loading = state.load(3, 'reviews')
  state.reviewKeyword = '9007199254740993'; await flush()
  expect(state.reviewPagination.page).toBe(1)
  expect(state.reviews).toEqual([])
  request.resolve(page([{ id: 'STALE' }])); await loading
  expect(state.reviews).toEqual([])
  await state.load(1, 'reviews')
  expect(mocks.reviews).toHaveBeenLastCalledWith({ bot_id: '9', keyword: '9007199254740993', status: 'pending', page: 1, page_size: 10 })
  expect(mocks.tasks).not.toHaveBeenCalled()
  mocks.reviews.mockResolvedValue(page())
  await state.resetFilters(); await flush()
  expect(state.reviewKeyword).toBe('')
  expect(root.querySelector('tbody td')?.getAttribute('colspan')).toBe('6')
 })

 for (const locale of ['zh-CN', 'zh-TW', 'en-US'] as const) {
  it(`keeps review status separate from reason and application time before actions in ${locale}`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
   const state = mountRecovery(locale, { mode: 'reviews' }), messages = membershipRecoveryMessages[locale]
   const receivedAt = '2026-11-05T08:38:42Z', reviewedAt = '2026-11-05T09:00:00Z'
   state.reviews = [{ id: 'INTERNAL_REVIEW_ID', chat_id: '-100123', telegram_id: '42', state: 'needs_review', received_at: receivedAt },
    { id: 'REVIEWED_INTERNAL_ID', chat_id: '-100456', telegram_id: '43', state: 'needs_review', received_at: receivedAt, reviewed_at: reviewedAt }]
   await flush()
   const headers = root.querySelectorAll('th'), rows = root.querySelectorAll('tbody tr')
   expect(headers).toHaveLength(6)
   expect(headers[4]?.textContent).toBe(messages.reviewReceivedAt)
   expect(rows[0]?.querySelectorAll('td')[4]?.textContent).toBe(formatDate(receivedAt))
   const pending = rows[0]!.querySelector('[data-testid="review-state"]')!, reviewed = rows[1]!.querySelector('[data-testid="review-state"]')!
   expect(pending.firstElementChild?.textContent).toBe(messages.reviewPending)
   expect(pending.textContent).toContain(messages.reviewReason)
   expect(pending.textContent).not.toContain('—')
   expect(reviewed.firstElementChild?.textContent).toBe(messages.reviewDone)
   expect(reviewed.textContent).toContain(formatDate(reviewedAt))
   expect(rows[0]?.querySelector('button')).not.toBeNull()
   expect(rows[1]?.querySelector('button')).toBeNull()
   expect(root.textContent).not.toMatch(/INTERNAL_REVIEW_ID|REVIEWED_INTERNAL_ID/)
   expect(root.textContent).not.toContain(messages.manualOnly)
  })
 }

 it('shows only trusted associations and keeps identifiers in independent columns', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  const state = mountRecovery('zh-CN', { mode: 'tasks' })
  state.tasks = [{ id: 'TASK_INTERNAL', target_id: 'TARGET_INTERNAL', source_id: 'SOURCE_INTERNAL', kind: 'approve', buyer_telegram_id: '9007199254740993', telegram_id: '77', chat_id: '-1009007199254740993', chat_title: '测试群名称'.repeat(10), order_no: 'ORDER-TRUSTED', state: 'unknown', review_reason: 'unknown_effect', error_class: 'network' },
   { id: 'unlinked', kind: 'create', telegram_id: '9007199254740993', state: 'pending' }]
  await flush()
  const rows = root.querySelectorAll('tbody tr')
  const cells = rows[0]!.querySelectorAll('td')
  expect(cells[0]?.textContent).toBe('ORDER-TRUSTED'); expect(cells[1]?.textContent).toBe('批准申请')
  expect(cells[2]?.textContent).toBe('9007199254740993'); expect(cells[3]?.textContent).toBe('-1009007199254740993'); expect(cells[4]?.textContent).toBe('测试群名称'.repeat(10))
  expect(cells[5]?.textContent).toContain('副作用未知，禁止盲重试')
  expect(cells[0]?.querySelector('.truncate')).not.toBeNull(); expect(cells[4]?.querySelector('.truncate')).not.toBeNull()
  expect(rows[1]!.querySelectorAll('td')[2]?.textContent).toBe('无')
  expect(rows[1]!.querySelectorAll('td')[0]?.textContent).toBe('无')
  expect(rows[1]!.querySelectorAll('td')[5]?.textContent).toContain('无')
  expect(root.textContent).not.toMatch(/TASK_INTERNAL|TARGET_INTERNAL|SOURCE_INTERNAL/)
  const trigger = cells[0]!.querySelector<HTMLElement>('[data-slot="tooltip-trigger"]')!
  trigger.focus(); await flush()
  expect(document.body.querySelector('[role="tooltip"]')?.textContent).toContain('ORDER-TRUSTED')
 })
 it('hides empty task actions but retains authorized recovery actions and a nonwrapping state row', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
  const state = mountRecovery('zh-CN', { mode: 'tasks' })
  state.tasks = [{ id: '7', state: 'failed', actions: [] }]; await flush()
  expect(root.querySelectorAll('thead th')).toHaveLength(8)
  expect(root.querySelector('thead th')?.textContent).toBe(membershipRecoveryMessages['zh-CN'].orderNo)
  const badges = root.querySelector('[data-testid="task-state"] > div')!
  expect(badges.classList.contains('flex-nowrap')).toBe(true)
  expect(badges.classList.contains('w-max')).toBe(true)
  state.tasks[0].actions = ['refresh_member']; await flush()
  expect(root.querySelectorAll('thead th')).toHaveLength(9)
  expect(root.querySelectorAll('tbody tr:first-child td')).toHaveLength(9)
  mocks.auth.hasPermission = (permission: string) => permission.startsWith('GET:'); await flush()
  expect(root.querySelectorAll('thead th')).toHaveLength(8)
  state.tasks = []; await flush()
  expect(root.querySelector('tbody td')?.getAttribute('colspan')).toBe('8')
 })
 it('passes trimmed order filters through numbered pages and discards stale filtered tasks', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  mocks.tasks.mockResolvedValue(page([{ id: 'first' }])); mocks.reviews.mockResolvedValue(page())
  const state = mountRecovery('en-US', { mode: 'tasks' }); state.bot = '9'; state.orderNo = '  ORDER-A  '
  await state.load(1); await state.load(3, 'tasks')
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 3, page_size: 20, order_no: 'ORDER-A' })
  const request = deferred(); mocks.tasks.mockReturnValueOnce(request.promise)
  const loading = state.load(4, 'tasks'); state.orderNo = 'ORDER-B'; await flush()
  expect(state.taskPagination.page).toBe(1); expect(state.tasks).toEqual([])
  request.resolve(page([{ id: 'STALE' }])); await loading
  expect(state.tasks).toEqual([])
  await state.load(1, 'tasks', 50)
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 50, order_no: 'ORDER-B' })
  await state.resetFilters(); expect(state.orderNo).toBe(''); expect(state.bot).toBe('9')
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 50, order_no: undefined })
  expect(mocks.reviews).not.toHaveBeenCalled()
 })
 for (const mode of ['tasks', 'reviews'] as const) {
  it(`requests only ${mode} and emits refreshed status`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
   mocks.tasks.mockResolvedValue(page()); mocks.reviews.mockResolvedValue(page())
   const updated = vi.fn(), state = mountRecovery('en-US', { mode, onUpdated: updated })
   state.bot = '9'; await state.load(); await flush()
   expect(mocks[mode]).toHaveBeenCalledTimes(1)
   expect(mocks[mode === 'tasks' ? 'reviews' : 'tasks']).not.toHaveBeenCalled()
   expect(root.querySelectorAll('table')).toHaveLength(1)
   expect(root.querySelector('h2')).toBeNull()
   expect(updated).toHaveBeenCalledTimes(1)
   mocks.auth.hasPermission = () => false; await flush(); await state.load()
   expect(mocks[mode]).toHaveBeenCalledTimes(1)
  })
 }
 it('closes teleported confirmation while keeping the hidden panel draft', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  const state = mountRecovery('en-US', { mode: 'tasks', active: true })
  state.bot = '9'; state.pending = { row: { id: '1' }, action: 'retry' }; await flush()
  expect(document.body.querySelector('[role="dialog"]')).not.toBeNull()
  ;(app as unknown as { _instance: { props: { active: boolean } } })._instance.props.active = false
  await flush()
  expect(state.pending).toBeUndefined(); expect(state.bot).toBe('9')
 })
 it('aligns both operational tables and row actions with product list styling', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  const state = mountRecovery()
  state.tasks = [{ id: '1', actions: ['review'] }]; state.reviews = [{ id: '2' }]
  await flush()
  const tables = [...root.querySelectorAll('table')]
  expect(tables).toHaveLength(2)
  for (const table of tables) {
   expect(table.closest('.rounded-xl')?.className).toContain('border border-border bg-card overflow-hidden')
   expect(table.querySelector('thead')?.className).toContain('border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground')
   expect(table.querySelector('tbody')?.className).toContain('divide-y divide-border')
   expect(table.querySelector('tbody tr')?.className).toContain('hover:bg-muted/30')
   for (const cell of table.querySelectorAll('th')) expect(cell.className).toContain('px-6 py-3')
   for (const cell of table.querySelectorAll('td')) expect(cell.className).toContain('px-6 py-4')
   expect(table.querySelector('th:last-child')?.className).toContain('text-right')
   expect(table.querySelector('td:last-child')?.className).toContain('text-right')
   expect(table.querySelector('td:last-child > div')?.className).toBe('flex flex-wrap items-center justify-end gap-2')
   expect(table.querySelector('button')?.className).toContain('border-input')
   expect(table.querySelector('button')?.className).toContain('h-8')
  }
  expect(mocks.recover).not.toHaveBeenCalled()
 })
 it('resets both lists with one first-page request per table and retains the required bot', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  mocks.tasks.mockResolvedValue(page()); mocks.reviews.mockResolvedValue(page())
  const state = mountRecovery(); state.bot = '9'
  state.orderNo = 'ORDER'; state.reviewKeyword = '@member'; state.reviewStatus = 'all'
  await state.load(2)
  await state.resetFilters(); await flush()
  expect(state.bot).toBe('9'); expect(state.orderNo).toBe(''); expect(state.reviewKeyword).toBe('')
  expect(state.taskPagination.page).toBe(1); expect(state.reviewPagination.page).toBe(1)
  expect(mocks.tasks).toHaveBeenCalledTimes(2); expect(mocks.reviews).toHaveBeenCalledTimes(2)
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 20, order_no: undefined })
  expect(mocks.reviews).toHaveBeenLastCalledWith({ bot_id: '9', page: 1, page_size: 20, status: 'pending', keyword: undefined })
  await state.resetFilters()
  expect(mocks.tasks).toHaveBeenCalledTimes(3); expect(mocks.reviews).toHaveBeenCalledTimes(3)
  state.loading = true; await state.resetFilters()
  expect(mocks.tasks).toHaveBeenCalledTimes(3)
  state.loading = false; state.bot = ''; await state.resetFilters()
  expect(mocks.tasks).toHaveBeenCalledTimes(3)
  expect(mocks.recover).not.toHaveBeenCalled()
 })
 for (const locale of ['zh-CN', 'zh-TW', 'en-US'] as const) {
  it(`shows unknown facts and only versioned authorized actions in ${locale}`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
   const task = { id: '3', target_id: '2', chat_id: '-1001', buyer_telegram_id: '9007199254740993', telegram_id: '9007199254740993', source_id: '1', kind: 'approve', state: 'unknown', version: '7', route_version: '2', actions: ['reconcile', 'review'] }
   mocks.tasks.mockResolvedValue({ data: { data: { items: [task], next_cursor: '' } } })
   mocks.reviews.mockResolvedValue({ data: { data: { items: [{ id: '8', chat_id: '-1001', telegram_id: '77', state: 'needs_review' }], next_cursor: '' } } })
   mocks.recover.mockResolvedValue({ data: { data: { accepted: true } } })
   const state = mountRecovery(locale)
   state.bot = '9'; await flush(); await state.load(); await flush()
   expect(root.textContent).toContain('9007199254740993')
   expect(root.textContent).toContain(membershipRecoveryMessages[locale].reviewReason)
   expect(root.textContent).not.toContain(membershipRecoveryMessages[locale].values.retry)
   state.pending = { row: task, action: 'reconcile' }; await state.recover(); await flush()
   expect(mocks.recover).toHaveBeenCalledWith('9', '3', { action: 'reconcile', expected_version: '7', expected_route_version: '2', reason: 'operator_verified' })
   mocks.auth.token = 'second'; await flush()
   expect(state.tasks).toEqual([]); expect(state.reviews).toEqual([])
  })
  it(`submits only a read-only member refresh with separate retry guidance in ${locale}`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
   const task = { id: '3', kind: 'create', state: 'failed', version: '7', route_version: '2', actions: ['refresh_member', 'retry'] }
   mocks.tasks.mockResolvedValue(page([task])); mocks.reviews.mockResolvedValue(page()); mocks.recover.mockResolvedValue({})
   const state = mountRecovery(locale); state.bot = '9'; await state.load(); await flush()
   const action = [...root.querySelectorAll('button')].find(button => button.getAttribute('aria-label') === membershipRecoveryMessages[locale].values.refresh_member)!
   action.click(); await flush()
   expect(document.body.textContent).toContain(membershipRecoveryMessages[locale].refreshHint)
   await state.recover(); await flush()
   expect(mocks.recover).toHaveBeenCalledTimes(1)
   expect(mocks.recover).toHaveBeenCalledWith('9', '3', { action: 'refresh_member', expected_version: '7', expected_route_version: '2', reason: 'operator_verified' })
  })
  it(`renders all delivery states separately from task results in ${locale}`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
   const states = ['not_requested', 'queued', 'sending', 'delivered', 'failed', 'unknown', 'superseded'] as const
   const updated = '2026-11-01T01:02:03Z', attempted = '2026-11-01T00:02:03Z'
   mocks.tasks.mockResolvedValue(page([
    ...states.map((state, index) => ({ id: String(index + 1), state: 'succeeded', error_class: 'network', updated_at: updated, last_attempt_at: attempted,
     delivery: { state, operation: 'internal-key', message_id: '123', invite_link: 'https://t.me/+SECRET', error: 'TOKEN_SECRET' } })),
    { id: '8', state: 'unknown' }, { id: '9', state: 'unknown', delivery: { state: 'FUTURE_SECRET' } },
   ]))
   mocks.reviews.mockResolvedValue(page())
   const state = mountRecovery(locale)
   state.bot = '9'; await state.load(); await flush()
   for (const [index, status] of states.entries()) {
    expect(state.deliveryLabel(state.tasks[index].delivery)).toBe(membershipRecoveryMessages[locale].deliveryStates[status])
    expect(state.taskDetails(state.tasks[index])).toContain(`${membershipRecoveryMessages[locale].delivery}: ${membershipRecoveryMessages[locale].deliveryStates[status]}`)
   }
   expect(state.deliveryLabel(state.tasks[7].delivery)).toBe(membershipRecoveryMessages[locale].deliveryStates.unavailable)
   expect(state.deliveryLabel(state.tasks[8].delivery)).toBe(membershipRecoveryMessages[locale].deliveryStates.unavailable)
   expect(root.querySelector('[data-testid="delivery-state"]')).toBeNull()
   expect(state.taskDetails(state.tasks[0]).join(' ')).toContain(formatDate(updated))
   expect(state.taskDetails(state.tasks[0]).join(' ')).toContain(formatDate(attempted))
   for (const cell of root.querySelectorAll('[data-testid="task-state"]')) expect(cell.querySelectorAll('[data-slot="tooltip-trigger"]')).toHaveLength(2)
   for (const cell of root.querySelectorAll('[data-testid="task-attempt"]')) expect(cell.querySelectorAll('p')).toHaveLength(2)
   expect(root.textContent).toContain(membershipRecoveryMessages[locale].values.network)
   expect(root.textContent).not.toMatch(/SECRET|internal-key/)
  })
 }
 it('jumps each table directly and resets size without traversing cursors', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
  mocks.tasks.mockImplementation(({ page: target }) => Promise.resolve(page([{ id: `task-${target}` }])))
  mocks.reviews.mockResolvedValue(page())
  const state = mountRecovery(); state.bot = '9'
  await state.load(); await state.load(3, 'tasks'); await flush()
  expect(mocks.tasks).toHaveBeenCalledTimes(2); expect(mocks.reviews).toHaveBeenCalledTimes(1)
  expect(mocks.tasks).toHaveBeenLastCalledWith({ bot_id: '9', page: 3, page_size: 20 })
  expect(state.taskPagination.page).toBe(3); expect(state.reviewPagination.page).toBe(1)
  await state.load(1, 'tasks', 50)
  expect(state.taskPagination).toEqual({ page: 1, page_size: 50, total: 50 })
  for (const table of root.querySelectorAll('table')) {
   const container = table.closest('.rounded-xl')!
   expect(container.querySelector('input[type="number"]')).not.toBeNull()
   expect(container.querySelector('input[type="number"]')?.closest('.overflow-auto')).toBeNull()
  }
 })
 it('keeps rows and page on failure and permits retry', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: [], hasPermission: () => true })
  mocks.tasks.mockResolvedValueOnce(page([{ id: '1' }])).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(page([{ id: '2' }]))
  mocks.reviews.mockResolvedValue(page())
  const state = mountRecovery(); state.bot = '9'
  await state.load(); await state.load(2)
  expect(state.error).toBeTruthy(); expect(state.taskPagination.page).toBe(1); expect(state.tasks[0].id).toBe('1')
  await state.load(2); expect(state.tasks[0].id).toBe('2'); expect(state.taskPagination.page).toBe(2)
 })
 it('keeps the displayed page while loading and disables both pagers', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: true, permissions: [], hasPermission: () => true })
  mocks.tasks.mockResolvedValue(page([{ id: '1' }])); mocks.reviews.mockResolvedValue(page())
  const state = mountRecovery(); state.bot = '9'; await state.load()
  const request = deferred(); mocks.tasks.mockReturnValueOnce(request.promise)
  const pending = state.load(3, 'tasks'); await flush()
  expect(state.taskPagination.page).toBe(1); expect(state.tasks[0].id).toBe('1')
  for (const input of root.querySelectorAll<HTMLInputElement>('input[type="number"]')) expect(input.disabled).toBe(true)
  request.resolve({ data: { data: { items: [], total: 0 } } }); await pending; await flush()
  expect(root.querySelectorAll('input[type="number"]')).toHaveLength(2)
 })
 it('clears rows, dialog and cursors and ignores late reads after permissions change', async () => {
  mocks.auth = reactive({ token: 'first', isSuper: false, permissions: ['read'], hasPermission: () => mocks.auth.permissions.includes('read') })
  mocks.tasks.mockResolvedValueOnce(page([{ id: '1' }], '1')); mocks.reviews.mockResolvedValue(page())
  const state = mountRecovery(); state.bot = '9'; await state.load()
  const request = deferred(); mocks.tasks.mockReturnValueOnce(request.promise)
  const loading = state.load(2)
  state.pending = { row: { id: '1' }, action: 'review' }
  mocks.auth.permissions = []; await flush()
  expect(state.pending).toBeUndefined(); expect(state.taskPagination.page).toBe(1); expect(state.taskPagination.total).toBe(0)
  request.resolve(page([{ id: 'LATE_SECRET' }], '3')); await loading; await flush()
  expect(state.tasks).toEqual([]); expect(state.loading).toBe(false); expect(root.textContent).not.toContain('LATE_SECRET')
  await state.load(); expect(mocks.tasks).toHaveBeenCalledTimes(2)
 })
 for (const outcome of ['resolve', 'reject'] as const) {
  it(`ignores stale recovery ${outcome} after authorization changes`, async () => {
   mocks.auth = reactive({ token: 'first', isSuper: false, permissions: ['write'], hasPermission: () => mocks.auth.permissions.includes('write') })
   const request = deferred(); mocks.recover.mockReturnValue(request.promise)
   const state = mountRecovery(); state.bot = '9'; state.pending = { row: { id: '1' }, action: 'review' }
   const recovery = state.recover(); mocks.auth.permissions = []; await flush()
   expect(state.pending).toBeUndefined(); expect(state.saving).toBe(false)
   if (outcome === 'resolve') request.resolve({}); else request.reject(new Error('old authorization'))
   await recovery; await flush()
   expect(mocks.tasks).not.toHaveBeenCalled(); expect(mocks.reviews).not.toHaveBeenCalled()
   expect(state.success).toBe(false); expect(state.error).toBe('')
  })
 }
})
