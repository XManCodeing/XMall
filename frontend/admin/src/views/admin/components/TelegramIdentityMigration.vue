<script setup lang="ts">
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ existingTaskID?: string }>()
import IdentitySelection from './IdentitySelection.vue'
import { useTelegramIdentity } from '@/composables/useTelegramIdentity'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
const { t, te } = useI18n()
const { draft, open, busy, error, taskID, detail, confirmOpen, acknowledged, uncertain, accepted,
  can, canPreview, valid, canConfirm, canResume, canCancel, canAct, canStartNext, canAbandon, phase, startNext, abandon,
  action, show, preview, refresh, askConfirm, confirm } = useTelegramIdentity(() => props.existingTaskID)
/** Open a saved task through its exact read permission. @returns Completion promise. */
async function inspect() {
  if (!props.existingTaskID || !can('GET', `/tasks/${props.existingTaskID}`)) return
  taskID.value = props.existingTaskID; open.value = true; await refresh()
}
/** Localize only recognized mapping states, never derive them from task approval.
 * @param state Persisted mapping state. @returns Safe localized label.
 */
function mappingLabel(state: string) {
  return t(`telegramRecovery.identity.${['awaiting_member', 'cleanup_pending', 'completed'].includes(state) ? state : 'unknown'}`)
}
/** Localize parent task states separately. @param state Task state. @returns Localized label. */
function taskLabel(state: string) { return te(`telegramRecovery.values.${state}`) ? t(`telegramRecovery.values.${state}`) : t('telegramRecovery.identity.unknown') }
</script>

