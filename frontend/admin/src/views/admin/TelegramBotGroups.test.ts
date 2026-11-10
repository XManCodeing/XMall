import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import Groups from './TelegramBotGroups.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), modules: vi.fn(), get: vi.fn(), check: vi.fn(), attention: vi.fn(), denied: [] as string[] }))
vi.mock('./components/SupportPolicyDialog.vue', () => ({ default: {
  props: ['botID', 'botLabel'], emits: ['close'],
  template: '<div data-policy-dialog :data-bot="botID"><button @click="$emit(\'close\')">Close policy</button></div>',
} }))
vi.mock('@/composables/useMembershipAttention', () => ({ useMembershipAttention: mocks.attention }))
vi.mock('@/api/telegramGroups', () => ({ telegramGroupsAPI: mocks }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ hasPermission: (key: string) => !mocks.denied.includes(key) }) }))
vi.mock('./components/MembershipAccessRecovery.vue', () => ({ default: {
  props: ['mode', 'active'],
  template: '<div :data-mode="mode" :data-active="active"><input :aria-label="mode" /></div>',
} }))
vi.mock('@/composables/useTelegramBotInstances', async () => {
  const { ref } = await import('vue')
  return { useTelegramBotInstances: () => ({ instances: ref([]), refresh: vi.fn(), refreshError: ref(''), loading: ref(false), canList: ref(true) }) }
})
let app: App | undefined, root: HTMLDivElement | undefined
/** Drain mocked requests and Vue rendering. @returns Completion promise. */
async function flush() { for (let turn = 0; turn < 8; turn++) { await Promise.resolve(); await nextTick() } }
/** Check the shared product-table contract. @param table Rendered table. @returns Nothing. */
function expectProductTable(table: HTMLTableElement) {
  expect(table.parentElement?.classList.contains('overflow-auto')).toBe(true)
  expect(table.parentElement?.parentElement?.className).toBe('rounded-xl border border-border bg-card overflow-hidden')
  expect(table.querySelector('thead')?.className).toContain('bg-muted/40 text-xs uppercase text-muted-foreground')
  for (const header of table.querySelectorAll('th')) expect(header.className).toContain('px-6 py-3')
  for (const cell of table.querySelectorAll('tbody td')) expect(cell.className).toContain('px-6 py-4')
  expect(table.querySelector('tbody tr')?.classList.contains('hover:bg-muted/30')).toBe(true)
}
afterEach(() => { app?.unmount(); root?.remove(); mocks.denied = []; vi.clearAllMocks() })

