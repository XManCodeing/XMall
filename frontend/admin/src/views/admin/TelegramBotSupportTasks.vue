<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import SupportNavigation from '@/components/telegram/SupportNavigation.vue'
import ListPagination from '@/components/ListPagination.vue'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { useI18n } from 'vue-i18n'
import { useTelegramSupportList } from '@/composables/useTelegramSupportList'
import { useTelegramSupportTask } from '@/composables/useTelegramSupportTask'
import { supportTaskStates, type SupportTaskRecord, type SupportTaskResource } from '@/api/telegramSupport'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import TelegramBotSelect from '@/components/telegram/TelegramBotSelect.vue'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import SupportTopicDeletion from '@/components/telegram/SupportTopicDeletion.vue'
import { formatDate } from '@/utils/format'

const { t, te } = useI18n()
const resource = ref<SupportTaskResource>('downstreams')
const resources = ['downstreams', 'upstreams', 'topic-operations'] as const
const { can, canList, items, page, total, pageSize, loading, error, botFilter, stateFilter, validFilter, requiresBot, load, search, resetFilters: resetListFilters } = useTelegramSupportList(resource)
/** 重置任务条件，保留上行与话题任务所需的机器人范围。@returns 单次第一页查询完成。 */
async function resetFilters(): Promise<void> {
  if (loading.value) return
  if (!requiresBot.value) { await resetListFilters(); return }
  stateFilter.value = 'all'
  page.value = 1
  await search()
}
const rows = computed(() => items.value as SupportTaskRecord[])
const { open, loading: detailLoading, saving, error: detailError, success, task, conflict, form, canResolve, reviewable, valid, isTopic, show, resolve, acknowledgeConflict } = useTelegramSupportTask(load, resource, botFilter)
const states = computed(() => supportTaskStates[resource.value])
/** Reload the same dialog after a verified fact; preserve the conflict review barrier. */
async function refreshDeletion(conflicted = false) {
  const current = task.value
  if (!current || resource.value !== 'topic-operations') return
  await show(current.id, conflicted)
  if (resource.value === 'topic-operations' && task.value?.id === current.id && task.value?.bot_id === current.bot_id) {
    if (!conflicted) success.value = t('telegramSupport.resolved')
    await load()
  }
}
/** Display only the kind/action metadata that exists in this resource DTO. */
const kindOf = (row: SupportTaskRecord) => 'action' in row ? row.action : 'kind' in row ? row.kind : 'upstream'
const detailEntries = computed(() => {
  const row = task.value
  if (!row) return []
  const entries = [['sessionID', row.session_id], ['version', row.version], ['kind', label(kindOf(row))], ['state', label(row.state)], ['updatedAt', formatDate(row.updated_at)]]
  return 'target_id' in row ? [...entries, ['targetID', row.target_id], ['routeVersion', row.route_version]] : [...entries, ['deliveryID', row.delivery_id || '—']]
})
/** Translate known metadata states without presenting unknown values as success. */
const label = (value: string) => te(`telegramSupport.values.${value}`) ? t(`telegramSupport.values.${value}`) : t('telegramSupport.values.unknown')
onMounted(() => { void load() })
</script>

