import { afterEach, describe, expect, it } from 'vitest'
import { computed, createApp, defineComponent, h, nextTick, provide, ref, type App } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ConfirmDialogHost from './ConfirmDialogHost.vue'
import { pageDialogTargetKey } from './ui/dialog/pageScope'
import { useConfirmStore } from '@/stores/confirm'

let app: App | undefined
let root: HTMLElement | undefined
/** 等待确认框传送及焦点同步。@returns 挂载完成。 */
async function settle() { await nextTick(); await new Promise(resolve => setTimeout(resolve, 30)); await nextTick() }
afterEach(() => { app?.unmount(); root?.remove(); document.body.innerHTML = '' })

describe('page-scoped action confirmation', () => {
  it('uses the shared page host and cancels the old action when switching routes', async () => {
    const pinia = createPinia()
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/first', component: { render: () => null } },
      { path: '/second', component: { render: () => null } },
    ] })
    await router.push('/first'); await router.isReady()
    const host = ref<HTMLElement | null>(null)
    const Harness = defineComponent({ setup() {
      provide(pageDialogTargetKey, computed(() => host.value))
      return () => h('section', [h('main'), h('div', { ref: host }), h(ConfirmDialogHost)])
    } })
    root = document.createElement('div'); document.body.append(root)
    app = createApp(Harness).use(pinia).use(router); app.mount(root)
    await settle()
    const store = useConfirmStore(pinia)
    const pending = store.ask({ title: 'Confirm action', description: 'Only for the first page', confirmText: 'Confirm', cancelText: 'Cancel', variant: 'destructive' })
    await settle()
    expect(host.value?.querySelector('[role="dialog"]')).not.toBeNull()
    expect(root.querySelector('main')!.inert).toBe(true)
    expect(document.body.style.pointerEvents).not.toBe('none')
    await router.push('/second'); await settle()
    expect(await pending).toBe(false)
    expect(store.open).toBe(false)
    expect(host.value?.querySelector('[role="dialog"]')).toBeNull()
    expect(root.querySelector('main')!.inert).toBeFalsy()
  })
})
