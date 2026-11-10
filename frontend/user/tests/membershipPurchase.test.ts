import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { ref, computed } from 'vue'
import { createPinia, defineStore, setActivePinia } from 'pinia'
import { parse } from 'vue/compiler-sfc'
import test from 'node:test'
import assert from 'node:assert/strict'
import { isMembershipProduct, membershipGroupNames, membershipSalesBlocked, membershipTerm, membershipTermLabel } from '../src/utils/membershipPurchase.ts'
import { savePurchaseContext, readPurchaseContext } from '../src/utils/purchaseContext.ts'

/** Execute the real checkout gate with catalog and identity dependencies supplied by the test. */
function checkoutGateFixture(changeAccount = false, closed = true, listed = false) {
  const auth = { user: { id: 7 }, isAuthenticated: true }
  const items = [{ productId: 1, skuId: 1, quantity: 1, slug: 'membership', membership: undefined as unknown },
    { productId: 2, skuId: 2, quantity: 3, slug: 'ordinary', membership: undefined as unknown }]
  const source = readFileSync(new URL('../src/composables/useMembershipCheckout.ts', import.meta.url), 'utf8')
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } })
  const exports: Record<string, any> = {}
  const dependencies: Record<string, unknown> = {
    vue: { ref, computed }, '../stores/userAuth': { useUserAuthStore: () => auth },
    '../utils/membershipPurchase': { membershipSalesBlocked },
    '../utils/cartStock': { refreshCartStockSnapshots: async (store: any, strict: boolean, current: () => boolean) => {
      assert.equal(strict, true)
      if (changeAccount) auth.user.id = 8
      if (!current()) throw new Error('Changed account')
      store.patchItem(1, 1, { membership: listed ? { ...product.membership, sales_open: true } : closed ? product.membership : undefined })
    } },
  }
  runInNewContext(compiled.outputText, { exports, require: (name: string) => dependencies[name], console })
  return { gate: exports.useMembershipCheckout(() => items), items }
}

test('checkout refresh discovers uncached membership and preserves mixed selections', async () => {
  const { gate, items } = checkoutGateFixture()
  assert.equal(await gate.refresh(), false)
  assert.equal(gate.blocked.value, true)
  assert.deepEqual(items.map(({ productId, quantity }) => [productId, quantity]), [[1, 1], [2, 3]])
})

test('checkout permits explicitly listed membership after authoritative catalog refresh', async () => {
  const { gate, items } = checkoutGateFixture(false, false, true)
  assert.equal(await gate.refresh(), true)
  assert.equal(gate.blocked.value, false)
  assert.deepEqual(items.map(({ productId, quantity }) => [productId, quantity]), [[1, 1], [2, 3]])
})

test('checkout rejects a late account response and ordinary catalog still permits quoting', async () => {
  const stale = checkoutGateFixture(true)
  assert.equal(await stale.gate.refresh(), false)
  assert.equal(stale.items[0]?.membership, undefined)
  const ordinary = checkoutGateFixture(false, false)
  assert.equal(await ordinary.gate.refresh(), true)
  assert.equal(ordinary.gate.blocked.value, false)
})

/** 加载真实购物车 store；返回 store 和隔离存储，避免读写浏览器或用户数据。 */
function membershipCartFixture() {
  const values = new Map<string, string>()
  const storage = {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
  }
  const source = readFileSync(new URL('../src/stores/cart.ts', import.meta.url), 'utf8')
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } })
  const exports: Record<string, any> = {}
  const dependencies: Record<string, unknown> = { vue: { ref, computed }, pinia: { defineStore } }
  setActivePinia(createPinia())
  runInNewContext(compiled.outputText, { exports, require: (name: string) => dependencies[name], localStorage: storage, console })
  return { cart: exports.useCartStore(), values, useCartStore: exports.useCartStore }
}

test('cart preserves membership on insert and refreshes it on same-SKU merge and reload', () => {
  const { cart, values, useCartStore } = membershipCartFixture()
  const item = { productId: 1, skuId: 1, slug: 'member', title: 'Member', priceAmount: '10', quantity: 1,
    minPurchaseQuantity: 1, maxPurchaseQuantity: 1, membership: product.membership }
  cart.addItem(item)
  assert.equal(cart.items[0].membership.sales_open, false)
  const listed = { ...product.membership, config_version: 2, sales_open: true }
  cart.addItem({ ...item, membership: listed })
  assert.equal(cart.items[0].membership.config_version, 2)
  assert.equal(cart.items[0].membership.sales_open, true)
  assert.equal(cart.items[0].quantity, 1)
  assert.equal(JSON.parse(values.get('cart_items')!)[0].membership.sales_open, true)
  setActivePinia(createPinia())
  // 同一 store 定义重新实例化，验证 localStorage 恢复的会员状态。
  assert.equal(useCartStore().items[0].membership.sales_open, true)
  cart.addItem({ ...item, membership: product.membership })
  assert.equal(cart.items[0].membership.sales_open, false)
  cart.addItem({ ...item, membership: undefined })
  assert.equal(cart.items[0].membership, undefined)
})

