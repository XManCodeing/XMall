<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import ListPagination from '@/components/ListPagination.vue'
import { Badge } from '@/components/ui/badge'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramMembershipAPI as api, membershipBase, type MembershipLifecycleTask as Task, type MembershipLifecycleStatus, type MembershipLifecycleMember } from '@/api/telegramMembership'
import { Input } from '@/components/ui/input'
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
import { lifecycleMemberViews } from '@/utils/lifecycleMembers'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { notifyErrorOnce } from '@/utils/notify'
import { formatDate } from '@/utils/format'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import TelegramBotSelect from '@/components/telegram/TelegramBotSelect.vue'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
interface Candidate { refund_id: string; order_item_id: string; account_id: string; expected_version: string; snapshot_hash: string; source_ids: number[]; beneficiary_telegram_id: string; disposed: boolean }
const props = withDefaults(defineProps<{ orderId?: number; embedded?: boolean; active?: boolean }>(), { embedded: false, active: true })
const emit = defineEmits<{ updated: [] }>()
const { t, te } = useI18n(), auth = useAdminAuthStore()
const status = ref<Exclude<MembershipLifecycleStatus, ''> | 'all'>('all')
const candidates = ref<Candidate[]>([]), tasks = ref<Task[]>([]), bot = ref(''), error = ref(''), accepted = ref(false), loading = ref(false), saving = ref(false)
const keyword = ref('')
const usernames = ref<Record<string, string>>({})
const expanded = ref<Set<string>>(new Set())
/** Format only the Telegram username supplied for this historical member identity.
 * @param id Member Telegram ID. @returns Username with one @ prefix, or an absent marker.
 */
function usernameLabel(id: string): string {
 const username = usernames.value[id]?.trim().replace(/^@+/, '')
 return username ? `@${username}` : '—'
}
/** Search explicitly from the first page; typing alone never sends a request.
 * @returns Completion of the authorized search, or nothing while another action is active.
 */
function searchMembers(): Promise<void> | undefined {
 if (saving.value || loading.value) return
 return load(1)
}
const members = computed(() => lifecycleMemberViews(tasks.value))
/** Toggle one member without numeric ID conversion. @param id Telegram member ID. @returns Nothing. */
function toggleMember(id: string) {
 const next = new Set(expanded.value)
 if (next.has(id)) next.delete(id)
 else next.add(id)
 expanded.value = next
}
/** Count the existing outcome labels over latest-per-chat evidence only.
 * @param latest Latest tasks for distinct chats. @param kind Independent result axis. @returns Localized counts.
 */
function summary(latest: Task[], kind: 'member' | 'unban' | 'cleanup'): string {
 const counts = new Map<string, number>()
 for (const task of latest) {
  const label = kind === 'member' ? memberLabel(task) : kind === 'unban' ? t(`membershipLifecycle.unbanStates.${unbanState(task)}`) : cleanupLabel(task)
  counts.set(label, (counts.get(label) || 0) + 1)
 }
 return [...counts].map(([label, count]) => `${label} ${count}`).join(' · ')
}
/** Translate known phases without exposing internal codes. @param phase Persisted phase. @returns Safe label. */
function phaseLabel(phase: string): string {
 const key = `lifecycleMembers.phases.${phase}`
 return t(te(key) ? key : 'lifecycleMembers.phases.other')
}
/** Select the latest valid evidence time; missing and zero dates remain unknown.
 * @param rows Tasks to inspect. @param field Timestamp axis. @returns Latest ISO timestamp or undefined.
 */
function latestDate(rows: Task[], field: 'updated_at' | 'last_attempt_at' | 'next_attempt_at'): string | undefined {
 return rows.map(task => task[field]).filter((value): value is string => !!value && !value.startsWith('0001-') && Number.isFinite(Date.parse(value))).sort((left, right) => Date.parse(right) - Date.parse(left))[0]
}
/** Sum attempts only when every latest task carries a valid counter.
 * @param latest Latest task per chat. @returns Total attempts or unknown marker.
 */
