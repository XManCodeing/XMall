import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, onUnmounted, ref, watch, type App } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, useRoute } from 'vue-router'
import { useAdminTabsStore } from '@/stores/adminTabs'
import { useAdminAuthStore } from '@/stores/auth'
import AdminRouteCache from './AdminRouteCache.vue'
import { createRoutePageCache, ROUTE_CACHE_LIMIT } from './RoutePage'

vi.mock('@/api/admin', () => ({ adminAPI: {} }))

let app: App | undefined
let root: HTMLDivElement

/** Settle reactive cache teardown and router jobs. @returns Settled microtasks. */
async function settle() { for (let turn = 0; turn < 12; turn++) await nextTick() }

/** Mount stateful pages using the production cache. @returns Navigation and lifecycle probes. */
async function mount() {
  const created: string[] = []
  const destroyed: string[] = []
  const observed = new Map<string, string[]>()
  const page = defineComponent({
    name: 'RegressionStatefulPage',
    setup() {
      const route = useRoute()
      const identity = route.path
      const draft = ref('')
      created.push(identity)
      observed.set(identity, [])
      watch(() => route.fullPath, value => observed.get(identity)!.push(value), { immediate: true, flush: 'sync' })
      onUnmounted(() => destroyed.push(identity))
      return () => h('section', { 'data-page': identity }, [
        h('span', { 'data-route': '' }, `${route.params.id ?? ''}:${route.query.filter ?? ''}`),
        h('input', { value: draft.value, onInput: (event: Event) => { draft.value = (event.target as HTMLInputElement).value } }),
      ])
    },
  })
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/items/:id', component: page },
    { path: '/login', component: defineComponent({ render: () => h('div', 'Login') }) },
  ] })
  const pinia = createPinia()
  const store = useAdminTabsStore(pinia)
  const scrollElement = document.createElement('div')
  // Production tab registration happens only after confirmed navigation.
  router.afterEach((to, _from, failure) => {
    if (!failure && to.path !== '/login') store.add({ path: to.path, fullPath: to.fullPath })
  })
  await router.push('/items/1?filter=first'); await router.isReady()
  root = document.createElement('div'); document.body.append(root)
  app = createApp(AdminRouteCache, { scrollElement })
  app.use(pinia).use(router); app.mount(root); await settle()
  /** Navigate and finish Vue cache work. @param path Destination. @returns Settled route. */
  async function visit(path: string) { await router.push(path); await settle() }
  return { router, store, scrollElement, created, destroyed, observed, visit, auth: useAdminAuthStore(pinia) }
}

/** Edit the active page's draft. @param value Draft text. @returns Settled view. */
async function edit(value: string) {
  const input = root.querySelector('input')!
  input.value = value; input.dispatchEvent(new Event('input')); await settle()
}

afterEach(() => { app?.unmount(); app = undefined; root?.remove(); localStorage.clear() })

