import { describe, expect, it } from 'vitest'
import { advanceCatalog, catalogQuery, emptyCatalogPagination, MAX_CATALOG_ITEMS } from './upstreamPagination'

describe('upstream catalog pagination', () => {
  it('continues unknown-total cursor pages and retains opaque identifiers', () => {
    const first = [{ id: '001' }, { id: '9007199254740993' }]
    const state = advanceCatalog(emptyCatalogPagination(), [], { items: first, total: 0, total_known: false, has_more: null, next_cursor: 'opaque+/=' }, 50)
    expect(state.total).toBeNull()
    expect(state.hasMore).toBe(true)
    expect(catalogQuery(state, 50)).toEqual({ page: 2, page_size: 50, offset: 2, cursor: 'opaque+/=' })
    const second = advanceCatalog(state, first, { items: [{ id: 'third' }], total_known: false, has_more: false }, 50)
    expect(second.offset).toBe(3)
    expect(second.hasMore).toBe(false)
    expect(first.map(item => item.id)).toEqual(['001', '9007199254740993'])
  })
  it('supports has_more without cursor and full-page fallback when total is unknown', () => {
    const first = advanceCatalog(emptyCatalogPagination(), [], { items: [{ id: 1 }], total_known: false, has_more: true }, 50)
    expect(catalogQuery(first, 50)).toEqual({ page: 2, page_size: 50, offset: 1 })
    expect(advanceCatalog(emptyCatalogPagination(), [], { items: [{ id: 1 }], has_more: null }, 1).hasMore).toBe(true)
    expect(advanceCatalog(emptyCatalogPagination(), [], { items: [], total_known: false, has_more: null }, 1).hasMore).toBe(false)
  })
  it('supports legacy known totals and explicit has_more false', () => {
    expect(advanceCatalog(emptyCatalogPagination(), [], { items: [{ id: 1 }], total: 2 }, 50).hasMore).toBe(true)
    expect(advanceCatalog(emptyCatalogPagination(), [], { items: [{ id: 1 }], total: 1 }, 50).hasMore).toBe(false)
    expect(advanceCatalog(emptyCatalogPagination(), [], { items: [{ id: 1 }], has_more: false, next_cursor: 'ignored' }, 1).hasMore).toBe(false)
  })
  it('rejects repeated pages, duplicate IDs and cursor loops without mutating state', () => {
    const loaded = [{ id: 'a' }]
    const state = advanceCatalog(emptyCatalogPagination(), [], { items: loaded, next_cursor: 'cursor1' }, 1)
    expect(() => advanceCatalog(state, loaded, { items: [{ id: 'a' }] }, 1)).toThrow('catalogRepeatedPage')
    expect(() => advanceCatalog(state, loaded, { items: [{ id: 'b' }, { id: 'b' }] }, 1)).toThrow('catalogRepeatedPage')
    expect(() => advanceCatalog(state, loaded, { items: [{ id: 'b' }], next_cursor: 'cursor1' }, 1)).toThrow('catalogRepeatedCursor')
    expect(state.offset).toBe(1)
    expect(loaded).toEqual([{ id: 'a' }])
  })
  it('rejects inconsistent pages and total/accumulation limits', () => {
    expect(() => advanceCatalog(emptyCatalogPagination(), [], { items: [], has_more: true }, 50)).toThrow('catalogInvalidPage')
    expect(() => advanceCatalog(emptyCatalogPagination(), [], { items: [{ id: 1 }], total: 0 }, 50)).toThrow('catalogInvalidPage')
    expect(() => advanceCatalog(emptyCatalogPagination(), [], { items: [], total: MAX_CATALOG_ITEMS + 1 }, 50)).toThrow('catalogLimit')
    expect(() => advanceCatalog({ ...emptyCatalogPagination(), offset: MAX_CATALOG_ITEMS }, [], { items: [{ id: 1 }], total_known: false }, 50)).toThrow('catalogLimit')
  })
})
