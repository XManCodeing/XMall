<script setup lang="ts">
// Reuse the ordinary order table's theme, density and receipt workflow.
// Membership fulfillment is owned by the backend; never replace it with a status edit.
import MembershipRefundAction from './components/MembershipRefundAction.vue'
import ManualPaymentAction from './components/ManualPaymentAction.vue'
import ManualPaymentConfirmation from './components/ManualPaymentConfirmation.vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramMembershipAPI, membershipBase, type MembershipOrder } from '@/api/telegramMembership'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import MembershipDisposition from './components/MembershipDisposition.vue'
import MembershipAccessRecovery from './components/MembershipAccessRecovery.vue'
import MembershipOrderLedger from './components/MembershipOrderLedger.vue'
import TableSkeleton from '@/components/TableSkeleton.vue'
import ListPagination from '@/components/ListPagination.vue'
import { formatDate, formatMoney, getLocalizedText } from '@/utils/format'
import { orderStatusLabel } from '@/utils/status'
import { Badge } from '@/components/ui/badge'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { membershipOrderFilters, membershipDimensionLabels, membershipOrderStateKey } from '@/utils/membershipOrderState'
import type { MembershipRightsState, MembershipMemberRemovalState, MembershipInviteCleanupState } from '@/api/telegramMembership'
import { Copy } from 'lucide-vue-next'
import { copyText } from '@/utils/clipboard'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
import { adminUrl } from '@/utils/adminBase'
import { formatSkuDisplayLabel } from '@/utils/sku'
import { useListRefresh, type ListFetchOptions } from '@/composables/useListRefresh'
import { Tabs, TabsContent } from '@/components/ui/tabs'
import MembershipTabsList from './components/MembershipTabsList.vue'
import { useMembershipAttention } from '@/composables/useMembershipAttention'
const { t, locale } = useI18n()
const { refreshing, refreshList } = useListRefresh()
const auth = useAdminAuthStore()
const can = (id?: number) => auth.hasPermission(`GET:${membershipBase}/orders${id ? `/${id}` : ''}`)
const canList = computed(() => can())
const attention = useMembershipAttention()
type Tab = 'orders' | 'tasks' | 'reviews' | 'lifecycle'
const currentTab = ref<Tab>('orders')
const tabs = computed(() => (['orders', 'tasks', 'reviews', 'lifecycle'] as const).filter(tab => {
  const suffix = { orders: 'orders', tasks: 'access/tasks', reviews: 'access/reviews', lifecycle: 'lifecycle/tasks' }[tab]
  return auth.hasPermission(`GET:${membershipBase}/${suffix}`)
}))
watch(tabs, available => { if (!available.includes(currentTab.value)) currentTab.value = available[0] || 'orders' }, { immediate: true, flush: 'sync' })
const orders = ref<MembershipOrder[]>([]), loading = ref(false), error = ref('')
const orderNo = ref(''), status = ref('all'), telegramIdentity = ref('')
const evidenceFilters = ref<{ rights: MembershipRightsState | 'all'; member_removal: MembershipMemberRemovalState | 'all'; invite_cleanup: MembershipInviteCleanupState | 'all' }>({ rights: 'all', member_removal: 'all', invite_cleanup: 'all' })
const pagination = ref({ page: 1, page_size: 20, total: 0, total_page: 0 })
const detailOpen = ref(false), detailLoading = ref(false), detailError = ref(''), detail = ref<MembershipOrder>()
const statuses = ['pending_payment', 'paid', 'fulfilling', 'partially_delivered', 'partially_refunded', 'delivered', 'completed', 'canceled', 'refunded']
/** Copy an order number. @param value Order number. @returns Completion after user feedback. */
async function copyOrderNo(value: string) {
  try { await copyText(value); notifySuccess(t('admin.common.copied')) }
  catch (cause) { notifyErrorOnce(cause, t('admin.common.copyFailed')) }
}
/** Refresh visible rows. @returns Completion; failures keep rows and show only a safe error. */
async function refresh() {
  try { await refreshList(() => fetchOrders(pagination.value.page, { preserveRows: true })) }
  catch (cause) { notifyErrorOnce(cause, t('common.api.requestFailed')) }
  finally { await attention.refresh() }
}
/** Reload immutable order facts after receipt confirmation, preserving the shared receipt form state. */
async function receiptConfirmed() {
  const current = detail.value?.id, request = detailGeneration
  await refresh()
  if (!current || request !== detailGeneration || !can(current)) return
  try {
    const response = await telegramMembershipAPI.order(current)
    if (request === detailGeneration && detail.value?.id === current && can(current)) detail.value = response.data.data
  } catch { if (request === detailGeneration) detailError.value = t('common.api.requestFailed') }
}
let generation = 0, detailGeneration = 0
/** Fetch the backend's immutable-membership scoped order view, never generic orders with a client-side filter. */
async function fetchOrders(page = 1, options: ListFetchOptions = {}) {
  const request = ++generation
  error.value = ''
  if (!canList.value) return
  if (!options.preserveRows) { orders.value = []; loading.value = true }
  try {
    const filters = evidenceFilters.value
    const response = await telegramMembershipAPI.orders({
      page, page_size: pagination.value.page_size, user_keyword: telegramIdentity.value.trim() || undefined,
      order_no: orderNo.value.trim() || undefined, status: status.value === 'all' ? undefined : status.value,
      rights: filters.rights === 'all' ? undefined : filters.rights,
      member_removal: filters.member_removal === 'all' ? undefined : filters.member_removal,
      invite_cleanup: filters.invite_cleanup === 'all' ? undefined : filters.invite_cleanup,
    })
    if (request !== generation || !canList.value) return
    if (!Array.isArray(response.data?.data)) throw new Error(t('common.api.responseMissing'))
    orders.value = response.data.data
    pagination.value = response.data.pagination || { ...pagination.value, page, total: orders.value.length, total_page: 1 }
  } catch (cause) {
    if (request === generation && canList.value) {
      error.value = t('common.api.requestFailed')
      if (!options.preserveRows) {
        pagination.value = { ...pagination.value, page, total: 0, total_page: 0 }
        notifyErrorOnce(cause, t('common.api.requestFailed'))
      }
    }
    // Refresh owns its fallback toast; preserve the original API notification marker.
    if (options.preserveRows) throw cause
  } finally { if (request === generation) loading.value = false }
}
/** Open authorized read-only detail; identity values remain strings to preserve Telegram's full integer precision. */
async function openDetail(id: number) {
  if (!can(id)) return
  const request = ++detailGeneration
  detailOpen.value = true; detailLoading.value = true; detailError.value = ''; detail.value = undefined
  try {
    const response = await telegramMembershipAPI.order(id)
    if (request === detailGeneration && can(id)) detail.value = response.data.data
  } catch { if (request === detailGeneration) detailError.value = t('common.api.requestFailed') }
  finally { if (request === detailGeneration) detailLoading.value = false }
}
/** 按当前筛选从第一页查询；重复搜索也重新读取服务端。@returns 查询完成。 */
async function searchOrders() {
  if (loading.value || refreshing.value) return
  pagination.value.page = 1
  await fetchOrders(1)
  await attention.refresh()
}
/** 清空订单筛选并回到第一页查询。@returns 查询完成。 */
async function resetFilters() {
  if (loading.value || refreshing.value) return
  orderNo.value = ''; status.value = 'all'; telegramIdentity.value = ''
  evidenceFilters.value = { rights: 'all', member_removal: 'all', invite_cleanup: 'all' }
  await searchOrders()
}
/** Existing pagination component reports page-size changes separately. */
function changePageSize(size: number) { pagination.value.page_size = size; void fetchOrders(1) }
watch(() => [orderNo.value, status.value, telegramIdentity.value, ...Object.values(evidenceFilters.value)], () => {
  // A response for an earlier filter draft must never appear under the new controls.
  generation++; loading.value = false; orders.value = []; error.value = ''
  pagination.value = { ...pagination.value, page: 1, total: 0, total_page: 0 }
}, { flush: 'sync' })
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => {
  generation++; detailGeneration++; orders.value = []; detail.value = undefined; detailOpen.value = false
  loading.value = false; detailLoading.value = false; error.value = ''; detailError.value = ''; pagination.value.total = 0
}, { flush: 'sync' })
watch(detailOpen, value => { if (!value) { detailGeneration++; detail.value = undefined } })
watch(currentTab, tab => { if (tab !== 'orders') detailOpen.value = false }, { flush: 'sync' })
onMounted(() => { void fetchOrders() })
</script>
<template>
  <div class="-mt-2 space-y-6 md:-mt-4">
    <p v-if="!tabs.length" class="text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>
    <Tabs v-else v-model="currentTab" class="flex flex-col gap-2">
      <MembershipTabsList data-guide="orders-tabs" class="membership-subtabs" :tabs="tabs" :states="attention.states" :label="t('telegramMembership.orders')" />
      <TabsContent v-if="canList" value="orders" :force-mount="true" v-show="currentTab === 'orders'" class="mt-0 space-y-4">
        <section class="space-y-4" :aria-label="t('telegramMembership.tabs.orders')">
    <div v-if="canList" data-guide="orders-filter" class="rounded-xl border border-border bg-card p-4 shadow-sm">
      <div class="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center">
        <Input v-model="orderNo" class="w-full md:w-48" :placeholder="t('admin.orders.filterOrderNo')" @keydown.enter="searchOrders" />
        <Input v-model="telegramIdentity" class="w-full md:w-64" :placeholder="t('membershipOrderState.searchIdentity')" :aria-label="t('membershipOrderState.searchIdentity')" @keydown.enter="searchOrders" />
        <Select v-model="status">
          <SelectTrigger class="h-9 w-full md:w-40"><SelectValue :placeholder="t('admin.orders.filterStatusAll')" /></SelectTrigger>
          <SelectContent><SelectItem value="all">{{ t('admin.orders.filterStatusAll') }}</SelectItem><SelectItem v-for="value in statuses" :key="value" :value="value">{{ orderStatusLabel(t, value) }}</SelectItem></SelectContent>
        </Select>
        <Select v-for="(values, dimension) in membershipOrderFilters" :key="dimension" v-model="evidenceFilters[dimension]">
          <SelectTrigger class="h-9 w-full md:w-44" :aria-label="t(`membershipOrderState.${membershipDimensionLabels[dimension]}`)">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{{ t(`membershipOrderState.${membershipDimensionLabels[dimension]}`) }} · {{ t('membershipOrderState.all') }}</SelectItem>
            <SelectItem v-for="value in values" :key="value" :value="value">{{ t(membershipOrderStateKey(dimension, value)) }}</SelectItem>
          </SelectContent>
        </Select>
        <Button size="sm" variant="outline" :disabled="loading || refreshing" @click="searchOrders">{{ t('admin.common.search') }}</Button>
        <Button size="sm" variant="outline" :disabled="loading || refreshing" @click="resetFilters">{{ t('admin.common.reset') }}</Button>
      </div>
    </div>
    <div v-if="canList" data-guide="orders-list" class="rounded-xl border border-border bg-card overflow-hidden relative">
      <Table class="min-w-[880px]">
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-4 py-3 min-w-[140px]">{{ t('admin.orders.table.orderNo') }}</TableHead>
            <TableHead class="px-4 py-3 min-w-[140px]">{{ t('admin.orders.table.items') }}</TableHead>
            <TableHead class="px-4 py-3 min-w-[140px]">{{ t('admin.orders.table.user') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('admin.orders.table.amount') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('admin.orders.table.status') }}</TableHead>
            <TableHead v-for="column in ['validity', 'rights', 'memberRemoval', 'inviteCleanup']" :key="column" class="px-4 py-3">{{ t(`membershipOrderState.${column}`) }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('admin.orders.table.createdAt') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('admin.orders.table.updatedAt') }}</TableHead>
            <TableHead class="px-4 py-3 min-w-[140px] text-right sticky right-0 bg-muted/40 z-10">{{ t('admin.orders.table.action') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-if="loading"><TableCell :colspan="12" class="p-0"><TableSkeleton :columns="12" :rows="5" /></TableCell></TableRow>
          <TableRow v-else-if="!orders.length && !error"><TableCell :colspan="12" class="px-4 py-8 text-center text-muted-foreground">{{ t('admin.orders.empty') }}</TableCell></TableRow>
          <TableRow v-for="order in orders" :key="order.id" class="hover:bg-muted/30">
            <TableCell class="w-[200px] min-w-[140px] max-w-[200px] px-4 py-3">
              <div class="flex min-w-0 items-center gap-1.5">
                <span class="min-w-0 truncate font-medium text-foreground text-xs" :title="order.order_no">{{ order.order_no }}</span>
                <button type="button" class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border/60 text-muted-foreground hover:text-foreground hover:border-border" :title="t('admin.common.copy')" @click="copyOrderNo(order.order_no)"><Copy class="h-3 w-3" /></button>
              </div>
            </TableCell>
            <TableCell class="w-[240px] min-w-[140px] max-w-[240px] px-4 py-3">
              <div v-if="order.items?.length" class="space-y-1">
                <div v-for="item in order.items" :key="item.id" class="truncate text-xs" :title="`${getLocalizedText(item.title) || '-'}${formatSkuDisplayLabel(item.sku_snapshot, locale) ? ` (${formatSkuDisplayLabel(item.sku_snapshot, locale)})` : ''} x${item.quantity}`">
                  <span class="text-foreground">{{ getLocalizedText(item.title) || '-' }}</span>
                  <span v-if="formatSkuDisplayLabel(item.sku_snapshot, locale)" class="ml-1 text-muted-foreground">({{ formatSkuDisplayLabel(item.sku_snapshot, locale) }})</span>
                  <span class="ml-1 text-muted-foreground">x{{ item.quantity }}</span>
                </div>
              </div>
              <span v-else class="text-xs text-muted-foreground">-</span>
            </TableCell>
            <TableCell class="w-[200px] min-w-[140px] max-w-[200px] px-4 py-3 text-xs text-muted-foreground">
              <div v-if="order.user_id" class="min-w-0">
                <a :href="adminUrl(`/users/${order.user_id}`)" target="_blank" rel="noopener" class="block truncate text-primary underline-offset-4 hover:underline" :title="order.user_display_name || order.user_email || '-'">{{ order.user_display_name || order.user_email || '-' }}</a>
                <div class="truncate" :title="order.user_email || '-'">{{ order.user_email || '-' }}</div>
              </div>
              <div v-else class="truncate" :title="`${t('admin.orders.guestLabel')}: ${order.guest_email || '-'}`">{{ t('admin.orders.guestLabel') }}: {{ order.guest_email || '-' }}</div>
            </TableCell>
            <TableCell class="px-4 py-3 font-mono text-xs text-foreground">{{ formatMoney(order.total_amount, order.currency) }}</TableCell>
            <TableCell class="px-4 py-3"><Badge :variant="membershipStatusVariant(order.status)">{{ orderStatusLabel(t, order.status) }}</Badge></TableCell>
            <TableCell class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">
              <template v-for="group in order.membership_groups" :key="`${group.item_id}:${group.target_id}`">
                <div v-if="!group.permanent && group.expires_at">{{ formatDate(group.expires_at) }}</div>
              </template>
            </TableCell>
            <TableCell v-for="dimension in (['rights', 'member_removal', 'invite_cleanup'] as const)" :key="dimension" class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">
              <div v-for="group in order.membership_groups" :key="`${group.item_id}:${group.target_id}`">
                <span v-if="group[dimension] === 'none'">{{ t('membershipOrderState.none') }}</span>
                <Badge v-else :variant="membershipStatusVariant(membershipOrderStateKey(dimension, group[dimension]).endsWith('.unconfirmed') ? 'unconfirmed' : group[dimension])">{{ t(membershipOrderStateKey(dimension, group[dimension])) }}</Badge>
              </div>
              <Badge v-if="!order.membership_groups?.length" :variant="membershipStatusVariant('unconfirmed')">{{ t('membershipOrderState.unconfirmed') }}</Badge>
            </TableCell>
            <TableCell class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">{{ formatDate(order.created_at) }}</TableCell>
            <TableCell class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">{{ formatDate(order.updated_at) }}</TableCell>
            <TableCell class="min-w-[140px] px-4 py-3 text-right sticky right-0 bg-card z-10">
              <div data-guide="orders-actions" class="flex flex-wrap items-center justify-end gap-1.5">
                <MembershipRefundAction v-if="currentTab === 'orders'" :order="order" @refresh="receiptConfirmed" />
                <ManualPaymentAction v-if="currentTab === 'orders'" :order="order" membership @refresh="receiptConfirmed" />
                <Button v-if="can(order.id)" size="xs" variant="outline" @click="openDetail(order.id)">{{ t('admin.orders.view') }}</Button>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
      <ListPagination always-show :loading="loading || refreshing" :page="pagination.page" :page-size="pagination.page_size" :page-size-options="[10, 20, 50, 100]" :total="pagination.total" :total-page="pagination.total_page" @change-page="fetchOrders" @change-page-size="changePageSize" />
    </div>
    </section>
    </TabsContent>
    <TabsContent v-if="tabs.includes('tasks')" value="tasks" :force-mount="true" v-show="currentTab === 'tasks'" class="mt-0">
      <section data-guide="orders-access" :aria-label="t('telegramMembership.tabs.tasks')"><MembershipAccessRecovery mode="tasks" :active="currentTab === 'tasks'" @updated="attention.refresh" /></section>
    </TabsContent>
    <TabsContent v-if="tabs.includes('reviews')" value="reviews" :force-mount="true" v-show="currentTab === 'reviews'" class="mt-0">
      <section data-guide="orders-reviews" :aria-label="t('telegramMembership.tabs.reviews')"><MembershipAccessRecovery mode="reviews" :active="currentTab === 'reviews'" @updated="attention.refresh" /></section>
    </TabsContent>
    <TabsContent v-if="tabs.includes('lifecycle')" value="lifecycle" :force-mount="true" v-show="currentTab === 'lifecycle'" class="mt-0">
      <section data-guide="orders-lifecycle" :aria-label="t('telegramMembership.tabs.lifecycle')"><MembershipDisposition embedded :active="currentTab === 'lifecycle'" @updated="attention.refresh" /></section>
    </TabsContent>
    </Tabs>
    <Dialog v-model:open="detailOpen"><DialogScrollContent class="sm:max-w-5xl"><DialogHeader><DialogTitle>{{ t('telegramMembership.orders') }} · {{ detail?.order_no }}</DialogTitle></DialogHeader>
      <DialogDescription>{{ t('telegramMembership.snapshotHint') }}</DialogDescription>
      <p v-if="detailLoading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="detailError" role="alert" class="text-destructive">{{ detailError }}</p>
      <template v-if="detail"><p><Badge :variant="membershipStatusVariant(detail.status)">{{ orderStatusLabel(t, detail.status) }}</Badge> · {{ formatMoney(detail.total_amount) }}</p>
        <ManualPaymentConfirmation :key="detail.id" :order="detail" auto-load @refresh="receiptConfirmed" />
        <MembershipRefundAction :order="detail" recovery @refresh="receiptConfirmed" />
        <MembershipDisposition :order-id="detail.id" @updated="attention.refresh" />
        <h3 class="font-semibold">{{ t('telegramMembership.snapshot') }}</h3><p class="text-sm text-muted-foreground">{{ t('telegramMembership.snapshotHint') }}</p>
        <div v-for="item in detail.items" :key="item.id" class="space-y-3 rounded-xl border border-border bg-card p-4">
          <p class="font-medium">{{ getLocalizedText(item.title) }} · {{ formatMoney(item.total_price) }}</p>
          <template v-if="item.membership_snapshot">
            <dl class="grid grid-cols-2 gap-3 text-sm"><div><dt class="text-muted-foreground">{{ t('telegramMembership.identity') }}</dt><dd class="font-mono">{{ item.membership_snapshot.beneficiary_telegram_id }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramMembership.configVersion') }}</dt><dd>{{ item.membership_snapshot.config_version }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramMembership.term') }}</dt><dd>{{ item.membership_snapshot.kind === 'permanent' ? t('telegramMembership.permanent') : `${item.membership_snapshot.months} ${t('telegramMembership.months')}` }}</dd></div></dl>
            <div class="rounded-xl border border-border bg-card overflow-x-auto">
              <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead class="px-4 py-3">{{ t('telegramMembership.targets') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramBot.groups.instance') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.routeVersion') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramBot.groups.chatID') }}</TableHead></TableRow></TableHeader><TableBody class="divide-y divide-border"><TableRow v-for="target in item.membership_snapshot.targets" :key="target.target_id" class="hover:bg-muted/30"><TableCell class="px-4 py-3">{{ target.target_id }}</TableCell><TableCell class="px-4 py-3">{{ target.bot_instance_id }}</TableCell><TableCell class="px-4 py-3">{{ target.route_version }}</TableCell><TableCell class="px-4 py-3 font-mono">{{ target.telegram_chat_id }}</TableCell></TableRow></TableBody></Table>
            </div>
          </template><p v-else class="text-sm text-muted-foreground">{{ t('telegramMembership.noSnapshot') }}</p>
          <MembershipOrderLedger v-if="item.membership_snapshot" :ledger="item.membership_ledger" />
        </div>
      </template>
    </DialogScrollContent></Dialog>
  </div>
</template>

<style scoped src="@/components/telegram/membership-subtabs.css"></style>
