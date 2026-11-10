import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import TelegramMembershipProducts from './TelegramMembershipProducts.vue'
import i18n from '@/i18n'

const mocks = vi.hoisted(() => ({ list: vi.fn(), share: vi.fn(), setActive: vi.fn(), remove: vi.fn(), confirm: vi.fn(), permission: vi.fn(), copy: vi.fn(), success: vi.fn(), failure: vi.fn() }))
vi.mock('@/utils/clipboard', () => ({ copyText: mocks.copy }))
vi.mock('@/utils/notify', () => ({ notifySuccess: mocks.success, notifyErrorOnce: mocks.failure }))
vi.mock('@/api/telegramMembership', () => ({
  membershipBase: '/admin/telegram-bot/membership',
  telegramMembershipAPI: { products: mocks.list, share: mocks.share, setActive: mocks.setActive, remove: mocks.remove },
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => ({ token: 'test', isSuper: false, permissions: [], hasPermission: mocks.permission }) }))
vi.mock('@/utils/confirm', () => ({ confirmAction: mocks.confirm }))
vi.mock('./components/MembershipProductEditModal.vue', () => ({ default: { template: '<div />' } }))
const shortShareLink = 'https://t.me/bound_shop_bot?start=K7m2Qx9Ba4Nz'
let app: App, root: HTMLDivElement

/** @param active Server-side listing flag. @param version Optimistic-lock version. @returns Fixture DTO. */
function product(active = false, version = 3) {
  return { id: 7, slug: 'membership-demo', is_active: active, title: { 'zh-CN': '会员' },
    share_url: active ? shortShareLink : '', share_unavailable_reason: active ? '' : 'unavailable',
    skus: [{ id: 9, sku_code: 'DEFAULT', price_amount: 28 }],
    membership: { product_id: 7, version, enabled: active, quantity: 1, target_ids: [2, 5], target_names: { '2': '会员交流群', '5': '课程群' } as Record<string, string>,
      terms: [{ sku_id: 9, sku_code: 'DEFAULT', kind: 'months', months: 5 }] } }
}
/** @param rows Product fixtures. @returns Backend list envelope. */
function response(rows = [product()]) {
  return { data: { data: rows, status_code: 0, msg: 'success', pagination: { page: 1, page_size: 10, total: rows.length, total_page: 1 } } }
}
/** @returns Settled Vue render and promise queue. */
async function settle() { for (let turn = 0; turn < 12; turn++) { await Promise.resolve(); await nextTick() } }
/** @returns Mounted list using real translations and theme-aware UI components. */
async function mount() {
  root = document.createElement('div'); document.body.append(root)
  app = createApp(TelegramMembershipProducts)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', missingWarn: false, fallbackWarn: false,
    messages: { 'zh-CN': i18n.global.getLocaleMessage('zh-CN') } }))
  app.mount(root)
  await settle()
}
/** @param label Exact button label. @returns Matching button if authorized and rendered. */
function button(label: string) { return Array.from(root.querySelectorAll('button')).find(element => element.textContent?.trim() === label) }

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe = vi.fn(); unobserve = vi.fn(); disconnect = vi.fn() })
  mocks.permission.mockReturnValue(true)
  mocks.list.mockResolvedValue(response())
  mocks.setActive.mockResolvedValue({ data: { status_code: 0, msg: 'success' } })
  mocks.remove.mockResolvedValue({ data: { status_code: 0, msg: 'success' } })
  mocks.confirm.mockResolvedValue(true)
})
afterEach(() => { app?.unmount(); root?.remove(); vi.unstubAllGlobals() })

