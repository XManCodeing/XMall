<script setup lang="ts">
import { toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { supportRecordedReceipt, type RecoverySupportDecision } from '@/api/telegramRecoverySupport'
import type { RecoveryTask } from '@/api/telegramRecovery'
import { useRecoverySupportReview } from '@/composables/useRecoverySupportReview'
import { recoverySupportMessages } from '@/i18n/recoverySupport'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const props = defineProps<{ task: RecoveryTask }>()
const { t, te } = useI18n({ useScope: 'local', messages: recoverySupportMessages })
const { t: actionText } = useI18n({ useScope: 'global' })
const { canEntries, canOperations, canResolve, entries, operations, entry, operation, entryPage, operationPage, entryNext, operationNext,
  entryLoading, operationLoading, saving, entryError, operationError, error, notice, blocked, reviewOpen, form, decisions, recordedReceipt, valid,
  loadEntries, selectEntry, loadOperations, review, resolve } = useRecoverySupportReview(toRef(props, 'task'))
/** Localize disclosed states with a fail-closed fallback. @param state Server state. @returns Localized label. */
function stateLabel(state: string) { return t(te(`states.${state}`) ? `states.${state}` : 'states.other') }
/** Separate receipt-only review from unknown reconciliation and retry. @param decision Wire intent. @returns Localized action label. */
function decisionLabel(decision: RecoverySupportDecision) {
  return t(recordedReceipt.value && decision !== 'retry' ? decision === 'succeeded' ? 'receiptSucceeded' : 'receiptFailed' : decision)
}
</script>

<template>
  <section class="space-y-3 border-t border-border pt-4">
    <div class="flex items-center justify-between gap-3"><h3 class="font-medium">{{ t('title') }}</h3><Button v-if="canEntries" size="sm" variant="outline" :disabled="saving || entryLoading" @click="loadEntries()">{{ t(entries.length || entry ? 'refresh' : 'entries') }}</Button></div>
    <p class="text-sm text-muted-foreground">{{ t('hint') }}</p>
    <p class="text-sm text-muted-foreground">{{ t('draftInvalidated') }}</p>
    <p v-if="!canEntries" class="text-sm text-muted-foreground">{{ t('forbidden') }}</p>
    <template v-else>
      <p v-if="entryLoading" role="status">{{ t('loading') }}</p><p v-if="entryError" role="alert" class="text-sm text-destructive">{{ t(entryError) }}</p>
      <div class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table>
          <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
            <TableRow>
              <TableHead class="px-4 py-3">{{ t('entry') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('target') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('revision') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('state') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('legacy') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('currentOperation') }}</TableHead>
              <TableHead class="px-4 py-3 text-right">{{ t('actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody class="divide-y divide-border">
            <TableRow v-for="row in entries" :key="row.id" class="hover:bg-muted/30">
              <TableCell class="px-4 py-3 font-mono">{{ row.id }}</TableCell>
              <TableCell class="px-4 py-3">{{ row.target_id }}</TableCell>
              <TableCell class="px-4 py-3">{{ row.revision }}</TableCell>
              <TableCell class="px-4 py-3"><Badge variant="secondary">{{ stateLabel(row.state) }}</Badge></TableCell>
              <TableCell class="px-4 py-3">{{ row.legacy }}</TableCell>
              <TableCell class="px-4 py-3">{{ row.operation_id }}</TableCell>
              <TableCell class="px-4 py-3 text-right">
                <div class="flex flex-wrap items-center justify-end gap-2">
                  <Button v-if="canOperations" size="sm" variant="outline" :disabled="saving || entryLoading" @click="selectEntry(row)" :title="t('operations')" :aria-label="t('operations')">{{ actionText('tableAction.detail') }}</Button>
                  <span v-else class="text-sm text-muted-foreground">{{ t('forbidden') }}</span>
                </div>
              </TableCell>
            </TableRow>
            <TableRow v-if="!entries.length && !entryLoading"><TableCell :colspan="7" class="px-4 py-3 text-muted-foreground">{{ t('empty') }}</TableCell></TableRow>
          </TableBody>
        </Table>
      </div>
      <div class="flex justify-end items-center gap-3"><Button size="sm" variant="outline" :disabled="saving || entryLoading || entryPage === 0" @click="loadEntries(entryPage - 1)">{{ t('previous') }}</Button><span class="text-sm text-muted-foreground">{{ t('page', { page: entryPage + 1 }) }}</span><Button size="sm" variant="outline" :disabled="saving || entryLoading || entryNext === '0'" @click="loadEntries(entryPage + 1)">{{ t('next') }}</Button></div>
    </template>
    <section v-if="entry && canOperations" class="space-y-3">
      <div class="flex items-center justify-between gap-3"><h4 class="font-medium">{{ t('entry') }} · {{ entry.id }}</h4><Button size="sm" variant="outline" :disabled="saving || operationLoading" @click="loadOperations()">{{ t('refresh') }}</Button></div>
      <p v-if="!canResolve" class="text-sm text-muted-foreground">{{ t('readOnly') }}</p>
      <p class="text-sm text-muted-foreground">{{ t('rules') }}</p>
      <p v-if="operationLoading" role="status">{{ t('loading') }}</p><p v-if="operationError" role="alert" class="text-sm text-destructive">{{ t(operationError) }}</p>
      <div class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table>
          <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
            <TableRow>
              <TableHead class="px-4 py-3">{{ t('operation') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('version') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('state') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('source') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('target') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('routeVersion') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('chat') }}</TableHead>
              <TableHead class="px-4 py-3">{{ t('action') }}</TableHead>
              <TableHead class="px-4 py-3 text-right">{{ t('actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody class="divide-y divide-border">
            <TableRow v-for="row in operations" :key="row.id" class="hover:bg-muted/30">
              <TableCell class="px-4 py-3 font-mono">{{ row.id }}</TableCell>
              <TableCell class="px-4 py-3">{{ row.version }}</TableCell>
              <TableCell class="px-4 py-3"><Badge variant="secondary">{{ stateLabel(row.state) }}</Badge></TableCell>
              <TableCell class="px-4 py-3">{{ t(row.retired ? 'retired' : 'current') }}</TableCell>
              <TableCell class="px-4 py-3">{{ row.target_id }}</TableCell>
              <TableCell class="px-4 py-3">{{ row.route_version }}</TableCell>
              <TableCell class="px-4 py-3 font-mono">{{ row.chat_id }}</TableCell>
              <TableCell class="px-4 py-3">{{ t(row.action === 'create' ? 'create' : 'otherAction') }}<span v-if="row.action && row.action !== 'create'" class="text-xs"> · {{ row.action }}</span></TableCell>
              <TableCell class="px-4 py-3 text-right">
                <div class="flex flex-wrap items-center justify-end gap-2">
                  <Button v-if="canResolve" size="sm" variant="outline" :disabled="saving || operationLoading" @click="review(row)" :title="t(supportRecordedReceipt(entry, row) ? 'receiptTitle' : 'review')" :aria-label="t(supportRecordedReceipt(entry, row) ? 'receiptTitle' : 'review')">{{ actionText('tableAction.reconcile') }}</Button>
                </div>
              </TableCell>
            </TableRow>
            <TableRow v-if="!operations.length && !operationLoading"><TableCell :colspan="9" class="px-4 py-3 text-muted-foreground">{{ t('empty') }}</TableCell></TableRow>
          </TableBody>
        </Table>
      </div>
      <div class="flex justify-end items-center gap-3"><Button size="sm" variant="outline" :disabled="saving || operationLoading || operationPage === 0" @click="loadOperations(operationPage - 1)">{{ t('previous') }}</Button><span class="text-sm text-muted-foreground">{{ t('page', { page: operationPage + 1 }) }}</span><Button size="sm" variant="outline" :disabled="saving || operationLoading || operationNext === '0'" @click="loadOperations(operationPage + 1)">{{ t('next') }}</Button></div>
    </section>
    <Dialog :open="reviewOpen" @update:open="value => { if (!saving) reviewOpen = value }"><DialogScrollContent class="sm:max-w-xl"><DialogHeader><DialogTitle>{{ t(recordedReceipt ? 'receiptTitle' : 'review') }} · {{ operation?.id }}</DialogTitle><DialogDescription>{{ t(recordedReceipt ? 'receiptHint' : 'rules') }}</DialogDescription></DialogHeader>
      <template v-if="operation && entry">
        <p class="text-sm">{{ t('entry') }}: {{ entry.id }} · {{ t('version') }}: {{ operation.version }} · {{ t(operation.retired ? 'retired' : 'current') }}</p>
        <p class="text-sm font-mono">{{ t('target') }}: {{ operation.target_id }} · {{ t('routeVersion') }}: {{ operation.route_version }} · {{ t('chat') }}: {{ operation.chat_id }}</p>
        <p v-if="recordedReceipt && operation.state === 'failed'" class="text-sm text-muted-foreground">{{ t('receiptRetryHint') }}</p>
        <p v-if="!decisions.length" class="text-sm text-muted-foreground">{{ t('unavailable') }}</p>
        <fieldset class="space-y-4" :disabled="saving || blocked || !canResolve">
          <div class="space-y-2"><Label for="support-review-decision">{{ t('decision') }}</Label><Select v-model="form.decision" :disabled="saving || blocked || !canResolve"><SelectTrigger id="support-review-decision"><SelectValue :placeholder="t('decision')" /></SelectTrigger><SelectContent><SelectItem v-for="decision in decisions" :key="decision" :value="decision">{{ decisionLabel(decision) }}</SelectItem></SelectContent></Select></div>
          <div class="space-y-2"><Label for="support-review-evidence">{{ t('evidence') }}</Label><Input id="support-review-evidence" v-model="form.evidence" maxlength="103" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('evidenceHint') }}</p></div>
          <div v-if="form.decision === 'succeeded'" class="space-y-2"><Label for="support-review-thread">{{ t('thread') }}</Label><Input id="support-review-thread" v-model="form.threadID" type="text" inputmode="numeric" maxlength="19" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t(recordedReceipt ? 'receiptThreadHint' : 'threadHint') }}</p></div>
          <div class="flex items-center gap-2"><Checkbox id="support-review-ack" :model-value="form.acknowledged" :disabled="saving || blocked || !canResolve" @update:model-value="form.acknowledged = $event === true" /><Label for="support-review-ack">{{ t(recordedReceipt && form.decision !== 'retry' ? 'receiptAcknowledge' : 'acknowledge') }}</Label></div>
        </fieldset>
        <p v-if="error" role="alert" class="text-sm text-destructive">{{ t(error) }}</p><p v-if="notice" role="status" class="text-sm text-muted-foreground">{{ t(notice) }}</p>
        <DialogFooter><Button variant="outline" :disabled="saving" @click="reviewOpen = false">{{ t('close') }}</Button><Button :disabled="saving || !valid" @click="resolve">{{ t(saving ? 'loading' : recordedReceipt && form.decision !== 'retry' ? 'receiptSave' : 'save') }}</Button></DialogFooter>
      </template>
    </DialogScrollContent></Dialog>
  </section>
</template>
