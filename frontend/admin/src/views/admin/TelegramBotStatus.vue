<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { botState, botStateVariant, useTelegramBotInstances, type BotInstance } from '@/composables/useTelegramBotInstances'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDate } from '@/utils/format'

const { t, te } = useI18n()
const { instances, loading, busy, testingId, refreshError, now, canList, canCreate, can, refresh, startPolling, create, patch, test } = useTelegramBotInstances()
const dialog = ref(false)
const editing = ref<BotInstance | null>(null)
const token = ref('')
const deliveryOpen = ref(false)
const deliveryLoading = ref(false)
const deliveries = ref<Array<{ id: number; status: string; attempts: number; last_error?: string }>>([])
const deliveryError = ref('')
const page = ref(1)
const deliveryTotal = ref(0)
let deliveryRequest = 0
const deliveryBot = ref<BotInstance | null>(null)
const stateLabel = (state: string) => te(`telegramBot.instances.states.${state}`) ? t(`telegramBot.instances.states.${state}`) : state
onMounted(startPolling)
onUnmounted(() => { deliveryRequest++ })
function open(item: BotInstance | null) { editing.value = item ? { ...item } : null; token.value = ''; dialog.value = true }
async function save() {
  if (!token.value.trim()) return
  const ok = editing.value ? await patch(editing.value, { token: token.value.trim() }) : await create(token.value.trim())
  if (ok) { token.value = ''; dialog.value = false }
}
async function loadDeliveries(item: BotInstance, nextPage = 1) {
  if (!can('GET', item.id, '/deliveries')) return
  const request = ++deliveryRequest
  deliveryBot.value = item; page.value = nextPage; deliveryOpen.value = true; deliveryLoading.value = true; deliveryError.value = ''
  deliveries.value = []; deliveryTotal.value = 0
  try {
    const res = await adminAPI.getTelegramBotInstanceDeliveries(item.id, { page: nextPage, page_size: 20 })
    if (request !== deliveryRequest) return
    deliveries.value = res.data.data.items
    deliveryTotal.value = res.data.data.total
  } catch { if (request === deliveryRequest) deliveryError.value = t('telegramBot.instances.failed') } finally { if (request === deliveryRequest) deliveryLoading.value = false }
}
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between gap-3">
      <div><h2 class="text-2xl font-bold tracking-tight">{{ t('telegramBot.instances.title') }}</h2><p class="text-muted-foreground">{{ t('telegramBot.instances.subtitle') }}</p></div>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="loading || busy || !canList" @click="refresh">{{ t('admin.common.search') }}</Button>
        <Button v-if="canCreate" :disabled="busy" @click="open(null)">{{ t('telegramBot.instances.create') }}</Button>
      </div>
    </div>
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramBot.instances.forbidden') }}</p>
    <p v-if="loading" role="status" class="text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <div v-if="canList" class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table class="min-w-[1080px]">
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
          <TableHead class="px-6 py-3">{{ t('telegramBot.instances.bot') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.token') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.desiredState') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.actualState') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.version') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.status.lastSeenAt') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.backlog') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
        </TableRow></TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-for="item in instances" :key="item.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4"><p class="font-medium">{{ item.username ? `@${item.username}` : item.telegram_id }}</p><p v-if="item.last_error" class="max-w-64 break-words text-xs text-destructive">{{ item.last_error }}</p></TableCell>
            <TableCell class="px-6 py-4">{{ t(item.token_set ? 'telegramBot.instances.tokenSet' : 'telegramBot.instances.tokenMissing') }}</TableCell>
            <TableCell class="px-6 py-4">{{ t(item.enabled ? 'telegramBot.instances.desiredRunning' : 'telegramBot.instances.desiredStopped') }}</TableCell>
            <TableCell class="px-6 py-4"><Badge :variant="botStateVariant(item, now)" class="rounded-full whitespace-nowrap"><span aria-hidden="true" class="size-1.5 shrink-0 rounded-full bg-current" />{{ stateLabel(botState(item, now)) }}</Badge></TableCell>
            <TableCell class="px-6 py-4 font-mono whitespace-nowrap">{{ item.applied_version }} / {{ item.version }}</TableCell>
            <TableCell class="px-6 py-4 whitespace-nowrap text-muted-foreground">{{ item.last_seen_at ? formatDate(item.last_seen_at) : '-' }}</TableCell>
            <TableCell class="px-6 py-4 whitespace-nowrap">{{ item.backlog ?? '-' }} / {{ item.unknown ?? '-' }}</TableCell>
            <TableCell class="min-w-[240px] px-6 py-4 text-right"><div class="flex flex-wrap items-center justify-end gap-2">
              <Button v-if="can('PATCH', item.id)" size="sm" :variant="item.enabled ? 'destructive' : 'outline'" :disabled="busy || loading" @click="patch(item, { enabled: !item.enabled })">{{ t(item.enabled ? 'telegramBot.instances.stop' : 'telegramBot.instances.start') }}</Button>
              <Button v-if="can('PATCH', item.id)" size="sm" variant="outline" :disabled="busy || loading" @click="open(item)" :title="t('telegramBot.instances.rotate')" :aria-label="t('telegramBot.instances.rotate')">{{ t('tableAction.rotate') }}</Button>
              <Button v-if="can('POST', item.id, '/test')" size="sm" variant="outline" :disabled="busy || loading" :aria-busy="testingId === item.id" @click="test(item)" :title="t('telegramBot.instances.test')" :aria-label="t('telegramBot.instances.test')">{{ t(testingId === item.id ? 'tableAction.testing' : 'tableAction.test') }}</Button>
              <Button v-if="can('GET', item.id, '/deliveries')" size="sm" variant="outline" @click="loadDeliveries(item)" :title="t('telegramBot.instances.deliveries')" :aria-label="t('telegramBot.instances.deliveries')">{{ t('tableAction.deliveries') }}</Button>
            </div></TableCell>
          </TableRow>
          <TableRow v-if="!instances.length && !loading && !refreshError"><TableCell :colspan="8" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
        </TableBody>
      </Table>
    </div>
    <Dialog v-model:open="dialog" @update:open="token = ''">
      <DialogContent><DialogHeader><DialogTitle>{{ t(editing ? 'telegramBot.instances.rotate' : 'telegramBot.instances.create') }}</DialogTitle></DialogHeader>
        <div class="space-y-3"><Label for="bot-token">{{ t('telegramBot.instances.token') }}</Label><Input id="bot-token" v-model="token" type="password" autocomplete="new-password" />
        </div>
        <DialogFooter><Button :disabled="busy || !token.trim()" @click="save">{{ t('telegramBot.settings.save') }}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
    <Dialog scope="page" v-model:open="deliveryOpen"><DialogContent class="sm:max-w-3xl"><DialogHeader><DialogTitle>{{ t('telegramBot.instances.deliveries') }}</DialogTitle></DialogHeader>
      <Button variant="outline" :disabled="deliveryLoading || !deliveryBot || !can('GET', deliveryBot.id, '/deliveries')" @click="deliveryBot && loadDeliveries(deliveryBot, 1)">{{ t('admin.common.search') }}</Button>
      <p v-if="deliveryError" role="alert" class="text-destructive">{{ deliveryError }}</p>
      <p v-if="deliveryLoading">{{ t('telegramBot.instances.loading') }}</p>
      <div class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead class="px-4 py-3">ID</TableHead><TableHead class="px-4 py-3">{{ t('telegramBot.instances.state') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramBot.instances.attempts') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramBot.instances.error') }}</TableHead></TableRow></TableHeader><TableBody class="divide-y divide-border">
          <TableRow v-for="item in deliveries" :key="item.id" class="hover:bg-muted/30"><TableCell class="px-4 py-3 font-mono">{{ item.id }}</TableCell><TableCell class="px-4 py-3">{{ stateLabel(item.status) }}</TableCell><TableCell class="px-4 py-3">{{ item.attempts }}</TableCell><TableCell class="px-4 py-3 max-w-80 break-words text-muted-foreground">{{ item.last_error || '-' }}</TableCell></TableRow>
          <TableRow v-if="!deliveries.length && !deliveryLoading"><TableCell :colspan="4" class="px-4 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
        </TableBody></Table>
      </div>
      <DialogFooter><Button variant="outline" :disabled="page <= 1 || deliveryLoading" @click="deliveryBot && loadDeliveries(deliveryBot, page - 1)">{{ t('telegramBot.instances.previous') }}</Button><Button variant="outline" :disabled="page * 20 >= deliveryTotal || deliveryLoading" @click="deliveryBot && loadDeliveries(deliveryBot, page + 1)">{{ t('telegramBot.instances.next') }}</Button></DialogFooter>
    </DialogContent></Dialog>
  </div>
</template>
