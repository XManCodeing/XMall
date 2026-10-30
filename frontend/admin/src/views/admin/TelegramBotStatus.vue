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
        <Button variant="outline" :disabled="loading || busy || !canList" @click="refresh">{{ t('telegramBot.instances.reload') }}</Button>
        <Button v-if="canCreate" :disabled="busy" @click="open(null)">{{ t('telegramBot.instances.create') }}</Button>
      </div>
    </div>
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramBot.instances.forbidden') }}</p>
    <p v-if="refreshError" role="alert" class="text-sm text-destructive">{{ refreshError }}</p>
    <p v-if="loading" role="status" class="text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <div v-if="canList" class="rounded-lg border border-border">
      <Table>
        <TableHeader><TableRow>
          <TableHead>{{ t('telegramBot.instances.bot') }}</TableHead><TableHead>{{ t('telegramBot.instances.token') }}</TableHead><TableHead>{{ t('telegramBot.instances.desiredState') }}</TableHead><TableHead>{{ t('telegramBot.instances.actualState') }}</TableHead><TableHead>{{ t('telegramBot.instances.version') }}</TableHead><TableHead>{{ t('telegramBot.status.lastSeenAt') }}</TableHead><TableHead>{{ t('telegramBot.instances.backlog') }}</TableHead><TableHead>{{ t('telegramBot.instances.actions') }}</TableHead>
        </TableRow></TableHeader>
        <TableBody>
          <TableRow v-for="item in instances" :key="item.id">
            <TableCell><p class="font-medium">{{ item.username ? `@${item.username}` : item.telegram_id }}</p><p v-if="item.last_error" class="max-w-64 break-words text-xs text-destructive">{{ item.last_error }}</p></TableCell>
            <TableCell>{{ t(item.token_set ? 'telegramBot.instances.tokenSet' : 'telegramBot.instances.tokenMissing') }}</TableCell>
            <TableCell>{{ t(item.enabled ? 'telegramBot.instances.desiredRunning' : 'telegramBot.instances.desiredStopped') }}</TableCell>
            <TableCell><Badge :variant="botStateVariant(item, now)" class="rounded-full whitespace-nowrap"><span aria-hidden="true" class="size-1.5 shrink-0 rounded-full bg-current" />{{ stateLabel(botState(item, now)) }}</Badge></TableCell>
            <TableCell>{{ item.applied_version }} / {{ item.version }}</TableCell>
            <TableCell>{{ item.last_seen_at ? formatDate(item.last_seen_at) : '-' }}</TableCell>
            <TableCell>{{ item.backlog ?? '-' }} / {{ item.unknown ?? '-' }}</TableCell>
            <TableCell><div class="flex flex-wrap gap-1">
              <Button v-if="can('PATCH', item.id)" size="sm" :variant="item.enabled ? 'destructive' : 'success'" :disabled="busy || loading" @click="patch(item, { enabled: !item.enabled })">{{ t(item.enabled ? 'telegramBot.instances.stop' : 'telegramBot.instances.start') }}</Button>
              <Button v-if="can('PATCH', item.id)" size="sm" variant="ghost" :disabled="busy || loading" @click="open(item)">{{ t('telegramBot.instances.rotate') }}</Button>
              <Button v-if="can('POST', item.id, '/test')" size="sm" variant="ghost" :disabled="busy || loading" :aria-busy="testingId === item.id" @click="test(item)">{{ t(testingId === item.id ? 'telegramBot.instances.testing' : 'telegramBot.instances.test') }}</Button>
              <Button v-if="can('GET', item.id, '/deliveries')" size="sm" variant="ghost" @click="loadDeliveries(item)">{{ t('telegramBot.instances.deliveries') }}</Button>
            </div></TableCell>
          </TableRow>
          <TableRow v-if="!instances.length && !loading"><TableCell :colspan="8" class="py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
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
    <Dialog v-model:open="deliveryOpen"><DialogContent class="sm:max-w-3xl"><DialogHeader><DialogTitle>{{ t('telegramBot.instances.deliveries') }}</DialogTitle></DialogHeader>
      <p v-if="deliveryError" role="alert" class="text-destructive">{{ deliveryError }}</p>
      <p v-if="deliveryLoading">{{ t('telegramBot.instances.loading') }}</p>
      <Table><TableHeader><TableRow><TableHead>ID</TableHead><TableHead>{{ t('telegramBot.instances.state') }}</TableHead><TableHead>{{ t('telegramBot.instances.attempts') }}</TableHead><TableHead>{{ t('telegramBot.instances.error') }}</TableHead></TableRow></TableHeader><TableBody>
        <TableRow v-for="item in deliveries" :key="item.id"><TableCell>{{ item.id }}</TableCell><TableCell>{{ stateLabel(item.status) }}</TableCell><TableCell>{{ item.attempts }}</TableCell><TableCell>{{ item.last_error || '-' }}</TableCell></TableRow>
        <TableRow v-if="!deliveries.length && !deliveryLoading"><TableCell :colspan="4">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
      </TableBody></Table>
      <DialogFooter><Button variant="outline" :disabled="page <= 1 || deliveryLoading" @click="deliveryBot && loadDeliveries(deliveryBot, page - 1)">{{ t('telegramBot.instances.previous') }}</Button><Button variant="outline" :disabled="page * 20 >= deliveryTotal || deliveryLoading" @click="deliveryBot && loadDeliveries(deliveryBot, page + 1)">{{ t('telegramBot.instances.next') }}</Button></DialogFooter>
    </DialogContent></Dialog>
  </div>
</template>
