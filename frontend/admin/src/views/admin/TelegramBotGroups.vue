<script setup lang="ts">
import ListPagination from '@/components/ListPagination.vue'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { onMounted, ref } from 'vue'
import SupportGroupBinding from './components/SupportGroupBinding.vue'
import SupportPolicyDialog from './components/SupportPolicyDialog.vue'
import { useAdminAuthStore } from '@/stores/auth'
import type { TelegramGroup } from '@/api/telegramGroups'
import { useI18n } from 'vue-i18n'
import { useTelegramGroups, groupFresh, groupState } from '@/composables/useTelegramGroups'
import { useTelegramBotInstances } from '@/composables/useTelegramBotInstances'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import TelegramBotSelect from '@/components/telegram/TelegramBotSelect.vue'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { formatDate } from '@/utils/format'

// Layout follows TelegramBotStatus: compact toolbar, bordered table and dialogs.
const { t, te } = useI18n()
const auth = useAdminAuthStore()
const policyBot = ref<{ id: number; label: string } | null>(null)
/** Check policy read permission separately from group and binding permissions.
 * @param group Selected group. @returns Whether its policy entry is visible.
 */
function canPolicy(group: TelegramGroup) {
  return group.purpose === 'support' && auth.hasPermission(`GET:/admin/telegram-bot/support/settings/${group.bot_instance_id}`)
}
/** Open the shared Bot policy, never a per-group copy.
 * @param group Selected support group. @returns Nothing.
 */
function openPolicy(group: TelegramGroup) {
  if (!canPolicy(group) || policyBot.value || bindingBusy.value || saving.value || loading.value) return
  const bot = instances.value.find(item => item.id === group.bot_instance_id)
  policyBot.value = { id: group.bot_instance_id, label: bot?.username ? `@${bot.username.replace(/^@/, '')}` : `${t('telegramBot.groups.instance')} ${group.bot_instance_id}` }
}
const bindingBusy = ref(false)
const bindingRevision = ref(0)
const { can, canList, items, page, pageSize, total, botFilter, purposeFilter, loading, error, load, validFilter, search, resetFilters,
  detail, detailOpen, detailLoading, detailError, showDetail, editorOpen, editing, form, formError, conflict,
  saving, checkingID, now, enableChecking, enableNotice, toggleEnabled, valid, openEditor, reloadEditor, save, check } = useTelegramGroups()
const { instances, refresh, refreshError: instanceError, loading: instancesLoading, canList: canListInstances } = useTelegramBotInstances()
const label = (value: string) => te(`telegramBot.groups.values.${value}`) ? t(`telegramBot.groups.values.${value}`) : value || '—'
const date = (value: string | null) => value ? formatDate(value) : '—'
const yesNo = (value: boolean) => t(`telegramBot.groups.${value ? 'yes' : 'no'}`)
const permissionFields = ['is_member', 'is_forum', 'can_manage_topics', 'can_invite_users', 'can_restrict_members'] as const
onMounted(() => { void load(); void refresh() })
</script>

