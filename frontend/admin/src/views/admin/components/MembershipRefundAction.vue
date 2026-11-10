<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramMembershipAPI, membershipBase, type MembershipOrder } from '@/api/telegramMembership'
import { membershipRefundAPI, type MembershipRefundInput, type MembershipRefundResult, type MembershipRefundPreview } from '@/api/membershipRefund'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { formatDate, formatMoney, getLocalizedText } from '@/utils/format'

import { canOfferMembershipRefund } from '@/utils/membershipOrder'
const props = defineProps<{ order: MembershipOrder; recovery?: boolean }>()
const emit = defineEmits<{ (event: 'refresh'): void }>()
const { t } = useI18n(), auth = useAdminAuthStore()
const open = ref(false), loading = ref(false), busy = ref(false), error = ref('')
const detail = ref<MembershipOrder>(), result = ref<MembershipRefundResult>(), command = ref<MembershipRefundInput>()
const selected = ref<number[]>([]), method = ref<'wallet' | 'manual' | 'existing'>('manual')
const walletWarningOpen = ref(false), walletAcknowledged = ref(false)
const confirmOpen = ref(false), manualConfirmed = ref(false), preview = ref<MembershipRefundPreview>()
const draft = ref<MembershipRefundInput>()
const amount = ref(''), existingID = ref(''), remark = ref('')
let generation = 0
const allowedMethods = computed(() => (['manual', 'wallet', 'existing'] as const).filter(value => auth.hasPermission(`POST:/admin/orders/${props.order.id}/${value === 'wallet' ? 'refund-to-wallet' : 'manual-refund'}`)))
const allowed = computed(() => allowedMethods.value.length > 0 && auth.hasPermission(`POST:${membershipBase}/refunds/dispose`) && auth.hasPermission(`GET:/admin/orders/${props.order.id}`) && auth.hasPermission(`GET:${membershipBase}/orders/${props.order.id}`) && auth.hasPermission(`GET:${membershipBase}/orders/${props.order.id}/refund-and-terminate`) && auth.hasPermission(`POST:${membershipBase}/orders/${props.order.id}/refund-and-terminate`))
const visible = computed(() => allowed.value && (props.recovery ? Boolean(props.order.paid_at) : canOfferMembershipRefund(props.order)))
/** 合并主单汇总与子单明细，同一实际订单项仅展示一次。@returns 本单唯一会员订单项 */
const items = computed(() => {
  const candidates = [
    ...(detail.value?.items || []),
    ...(detail.value?.children || []).flatMap(child => child.items || []),
  ].filter(item => item.membership_snapshot)
  // 主单汇总可能重写 order_id，必须以实际订单项 id 而非所属订单 id 去重。
  return [...new Map(candidates.map(item => [item.id, item])).values()]
})
const remaining = computed(() => Math.max(0, Number(detail.value?.total_amount || 0) - Number(detail.value?.refunded_amount || 0)))
/** 填充线下退款默认值，不覆盖管理员已修改的金额或说明。@returns 无。 */
function fillManualDefaults(): void {
  if (method.value !== 'manual' || command.value || !detail.value) return
  if (!amount.value) amount.value = remaining.value.toFixed(2)
  if (!remark.value) remark.value = t('membershipRefund.defaultRemark')
}
/** 选择退款方式，账户余额退款须先确认资金流向。@param value 选中的方式。@returns 无。 */
function changeMethod(value: unknown): void {
  if (busy.value || command.value || !allowedMethods.value.includes(value as typeof method.value)) return
  if (value === 'wallet') { walletWarningOpen.value = true; return }
  method.value = value as typeof method.value
  walletAcknowledged.value = false; manualConfirmed.value = false; error.value = ''
  fillManualDefaults()
}
/** 确认余额退款风险，仅改变本地选择，不请求退款。@returns 无。 */
function acceptWallet(): void {
  if (!open.value || !allowed.value || !allowedMethods.value.includes('wallet') || command.value) return
  method.value = 'wallet'; walletAcknowledged.value = true; walletWarningOpen.value = false
  manualConfirmed.value = false; error.value = ''
}
/** Reset sensitive context after permission/account/order changes. @returns Nothing. */
function reset() { generation++; walletWarningOpen.value = false; walletAcknowledged.value = false; confirmOpen.value = false; preview.value = undefined; draft.value = undefined; manualConfirmed.value = false; open.value = false; detail.value = undefined; command.value = undefined; result.value = undefined; selected.value = []; amount.value = ''; existingID.value = ''; remark.value = ''; error.value = ''; busy.value = false; loading.value = false }
/** Load authoritative items and persisted pending commands before enabling another monetary submission. @returns Completion. */
async function show() {
  if (!allowed.value || busy.value || loading.value) return
  const request = ++generation
  // Never expose stale detail after a failed refresh, or replace an uncertain command with a new key.
  detail.value = undefined
  open.value = true; loading.value = true; error.value = ''
  method.value = allowedMethods.value[0] || 'manual'
  manualConfirmed.value = false; walletAcknowledged.value = false; walletWarningOpen.value = false
  try {
    const [orderResponse, requestsResponse] = await Promise.all([telegramMembershipAPI.order(props.order.id), membershipRefundAPI.requests(props.order.id)])
    if (request !== generation || !allowed.value) return
    detail.value = orderResponse.data.data
    const pending = (requestsResponse.data.data?.items || []).find((row: MembershipRefundResult) => row.rights_state === 'pending')
    if (pending) { command.value = { request_key: pending.request_key, method: pending.method, amount: pending.amount, existing_refund_id: pending.existing_refund_id, item_ids: pending.item_ids, remark: pending.remark }; result.value = pending }
    else if (props.recovery) error.value = t('membershipOrderState.noPendingRefund')
    fillManualDefaults()
    if (!command.value && method.value === 'wallet') walletWarningOpen.value = true
  } catch (cause) { if (request === generation) error.value = cause instanceof Error ? cause.message : t('common.api.requestFailed') }
  finally { if (request === generation) loading.value = false }
}
/** Toggle an explicit purchase selection; no monetary ratio is used. @param id Actual item ID. @returns Nothing. */
function toggle(id: number) { selected.value = selected.value.includes(id) ? selected.value.filter(value => value !== id) : [...selected.value, id] }
/** Prepare an immutable draft and fetch authoritative rights before opening final confirmation. @returns Completion. */
async function submit() {
  if (props.recovery && !command.value) return
  if (busy.value || loading.value || !detail.value || !allowed.value || result.value?.rights_state === 'queued') return
  const effectiveMethod = command.value?.method || method.value
  if (effectiveMethod === 'wallet' && !command.value && !walletAcknowledged.value) { walletWarningOpen.value = true; return }
  if (effectiveMethod === 'manual' && !manualConfirmed.value) { error.value = t('membershipRefund.manualConfirm'); return }
  if (!command.value) {
    const validMoney = /^\d+(\.\d{1,2})?$/.test(amount.value) && Number(amount.value) > 0 && Number(amount.value) <= remaining.value
    if (!selected.value.length || (method.value === 'existing' ? !/^[1-9]\d*$/.test(existingID.value) : !validMoney)) { error.value = t('membershipRefund.required'); return }
  }
  const input: MembershipRefundInput = command.value || { request_key: crypto.randomUUID(), method: method.value, amount: method.value === 'existing' ? '' : amount.value, existing_refund_id: method.value === 'existing' ? existingID.value : '0', item_ids: [...selected.value], remark: remark.value }
  const request = generation
  busy.value = true; error.value = ''; preview.value = undefined
  try {
    const response = await membershipRefundAPI.preview(props.order.id, input.method, input.item_ids)
    if (request !== generation || !allowed.value) return
    preview.value = response.data.data
    if (!preview.value?.groups.length) throw new Error(t('membershipRefund.previewUnavailable'))
    draft.value = { ...input, manual_confirmed: input.method === 'manual' && manualConfirmed.value }
    confirmOpen.value = true
  } catch (cause) { if (request === generation) error.value = cause instanceof Error ? cause.message : t('membershipRefund.previewUnavailable') }
  finally { if (request === generation) busy.value = false }
}
/** Commit only the reviewed draft; uncertain outcomes preserve the exact idempotency key. @returns Completion. */
async function confirmSubmit() {
  if (!confirmOpen.value || !draft.value || !preview.value || busy.value || !allowed.value) return
  command.value = draft.value
  confirmOpen.value = false
  const request = generation
  busy.value = true; error.value = ''
  try {
    const response = await membershipRefundAPI.execute(props.order.id, command.value)
    if (request !== generation || !allowed.value) return
    result.value = response.data.data
    emit('refresh')
  } catch { if (request === generation) error.value = t('membershipRefund.unknown') }
  finally { if (request === generation) busy.value = false }
}
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions), props.order.id], reset, { flush: 'sync' })
watch(open, value => { if (!value) walletWarningOpen.value = false })
onBeforeUnmount(reset)
</script>