function attempts(latest: Task[]): number | string {
 return latest.every(task => Number.isSafeInteger(task.attempt) && task.attempt! >= 0) ? latest.reduce((total, task) => total + task.attempt!, 0) : '—'
}
const pending = ref<{ candidate?: Candidate; task?: Task; action?: string }>()
const pagination = ref({ page: 1, page_size: 20, total: 0 })
let generation = 0, contextVersion = 0
const permission = (method: string, suffix: string) => auth.hasPermission(`${method}:${membershipBase}/${suffix}`)
const canRead = computed(() => permission('GET', props.orderId ? 'refunds' : 'lifecycle/tasks'))
/** Maps actual persisted phase evidence; a successful API command is never a confirmed kick.
 * @param task Durable per-group task. @returns Localized outcome label.
 */
function state(task: Task): string {
 if (task.review_reason === 'payment_pending') return 'payment_pending'
 if (task.review_reason === 'entitlement_active') return 'entitlement_active'
 if (task.review_reason === 'admission_restored') return 'admission_restored'
 // A confirmed removal remains true even when the independent unban step fails.
 if (task.review_reason === 'member_without_entitlement') return 'failed'
 if (task.ban_confirmed || task.ban_outcome === 'succeeded') return 'removed'
 if (task.member_outcome === 'not_required') return 'memberNotRequired'
 if (task.member_outcome && ['removed', 'already_left', 'retained', 'unknown', 'failed', 'processing', 'queued', 'unavailable'].includes(task.member_outcome)) return task.member_outcome
 const memberState = task.member_outcome || task.state
 if (task.phase?.startsWith('unknown') || task.ban_outcome === 'unknown' || task.unban_outcome === 'unknown') return 'unknown'
 if (memberState === 'succeeded') return task.review_reason === 'already_left' ? 'already_left' : task.review_reason === 'removed' ? 'removed' : ['entitlement_active', 'admission_restored'].includes(task.review_reason) ? 'retained' : 'unavailable'
 if (['needs_review', 'failed', 'retryable'].includes(memberState)) return 'failed'
 return memberState === 'creating' ? 'processing' : 'queued'
}
/** Derives only recovery commands accepted by the server's persisted lifecycle evidence.
 * @param task Current server task. @returns Allowed member-recovery actions; link cleanup is separate.
 */
function recoveryActions(task: Task): string[] {
 if (!permission('POST', `lifecycle/tasks/${task.id}/recover`) || task.ban_blocked || task.state !== 'needs_review') return []
 const actions = ['recheck']
 if (task.unban_outcome !== 'succeeded' && !task.phase?.startsWith('unknown') && task.ban_outcome !== 'unknown' && task.unban_outcome !== 'unknown') actions.push('retry')
 actions.push('block_ban')
 return actions
}
/** Translate lifecycle-only results without changing refund copy. @param task Persisted task. @returns Localized outcome. */
function memberLabel(task: Task): string {
 const value = state(task)
 return t(`${['payment_pending', 'entitlement_active', 'admission_restored'].includes(value) ? 'membershipLifecycle.results' : 'membershipDisposition'}.${value}`)
}
/** Keep unban evidence independent from member removal. @param task Persisted task. @returns Known unban status. */
function unbanState(task: Task): string {
 if (task.ban_blocked) return 'blocked'
 if (task.phase === 'unknown_unban' || task.unban_outcome === 'unknown') return 'unknown'
 if (task.unban_outcome && ['succeeded', 'failed', 'retryable'].includes(task.unban_outcome)) return task.unban_outcome
 if (task.ban_confirmed || task.ban_outcome === 'succeeded') return task.state === 'creating' && task.phase === 'unban' ? 'processing' : 'pending'
 return ['already_left', 'entitlement_active'].includes(task.review_reason) ? 'not_required' : 'unavailable'
}
/** Explain safe server review categories; unrecognized codes never become raw user-facing errors.
 * @param task Persisted task. @returns Localized reason, or no reason when absent.
 */
