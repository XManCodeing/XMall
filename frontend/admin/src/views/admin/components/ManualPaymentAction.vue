<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { telegramMembershipAPI, membershipBase } from '@/api/telegramMembership'
import type { AdminOrder } from '@/api/types'
import { useAdminAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import ManualPaymentConfirmation from './ManualPaymentConfirmation.vue'

const props = defineProps<{ order: AdminOrder; membership?: boolean }>()
const emit = defineEmits<{ (event: 'refresh'): void }>()
const { t } = useI18n(), auth = useAdminAuthStore()
const receiptBusy = ref(false)
const open = ref(false), loading = ref(false), error = ref(''), detail = ref<AdminOrder>()
let generation = 0
const path = computed(() => `${props.membership ? membershipBase : '/admin'}/orders/${props.order.id}`)
const allowed = computed(() => auth.hasPermission(`GET:${path.value}`))
const visible = computed(() => !props.order.parent_id && ['pending_payment', 'canceled'].includes(props.order.status) && allowed.value)
/** Open the same receipt dialog for either list. @returns Completion after read-only detail loading. */
async function show() {
  if (!visible.value || loading.value) return
  const request = ++generation
  open.value = true; loading.value = true; error.value = ''; detail.value = undefined
  try {
    const response = await (props.membership ? telegramMembershipAPI.order(props.order.id) : adminAPI.getOrder(props.order.id))
    if (request !== generation || !allowed.value) return
    const order = response.data.data
    if (!order || order.id !== props.order.id || order.parent_id) throw new Error('Invalid payment root')
    detail.value = order
  } catch {
    if (request === generation) error.value = t('common.api.requestFailed')
  } finally { if (request === generation) loading.value = false }
}
/** Drop stale detail when the dialog/account/order changes. @returns Nothing. */
function reset() { generation++; detail.value = undefined; loading.value = false; error.value = '' }
/** Refuse closing the parent while the shared receipt is submitting. @param value Requested state. @returns Nothing. */
function changeOpen(value: boolean) { if (!receiptBusy.value) open.value = value }
watch(open, value => { if (!value) reset() })
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions), props.order.id], () => { open.value = false; reset() }, { flush: 'sync' })
</script>

<template>
  <Button v-if="visible" size="xs" variant="outline" :title="t('manualPayment.title')" :aria-label="t('manualPayment.title')" @click="show">{{ t('tableAction.receipt') }}</Button>
  <Dialog :open="open" @update:open="changeOpen">
    <DialogScrollContent class="sm:max-w-3xl" @escape-key-down="receiptBusy && $event.preventDefault()" @interact-outside="receiptBusy && $event.preventDefault()">
      <DialogHeader>
        <DialogTitle>{{ t('manualPayment.title') }} · {{ order.order_no }}</DialogTitle>
        <DialogDescription>{{ t('manualPayment.selectPayment') }}</DialogDescription>
      </DialogHeader>
      <p v-if="loading" role="status" class="text-sm text-muted-foreground">{{ t('manualPayment.loading') }}</p>
      <div v-else-if="error" class="space-y-3">
        <p role="alert" class="text-sm text-destructive">{{ error }}</p>
        <Button size="sm" variant="outline" @click="show">{{ t('manualPayment.refresh') }}</Button>
      </div>
      <ManualPaymentConfirmation v-else-if="detail" :order="detail" auto-load @busy="receiptBusy = $event" @refresh="emit('refresh')" />
    </DialogScrollContent>
  </Dialog>
</template>
