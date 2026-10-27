import assert from 'node:assert/strict'
import test from 'node:test'
import { orbitRootPaths, orbitParentPath, safeOrbitHistoryPath, orbitNavigationMotion, orbitKeyboardVisible } from '../src/templates/orbit/navigation.ts'

test('Orbit root pages retain search and nested pages have parents', () => {
  assert.deepEqual([...orbitRootPaths], ['/', '/products', '/cart', '/me'])
  for (const [path, parent] of [['/products/demo', '/products'], ['/categories/a', '/products'], ['/me/security', '/me'], ['/orders/123', '/me/orders'], ['/guest/orders/123', '/guest/orders'], ['/blog/a', '/blog']]) {
    assert.equal(orbitParentPath(path!, {}), parent)
  }
  assert.equal(orbitParentPath('/checkout', {}, 'demo'), '/products/demo')
  assert.equal(orbitParentPath('/checkout', {}), '/cart')
})

test('Orbit keyboard detection ignores pinch zoom, browser chrome and desktop', () => {
  assert.equal(orbitKeyboardVisible(true, true, 800, 480, 1), true)
  assert.equal(orbitKeyboardVisible(true, true, 800, 480, 2), false)
  assert.equal(orbitKeyboardVisible(true, true, 800, 710, 1), false)
  assert.equal(orbitKeyboardVisible(true, false, 800, 480, 1), false)
  assert.equal(orbitKeyboardVisible(false, true, 800, 480, 1), false)
})

test('Orbit motion distinguishes nested forward, history back and root tab switches', () => {
  assert.equal(orbitNavigationMotion('/products', '/products/demo', 2, 3), 'forward')
  assert.equal(orbitNavigationMotion('/checkout', '/products/demo', 4, 3), 'back')
  assert.equal(orbitNavigationMotion('/products/demo', '/products', null, null), 'back')
  assert.equal(orbitNavigationMotion('/', '/cart', 1, 2), 'neutral')
  assert.equal(orbitNavigationMotion('/products', '/products', 2, 2), 'neutral')
})

test('Orbit history refuses external and malformed paths', () => {
  for (const path of [null, undefined, 'https://example.com', '//example.com', '/\\example.com', '/ products']) assert.equal(safeOrbitHistoryPath(path), null)
  assert.equal(safeOrbitHistoryPath('/products?q=test'), '/products?q=test')
})

test('Orbit payment returns to the correct order, including provider callbacks and recharge', () => {
  assert.equal(orbitParentPath('/pay', { order_no: 'A1' }), '/orders/A1')
  assert.equal(orbitParentPath('/pay', { order_no: 'A1', guest: '1' }), '/guest/orders/A1')
  assert.equal(orbitParentPath('/pay', { out_trade_no: 'A1', guest: 'true' }), '/guest/orders/A1')
  assert.equal(orbitParentPath('/pay', { recharge_no: 'WR123' }), '/recharge-orders/WR123')
  assert.equal(orbitParentPath('/pay', { order_no: 'WR123' }), '/recharge-orders/WR123')
  assert.equal(orbitParentPath('/pay', {}), '/me/orders')
  assert.equal(orbitParentPath('/pay', { guest: '1' }), '/guest/orders')
})