describe('membership product share links', () => {
  it('copies the associated bot deep link without requiring bot/group permissions or writing orders', async () => {
    mocks.permission.mockImplementation((key: string) => key === 'GET:/admin/telegram-bot/membership/products')
    mocks.list.mockResolvedValue(response([product(true)]))
    await mount()
    expect(Array.from(root.querySelectorAll('th')).map(cell => cell.textContent)).toContain('分享')
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    expect(share.querySelector('svg')).not.toBeNull()
    expect(share.disabled).toBe(false)
    share.click(); await settle()
    expect(mocks.copy).toHaveBeenCalledExactlyOnceWith(shortShareLink)
    expect(mocks.success).toHaveBeenCalledWith('机器人商品链接已复制')
    expect(mocks.setActive).not.toHaveBeenCalled()
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it.each(['https://example.com/product/7', `https://t.me/bound_shop_bot?start=p1${'A'.repeat(49)}`, 'https://t.me/bound_shop_bot?start=product_7', `${shortShareLink}&other=1`, shortShareLink.slice(0, -1), `${shortShareLink}A`, ...['_', '-', '+', '/', '=', '%'].map(char => shortShareLink.slice(0, -1) + char)])('disables missing or invalid share links: %s', async link => {
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: link }]))
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    expect(share.disabled).toBe(true)
    share.click(); await settle()
    expect(mocks.copy).not.toHaveBeenCalled()
  })

  it('generates a missing code only on click and reuses it for subsequent copies', async () => {
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    mocks.share.mockResolvedValue({ data: { data: { share_url: shortShareLink, share_unavailable_reason: '' } } })
    await mount()
    expect(mocks.share).not.toHaveBeenCalled()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    expect(share.disabled).toBe(false)
    share.click(); share.click(); await settle()
    expect(mocks.share).toHaveBeenCalledExactlyOnceWith(7)
    expect(mocks.copy).toHaveBeenCalledExactlyOnceWith(shortShareLink)
    share.click(); await settle()
    expect(mocks.share).toHaveBeenCalledTimes(1)
    expect(mocks.copy).toHaveBeenCalledTimes(2)
  })

  it('requires the dedicated permission only when a code must be generated', async () => {
    mocks.permission.mockImplementation((key: string) => key.startsWith('GET:'))
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    expect(share.disabled).toBe(true)
    expect(share.parentElement?.title).toBe('无分享短码生成权限')
    share.click(); await settle()
    expect(mocks.share).not.toHaveBeenCalled()
  })

  it.each([null, { share_url: 'https://example.com', share_unavailable_reason: '' }])('rejects invalid share API response: %s', async result => {
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    mocks.share.mockResolvedValue({ data: { data: result } })
    await mount()
    root.querySelector<HTMLButtonElement>('[data-product-share]')!.click(); await settle()
    expect(mocks.copy).not.toHaveBeenCalled()
    expect(mocks.success).not.toHaveBeenCalled()
    expect(mocks.failure).toHaveBeenCalledWith(expect.any(Error), '分享链接生成失败，请重试')
  })

  it('reports generation failures and permits safe idempotent retry', async () => {
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    mocks.share.mockRejectedValue(new Error('network failure'))
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    share.click(); await settle()
    expect(mocks.copy).not.toHaveBeenCalled()
    expect(share.disabled).toBe(false)
    share.click(); await settle()
    expect(mocks.share).toHaveBeenCalledTimes(2)
  })

  it('keeps the persisted code after clipboard failure so retry does not regenerate it', async () => {
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    mocks.share.mockResolvedValue({ data: { data: { share_url: shortShareLink, share_unavailable_reason: '' } } })
    mocks.copy.mockRejectedValueOnce(new Error('Clipboard blocked')).mockResolvedValue(undefined)
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    share.click(); await settle()
    expect(mocks.success).not.toHaveBeenCalled()
    share.click(); await settle()
    expect(mocks.share).toHaveBeenCalledTimes(1)
    expect(mocks.copy).toHaveBeenCalledTimes(2)
    expect(mocks.success).toHaveBeenCalledTimes(1)
  })

  it('renders a newly unavailable share response without copying an empty URL', async () => {
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    mocks.share.mockResolvedValue({ data: { data: { share_url: '', share_unavailable_reason: 'bot_unavailable' } } })
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    share.click(); await settle()
    expect(share.disabled).toBe(true)
    expect(mocks.copy).not.toHaveBeenCalled()
    expect(mocks.failure).toHaveBeenCalledWith(expect.any(Error), i18n.global.t('telegramMembership.shareReasons.bot_unavailable'))
  })

  it('does not copy an obsolete generation response after the list reloads', async () => {
    let resolve!: (value: unknown) => void
    mocks.list.mockResolvedValue(response([{ ...product(true), share_url: '' }]))
    mocks.share.mockReturnValue(new Promise(done => { resolve = done }))
    await mount()
    root.querySelector<HTMLButtonElement>('[data-product-share]')!.click(); await settle()
    button('搜索')!.click(); await settle()
    resolve({ data: { data: { share_url: shortShareLink, share_unavailable_reason: '' } } }); await settle()
    expect(mocks.copy).not.toHaveBeenCalled()
    expect(mocks.success).not.toHaveBeenCalled()
  })

  it.each(['unavailable', 'missing_binding', 'mixed_bots', 'bot_unavailable'])('shows a translated unavailable reason: %s', async reason => {
    mocks.list.mockResolvedValue(response([{ ...product(), share_unavailable_reason: reason }]))
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    expect(share.disabled).toBe(true)
    expect(share.parentElement?.title).toBe(i18n.global.t(`telegramMembership.shareReasons.${reason}`))
  })

  it('blocks repeated copies while pending and reports clipboard failure without success', async () => {
    let reject!: (failure: Error) => void
    mocks.copy.mockReturnValue(new Promise<void>((_resolve, rejectCopy) => { reject = rejectCopy }))
    mocks.list.mockResolvedValue(response([product(true)]))
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    share.click(); share.click(); await settle()
    expect(mocks.copy).toHaveBeenCalledTimes(1)
    expect(share.disabled).toBe(true)
    const failure = new Error('Clipboard denied')
    reject(failure); await settle()
    expect(mocks.failure).toHaveBeenCalledWith(failure, i18n.global.t('admin.common.copyFailed'))
    expect(mocks.success).not.toHaveBeenCalled()
    expect(share.disabled).toBe(false)
  })

  it.each([false, true])('does not copy an unavailable product even if a stale link exists: membershipDisabled=%s', async membershipDisabled => {
    const row = product(true)
    if (membershipDisabled) row.membership.enabled = false
    else row.is_active = false
    mocks.list.mockResolvedValue(response([row]))
    await mount()
    const share = root.querySelector<HTMLButtonElement>('[data-product-share]')!
    expect(share.disabled).toBe(true)
    share.click(); await settle()
    expect(mocks.copy).not.toHaveBeenCalled()
  })

  it('updates the empty table span for the added share column', async () => {
    mocks.list.mockResolvedValue(response([]))
    await mount()
    expect(root.querySelector('tbody td')?.getAttribute('colspan')).toBe('7')
  })
})

