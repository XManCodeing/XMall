<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminOrder, AdminPayment } from '@/api/types'
import { isManualPaymentReview, manualPaymentAPI, manualPaymentPath, validManualPaymentInput, type ManualPaymentReview, type ManualPaymentInput } from '@/api/manualPayment'
import { useAdminAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { paymentStatusLabel, orderStatusLabel } from '@/utils/status'
import { formatDate, toISO } from '@/utils/format'

const props = defineProps<{ order: AdminOrder; autoLoad?: boolean }>()
const emit = defineEmits<{ (event: 'refresh'): void; (event: 'busy', value: boolean): void }>()
const auth = useAdminAuthStore(), { t } = useI18n()
const open = ref(false), busy = ref(false), listBusy = ref(false), error = ref(''), listError = ref('')
const payment = ref<AdminPayment>(), review = ref<ManualPaymentReview>(), rows = ref<AdminPayment[]>([])
const step = ref<'edit' | 'confirm'>('edit'), acknowledged = ref(false), locked = ref(false), accepted = ref(false)
const paymentsLoaded = ref(false)
const timeoutOpen = ref(false)
const page = ref(1), totalPages = ref(1)
const draft = reactive({ amount: '', currency: '', paid_at: '', transaction_id: '', evidence_reference: '', reason: '' })
let generation = 0, listGeneration = 0
const rootOrder = computed(() => !props.order.parent_id)
/** Authorize the exact payment route, never inherit order-update authority. @param method Verb. @param id Payment ID. @returns Permission. */
const can = (method: string, id: number) => auth.hasPermission(`${method}:${manualPaymentPath(id)}`)
const availableRows = computed(() => (props.order.payments || rows.value).filter(row => row.order_id === props.order.id))
const canSubmit = computed(() => !!payment.value && can('POST', payment.value.id) && review.value?.can_confirm === true
  && rootOrder.value && !locked.value && !accepted.value && !busy.value)
const fields = ['amount', 'currency', 'paid_at', 'transaction_id', 'evidence_reference', 'reason'] as const
/** Convert UTC+08 date entry to RFC3339 without modifying the draft. @returns Request evidence. */
function evidence() {
  return { ...draft, amount: draft.amount.trim(), currency: draft.currency.trim().toUpperCase(),
    paid_at: toISO(draft.paid_at), transaction_id: draft.transaction_id.trim(),
    evidence_reference: draft.evidence_reference.trim(), reason: draft.reason.trim() }
}
const valid = computed(() => validManualPaymentInput(evidence()))
/** Map errors without exposing provider payloads. @param cause Network rejection. @returns Localized safe message. */
function errorMessage(cause: unknown) {
  const status = (cause as { response?: { status?: number } })?.response?.status
  return t(`manualPayment.${status === 403 ? 'forbidden' : status === 409 ? 'conflict' : 'uncertain'}`)
}
/** Load actual records with exact order filter; never infer a payment ID from an order ID. @param nextPage List page. @returns Completion. */
async function loadPayments(nextPage = 1) {
  if (!rootOrder.value || props.order.payments || !auth.hasPermission('GET:/admin/payments') || listBusy.value) return
  const request = ++listGeneration, orderID = props.order.id
  listBusy.value = true; listError.value = ''
  try {
    const response = await adminAPI.getPayments({ order_id: orderID, page: nextPage, page_size: 20 })
    if (request !== listGeneration || orderID !== props.order.id) return
    rows.value = Array.isArray(response.data.data) ? response.data.data : []
    paymentsLoaded.value = true
    page.value = nextPage; totalPages.value = response.data.pagination?.total_page || 1
  } catch (cause) { if (request === listGeneration) listError.value = errorMessage(cause) }
  finally { if (request === listGeneration) listBusy.value = false }
}
/** Read safe review for the explicitly selected record. Unknown POST results remain locked even after reading. @returns Completion. */
async function refreshReview() {
  const selected = payment.value, request = generation
  if (!selected || busy.value || !can('GET', selected.id)) return
  timeoutOpen.value = false
  busy.value = true; error.value = ''; step.value = 'edit'; acknowledged.value = false; review.value = undefined
  try {
    const response = await manualPaymentAPI.review(selected.id)
    if (request !== generation || !can('GET', selected.id)) return
    if (!isManualPaymentReview(response.data.data, selected.id, props.order.id)) throw new Error('Invalid payment review')
    review.value = response.data.data
  } catch (cause) { if (request === generation) error.value = errorMessage(cause) }
  finally { if (request === generation) busy.value = false }
}
/** Select a real record and retain same-record drafts across closing/reopening. @param selected Payment. @returns Completion. */
async function show(selected: AdminPayment) {
  if (busy.value || !rootOrder.value || selected.order_id !== props.order.id || !can('GET', selected.id) || !can('POST', selected.id)) return
  if (payment.value?.id !== selected.id) {
    // Do not let a different record silently replace evidence after an uncertain submission.
    if (locked.value) return
    clearDraft(); payment.value = selected
  }
  open.value = true
  await refreshReview()
}
/** Present an explicit second review; no payment mutation is made here. @returns Nothing. */
function askConfirm() {
  if (!canSubmit.value || !valid.value) return
  step.value = 'confirm'; acknowledged.value = false
}
/** Submit once. Any failure locks this draft against blind replay, preserving evidence for read-only reconciliation. @returns Completion. */
async function submit(timeoutConfirmed = false) {
  const selected = payment.value, current = review.value, request = generation
  if (!selected || !current || !canSubmit.value || !valid.value || step.value !== 'confirm' || !acknowledged.value) return
  if (current.requires_timeout_confirmation && (!timeoutConfirmed || !timeoutOpen.value)) {
    timeoutOpen.value = true
    return
  }
  const confirmTimeout = current.requires_timeout_confirmation && timeoutConfirmed && timeoutOpen.value
  timeoutOpen.value = false
  busy.value = true; error.value = ''
  try {
    const input: ManualPaymentInput = { ...evidence(), request_id: crypto.randomUUID(), expected_status: current.payment_status, confirm_timeout: confirmTimeout }
    const response = await manualPaymentAPI.confirm(selected.id, input)
    if (request !== generation) return
    if (!isManualPaymentReview(response.data.data, selected.id, props.order.id) || response.data.data.payment_status !== 'success') throw new Error('Invalid receipt result')
    review.value = response.data.data; accepted.value = true; step.value = 'edit'; emit('refresh')
  } catch (cause) { if (request === generation) { error.value = errorMessage(cause); locked.value = true; acknowledged.value = false } }
  finally { if (request === generation) busy.value = false }
}
/** Close only idle dialogs; keep evidence in memory for the same account. @param value Open state. @returns Nothing. */
function changeOpen(value: boolean) { if (!busy.value) { open.value = value; if (!value) timeoutOpen.value = false } }
/** Clear sensitive evidence and invalidate stale responses. @returns Nothing. */
function clearDraft() {
  generation++; Object.assign(draft, { amount: '', currency: '', paid_at: '', transaction_id: '', evidence_reference: '', reason: '' })
  payment.value = undefined; review.value = undefined; busy.value = false; error.value = ''; locked.value = false
  timeoutOpen.value = false
  accepted.value = false; acknowledged.value = false; step.value = 'edit'
}
/** Isolate identities and order roots, including in-flight callbacks. @returns Nothing. */
function reset() { clearDraft(); listGeneration++; rows.value = []; paymentsLoaded.value = false; listBusy.value = false; listError.value = ''; open.value = false }
watch(busy, value => emit('busy', value), { flush: 'sync' })
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions), props.order.id], reset, { flush: 'sync' })
// The membership list shortcut loads real payment records, but never selects or confirms one automatically.
onMounted(() => { if (props.autoLoad) void loadPayments() })
onBeforeUnmount(reset)
</script>

