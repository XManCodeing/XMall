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

describe('Telegram instance navigation', () => {
  it('has one management page and redirects the former status entry', () => {
    const children = registered.routes.flatMap((item) => item.children || [])
    const management = children.find((item) => item.name === 'telegram-bot')
    expect(management?.meta.permission).toBe('GET:/admin/telegram-bot/instances')
    const status = children.find((item) => item.name === 'telegram-bot-status')
    expect(status?.redirect).toEqual({ name: 'telegram-bot' })
    expect(status?.component).toBeUndefined()
    const clients = children.find((item) => item.name === 'telegram-bot-channel-clients')
    expect(clients?.redirect).toEqual({ name: 'telegram-bot' })
    expect(clients?.component).toBeUndefined()
  })

  it.each(['/telegram-bot/status', '/telegram-bot/channel-clients'])(
    'redirects %s through the instance permission guard',
    async (path) => {
      const { createRouter, createMemoryHistory } = await vi.importActual<typeof import('vue-router')>('vue-router')
      const children = registered.routes.flatMap((item) => item.children || [])
      const router = createRouter({
        history: createMemoryHistory('/admin/'),
        routes: [
          ...children.filter((item) => ['telegram-bot', 'telegram-bot-status', 'telegram-bot-channel-clients'].includes(item.name)).map((item) => ({
            ...item,
            path: `/${item.path}`,
            ...(item.redirect ? {} : { component: { render: () => null } }),
            meta: { requiresAuth: true, ...item.meta },
          })),
          { path: '/forbidden', component: { render: () => null } },
          { path: '/login', component: { render: () => null } },
        ],
      })
      router.beforeEach(registered.guard)
      auth.token = 'test-token'
      auth.permissionsLoaded = true
      auth.hasPermission.mockReturnValue(true)
      try {
        await router.push(`${path}?from=bookmark#instances`)
        expect(router.currentRoute.value.path).toBe('/telegram-bot')
        expect(router.currentRoute.value.query).toEqual({ from: 'bookmark' })
        expect(router.currentRoute.value.hash).toBe('#instances')
        auth.hasPermission.mockReturnValue(false)
        await router.push(path)
        expect(router.currentRoute.value.path).toBe('/forbidden')
        expect(auth.hasPermission).toHaveBeenLastCalledWith('GET:/admin/telegram-bot/instances')
        auth.token = ''
        await router.push(path)
        expect(router.currentRoute.value.path).toBe('/login')
      } finally {
        auth.token = 'test-token'
        auth.hasPermission.mockReturnValue(true)
      }
    },
  )
})

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
