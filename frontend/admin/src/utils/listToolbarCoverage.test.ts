import { describe, expect, it } from 'vitest'

const pages = import.meta.glob<string>('../views/admin/**/*.vue', { query: '?raw', import: 'default', eager: true })

/** 只识别查询条件，不把编辑表单或表格行内输入误当搜索栏。@param source 页面源码。@returns 是否属于筛选表格。 */
function hasTableQuery(source: string): boolean {
  return /<(?:Table|table)(?:\s|>)/.test(source)
    && /v-model(?::[\w-]+)?="(?:filters\.|notificationLogFilters\.|searchQuery|searchKeyword|filterStatus|filterKeyword|botFilter|stateFilter|purposeFilter|reviewKeyword|orderNo|keyword|adminKeyword|catalogKeyword|scopeFilterKeyword|skuFilterValue)/.test(source)
}

describe('table filter toolbar coverage', () => {
  it('requires both search and reset actions on every detected filtered table page', () => {
    let checked = 0
    for (const [path, source] of Object.entries(pages)) {
      if (!hasTableQuery(source)) continue
      const template = source.slice(source.indexOf('<template>'))
      const buttons = [...template.matchAll(/<Button\b[^>]*>[\s\S]*?<\/Button>/g)].map(match => match[0])
      expect.soft(buttons.some(button => /(?:\.search|actions.search|searchLabel)/i.test(button) && /@click=/.test(button)), `${path} 缺少搜索按钮`).toBe(true)
      expect.soft(buttons.some(button => /(?:\.reset|actions.reset|resetLabel)/i.test(button) && /@click=/.test(button)), `${path} 缺少重置按钮`).toBe(true)
      checked++
    }
    expect(checked).toBeGreaterThan(20)
  })

  it.each([
    ['Authz.vue', 'adminKeyword'],
    ['Authz.vue', 'catalogKeyword'],
  ])('keeps search and reset next to the %s %s query field', (file, field) => {
    const source = pages[`../views/admin/${file}`]!
    const position = source.indexOf(`v-model="${field}"`)
    expect(position, `${file}: 查询字段不存在`).toBeGreaterThan(-1)
    const toolbar = source.slice(position, source.indexOf('<Table', position))
    expect(toolbar).toMatch(/@click="[^"]*(?:[Ss]earch|[Ff]etch|[Ll]oad)/)
    expect(toolbar).toMatch(/@click="[^"]*[Rr]eset/)
  })
})
