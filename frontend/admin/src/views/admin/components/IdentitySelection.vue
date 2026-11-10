<script setup lang="ts">
import { toRef, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useIdentitySelection } from '@/composables/useIdentitySelection'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
const props = defineProps<{ draft: { account: string; oldID: string; newID: string; targets: string; verified: boolean }; locked: boolean }>()
const { t } = useI18n()
const { keyword, users, orders, targets, userPage, orderPage, userNext, orderNext, busy, error, orderID,
  canUsers, canOrders, canOrder, load, resetUsers, selectOrder } = useIdentitySelection(toRef(props.draft, 'account'), toRef(props.draft, 'oldID'), toRef(props, 'locked'))
const selected = computed(() => props.draft.targets.split(/[\s,，]+/).filter(Boolean))
/** Select a website account without carrying identity approval across users.
 * @param value Selected existing user ID. @returns Nothing.
 */
function selectUser(value: unknown) {
  if (props.locked || busy.value || typeof value !== 'string' || !users.value.some(user => user.id === value)) return
  if (props.draft.account !== value) Object.assign(props.draft, { account: value, oldID: '', newID: '', targets: '', verified: false })
}
/** Add or remove a displayed candidate; selection never implies entitlement validity.
 * @param id Candidate target ID. @param checked Desired checkbox state. @returns Nothing.
 */
function toggle(id: string, checked: boolean) {
  if (props.locked || busy.value || !targets.value.includes(id)) return
  const ids = new Set(selected.value)
  if (checked && ids.size < 100) ids.add(id)
  else if (!checked) ids.delete(id)
  props.draft.targets = [...ids].join(', '); props.draft.verified = false
}
</script>
<template>
  <section class="space-y-3 border-b border-border pb-4">
    <p class="text-sm text-muted-foreground">{{ t('telegramRecovery.identity.selectionHint') }}</p>
    <div class="grid grid-cols-2 gap-4">
      <div class="space-y-2">
        <Label for="identity-user-search">{{ t('telegramRecovery.identity.selectUser') }}</Label>
        <div class="flex gap-2"><Input id="identity-user-search" v-model="keyword" :disabled="locked || busy || !canUsers" :placeholder="t('telegramRecovery.identity.userSearch')" @keydown.enter="load('users', 1)" /><Button variant="outline" :disabled="locked || busy || !canUsers" @click="load('users', 1)">{{ t('admin.common.search') }}</Button><Button variant="outline" :disabled="locked || busy || !canUsers" @click="resetUsers">{{ t('admin.common.reset') }}</Button></div>
        <Select :model-value="draft.account" :disabled="locked || busy || !canUsers" @update:model-value="selectUser"><SelectTrigger><SelectValue :placeholder="t('telegramRecovery.identity.selectUser')" /></SelectTrigger><SelectContent><SelectItem v-for="user in users" :key="user.id" :value="user.id">{{ user.label }} · {{ user.id }}</SelectItem></SelectContent></Select>
        <p v-if="!canUsers" class="text-xs text-muted-foreground">{{ t('telegramRecovery.forbidden') }}</p>
        <p v-else-if="!busy && !users.length" class="text-xs text-muted-foreground">{{ t('admin.users.empty') }}</p>
        <div class="flex items-center gap-2"><Button size="sm" variant="ghost" :disabled="locked || busy || !canUsers || userPage <= 1" @click="load('users', userPage - 1)">{{ t('telegramRecovery.previous') }}</Button><span class="text-xs">{{ t('telegramRecovery.page', { page: userPage }) }}</span><Button size="sm" variant="ghost" :disabled="locked || busy || !canUsers || !userNext" @click="load('users', userPage + 1)">{{ t('telegramRecovery.next') }}</Button></div>
      </div>
      <div class="space-y-2">
        <Label for="identity-order">{{ t('telegramRecovery.identity.selectOrder') }}</Label>
        <Button variant="outline" :disabled="locked || busy || !canOrders || !draft.account" @click="load('orders', 1)">{{ t('admin.common.search') }}</Button>
        <Select :model-value="orderID" :disabled="locked || busy || !canOrders || !draft.oldID" @update:model-value="value => typeof value === 'string' && selectOrder(value)"><SelectTrigger id="identity-order"><SelectValue :placeholder="t('telegramRecovery.identity.selectOrder')" /></SelectTrigger><SelectContent><SelectItem v-for="order in orders" :key="order.id" :value="order.id" :disabled="!canOrder(order.id)">{{ order.label }} · {{ order.id }}</SelectItem></SelectContent></Select>
        <p v-if="!canOrders" class="text-xs text-muted-foreground">{{ t('telegramRecovery.forbidden') }}</p>
        <p v-else-if="!busy && !orders.length" class="text-xs text-muted-foreground">{{ t('admin.orders.empty') }}</p>
        <div class="flex items-center gap-2"><Button size="sm" variant="ghost" :disabled="locked || busy || !canOrders || orderPage <= 1" @click="load('orders', orderPage - 1)">{{ t('telegramRecovery.previous') }}</Button><span class="text-xs">{{ t('telegramRecovery.page', { page: orderPage }) }}</span><Button size="sm" variant="ghost" :disabled="locked || busy || !canOrders || !orderNext" @click="load('orders', orderPage + 1)">{{ t('telegramRecovery.next') }}</Button></div>
      </div>
    </div>
    <p v-if="busy" role="status" class="text-sm">{{ t('telegramRecovery.loading') }}</p><p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <div v-if="targets.length" class="flex flex-wrap gap-4"><div v-for="id in targets" :key="id" class="flex items-center gap-2"><Checkbox :id="`identity-target-${id}`" :model-value="selected.includes(id)" :disabled="locked || busy || (!selected.includes(id) && selected.length >= 100)" @update:model-value="toggle(id, $event === true)" /><Label :for="`identity-target-${id}`">{{ t('telegramRecovery.target_id') }}: {{ id }}</Label></div></div>
    <p v-else-if="orderID && !busy && !error" class="text-sm text-muted-foreground">{{ t('telegramRecovery.identity.noCandidates') }}</p>
  </section>
</template>
