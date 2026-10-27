import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveSiteName } from '../src/utils/brand.ts'

test('missing and legacy default names use the localized storefront name', () => {
  for (const fallback of ['商城', 'Store']) {
    for (const value of [undefined, null, '', '  ', 123, {}, 'Dujiao-Next', ' dujiao-next ']) {
      assert.equal(resolveSiteName(value, fallback), fallback)
    }
  }
})

test('custom brands and SEO titles are preserved, not globally rewritten', () => {
  for (const value of ['My Store', '我的商城', 'Dujiao-Next Custom', 'D&J Studio']) {
    assert.equal(resolveSiteName(` ${value} `, 'Store'), value)
  }
})
