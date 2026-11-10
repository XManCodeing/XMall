import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { normalizeTabs, useAdminTabsStore } from './adminTabs'
import { useAdminAuthStore } from './auth'

vi.mock('@/api/admin', () => ({ adminAPI: {} }))

afterEach(() => { localStorage.clear(); vi.restoreAllMocks() })

describe('adminTabs store', () => {
  it('deduplicates paths without changing order and remembers the latest query and hash', () => {
    expect(normalizeTabs([
      { path: '/items/1', fullPath: '/items/1?page=1' },
      { path: '/items/2', fullPath: '/items/2' },
      { path: '/items/1', fullPath: '/items/1?page=2#details' },
    ])).toEqual([
      { path: '/items/1', fullPath: '/items/1?page=2#details' },
      { path: '/items/2', fullPath: '/items/2' },
    ])
  })

  it('retains only requested tabs and never persists business addresses', () => {
    setActivePinia(createPinia())
    const persist = vi.spyOn(Storage.prototype, 'setItem')
    const store = useAdminTabsStore()
    store.add({ path: '/items/1', fullPath: '/items/1?q=private' })
    store.add({ path: '/items/2', fullPath: '/items/2' })
    store.retain(['/items/2'])
    expect(store.tabs).toEqual([{ path: '/items/2', fullPath: '/items/2' }])
    expect(persist).not.toHaveBeenCalled()
  })

  it('clears pending refresh and invalidates all cached instances on logout', () => {
    setActivePinia(createPinia())
    const store = useAdminTabsStore()
    const auth = useAdminAuthStore()
    store.add({ path: '/items/1', fullPath: '/items/1' })
    store.refreshing = '/items/1'
    auth.token = 'test-session'
    const epoch = store.epoch
    auth.logout()
    expect(store.tabs).toEqual([])
    expect(store.refreshing).toBe('')
    expect(store.epoch).toBe(epoch + 1)
    expect(auth.token).toBe('')
    store.clear()
    expect(store.epoch).toBe(epoch + 2)
  })
})