describe('AdminRouteCache lifecycle', () => {
  it('retains drafts and scroll while isolating inactive dynamic-route params and queries', async () => {
    const { visit, observed, created, scrollElement } = await mount()
    await edit('first draft'); scrollElement.scrollTop = 180
    await visit('/items/2?filter=second')
    expect(root.querySelector('[data-route]')!.textContent).toBe('2:second')
    expect(root.querySelector('input')!.value).toBe('')
    expect(scrollElement.scrollTop).toBe(0)
    await edit('second draft'); scrollElement.scrollTop = 260
    expect(observed.get('/items/1')).toEqual(['/items/1?filter=first'])
    await visit('/items/1?filter=updated')
    expect(root.querySelector('input')!.value).toBe('first draft')
    expect(root.querySelector('[data-route]')!.textContent).toBe('1:updated')
    expect(scrollElement.scrollTop).toBe(180)
    expect(observed.get('/items/2')).toEqual(['/items/2?filter=second'])
    expect(created).toEqual(['/items/1', '/items/2'])
    await visit('/items/2?filter=second')
    expect(root.querySelector('input')!.value).toBe('second draft')
    expect(scrollElement.scrollTop).toBe(260)
  })

  it('reuses the current instance for query-only navigation', async () => {
    const { visit, created, observed } = await mount()
    await edit('unsaved')
    await visit('/items/1?filter=changed#section')
    expect(created).toEqual(['/items/1'])
    expect(root.querySelector('input')!.value).toBe('unsaved')
    expect(root.querySelector('[data-route]')!.textContent).toBe('1:changed')
    expect(observed.get('/items/1')).toContain('/items/1?filter=changed#section')
  })

  it('unmounts closed inactive pages and reopens them without stale state or scroll', async () => {
    const { visit, store, destroyed, scrollElement } = await mount()
    await edit('discard me'); scrollElement.scrollTop = 300
    await visit('/items/2')
    store.retain(['/items/2']); await settle()
    expect(destroyed).toEqual(['/items/1'])
    await visit('/items/1')
    expect(root.querySelector('input')!.value).toBe('')
    expect(scrollElement.scrollTop).toBe(0)
  })

  it('refreshes only the current page and preserves other cached drafts', async () => {
    const { visit, store, created, destroyed, scrollElement } = await mount()
    await edit('preserved')
    await visit('/items/2'); await edit('reset me'); scrollElement.scrollTop = 150
    store.refreshing = '/items/2'; await settle()
    expect(store.refreshing).toBe('')
    expect(destroyed).toEqual(['/items/2'])
    expect(created.filter(path => path === '/items/2')).toHaveLength(2)
    expect(root.querySelector('input')!.value).toBe('')
    expect(scrollElement.scrollTop).toBe(0)
    expect(store.tabs).toHaveLength(2)
    await visit('/items/1')
    expect(root.querySelector('input')!.value).toBe('preserved')
  })

  it('limits live page instances to ten and evicts the least recently used page', async () => {
    const { visit, created, destroyed, scrollElement } = await mount()
    await edit('recent page'); scrollElement.scrollTop = 90
    for (let identity = 2; identity <= ROUTE_CACHE_LIMIT; identity++) await visit(`/items/${identity}`)
    expect(created.length - destroyed.length).toBe(10)
    await visit('/items/1?filter=first')
    expect(root.querySelector('input')!.value).toBe('recent page')
    await visit('/items/11')
    expect(destroyed).toContain('/items/2')
    expect(destroyed).not.toContain('/items/1')
    expect(created.length - destroyed.length).toBe(10)
    await visit('/items/2')
    expect(root.querySelector('input')!.value).toBe('')
    expect(scrollElement.scrollTop).toBe(0)
    expect(created.filter(path => path === '/items/2')).toHaveLength(2)
  })

  it('releases all old session instances on logout and starts with clean state', async () => {
    const { visit, auth, store, destroyed } = await mount()
    await edit('private draft'); await visit('/items/2')
    auth.logout(); await visit('/login')
    expect(store.tabs).toEqual([])
    expect(store.refreshing).toBe('')
    expect(destroyed).toContain('/items/1')
    expect(destroyed).toContain('/items/2')
    await visit('/items/1')
    expect(root.querySelector('input')!.value).toBe('')
  })
})

describe('route wrapper definition cache', () => {
  it('uses an LRU bound and removes closed definitions', () => {
    const cache = createRoutePageCache(2)
    const first = cache.get('/items/1')
    const second = cache.get('/items/2')
    expect(cache.get('/items/1')).toBe(first)
    cache.get('/items/3')
    expect(cache.get('/items/1')).toBe(first)
    expect(cache.get('/items/2')).not.toBe(second)
    cache.retain(['/items/2'])
    expect(cache.get('/items/1')).not.toBe(first)
    const current = cache.get('/items/2')
    cache.clear()
    expect(cache.get('/items/2')).not.toBe(current)
  })
})
