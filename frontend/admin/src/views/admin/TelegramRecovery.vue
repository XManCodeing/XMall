<script setup lang="ts">
import ListPagination from '@/components/ListPagination.vue'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import TelegramIdentityMigration from './components/TelegramIdentityMigration.vue'
import RecoverySupportReview from '@/components/telegram/RecoverySupportReview.vue'
import RecoveryBotRoutesEditor from '@/components/telegram/RecoveryBotRoutesEditor.vue'
import RecoveryBotRouteSnapshot from '@/components/telegram/RecoveryBotRouteSnapshot.vue'
import { useTelegramRecovery } from '@/composables/useTelegramRecovery'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDate } from '@/utils/format'

const { t, te } = useI18n()
const { botRoutes, can, canList, canCreate, canGroups, canBotCreate, canBots, draftKind, sourceBotID, destinationBotID, bots, botsLoading, botsError, refreshBots, items, loading, error, success, page, pageSize, total, totalPage, load, changePageSize,
  detail, detailOpen, detailLoading, detailError, show, steps, stepsLoading, stepsError, stepsPage, stepsPageSize, stepsTotal, stepsTotalPage, loadSteps, changeStepsPageSize,
  editorOpen, groupPurpose, changeGroupPurpose, saving, formError, conflict, groups, groupsLoading, groupsError, groupsPage, groupsTotal, loadGroups,
  targetID, chatID, reason, selected, valid, openEditor, submit, canCancel, askCancel, cancelPending, cancelling, cancelError, cancel,
  executionPending, executing, executionError, executionAcknowledged, executionSuccess, refreshingProgress, canExecute, askExecute, execute, refreshProgress } = useTelegramRecovery()