<template>
  <Button v-if="visible" size="xs" variant="outline" :title="t(recovery ? 'membershipOrderState.recoverRefund' : 'membershipOrderState.refundedAction')" :aria-label="t(recovery ? 'membershipOrderState.recoverRefund' : 'membershipOrderState.refundedAction')" @click="show">{{ t(recovery ? 'tableAction.recover' : 'tableAction.refund') }}</Button>
  <Dialog v-model:open="open">
    <DialogScrollContent class="sm:max-w-3xl" @escape-key-down="busy && $event.preventDefault()" @interact-outside="busy && $event.preventDefault()">
      <DialogHeader><DialogTitle>{{ t('membershipRefund.title') }} · {{ order.order_no }}</DialogTitle><DialogDescription>{{ t('membershipRefund.hint') }}</DialogDescription></DialogHeader>
      <p v-if="loading" role="status" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
      <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
      <template v-if="detail && !loading && (!recovery || command)">
        <template v-if="!command">
          <p class="text-sm text-muted-foreground">{{ t('membershipRefund.remaining') }}: {{ formatMoney(remaining, detail.currency) }}</p>
          <Select :model-value="method" @update:model-value="changeMethod"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="value in allowedMethods" :key="value" :value="value">{{ t(`membershipRefund.${value}`) }}</SelectItem></SelectContent></Select>
          <p v-if="method === 'wallet'" class="text-sm text-destructive">{{ t('membershipRefund.walletWarning') }}</p>
          <p v-if="method === 'manual'" class="text-sm text-muted-foreground">{{ t('membershipRefund.manualHint') }}</p>
          <Label>{{ t(method === 'existing' ? 'membershipRefund.refundID' : 'membershipRefund.amount') }}<Input v-if="method === 'existing'" v-model="existingID" inputmode="numeric" /><Input v-else v-model="amount" inputmode="decimal" /></Label>
          <Label>{{ t('membershipRefund.remark') }}<Input v-model="remark" maxlength="1000" /></Label>
          <div class="overflow-x-auto rounded-xl border border-border bg-card"><Table><TableHeader class="bg-muted/40"><TableRow><TableHead>{{ t('membershipRefund.item') }}</TableHead><TableHead>{{ t('admin.orders.table.amount') }}</TableHead></TableRow></TableHeader><TableBody><TableRow v-for="item in items" :key="item.id"><TableCell><label class="flex items-center gap-2"><Checkbox :model-value="selected.includes(item.id)" @update:model-value="toggle(item.id)" />{{ item.id }} {{ getLocalizedText(item.title) }}</label></TableCell><TableCell>{{ formatMoney(item.total_price, detail.currency) }}</TableCell></TableRow></TableBody></Table></div>
          <p v-if="!items.length" class="text-sm text-muted-foreground">{{ t('membershipRefund.noItems') }}</p>
        </template>
        <label v-if="(command?.method || method) === 'manual'" class="flex items-center gap-2 text-sm"><Checkbox v-model="manualConfirmed" />{{ t('membershipRefund.manualConfirm') }}</label>
        <div v-if="result" role="status" class="space-y-2 text-sm"><p>{{ t('membershipRefund.funds') }} · #{{ result.refund_id }}</p><p>{{ t(`membershipRefund.${result.rights_state === 'queued' ? 'queued' : 'pending'}`) }}</p><p>{{ t('membershipRefund.removalUnconfirmed') }}</p><p>{{ t('membershipRefund.cleanupUnconfirmed') }}</p></div>
        <div class="flex justify-end"><Button v-if="result?.rights_state !== 'queued'" size="sm" :disabled="busy || !items.length" @click="submit">{{ busy ? t('telegramBot.instances.loading') : t(command ? 'membershipRefund.retry' : 'membershipRefund.submit') }}</Button></div>
      </template>
    </DialogScrollContent>
  </Dialog>
  <Dialog v-model:open="walletWarningOpen"><DialogScrollContent class="sm:max-w-lg">
    <DialogHeader><DialogTitle>{{ t('membershipRefund.walletWarningTitle') }}</DialogTitle><DialogDescription>{{ t('membershipRefund.walletWarning') }}</DialogDescription></DialogHeader>
    <div class="flex justify-end gap-2"><Button variant="outline" @click="walletWarningOpen = false">{{ t('membershipRefund.cancel') }}</Button><Button variant="destructive" @click="acceptWallet">{{ t('membershipRefund.walletAccept') }}</Button></div>
  </DialogScrollContent></Dialog>
  <Dialog v-model:open="confirmOpen"><DialogScrollContent class="sm:max-w-3xl">
    <DialogHeader><DialogTitle>{{ t('membershipRefund.confirmTitle') }}</DialogTitle><DialogDescription>{{ t('membershipRefund.previewHint') }}</DialogDescription></DialogHeader>
    <p v-if="draft" class="text-sm">{{ t(`membershipRefund.${draft.method}`) }} · {{ draft.method === 'existing' ? `#${draft.existing_refund_id}` : formatMoney(draft.amount, detail?.currency) }}</p>
    <p v-if="draft?.method === 'wallet'" class="text-sm text-destructive">{{ t('membershipRefund.walletWarning') }}</p>
    <p class="text-xs text-muted-foreground">{{ t('membershipRefund.observedAt') }}: {{ formatDate(preview?.observed_at) }}</p>
    <Table><TableHeader><TableRow><TableHead>{{ t('membershipRefund.group') }}</TableHead><TableHead>{{ t('membershipRefund.afterRights') }}</TableHead></TableRow></TableHeader><TableBody><TableRow v-for="group in preview?.groups" :key="`${group.item_id}:${group.target_id}`"><TableCell>{{ group.item_id }} · {{ group.target_id }} · {{ group.chat_id }}<br />{{ group.beneficiary_telegram_id }}</TableCell><TableCell>{{ group.permanent ? t('membershipRefund.permanent') : group.active ? formatDate(group.expires_at) : t('membershipRefund.noRights') }}</TableCell></TableRow></TableBody></Table>
    <div class="flex justify-end gap-2"><Button variant="outline" @click="confirmOpen = false">{{ t('membershipRefund.cancel') }}</Button><Button :disabled="busy" @click="confirmSubmit">{{ t('membershipRefund.submit') }}</Button></div>
  </DialogScrollContent></Dialog>
</template>
