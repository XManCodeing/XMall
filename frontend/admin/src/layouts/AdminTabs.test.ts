import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, type App } from 'vue'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { useAdminAuthStore } from '@/stores/auth'
import { useAdminTabsStore } from '@/stores/adminTabs'
import AdminTabs from './AdminTabs.vue'

vi.mock('@/api/admin', () => ({ adminAPI: {} }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn() }))
vi.mock('@/components/ui/select', () => {
  const passthrough = defineComponent({ setup: (_, { slots }) => () => h('div', slots.default?.()) })
  return {
    Select: defineComponent({
      props: ['modelValue', 'disabled'], emits: ['update:modelValue'],
      setup: (props, { emit }) => () => h('select', {
        'data-actions': '', disabled: props.disabled, value: props.modelValue,
        onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value),
      }, ['', 'refresh', 'current', 'others', 'all'].map(value => h('option', { value }, value))),
    }),
    SelectContent: passthrough, SelectItem: passthrough, SelectTrigger: passthrough, SelectValue: passthrough,
  }
})

let app: App | undefined
let root: HTMLDivElement

/** Drain router promise chains and Vue jobs. @returns Settled event loop. */
async function settle() {
  await new Promise<void>(resolve => setTimeout(resolve, 0))
  await nextTick()
}

/** Mount real tab logic with a memory router. @param initial Initial URL. @returns Router and store. */
async function mount(initial = '/items/1') {
  const pinia = createPinia()
  const page = defineComponent({ render: () => h('div') })
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/', component: page, meta: { requiresAuth: true } },
    { path: '/items/:id', component: page, meta: { requiresAuth: true } },
    { path: '/login', component: page, meta: { adminTab: false } },
  ] })
  await router.push(initial)
  await router.isReady()
  root = document.createElement('div'); document.body.append(root)
  app = createApp(AdminTabs, { navigation: [{ label: 'Home', to: '/' }, { label: 'Items', to: '/items/1' }] })
  app.use(pinia).use(router).use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
  app.mount(root)
  await settle()
  return { router, store: useAdminTabsStore(pinia) }
}

/** Find a tab's close control. @param path Registered path. @returns Close button. */
function closeButton(path: string) {
  const store = useAdminTabsStore()
  const position = store.tabs.findIndex(tab => tab.path === path)
  const button = root.querySelectorAll<HTMLButtonElement>('button[aria-label]')[position]
  if (!button) throw new Error(`Missing close control for ${path}`)
  return button
}

/** Choose a toolbar operation. @param action Operation key. @returns Settled UI. */
async function perform(action: string) {
  const select = root.querySelector<HTMLSelectElement>('[data-actions]')!
  select.value = action
  select.dispatchEvent(new Event('change'))
  await settle()
}

afterEach(() => { app?.unmount(); app = undefined; root?.remove(); localStorage.clear(); vi.restoreAllMocks() })

describe('AdminTabs navigation and safe closing', () => {
  it('clears labels and cached generations on session replacement', async () => {
    const { store } = await mount()
    const previous = store.epoch
    useAdminAuthStore().token = 'replacement-test-session'
    await settle()
    expect(store.epoch).toBeGreaterThan(previous)
    expect(store.tabs.map(tab => tab.path)).toEqual(['/items/1'])
    useAdminAuthStore().logout()
    await settle()
    expect(store.tabs).toEqual([])
  })

  it('closes all from the default page without retaining other tabs', async () => {
    const { router, store } = await mount()
    await router.push('/'); await settle()
    await perform('all')
    expect(store.tabs.map(tab => tab.path)).toEqual(['/'])
    expect(store.refreshing).toBe('/')
  })

  it('records only successful visits and navigates back to the latest query/hash', async () => {
    const { router, store } = await mount('/items/1?page=1')
    await router.push('/items/1?page=2#detail'); await settle()
    await router.push('/items/2'); await settle()
    expect(store.tabs).toHaveLength(2)
    root.querySelector<HTMLButtonElement>('button[title="Items"]')!.click()
    await settle()
    expect(router.currentRoute.value.fullPath).toBe('/items/1?page=2#detail')
    router.beforeEach(to => to.path === '/items/3' ? false : true)
    await router.push('/items/3'); await settle()
    expect(store.tabs.map(tab => tab.path)).toEqual(['/items/1', '/items/2'])
    await router.push('/login'); await settle()
    expect(store.tabs.map(tab => tab.path)).toEqual(['/items/1', '/items/2'])
  })

  it.each(['cancel', 'throw'])('keeps all tabs when close navigation fails: %s', async failure => {
    const { router, store } = await mount()
    await router.push('/items/2'); await settle()
    const before = [...store.tabs]
    router.onError(() => undefined)
    router.beforeEach(() => { if (failure === 'throw') throw new Error('Unsaved changes'); return false })
    closeButton('/items/2').click(); await settle()
    expect(router.currentRoute.value.path).toBe('/items/2')
    expect(store.tabs).toEqual(before)
    expect(closeButton('/items/2').disabled).toBe(false)
    await perform('all')
    expect(store.tabs).toEqual(before)
  })

  it('waits for asynchronous guards before removing the active tab', async () => {
    const { router, store } = await mount()
    await router.push('/items/2'); await settle()
    let allowNavigation!: (allowed: boolean) => void
    router.beforeEach(() => new Promise<boolean>(resolve => { allowNavigation = resolve }))
    closeButton('/items/2').click(); await settle()
    expect(store.tabs).toHaveLength(2)
    expect(closeButton('/items/2').disabled).toBe(true)
    allowNavigation(true); await settle()
    expect(router.currentRoute.value.path).toBe('/items/1')
    expect(store.tabs.map(tab => tab.path)).toEqual(['/items/1'])
  })

  it('closes inactive tabs without navigation and preserves the actual redirected target', async () => {
    const { router, store } = await mount()
    await router.push('/items/2'); await router.push('/items/3'); await settle()
    const guard = vi.fn((to: { path: string }) => to.path === '/items/2' ? '/' : true)
    router.beforeEach(guard)
    closeButton('/items/1').click(); await settle()
    expect(guard).not.toHaveBeenCalled()
    closeButton('/items/3').click(); await settle()
    expect(router.currentRoute.value.path).toBe('/')
    expect(store.tabs.map(tab => tab.path)).toEqual(['/items/2', '/'])
  })

  it('supports close others, refresh current, and close all fallback', async () => {
    const { router, store } = await mount()
    await router.push('/items/2'); await settle()
    await perform('others')
    expect(store.tabs.map(tab => tab.path)).toEqual(['/items/2'])
    await perform('refresh')
    expect(store.refreshing).toBe('/items/2')
    expect(store.tabs).toHaveLength(1)
    store.refreshing = ''; await settle()
    await perform('all')
    expect(router.currentRoute.value.path).toBe('/')
    expect(store.tabs.map(tab => tab.path)).toEqual(['/'])
  })
})