<template>
  <div class="space-y-6">
    <div v-if="!canList && can('POST')" class="flex justify-end">
      <Button data-guide="groups-create" :disabled="saving || bindingBusy" @click="openEditor()">{{ t('telegramBot.groups.create') }}</Button>
    </div>
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>
    <div v-if="canList" data-guide="groups-filter" class="flex items-end gap-3">
      <div class="w-56 space-y-2"><Label>{{ t('telegramBot.groups.instance') }}</Label>
        <TelegramBotSelect v-model="botFilter" :disabled="loading || bindingBusy" />
      </div>
      <div class="w-48 space-y-2"><Label>{{ t('telegramBot.groups.purpose') }}</Label>
        <Select v-model="purposeFilter" :disabled="loading || bindingBusy"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>
          <SelectItem value="all">{{ t('telegramBot.groups.all') }}</SelectItem><SelectItem value="support">{{ label('support') }}</SelectItem><SelectItem value="membership">{{ label('membership') }}</SelectItem>
        </SelectContent></Select>
      </div>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="loading || bindingBusy || !validFilter" @click="search">{{ t('admin.common.search') }}</Button>
        <Button variant="outline" :disabled="loading || bindingBusy" @click="resetFilters">{{ t('admin.common.reset') }}</Button>
      </div>
      <Button v-if="can('POST')" data-guide="groups-create" class="ml-auto" :disabled="saving || bindingBusy" @click="openEditor()">{{ t('telegramBot.groups.create') }}</Button>
    </div>
    <p v-if="loading" role="status" class="text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <div v-if="canList" data-guide="groups-list" class="rounded-xl border border-border bg-card overflow-hidden">
      <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
        <TableHead class="px-6 py-3">{{ t('telegramBot.groups.name') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.purpose') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.instance') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.chatID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.enabled') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupportSettings.reception') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.capabilities') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.lastSuccess') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
      </TableRow></TableHeader><TableBody class="divide-y divide-border">
        <TableRow v-for="item in items" :key="item.id" class="hover:bg-muted/30">
          <TableCell class="px-6 py-4"><p class="font-medium">{{ item.name }}</p></TableCell>
          <TableCell class="px-6 py-4">{{ label(item.purpose) }}</TableCell><TableCell class="px-6 py-4">{{ item.bot_instance_id }}</TableCell><TableCell class="px-6 py-4 font-mono">{{ item.telegram_chat_id }}</TableCell><TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(item.enabled ? 'active' : 'inactive')">{{ yesNo(item.enabled) }}</Badge></TableCell>
          <TableCell class="px-6 py-4">
            <SupportGroupBinding v-if="item.purpose === 'support'" :group="item" :now="now" :revision="bindingRevision" :disabled="bindingBusy || saving || loading" @busy="bindingBusy = $event" @saved="bindingRevision++" />
            <span v-else>—</span>
          </TableCell>
          <TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(groupState(item, now))">{{ label(groupState(item, now)) }}</Badge><p v-if="item.observation.error_class" class="text-xs text-destructive">{{ label(item.observation.error_class) }}</p></TableCell>
          <TableCell class="px-6 py-4">{{ date(item.observation.last_success_at) }}</TableCell>
          <TableCell class="px-6 py-4 text-right"><div data-guide="groups-actions" class="flex flex-wrap items-center justify-end gap-2">
            <Button v-if="canPolicy(item)" size="sm" variant="outline" :disabled="saving || loading || bindingBusy" :title="t('telegramSupportSettings.dialogTitle')" :aria-label="t('telegramSupportSettings.dialogTitle')" @click="openPolicy(item)">{{ t('telegramSupportSettings.settingsEntry') }}</Button>
            <Button v-if="can('GET', `/${item.id}`)" size="sm" variant="outline" @click="showDetail(item.id)" :title="t('telegramBot.groups.detail')" :aria-label="t('telegramBot.groups.detail')">{{ t('tableAction.detail') }}</Button>
            <Button v-if="can('PATCH', `/${item.id}`)" size="sm" variant="outline" :disabled="saving || loading || bindingBusy" @click="openEditor(item)" :title="t('telegramBot.groups.edit')" :aria-label="t('telegramBot.groups.edit')">{{ t('admin.common.edit') }}</Button>
            <Button v-if="can('POST', `/${item.id}/check`)" size="sm" variant="outline" :disabled="checkingID !== null || item.checking || loading" @click="check(item)" :title="t('telegramBot.groups.check')" :aria-label="t('telegramBot.groups.check')" :aria-busy="checkingID === item.id">{{ t(checkingID === item.id ? 'tableAction.checking' : 'tableAction.check') }}</Button>
          </div></TableCell>
        </TableRow>
        <TableRow v-if="!items.length && !loading && !error"><TableCell :colspan="9" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
      </TableBody></Table>
      <ListPagination always-show :page="page" :page-size="pageSize" :page-size-options="[10, 20, 50, 100]" :total="total" :total-page="Math.ceil(total / pageSize)" :loading="loading" :disabled="bindingBusy || !validFilter" @change-page="load" @change-page-size="size => { pageSize = size; load(1) }" />
    </div>

    <SupportPolicyDialog v-if="policyBot" :key="policyBot.id" :botID="policyBot.id" :bot-label="policyBot.label" @close="policyBot = null" />
    <Dialog v-model:open="editorOpen"><DialogContent><DialogHeader><DialogTitle>{{ t(editing ? 'telegramBot.groups.edit' : 'telegramBot.groups.create') }}</DialogTitle></DialogHeader>
      <p class="text-sm text-muted-foreground">{{ t('telegramBot.groups.routeHint') }}</p>
      <p v-if="formError" role="alert" class="text-sm text-destructive">{{ formError }}</p>
      <p v-if="instanceError" role="alert" class="text-sm text-destructive">{{ instanceError }}</p>
      <fieldset :disabled="saving || bindingBusy" class="space-y-3">
        <div v-if="editing" class="space-y-2"><Label for="group-name">{{ t('telegramBot.groups.name') }}</Label><Input id="group-name" v-model="form.name" maxlength="120" /></div>
        <p v-else class="text-sm text-muted-foreground">{{ t('telegramBot.groups.autoNameHint') }}</p>
        <div class="space-y-2"><Label>{{ t('telegramBot.groups.purpose') }}</Label><Select v-model="form.purpose" :disabled="saving || !!editing?.enabled"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="support">{{ label('support') }}</SelectItem><SelectItem value="membership">{{ label('membership') }}</SelectItem></SelectContent></Select></div>
        <div class="space-y-2"><Label>{{ t('telegramBot.groups.instance') }}</Label>
          <Select :model-value="String(form.bot_instance_id || '')" :disabled="saving || !!editing?.enabled || instancesLoading || !canListInstances" @update:model-value="value => form.bot_instance_id = Number(value)"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="bot in instances" :key="bot.id" :value="String(bot.id)">{{ bot.id }} · {{ bot.username || bot.telegram_id }}</SelectItem></SelectContent></Select>
          <p v-if="!canListInstances" class="text-xs text-muted-foreground">{{ t('telegramBot.groups.instancePermission') }}</p>
          <p v-else-if="!instancesLoading && !instances.length" class="text-xs text-muted-foreground">{{ t('telegramBot.instances.empty') }}</p>
        </div>
        <div class="space-y-2"><Label for="group-chat">{{ t('telegramBot.groups.chatID') }}</Label><Input id="group-chat" v-model="form.telegram_chat_id" type="text" :disabled="!!editing?.enabled" /><p class="text-xs text-muted-foreground">{{ t('telegramBot.groups.chatHint') }}</p></div>
        <div class="flex items-center gap-2"><Switch id="group-enabled" :model-value="form.enabled" :disabled="saving || enableChecking" @update:model-value="toggleEnabled" /><Label for="group-enabled">{{ t('telegramBot.groups.enabled') }}</Label></div>
      </fieldset>
      <p v-if="enableChecking" role="status" class="text-sm text-muted-foreground">{{ t('telegramBot.groups.checking') }}</p>
      <p v-if="enableNotice" role="status" class="text-sm text-muted-foreground">{{ enableNotice }}</p>
      <DialogFooter>
        <Button v-if="editing && can('GET', `/${editing.id}`)" variant="outline" :disabled="saving || bindingBusy" @click="reloadEditor">{{ t('telegramBot.groups.reloadDraft') }}</Button>
        <Button :disabled="saving || enableChecking || conflict || !valid" @click="save">{{ t('telegramBot.settings.save') }}</Button>
      </DialogFooter>
    </DialogContent></Dialog>

    <Dialog scope="page" v-model:open="detailOpen"><DialogContent class="sm:max-w-5xl max-h-[85vh] overflow-y-auto"><DialogHeader><DialogTitle>{{ t('telegramBot.groups.detail') }}{{ detail ? ` · ${detail.name}` : '' }}</DialogTitle></DialogHeader>
      <p v-if="detailLoading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="detailError" role="alert" class="text-sm text-destructive">{{ detailError }}</p>
      <template v-if="detail">
        <div class="flex items-center gap-3"><Badge :variant="membershipStatusVariant(groupState(detail, now))">{{ label(groupState(detail, now)) }}</Badge><span class="text-sm">{{ t(groupFresh(detail, now) ? 'telegramBot.groups.fresh' : 'telegramBot.groups.stale') }}</span>
          <Button v-if="can('POST', `/${detail.id}/check`)" size="sm" variant="outline" :disabled="checkingID !== null || detail.checking" @click="check(detail)">{{ t('telegramBot.groups.check') }}</Button>
          <Button size="sm" variant="outline" @click="showDetail(detail.id)">{{ t('telegramBot.instances.reload') }}</Button>
        </div>
        <p class="text-xs text-muted-foreground">{{ t('telegramBot.groups.readOnlyHint') }}</p>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
          <div v-for="entry in [
            ['instance', detail.bot_instance_id], ['botIdentity', detail.observation.bot_telegram_id], ['chatID', detail.telegram_chat_id], ['purpose', label(detail.purpose)],
            ['chatType', label(detail.observation.chat_type)], ['memberStatus', label(detail.observation.member_status)], ['version', detail.version], ['routeVersion', detail.route_version],
            ['lastAttempt', date(detail.observation.checked_at)], ['observedAt', date(detail.observation.observed_at)], ['lastSuccess', date(detail.observation.last_success_at)], ['lastFailure', date(detail.observation.last_failure_at)],
            ['freshUntil', date(detail.fresh_until)], ['source', label(detail.observation.source)], ['errorClass', label(detail.observation.error_class)], ['retryAfter', detail.observation.retry_after_seconds], ['nextCheck', date(detail.next_check_at)],
          ]" :key="String(entry[0])" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramBot.groups.${entry[0]}`) }}</dt><dd class="break-all text-right">{{ entry[1] }}</dd></div>
          <div v-for="field in permissionFields" :key="field" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ label(field) }}</dt><dd>{{ yesNo(detail.observation[field]) }}</dd></div>
        </dl>
        <p class="text-sm">{{ t('telegramBot.groups.missing') }}: {{ detail.missing_capabilities?.map(label).join(', ') || '—' }}</p>
        <p class="text-sm">{{ t('telegramBot.groups.affected') }}: {{ (groupFresh(detail, now) ? detail.affected_functions : [detail.purpose])?.map(label).join(', ') || '—' }}</p>
        <h3 class="font-medium">{{ t('telegramBot.groups.routes') }}</h3>
        <div class="rounded-xl border border-border bg-card overflow-hidden">
          <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
            <TableHead class="px-6 py-3">{{ t('telegramBot.groups.routeVersion') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.instance') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.chatID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.effectiveAt') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.retiredAt') }}</TableHead>
          </TableRow></TableHeader><TableBody class="divide-y divide-border">
            <TableRow v-for="route in detail.routes" :key="route.id" class="hover:bg-muted/30"><TableCell class="px-6 py-4">{{ route.route_version }}</TableCell><TableCell class="px-6 py-4">{{ route.bot_instance_id }}</TableCell><TableCell class="px-6 py-4">{{ route.telegram_chat_id }}</TableCell><TableCell class="px-6 py-4">{{ date(route.effective_at) }}</TableCell><TableCell class="px-6 py-4">{{ date(route.retired_at) }}</TableCell></TableRow>
            <TableRow v-if="!detail.routes?.length"><TableCell :colspan="5" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
          </TableBody></Table>
        </div>
        <h3 class="font-medium">{{ t('telegramBot.groups.operations') }}</h3>
        <div class="rounded-xl border border-border bg-card overflow-hidden">
          <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
            <TableHead class="px-6 py-3">{{ t('telegramBot.groups.routeVersion') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.actor') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.source') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.capabilities') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.errorClass') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.startedAt') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.finishedAt') }}</TableHead>
          </TableRow></TableHeader><TableBody class="divide-y divide-border">
            <TableRow v-for="op in detail.operations" :key="op.id" class="hover:bg-muted/30"><TableCell class="px-6 py-4">{{ op.route_version }}</TableCell><TableCell class="px-6 py-4">{{ op.actor_admin_id }}</TableCell><TableCell class="px-6 py-4">{{ label(op.source) }}</TableCell><TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(op.state)">{{ label(op.state) }}</Badge></TableCell><TableCell class="px-6 py-4">{{ label(op.error_class) }}</TableCell><TableCell class="px-6 py-4">{{ date(op.started_at) }}</TableCell><TableCell class="px-6 py-4">{{ date(op.finished_at) }}</TableCell></TableRow>
            <TableRow v-if="!detail.operations?.length"><TableCell :colspan="7" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
          </TableBody></Table>
        </div>
      </template>
    </DialogContent></Dialog>

  </div>
</template>
