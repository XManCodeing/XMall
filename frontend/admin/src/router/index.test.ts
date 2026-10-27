import { beforeEach, describe, expect, it, vi } from 'vitest'

const { auth, registered } = vi.hoisted(() => ({
  auth: {
    token: 'test-token',
    permissionsLoaded: true,
    isSuper: false,
    hasPermission: vi.fn(() => true),
    loadAuthz: vi.fn(),
    logout: vi.fn(),
  },
  registered: { guard: undefined as any, routes: [] as any[] },
}))

vi.mock('@/stores/auth', () => ({ useAdminAuthStore: () => auth }))
vi.mock('@/utils/adminBase', () => ({ ADMIN_BASE: '' }))
vi.mock('vue-router', () => ({
  createWebHistory: vi.fn(),
  createRouter: (options: { routes: any[] }) => {
    registered.routes = options.routes
    return { beforeEach: (guard: unknown) => { registered.guard = guard } }
  },
}))

import './index'

const financeRoutes = [
  'payments',
  'payment-channels',
  'wallet-config',
  'wallet-recharges',
  'reconciliation',
  'affiliates-withdraws',
  'affiliates-commissions',
  'resellers-operations',
  'resellers-ledger-entries',
  'resellers-balance-accounts',
  'resellers-withdraws',
]

function target(name: string) {
  const route = registered.routes.flatMap((item) => item.children || []).find((item) => item.name === name)
  expect(route, `Missing route: ${name}`).toBeDefined()
  return {
    name,
    path: `/${route.path}`,
    fullPath: `/${route.path}`,
    meta: { requiresAuth: true, ...route.meta },
  }
}

describe('admin finance navigation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    auth.token = 'test-token'
    auth.permissionsLoaded = true
    auth.hasPermission.mockReturnValue(true)
    auth.loadAuthz.mockReset()
  })

  it('loads permissions before allowing finance navigation', async () => {
    auth.permissionsLoaded = false
    expect(await registered.guard(target('payments'))).toBe(true)
    expect(auth.loadAuthz).toHaveBeenCalledOnce()
  })

  it('returns to login when authorization loading fails', async () => {
    auth.permissionsLoaded = false
    auth.loadAuthz.mockRejectedValueOnce(new Error('Session expired'))
    expect(await registered.guard(target('payments'))).toEqual({ path: '/login' })
    expect(auth.logout).toHaveBeenCalledOnce()
  })

  it.each(financeRoutes)('allows an authorized non-super administrator into %s without a declaration', async (name) => {
    expect(await registered.guard(target(name))).toBe(true)
  })

  it.each(financeRoutes)('still requires authentication for %s', async (name) => {
    auth.token = ''
    expect(await registered.guard(target(name))).toEqual({ path: '/login' })
  })

  it.each(financeRoutes)('still enforces permissions for %s', async (name) => {
    auth.hasPermission.mockReturnValue(false)
    const to = target(name)
    expect(to.meta.permission).toBeTruthy()
    expect(await registered.guard(to)).toEqual({ path: '/forbidden', query: { from: to.fullPath } })
  })
})