describe('membership product list response contract', () => {
  it('searches and resets on page one without a standalone refresh button', async () => {
    await mount()
    const input = root.querySelector<HTMLInputElement>('#membership-products-search')!
    input.value = 'vip'; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    button('搜索')!.click(); await settle()
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 10, search: 'vip' })
    button('搜索')!.click(); await settle()
    expect(mocks.list).toHaveBeenCalledTimes(3)
    button('重置')!.click(); await settle()
    expect(input.value).toBe('')
    expect(mocks.list).toHaveBeenLastCalledWith({ page: 1, page_size: 10, search: '' })
    expect(button('刷新')).toBeUndefined()
    expect(button('搜索')?.classList.contains('border-input')).toBe(true)
  })
  it('renders DTO target IDs, SKU and clickable state without standalone listing actions', async () => {
    await mount()
    expect(root.textContent).toContain('会员')
    expect(root.textContent).toContain('会员交流群、课程群')
    expect(root.querySelector('[data-price-summary]')?.getAttribute('aria-label')).toContain('DEFAULT')
    expect(root.textContent).toContain('已下架')
    expect(root.textContent).not.toContain('会员销售尚未开放')
    expect(button('上架')).toBeUndefined()
    expect(button('下架')).toBeUndefined()
    expect(button('已下架')?.disabled).toBe(false)
    expect(button('已下架')?.title).toBe(String(i18n.global.t('admin.products.status.clickToActivate')))
    expect(mocks.setActive).not.toHaveBeenCalled()
    expect(Array.from(root.querySelectorAll('th')).map(cell => cell.textContent)).toContain('名称')
    expect(root.querySelector('thead')?.textContent).not.toContain('*')
  })

  it('shows bounded group names with full tooltip and no configuration-version column', async () => {
    const row = product(true)
    const longName = '<b>会员群</b>'.repeat(40)
    row.membership.target_names = { '2': longName }
    mocks.list.mockResolvedValue(response([row]))
    await mount()
    const group = root.querySelector<HTMLElement>('[data-group-names]')!
    expect(group.textContent).toBe(`${longName}、#5`)
    expect(group.classList.contains('truncate')).toBe(true)
    expect(group.classList.contains('w-48')).toBe(true)
    expect(group.classList.contains('rounded-full')).toBe(false)
    expect(group.classList.contains('border')).toBe(false)
    expect(group.querySelector('b')).toBeNull()
    expect(root.querySelectorAll('th')).toHaveLength(7)
    expect(root.querySelector('th')?.textContent).toBe(i18n.global.t('admin.products.table.name'))
    expect(Array.from(root.querySelectorAll('th')).map(cell => cell.textContent)).not.toContain(i18n.global.t('admin.products.table.id'))
    expect(root.querySelector('thead')?.textContent).not.toContain(i18n.global.t('telegramMembership.configVersion'))
    group.focus(); await settle()
    expect(document.body.textContent).toContain(longName)
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('keeps many SKUs in a single bounded summary with complete accessible details', async () => {
    const row = product(true)
    row.skus = Array.from({ length: 20 }, (_, index) => ({ id: index + 1, sku_code: `SKU-${index}`, price_amount: index + 10 }))
    row.membership.terms = row.skus.map(sku => ({ sku_id: sku.id, sku_code: sku.sku_code, kind: sku.id === 20 ? 'permanent' : 'months', months: 5 }))
    row.title['zh-CN'] = '很长的会员商品名称'.repeat(20)
    row.slug = 'long-slug-'.repeat(50)
    mocks.list.mockResolvedValue(response([row]))
    await mount()
    const price = root.querySelector<HTMLElement>('[data-price-summary]')!
    const term = root.querySelector<HTMLElement>('[data-term-summary]')!
    expect(price.textContent).toBe('10 – 29')
    expect(term.textContent).toBe(`5 ${i18n.global.t('telegramMembership.months')} / ${i18n.global.t('telegramMembership.permanent')}`)
    for (const summary of [price, term]) {
      expect(summary.classList.contains('truncate')).toBe(true)
      expect(summary.classList.contains('w-40')).toBe(true)
      expect(summary.children.length).toBe(0)
      expect(summary.getAttribute('aria-label')).toContain('SKU-19 · 29')
    }
    expect(root.querySelector('tbody td:nth-child(1) > div')?.classList.contains('h-12')).toBe(true)
    expect(root.querySelector('tbody td:nth-child(1) [title]')?.classList.contains('truncate')).toBe(true)
    price.focus(); await settle()
    expect(document.body.textContent).toContain('SKU-19 · 29')
  })

  it('renders missing prices and terms without fabricating zero or expanding the row', async () => {
    const row = product()
    row.skus = []; row.membership.terms = []
    mocks.list.mockResolvedValue(response([row]))
    await mount()
    expect(root.querySelector('[data-price-summary]')?.textContent).toBe('—')
    expect(root.querySelector('[data-term-summary]')?.textContent).toBe('—')
  })

  it('uses the existing product table spacing, identity block and button variants', async () => {
    await mount()
    expect(root.querySelector('thead')?.classList.contains('bg-muted/40')).toBe(true)
    expect(root.querySelector('tbody td')?.classList.contains('px-6')).toBe(true)
    expect(root.textContent).toContain('membership-demo')
    expect(root.textContent).toContain(String(i18n.global.t('admin.common.noImage')))
    expect(button('编辑')?.classList.contains('border-input')).toBe(true)
    expect(button('删除')?.classList.contains('bg-destructive')).toBe(true)
    expect(root.querySelectorAll('th')[0]?.classList.contains('min-w-[320px]')).toBe(true)
    expect(root.querySelector('tbody td:nth-child(1) > div')?.classList.contains('min-w-[320px]')).toBe(true)
    expect(root.querySelector('tbody td:nth-child(2) .font-mono.text-foreground')?.textContent).toBe('28')
    expect(button('已下架')?.classList.contains('bg-muted/30')).toBe(true)
    expect(button('已下架')?.classList.contains('font-semibold')).toBe(false)
    expect(button('已下架')?.classList.contains('rounded-md')).toBe(false)
    expect(root.querySelector('tbody')?.textContent).not.toContain('会员销售尚未开放')
    expect(root.querySelector('thead')?.textContent).not.toContain('必选')
  })

  it.each([true, false])('uses independent membership PATCH and DELETE permissions for active=%s', async active => {
    mocks.permission.mockImplementation((key: string) => key.startsWith('GET:') || key.startsWith('PUT:') || key.includes('/admin/products'))
    mocks.list.mockResolvedValue(response([product(active)]))
    await mount()
    expect(button('编辑')).toBeDefined()
    expect(button(active ? '已上架' : '已下架')?.disabled).toBe(true)
    button(active ? '已上架' : '已下架')?.click(); await settle()
    expect(mocks.setActive).not.toHaveBeenCalled()
    expect(button('删除')).toBeUndefined()
    expect(mocks.permission).toHaveBeenCalledWith('PATCH:/admin/telegram-bot/membership/products/7')
    expect(mocks.permission).toHaveBeenCalledWith('DELETE:/admin/telegram-bot/membership/products/7')
  })

  it('cancels deletion without writing or refreshing', async () => {
    mocks.confirm.mockResolvedValue(false)
    await mount()
    button('删除')?.click(); await settle()
    expect(mocks.confirm).toHaveBeenCalledWith(expect.objectContaining({ variant: 'destructive', description: expect.stringContaining('ID：7') }))
    expect(mocks.remove).not.toHaveBeenCalled()
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('locks double clicks during confirmation and deletes with the captured version', async () => {
    let confirm!: (value: boolean) => void
    mocks.confirm.mockReturnValue(new Promise<boolean>(resolve => { confirm = resolve }))
    await mount()
    const remove = button('删除')!
    remove.click(); remove.click(); await settle()
    expect(mocks.confirm).toHaveBeenCalledTimes(1)
    mocks.list.mockResolvedValue(response([]))
    confirm(true); await settle()
    expect(mocks.remove).toHaveBeenCalledExactlyOnceWith(7, 3)
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(root.textContent).not.toContain('DEFAULT')
  })

  it('unlists with expected version then renders only the refreshed server state', async () => {
    mocks.list.mockResolvedValueOnce(response([product(true)])).mockResolvedValue(response([product(false, 4)]))
    await mount()
    expect(root.textContent).toContain('已上架')
    const deactivate = button('已上架')!
    deactivate.click(); deactivate.click(); await settle()
    expect(mocks.setActive).toHaveBeenCalledExactlyOnceWith(7, false, 3)
    expect(root.textContent).toContain('已下架')
    expect(button('上架')).toBeUndefined()
    expect(button('下架')).toBeUndefined()
    expect(button('已下架')?.disabled).toBe(false)
  })

  it('lists through the status bubble once, then renders the authoritative version', async () => {
    mocks.list.mockResolvedValueOnce(response()).mockResolvedValue(response([product(true, 4)]))
    await mount()
    const activate = button('已下架')!
    activate.click(); activate.click(); await settle()
    expect(mocks.setActive).toHaveBeenCalledExactlyOnceWith(7, true, 3)
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(button('已上架')?.disabled).toBe(false)
    expect(button('上架')).toBeUndefined()
  })

  it('keeps the product unlisted and displays a rejected capability check', async () => {
    mocks.setActive.mockRejectedValue({ response: { status: 400, data: { msg: '群权限已过期，请重新检测' } } })
    await mount()
    button('已下架')?.click(); await settle()
    expect(root.querySelector('[role="alert"]')).toBeNull()
    expect(mocks.failure).toHaveBeenCalledTimes(1)
    expect(button('已下架')?.disabled).toBe(false)
    expect(button('已上架')).toBeUndefined()
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it.each([409, undefined])('blocks replay after a conflicted or uncertain activation (%s)', async status => {
    mocks.setActive.mockRejectedValue(status ? { response: { status, data: { msg: '版本冲突' } } } : new TypeError('Failed to fetch'))
    await mount()
    button('已下架')?.click(); await settle()
    expect(button('已下架')?.disabled).toBe(true)
    button('已下架')?.click(); await settle()
    expect(mocks.setActive).toHaveBeenCalledTimes(1)
  })

  it('disables an unknown state rather than guessing an activation', async () => {
    mocks.list.mockResolvedValue({ data: { ...response().data, data: [{ ...product(), is_active: null }] } })
    await mount()
    expect(button('状态待确认')?.disabled).toBe(true)
    button('状态待确认')?.click(); await settle()
    expect(mocks.setActive).not.toHaveBeenCalled()
  })

  it('preserves real business error and never replaces it with generic request failure', async () => {
    mocks.remove.mockRejectedValue({ message: '存在关联订单，不允许删除', response: { status: 422, data: { msg: '存在关联订单，不允许删除' } } })
    await mount()
    button('删除')?.click(); await settle()
    expect(root.textContent).not.toContain('存在关联订单，不允许删除')
    expect(mocks.failure).toHaveBeenCalledTimes(1)
    expect(button('删除')?.disabled).toBe(false)
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('blocks version-conflicted writes until a different version is read', async () => {
    mocks.remove.mockRejectedValue({ response: { status: 409, data: { msg: '版本冲突' } } })
    await mount()
    button('删除')?.click(); await settle()
    expect(root.textContent).not.toContain('版本冲突')
    expect(root.textContent).toContain(i18n.global.t('telegramMembership.operations.conflict'))
    expect(button('删除')?.disabled).toBe(true)
    button('删除')?.click()
    expect(mocks.remove).toHaveBeenCalledTimes(1)
    mocks.list.mockResolvedValue(response([product(false, 4)]))
    button(String(i18n.global.t('admin.common.search')))?.click(); await settle()
    expect(button('删除')?.disabled).toBe(false)
  })

  it.each([new DOMException('timeout', 'AbortError'), new TypeError('Failed to fetch'), { resultUnknown: true }])('does not replay uncertain writes after refresh of the same version (%s)', async failure => {
    mocks.remove.mockRejectedValue(failure)
    await mount()
    button('删除')?.click(); await settle()
    expect(root.textContent).toContain('操作结果未知')
    expect(button('删除')?.disabled).toBe(true)
    button(String(i18n.global.t('admin.common.search')))?.click(); await settle()
    button('删除')?.click(); await settle()
    expect(mocks.remove).toHaveBeenCalledTimes(1)
    expect(mocks.list).toHaveBeenCalledTimes(2)
  })

  it('treats an invalid write envelope as unknown and prevents replay', async () => {
    mocks.remove.mockResolvedValue({ data: {} })
    await mount()
    button('删除')?.click(); await settle()
    expect(root.textContent).toContain('操作结果未知')
    expect(button('删除')?.disabled).toBe(true)
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('does not claim deletion success when read-back fails', async () => {
    await mount()
    mocks.list.mockRejectedValue(new Error('读取失败'))
    button('删除')?.click(); await settle()
    expect(root.textContent).toContain('操作响应已收到，但列表刷新失败')
    expect(root.textContent).not.toContain('删除成功')
  })
})
