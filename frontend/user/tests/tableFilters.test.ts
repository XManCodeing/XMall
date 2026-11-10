import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, reactive, ref } from 'vue'
import { parse, compileTemplate } from 'vue/compiler-sfc'

/** 读取真实 SFC。@param path user/src 下的相对路径。@returns SFC 源码。 */
function source(path: string): string {
  return readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
}

/** 执行页面真实 setup，替换边界依赖，禁止请求真实业务接口。@param path SFC 路径。@param dependencies 依赖替身。@param exposed 暴露的状态名。@returns 页面状态。 */
function setup(path: string, dependencies: Record<string, unknown>, exposed: string): any {
  const script = parse(source(path)).descriptor.scriptSetup!.content
    .replace(/import[\s\S]*?from ['"][^'"]+['"]\n/g, '')
  const compiled = ts.transpileModule(`${script}\n globalThis.result = { ${exposed} }`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  })
  const context: Record<string, any> = {
    computed, reactive, ref, onMounted: () => undefined,
    useI18n: () => ({ t: (key: string) => key, te: () => true }),
    useRouter: () => ({ push: () => undefined }), ...dependencies,
  }
  runInNewContext(compiled.outputText, context)
  return context.result
}

/** 建立可手动结束的异步请求。@returns Promise 和完成函数。 */
function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => { resolve = done })
  return { promise, resolve }
}

test('orders search/reset return to page one, preserve page size and wait for statistics', async () => {
  const loading = ref(false)
  const pagination = reactive({ page: 4, page_size: 50 })
  const requests: any[] = []
  const statistics: any[] = []
  let statsRequest = deferred()
  const view = setup('views/reseller/ResellerOrders.vue', {
    useResellerOrders: () => ({ loading, pagination,
      load: async (params: unknown) => { requests.push(params) },
      loadStats: async (params: unknown) => { statistics.push(params); await statsRequest.promise },
    }),
  }, 'filters, reload, resetFilters, goPage, queryLoading')
  Object.assign(view.filters, { order_no: 'ORDER-1', status: 'paid', created_from: '2026-11-01', created_to: '2026-11-07' })
  const search = view.reload()
  assert.equal(pagination.page, 1)
  assert.equal(pagination.page_size, 50)
  assert.equal(requests[0].order_no, 'ORDER-1')
  assert.equal(requests[0].page, 1)
  assert.equal(statistics[0].created_from, '2026-11-01')
  assert.equal(view.queryLoading.value, true)
  await view.reload()
  await view.resetFilters()
  await view.goPage(2)
  assert.equal(requests.length, 1)
  assert.equal(view.filters.status, 'paid')
  statsRequest.resolve()
  await search
  statsRequest = deferred()
  pagination.page = 4
  const reset = view.resetFilters()
  assert.equal(pagination.page, 1)
  for (const field of ['order_no', 'created_from', 'created_to']) assert.equal(view.filters[field], '')
  assert.equal(view.filters.status, 'all')
  assert.equal(requests.length, 2)
  for (const field of ['order_no', 'status', 'created_from', 'created_to']) assert.equal(requests[1][field], undefined)
  statsRequest.resolve()
  await reset
  await view.reload()
  assert.equal(requests.length, 3, 'unchanged search still refreshes once')
})

test('ledger reset clears every query field once and blocks pending reset/search', async () => {
  const ledgerLoading = ref(false)
  const ledgerPagination = reactive({ page: 5, page_size: 20 })
  const requests: any[] = []
  const pending = deferred()
  const view = setup('views/reseller/ResellerLedger.vue', {
    useResellerFinance: () => ({ ledgerLoading, ledgerPagination,
      loadLedgerEntries: async (params: unknown) => {
        ledgerLoading.value = true
        requests.push(params)
        await pending.promise
        ledgerLoading.value = false
      },
    }),
  }, 'filters, reload, resetFilters, goPage')
  Object.assign(view.filters, { type: 'order_profit', status: 'available', order_id: 42 })
  const search = view.reload()
  assert.equal(ledgerPagination.page, 1)
  assert.equal(requests[0].order_id, 42)
  await view.resetFilters()
  await view.reload()
  await view.goPage(2)
  assert.equal(requests.length, 1)
  assert.equal(view.filters.order_id, 42)
  pending.resolve()
  await search
  ledgerPagination.page = 5
  await view.resetFilters()
  assert.equal(ledgerPagination.page, 1)
  assert.equal(ledgerPagination.page_size, 20)
  assert.equal(view.filters.type, 'all')
  assert.equal(view.filters.status, 'all')
  assert.equal(view.filters.order_id, undefined)
  assert.equal(requests.length, 2)
  for (const field of ['type', 'status', 'order_id']) assert.equal(requests[1][field], undefined)
})

test('shared filter form provides native Enter submit and guarded search/reset with disabled controls', () => {
  const path = 'components/reseller-console/ResellerFilterBar.vue'
  const props = reactive({ loading: false })
  const events: string[] = []
  const view = setup(path, {
    defineProps: () => props, withDefaults: (value: unknown) => value,
    defineEmits: () => (event: string) => events.push(event),
  }, 'search, reset')
  view.search()
  view.reset()
  assert.deepEqual(events, ['search', 'reset'])
  props.loading = true
  view.search()
  view.reset()
  assert.equal(events.length, 2)
  const template = parse(source(path)).descriptor.template!.content
  assert.match(template, /@submit\.prevent="search"/)
  assert.match(template, /<fieldset :disabled="loading"/)
  assert.match(template, /type="submit"[^>]*:disabled="loading"/)
  assert.match(template, /type="button"[^>]*:disabled="loading"[^>]*@click="reset"/)
  for (const page of ['ResellerOrders', 'ResellerLedger']) {
    const text = source(`views/reseller/${page}.vue`)
    assert.match(text, /<ResellerFilterBar :loading=/)
    assert.doesNotMatch(text, /watch\(/, 'explicit search must not add watcher-driven duplicate requests')
    const result = compileTemplate({ source: parse(text).descriptor.template!.content, filename: `${page}.vue`, id: page })
    assert.deepEqual(result.errors, [])
  }
})
