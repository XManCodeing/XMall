<script setup lang="ts">
// Reuses the membership orders table/dialog rhythm and global theme tokens; no new navigation or visual system.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import ListPagination from '@/components/ListPagination.vue'
import { Badge } from '@/components/ui/badge'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramMembershipAPI, membershipBase, type MembershipAccessTaskAssociation } from '@/api/telegramMembership'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { Button } from '@/components/ui/button'
import TelegramBotSelect from '@/components/telegram/TelegramBotSelect.vue'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { notifyErrorOnce } from '@/utils/notify'
import { formatDate } from '@/utils/format'
import { formatOAuthIdentityUsername } from '@/utils/oauthIdentity'
interface Delivery { operation: string; state: string; message_id?: string }
interface Row extends MembershipAccessTaskAssociation { delivery?: Delivery; id: string; target_id?: string; chat_id: string; telegram_id: string; telegram_username?: string; source_id?: string; kind?: string; state: string; error_class?: string; review_reason?: string; blocked_reason?: string; updated_at: string; last_attempt_at?: string; received_at?: string; observed_at?: string; member_status?: string; attempt?: number; next_attempt_at?: string; expires_at?: string; parent_operation_id?: string; version?: string; route_version?: string; actions?: string[]; reviewed_at?: string }
const props = withDefaults(defineProps<{ mode?: 'tasks' | 'reviews' | 'all'; active?: boolean }>(), { mode: 'all', active: true })
const emit = defineEmits<{ updated: [] }>()
const { t, te } = useI18n()
const auth = useAdminAuthStore()
const reviewStatus = ref<'pending' | 'reviewed' | 'all'>('pending')
const orderNo = ref(''), reviewKeyword = ref('')
/** Format a review's current Telegram username using the shared identity formatter.
 * @param row Application projection. @returns Prefixed username or an absent marker.
 */
function reviewUsername(row: Row): string {
 const value = formatOAuthIdentityUsername({ provider: 'telegram', username: row.telegram_username || '' })
 return value === '-' ? '—' : value
}
const bot = ref(''), tasks = ref<Row[]>([]), reviews = ref<Row[]>([]), loading = ref(false), error = ref(''), success = ref(false)
const taskPagination = ref({ page: 1, page_size: 20, total: 0 }), reviewPagination = ref({ page: 1, page_size: 20, total: 0 })
const successAction = ref('')
const pending = ref<{ row: Row; action: string }>(), saving = ref(false)
let generation = 0, contextVersion = 0
const canTasks = computed(() => props.mode !== 'reviews' && auth.hasPermission(`GET:${membershipBase}/access/tasks`))
const canReviews = computed(() => props.mode !== 'tasks' && auth.hasPermission(`GET:${membershipBase}/access/reviews`))
const validBot = computed(() => /^[1-9]\d*$/.test(bot.value))
const canRecover = (id: string) => auth.hasPermission(`POST:${membershipBase}/access/tasks/${id}/recover`)
// 仅在当前页存在获授权的服务端操作时显示操作列。
const showTaskActions = computed(() => tasks.value.some(row => canRecover(row.id) && !!row.actions?.length))
const label = (value?: string) => value ? (te(`membershipRecovery.values.${value}`) ? t(`membershipRecovery.values.${value}`) : value) : '—'
const date = (value?: string) => value ? formatDate(value) : '—'
/** Choose the actionable server reason, retaining every reason in tooltip details.
 * @param row Server task. @returns Localized reason or explicit absence.
 */
function reasonLabel(row: Row): string {
 const reason = row.review_reason || row.blocked_reason || row.error_class
 return reason ? label(reason) : t('membershipRecovery.none')
}
/** Format compact task timestamps without losing the full tooltip values.
 * @param value Server timestamp. @returns Date and minute precision, or unavailable marker.
 */
function compactDate(value?: string): string { return date(value).slice(0, 16) }
/** Compose safe task details; never include invite URLs, opaque operation keys or provider diagnostics.
 * @param row Server projection. @returns Localized lines for the shared tooltip.
 */
