import { afterEach, describe, expect, it } from 'vitest'
import { computed, provide, createApp, defineComponent, h, KeepAlive, nextTick, ref, type App } from 'vue'
import { pageDialogTargetKey, lockPageBackground } from './pageScope'
import { Sheet, SheetContent } from '../sheet'
import { Dialog, DialogScrollContent, DialogContent, DialogTitle, DialogDescription } from '.'

let app: App | undefined
let root: HTMLElement | undefined
/** 等待浮层挂载和缓存钩子完成。@returns 完成通知 */
async function settle() { await nextTick(); await new Promise(resolve => setTimeout(resolve, 30)); await nextTick() }
afterEach(() => { app?.unmount(); root?.remove(); document.body.innerHTML = '' })

describe('cached dialog lifecycle', () => {
  it.each([[Dialog, DialogContent], [Dialog, DialogScrollContent], [Sheet, SheetContent]])('automatically scopes cached dialogs and sheets while preserving explicit global modals', async (Root, Content) => {
    const active = ref(true)
    const open = ref(true)
    const globalOpen = ref(false)
    const host = ref<HTMLElement | null>(null)
    const draft = ref('retained draft')
    const Page = defineComponent({ setup: () => () => h(Root, {
      open: open.value, 'onUpdate:open': (value: boolean) => { open.value = value },
    }, () => h(Content, {}, () => [h(DialogTitle, () => 'Editor'), h(DialogDescription, () => 'Page editor'), h('input', { value: draft.value })])) })
    const Other = defineComponent({ render: () => h('div', 'Other') })
    root = document.createElement('div'); document.body.append(root)
    app = createApp({ setup() {
      provide(pageDialogTargetKey, computed(() => host.value))
      return () => h('div', [
        h('header', [h('button', { onClick: () => { active.value = !active.value } }, 'Switch')]),
        h('section', [h('main', [h(KeepAlive, null, () => h(active.value ? Page : Other))]), h('div', { ref: host, 'data-host': '' })]),
        h(Dialog, { scope: 'global', open: globalOpen.value }, () => h(DialogContent, {}, () => [h(DialogTitle, () => 'Confirm'), h(DialogDescription, () => 'Global confirmation')])),
      ])
    } })
    app.mount(root); await settle()
    expect(host.value?.querySelector('[role="dialog"]')).not.toBeNull()
    expect(root.querySelector('main')!.inert).toBe(true)
    const button = root.querySelector('header button') as HTMLButtonElement
    button.focus(); await settle()
    expect(document.activeElement).toBe(button)
    expect(open.value).toBe(true)
    button.click(); await settle()
    expect(host.value?.querySelector('[role="dialog"]')).toBeNull()
    expect(root.querySelector('main')!.inert).toBeFalsy()
    button.click(); await settle()
    expect((host.value?.querySelector('input') as HTMLInputElement).value).toBe('retained draft')
    globalOpen.value = true; await settle()
    expect([...document.querySelectorAll('[role="dialog"]')].some(node => !host.value?.contains(node))).toBe(true)
  })

  it('keeps page-marked children inside a global modal on the global portal', async () => {
    const host = document.createElement('div'); document.body.append(host)
    root = document.createElement('div'); document.body.append(root)
    app = createApp({ setup() {
      provide(pageDialogTargetKey, computed(() => host))
      return () => h(Dialog, { scope: 'global', open: true }, () => h(DialogContent, {}, () => [
        h(DialogTitle, () => 'Global'), h(DialogDescription, () => 'Global parent'),
        h(Dialog, { scope: 'page', open: true }, () => h(DialogContent, {}, () => [h(DialogTitle, () => 'Child'), h(DialogDescription, () => 'Nested child')])),
      ]))
    } })
    app.mount(root); await settle()
    expect(document.querySelectorAll('[role="dialog"]').length).toBe(2)
    expect(host.querySelector('[role="dialog"]')).toBeNull()
  })

  it('only releases the background after the last nested page dialog closes', () => {
    const section = document.createElement('section')
    const main = document.createElement('main')
    const host = document.createElement('div')
    section.append(main, host)
    const releaseFirst = lockPageBackground(host)
    const releaseSecond = lockPageBackground(host)
    releaseFirst(); expect(main.inert).toBe(true)
    releaseSecond(); expect(main.inert).toBeFalsy()
  })

  it('hides portals while inactive without closing the form or losing its draft', async () => {
    const active = ref(true)
    const open = ref(true)
    const draft = ref('draft')
    const Page = defineComponent({ setup: () => () => h(Dialog, {
      open: open.value, 'onUpdate:open': (value: boolean) => { open.value = value },
    }, () => h(DialogContent, {}, () => [
      h(DialogTitle, () => 'Draft'), h(DialogDescription, () => 'Cached form'), h('input', { value: draft.value }),
    ])) })
    const Other = defineComponent({ render: () => h('div', 'Other') })
    root = document.createElement('div'); document.body.append(root)
    app = createApp({ render: () => h(KeepAlive, null, () => h(active.value ? Page : Other)) })
    app.mount(root); await settle()
    expect(document.querySelector('[role="dialog"] input')).not.toBeNull()
    active.value = false; await settle()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(open.value).toBe(true)
    active.value = true; await settle()
    expect((document.querySelector('[role="dialog"] input') as HTMLInputElement)?.value).toBe('draft')
  })
})