describe('Telegram group product-table styling', () => {
  it('shows only group management without duplicated membership panels or polling', async () => {
    mocks.list.mockResolvedValue({ data: { data: { items: [], total: 0, page: 1 } } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Groups)
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    app.mount(root); await flush()
    expect(root.querySelector('[role="tablist"]')).toBeNull()
    expect(root.querySelector('[data-mode]')).toBeNull()
    expect(root.querySelectorAll('table')).toHaveLength(1)
    expect(root.textContent).not.toContain('telegramBot.groups.subtitle')
    const createButton = [...root.querySelectorAll('button')].find(button => button.textContent === 'telegramBot.groups.create')!
    expect(createButton.parentElement?.querySelector('button')?.textContent).not.toBe('telegramBot.groups.create')
    expect(createButton.classList.contains('ml-auto')).toBe(true)
    expect(mocks.attention).not.toHaveBeenCalled()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    mocks.list.mockRejectedValueOnce({ __notified: true, response: { status: 500 } })
    const searchButton = [...root.querySelectorAll('button')].find(button => button.textContent === 'admin.common.search')!
    searchButton.click(); await flush()
    expect(root.querySelector('p[role="alert"]')).toBeNull()
    expect(root.querySelector('td[colspan="9"]')).toBeNull()
  })
  it('renders reception switches only for support rows and refreshes sibling bindings after saving', async () => {
    const observation = { state: 'ready', route_version: 1, error_class: '' }
    const group = { id: 7, name: 'Support', purpose: 'support', bot_instance_id: 1, telegram_chat_id: '-1007', enabled: true, version: 1, route_version: 1, observation,
      fresh: true, fresh_until: new Date(Date.now() + 60000).toISOString(), checking: false, missing_capabilities: [] }
    mocks.list.mockResolvedValue({ data: { data: { items: [group, { ...group, id: 8 }, { ...group, id: 9, purpose: 'membership' }], total: 3, page: 1 } } })
    mocks.modules.mockResolvedValue({ data: { data: { items: [{ partition: 'support', bot_instance_id: 1, version: 1, target_id: 7, configured_enabled: true, effective_enabled: true }] } } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Groups)
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    const instance = app.mount(root); await flush()
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    expect(root.querySelectorAll('thead th')).toHaveLength(9)
    expect(root.querySelectorAll('[role="switch"]')).toHaveLength(2)
    const policyButtons = [...root.querySelectorAll<HTMLButtonElement>('button')].filter(button => button.textContent === 'telegramSupportSettings.settingsEntry')
    expect(policyButtons).toHaveLength(2)
    policyButtons[0]!.click(); await flush()
    expect(root.querySelector('[data-policy-dialog]')?.getAttribute('data-bot')).toBe('1')
    root.querySelector<HTMLButtonElement>('[data-policy-dialog] button')!.click(); await flush()
    policyButtons[1]!.click(); await flush()
    expect(root.querySelector('[data-policy-dialog]')?.getAttribute('data-bot')).toBe('1')
    root.querySelector<HTMLButtonElement>('[data-policy-dialog] button')!.click(); await flush()
    mocks.denied = ['GET:/admin/telegram-bot/support/settings/1']
    state.items = [...state.items]; await flush()
    expect([...root.querySelectorAll('button')].filter(button => button.textContent === 'telegramSupportSettings.settingsEntry')).toHaveLength(0)
    state.openPolicy(group); expect(state.policyBot).toBeNull()
    mocks.denied = []
    expect([...root.querySelectorAll('[role="switch"]')].map(control => control.getAttribute('aria-checked'))).toEqual(['true', 'false'])
    mocks.modules.mockResolvedValue({ data: { data: { items: [{ partition: 'support', bot_instance_id: 1, version: 2, target_id: 8, configured_enabled: true, effective_enabled: true }] } } })
    state.bindingRevision++; await flush()
    expect([...root.querySelectorAll('[role="switch"]')].map(control => control.getAttribute('aria-checked'))).toEqual(['false', 'true'])
  })
  it('omits the new-group name field while preserving existing editable names', async () => {
    mocks.list.mockResolvedValue({ data: { data: { items: [], total: 0, page: 1 } } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Groups)
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    const instance = app.mount(root); await flush()
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    state.openEditor(); await flush()
    expect(document.querySelector('#group-name')).toBeNull()
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('telegramBot.groups.autoNameHint')
    state.form.bot_instance_id = 2; state.form.telegram_chat_id = '123456'
    await flush(); expect(state.valid).toBe(true); expect(state.form.name).toBe('')
    state.editorOpen = false; await flush()
    state.openEditor({ id: 1, name: 'Existing custom name', purpose: 'membership', bot_instance_id: 2, telegram_chat_id: '-123456', enabled: false, version: 1, route_version: 1, observation: {} })
    await flush()
    expect(document.querySelector<HTMLInputElement>('#group-name')?.value).toBe('Existing custom name')
    state.form.name = ''; await flush(); expect(state.valid).toBe(false)
  })
  it('keeps the new-group switch clickable and explains incomplete input', async () => {
    mocks.list.mockResolvedValue({ data: { data: { items: [], total: 0, page: 1 } } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Groups)
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    const instance = app.mount(root); await flush()
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    state.openEditor(); await flush()
    const toggle = document.querySelector<HTMLButtonElement>('#group-enabled')!
    expect(toggle.disabled).toBe(false)
    toggle.click(); await flush()
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('telegramBot.groups.enableIncomplete')
  })
  it('does not expose membership panels when group access is denied', async () => {
    mocks.denied = ['GET:/admin/telegram-bot/groups']
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Groups)
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    app.mount(root); await flush()
    expect(root.querySelector('[role="tab"]')).toBeNull()
    expect(root.querySelector('[data-mode]')).toBeNull()
    expect(root.querySelector('table')).toBeNull()
    expect(root.textContent).toContain('telegramBot.groups.forbidden')
    expect(mocks.list).not.toHaveBeenCalled()
  })
  it('styles the list and both detail tables while retaining action guards and searches', async () => {
    const row = {
      id: 1, name: 'Group', purpose: 'membership', bot_instance_id: 2, telegram_chat_id: '-100123', enabled: false,
      version: 4, route_version: 3, fresh: false, fresh_until: '', missing_capabilities: [], affected_functions: [], checking: true,
      observation: { state: 'unknown', route_version: 3, error_class: '' },
      routes: [{ id: 1, route_version: 3, bot_instance_id: 2, telegram_chat_id: '-100123', effective_at: '', retired_at: '' }],
      operations: [{ id: 1, route_version: 3, actor_admin_id: 2, source: 'admin', state: 'unknown', error_class: '', started_at: '', finished_at: '' }],
    }
    mocks.list.mockResolvedValue({ data: { data: { items: [row], total: 1, page: 1 } } })
    mocks.get.mockResolvedValue({ data: { data: row } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Groups)
    app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} }))
    const instance = app.mount(root); await flush()
    const state = (instance.$ as unknown as { setupState: Record<string, any> }).setupState
    const table = root.querySelector('table')!
    expectProductTable(table)
    expect(root.textContent).not.toContain('telegramBot.groups.modules')
    expect(mocks.modules).not.toHaveBeenCalled()
    expect(table.querySelector('tbody td:first-child')?.textContent).toBe('Group')
    expect(table.querySelector('th:last-child')?.classList.contains('text-right')).toBe(true)
    expect(table.querySelector('td:last-child')?.classList.contains('text-right')).toBe(true)
    expect(table.querySelector('td:last-child > div')?.className).toBe('flex flex-wrap items-center justify-end gap-2')
    const actions = [...table.querySelectorAll<HTMLButtonElement>('td:last-child button')]
    expect(actions).toHaveLength(3)
    for (const action of actions) {
      expect(action.classList.contains('border-input')).toBe(true)
      expect(action.classList.contains('h-8')).toBe(true)
    }
    expect(actions[2]!.disabled).toBe(true)
    actions[2]!.click(); expect(mocks.check).not.toHaveBeenCalled()
    actions[0]!.click(); await flush()
    expect(mocks.get).toHaveBeenCalledWith(1)
    const detailTables = document.querySelectorAll<HTMLTableElement>('[role="dialog"] table')
    expect(detailTables).toHaveLength(2)
    detailTables.forEach(expectProductTable)
    state.detailOpen = false; await flush()
    state.botFilter = '2'; state.purposeFilter = 'support'; await flush()
    const search = [...root.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'admin.common.search')!
    search.click(); await flush()
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_instance_id: 2, purpose: 'support' })
    const reset = [...root.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'admin.common.reset')!
    reset.click(); await flush()
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 20, bot_instance_id: undefined, purpose: undefined })
    mocks.denied = ['GET:/admin/telegram-bot/groups/1', 'PATCH:/admin/telegram-bot/groups/1', 'POST:/admin/telegram-bot/groups/1/check']
    state.items = [{ ...row, name: 'Updated' }]; await flush()
    expect(table.querySelectorAll('td:last-child button')).toHaveLength(0)
  })
})
