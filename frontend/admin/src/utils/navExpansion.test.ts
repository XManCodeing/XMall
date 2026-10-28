import { describe, expect, it } from 'vitest'
import { parseNavExpansion, resolveNavExpansion } from './navExpansion'

describe('sidebar group expansion', () => {
  it('defaults to collapsed except for the current route group', () => {
    expect(resolveNavExpansion(undefined, false, false)).toBe(false)
    expect(resolveNavExpansion(undefined, true, false)).toBe(true)
  })

  it('preserves explicit manual choices and temporarily expands search results', () => {
    expect(resolveNavExpansion(false, true, false)).toBe(false)
    expect(resolveNavExpansion(true, false, false)).toBe(true)
    expect(resolveNavExpansion(false, false, true)).toBe(true)
  })

  it('resets old all-expanded defaults but retains mixed preferences', () => {
    expect(parseNavExpansion('{"products":true,"orders":true}', true)).toEqual({})
    expect(parseNavExpansion('{"products":false,"orders":true}', true)).toEqual({ products: false, orders: true })
    expect(parseNavExpansion('{"products":true,"orders":true}')).toEqual({ products: true, orders: true })
  })

  it('ignores malformed storage and non-boolean values', () => {
    for (const raw of [null, '', 'broken', 'null', '[]', '42']) {
      expect(parseNavExpansion(raw)).toEqual({})
    }
    expect(parseNavExpansion('{"products":false,"orders":"true"}')).toEqual({ products: false })
  })
})