<template>
  <Button v-if="props.existingTaskID && can('GET', `/tasks/${props.existingTaskID}`)" variant="outline" @click="inspect">{{ t('telegramRecovery.identity.create') }}</Button>
  <Button v-else-if="!props.existingTaskID && canPreview" variant="outline" @click="show">{{ t('telegramRecovery.identity.create') }}</Button>
  <Dialog :open="open" @update:open="value => { if (!busy) open = value }"><DialogScrollContent class="sm:max-w-4xl">
    <DialogHeader><DialogTitle>{{ t('telegramRecovery.identity.create') }}</DialogTitle><DialogDescription>{{ t('telegramRecovery.identity.hint') }}</DialogDescription></DialogHeader>
    <p class="text-sm text-muted-foreground">{{ t('telegramRecovery.identity.bindingRequired') }}</p>
    <IdentitySelection v-if="!taskID && !props.existingTaskID" :draft="draft" :locked="busy || !!taskID || uncertain" />
    <fieldset :disabled="busy || !!taskID || uncertain" class="grid grid-cols-2 gap-4">
      <div class="space-y-2"><Label for="identity-account">{{ t('telegramRecovery.identity.account') }}</Label><Input id="identity-account" v-model="draft.account" type="text" inputmode="numeric" maxlength="20" autocomplete="off" /></div>
      <div class="space-y-2"><Label for="identity-targets">{{ t('telegramRecovery.identity.targets') }}</Label><Input id="identity-targets" v-model="draft.targets" type="text" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('telegramRecovery.identity.targetsHint') }}</p></div>
      <div class="space-y-2"><Label for="identity-old">{{ t('telegramRecovery.identity.oldID') }}</Label><Input id="identity-old" v-model="draft.oldID" type="text" inputmode="numeric" maxlength="19" autocomplete="off" /></div>
      <div class="space-y-2"><Label for="identity-new">{{ t('telegramRecovery.identity.newID') }}</Label><Input id="identity-new" v-model="draft.newID" type="text" inputmode="numeric" maxlength="19" autocomplete="off" /></div>
      <div class="col-span-2 space-y-2"><Label for="identity-note">{{ t('telegramRecovery.identity.note') }}</Label><Textarea id="identity-note" v-model="draft.note" maxlength="1024" /><p class="text-xs text-muted-foreground">{{ t('telegramRecovery.identity.noteHint') }}</p></div>
      <div class="col-span-2 space-y-2"><Label for="identity-reason">{{ t('telegramRecovery.reason') }}</Label><Input id="identity-reason" v-model="draft.reason" maxlength="512" /><p class="text-xs text-muted-foreground">{{ t('telegramRecovery.reasonHint') }}</p></div>
      <div class="col-span-2 flex items-center gap-2"><Checkbox id="identity-verified" :model-value="draft.verified" :disabled="busy || !!taskID || uncertain" @update:model-value="draft.verified = $event === true" /><Label for="identity-verified">{{ t('telegramRecovery.identity.verified') }}</Label></div>
    </fieldset>
    <p v-if="busy" role="status">{{ t('telegramRecovery.loading') }}</p><p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <p v-if="uncertain" class="text-sm text-muted-foreground">{{ t('telegramRecovery.identity.uncertain') }}</p>
    <p v-if="uncertain && !taskID" class="text-sm text-muted-foreground">{{ t('telegramRecovery.identity.abandonHint') }}</p>
    <p v-if="accepted" role="status" class="text-sm">{{ t('telegramRecovery.identity.accepted') }}</p>
    <template v-if="taskID"><div class="flex items-center justify-between"><p class="font-mono">{{ t('telegramRecovery.id') }}: {{ taskID }}</p><Button v-if="can('GET', `/tasks/${taskID}`)" variant="outline" size="sm" :disabled="busy" @click="refresh">{{ t('telegramRecovery.refreshProgress') }}</Button></div><p v-if="!can('GET', `/tasks/${taskID}`)" class="text-sm">{{ t('telegramRecovery.forbidden') }}</p></template>
    <template v-if="detail">
      <p v-if="phase" role="status" class="text-sm text-muted-foreground">{{ t(`telegramRecovery.identity.${phase}`) }}</p>
      <div class="flex items-center gap-2"><span>{{ t('telegramRecovery.identity.taskState') }}</span><Badge :variant="membershipStatusVariant(detail.task.state)">{{ taskLabel(detail.task.state) }}</Badge></div>
      <dl class="grid grid-cols-2 gap-3 text-sm"><div><dt class="text-muted-foreground">{{ t('telegramRecovery.identity.account') }}</dt><dd>{{ detail.snapshot.old.account_id }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.binding') }}</dt><dd>{{ detail.snapshot.expected_old.binding }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.identity.oldID') }}</dt><dd class="font-mono">{{ detail.snapshot.old.telegram_id }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.identity.newID') }}</dt><dd class="font-mono">{{ detail.snapshot.new.telegram_id }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.identity.note') }}</dt><dd>{{ detail.task.verification_note }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.reason') }}</dt><dd>{{ detail.task.reason }}</dd></div></dl>
      <div class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead class="px-4 py-3">{{ t('telegramRecovery.target_id') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramRecovery.bot_id') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramRecovery.chat_id') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramRecovery.entitlement') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramRecovery.route') }}</TableHead></TableRow></TableHeader>
          <TableBody class="divide-y divide-border"><TableRow v-for="target in detail.snapshot.identity_targets" :key="target.target_id" class="hover:bg-muted/30"><TableCell class="px-4 py-3">{{ target.target_id }}</TableCell><TableCell class="px-4 py-3">{{ target.bot_id }}</TableCell><TableCell class="px-4 py-3">{{ target.chat_id }}</TableCell><TableCell class="px-4 py-3">{{ target.entitlement_version }}</TableCell><TableCell class="px-4 py-3">{{ target.route_version }}</TableCell></TableRow></TableBody>
        </Table>
      </div>
      <h3 class="font-medium">{{ t('telegramRecovery.identity.mappingState') }}</h3><p class="text-sm text-muted-foreground">{{ t('telegramRecovery.identity.progressHint') }}</p>
      <div class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead class="px-4 py-3">{{ t('telegramRecovery.target_id') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramRecovery.identity.mappingState') }}</TableHead></TableRow></TableHeader>
          <TableBody class="divide-y divide-border"><TableRow v-for="mapping in detail.identity_mappings || []" :key="mapping.id" class="hover:bg-muted/30"><TableCell class="px-4 py-3">{{ mapping.target_id }}</TableCell><TableCell class="px-4 py-3"><Badge :variant="membershipStatusVariant(mapping.state)">{{ mappingLabel(mapping.state) }}</Badge></TableCell></TableRow><TableRow v-if="!detail.identity_mappings?.length"><TableCell :colspan="2" class="px-4 py-8 text-center text-muted-foreground">{{ t('telegramRecovery.identity.noMappings') }}</TableCell></TableRow></TableBody>
        </Table>
      </div>
      <p v-if="detail.execution_available !== true" class="text-sm text-muted-foreground">{{ t('telegramRecovery.executionUnavailable') }}</p>
    </template>
    <DialogFooter><Button variant="outline" :disabled="busy" @click="open = false">{{ t('telegramRecovery.close') }}</Button><Button v-if="!taskID && !props.existingTaskID" :disabled="busy || !valid || !canPreview || uncertain" @click="preview">{{ t('telegramRecovery.identity.preview') }}</Button><Button v-if="canConfirm" :disabled="busy" @click="askConfirm('confirm')">{{ t('telegramRecovery.identity.confirm') }}</Button><Button v-if="canResume" :disabled="busy" @click="askConfirm('resume')">{{ t('telegramRecovery.resume') }}</Button><Button v-if="canCancel" variant="outline" :disabled="busy" @click="askConfirm('cancel')">{{ t('telegramRecovery.cancel') }}</Button><Button v-if="canStartNext" @click="startNext">{{ t('telegramRecovery.identity.startNext') }}</Button><Button v-if="canAbandon" variant="outline" @click="abandon">{{ t('telegramRecovery.identity.abandon') }}</Button></DialogFooter>
  </DialogScrollContent></Dialog>
  <Dialog :open="confirmOpen" @update:open="value => { if (!busy) confirmOpen = value }"><DialogScrollContent><DialogHeader><DialogTitle>{{ t(action === 'cancel' ? 'telegramRecovery.cancelTitle' : action === 'resume' ? 'telegramRecovery.resumeTitle' : 'telegramRecovery.identity.confirm') }}</DialogTitle><DialogDescription>{{ t(action === 'cancel' ? 'telegramRecovery.identity.cancelHint' : 'telegramRecovery.identity.confirmHint') }}</DialogDescription></DialogHeader>
    <p class="font-mono">{{ detail?.snapshot.old.telegram_id }} → {{ detail?.snapshot.new.telegram_id }}</p>
    <p>{{ t('telegramRecovery.identity.targets') }}: {{ detail?.snapshot.identity_targets?.map(target => target.target_id).join(', ') }}</p>
    <div class="flex items-center gap-2"><Checkbox id="identity-ack" :model-value="acknowledged" :disabled="busy || uncertain" @update:model-value="acknowledged = $event === true" /><Label for="identity-ack">{{ t(action === 'cancel' ? 'telegramRecovery.identity.cancelAcknowledge' : 'telegramRecovery.identity.acknowledge') }}</Label></div>
    <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <DialogFooter><Button variant="outline" :disabled="busy" @click="confirmOpen = false">{{ t('telegramRecovery.close') }}</Button><Button :disabled="busy || !acknowledged || !canAct" @click="confirm">{{ t(action === 'cancel' ? 'telegramRecovery.cancel' : action === 'confirm' ? 'telegramRecovery.identity.confirm' : 'telegramRecovery.resume') }}</Button></DialogFooter>
  </DialogScrollContent></Dialog>
</template>
