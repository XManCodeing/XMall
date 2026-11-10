import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { ref, computed, watch } from 'vue'
import * as contextUtils from '../src/utils/purchaseContext.ts'

/** 在隔离 VM 内执行真实 TS 模块；依赖通过参数替换，无网络与环境文件加载。 */
function loadModule(path: string, dependencies: Record<string, unknown>) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: Record<string, any> = {}
  runInNewContext(compiled.outputText, { exports, require: (name: string) => {
    if (!(name in dependencies)) throw new Error(`Missing test dependency: ${name}`)
    return dependencies[name]
  }, console, Date, Set, Map })
  return exports
}

/** 生成账号隔离存储和服务端刷新桩，返回真实 store 工厂的测试依赖。 */
function buyNowFixture() {
  let context: any = null
  let accountId = 7
  let refreshes = 0
  const auth = { user: { id: 7 }, isAuthenticated: true }
  const dependencies = {
    pinia: { defineStore: (_name: string, setup: () => unknown) => setup },
    vue: { ref, computed, watch }, './userAuth': { useUserAuthStore: () => auth },
    '../api': { userProfileAPI: { current: async () => ({ data: { data: { id: accountId } } }) } },
    '../utils/cartStock': { refreshCartStockSnapshots: async (store: any, strict: boolean) => {
      assert.equal(strict, true)
      refreshes += 1
      store.patchItem(3, 5, { priceAmount: '20.00', title: 'fresh product' })
    } },
    '../utils/purchaseContext': { ...contextUtils,
      clearPurchaseContext: () => { context = null },
      savePurchaseContext: (value: any) => { context = contextUtils.validatePurchaseContext(value, 7); return true },
      readPurchaseContext: (id: number) => contextUtils.validatePurchaseContext(context, id),
    },
  }
  const create = () => loadModule('../src/stores/buyNow.ts', dependencies).useBuyNowStore()
  return { create, auth, context: () => context, setServerAccount: (id: number) => { accountId = id }, refreshes: () => refreshes }
}

test('buy-now reload restores only SKU/quantity and fetches current product with current account', async () => {
  const fixture = buyNowFixture()
  const store = fixture.create()
  store.setItem({ productId: 3, skuId: 5, slug: 'product', quantity: 2, priceAmount: '0.01', title: 'stale', manualFormData: 'secret' })
  assert.equal(fixture.context().items[0].priceAmount, undefined)
  const reloaded = fixture.create()
  assert.equal(reloaded.item.value, null)
  assert.equal(await reloaded.restore(), true)
  assert.equal(reloaded.item.value.priceAmount, '20.00')
  assert.equal(reloaded.item.value.quantity, 2)
  assert.equal(reloaded.item.value.manualFormData, undefined)
  assert.equal(fixture.refreshes(), 1)
  reloaded.clear()
  assert.equal(fixture.context(), null)
})

test('buy-now refuses stale account identity before fetching products', async () => {
  const fixture = buyNowFixture()
  fixture.create().setItem({ productId: 3, skuId: 5, slug: 'product', quantity: 2 })
  fixture.setServerAccount(8)
  await assert.rejects(fixture.create().restore(), /account changed/)
  assert.equal(fixture.refreshes(), 0)
})

/** 构造绑定完成流程；保留混合购物车对象供验证，不提供任何支付 API。 */
function returnFixture(options: { bound?: boolean; serverAccount?: number; refreshError?: boolean; switchAccountDuringRefresh?: boolean } = {}) {
  const auth = { user: { id: 7 }, isAuthenticated: true }
  const cart = { items: [{ productId: 1, quantity: 2 }, { productId: 2, quantity: 1 }] }
  const paths: string[] = []
  const refreshed: any[] = []
  let consumed = false
  const module = loadModule('../src/composables/usePurchaseReturn.ts', {
    'vue-router': { useRouter: () => ({ replace: async (path: string) => { paths.push(path) } }) },
    '../api': { userProfileAPI: {
      current: async () => ({ data: { data: { id: options.serverAccount ?? 7 } } }),
      getTelegramBinding: async () => ({ data: { data: { bound: options.bound ?? true } } }),
    } },
    '../stores/userAuth': { useUserAuthStore: () => auth },
    '../stores/buyNow': { useBuyNowStore: () => { throw new Error('Not a buy-now return') } },
    '../stores/cart': { useCartStore: () => cart },
    '../utils/cartStock': { refreshCartStockSnapshots: async (value: any, strict: boolean, isCurrent: () => boolean) => {
      assert.equal(strict, true)
      assert.equal(typeof isCurrent, 'function')
      if (options.switchAccountDuringRefresh) auth.user.id = 8
      if (!isCurrent()) throw new Error('Purchase context changed')
      if (options.refreshError) throw new Error('Product unavailable')
      refreshed.push(value)
    } },
    '../utils/purchaseContext': { readPurchaseContext: (_id: number, _key: unknown, consume: boolean) => {
      assert.equal(consume, true)
      if (consumed) return null
      consumed = true
      return { returnPath: '/checkout', items: [{ productId: 1 }] }
    } },
  })
  return { finish: module.usePurchaseReturn().finishPurchaseReturn, cart, paths, refreshed }
}