test('detail and both quick-buy payloads carry the public membership projection', () => {
  const detail = readFileSync(new URL('../src/composables/useProductDetail.ts', import.meta.url), 'utf8')
  const quick = readFileSync(new URL('../src/components/ProductQuickBuy.vue', import.meta.url), 'utf8')
  assert.match(detail, /const buildItemPayload = [\s\S]*?membership: product\.value\.membership/)
  for (const method of ['cartStore.addItem', 'buyNowStore.setItem']) {
    const payload = quick.slice(quick.indexOf(`${method}({`)).split('})')[0]!
    assert.match(payload, /membership: props\.product\.membership/)
  }
})

test('Orbit remove action is outside the ordinary-only quantity controls', () => {
  const source = readFileSync(new URL('../src/templates/orbit/Cart.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  let removeActions = 0
  /** 遍历模板节点及祖先指令；返回 void，断言删除按钮不受会员数量条件限制。 */
  const visit = (node: any, ordinaryOnly = false): void => {
    const restricted = ordinaryOnly || (node.props || []).some((prop: any) =>
      prop.type === 7 && prop.name === 'if' && prop.exp?.content === '!item.membership')
    if ((node.props || []).some((prop: any) => prop.type === 7 && prop.name === 'on' && prop.exp?.content === 'removeWithUndo(item)')) {
      removeActions += 1
      assert.equal(restricted, false)
    }
    for (const child of node.children || []) visit(child, restricted)
  }
  visit(descriptor.template!.ast)
  assert.equal(removeActions, 1)
})

const translate = (key: string, values?: Record<string, number>) => `${key}:${JSON.stringify(values || {})}`
const product = { membership: { config_version: 1, quantity: 1, sales_open: false,
  terms: [2, 5, 18].map((months, index) => ({ sku_id: index + 1, sku_code: index ? `M${months}` : 'DEFAULT', kind: 'months', months }))
    .concat([{ sku_id: 4, sku_code: 'PERMANENT', kind: 'permanent', months: 0 }]) } }

test('DEFAULT and custom 2/5/18 month plans use exactly 30 days per month', () => {
  for (const [index, months] of [2, 5, 18].entries()) {
    assert.equal(membershipTerm(product, index + 1)?.months, months)
    assert.equal(membershipTermLabel(product, index + 1, translate),
      `membershipPurchase.months:${JSON.stringify({ months, days: months * 30 })}`)
  }
  assert.equal(membershipTerm(product, 1)?.sku_code, 'DEFAULT')
})

test('permanent has no fabricated expiry; missing SKU does not select another plan', () => {
  assert.equal(membershipTermLabel(product, 4, translate), 'membershipPurchase.permanent:{}')
  assert.equal(membershipTerm(product, 999), null)
})

test('ordinary login-only member product is not Telegram membership', () => {
  const ordinary = { purchase_type: 'member', membership: undefined }
  assert.equal(isMembershipProduct(ordinary), false)
  assert.equal(membershipSalesBlocked(ordinary), false)
  assert.equal(membershipSalesBlocked(product), true)
  assert.equal(membershipSalesBlocked({ membership: { sales_open: true } }), false)
  for (const membership of [{}, { sales_open: 'true' }, { sales_open: 1 }, true, 'invalid']) {
    assert.equal(membershipSalesBlocked({ membership }), true)
  }
})

test('public group names preserve order and never fall back to private target fields', () => {
  assert.deepEqual(membershipGroupNames({ membership: { group_names: [' 群二 ', '群一', '<b>群名</b>'] } }),
    ['群二', '群一', '<b>群名</b>'])
  for (const membership of [undefined, null, {}, { targets: [{ name: 'private', telegram_chat_id: '-123' }] },
    { group_names: 'group' }, { group_names: ['valid', 1] }, { group_names: [''] }]) {
    assert.deepEqual(membershipGroupNames({ membership }), [])
  }
  const component = readFileSync(new URL('../src/components/product/MembershipPurchaseInfo.vue', import.meta.url), 'utf8')
  assert.match(component, /\{\{ name \}\}/)
  assert.doesNotMatch(component, /v-html/)
})

test('binding return preserves SKU/source, strips prices, and isolates switched accounts', () => {
  const values = new Map<string, string>()
  const storage = { getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) } }
  const context = { version: 1 as const, accountId: 7, issuedAt: Date.now(), returnPath: '/products/membership',
    items: [{ productId: 1, skuId: 3, slug: 'membership', quantity: 1, priceAmount: '1' }] }
  assert.equal(savePurchaseContext(context, undefined, storage), true)
  const restored = readPurchaseContext(7, undefined, true, storage)
  assert.equal(restored?.returnPath, '/products/membership')
  assert.deepEqual(restored?.items, [{ productId: 1, skuId: 3, slug: 'membership', quantity: 1 }])
  assert.equal(readPurchaseContext(7, undefined, true, storage), null)
  assert.equal(savePurchaseContext(context, undefined, storage), true)
  assert.equal(readPurchaseContext(8, undefined, true, storage), null)
})