const endpointFields = ['target_id', 'bot_id', 'chat_id', 'account_id', 'telegram_id'] as const
const versionFields = ['config', 'route', 'generation', 'binding', 'entitlement'] as const
const scopeFields = ['target_ids', 'chat_ids', 'bot_ids', 'account_ids', 'telegram_ids'] as const
const taskFields = ['id', 'version', 'actor_id', 'confirmed_actor_id', 'request_key', 'preflight_ref', 'reason', 'verification_note'] as const
/** Localize known states without treating unknown states as success. @param value Server enum. @returns Localized label. */
function label(value: string) { return te(`telegramRecovery.values.${value}`) ? t(`telegramRecovery.values.${value}`) : t('telegramRecovery.values.unknown') }
/** Format optional task dates. @param value ISO timestamp. @returns Display date or empty marker. */
function date(value: string) { return value ? formatDate(value) : '—' }
onMounted(() => { void load() })
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between gap-3">
      <h2 data-guide="recovery-hint" class="text-2xl font-bold tracking-tight">{{ t('telegramRecovery.title') }}</h2>
      <div data-guide="recovery-actions" class="flex gap-2"><Button variant="outline" :disabled="loading || !canList" @click="load(1)">{{ t('telegramRecovery.refresh') }}</Button><TelegramIdentityMigration /><Button v-if="canBotCreate" variant="outline" :disabled="saving" @click="openEditor('bot')">{{ t('telegramRecovery.createBot') }}</Button><Button v-if="canCreate" :disabled="saving" @click="openEditor('group')">{{ t('telegramRecovery.create') }}</Button></div>
    </div>
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramRecovery.forbidden') }}</p>
    <p v-if="loading" role="status">{{ t('telegramRecovery.loading') }}</p><p v-if="success" role="status" class="text-sm">{{ success }}</p>
    <div v-if="canList" data-guide="recovery-list" class="rounded-xl border border-border bg-card">
      <Table>
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
          <TableHead class="px-6 py-3">{{ t('telegramRecovery.id') }}</TableHead>
          <TableHead class="px-6 py-3">{{ t('telegramRecovery.kind') }}</TableHead>
          <TableHead class="px-6 py-3">{{ t('telegramRecovery.state') }}</TableHead>
          <TableHead class="px-6 py-3">{{ t('telegramRecovery.actor_id') }}</TableHead>
          <TableHead class="px-6 py-3">{{ t('telegramRecovery.updated_at') }}</TableHead>
          <TableHead class="px-6 py-3 text-right">{{ t('telegramRecovery.actions') }}</TableHead>
        </TableRow></TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-for="task in items" :key="task.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4 font-mono">{{ task.id }}</TableCell>
            <TableCell class="px-6 py-4">{{ label(task.kind) }}</TableCell>
            <TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(task.state)">{{ label(task.state) }}</Badge></TableCell>
            <TableCell class="px-6 py-4">{{ task.actor_id }}</TableCell>
            <TableCell class="px-6 py-4">{{ date(task.updated_at) }}</TableCell>
            <TableCell class="px-6 py-4 text-right">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <Button data-guide="recovery-detail" v-if="can('GET', `/tasks/${task.id}`)" size="sm" variant="outline" @click="show(task.id)" :title="t('telegramRecovery.detail')" :aria-label="t('telegramRecovery.detail')">{{ t('tableAction.detail') }}</Button>
                <Button v-if="canCancel(task)" size="sm" variant="outline" :disabled="cancelling" @click="askCancel(task)" :title="t('telegramRecovery.cancel')" :aria-label="t('telegramRecovery.cancel')">{{ t('admin.common.cancel') }}</Button>
              </div>
            </TableCell>
          </TableRow>
          <TableRow v-if="!items.length && !loading && !error"><TableCell :colspan="6" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramRecovery.empty') }}</TableCell></TableRow>
        </TableBody>
      </Table>
      <ListPagination always-show :page="page" :page-size="pageSize" :page-size-options="[10, 20, 50, 100]" :total="total" :total-page="totalPage" :loading="loading" @change-page="load" @change-page-size="changePageSize" />
    </div>

    <Dialog :open="editorOpen" @update:open="value => { if (!saving) editorOpen = value }"><DialogScrollContent class="sm:max-w-2xl"><DialogHeader><DialogTitle>{{ t(draftKind === 'bot' ? 'telegramRecovery.createBot' : 'telegramRecovery.create') }}</DialogTitle><DialogDescription>{{ t('telegramRecovery.hint') }}</DialogDescription></DialogHeader>
      <p v-if="draftKind === 'group' && !canGroups" class="text-sm text-muted-foreground">{{ t('telegramRecovery.sourcePermission') }}</p>
      <p v-if="draftKind === 'group' && groupsLoading" role="status">{{ t('telegramRecovery.loading') }}</p><p v-if="draftKind === 'group' && groupsError" role="alert" class="text-destructive">{{ groupsError }}</p><p v-if="formError" role="alert" class="text-destructive">{{ formError }}</p>
      <fieldset :disabled="saving" class="space-y-4">
        <template v-if="draftKind === 'bot'">
          <p class="text-sm text-muted-foreground">{{ t('telegramRecovery.botPreviewHint') }}</p>
          <p v-if="!canBots" class="text-sm text-muted-foreground">{{ t('telegramRecovery.botPermission') }}</p>
          <p v-if="botsLoading" role="status">{{ t('telegramRecovery.loading') }}</p><p v-if="botsError" role="alert" class="text-destructive">{{ botsError }}</p>
          <div class="space-y-2"><Label for="recovery-source-bot">{{ t('telegramRecovery.sourceBot') }}</Label><Select v-model="sourceBotID" :disabled="saving || botsLoading || !canBots || conflict"><SelectTrigger id="recovery-source-bot"><SelectValue :placeholder="t('telegramRecovery.sourceBot')" /></SelectTrigger><SelectContent><SelectItem v-for="bot in bots" :key="bot.id" :value="bot.id">{{ bot.id }} · {{ bot.username }}</SelectItem></SelectContent></Select></div>
          <div class="space-y-2"><Label for="recovery-destination-bot">{{ t('telegramRecovery.destinationBot') }}</Label><Select v-model="destinationBotID" :disabled="saving || botsLoading || !canBots || conflict"><SelectTrigger id="recovery-destination-bot"><SelectValue :placeholder="t('telegramRecovery.destinationBot')" /></SelectTrigger><SelectContent><SelectItem v-for="bot in bots" :key="bot.id" :value="bot.id" :disabled="bot.id === sourceBotID">{{ bot.id }} · {{ bot.username }}</SelectItem></SelectContent></Select></div>
          <p v-if="canBots && !botsLoading && !bots.length" class="text-sm text-muted-foreground">{{ t('telegramRecovery.botsEmpty') }}</p>
          <Button type="button" size="sm" variant="outline" :disabled="saving || botsLoading || !canBots" @click="refreshBots">{{ t('telegramRecovery.refreshBots') }}</Button>
          <RecoveryBotRoutesEditor :model="botRoutes" :source="sourceBotID" :allowed="canGroups" :disabled="saving || conflict || !canBotCreate" />
        </template>
        <template v-else>
        <div class="space-y-2"><Label for="recovery-purpose">{{ t('telegramRecovery.purpose') }}</Label><Select :model-value="groupPurpose" :disabled="saving || !canCreate || !canGroups" @update:model-value="value => changeGroupPurpose(String(value))"><SelectTrigger id="recovery-purpose"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="membership">{{ t('telegramRecovery.membershipPurpose') }}</SelectItem><SelectItem value="support">{{ t('telegramRecovery.supportPurpose') }}</SelectItem></SelectContent></Select></div>
        <div class="space-y-2"><Label for="recovery-source">{{ t('telegramRecovery.source') }}</Label><Select v-model="targetID" :disabled="saving || groupsLoading || !canGroups || conflict"><SelectTrigger id="recovery-source"><SelectValue :placeholder="t('telegramRecovery.source')" /></SelectTrigger><SelectContent><SelectItem v-for="group in groups" :key="group.id" :value="String(group.id)">{{ group.name }} · {{ group.telegram_chat_id }}</SelectItem></SelectContent></Select>
          <p v-if="canGroups && !groupsLoading && !groups.length" class="text-sm text-muted-foreground">{{ t('telegramRecovery.sourceEmpty') }}</p>
          <div class="flex items-center gap-2"><Button type="button" size="sm" variant="outline" :disabled="saving || groupsLoading || !canGroups" @click="loadGroups(groupsPage)">{{ t('telegramRecovery.refreshSource') }}</Button><Button type="button" size="sm" variant="ghost" :disabled="saving || groupsLoading || groupsPage <= 1" @click="loadGroups(groupsPage - 1)">{{ t('telegramRecovery.previous') }}</Button><span class="text-xs text-muted-foreground">{{ t('telegramRecovery.page', { page: groupsPage }) }}</span><Button type="button" size="sm" variant="ghost" :disabled="saving || groupsLoading || groupsPage * 20 >= groupsTotal" @click="loadGroups(groupsPage + 1)">{{ t('telegramRecovery.next') }}</Button></div>
        </div>
        <div v-if="selected" class="space-y-1 text-sm"><p>{{ t('telegramRecovery.bot_id') }}: {{ selected.bot_instance_id }} · {{ t('telegramRecovery.config') }}: {{ selected.version }} · {{ t('telegramRecovery.route') }}: {{ selected.route_version }}</p><p class="text-muted-foreground">{{ t('telegramRecovery.sameBot') }}</p></div>
        <div class="space-y-2"><Label for="recovery-chat">{{ t('telegramRecovery.destination') }}</Label><Input id="recovery-chat" v-model="chatID" type="text" maxlength="20" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('telegramRecovery.chatHint') }}</p></div>
        </template>
        <div class="space-y-2"><Label for="recovery-reason">{{ t('telegramRecovery.reason') }}</Label><Input id="recovery-reason" v-model="reason" maxlength="512" /><p class="text-xs text-muted-foreground">{{ t('telegramRecovery.reasonHint') }}</p></div>
      </fieldset>
      <DialogFooter><Button variant="outline" :disabled="saving" @click="editorOpen = false">{{ t('telegramRecovery.close') }}</Button><Button :disabled="saving || !valid || (draftKind === 'bot' ? botsLoading || !canBotCreate || !canBots : groupsLoading || !canCreate || !canGroups)" @click="submit">{{ t(saving ? 'telegramRecovery.loading' : 'telegramRecovery.submit') }}</Button></DialogFooter>
    </DialogScrollContent></Dialog>

    <Dialog v-model:open="detailOpen"><DialogScrollContent class="sm:max-w-5xl"><DialogHeader><DialogTitle>{{ t('telegramRecovery.detail') }} · {{ detail?.task.id }}</DialogTitle><DialogDescription>{{ t('telegramRecovery.hint') }}</DialogDescription></DialogHeader>
      <p v-if="detailLoading" role="status">{{ t('telegramRecovery.loading') }}</p><p v-if="detailError" role="alert" class="text-destructive">{{ detailError }}</p>
      <template v-if="detail">
        <TelegramIdentityMigration v-if="detail.task.kind === 'identity_transfer'" :key="detail.task.id" :existing-task-i-d="detail.task.id" />
        <div class="flex items-center gap-3"><Badge :variant="membershipStatusVariant(detail.task.state)">{{ label(detail.task.state) }}</Badge><span>{{ label(detail.task.kind) }}</span><Button size="sm" variant="outline" :disabled="detailLoading || cancelling || executing || refreshingProgress" @click="refreshProgress">{{ t('telegramRecovery.refreshProgress') }}</Button></div>
        <p class="text-sm text-muted-foreground">{{ t('telegramRecovery.progressHint') }}</p>
        <p v-if="detail.execution_available !== true" class="text-sm text-muted-foreground">{{ t('telegramRecovery.executionUnavailable') }}</p>
        <p v-if="['needs_review', 'partial_failed'].includes(detail.task.state)" class="text-sm text-muted-foreground">{{ t('telegramRecovery.reviewRequired') }}</p>
        <p v-if="executionSuccess" role="status" class="text-sm">{{ executionSuccess }}</p>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm"><div v-for="field in taskFields" :key="field" class="border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramRecovery.${field}`) }}</dt><dd class="break-all">{{ detail.task[field] || '—' }}</dd></div><div v-for="field in (['expires_at', 'created_at', 'updated_at'] as const)" :key="field" class="border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramRecovery.${field}`) }}</dt><dd>{{ date(detail.task[field]) }}</dd></div></dl>
        <div class="rounded-xl border border-border bg-card overflow-x-auto">
          <Table>
            <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
              <TableHead class="px-6 py-3"></TableHead>
              <TableHead class="px-6 py-3">{{ t('telegramRecovery.old') }}</TableHead>
              <TableHead class="px-6 py-3">{{ t('telegramRecovery.new') }}</TableHead>
            </TableRow></TableHeader>
            <TableBody class="divide-y divide-border">
              <TableRow v-for="field in endpointFields" :key="field" class="hover:bg-muted/30">
                <TableCell class="px-6 py-4">{{ t(`telegramRecovery.${field}`) }}</TableCell>
                <TableCell class="px-6 py-4 font-mono">{{ detail.snapshot.old[field] }}</TableCell>
                <TableCell class="px-6 py-4 font-mono">{{ detail.snapshot.new[field] }}</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <div class="grid grid-cols-2 gap-6"><section v-for="side in (['expected_old', 'expected_new'] as const)" :key="side" class="space-y-2"><h3 class="font-medium">{{ t(`telegramRecovery.${side}`) }}</h3><dl class="text-sm"><div v-for="field in versionFields" :key="field" class="flex justify-between border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramRecovery.${field}`) }}</dt><dd>{{ detail.snapshot[side]?.[field] || '—' }}</dd></div></dl></section></div>
        <section class="space-y-2"><h3 class="font-medium">{{ t('telegramRecovery.scope') }}</h3><dl class="text-sm"><div v-for="field in scopeFields" :key="field" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramRecovery.${field}`) }}</dt><dd class="break-all">{{ detail.snapshot.scope[field]?.join(', ') || '—' }}</dd></div></dl></section>
        <section class="space-y-2"><div class="flex items-center justify-between"><h3 class="font-medium">{{ t('telegramRecovery.steps') }}</h3><Button v-if="can('GET', `/tasks/${detail.task.id}/steps`)" size="sm" variant="outline" :disabled="stepsLoading" @click="loadSteps()">{{ t('telegramRecovery.refresh') }}</Button></div>
          <p v-if="!can('GET', `/tasks/${detail.task.id}/steps`)" class="text-sm text-muted-foreground">{{ t('telegramRecovery.forbidden') }}</p>
          <template v-else>
            <p v-if="stepsLoading" role="status">{{ t('telegramRecovery.loading') }}</p>
            <p v-if="stepsError" role="alert" class="text-destructive">{{ stepsError }}</p>
            <div class="rounded-xl border border-border bg-card">
              <Table>
                <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
                  <TableHead class="px-6 py-3">{{ t('telegramRecovery.id') }}</TableHead>
                  <TableHead class="px-6 py-3">{{ t('telegramRecovery.operation') }}</TableHead>
                  <TableHead class="px-6 py-3">{{ t('telegramRecovery.state') }}</TableHead>
                  <TableHead class="px-6 py-3">{{ t('telegramRecovery.result_code') }}</TableHead>
                </TableRow></TableHeader>
                <TableBody class="divide-y divide-border">
                  <TableRow v-for="step in steps" :key="step.id" class="hover:bg-muted/30">
                    <TableCell class="px-6 py-4">{{ step.id }}</TableCell>
                    <TableCell class="px-6 py-4">{{ step.operation }}</TableCell>
                    <TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(step.state)">{{ label(step.state) }}</Badge></TableCell>
                    <TableCell class="px-6 py-4">{{ step.result_code || '—' }}</TableCell>
                  </TableRow>
                  <TableRow v-if="!steps.length && !stepsLoading"><TableCell :colspan="4" class="px-6 py-8 text-center text-muted-foreground">{{ stepsError || t('telegramRecovery.noSteps') }}</TableCell></TableRow>
                </TableBody>
              </Table>
              <ListPagination always-show :page="stepsPage" :page-size="stepsPageSize" :page-size-options="[10, 20, 50, 100]" :total="stepsTotal" :total-page="stepsTotalPage" :loading="stepsLoading" @change-page="loadSteps" @change-page-size="changeStepsPageSize" />
            </div>
          </template>
        </section>
        <RecoveryBotRouteSnapshot v-if="detail.task.kind === 'bot_replace' && detail.snapshot.bot_routes?.length" :routes="detail.snapshot.bot_routes" />
        <RecoverySupportReview v-if="detailOpen && ['group_replace', 'bot_replace'].includes(detail.task.kind)" :key="detail.task.id" :task="detail.task" />
        <DialogFooter><Button v-if="canCancel(detail.task)" variant="outline" :disabled="cancelling || executing" @click="askCancel(detail.task)">{{ t('telegramRecovery.cancel') }}</Button><Button v-if="canExecute('confirm')" :disabled="executing || cancelling || refreshingProgress" @click="askExecute('confirm')">{{ t('telegramRecovery.confirm') }}</Button><Button v-if="canExecute('resume')" :disabled="executing || cancelling || refreshingProgress" @click="askExecute('resume')">{{ t(detail.task.kind === 'group_replace' && ['needs_review', 'partial_failed'].includes(detail.task.state) ? 'telegramRecovery.reviewAndResume' : 'telegramRecovery.resume') }}</Button></DialogFooter>
      </template>
    </DialogScrollContent></Dialog>

    <Dialog :open="!!executionPending" @update:open="value => { if (!value && !executing) executionPending = undefined }"><DialogScrollContent><DialogHeader><DialogTitle>{{ t(executionPending?.action === 'confirm' ? 'telegramRecovery.confirmTitle' : 'telegramRecovery.resumeTitle') }} · {{ executionPending?.task.id }}</DialogTitle><DialogDescription>{{ t(executionPending?.task.kind === 'bot_replace' ? 'telegramRecovery.botExecutionHint' : executionPending?.action === 'confirm' ? 'telegramRecovery.confirmHint' : 'telegramRecovery.resumeHint') }}</DialogDescription></DialogHeader>
      <template v-if="executionPending"><dl class="space-y-2 text-sm"><div><dt class="text-muted-foreground">{{ t('telegramRecovery.old') }}</dt><dd class="font-mono">{{ executionPending.snapshot.old[executionPending.task.kind === 'bot_replace' ? 'bot_id' : 'chat_id'] }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.new') }}</dt><dd class="font-mono">{{ executionPending.snapshot.new[executionPending.task.kind === 'bot_replace' ? 'bot_id' : 'chat_id'] }}</dd></div><div><dt class="text-muted-foreground">{{ t('telegramRecovery.version') }}</dt><dd>{{ executionPending.task.version }}</dd></div></dl>
        <RecoveryBotRouteSnapshot v-if="executionPending.task.kind === 'bot_replace' && executionPending.snapshot.bot_routes?.length" :routes="executionPending.snapshot.bot_routes" />
        <div class="flex items-center gap-2"><Checkbox id="recovery-execution-ack" :model-value="executionAcknowledged" :disabled="executing || !!executionError" @update:model-value="executionAcknowledged = $event === true" /><Label for="recovery-execution-ack">{{ t(executionPending.task.kind === 'bot_replace' ? 'telegramRecovery.botAcknowledge' : 'telegramRecovery.executionAcknowledge') }}</Label></div>
        <p v-if="executionError" role="alert" class="text-destructive">{{ executionError }}</p>
        <DialogFooter><Button variant="outline" :disabled="executing" @click="executionPending = undefined">{{ t('telegramRecovery.close') }}</Button><Button :disabled="executing || cancelling || !executionAcknowledged || !!executionError || !canExecute(executionPending.action)" @click="execute">{{ t(executing ? 'telegramRecovery.loading' : executionPending.action === 'confirm' ? 'telegramRecovery.confirm' : 'telegramRecovery.resume') }}</Button></DialogFooter>
      </template>
    </DialogScrollContent></Dialog>

    <Dialog :open="!!cancelPending" @update:open="value => { if (!value && !cancelling) cancelPending = undefined }"><DialogScrollContent><DialogHeader><DialogTitle>{{ t('telegramRecovery.cancelTitle') }} · {{ cancelPending?.id }}</DialogTitle><DialogDescription>{{ t('telegramRecovery.cancelHint') }}</DialogDescription></DialogHeader><p v-if="cancelError" role="alert" class="text-destructive">{{ cancelError }}</p><DialogFooter><Button variant="outline" :disabled="cancelling" @click="cancelPending = undefined">{{ t('telegramRecovery.close') }}</Button><Button :disabled="cancelling || !!cancelError" @click="cancel">{{ t('telegramRecovery.cancel') }}</Button></DialogFooter></DialogScrollContent></Dialog>
  </div>
</template>