function reasonLabel(task: Task): string {
 if (!task.review_reason) return ''
 const key = `membershipLifecycle.reasons.${task.review_reason}`
 return te(key) ? t(key) : t('membershipLifecycle.reasons.other')
}
/** Treat missing, invalid and Go zero dates as unknown. @param value ISO observation time. @returns Shared date formatting. */
function observedDate(value?: string | null): string {
 return value && !value.startsWith('0001-') && Number.isFinite(Date.parse(value)) ? formatDate(value) : t('membershipLifecycle.unobserved')
}
/** Display only observed membership, never infer presence from restored admission.
 * @param task Persisted task. @returns Localized member status and presence.
 */
function observationLabel(task: Task): string {
 if (!task.observed_at || task.observed_at.startsWith('0001-') || !Number.isFinite(Date.parse(task.observed_at))) return t('membershipLifecycle.unobserved')
 const key = `telegramBot.groups.values.${task.member_status}`
 const label = task.member_status && te(key) ? t(key) : t('membershipDisposition.unavailable')
 return `${label} · ${t(`membershipLifecycle.${task.member_is_member === true ? 'present' : task.member_is_member === false ? 'absent' : 'unobserved'}`)}`
}
/** Name the existing retry command by its persisted phase. @param task Task. @param action Existing API action. @returns Localized action. */
function actionLabel(task: Task, action: string): string {
 return t(`membershipLifecycle.actions.${action === 'retry' ? task.ban_confirmed ? 'retryUnban' : 'retryRemoval' : action}`)
}
/** Resolves only known cleanup facts; missing historical evidence never implies success.
 * @param task Persisted lifecycle projection. @returns Localized cleanup label.
 */
function cleanupLabel(task: Task): string {
 const value = task.invite_cleanup
 const known = ['pending', 'processing', 'succeeded', 'partial', 'failed', 'unknown', 'retained', 'not_required']
 return t(`membershipDisposition.cleanupStates.${value && known.includes(value) ? value : 'unavailable'}`)
}
/** Formats nonnegative integral cleanup counters without interpreting absence as zero.
 * @param task Persisted lifecycle projection. @returns Counts, or an empty string for absent evidence.
 */
function cleanupCounts(task: Task): string {
 if (!task.invite_cleanup || task.invite_cleanup === 'unconfirmed') return ''
 const counts = [task.invite_cleanup_total, task.invite_cleanup_completed, task.invite_cleanup_failed, task.invite_cleanup_unknown]
 if (!counts.every(value => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0)) return ''
 return t('membershipDisposition.cleanupCounts', { total: task.invite_cleanup_total, settled: task.invite_cleanup_completed, failed: task.invite_cleanup_failed, unknown: task.invite_cleanup_unknown })
}
/** Loads one authorized numbered page, retaining current rows/page on failure.
 * @param page Requested page; omitted refreshes current. @param size Requested page size.
 * @returns Completion of visible refresh.
 */
