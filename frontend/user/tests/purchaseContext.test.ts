import test from 'node:test'
import assert from 'node:assert/strict'
import { PURCHASE_CONTEXT_KEY, PURCHASE_CONTEXT_TTL_MS, purchaseReturnPath, purchaseSelection,
  readPurchaseContext, savePurchaseContext, validatePurchaseContext, type PurchaseContext } from '../src/utils/purchaseContext.ts'

/** 创建隔离内存存储；返回 Storage 的最小读写接口。 */
function storageFixture() {
  const data = new Map<string, string>()
  return { data, getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => { data.set(key, value) },
    removeItem: (key: string) => { data.delete(key) } }
}
/** 创建当前账号的最小选择，now 为测试时钟。 */
function contextFixture(now = Date.now()): PurchaseContext {
  return { version: 1, accountId: 7, issuedAt: now, returnPath: '/checkout?mode=buynow',
    items: [{ productId: 3, skuId: 5, slug: 'default-product', quantity: 2 }] }
}

test('purchase return allows only internal purchase routes', () => {
  for (const path of ['/checkout', '/checkout?mode=buynow', '/cart', '/products/valid-slug']) assert.equal(purchaseReturnPath(path), path)
  for (const path of ['//evil.test', '/\\evil', 'https://evil.test', '/pay', '/auth/login', '/checkout?redirect=//evil',
    '/products/../pay', '/products/%2f%2fevil', '/checkout#secret', '/checkout\n', '/products/x?token=secret']) {
    assert.equal(purchaseReturnPath(path), '', path)
  }
})

test('context is account scoped, bounded and expires without renewal on read', () => {
  const now = Date.now()
  const context = contextFixture(now)
  assert.ok(validatePurchaseContext(context, 7, now))
  assert.equal(validatePurchaseContext(context, 8, now), null)
  assert.equal(validatePurchaseContext(context, 7, now - 1), null)
  assert.equal(validatePurchaseContext(context, 7, now + PURCHASE_CONTEXT_TTL_MS), null)
  assert.equal(validatePurchaseContext({ ...context, items: Array(101).fill(context.items[0]) }, 7, now), null)
  assert.equal(validatePurchaseContext({ ...context, items: [context.items[0], context.items[0]], returnPath: '/checkout' }, 7, now), null)
})

test('persistence discards amounts, identity assertions and sensitive forms', () => {
  const storage = storageFixture()
  const context = contextFixture()
  Object.assign(context.items[0]!, { priceAmount: '0.01', manualFormData: { password: 'secret' }, telegramId: '999' })
  assert.equal(savePurchaseContext(context, PURCHASE_CONTEXT_KEY, storage), true)
  const raw = storage.data.get(PURCHASE_CONTEXT_KEY)!
  assert.equal(/priceAmount|password|secret|telegramId/.test(raw), false)
  assert.equal(readPurchaseContext(7, PURCHASE_CONTEXT_KEY, true, storage)?.items[0]?.quantity, 2)
  assert.equal(readPurchaseContext(7, PURCHASE_CONTEXT_KEY, true, storage), null)
})

test('malformed and inaccessible storage fails closed', () => {
  const storage = storageFixture()
  for (const raw of ['{', 'null', '[]', 'x'.repeat(32769)]) {
    storage.setItem(PURCHASE_CONTEXT_KEY, raw)
    assert.equal(readPurchaseContext(7, PURCHASE_CONTEXT_KEY, false, storage), null)
    assert.equal(storage.getItem(PURCHASE_CONTEXT_KEY), null)
  }
  const blocked = { getItem() { throw new Error('blocked') }, setItem() { throw new Error('blocked') }, removeItem() { throw new Error('blocked') } }
  assert.equal(savePurchaseContext(contextFixture(), PURCHASE_CONTEXT_KEY, blocked), false)
  assert.equal(readPurchaseContext(7, PURCHASE_CONTEXT_KEY, true, blocked), null)
  assert.equal(readPurchaseContext(7, PURCHASE_CONTEXT_KEY, false, null), null)
})

test('selection rejects invalid IDs and quantity instead of silently changing choices', () => {
  const selection = contextFixture().items[0]!
  for (const quantity of [0, -1, 1.5, Infinity, 1000001]) assert.equal(purchaseSelection({ ...selection, quantity }), null)
  for (const skuId of [0, '5', Number.MAX_SAFE_INTEGER + 1]) assert.equal(purchaseSelection({ ...selection, skuId }), null)
})