test('successful binding returns once, refreshes all mixed cart items and never deletes selections', async () => {
  const fixture = returnFixture()
  const before = JSON.stringify(fixture.cart)
  assert.equal(await fixture.finish(), true)
  assert.equal(await fixture.finish(), false)
  assert.deepEqual(fixture.paths, ['/checkout'])
  assert.equal(fixture.refreshed[0], fixture.cart)
  assert.equal(JSON.stringify(fixture.cart), before)
})

test('binding return fails closed for account mismatch, unbound identity and product refresh failure', async () => {
  for (const options of [{ serverAccount: 8 }, { bound: false }, { refreshError: true }, { switchAccountDuringRefresh: true }]) {
    const fixture = returnFixture(options)
    await assert.rejects(fixture.finish())
    assert.deepEqual(fixture.paths, [])
    assert.equal(fixture.cart.items.length, 2)
  }
})

test('widget and Mini App store actions recheck binding then invoke purchase return without payment', async () => {
  for (const action of ['bindTelegram', 'bindTelegramMiniApp']) {
    let returned = 0
    let bindingChecks = 0
    const module = loadModule('../src/stores/userProfile.ts', {
      pinia: { defineStore: (_name: string, setup: () => unknown) => setup },
      vue: { ref, computed },
      '../api': { userProfileAPI: {
        bindTelegram: async () => ({}), bindTelegramMiniApp: async () => ({}),
        getTelegramBinding: async () => { bindingChecks += 1; return { data: { data: { bound: true } } } },
      } },
      './userAuth': { useUserAuthStore: () => ({}) },
      '../composables/usePurchaseReturn': { usePurchaseReturn: () => ({ finishPurchaseReturn: async () => { returned += 1 } }) },
    })
    assert.equal(await module.useUserProfileStore()[action]({}), true)
    assert.equal(bindingChecks, 1)
    assert.equal(returned, 1)
  }
})

test('OIDC bind uses purchase return, fallback stays on security, inaccessible intent never logs in', async () => {
  for (const scenario of ['purchase', 'fallback', 'blocked'] as const) {
    let mounted: (() => Promise<void>) | undefined
    const paths: unknown[] = []
    let exchanges = 0
    const module = loadModule('../src/composables/useTelegramCallback.ts', {
      vue: { ref, onMounted: (callback: () => Promise<void>) => { mounted = callback } },
      'vue-router': { useRoute: () => ({ query: { code: 'code', state: 'state' } }),
        useRouter: () => ({ replace: async (path: unknown) => { paths.push(path) } }) },
      'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
      '../stores/userAuth': { useUserAuthStore: () => ({ isAuthenticated: true,
        telegramOidcLogin: () => { throw new Error('Must not switch bind into login') } }) },
      '../api': { userProfileAPI: { telegramOidcBindCallback: async () => { exchanges += 1 } } },
      '../utils/googleRedirect': { normalizeGoogleRedirectReturnPath: () => '',
        getGoogleRedirectSessionStorage: () => scenario === 'blocked' ? null : {
          getItem: (key: string) => key === 'tg_oidc_intent' ? 'bind' : '', removeItem() {},
        } },
      '../utils/purchaseContext': { clearPurchaseContext() {} },
      './usePurchaseReturn': { usePurchaseReturn: () => ({ finishPurchaseReturn: async () => scenario === 'purchase' }) },
    })
    const callback = module.useTelegramCallback()
    await mounted!()
    assert.equal(exchanges, scenario === 'blocked' ? 0 : 1)
    assert.equal(paths.length, scenario === 'fallback' ? 1 : 0)
    if (scenario === 'blocked') assert.equal(callback.loading.value, false)
  }
})

