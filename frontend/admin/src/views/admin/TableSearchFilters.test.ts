import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const pages = [
  'AffiliateCommissions', 'AffiliateUsers', 'AffiliateWithdraws', 'ApiCredentials', 'Authz',
  'AuthzAuditLogs', 'Banners', 'CardSecrets', 'Coupons', 'GiftCards', 'InviteCodes', 'Media',
  'Notifications', 'OrderRefunds', 'Orders', 'PaymentChannels', 'Payments', 'ProcurementOrders',
  'ProductMappings', 'Products', 'Promotions', 'Reconciliation', 'ResellerBalanceAccounts',
  'ResellerDomains', 'ResellerLedgerEntries', 'ResellerOperationsDashboard', 'ResellerProductSettings',
  'ResellerProfiles', 'ResellerSiteConfigs', 'ResellerWithdraws', 'UserLoginLogs', 'Users',
  'WalletRecharges', 'WholesalePrices',
]

/** 只读取本范围页面，不加载环境文件或调用业务接口。@param name 页面名。@returns Vue 源码。 */
function sourceFor(name: string): string {
  return readFileSync(join(dirname(fileURLToPath(import.meta.url)), `${name}.vue`), 'utf8')
}

describe('顶层非 Telegram 表格手动搜索契约', () => {
  for (const name of pages) {
    it(`${name}: 脚本和模板可编译`, () => {
      const { descriptor, errors } = parse(sourceFor(name))
      expect(errors).toEqual([])
      const script = compileScript(descriptor, { id: name })
      const template = compileTemplate({
        source: descriptor.template!.content,
        filename: `${name}.vue`,
        id: name,
        compilerOptions: { bindingMetadata: script.bindings },
      })
      expect(template.errors).toEqual([])
    })
    it(`${name}: 提供搜索、重置且移除自动搜索防抖`, () => {
      const source = sourceFor(name)
      expect(source).toMatch(/@click="(?:handleSearch|handleFilterChange|searchTables|searchAdmins|loadAll|handleNotificationLogSearch)"/)
      expect(source).toMatch(/@click="(?:resetFilters|handleReset|resetTableFilters|resetAdminFilters|resetNotificationLogFilters)"/)
      expect(source).not.toMatch(/debouncedSearch|debouncedFetch|debouncedNotificationLogSearch/)
    })
  }

  it('手动搜索和重置各请求一次，加载期间拒绝重复操作且保留分页尺寸', async () => {
    const source = sourceFor('AffiliateCommissions')
    const handlers = source.match(/const handleSearch = async[\s\S]*?\n}\n\/\*\*[\s\S]*?const resetFilters = async[\s\S]*?\n}/)?.[0]
    expect(handlers).toBeTruthy()
    const loading = { value: false }
    const refreshing = { value: false }
    const pagination = { value: { page: 5, page_size: 50 } }
    const filters = { keyword: 'buyer', orderNo: 'order', affiliateProfileId: '8', status: 'available' }
    const fetchRows = vi.fn(async () => undefined)
    // 运行实际处理函数，仅替换网络边界，不挂载页面或访问业务服务。
    const factory = new Function('loading', 'refreshing', 'pagination', 'filters', 'fetchRows',
      `${handlers!.replace(/: Promise<void>/g, '')}; return { handleSearch, resetFilters }`)
    const actions = factory(loading, refreshing, pagination, filters, fetchRows)
    await actions.handleSearch()
    expect(fetchRows).toHaveBeenCalledExactlyOnceWith(1)
    expect(pagination.value).toEqual({ page: 1, page_size: 50 })
    loading.value = true
    await actions.resetFilters()
    expect(filters.keyword).toBe('buyer')
    expect(fetchRows).toHaveBeenCalledTimes(1)
    loading.value = false
    await actions.resetFilters()
    expect(fetchRows).toHaveBeenCalledTimes(2)
    expect(filters).toEqual({ keyword: '', orderNo: '', affiliateProfileId: '', status: '__all__' })
    expect(pagination.value.page_size).toBe(50)
  })

  it('卡密两个独立筛选栏都有加载保护和重置', () => {
    const source = sourceFor('CardSecrets')
    expect(source).toContain('@click="resetTableFilters"')
    expect(source).toContain('@click="resetListFilters"')
    expect(source).toContain('page_size: batchPagination.value.page_size')
    expect(source).toContain(':disabled="cardSecretsLoading || refreshing" @click="applyListFilters"')
  })

  it('权限管理两个独立查询互不影响', () => {
    const source = sourceFor('Authz')
    expect(source).toContain('@click="resetAdminFilters"')
    expect(source).toContain('@click="resetCatalogFilters"')
    expect(source).toContain('appliedAdminKeyword.value.trim()')
    expect(source).toContain('appliedCatalogKeyword.value.trim()')
  })
})