function taskDetails(row: Row): string[] {
 return [
  `${t('membershipRecovery.lastAttempt')}: ${date(row.last_attempt_at)}`,
  `${t('membershipRecovery.updated')}: ${date(row.updated_at)}`,
  `${t('membershipRecovery.next')}: ${date(row.next_attempt_at)}`,
  `${t('membershipRecovery.expires')}: ${date(row.expires_at)}`,
  `${t('membershipRecovery.delivery')}: ${deliveryLabel(row.delivery)}`,
 ]
}
/** Translate only known delivery states; absent projection is unavailable, never presumed unsent.
 * @param delivery Secret-free runtime projection. @returns Localized delivery state.
 */
function deliveryLabel(delivery?: Delivery): string {
 const state = delivery?.state
 const known = ['not_requested', 'queued', 'sending', 'delivered', 'failed', 'unknown', 'superseded']
 return t(`membershipRecovery.deliveryStates.${state && known.includes(state) ? state : 'unavailable'}`)
}
/** Load real numbered pages; only successful responses commit rows and pagination.
 * @param target Requested page; omitted refreshes both current pages. @param kind Optional table.
 * @param size Requested page size. @returns Completion of the authorized refresh.
 */
async function load(target?: number, kind?: 'tasks' | 'reviews', size?: number) {
 if (!validBot.value || loading.value || (!canTasks.value && !canReviews.value)) return
 const request = ++generation
 const taskPage = { page: target ?? taskPagination.value.page, page_size: size ?? taskPagination.value.page_size }
 const reviewPage = { page: target ?? reviewPagination.value.page, page_size: size ?? reviewPagination.value.page_size }
 loading.value = true; error.value = ''; success.value = false
 try {
  const [taskResponse, reviewResponse] = await Promise.all([
   canTasks.value && kind !== 'reviews' ? telegramMembershipAPI.accessTasks({ bot_id: bot.value, order_no: orderNo.value.trim() || undefined, ...taskPage }) : undefined,
   canReviews.value && kind !== 'tasks' ? telegramMembershipAPI.accessReviews({ bot_id: bot.value, keyword: reviewKeyword.value.trim() || undefined, status: reviewStatus.value, ...reviewPage }) : undefined,
  ])
  if (request !== generation) return
  for (const response of [taskResponse, reviewResponse]) {
   if (props.mode !== 'all' && response && (!Array.isArray(response.data?.data?.items) || !Number.isSafeInteger(response.data.data.total) || response.data.data.total < 0)) throw new Error(t('common.api.responseMissing'))
  }
  if (taskResponse) {
   tasks.value = taskResponse.data.data.items || []
   taskPagination.value = { ...taskPage, total: taskResponse.data.data.total }
  }
  if (reviewResponse) {
   // Marking the last pending application reviewed can shrink the filtered page count.
   const lastPage = Math.max(1, Math.ceil(reviewResponse.data.data.total / reviewPage.page_size))
   if (reviewPage.page > lastPage) {
    loading.value = false
    await load(lastPage, 'reviews', reviewPage.page_size)
    return
   }
   reviews.value = reviewResponse.data.data.items || []
   reviewPagination.value = { ...reviewPage, total: reviewResponse.data.data.total }
  }
 } catch (cause) {
  if (request === generation) {
   error.value = t('common.api.requestFailed')
   if (props.mode !== 'all') notifyErrorOnce(cause, t('common.api.requestFailed'))
  }
 } finally { if (request === generation) { loading.value = false; emit('updated') } }
}
/** Submit a versioned action; responses from an old authorization context are discarded.
 * @returns Resolves after authorized state refresh or failure feedback.
 */
