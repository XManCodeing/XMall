import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import ProductCategoryField from './ProductCategoryField.vue'
import CategoryEditModal from './CategoryEditModal.vue'
const mocks = vi.hoisted(() => ({ list: vi.fn(), create: vi.fn(), update: vi.fn(), outerSubmit: vi.fn(), auth: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { getCategories: mocks.list, createCategory: mocks.create, updateCategory: mocks.update } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/components/admin/MediaPicker.vue', () => ({ default: { template: '<div />' } }))
let app: App, root: HTMLDivElement, field: any, category: any, draft: any, active: { value: boolean }
const original = { id: 1, parent_id: 0, name: { 'zh-CN': '原分类' }, slug: 'original' }
const created = { id: 2, parent_id: 1, name: { 'zh-CN': '新分类' }, slug: 'new' }
/** Drain Vue and mocked response microtasks. @returns Completion promise. */
async function flush() { for (let index = 0; index < 12; index++) { await Promise.resolve(); await nextTick() } }
/** Open the actual nested dialog and supply valid input. @returns Completion promise. */
async function openCategory() { field.creating = true; await flush(); category.form.name['zh-CN'] = '新分类'; category.form.slug = 'new' }
beforeEach(async () => {
  vi.clearAllMocks()
  mocks.auth = reactive({ token: 'one', isSuper: false, permissions: [], denied: [] as string[], hasPermission(value: string) { return !this.denied.includes(value) } })
  mocks.list.mockResolvedValue({ data: { data: [original] } }); mocks.create.mockResolvedValue({ data: { data: created } })
  draft = reactive({ category: 1, title: '商品草稿', price: 27 }); active = reactive({ value: true })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(defineComponent({ setup: () => () => h('form', { onSubmit: mocks.outerSubmit }, [h(ProductCategoryField, {
    modelValue: draft.category, categories: [original] as any, active: active.value,
    'onUpdate:modelValue': (value: number) => { draft.category = value },
    ref: (instance: any) => { if (instance) field = instance.$.setupState },
  })]) }))
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} }))
  app.mount(root); await flush()
  // Read the shared editor instance through the real component tree, not a substitute implementation.
  const child = (app as any)._instance.subTree.children[0].component.subTree.children.find((node: any) => node.type === CategoryEditModal)
  category = child.component.setupState
})
afterEach(() => { app?.unmount(); root?.remove() })

describe('shared inline category creation', () => {
  it('refreshes and selects the created leaf without submitting or replacing product draft', async () => {
    await openCategory(); category.form.parent_id = 1
    mocks.list.mockResolvedValueOnce({ data: { data: [original, created] } })
    document.querySelector('form[data-category-form]')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    expect(mocks.create).toHaveBeenCalledTimes(1); expect(mocks.outerSubmit).not.toHaveBeenCalled()
    expect(mocks.create.mock.calls[0]![0]).toMatchObject({ parent_id: 1, slug: 'new', name: { 'zh-CN': '新分类' } })
    expect(draft).toEqual({ category: 2, title: '商品草稿', price: 27 }); expect(field.creating).toBe(false)
  })
  it('keeps product and category drafts on failure and supports cancel without product submission', async () => {
    await openCategory(); mocks.create.mockRejectedValueOnce(new Error('offline'))
    await category.submit(); await flush()
    expect(category.form.slug).toBe('new'); expect(category.error).toBeTruthy(); expect(draft.category).toBe(1)
    category.close(); await flush(); expect(field.creating).toBe(false)
    expect(draft.title).toBe('商品草稿'); expect(mocks.outerSubmit).not.toHaveBeenCalled()
  })
  it('retries failed list refresh without creating a duplicate category', async () => {
    await openCategory(); mocks.list.mockRejectedValueOnce(new Error('offline'))
    await category.submit(); await flush()
    expect(draft.category).toBe(1); expect(field.pendingID).toBe(2); expect(field.error).toBeTruthy()
    mocks.list.mockResolvedValueOnce({ data: { data: [original, created] } })
    await field.refresh(); expect(draft.category).toBe(2); expect(mocks.create).toHaveBeenCalledTimes(1)
  })
  it.each(['GET', 'POST'])('requires independent %s permission at open and submission', async method => {
    mocks.auth.denied = [`${method}:/admin/categories`]; await flush()
    expect(field.canCreate).toBe(false)
    await openCategory(); await category.submit(); expect(mocks.create).not.toHaveBeenCalled()
    if (method === 'GET') expect(mocks.list).not.toHaveBeenCalled()
  })
  it('blocks repeated submission and ignores successful responses after cancellation', async () => {
    await openCategory(); let resolve!: (value: unknown) => void
    mocks.create.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const pending = category.submit(); await category.submit(); expect(mocks.create).toHaveBeenCalledTimes(1)
    category.close(); await flush(); resolve({ data: { data: created } }); await pending; await flush()
    expect(draft.category).toBe(1); expect(mocks.list).toHaveBeenCalledTimes(1)
  })
  it('invalidates pending refresh when switching accounts', async () => {
    await openCategory(); let resolve!: (value: unknown) => void
    mocks.list.mockReturnValueOnce(new Promise(done => { resolve = done }))
    await category.submit(); await flush()
    mocks.auth.token = 'two'; await flush()
    resolve({ data: { data: [original, created] } }); await flush()
    expect(draft.category).toBe(1); expect(field.creating).toBe(false); expect(field.rows).toEqual([])
  })
  it('does not auto-select a new category that now has children', async () => {
    await openCategory(); mocks.list.mockResolvedValueOnce({ data: { data: [original, created, { ...created, id: 3, parent_id: 2 }] } })
    await category.submit(); await flush(); expect(draft.category).toBe(1); expect(field.error).toBeTruthy()
  })
})
