// Orbit-only navigation policy. No browser globals so route decisions are testable.
export const orbitRootPaths = new Set(['/', '/products', '/cart', '/me'])

export function orbitKeyboardVisible(mobile: boolean, editing: boolean, layoutHeight: number, visibleHeight: number, scale: number) {
  return mobile && editing && scale === 1 && layoutHeight - visibleHeight > 120
}

export function orbitNavigationMotion(from: string, to: string, previous: number | null, current: number | null) {
  if (from === to || (orbitRootPaths.has(from) && orbitRootPaths.has(to))) return 'neutral'
  if (previous !== null && current !== null && current < previous) return 'back'
  if (orbitParentPath(from, {}) === to) return 'back'
  return 'forward'
}

export function safeOrbitHistoryPath(value: unknown): string | null {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\\s]/.test(value)) return null
  return value
}

export function orbitParentPath(path: string, query: Record<string, unknown>, productSlug?: string): string {
  const text = (key: string) => typeof query[key] === 'string' ? (query[key] as string).trim() : ''
  if (path === '/pay') {
    const order = text('order_no') || text('out_trade_no')
    const recharge = text('recharge_no') || (/^WR/i.test(order) ? order : '')
    if (recharge) return `/recharge-orders/${encodeURIComponent(recharge)}`
    const base = text('guest') === '1' || text('guest') === 'true' ? '/guest/orders' : '/orders'
    return order ? `${base}/${encodeURIComponent(order)}` : base === '/orders' ? '/me/orders' : base
  }
  if (path === '/checkout') return productSlug ? `/products/${encodeURIComponent(productSlug)}` : '/cart'
  if (path.startsWith('/products/') || path.startsWith('/categories/')) return '/products'
  if (path.startsWith('/guest/orders/')) return '/guest/orders'
  if (path.startsWith('/orders/')) return '/me/orders'
  if (path.startsWith('/recharge-orders/')) return '/me/wallet'
  if (path.startsWith('/me/')) return '/me'
  if (path.startsWith('/blog/')) return '/blog'
  if (path === '/auth/register' || path === '/auth/forgot') return '/auth/login'
  return '/'
}