async function recover() {
 const selection = pending.value
 if (!selection || saving.value || !canRecover(selection.row.id)) return
 const context = contextVersion, botID = bot.value
 saving.value = true; error.value = ''
 try {
  await telegramMembershipAPI.recoverAccess(botID, selection.row.id, { action: selection.action, expected_version: selection.row.version || '0', expected_route_version: selection.row.route_version || '0', reason: 'operator_verified' })
  if (context !== contextVersion) return
  pending.value = undefined
  await load()
  if (context === contextVersion) { successAction.value = selection.action; success.value = true }
 } catch (cause) {
  if (context !== contextVersion) return
  if (props.mode !== 'all') notifyErrorOnce(cause, t('membershipRecovery.blocked'))
  pending.value = undefined; await load()
  if (context === contextVersion) error.value = t('membershipRecovery.blocked')
 } finally { if (context === contextVersion) saving.value = false }
}
/** 清理查询上下文及页码，阻止旧请求覆盖新筛选。@returns 无。 */
function clearContext() {
 generation++; contextVersion++; tasks.value = []; reviews.value = []; taskPagination.value = { ...taskPagination.value, page: 1, total: 0 }; reviewPagination.value = { ...reviewPagination.value, page: 1, total: 0 }; pending.value = undefined; loading.value = false; saving.value = false; error.value = ''; success.value = false
}
/** 保留必填机器人范围，清空查询条件并重新读取两张表的第一页。@returns 查询完成。 */
async function resetFilters(): Promise<void> {
 if (saving.value || loading.value) return
 orderNo.value = ''; reviewKeyword.value = ''; reviewStatus.value = 'pending'
 clearContext()
 await load(1)
}
watch(orderNo, () => {
 // Invalidate old task pages immediately, but leave the independent review table untouched.
 generation++; contextVersion++; tasks.value = []; pending.value = undefined; loading.value = false
 taskPagination.value = { ...taskPagination.value, page: 1, total: 0 }; error.value = ''; success.value = false
}, { flush: 'sync' })
/** Invalidate outstanding review reads/actions when the keyword changes; keep task filters independent. */
watch([reviewKeyword, reviewStatus], () => {
 generation++; contextVersion++; reviews.value = []; pending.value = undefined; loading.value = false
 reviewPagination.value = { ...reviewPagination.value, page: 1, total: 0 }; error.value = ''; success.value = false
}, { flush: 'sync' })
watch(() => [props.mode, bot.value, auth.token, auth.isSuper, JSON.stringify(auth.permissions)], clearContext, { flush: 'sync' })
// Keep query drafts mounted, but never leave a teleported confirmation on a hidden tab.
watch(() => props.active, active => { if (!active) pending.value = undefined }, { flush: 'sync' })
onBeforeUnmount(() => { generation++; contextVersion++ })
</script>
<template>
 <section v-if="canTasks || canReviews" class="space-y-4">
  <h2 v-if="mode === 'all'" class="text-xl font-semibold">{{ t('membershipRecovery.title') }}</h2>
  <p class="text-sm text-muted-foreground">{{ t('membershipRecovery.hint') }}</p>
  <div class="flex flex-wrap items-center gap-3"><TelegramBotSelect v-model="bot" required class="w-56" :disabled="saving" /><Input v-if="canTasks" v-model="orderNo" class="w-56" :disabled="saving" :placeholder="t('admin.orders.filterOrderNo')" :aria-label="t('admin.orders.filterOrderNo')" @keydown.enter="load(1)" /><Input v-if="canReviews" v-model="reviewKeyword" class="h-9 w-64" :maxlength="64" :disabled="saving" :placeholder="t('lifecycleMembers.search')" :aria-label="t('lifecycleMembers.search')" @keydown.enter="load(1, 'reviews')" /><Select v-if="canReviews" v-model="reviewStatus" :disabled="saving"><SelectTrigger class="h-9 w-40" :aria-label="t('membershipRecovery.reviewFilter')"><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="value in ['pending', 'reviewed', 'all']" :key="value" :value="value">{{ t(`membershipRecovery.reviewFilters.${value}`) }}</SelectItem></SelectContent></Select><Button variant="outline" :disabled="loading || saving || !validBot" @click="load(1)">{{ t('admin.common.search') }}</Button><Button variant="outline" :disabled="loading || saving" @click="resetFilters">{{ t('admin.common.reset') }}</Button></div>
  <p v-if="error && mode === 'all'" role="alert" class="text-sm text-destructive">{{ error }}</p><p v-if="success" role="status" class="text-sm text-muted-foreground">{{ t(successAction === 'review_join' ? 'membershipRecovery.reviewAccepted' : 'membershipRecovery.accepted') }}</p>
  <p v-if="loading" role="status">{{ t('telegramBot.instances.loading') }}</p>
  <TooltipProvider>
  <div v-if="canTasks" class="rounded-xl border border-border bg-card overflow-hidden">
   <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
    <TableHead v-for="column in ['orderNo', 'taskType', 'buyerTelegramID', 'chatID', 'chatTitle', 'state', 'attempt', 'observation']" :key="column" class="px-6 py-3 whitespace-nowrap">{{ t(`membershipRecovery.${column}`) }}</TableHead><TableHead v-if="showTaskActions" class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
   </TableRow></TableHeader><TableBody class="divide-y divide-border">
    <TableRow v-for="row in tasks" :key="row.id" class="hover:bg-muted/30">
     <TableCell class="px-6 py-4">
      <Tooltip v-if="row.order_no"><TooltipTrigger as-child><span tabindex="0" class="block max-w-40 truncate text-xs">{{ row.order_no }}</span></TooltipTrigger><TooltipContent class="max-w-sm break-all">{{ row.order_no }}</TooltipContent></Tooltip>
      <span v-else class="text-xs text-muted-foreground">{{ t('membershipRecovery.none') }}</span>
     </TableCell>
     <TableCell class="px-6 py-4 whitespace-nowrap">{{ label(row.kind) }}</TableCell>
     <TableCell class="px-6 py-4 font-mono text-xs whitespace-nowrap">{{ row.buyer_telegram_id || t('membershipRecovery.none') }}</TableCell>
     <TableCell class="px-6 py-4 font-mono text-xs whitespace-nowrap">{{ row.chat_id || t('membershipRecovery.none') }}</TableCell>
     <TableCell class="px-6 py-4">
      <Tooltip v-if="row.chat_title"><TooltipTrigger as-child><span tabindex="0" class="block max-w-40 truncate text-xs">{{ row.chat_title }}</span></TooltipTrigger><TooltipContent class="max-w-sm break-all">{{ row.chat_title }}</TooltipContent></Tooltip>
      <span v-else class="text-xs text-muted-foreground">{{ t('membershipRecovery.none') }}</span>
     </TableCell>
     <TableCell class="px-6 py-4" data-testid="task-state">
      <div class="flex w-max flex-nowrap items-center gap-1 whitespace-nowrap [&>*]:shrink-0">
       <Tooltip><TooltipTrigger as-child><Badge tabindex="0" :variant="membershipStatusVariant(row.state)">{{ label(row.state) }}</Badge></TooltipTrigger><TooltipContent><p data-testid="delivery-state">{{ t('membershipRecovery.delivery') }}: {{ deliveryLabel(row.delivery) }}</p></TooltipContent></Tooltip>
       <Tooltip><TooltipTrigger as-child><Badge tabindex="0" variant="secondary" class="max-w-40"><span class="truncate">{{ reasonLabel(row) }}</span></Badge></TooltipTrigger><TooltipContent class="max-w-sm break-words"><p>{{ reasonLabel(row) }}</p><p v-for="reason in [...new Set([row.review_reason, row.blocked_reason, row.error_class].filter(Boolean))]" :key="reason">{{ label(reason) }}</p></TooltipContent></Tooltip>
      </div>
     </TableCell>
     <TableCell class="px-6 py-4 text-xs whitespace-nowrap" data-testid="task-attempt">
      <Tooltip><TooltipTrigger as-child><div tabindex="0" class="max-w-48 space-y-1"><p class="truncate">{{ row.attempt == null ? '—' : t('membershipRecovery.attemptCount', { count: row.attempt }) }} · {{ compactDate(row.last_attempt_at) }}</p><p class="truncate text-muted-foreground">{{ compactDate(row.updated_at) }}</p></div></TooltipTrigger><TooltipContent><p v-for="line in taskDetails(row)" :key="line">{{ line }}</p></TooltipContent></Tooltip>
     </TableCell>
     <TableCell class="px-6 py-4 text-xs whitespace-nowrap">{{ label(row.member_status) }}<p class="text-muted-foreground">{{ date(row.observed_at) }}</p></TableCell>
     <TableCell v-if="showTaskActions" class="px-6 py-4 text-right"><div v-if="canRecover(row.id)" class="flex flex-wrap items-center justify-end gap-2"><Button v-for="action in row.actions" :key="action" size="sm" variant="outline" :disabled="saving || loading" @click="pending = { row, action }" :title="label(action)" :aria-label="label(action)">{{ t(`tableAction.${action}`) }}</Button></div></TableCell>
    </TableRow>
    <TableRow v-if="!tasks.length && !loading && (!error || mode === 'all')"><TableCell :colspan="showTaskActions ? 9 : 8" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
   </TableBody></Table>
   <ListPagination always-show :page="taskPagination.page" :page-size="taskPagination.page_size" :total="taskPagination.total" :total-page="Math.ceil(taskPagination.total / taskPagination.page_size)" :page-size-options="[10, 20, 50, 100]" :loading="loading" :disabled="saving || !validBot" @change-page="page => load(page, 'tasks')" @change-page-size="size => load(1, 'tasks', size)" />
  </div>
  </TooltipProvider>
  <h3 v-if="canReviews && mode === 'all'" class="font-medium">{{ t('membershipRecovery.reviews') }}</h3>
  <div v-if="canReviews" class="rounded-xl border border-border bg-card overflow-hidden">
   <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
    <TableHead class="px-6 py-3">{{ t('telegramBot.groups.chatID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramMembership.identity') }}</TableHead><TableHead class="px-6 py-3">{{ t('lifecycleMembers.username') }}</TableHead><TableHead class="px-6 py-3">{{ t('membershipRecovery.state') }}</TableHead><TableHead class="px-6 py-3 whitespace-nowrap">{{ t('membershipRecovery.reviewReceivedAt') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
   </TableRow></TableHeader><TableBody class="divide-y divide-border">
    <TableRow v-for="row in reviews" :key="row.id" class="hover:bg-muted/30">
     <TableCell class="px-6 py-4 font-mono text-xs whitespace-nowrap">{{ row.chat_id }}</TableCell>
     <TableCell class="px-6 py-4 font-mono text-xs whitespace-nowrap">{{ row.telegram_id }}</TableCell>
     <TableCell class="px-6 py-4 text-xs whitespace-nowrap"><span class="block max-w-40 truncate" :title="reviewUsername(row)" data-testid="review-username">{{ reviewUsername(row) }}</span></TableCell>
     <TableCell class="px-6 py-4" data-testid="review-state">
      <Badge :variant="membershipStatusVariant(row.reviewed_at ? 'inactive' : 'needs_review')">{{ t(row.reviewed_at ? 'membershipRecovery.reviewDone' : 'membershipRecovery.reviewPending') }}</Badge>
      <p class="mt-1 max-w-sm text-xs text-muted-foreground leading-relaxed">{{ t('membershipRecovery.reviewReason') }}</p>
      <p v-if="row.reviewed_at" class="mt-1 text-xs text-muted-foreground">{{ t('membershipRecovery.reviewTime') }}：{{ date(row.reviewed_at) }}</p>
     </TableCell>
     <TableCell class="px-6 py-4 text-xs text-muted-foreground whitespace-nowrap">{{ date(row.received_at) }}</TableCell>
     <TableCell class="px-6 py-4 text-right"><div class="flex flex-wrap items-center justify-end gap-2"><Button v-if="canRecover(row.id) && !row.reviewed_at" size="sm" variant="outline" :disabled="saving || loading" @click="pending = { row, action: 'review_join' }" :title="t('membershipRecovery.reviewAction')" :aria-label="t('membershipRecovery.reviewAction')">{{ t('membershipRecovery.reviewEntry') }}</Button></div></TableCell>
    </TableRow>
    <TableRow v-if="!reviews.length && !loading && (!error || mode === 'all')"><TableCell :colspan="6" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
   </TableBody></Table>
   <ListPagination always-show :page="reviewPagination.page" :page-size="reviewPagination.page_size" :total="reviewPagination.total" :total-page="Math.ceil(reviewPagination.total / reviewPagination.page_size)" :page-size-options="[10, 20, 50, 100]" :loading="loading" :disabled="saving || !validBot" @change-page="page => load(page, 'reviews')" @change-page-size="size => load(1, 'reviews', size)" />
  </div>
  <Dialog :open="active && !!pending" @update:open="value => { if (!value && !saving) pending = undefined }"><DialogContent><DialogHeader><DialogTitle>{{ pending?.action === 'review_join' ? t('membershipRecovery.reviewAction') : label(pending?.action) }}</DialogTitle><DialogDescription>{{ t(pending?.action === 'review_join' ? 'membershipRecovery.reviewDescription' : pending?.action === 'refresh_member' ? 'membershipRecovery.refreshHint' : 'membershipRecovery.confirm') }}</DialogDescription></DialogHeader><p class="text-sm text-muted-foreground">{{ t(pending?.action === 'review_join' ? 'membershipRecovery.reviewNotice' : 'membershipRecovery.manualOnly') }}</p><DialogFooter><Button :disabled="saving" @click="recover">{{ t(pending?.action === 'review_join' ? 'membershipRecovery.reviewConfirm' : 'membershipRecovery.confirmAction') }}</Button></DialogFooter></DialogContent></Dialog>
 </section>
</template>