async function load(page = pagination.value.page, size = pagination.value.page_size) {
 if (!canRead.value || (!props.orderId && !/^[1-9]\d*$/.test(bot.value))) return
 const request = ++generation
 if (page !== pagination.value.page || size !== pagination.value.page_size) expanded.value = new Set()
 // New numbered-page requests invalidate an old confirmation as well as late reads.
 if (!saving.value) { contextVersion++; pending.value = undefined }
 loading.value = true; error.value = ''; accepted.value = false
 try {
  if (props.orderId) {
   const response = await api.refunds(props.orderId)
   if (request === generation) candidates.value = response.data.data.items || []
  } else {
   // A recovery can remove members from the active filter. Requery a valid page
   // within this request generation so stale responses cannot change the new scope.
   while (true) {
    const response = await api.lifecycleMembers({ bot_id: bot.value, page, page_size: size, status: status.value === 'all' ? '' : status.value, keyword: keyword.value.trim() || undefined })
    if (request !== generation) return
    const data = response.data?.data
    if (!Array.isArray(data?.items) || !Number.isSafeInteger(data.total) || data.total < 0) throw new Error(t('common.api.responseMissing'))
    const total: number = data.total
    const lastPage = Math.max(1, Math.ceil(total / size))
    if (page > lastPage) {
     page = lastPage
     expanded.value = new Set()
     if (total > 0) continue
    }
    const items: MembershipLifecycleMember[] = response.data.data.items || []
    tasks.value = items.flatMap(member => member.tasks.map(task => ({ ...task, telegram_id: member.telegram_id })))
    const visible = new Set(items.map(member => member.telegram_id))
    usernames.value = Object.fromEntries(items.map(member => [member.telegram_id, member.telegram_username || '']))
    expanded.value = new Set([...expanded.value].filter(id => visible.has(id)))
    pagination.value = { page, page_size: size, total }
    break
   }
  }
 } catch (cause) {
  if (request === generation) {
   error.value = t('common.api.requestFailed')
   if (!props.orderId) notifyErrorOnce(cause, t('common.api.requestFailed'))
  }
 }
 finally { if (request === generation) { loading.value = false; emit('updated') } }
}
/** Sends an explicit versioned decision and then reloads server evidence.
 * @returns Completion; obsolete authorization contexts cannot publish response state.
 */
