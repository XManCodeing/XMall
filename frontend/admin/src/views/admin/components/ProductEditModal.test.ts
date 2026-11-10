import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, reactive, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import ProductEditModal from './ProductEditModal.vue'
import ProductCategoryField from './ProductCategoryField.vue'
const mocks = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), confirm: vi.fn(), create: vi.fn(), update: vi.fn(), categories: vi.fn(), payments: vi.fn(), auth: null as any }))
vi.mock('@/api/admin', () => ({ adminAPI: { getProducts: mocks.list, getProduct: mocks.get, createProduct: mocks.create, updateProduct: mocks.update, getCategories: mocks.categories, getPaymentChannels: mocks.payments } }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => mocks.auth }))
vi.mock('@/components/admin/MediaPicker.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/RichEditor.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/utils/confirm', () => ({ confirmAction: mocks.confirm }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn() }))
let app: App, root: HTMLDivElement, state: any, props: any
const original = { id: 1, parent_id: 0, name: { 'zh-CN': '分类' }, slug: 'first' }
/** Drain API and rendering work. @returns Completion promise. */
async function flush() { for (let index = 0; index < 12; index++) { await Promise.resolve(); await nextTick() } }
/** Find a real child instance through rendered component/fragment nodes. @param node Render node. @returns Category field state or undefined. */
function categoryState(node: any): any {
  if (node?.type === ProductCategoryField) return node.component.setupState
  if (node?.component) { const result = categoryState(node.component.subTree); if (result) return result }
  if (Array.isArray(node?.children)) for (const child of node.children) { const result = categoryState(child); if (result) return result }
}
beforeEach(async () => {
  vi.clearAllMocks()
  mocks.auth = reactive({ token: 'one', isSuper: true, permissions: [], hasPermission: () => true })
  mocks.payments.mockResolvedValue({ data: { data: [] } })
  mocks.get.mockResolvedValue({ data: { data: { id: 7, category_id: 1, title: { 'zh-CN': '已有商品' }, slug: 'existing', price_amount: 12, skus: [] } } })
  props = reactive({ modelValue: false, productId: null, categories: [original], siteCurrency: 'CNY', supportedLocales: ['zh-CN'] })
  root = document.createElement('div'); document.body.append(root)
  app = createApp(defineComponent({ setup: () => () => h(ProductEditModal, { ...props, 'onUpdate:modelValue': (value: boolean) => { props.modelValue = value }, ref: (instance: any) => { if (instance) state = instance.$.setupState } }) }))
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false, messages: {} })); app.mount(root)
  await flush()
})
afterEach(() => { app?.unmount(); root?.remove() })
describe('website product shared category integration', () => {
  it.each([null, 7])('keeps unknown ordinary-product drafts locked across close and reopen for id %s', async id => {
    props.productId = id; props.modelValue = true; await flush()
    Object.assign(state.form, { title: { 'zh-CN': '保留草稿' }, slug: 'exact', category_id: 1, price_amount: 20 })
    const write = id ? mocks.update : mocks.create
    write.mockRejectedValue(Object.assign(new Error('请求超时'), { resultUnknown: true }))
    await state.handleSubmit(); await flush()
    expect(state.recovery.unknown.value).toBe(true)
    mocks.confirm.mockResolvedValue(false); await state.closeModal(); expect(props.modelValue).toBe(true)
    mocks.confirm.mockResolvedValue(true); await state.closeModal(); await flush()
    props.productId = null; props.modelValue = true; await flush()
    expect(state.form.title['zh-CN']).toBe('保留草稿')
    expect(state.form.slug).toBe('exact')
    await state.handleSubmit(); expect(write).toHaveBeenCalledTimes(1)
    mocks.list.mockResolvedValue({ data: { data: [{ id: 8, slug: 'exact-other' }] } })
    await state.recovery.check()
    if (!id) expect(state.recovery.candidate.value).toBeUndefined()
    expect(write).toHaveBeenCalledTimes(1)
  })
  it('opens a confirmed candidate separately without discarding the uncertain ordinary draft', async () => {
    props.modelValue = true; await flush()
    Object.assign(state.form, { title: { 'zh-CN': '保留草稿' }, slug: 'exact', category_id: 1, price_amount: 20 })
    mocks.create.mockRejectedValue(Object.assign(new Error('offline'), { resultUnknown: true }))
    await state.handleSubmit()
    mocks.list.mockResolvedValue({ data: { data: [{ id: 9, slug: 'exact' }] } })
    await state.recovery.check()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    mocks.confirm.mockResolvedValue(false); await state.recovery.openCandidate(); expect(open).not.toHaveBeenCalled()
    mocks.confirm.mockResolvedValue(true); await state.recovery.openCandidate()
    expect(open).toHaveBeenCalledWith(expect.stringContaining('/products?product_id=9'), '_blank', 'noopener,noreferrer')
    expect(state.form.title['zh-CN']).toBe('保留草稿'); expect(state.recovery.unknown.value).toBe(true)
    expect(mocks.create).toHaveBeenCalledTimes(1)
    open.mockRestore()
  })
  it.each([null, 7])('selects refreshed category while preserving new/edit draft for id %s', async id => {
    props.productId = id; props.modelValue = true; await flush()
    state.form.title['zh-CN'] = '未保存草稿'; state.form.slug = 'manual-slug'; state.form.price_amount = 97
    const field = categoryState((app as any)._instance.subTree)
    const added = { ...original, id: 2, slug: 'second' }
    mocks.categories.mockResolvedValue({ data: { data: [original, added] } })
    await field.created(added); await flush()
    expect(state.form.category_id).toBe(2); expect(state.form.title['zh-CN']).toBe('未保存草稿')
    expect(state.form.price_amount).toBe(97); expect(state.form.slug).toBe('manual-slug')
    expect(state.isProductCategorySelectable(2)).toBe(true)
    expect(mocks.get).toHaveBeenCalledTimes(id ? 1 : 0)
    expect(mocks.create).not.toHaveBeenCalled(); expect(mocks.update).not.toHaveBeenCalled()
  })
  it('clears drafts and invalidates product detail on account replacement', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockReturnValueOnce(new Promise(done => { resolve = done }))
    props.productId = 7; props.modelValue = true; await flush()
    mocks.auth.token = 'two'; await flush()
    resolve({ data: { data: { id: 7, title: { 'zh-CN': '旧账号商品' }, category_id: 1 } } }); await flush()
    expect(props.modelValue).toBe(false); expect(state.form.title['zh-CN']).toBe('')
  })
})
