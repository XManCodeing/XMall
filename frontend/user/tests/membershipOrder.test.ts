import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { createSSRApp, defineComponent, h, ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import { compileScript, parse } from 'vue/compiler-sfc'
import ts from 'typescript'
import { membershipOrderDuration, membershipOrderSnapshot } from '../src/utils/membershipOrder.ts'

const require = createRequire(import.meta.url)
const sourceRoot = resolve(import.meta.dirname, '../src')
const snapshot = { beneficiary_telegram_id: '9007199254740993', config_version: 987654321, kind: 'months', months: 3, quantity: 1 }
const item = { title: { 'zh-CN': '测试商品' }, quantity: 1, original_unit_price: '10', original_total_price: '10', membership_snapshot: snapshot }
const baseOrder = { order_no: 'ORDER1', status: 'paid', currency: 'CNY', total_amount: '10', items: [item] }
const slotStub = defineComponent({ setup: (_props, { slots }) => () => h('div', slots.default?.()) })

/**
 * 编译真实订单 SFC 及共享展示逻辑，隔离网络、路由和无关 UI。
 * @param order 接口响应夹具。
 * @returns 加载实际订单组件的函数；不启动 Vite 或读取环境文件。
 */
function componentLoader(order: unknown) {
  const cache = new Map<string, any>()
  const load = (filename: string): any => {
    if (cache.has(filename)) return cache.get(filename)
    const source = readFileSync(filename, 'utf8')
    const script = filename.endsWith('.vue')
      ? compileScript(parse(source, { filename }).descriptor, { id: filename, inlineTemplate: true }).content
      : source
    const compiled = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
    const localRequire = (specifier: string): any => {
      if (specifier.includes('composables/useOrderDetail')) return {
        useOrderDetail: () => ({
          ...load(resolve(sourceRoot, 'composables/useOrderDisplayHelpers.ts')).useOrderDisplayHelpers(ref(order)),
          order: ref(order), loading: ref(false), fulfillmentDownloading: ref(false),
        }),
      }
      if (specifier === 'vue-router') return { useRouter: () => ({ push: async () => {} }) }
      if (specifier.includes('stores/userAuth')) return { useUserAuthStore: () => ({ isAuthenticated: false }) }
      if (specifier.includes('utils/purchaseContext')) return { purchaseSelection: () => null, savePurchaseContext: () => false }
      if (specifier.includes('utils/image')) return { getImageUrl: (path: string) => path || '' }
      if (specifier.includes('stores/app')) return { useAppStore: () => ({ locale: 'zh-CN' }) }
      if (specifier.includes('/ui/')) return new Proxy({}, { get: () => slotStub })
      if (/(SmartImage|BreadcrumbNav|EmptyState)\.vue$/.test(specifier)) return { default: slotStub }
      if (!specifier.startsWith('.') && !specifier.startsWith('@/')) return require(specifier)
      let dependency = specifier.startsWith('@/') ? resolve(sourceRoot, specifier.slice(2)) : resolve(dirname(filename), specifier)
      if (!/\.(ts|vue)$/.test(dependency)) dependency += '.ts'
      return load(dependency)
    }
    const module = { exports: {} }
    new Function('require', 'module', 'exports', compiled)(localRequire, module, module.exports)
    cache.set(filename, module.exports)
    return module.exports
  }
  return load
}

/**
 * 服务端实际渲染指定模板订单页。
 * @param page 真实页面相对路径。
 * @param order 订单响应。
 * @param locale 三语之一。
 * @param props 页面或共享组件属性。
 * @returns 完整 HTML，用于断言最终用户可见内容。
 */
async function renderOrder(page: string, order: unknown, locale = 'zh-CN', props = {}) {
  const component = componentLoader(order)(resolve(sourceRoot, page)).default
  const app = createSSRApp(component, props)
  app.use(createI18n({ legacy: false, locale, messages: { [locale]: JSON.parse(readFileSync(resolve(sourceRoot, `i18n/locales/${locale}.json`), 'utf8')) } }))
  app.component('RouterLink', slotStub)
  return renderToString(app)
}

for (const page of ['views/OrderDetail.vue', 'templates/vault/OrderDetail.vue', 'templates/orbit/OrderDetail.vue']) {
  test(`${page}: renders parent and child purchase snapshots without fulfillment`, async () => {
    const html = await renderOrder(page, { ...baseOrder, children: [{ ...baseOrder, id: 2, items: [{ ...item, membership_snapshot: { ...snapshot, kind: 'permanent', months: 0 } }] }] })
    assert.equal((html.match(/data-membership-order/g) || []).length, 2)
    assert.match(html, /9007199254740993/)
    assert.match(html, /3 个月（每月固定 30 天，共 90 天）/)
    assert.match(html, /永久/)
    assert.match(html, /仅记录本次购买/)
    assert.doesNotMatch(html, /987654321|config_version|已开通|邀请|有效期/)
  })
  test(`${page}: ordinary items stay unchanged`, async () => {
    const html = await renderOrder(page, { ...baseOrder, items: [{ ...item, membership_snapshot: undefined }] })
    assert.doesNotMatch(html, /data-membership-order|会员购买信息|9007199254740993/)
  })
}

test('vault independent delivery section does not gate purchase snapshots or invent activation', async () => {
  const html = await renderOrder('templates/vault/OrderDetail.vue', { ...baseOrder, fulfillment: { type: 'manual', status: 'pending', payload: '' } })
  assert.match(html, /9007199254740993/)
  assert.doesNotMatch(html, /已开通|邀请|987654321/)
})

test('vault shared body keeps guest snapshots hidden even if supplied', async () => {
  const html = await renderOrder('templates/vault/components/VaultOrderBody.vue', baseOrder, 'zh-CN', { order: baseOrder, variant: 'guest', fulfillmentDownloading: false })
  assert.doesNotMatch(html, /data-membership-order|9007199254740993/)
})

test('membership purchase renders all three translations without internal metadata', async () => {
  for (const locale of ['zh-CN', 'zh-TW', 'en-US']) {
    const html = await renderOrder('components/MembershipOrderSnapshot.vue', null, locale, { snapshot })
    assert.match(html, /9007199254740993/)
    assert.match(html, /30/)
    assert.match(html, /90/)
    assert.doesNotMatch(html, /membershipOrder\.|987654321|config_version/)
  }
})

test('purchase component renders public groups as escaped text in all locales', async () => {
  const product = { membership: { config_version: 1, quantity: 1, sales_open: false,
    group_names: ['群二', '<img src=x onerror=alert(1)>'],
    terms: [{ sku_id: 1, sku_code: 'DEFAULT', kind: 'months', months: 5 }] } }
  for (const locale of ['zh-CN', 'zh-TW', 'en-US']) {
    const html = await renderOrder('components/product/MembershipPurchaseInfo.vue', null, locale, { product, skuId: 1 })
    assert.match(html, /群二/)
    assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/)
    assert.match(html, /150/)
    assert.doesNotMatch(html, /<img|membershipPurchase\.|telegram_chat_id|bot_instance_id/)
  }
  const listed = await renderOrder('components/product/MembershipPurchaseInfo.vue', null, 'zh-CN', {
    product: { ...product, membership: { ...product.membership, sales_open: true } }, skuId: 1,
  })
  assert.doesNotMatch(listed, /role="status"/)
  assert.match(listed, /群二/)
  const ordinary = await renderOrder('components/product/MembershipPurchaseInfo.vue', null, 'zh-CN', { product: { purchase_type: 'member' } })
  assert.doesNotMatch(ordinary, /包含群组|绑定 Telegram/)
})

test('snapshot validation excludes missing, malformed and non-membership records', () => {
  for (const invalid of [null, {}, { ...snapshot, quantity: 2 }, { ...snapshot, kind: 'other' }, { ...snapshot, months: 0 }, { ...snapshot, months: 1.5 }, { ...snapshot, beneficiary_telegram_id: 123 }, { ...snapshot, beneficiary_telegram_id: '<script>' }]) {
    assert.equal(membershipOrderSnapshot(invalid), null)
  }
  const valid = membershipOrderSnapshot(snapshot)!
  assert.equal(valid.beneficiary_telegram_id, snapshot.beneficiary_telegram_id)
  assert.equal(membershipOrderDuration(valid, (key, values) => `${key}:${values?.days}`), 'membershipOrder.months:90')
})