async function submit() {
 const choice = pending.value
 if (!choice || saving.value || !props.active || !canRead.value) return
 if (choice.task && (!/^[1-9]\d*$/.test(bot.value) || !recoveryActions(choice.task).includes(choice.action || ''))) return
 const suffix = choice.candidate ? 'refunds/dispose' : `lifecycle/tasks/${choice.task?.id}/recover`
 if (!permission('POST', suffix)) return
 const request = contextVersion
 saving.value = true; error.value = ''
 try {
  if (choice.candidate) await api.disposeRefund({ ...choice.candidate })
  else if (choice.task) await api.recoverLifecycle(bot.value, choice.task.id, { expected_version: choice.task.version, action: choice.action })
  if (request !== contextVersion) return
  pending.value = undefined
  await load()
  if (request === contextVersion) accepted.value = true
 } catch (cause) {
  if (request === contextVersion) {
   error.value = t('common.api.requestFailed')
   if (!props.orderId) notifyErrorOnce(cause, t('common.api.requestFailed'))
   emit('updated')
  }
 }
 finally { if (request === contextVersion) saving.value = false }
}
/** 清理列表、页码和待确认操作，丢弃旧上下文回包。@returns 无。 */
function clearContext() {
 generation++; contextVersion++; usernames.value = {}; expanded.value = new Set(); candidates.value = []; tasks.value = []; pending.value = undefined; pagination.value = { ...pagination.value, page: 1, total: 0 }; error.value = ''; accepted.value = false; loading.value = false; saving.value = false
}
/** 保留必填机器人范围，将成员查询恢复默认条件并读取第一页。@returns 查询完成。 */
async function resetFilters(): Promise<void> {
 if (saving.value || loading.value || props.orderId) return
 status.value = 'all'; keyword.value = ''
 clearContext()
 await searchMembers()
}
watch(() => [props.orderId, bot.value, status.value, keyword.value, auth.token, auth.isSuper, JSON.stringify(auth.permissions)], clearContext, { flush: 'sync' })
// Hidden panels retain filters, not teleported confirmation dialogs.
watch(() => props.active, active => { if (!active) pending.value = undefined }, { flush: 'sync' })
onBeforeUnmount(() => { generation++; contextVersion++ })
</script>
<template>
 <section v-if="canRead" class="space-y-3" :class="{ 'border-t border-border pt-3': !embedded }">
  <h3 v-if="!embedded" class="font-semibold">{{ t(`membershipDisposition.${orderId ? 'title' : 'lifecycle'}`) }}</h3>
  <p class="text-sm text-muted-foreground">{{ t(orderId ? 'membershipDisposition.hint' : 'membershipDisposition.lifecycleHint') }}</p>
  <div class="flex flex-wrap items-center gap-3"><TelegramBotSelect v-if="!orderId" v-model="bot" required class="w-56" :disabled="saving" /><Input v-if="!orderId" v-model="keyword" class="h-9 w-64" :maxlength="64" :placeholder="t('lifecycleMembers.search')" :aria-label="t('lifecycleMembers.search')" :disabled="saving" @keydown.enter="searchMembers" /><Select v-if="!orderId" v-model="status" :disabled="saving"><SelectTrigger class="h-9 w-40" :aria-label="t('membershipLifecycle.filter')"><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="value in ['all', 'processing', 'needs_review', 'completed']" :key="value" :value="value">{{ t(`membershipLifecycle.filters.${value}`) }}</SelectItem></SelectContent></Select><Button variant="outline" :disabled="loading || saving || (!orderId && !/^[1-9]\d*$/.test(bot))" @click="searchMembers">{{ t(orderId ? 'membershipDisposition.load' : 'admin.common.search') }}</Button><Button v-if="!orderId" variant="outline" :disabled="loading || saving" @click="resetFilters">{{ t('admin.common.reset') }}</Button></div>
  <p v-if="loading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="error && orderId" role="alert" class="text-destructive">{{ error }}</p><p v-if="accepted" role="status">{{ t('membershipDisposition.accepted') }}</p>
  <p v-if="!orderId" class="text-xs text-muted-foreground">{{ t('lifecycleMembers.latest') }}</p>
  <div class="rounded-xl border border-border bg-card overflow-hidden">
   <Table v-if="orderId">
    <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
     <TableHead class="px-4 py-3">{{ t('membershipDisposition.refund') }}</TableHead><TableHead class="px-4 py-3">{{ t('membershipDisposition.item') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.identity') }}</TableHead><TableHead class="px-4 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
    </TableRow></TableHeader>
    <TableBody class="divide-y divide-border">
     <TableRow v-for="row in candidates" :key="`${row.refund_id}:${row.order_item_id}`" class="hover:bg-muted/30">
      <TableCell class="px-4 py-3">{{ row.refund_id }}</TableCell><TableCell class="px-4 py-3">{{ row.order_item_id }}<p v-if="!row.source_ids.length && !row.disposed" class="text-sm text-muted-foreground">{{ t('membershipDisposition.unposted') }}</p></TableCell><TableCell class="px-4 py-3">{{ row.beneficiary_telegram_id }}</TableCell>
      <TableCell class="px-4 py-3 text-right"><div class="flex flex-wrap items-center justify-end gap-2"><span v-if="row.disposed">{{ t('membershipDisposition.disposed') }}</span><Button v-else-if="permission('POST', 'refunds/dispose')" size="sm" variant="outline" :disabled="saving || loading" @click="pending = { candidate: row }" :title="t('membershipDisposition.revoke')" :aria-label="t('membershipDisposition.revoke')">{{ t('tableAction.dispose') }}</Button></div></TableCell>
     </TableRow>
     <TableRow v-if="!candidates.length && !loading"><TableCell :colspan="4" class="px-4 py-8 text-center text-muted-foreground">{{ t('membershipDisposition.empty') }}</TableCell></TableRow>
    </TableBody>
   </Table>
   <TooltipProvider v-else>
   <Table>
    <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
     <TableHead v-for="column in ['member', 'username', 'groups']" :key="column" class="px-6 py-3">{{ t(`lifecycleMembers.${column}`) }}</TableHead>
     <TableHead class="px-6 py-3">{{ t('membershipDisposition.memberOutcome') }}</TableHead><TableHead class="px-6 py-3">{{ t('membershipLifecycle.unban') }}</TableHead><TableHead class="px-6 py-3">{{ t('membershipDisposition.inviteCleanup') }}</TableHead><TableHead class="px-6 py-3">{{ t('lifecycleMembers.attempt') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('lifecycleMembers.updated') }}</TableHead>
    </TableRow></TableHeader>
    <TableBody class="divide-y divide-border">
     <template v-for="member in members" :key="member.telegram_id">
      <TableRow class="hover:bg-muted/30" data-testid="lifecycle-member">
       <TableCell class="px-6 py-4 font-mono text-xs whitespace-nowrap">{{ member.telegram_id }}</TableCell>
       <TableCell class="px-6 py-4 text-xs whitespace-nowrap"><span class="block max-w-40 truncate" :title="usernameLabel(member.telegram_id)" data-testid="member-username">{{ usernameLabel(member.telegram_id) }}</span></TableCell>
       <TableCell class="px-6 py-4">
        <div class="flex items-center gap-1"><span class="font-mono text-xs break-all" data-testid="member-groups">{{ expanded.has(member.telegram_id) ? member.chatIds.join(', ') : member.chatIds[0] }}</span><span v-if="!expanded.has(member.telegram_id) && member.chatIds.length > 1" class="text-xs text-muted-foreground">{{ t('lifecycleMembers.groupCount', { count: member.chatIds.length }) }}</span><Button variant="ghost" size="sm" :aria-label="t(expanded.has(member.telegram_id) ? 'lifecycleMembers.collapse' : 'lifecycleMembers.expand')" :aria-expanded="expanded.has(member.telegram_id)" @click="toggleMember(member.telegram_id)"><ChevronDown v-if="expanded.has(member.telegram_id)" class="h-4 w-4" /><ChevronRight v-else class="h-4 w-4" /></Button></div>
        <p v-if="member.historyPending" class="text-xs text-muted-foreground">{{ t('lifecycleMembers.historyPending', { count: member.historyPending }) }}</p>
       </TableCell>
       <TableCell class="px-6 py-4 text-xs">{{ summary(member.memberEvidence, 'member') }}</TableCell>
       <TableCell class="px-6 py-4 text-xs">{{ summary(member.memberEvidence, 'unban') }}</TableCell>
       <TableCell class="px-6 py-4 text-xs">{{ summary(member.latest, 'cleanup') }}</TableCell>
       <TableCell class="px-6 py-4"><Tooltip><TooltipTrigger as-child><span tabindex="0">{{ attempts(member.memberEvidence) }}</span></TooltipTrigger><TooltipContent><p>{{ t('membershipRecovery.lastAttempt') }}: {{ observedDate(latestDate(member.memberEvidence, 'last_attempt_at')) }}</p><p>{{ t('membershipRecovery.next') }}: {{ observedDate(latestDate(member.latest, 'next_attempt_at')) }}</p></TooltipContent></Tooltip></TableCell>
       <TableCell class="px-6 py-4 text-right text-xs">{{ observedDate(latestDate(member.tasks, 'updated_at')) }}</TableCell>
      </TableRow>
      <TableRow v-if="expanded.has(member.telegram_id)" data-testid="lifecycle-details">
       <TableCell :colspan="8" class="px-6 py-3 bg-muted/10">
        <Table :aria-label="t('lifecycleMembers.details')">
         <TableHeader><TableRow><TableHead>{{ t('lifecycleMembers.groups') }}</TableHead><TableHead>{{ t('membershipDisposition.memberOutcome') }}</TableHead><TableHead>{{ t('membershipLifecycle.unban') }}</TableHead><TableHead>{{ t('membershipDisposition.inviteCleanup') }}</TableHead><TableHead>{{ t('membershipLifecycle.reason') }}</TableHead><TableHead>{{ t('membershipLifecycle.observation') }}</TableHead><TableHead>{{ t('lifecycleMembers.attempt') }}</TableHead><TableHead>{{ t('lifecycleMembers.updated') }}</TableHead><TableHead class="text-right">{{ t('telegramBot.instances.actions') }}</TableHead></TableRow></TableHeader>
         <TableBody><TableRow v-for="row in member.tasks" :key="row.id" :data-task-id="row.id" class="hover:bg-muted/30">
          <TableCell><span class="font-mono text-xs">{{ row.chat_id }}</span><p class="text-xs text-muted-foreground">{{ t(member.latest.some(task => task.id === row.id) ? 'lifecycleMembers.latestTask' : member.memberEvidence.some(task => task.id === row.id) ? 'lifecycleMembers.evidenceTask' : 'lifecycleMembers.history') }}</p></TableCell>
          <TableCell><Badge :variant="membershipStatusVariant(state(row))">{{ memberLabel(row) }}</Badge></TableCell>
          <TableCell><Badge :variant="membershipStatusVariant(unbanState(row))">{{ t(`membershipLifecycle.unbanStates.${unbanState(row)}`) }}</Badge></TableCell>
          <TableCell><Badge :variant="membershipStatusVariant(row.invite_cleanup)">{{ cleanupLabel(row) }}</Badge><p v-if="cleanupCounts(row)" class="text-xs text-muted-foreground">{{ cleanupCounts(row) }}</p></TableCell>
          <TableCell class="text-xs text-muted-foreground"><p>{{ phaseLabel(row.phase) }}</p><p>{{ reasonLabel(row) || '—' }}</p></TableCell>
          <TableCell class="text-xs text-muted-foreground"><p>{{ observationLabel(row) }}</p><p>{{ observedDate(row.observed_at) }}</p></TableCell>
          <TableCell><Tooltip><TooltipTrigger as-child><span tabindex="0">{{ row.attempt ?? '—' }}</span></TooltipTrigger><TooltipContent><p>{{ t('membershipRecovery.lastAttempt') }}: {{ observedDate(row.last_attempt_at) }}</p><p>{{ t('membershipRecovery.next') }}: {{ observedDate(row.next_attempt_at) }}</p></TooltipContent></Tooltip></TableCell>
          <TableCell class="text-xs whitespace-nowrap">{{ observedDate(row.updated_at) }}</TableCell>
          <TableCell class="text-right"><div v-if="recoveryActions(row).length" class="flex flex-wrap items-center justify-end gap-2"><Button v-for="action in recoveryActions(row)" :key="action" variant="outline" size="sm" :disabled="saving || loading" @click="pending = { task: row, action }" :title="actionLabel(row, action)" :aria-label="actionLabel(row, action)">{{ actionLabel(row, action) }}</Button></div></TableCell>
         </TableRow></TableBody>
        </Table>
       </TableCell>
      </TableRow>
     </template>
     <TableRow v-if="!members.length && !loading && !error"><TableCell :colspan="8" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
    </TableBody>
   </Table>
   </TooltipProvider>
   <ListPagination v-if="!orderId" always-show :page="pagination.page" :page-size="pagination.page_size" :total="pagination.total" :total-page="Math.ceil(pagination.total / pagination.page_size)" :page-size-options="[10, 20, 50, 100]" :loading="loading" :disabled="saving || !/^[1-9]\d*$/.test(bot)" @change-page="load" @change-page-size="size => load(1, size)" />
  </div>
  <Dialog :open="active && !!pending" @update:open="value => { if (!value && !saving) pending = undefined }"><DialogContent><DialogHeader><DialogTitle>{{ pending?.task ? actionLabel(pending.task, pending.action || 'recheck') : t('membershipDisposition.title') }}</DialogTitle><DialogDescription>{{ pending?.task ? t(`membershipLifecycle.confirm.${pending.action === 'retry' ? pending.task.ban_confirmed ? 'retryUnban' : 'retryRemoval' : pending.action}`) : t('membershipDisposition.confirm') }}</DialogDescription><p v-if="pending?.task" class="text-sm font-mono break-all">{{ t('lifecycleMembers.confirmIdentity', { member: pending.task.telegram_id, chat: pending.task.chat_id }) }}</p></DialogHeader><DialogFooter><Button :disabled="saving" @click="submit">{{ t('membershipRecovery.confirmAction') }}</Button></DialogFooter></DialogContent></Dialog>
 </section>
</template>
