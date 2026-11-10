<script setup lang="ts">
// Products.vue copy adapted to the membership-only API: retain toolbar, table, pagination and editor dialog.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramMembershipAPI, membershipBase, type MembershipProduct, type MembershipTerm } from '@/api/telegramMembership'
import { getLocalizedText, formatMoney } from '@/utils/format'
import { getFirstImageUrl } from '@/utils/image'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import TableSkeleton from '@/components/TableSkeleton.vue'
import ListPagination from '@/components/ListPagination.vue'
import MembershipProductEditModal from './components/MembershipProductEditModal.vue'
import { useMembershipProductOperations } from '@/composables/useMembershipProductOperations'
import { Share2 } from 'lucide-vue-next'
import { copyText } from '@/utils/clipboard'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
const route = useRoute()
const { t } = useI18n()
const auth = useAdminAuthStore()
const can = (method: string, id?: number) => auth.hasPermission(`${method}:${membershipBase}/products${id ? `/${id}` : ''}`)
const canList = computed(() => can('GET'))
const products = ref<MembershipProduct[]>([]), loading = ref(false), error = ref(''), searchQuery = ref('')
const pagination = ref({ page: 1, page_size: 10, total: 0, total_page: 0 })
const editor = ref<InstanceType<typeof MembershipProductEditModal>>()
let generation = 0
const operations = useMembershipProductOperations(can, () => fetchProducts())
/** @param term 服务端期限。@returns 本地化期限文本。 */
function termLabel(term: MembershipTerm): string {
  return term.kind === 'permanent' ? t('telegramMembership.permanent') : `${term.months} ${t('telegramMembership.months')}`
}
/** @param product 商品及其 SKU。@returns 单行价格范围；空数据不伪装成零元。 */
function priceSummary(product: MembershipProduct): string {
  const prices = (product.skus || []).map(sku => Number(sku.price_amount)).filter(Number.isFinite)
  if (!prices.length) return '—'
  const minimum = Math.min(...prices), maximum = Math.max(...prices)
  return minimum === maximum ? formatMoney(minimum) : `${formatMoney(minimum)} – ${formatMoney(maximum)}`
}
/** @param product 商品及其期限。@returns 去重的横向期限摘要。 */
function termSummary(product: MembershipProduct): string {
  return [...new Set(product.membership.terms.map(termLabel))].join(' / ') || '—'
}
/** @param product 商品配置。@returns 完整 SKU、价格与期限对应关系，供键盘聚焦和悬停查看。 */
function skuDetails(product: MembershipProduct): string {
  return (product.skus || []).map(sku => {
    const term = product.membership.terms.find(item => item.sku_id === sku.id || item.sku_code === sku.sku_code)
    return `${sku.sku_code} · ${formatMoney(sku.price_amount)} · ${term ? termLabel(term) : '—'}`
  }).join('\n') || '—'
}
/** @param product 当前商品绑定。@returns 按绑定顺序展示群名，缺失名称退回 ID，不额外请求群管理接口。 */
function groupNames(product: MembershipProduct): string[] {
  return product.membership.target_ids.map(id => product.membership.target_names?.[String(id)]?.trim() || `#${id}`)
}
const copyingId = ref<number | null>(null)
/**
 * 校验服务端分享链接；不从其他机器人或网站地址猜测链接。
 * @param product 当前商品行。
 * @returns 格式有效的服务端 Telegram 深链；真实性和商品归属仅由后端校验。
 */
