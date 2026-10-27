export interface CatalogPage<T> {
  items: T[]
  total?: number
  total_known?: boolean
  has_more?: boolean | null
  next_cursor?: string
}
export interface CatalogPagination {
  page: number
  offset: number
  total: number | null
  hasMore: boolean
  cursor: string
  seenCursors: string[]
}
export const MAX_CATALOG_ITEMS = 10000
export const emptyCatalogPagination = (): CatalogPagination => ({ page: 0, offset: 0, total: null, hasMore: false, cursor: '', seenCursors: [] })

// Validate before appending, so a malformed/repeated page never corrupts already loaded data.
export function advanceCatalog<T extends { id: string | number }>(
  state: CatalogPagination, loaded: T[], page: CatalogPage<T>, pageSize: number,
): CatalogPagination {
  const known = page.total_known ?? (typeof page.total === 'number' && Number.isFinite(page.total) && page.total >= 0)
  const total = known && typeof page.total === 'number' && Number.isFinite(page.total) && page.total >= 0 ? page.total : null
  const offset = state.offset + page.items.length
  if (offset > MAX_CATALOG_ITEMS || (total !== null && total > MAX_CATALOG_ITEMS)) throw new Error('httpIntegration.catalogLimit')
  const ids = new Set(loaded.map(item => item.id))
  for (const item of page.items) {
    if (ids.has(item.id)) throw new Error('httpIntegration.catalogRepeatedPage')
    ids.add(item.id)
  }
  const cursor = page.next_cursor || ''
  if (cursor && (cursor === state.cursor || state.seenCursors.includes(cursor))) throw new Error('httpIntegration.catalogRepeatedCursor')
  const hasMore = typeof page.has_more === 'boolean' ? page.has_more
    : cursor ? true : total !== null ? offset < total : page.items.length >= pageSize
  if ((!page.items.length && hasMore) || (total !== null && offset > total)) throw new Error('httpIntegration.catalogInvalidPage')
  if (offset >= MAX_CATALOG_ITEMS && hasMore) throw new Error('httpIntegration.catalogLimit')
  return { page: state.page + 1, offset, total, hasMore, cursor, seenCursors: cursor ? [...state.seenCursors, cursor] : state.seenCursors }
}
export function catalogQuery(state: CatalogPagination, pageSize: number) {
  return { page: state.page + 1, page_size: pageSize, offset: state.offset, ...(state.cursor ? { cursor: state.cursor } : {}) }
}
