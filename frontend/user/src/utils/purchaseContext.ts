import { getGoogleRedirectSessionStorage, normalizeGoogleRedirectReturnPath } from './googleRedirect.ts'

export const PURCHASE_CONTEXT_KEY = 'xmall_purchase_return_v1'
export const PURCHASE_CONTEXT_TTL_MS = 10 * 60 * 1000
export const BUY_NOW_CONTEXT_KEY = 'xmall_buy_now_v1'
const MAX_ITEMS = 100
const MAX_BYTES = 32768

export interface PurchaseSelection {
  productId: number
  skuId: number
  slug: string
  quantity: number
}
export interface PurchaseContext {
  version: 1
  accountId: number
  issuedAt: number
  returnPath: string
  items: PurchaseSelection[]
}
export type ContextStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/** 校验站内购买返回地址。参数为不可信 URL；返回允许列表地址或空串。 */
export function purchaseReturnPath(value: unknown): string {
  if (typeof value !== 'string' || /[\s%\\#]/.test(value)) return ''
  const path = normalizeGoogleRedirectReturnPath(value, '')
  if (!path || path.length > 512 || /[\s%\\#]/.test(path)) return ''
  if (path === '/cart' || path === '/checkout' || path === '/checkout?mode=buynow') return path
  return /^\/products\/[A-Za-z0-9_-]{1,160}$/.test(path) ? path : ''
}

/** 从不可信对象提取 SKU/数量；额外金额、身份与表单字段一律丢弃。 */
export function purchaseSelection(value: unknown): PurchaseSelection | null {
  if (!value || typeof value !== 'object') return null
  const row = value as PurchaseSelection
  if (![row.productId, row.skuId, row.quantity].every((entry) => Number.isSafeInteger(entry) && entry > 0)) return null
  if (row.quantity > 1000000 || typeof row.slug !== 'string' || !/^[A-Za-z0-9_-]{1,160}$/.test(row.slug)) return null
  return { productId: row.productId, skuId: row.skuId, slug: row.slug, quantity: row.quantity }
}

/** 校验 TTL、账号隔离及有界条目。accountId 必须由调用方当前会话提供，不代表授权。 */
export function validatePurchaseContext(value: unknown, accountId: number, now = Date.now()): PurchaseContext | null {
  if (!value || typeof value !== 'object' || !Number.isSafeInteger(accountId) || accountId <= 0) return null
  const row = value as PurchaseContext
  if (row.version !== 1 || row.accountId !== accountId || !Number.isFinite(row.issuedAt)) return null
  const age = now - row.issuedAt
  if (!Number.isFinite(age) || age < 0 || age >= PURCHASE_CONTEXT_TTL_MS) return null
  const returnPath = purchaseReturnPath(row.returnPath)
  if (!returnPath || !Array.isArray(row.items) || row.items.length < 1 || row.items.length > MAX_ITEMS) return null
  const items = row.items.map(purchaseSelection)
  if (items.some((item) => !item)) return null
  const selections = items as PurchaseSelection[]
  if (new Set(selections.map((item) => `${item.productId}:${item.skuId}`)).size !== selections.length) return null
  if (returnPath === '/checkout?mode=buynow' && selections.length !== 1) return null
  return { version: 1, accountId, issuedAt: row.issuedAt, returnPath, items: selections }
}

/** 安全删除单个上下文；存储禁用时返回 false，不输出存储内容。 */
export function clearPurchaseContext(key = PURCHASE_CONTEXT_KEY, storage = getGoogleRedirectSessionStorage()): boolean {
  try {
    storage?.removeItem(key)
    return storage !== null
  } catch {
    console.warn('Purchase context storage unavailable')
    return false
  }
}

/** 写入最小上下文；返回持久化结果，失败不影响当前内存购物选择。 */
export function savePurchaseContext(value: PurchaseContext, key = PURCHASE_CONTEXT_KEY, storage: ContextStorage | null = getGoogleRedirectSessionStorage()): boolean {
  const context = validatePurchaseContext(value, value.accountId)
  if (!context || !storage) return false
  try {
    const serialized = JSON.stringify(context)
    if (serialized.length > MAX_BYTES) { storage.removeItem(key); return false }
    storage.removeItem(key)
    storage.setItem(key, serialized)
    return true
  } catch {
    console.warn('Purchase context could not be saved')
    return false
  }
}

/** 有界读取并校验上下文；consume=true 时先删除，避免回调重放。 */
export function readPurchaseContext(accountId: number, key = PURCHASE_CONTEXT_KEY, consume = false,
  storage: ContextStorage | null = getGoogleRedirectSessionStorage(), now = Date.now()): PurchaseContext | null {
  if (!storage) return null
  try {
    const raw = storage.getItem(key)
    if (consume) storage.removeItem(key)
    if (!raw) return null
    const context = raw.length <= MAX_BYTES ? validatePurchaseContext(JSON.parse(raw), accountId, now) : null
    if (!context) storage.removeItem(key)
    return context
  } catch {
    console.warn('Purchase context unavailable or invalid')
    // 不回显解析异常，避免日志意外包含被篡改的数据。
    try { storage.removeItem(key) } catch { return null }
    return null
  }
}