function shareLink(product: MembershipProduct): string {
  const link = product.share_url
  if (!product.is_active || !product.membership.enabled || typeof link !== 'string') return ''
  return /^https:\/\/t\.me\/[A-Za-z][A-Za-z0-9_]{4,31}\?start=[A-Za-z0-9]{12}$/.test(link) ? link : ''
}
/** @param product 当前商品行。@returns 是否可复制或首次生成，缺失协议字段时拒绝猜测。 */
function canShare(product: MembershipProduct): boolean {
  if (!canList.value || !product.is_active || !product.membership.enabled || product.share_unavailable_reason !== '') return false
  if (shareLink(product)) return true
  return product.share_url === '' && auth.hasPermission(`POST:${membershipBase}/products/${product.id}/share`)
}
/** @param product 当前商品行。@returns 分享按钮提示，未知原因保持安全降级。 */
function shareTitle(product: MembershipProduct): string {
  if (canShare(product)) return t('telegramMembership.shareCopy')
  if (product.share_url === '' && product.share_unavailable_reason === '' && !auth.hasPermission(`POST:${membershipBase}/products/${product.id}/share`)) return t('telegramMembership.sharePermission')
  const reason = product.share_unavailable_reason
  const reasons = ['unavailable', 'missing_binding', 'mixed_bots', 'bot_unavailable']
  return t(`telegramMembership.shareReasons.${reason && reasons.includes(reason) ? reason : 'unavailable'}`)
}
/**
 * 复用剪贴板兼容处理，仅复制链接，不打开 Telegram 或生成订单。
 * @param product 当前商品行。
 * @returns 复制及成功/失败反馈完成。
 */
async function copyShareLink(product: MembershipProduct): Promise<void> {
  if (!canShare(product) || loading.value || copyingId.value !== null) return
  const request = generation
  copyingId.value = product.id
  let generated = Boolean(shareLink(product))
  try {
    if (!generated) {
      const response = await telegramMembershipAPI.share(product.id)
      if (request !== generation || !canList.value) return
      const result = response.data?.data
      if (typeof result?.share_url !== 'string' || typeof result.share_unavailable_reason !== 'string') throw new Error('Invalid share response')
      if (!result.share_unavailable_reason && !shareLink({ ...product, share_url: result.share_url })) throw new Error('Invalid share URL')
      product.share_url = result.share_url
      product.share_unavailable_reason = result.share_unavailable_reason
      if (result.share_unavailable_reason) {
        notifyErrorOnce(new Error('Share unavailable'), shareTitle(product))
        return
      }
      if (!shareLink(product)) throw new Error('Invalid share URL')
      generated = true
    }
    await copyText(shareLink(product))
    if (request === generation && canList.value) notifySuccess(t('telegramMembership.shareCopied'))
  } catch (cause) {
    if (request === generation && canList.value) notifyErrorOnce(cause, t(generated ? 'admin.common.copyFailed' : 'telegramMembership.shareFailed'))
  } finally { copyingId.value = null }
}
/**
 * Read the membership list without optimistic state changes.
 * @param page Requested page number.
 * @returns Whether an authorized, current and valid list response was rendered.
 */
async function fetchProducts(page = pagination.value.page): Promise<boolean> {
  const request = ++generation
  products.value = []; error.value = ''
  if (!canList.value) return false
  loading.value = true
  try {
    const response = await telegramMembershipAPI.products({ page, page_size: pagination.value.page_size, search: searchQuery.value })
    if (request !== generation || !canList.value) return false
    if (!Array.isArray(response.data?.data) || !response.data.pagination) throw new Error(t('common.api.responseMissing'))
    products.value = response.data.data
    pagination.value = response.data.pagination
    return true
  } catch (failure: unknown) {
    if (request === generation && canList.value) {
      error.value = t('common.api.requestFailed')
      notifyErrorOnce(failure, t('common.api.requestFailed'))
    }
    return false
  } finally { if (request === generation) loading.value = false }
}
/**
 * 通过专用版本化接口切换状态，权限和配置能力由服务端最终校验。
 * @param product 当前服务端商品行。
 * @returns 请求及权威状态回读完成；未知状态不提交。
 */
