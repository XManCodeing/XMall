import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

const entry = readFileSync(new URL('../src/main.ts', import.meta.url), 'utf8')
  .replace(/^import .*$/gm, '')

const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>(done => { resolve = done })
  return { promise, resolve }
}

for (const delayed of ['route', 'locale', 'telegram'] as const) {
  test(`initial mount waits for ${delayed} readiness`, async () => {
    const ready = { route: deferred(), locale: deferred(), telegram: deferred() }
    const mounts: string[] = []
    const app = { use() {}, mount: (selector: string) => mounts.push(selector) }
    runInNewContext(entry, {
      createApp: () => app,
      createHead: () => ({}),
      createPinia: () => ({}),
      App: {},
      router: { isReady: () => ready.route.promise },
      i18n: {},
      initTemplateOverride() {},
      detectLocale: () => 'zh-CN',
      setI18nLocale: () => ready.locale.promise,
      useTelegramMiniAppStore: () => ({ init: () => ready.telegram.promise }),
      warmupCommonRoutes() {},
      warmupLocaleMessages() {},
    })
    for (const [name, gate] of Object.entries(ready)) {
      if (name !== delayed) gate.resolve()
    }
    await new Promise(resolve => setImmediate(resolve))
    assert.deepEqual(mounts, [])
    ready[delayed].resolve()
    await new Promise(resolve => setImmediate(resolve))
    assert.deepEqual(mounts, ['#app'])
  })
}