test('strict catalogue refresh rejects replacement DEFAULT SKU and product mismatch without changing quantity', async () => {
  for (const change of ['valid', 'sku', 'product', 'network']) {
    const item = { productId: 3, skuId: 5, slug: 'product', quantity: 2, priceAmount: '1.00' }
    const module = loadModule('../src/utils/cartStock.ts', {
      '../api': { productAPI: { detail: async () => {
        if (change === 'network') throw new Error('offline')
        return { data: { data: { id: change === 'product' ? 4 : 3, title: 'current',
          skus: [{ id: change === 'sku' ? 6 : 5, sku_code: 'DEFAULT', is_active: true, price_amount: '20.00' }] } } }
      } } },
      './cartPricingSnapshot': { resolveCartPricingSnapshot: (_product: unknown, sku: any) => ({ priceAmount: sku.price_amount }) },
      './sku': { normalizeSkuId: (value: unknown) => Number(value) || 0 },
    })
    const store = { items: [item], patchItem: (_id: number, _sku: number, patch: any) => Object.assign(item, patch) }
    if (change === 'valid') {
      await module.refreshCartStockSnapshots(store, true)
      assert.equal(item.priceAmount, '20.00')
    } else {
      await assert.rejects(module.refreshCartStockSnapshots(store, true))
      assert.equal(item.priceAmount, '1.00')
    }
    assert.equal(item.quantity, 2)
    assert.equal(item.skuId, 5)
  }
})

test('strict mixed-cart recovery validates every item before committing refreshed prices', async () => {
  const items = [
    { productId: 3, skuId: 5, slug: 'first', quantity: 1, priceAmount: '1.00' },
    { productId: 4, skuId: 6, slug: 'second', quantity: 1, priceAmount: '2.00' },
  ]
  const module = loadModule('../src/utils/cartStock.ts', {
    '../api': { productAPI: { detail: async (slug: string) => ({ data: { data: {
      id: slug === 'first' ? 3 : 4,
      skus: [{ id: slug === 'first' ? 5 : 99, is_active: true, price_amount: '20.00' }],
    } } }) } },
    './cartPricingSnapshot': { resolveCartPricingSnapshot: () => ({ priceAmount: '20.00' }) },
    './sku': { normalizeSkuId: (value: unknown) => Number(value) || 0 },
  })
  let writes = 0
  await assert.rejects(module.refreshCartStockSnapshots({ items, patchItem: () => { writes += 1 } }, true), /SKU/)
  assert.equal(writes, 0)
  assert.deepEqual(items.map((item) => item.priceAmount), ['1.00', '2.00'])
})

test('late catalogue responses cannot patch a different purchase account', async () => {
  let current = true
  let writes = 0
  const module = loadModule('../src/utils/cartStock.ts', {
    '../api': { productAPI: { detail: async () => {
      current = false
      return { data: { data: { id: 3, skus: [{ id: 5, is_active: true, price_amount: '20.00' }] } } }
    } } },
    './cartPricingSnapshot': { resolveCartPricingSnapshot: () => ({ priceAmount: '20.00' }) },
    './sku': { normalizeSkuId: (value: unknown) => Number(value) || 0 },
  })
  await assert.rejects(module.refreshCartStockSnapshots({
    items: [{ productId: 3, skuId: 5, slug: 'product', quantity: 1 }],
    patchItem: () => { writes += 1 },
  }, true, () => current), /context changed/)
  assert.equal(writes, 0)
})

test('strict recovery rejects missing product slugs instead of silently retaining stale prices', async () => {
  const module = loadModule('../src/utils/cartStock.ts', {
    '../api': { productAPI: { detail: async () => { throw new Error('Unexpected catalogue request') } } },
    './cartPricingSnapshot': { resolveCartPricingSnapshot: () => ({}) },
    './sku': { normalizeSkuId: (value: unknown) => Number(value) || 0 },
  })
  await assert.rejects(module.refreshCartStockSnapshots({
    items: [{ productId: 3, skuId: 5, slug: '', quantity: 1 }],
    patchItem: () => assert.fail('Invalid selection must not be patched'),
  }, true), /product is unavailable/)
})

test('three templates use shared checkout and both purchase producers use the persisted store', () => {
  for (const path of ['../src/views/Checkout.vue', '../src/templates/vault/Checkout.vue', '../src/templates/orbit/Checkout.vue']) {
    assert.match(readFileSync(new URL(path, import.meta.url), 'utf8'), /useCheckout\(\)/)
  }
  for (const path of ['../src/components/ProductQuickBuy.vue', '../src/composables/useProductDetail.ts']) {
    assert.match(readFileSync(new URL(path, import.meta.url), 'utf8'), /buyNowStore\.setItem\(/)
  }
  const checkout = readFileSync(new URL('../src/composables/useCheckout.ts', import.meta.url), 'utf8')
  assert.match(checkout, /await syncCartStockSnapshots\(\)\s+debouncedLoadPreview\(\)/)
  assert.match(checkout, /userOrderAPI\.preview\(payload\)/)
})