async function toggleStatus(product: MembershipProduct): Promise<void> {
  if (!can('PATCH', product.id) || loading.value || operations.blocked(product) || typeof product.is_active !== 'boolean') return
  await operations.operate(product, product.is_active ? 'deactivate' : 'activate')
}
/** 按当前关键词从第一页查询，重复搜索重新读取数据。@returns 查询完成。 */
async function searchProducts() {
  if (loading.value) return
  pagination.value.page = 1
  await fetchProducts(1)
}
/** 清空关键词并回到第一页查询。@returns 查询完成。 */
async function resetFilters() {
  if (loading.value) return
  searchQuery.value = ''
  await searchProducts()
}
/** Existing pagination component reports page-size changes separately. */
function changePageSize(size: number) { pagination.value.page_size = size; void fetchProducts(1) }
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => { generation++; operations.reset(); products.value = []; loading.value = false; error.value = ''; searchQuery.value = ''; pagination.value.total = 0 }, { flush: 'sync' })
onMounted(() => {
  void fetchProducts()
  const id = Number(route?.query.product_id)
  if (Number.isSafeInteger(id) && id > 0 && can('GET', id) && can('PUT', id)) editor.value?.edit(id)
})
</script>
<template>
  <div class="space-y-6">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h1 class="text-2xl font-semibold">{{ t('telegramMembership.products') }}</h1>
      </div>
      <Button v-if="can('POST')" data-guide="products-create" class="w-full sm:w-auto" @click="editor?.edit()">{{ t('admin.products.create') }}</Button>
    </div>
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>
    <div v-if="canList" data-guide="products-filter" class="rounded-xl border border-border bg-card p-4 shadow-sm">
      <div class="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center">
        <div class="relative w-full md:w-80">
          <div class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground">
            <svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
            </svg>
          </div>
          <Input id="membership-products-search" v-model="searchQuery" class="pl-9" :placeholder="t('admin.products.searchPlaceholder')" :aria-label="t('admin.products.searchPlaceholder')" @keydown.enter="searchProducts" />
        </div>
        <Button variant="outline" size="sm" class="h-9 w-full sm:w-auto" :disabled="loading" @click="searchProducts">{{ t('admin.common.search') }}</Button>
        <Button variant="outline" size="sm" class="h-9 w-full sm:w-auto" :disabled="loading" @click="resetFilters">{{ t('admin.common.reset') }}</Button>
      </div>
    </div>
    <p v-if="operations.notice.value" role="alert" class="text-sm text-destructive">{{ operations.notice.value }}</p>
    <TooltipProvider>
    <div v-if="canList" data-guide="products-list" class="rounded-xl border border-border bg-card overflow-hidden">
      <Table class="min-w-[1160px]">
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="min-w-[320px] px-6 py-3">{{ t('admin.products.table.name') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramMembership.price') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramMembership.term') }}</TableHead>
            <TableHead class="px-6 py-3 whitespace-nowrap">{{ t('telegramMembership.groupName') }}</TableHead>
            <TableHead class="min-w-[100px] px-6 py-3 whitespace-nowrap">{{ t('admin.products.table.status') }}</TableHead>
            <TableHead class="px-3 py-3 text-center whitespace-nowrap">{{ t('telegramMembership.share') }}</TableHead>
            <TableHead class="px-6 py-3 text-right">{{ t('admin.products.table.action') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-if="loading"><TableCell :colspan="7" class="p-0"><TableSkeleton :columns="7" :rows="5" /></TableCell></TableRow>
          <TableRow v-for="product in products" :key="product.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4">
              <div class="flex h-12 w-[320px] min-w-[320px] items-center gap-4">
                <div class="flex h-12 w-12 shrink-0 items-center justify-center overflow-hidden rounded-lg border border-border bg-muted/40 text-xs text-muted-foreground">
                  <img v-if="getFirstImageUrl(product.images)" :src="getFirstImageUrl(product.images)" :alt="getLocalizedText(product.title)" class="h-full w-full object-cover" />
                  <span v-else>{{ t('admin.common.noImage') }}</span>
                </div>
                <div class="min-w-0 flex-1">
                  <div class="truncate font-medium text-foreground" :title="getLocalizedText(product.title)">{{ getLocalizedText(product.title) }}</div>
                  <div class="truncate text-xs text-muted-foreground font-mono" :title="product.slug">{{ product.slug }}</div>
                </div>
              </div>
            </TableCell>
            <TableCell class="px-6 py-4">
              <Tooltip><TooltipTrigger as-child>
                <span tabindex="0" :aria-label="skuDetails(product)" data-price-summary class="block w-40 truncate font-mono leading-5 text-foreground">{{ priceSummary(product) }}</span>
              </TooltipTrigger><TooltipContent class="max-w-sm whitespace-pre-line break-all">{{ skuDetails(product) }}</TooltipContent></Tooltip>
            </TableCell>
            <TableCell class="px-6 py-4">
              <Tooltip><TooltipTrigger as-child>
                <span tabindex="0" :aria-label="skuDetails(product)" data-term-summary class="block w-40 truncate leading-5 text-foreground">{{ termSummary(product) }}</span>
              </TooltipTrigger><TooltipContent class="max-w-sm whitespace-pre-line break-all">{{ skuDetails(product) }}</TooltipContent></Tooltip>
            </TableCell>
            <TableCell class="px-6 py-4">
              <Tooltip><TooltipTrigger as-child>
                <span tabindex="0" data-group-names :aria-label="groupNames(product).join('、') || '—'" class="block w-48 truncate leading-5 text-foreground">{{ groupNames(product).join('、') || '—' }}</span>
              </TooltipTrigger><TooltipContent class="max-w-sm whitespace-pre-line break-all">{{ groupNames(product).join('\n') || '—' }}</TooltipContent></Tooltip>
            </TableCell>
            <TableCell class="min-w-[100px] whitespace-nowrap px-6 py-4">
              <button
                type="button"
                class="inline-flex shrink-0 cursor-pointer items-center justify-center whitespace-nowrap rounded-full border px-2.5 py-1 text-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                :class="product.is_active === true ? 'text-success border-success/20 bg-success/10 hover:bg-success/20' : 'text-muted-foreground border-border bg-muted/30 hover:bg-muted/50'"
                :disabled="!can('PATCH', product.id) || operations.blocked(product) || loading || typeof product.is_active !== 'boolean'"
                :title="product.is_active === true ? t('admin.products.status.clickToDeactivate') : t('admin.products.status.clickToActivate')"
                @click="toggleStatus(product)"
              >{{ t(`telegramMembership.operations.${product.is_active === true ? 'active' : product.is_active === false ? 'inactive' : 'unknownStatus'}`) }}</button>
            </TableCell>
            <TableCell class="px-3 py-4 text-center">
              <span class="inline-flex" :title="shareTitle(product)">
                <Button variant="ghost" size="icon-sm" data-product-share
                  :aria-label="shareTitle(product)" :aria-busy="copyingId === product.id"
                  :disabled="!canShare(product) || copyingId !== null || loading"
                  @click="copyShareLink(product)">
                  <Share2 class="h-4 w-4" aria-hidden="true" />
                </Button>
              </span>
            </TableCell>
            <TableCell class="px-6 py-4 text-right">
              <div data-guide="products-actions" class="flex items-center justify-end gap-2 whitespace-nowrap">
                <Button v-if="can('GET', product.id) && can('PUT', product.id)" variant="outline" size="sm" :disabled="operations.pendingId.value !== null" @click="editor?.edit(product.id)">{{ t('admin.common.edit') }}</Button>
                <Button v-if="can('DELETE', product.id)" variant="destructive" size="sm" :disabled="operations.blocked(product) || loading" @click="operations.operate(product, 'remove')">{{ t('telegramMembership.operations.remove') }}</Button>
              </div>
              <p v-if="!operations.validVersion(product)" class="max-w-48 truncate text-xs leading-4 text-muted-foreground" :title="t('telegramMembership.operations.invalidVersion')">{{ t('telegramMembership.operations.invalidVersion') }}</p>
              <p v-else-if="operations.pendingId.value === null && operations.blocked(product)" class="max-w-48 truncate text-xs leading-4 text-muted-foreground" :title="t('telegramMembership.operations.unchanged')">{{ t('telegramMembership.operations.unchanged') }}</p>
            </TableCell>
          </TableRow>
          <TableRow v-if="!products.length && !loading && !error"><TableCell :colspan="7" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
        </TableBody>
      </Table>
      <ListPagination always-show :loading="loading" :page="pagination.page" :page-size="pagination.page_size" :total="pagination.total" :total-page="pagination.total_page" :page-size-options="[10, 20, 50, 100]" @change-page="fetchProducts" @change-page-size="changePageSize" />
    </div>
    </TooltipProvider>
    <MembershipProductEditModal ref="editor" @success="fetchProducts()" />
  </div>
</template>