<template>
  <section class="space-y-3">
    <h3 class="font-semibold">{{ t('manualPayment.title') }}</h3>
    <p class="text-sm text-muted-foreground">{{ t(rootOrder ? 'manualPayment.warning' : 'manualPayment.childOrder') }}</p>
    <template v-if="rootOrder">
      <Button v-if="!order.payments && auth.hasPermission('GET:/admin/payments')" size="sm" variant="outline" :disabled="listBusy" @click="loadPayments(page)">{{ t('manualPayment.loadPayments') }}</Button>
      <p v-if="!order.payments && !auth.hasPermission('GET:/admin/payments')" class="text-sm text-muted-foreground">{{ t('manualPayment.forbidden') }}</p>
      <p v-if="listBusy" role="status">{{ t('manualPayment.loading') }}</p>
      <p v-if="listError" role="alert" class="text-sm text-destructive">{{ listError }}</p>
      <Table v-if="availableRows.length"><TableHeader><TableRow>
        <TableHead>{{ t('manualPayment.payment') }}</TableHead><TableHead>{{ t('manualPayment.channel') }}</TableHead><TableHead>{{ t('manualPayment.status') }}</TableHead><TableHead>{{ t('manualPayment.createdAt') }}</TableHead><TableHead>{{ t('manualPayment.actions') }}</TableHead>
      </TableRow></TableHeader><TableBody><TableRow v-for="row in availableRows" :key="row.id">
        <TableCell>{{ row.payment_no || row.id }}</TableCell><TableCell>{{ row.channel_name || row.channel_id }} · {{ row.provider_type }}</TableCell><TableCell>{{ paymentStatusLabel(t, row.status) }}</TableCell><TableCell>{{ formatDate(row.created_at) }}</TableCell>
        <TableCell><Button v-if="can('GET', row.id) && can('POST', row.id)" size="sm" variant="outline" :disabled="busy || (locked && payment?.id !== row.id)" @click="show(row)">{{ t('manualPayment.selectPayment') }}</Button></TableCell>
      </TableRow></TableBody></Table>
      <p v-else-if="!listBusy" class="text-sm text-muted-foreground">{{ t(paymentsLoaded || order.payments ? 'manualPayment.emptyPayments' : 'manualPayment.noPayments') }}</p>
      <div v-if="totalPages > 1" class="flex justify-end gap-2"><Button variant="outline" :disabled="page <= 1 || listBusy" @click="loadPayments(page - 1)">{{ t('manualPayment.previous') }}</Button><span>{{ page }} / {{ totalPages }}</span><Button variant="outline" :disabled="page >= totalPages || listBusy" @click="loadPayments(page + 1)">{{ t('manualPayment.next') }}</Button></div>
    </template>
    <Dialog :open="open" @update:open="changeOpen"><DialogScrollContent class="sm:max-w-2xl" @escape-key-down="busy && $event.preventDefault()" @interact-outside="busy && $event.preventDefault()"><DialogHeader><DialogTitle>{{ t('manualPayment.title') }}</DialogTitle><DialogDescription>{{ t('manualPayment.warning') }}</DialogDescription></DialogHeader>
      <p class="text-sm">{{ t('manualPayment.payment') }}: {{ payment?.payment_no || payment?.id }} · {{ order.order_no }}</p>
      <p v-if="busy" role="status">{{ t('manualPayment.loading') }}</p>
      <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
      <p v-if="locked" class="text-sm text-muted-foreground">{{ t('manualPayment.locked') }}</p>
      <p v-if="accepted" role="status" class="text-sm">{{ t('manualPayment.success') }}</p>
      <template v-if="review">
        <dl class="grid grid-cols-2 gap-3 text-sm"><div><dt class="text-muted-foreground">{{ t('manualPayment.expectedAmount') }}</dt><dd>{{ review.amount }} {{ review.currency }}</dd></div><div><dt class="text-muted-foreground">{{ t('manualPayment.status') }}</dt><dd>{{ paymentStatusLabel(t, review.payment_status) }}</dd></div><div><dt class="text-muted-foreground">{{ t('admin.orders.table.status') }}</dt><dd>{{ orderStatusLabel(t, review.order_status) }}</dd></div><div><dt class="text-muted-foreground">{{ t('manualPayment.channel') }}</dt><dd>{{ review.channel_id }} · {{ review.provider_type }}</dd></div></dl>
        <p v-if="!review.can_confirm && !accepted" class="text-sm text-muted-foreground">{{ t('manualPayment.blocked') }}</p>
        <p class="text-sm text-muted-foreground">{{ t('manualPayment.evidenceHint') }}</p>
        <fieldset v-if="step === 'edit'" :disabled="busy || locked || accepted" class="grid grid-cols-2 gap-4">
          <label v-for="field in fields" :key="field" class="space-y-1 text-sm" :class="field === 'reason' || field === 'evidence_reference' ? 'col-span-2' : ''">
            <span>{{ t(`manualPayment.${field}`) }}</span><Input v-model="draft[field]" :name="field" :type="field === 'paid_at' ? 'datetime-local' : 'text'" :step="field === 'paid_at' ? '1' : undefined" :inputmode="field === 'amount' ? 'decimal' : undefined" autocomplete="off" />
          </label>
        </fieldset>
        <div v-else class="space-y-4"><p class="font-medium">{{ t('manualPayment.secondConfirm') }}</p><dl class="grid grid-cols-2 gap-3 text-sm"><div v-for="field in fields" :key="field" class="break-all"><dt class="text-muted-foreground">{{ t(`manualPayment.${field}`) }}</dt><dd>{{ evidence()[field] || '—' }}</dd></div></dl>
          <label class="flex items-start gap-2 text-sm"><Checkbox v-model="acknowledged" :disabled="busy || locked" />{{ t('manualPayment.acknowledge') }}</label>
        </div>
      </template>
      <div class="flex justify-end gap-2">
        <Button variant="outline" :disabled="busy" @click="refreshReview">{{ t('manualPayment.refresh') }}</Button>
        <Button v-if="step === 'confirm'" variant="outline" :disabled="busy" @click="step = 'edit'; acknowledged = false">{{ t('manualPayment.back') }}</Button>
        <Button v-if="step === 'edit'" :disabled="!canSubmit || !valid" @click="askConfirm">{{ t('manualPayment.review') }}</Button>
        <Button v-else :disabled="!canSubmit || !acknowledged" @click="submit()">{{ t('manualPayment.confirm') }}</Button>
      </div>
    </DialogScrollContent></Dialog>
    <Dialog v-model:open="timeoutOpen">
      <DialogScrollContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{{ t('manualPayment.timeoutTitle') }}</DialogTitle>
          <DialogDescription>{{ t('manualPayment.timeoutWarning') }}</DialogDescription>
        </DialogHeader>
        <p class="break-all text-sm">{{ order.order_no }} · {{ evidence().amount }} {{ evidence().currency }}</p>
        <div class="flex justify-end gap-2">
          <Button variant="outline" @click="timeoutOpen = false">{{ t('manualPayment.back') }}</Button>
          <Button :disabled="!canSubmit || !acknowledged" @click="submit(true)">{{ t('manualPayment.timeoutConfirm') }}</Button>
        </div>
      </DialogScrollContent>
    </Dialog>
  </section>
</template>