<template>
  <div class="-mt-2 space-y-6 md:-mt-4">
    <SupportNavigation data-guide="support-tabs" active="tasks" class="mb-2" />
    <div data-guide="support-resource" class="w-56 space-y-2"><Label>{{ t('telegramSupport.resource') }}</Label><Select v-model="resource"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="kind in resources" :key="kind" :value="kind">{{ t(`telegramSupport.resources.${kind}`) }}</SelectItem></SelectContent></Select></div>
    <p class="text-sm text-muted-foreground">{{ t(`telegramSupport.identities.${resource}`) }}</p>
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>    <div v-if="canList" data-guide="support-filter" class="flex items-end gap-3">
      <div class="w-56 space-y-2"><Label for="support-task-bot">{{ t('telegramBot.groups.instance') }}</Label><TelegramBotSelect id="support-task-bot" v-model="botFilter" :required="requiresBot" :disabled="loading" /></div>
      <div class="w-48 space-y-2"><Label>{{ t('telegramBot.instances.state') }}</Label><Select v-model="stateFilter" :disabled="loading"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{{ t('telegramBot.groups.all') }}</SelectItem><SelectItem v-for="state in states" :key="state" :value="state">{{ label(state) }}</SelectItem></SelectContent></Select></div>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="loading || !validFilter" @click="search">{{ t('admin.common.search') }}</Button>
        <Button variant="outline" :disabled="loading" @click="resetFilters">{{ t('admin.common.reset') }}</Button>
      </div>
    </div>
    <p v-if="loading" role="status" class="text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <div v-if="canList" data-guide="support-list" class="rounded-xl border border-border bg-card overflow-hidden">
      <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
        <TableHead class="px-6 py-3">{{ t('telegramSupport.taskID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.instance') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.sessionID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.kind') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.state') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.updatedAt') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
      </TableRow></TableHeader><TableBody class="divide-y divide-border">
        <TableRow v-for="row in rows" :key="row.id" class="hover:bg-muted/30"><TableCell class="px-6 py-4">{{ row.id }}</TableCell><TableCell class="px-6 py-4">{{ row.bot_id }}</TableCell><TableCell class="px-6 py-4">{{ row.session_id }}</TableCell><TableCell class="px-6 py-4">{{ label(kindOf(row)) }}</TableCell><TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(row.state === 'ready' ? 'queued' : row.state)">{{ label(row.state) }}</Badge></TableCell><TableCell class="px-6 py-4">{{ formatDate(row.updated_at) }}</TableCell><TableCell class="px-6 py-4 text-right"><div data-guide="support-actions" class="flex flex-wrap items-center justify-end gap-2"><Button v-if="can('GET', `/${row.id}`)" variant="outline" size="sm" @click="show(row.id)" :title="t('telegramBot.groups.detail')" :aria-label="t('telegramBot.groups.detail')">{{ t('tableAction.detail') }}</Button></div></TableCell></TableRow>
        <TableRow v-if="!rows.length && !loading && !error"><TableCell :colspan="7" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
      </TableBody></Table>
      <ListPagination always-show :page="page" :page-size="pageSize" :page-size-options="[10, 20, 50, 100]" :total="total" :total-page="Math.ceil(total / pageSize)" :loading="loading" :disabled="!validFilter" @change-page="load" @change-page-size="size => { pageSize = size; load(1) }" />
    </div>

    <Dialog v-model:open="open"><DialogContent class="sm:max-w-2xl max-h-[85vh] overflow-y-auto"><DialogHeader><DialogTitle>{{ t('telegramSupport.taskDetail') }}{{ task ? ` · ${task.id}` : '' }}</DialogTitle><DialogDescription>{{ t(`telegramSupport.identities.${resource}`) }}</DialogDescription></DialogHeader>
      <p v-if="detailLoading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="detailError" role="alert" class="text-sm text-destructive">{{ detailError }}</p><p v-if="success" role="status" class="text-sm">{{ success }}</p>
      <template v-if="task">
        <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm"><div v-for="entry in detailEntries" :key="String(entry[0])" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramSupport.${entry[0]}`) }}</dt><dd class="break-all text-right">{{ entry[1] }}</dd></div></dl>
        <p v-if="!reviewable" class="text-sm text-muted-foreground">{{ t('telegramSupport.readOnlyTask') }}</p>
        <template v-else-if="canResolve">
          <p class="text-sm text-muted-foreground">{{ t(isTopic ? 'telegramSupport.topicResolveHint' : 'telegramSupport.resolveHint') }}</p>
          <p v-if="conflict" role="alert" class="text-sm text-destructive">{{ t('telegramSupport.conflict') }}</p>
          <fieldset :disabled="saving || detailLoading || conflict" class="space-y-3">
            <div class="space-y-2"><Label>{{ t('telegramSupport.decision') }}</Label><Select v-model="form.decision" :disabled="saving || detailLoading || conflict"><SelectTrigger><SelectValue :placeholder="t('telegramSupport.selectDecision')" /></SelectTrigger><SelectContent><SelectItem :value="isTopic ? 'succeeded' : 'delivered'">{{ t(isTopic ? 'telegramSupport.confirmSucceeded' : 'telegramSupport.confirmDelivered') }}</SelectItem><SelectItem value="failed">{{ t('telegramSupport.confirmFailed') }}</SelectItem></SelectContent></Select></div>
            <div class="space-y-2"><Label for="support-evidence">{{ t('telegramSupport.evidence') }}</Label><Input id="support-evidence" v-model="form.evidence" maxlength="103" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('telegramSupport.evidenceHint') }}</p></div>
            <div v-if="!isTopic" class="space-y-2"><Label for="support-delivery">{{ t('telegramSupport.deliveryID') }}</Label><Input id="support-delivery" v-model="form.deliveryID" inputmode="numeric" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('telegramSupport.deliveryHint') }}</p></div>
            <div v-else-if="form.decision === 'succeeded'" class="space-y-2"><Label for="support-thread">{{ t('telegramSupport.threadID') }}</Label><Input id="support-thread" v-model="form.threadID" inputmode="numeric" maxlength="19" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('telegramSupport.threadHint') }}</p></div>
            <div v-else-if="form.decision === 'failed'" class="flex items-center gap-2"><Checkbox id="support-no-effect" :model-value="form.noEffect" @update:model-value="form.noEffect = $event === true" /><Label for="support-no-effect">{{ t('telegramSupport.noEffect') }}</Label></div>
          </fieldset>
        </template><p v-else class="text-sm text-muted-foreground">{{ t('telegramSupport.resolveForbidden') }}</p>
        <SupportTopicDeletion v-if="isTopic && 'action' in task" :key="`${task.bot_id}:${task.id}`" :task="task" :busy="saving || detailLoading" :conflict="conflict" @recorded="refreshDeletion()" @conflict="refreshDeletion(true)" />
        <p v-if="conflict && isTopic && !reviewable" role="alert" class="text-sm text-destructive">{{ t('telegramSupport.conflict') }}</p>
        <Button v-if="conflict && isTopic && !reviewable" variant="outline" :disabled="saving || detailLoading" @click="acknowledgeConflict">{{ t('telegramSupport.reviewAgain') }}</Button>
        <DialogFooter><Button variant="outline" :disabled="saving || detailLoading" @click="show(task.id)">{{ t('telegramBot.instances.reload') }}</Button><Button v-if="conflict && canResolve && reviewable" variant="outline" :disabled="saving || detailLoading" @click="acknowledgeConflict">{{ t('telegramSupport.reviewAgain') }}</Button><Button v-if="canResolve && reviewable" :disabled="!valid || saving || detailLoading" @click="resolve">{{ t(saving ? 'telegramSupport.saving' : 'telegramSupport.resolve') }}</Button></DialogFooter>
      </template>
    </DialogContent></Dialog>
  </div>
</template>
