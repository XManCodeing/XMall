import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, ref, type App } from 'vue'
import { useRecoveryBotRoutes, validBotRoutes } from './useRecoveryBotRoutes'
const mocks = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/telegramGroups', () => ({ telegramGroupsAPI: mocks }))
let app: App
/** Mount source-scoped mapping controller. @returns Controller and input refs. */
function setup() {
  const source = ref('9007199254740993'), allowed = ref(true)
  let state!: ReturnType<typeof useRecoveryBotRoutes>
  app = createApp({ setup() { state = useRecoveryBotRoutes(source, allowed); return () => null } }); app.mount(document.createElement('div'))
  return { source, allowed, state }
}
const group = { id: '9007199254740995', bot_instance_id: '9007199254740993', purpose: 'membership', telegram_chat_id: '-1001', name: 'member' }
/** Create a real group-list response shape. @param items Raw options. @returns API response. */
function response(items: unknown[]) { return { data: { data: { items, total: 60 } } } }
afterEach(() => { app?.unmount(); vi.resetAllMocks() })

describe('optional cross-Bot route intent', () => {
  it('uses lossless source filter and accepts only current source membership options', async () => {
    const { state } = setup(); mocks.list.mockResolvedValue(response([group, { ...group, id: '3', purpose: 'support' }, { ...group, id: '4', bot_instance_id: '4' }, { ...group, id: 9007199254740995 }]))
    await state.load(); expect(mocks.list).toHaveBeenCalledWith({ bot_instance_id: group.bot_instance_id, purpose: 'membership', page: 1, page_size: 20 })
    expect(state.options.value).toEqual([{ id: group.id, name: 'member', chat: '-1001' }]); state.add('3'); expect(state.routes.value).toEqual([])
    expect(state.payload()).toEqual({}); state.add(group.id); expect(state.valid.value).toBe(false)
    state.routes.value[0]!.chat_id = '-9007199254740993'; expect(state.valid.value).toBe(true)
    Object.assign(state.routes.value[0]!, { version: '999', snapshot: { malicious: true } })
    expect(state.payload()).toEqual({ routes: [{ target_id: group.id, chat_id: '-9007199254740993' }] })
    state.remove(group.id); expect(state.payload()).toEqual({})
  })
  it('rejects duplicates, unknown targets, invalid int64 and over 100 mappings', () => {
    const known = Array.from({ length: 101 }, (_, index) => ({ id: String(index + 1), name: '', chat: String(-index - 1) }))
    const routes = known.map(item => ({ target_id: item.id, chat_id: item.chat }))
    expect(validBotRoutes(routes.slice(0, 100), known)).toBe(true); expect(validBotRoutes(routes, known)).toBe(false)
    expect(validBotRoutes([{ target_id: '999', chat_id: '-2' }], known)).toBe(false)
    expect(validBotRoutes([routes[0]!, routes[0]!], known)).toBe(false)
    expect(validBotRoutes([routes[0]!, { target_id: '2', chat_id: '-1' }], known)).toBe(false)
    for (const chat_id of ['-0', '0', '1', '-01', '-1e3', '-9223372036854775809', '-1\n', ' -1']) expect(validBotRoutes([{ target_id: '1', chat_id }], known)).toBe(false)
    expect(validBotRoutes([{ target_id: '1', chat_id: '-9223372036854775808' }], known)).toBe(true)
  })
  it('invalidates source A-B-A late responses and clears selected mappings', async () => {
    const { state, source } = setup(); let finish!: (value: unknown) => void
    mocks.list.mockResolvedValueOnce(response([group])); await state.load(); state.add(group.id); state.routes.value[0]!.chat_id = '-2'
    mocks.list.mockImplementationOnce(() => new Promise(resolve => { finish = resolve })); const old = state.load(2)
    source.value = '2'; source.value = group.bot_instance_id; expect(state.routes.value).toEqual([])
    mocks.list.mockResolvedValueOnce(response([{ ...group, id: '8' }])); await state.load(); finish(response([group])); await old
    expect(state.options.value[0]?.id).toBe('8'); expect(state.loading.value).toBe(false)
  })
  it('retains only selected options across bounded pages and denies revoked permission', async () => {
    const { state, allowed } = setup(); mocks.list.mockResolvedValue(response([group])); await state.load(); state.add(group.id); state.routes.value[0]!.chat_id = '-2'
    mocks.list.mockResolvedValue(response([{ ...group, id: '9' }])); await state.load(2)
    expect(state.options.value).toHaveLength(1); expect(state.valid.value).toBe(true); allowed.value = false
    expect(state.routes.value).toEqual([]); await state.load(); expect(mocks.list).toHaveBeenCalledTimes(2)
  })
})
