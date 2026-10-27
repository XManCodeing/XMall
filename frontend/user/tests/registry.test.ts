import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

// Node has no Vite glob transform. Stub only the store and glob boundary while
// executing the real registry implementation, with no Vite config or env loading.
const source = readFileSync(new URL('../src/templates/registry.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source.replaceAll('import.meta.glob', 'templateGlob'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

function createRegistry(options: { config?: unknown; storageUnavailable?: boolean; storeUnavailable?: boolean } = {}) {
  const storage = new Map<string, string>()
  const store = { config: options.config }
  const location = { search: '' }
  const exports: Record<string, any> = {}
  const pageLoads: string[] = []
  runInNewContext(compiled, {
    exports,
    require: (id: string) => {
      assert.equal(id, '../stores/app')
      return { useAppStore: () => {
        if (options.storeUnavailable) throw new Error('Pinia unavailable')
        return store
      } }
    },
    templateGlob: (pattern: string) => {
      assert.ok(['./vault/**/*.vue', './orbit/**/*.vue'].includes(pattern))
      const template = pattern.split('/')[1]
      return Object.fromEntries(['Home', 'account/Profile'].map((name) => [
        `./${template}/${name}.vue`,
        async () => {
          const page = `${template}/${name}`
          pageLoads.push(page)
          return page
        },
      ]))
    },
    URLSearchParams,
    window: { location },
    localStorage: {
      getItem: (key: string) => {
        if (options.storageUnavailable) throw new Error('Storage unavailable')
        return storage.get(key) ?? null
      },
      setItem: (key: string, value: string) => {
        if (options.storageUnavailable) throw new Error('Storage unavailable')
        storage.set(key, value)
      },
      removeItem: (key: string) => {
        if (options.storageUnavailable) throw new Error('Storage unavailable')
        storage.delete(key)
      },
    },
  })
  return { registry: exports, storage, store, location, pageLoads }
}

const overrideKey = 'dj-storefront-template'

test('registry supports classic, vault and orbit while retaining the classic default', () => {
  const { registry } = createRegistry()
  assert.deepEqual(Array.from(registry.STOREFRONT_TEMPLATES), ['classic', 'vault', 'orbit'])
  assert.equal(registry.DEFAULT_TEMPLATE, 'classic')
  for (const template of ['classic', 'vault', 'orbit']) {
    assert.equal(createRegistry({ config: { storefront_template: template } }).registry.getActiveTemplate(), template)
  }
  for (const template of [undefined, null, '', 'unknown', 'Orbit', 42, {}]) {
    assert.equal(createRegistry({ config: { storefront_template: template } }).registry.getActiveTemplate(), 'classic')
  }
  assert.equal(registry.getActiveTemplate(), 'classic')
})

test('preview persists all supported templates and reset restores server configuration', () => {
  const { registry, storage, location, store } = createRegistry({ config: { storefront_template: 'vault' } })
  for (const template of ['orbit', 'classic', 'vault']) {
    location.search = `?template=${template}`
    registry.initTemplateOverride()
    assert.equal(storage.get(overrideKey), template)
    assert.equal(registry.getActiveTemplate(), template)
    assert.equal((store.config as any).storefront_template, 'vault')
    location.search = ''
    registry.initTemplateOverride()
    assert.equal(registry.getActiveTemplate(), template)
  }
  location.search = '?template=orbit'
  registry.initTemplateOverride()
  location.search = '?template=reset'
  registry.initTemplateOverride()
  assert.equal(storage.has(overrideKey), false)
  assert.equal(registry.getActiveTemplate(), 'vault')
})

test('unknown previews do not overwrite a valid preview and stale storage falls back to config', () => {
  const { registry, storage, location } = createRegistry({ config: { storefront_template: 'orbit' } })
  storage.set(overrideKey, 'vault')
  location.search = '?template=unknown'
  registry.initTemplateOverride()
  assert.equal(registry.getActiveTemplate(), 'vault')
  storage.set(overrideKey, 'unknown')
  assert.equal(registry.getActiveTemplate(), 'orbit')
})

test('unavailable storage and Pinia preserve safe fallback behavior', () => {
  const { registry, location } = createRegistry({ config: { storefront_template: 'orbit' }, storageUnavailable: true })
  for (const query of ['?template=orbit', '?template=reset']) {
    location.search = query
    assert.doesNotThrow(() => registry.initTemplateOverride())
    assert.equal(registry.getActiveTemplate(), 'orbit')
  }
  assert.equal(createRegistry({ storeUnavailable: true }).registry.getActiveTemplate(), 'classic')
})

test('view resolution is lazy, supports nested pages and falls back per missing page', async () => {
  const { registry, store, pageLoads } = createRegistry({ config: { storefront_template: 'classic' } })
  let classicLoads = 0
  const classicLoader = async () => { classicLoads++; return 'classic' }
  const home = registry.templateView('Home', classicLoader)
  assert.deepEqual(pageLoads, [])
  assert.equal(classicLoads, 0)
  assert.equal(await home(), 'classic')
  for (const template of ['vault', 'orbit']) {
    store.config = { storefront_template: template }
    assert.equal(await home(), `${template}/Home`)
    assert.equal(await registry.templateView('account/Profile', classicLoader)(), `${template}/account/Profile`)
    assert.equal(await registry.templateView('MissingPage', classicLoader)(), 'classic')
  }
  store.config = { storefront_template: 'unknown' }
  assert.equal(await home(), 'classic')
  assert.equal(classicLoads, 4)
  assert.deepEqual(pageLoads, ['vault/Home', 'vault/account/Profile', 'orbit/Home', 'orbit/account/Profile'])
})
