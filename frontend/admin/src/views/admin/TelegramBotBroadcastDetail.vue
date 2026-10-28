<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useAdminAuthStore } from '@/stores/auth'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminTelegramBroadcast } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDate } from '@/utils/format'
import { notifyError } from '@/utils/notify'
import { sanitizeBroadcastHtml } from '@/utils/broadcastHtml'
import { ArrowLeft, Loader2, Paperclip } from 'lucide-vue-next'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useTelegramBroadcastDeliveries } from '@/composables/useTelegramBroadcastDeliveries'

const { t, te } = useI18n()
const route = useRoute()
const auth = useAdminAuthStore()
const error = ref('')
let requestID = 0
const broadcastId = computed(() => Number(route.params.id))

const loading = ref(false)
const broadcast = ref<AdminTelegramBroadcast | null>(null)
const messageHtml = computed(() => sanitizeBroadcastHtml(broadcast.value?.message_html || ''))
const supportsDeliveries = computed(() => broadcast.value?.delivery_version === 1 && Boolean(broadcast.value?.bot_id))
const {
  items: deliveries, loading: deliveriesLoading, retrying, error: deliveryError,
  notice: deliveryNotice, pagination: deliveryPagination, canList: canListDeliveries,
  canRetry, fetchPage: fetchDeliveries, retry,
} = useTelegramBroadcastDeliveries(broadcastId, supportsDeliveries)

const formatRecipientType = (value: string) =>
  value === 'specific' ? t('telegramBot.broadcasts.recipientTypeSpecific') : t('telegramBot.broadcasts.recipientTypeAll')

const formatStatus = (value: string) => {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'running') return t('telegramBot.broadcasts.statusRunning')
  if (normalized === 'completed') return t('telegramBot.broadcasts.statusCompleted')
  if (normalized === 'failed') return t('telegramBot.broadcasts.statusFailed')
  if (normalized === 'pending') return t('telegramBot.broadcasts.statusPending')
  return te(`telegramBot.instances.states.${normalized}`) ? t(`telegramBot.instances.states.${normalized}`) : value
}

const statusClass = (value: string) => {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'completed') return 'text-primary'
  if (normalized === 'failed') return 'text-destructive'
  if (normalized === 'running') return 'text-primary'
  return 'text-muted-foreground'
}

const fetchBroadcast = async () => {
  const request = ++requestID
  broadcast.value = null
  error.value = ''
  if (!auth.hasPermission(`GET:/admin/telegram-bot/broadcasts/${broadcastId.value}`)) {
    error.value = t('telegramBot.instances.forbidden')
    return
  }
  loading.value = true
  try {
    const res = await adminAPI.getTelegramBroadcast(broadcastId.value)
    if (request === requestID) broadcast.value = res.data?.data || null
  } catch {
    if (request === requestID) {
      error.value = t('telegramBot.broadcasts.detailLoadFailed')
      notifyError(error.value)
    }
  } finally {
    if (request === requestID) loading.value = false
  }
}

watch(broadcastId, fetchBroadcast, { immediate: true })
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h2 class="text-2xl font-bold tracking-tight">{{ t('telegramBot.broadcasts.detailTitle') }}</h2>
        <p class="text-muted-foreground">{{ t('telegramBot.broadcasts.detailSubtitle') }}</p>
      </div>
      <Button variant="outline" class="w-full sm:w-auto" as-child>
        <RouterLink to="/telegram-bot/broadcasts">
          <ArrowLeft class="h-4 w-4 mr-2" />
          {{ t('telegramBot.broadcasts.backToList') }}
        </RouterLink>
      </Button>
    </div>

    <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <Button variant="outline" :disabled="loading || retrying !== null" @click="fetchBroadcast">{{ t('telegramBot.instances.reload') }}</Button>
    <div v-if="loading" class="flex justify-center py-20">
      <Loader2 class="h-6 w-6 animate-spin text-muted-foreground" />
    </div>

    <template v-else-if="broadcast">
      <!-- Basic Info -->
      <Card>
        <CardHeader>
          <CardTitle>{{ t('telegramBot.broadcasts.detailBasicInfo') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <dt class="text-sm font-medium text-muted-foreground">ID</dt>
              <dd class="mt-1">{{ broadcast.id }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.fieldTitle') }}</dt>
              <dd class="mt-1">{{ broadcast.title }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.tableRecipientType') }}</dt>
              <dd class="mt-1">{{ formatRecipientType(broadcast.recipient_type) }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.tableCreatedAt') }}</dt>
              <dd class="mt-1">{{ formatDate(broadcast.created_at) || '-' }}</dd>
            </div>
          </dl>
        </CardContent>
      </Card>

      <!-- Execution Status -->
      <Card>
        <CardHeader>
          <CardTitle>{{ t('telegramBot.broadcasts.detailExecution') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.tableStatus') }}</dt>
              <dd class="mt-1 font-semibold" :class="statusClass(broadcast.status)">{{ formatStatus(broadcast.status) }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.tableRecipientCount') }}</dt>
              <dd class="mt-1">{{ broadcast.recipient_count }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.tableSuccessCount') }}</dt>
              <dd class="mt-1 text-primary">{{ broadcast.success_count }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.tableFailedCount') }}</dt>
              <dd class="mt-1" :class="broadcast.failed_count > 0 ? 'text-destructive' : ''">{{ broadcast.failed_count }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.fieldStartedAt') }}</dt>
              <dd class="mt-1">{{ formatDate(broadcast.started_at || '') || '-' }}</dd>
            </div>
            <div>
              <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.fieldCompletedAt') }}</dt>
              <dd class="mt-1">{{ formatDate(broadcast.completed_at || '') || '-' }}</dd>
            </div>
          </dl>
          <div v-if="broadcast.last_error" class="mt-4">
            <dt class="text-sm font-medium text-muted-foreground">{{ t('telegramBot.broadcasts.fieldLastError') }}</dt>
            <dd class="mt-1 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{{ broadcast.last_error }}</dd>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle>{{ t('telegramBot.instances.recipientDetails') }}</CardTitle></CardHeader>
        <CardContent class="space-y-3">
          <dl class="flex gap-8 text-sm">
            <div><dt class="text-muted-foreground">{{ t('telegramBot.instances.bot') }}</dt><dd>{{ broadcast.bot_id || '-' }}</dd></div>
            <div><dt class="text-muted-foreground">{{ t('telegramBot.instances.states.unknown') }}</dt><dd>{{ broadcast.unknown_count ?? 0 }}</dd></div>
          </dl>
          <p v-if="!supportsDeliveries" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.recipientUnavailable') }}</p>
          <p v-else-if="!canListDeliveries" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.recipientForbidden') }}</p>
          <template v-else>
            <div class="flex items-center justify-between gap-3">
              <p class="text-sm text-muted-foreground">{{ t('telegramBot.instances.retryHint') }}</p>
              <Button variant="outline" size="sm" :disabled="deliveriesLoading || retrying !== null" @click="fetchDeliveries()">{{ t('telegramBot.instances.reload') }}</Button>
            </div>
            <p v-if="deliveryError" role="alert" class="text-sm text-destructive">{{ deliveryError }}</p>
            <p v-if="deliveryNotice" role="status" class="text-sm text-muted-foreground">{{ deliveryNotice }}</p>
            <Table>
              <TableHeader><TableRow>
                <TableHead>ID</TableHead>
                <TableHead>{{ t('telegramBot.instances.recipientUser') }}</TableHead>
                <TableHead>{{ t('telegramBot.instances.state') }}</TableHead>
                <TableHead>{{ t('telegramBot.instances.attempts') }}</TableHead>
                <TableHead>{{ t('telegramBot.instances.messageID') }}</TableHead>
                <TableHead>{{ t('telegramBot.instances.error') }}</TableHead>
                <TableHead>{{ t('telegramBot.instances.updatedAt') }}</TableHead>
                <TableHead class="text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
              </TableRow></TableHeader>
              <TableBody>
                <TableRow v-if="deliveriesLoading"><TableCell :colspan="8" class="py-8 text-center"><Loader2 class="mx-auto h-5 w-5 animate-spin text-muted-foreground" /></TableCell></TableRow>
                <TableRow v-else-if="!deliveries.length"><TableCell :colspan="8" class="py-8 text-center text-muted-foreground">{{ deliveryError || t('telegramBot.instances.empty') }}</TableCell></TableRow>
                <TableRow v-for="item in deliveriesLoading ? [] : deliveries" :key="item.id">
                  <TableCell>{{ item.id }}</TableCell>
                  <TableCell><p>{{ item.user_id }}</p><p class="text-xs text-muted-foreground">Telegram: {{ item.telegram_user_id }}</p></TableCell>
                  <TableCell>{{ formatStatus(item.status) }}</TableCell>
                  <TableCell>{{ item.attempts }}</TableCell>
                  <TableCell>{{ item.message_id || '-' }}</TableCell>
                  <TableCell class="max-w-64 break-words text-xs">{{ item.last_error || '-' }}</TableCell>
                  <TableCell>{{ formatDate(item.updated_at) || '-' }}</TableCell>
                  <TableCell class="text-right"><Button v-if="canRetry(item)" variant="outline" size="sm" :disabled="retrying !== null" @click="retry(item)">{{ t('telegramBot.instances.retryRecipient') }}</Button></TableCell>
                </TableRow>
              </TableBody>
            </Table>
            <div class="flex items-center justify-end gap-3">
              <Button variant="outline" size="sm" :disabled="deliveriesLoading || retrying !== null || deliveryPagination.page <= 1" @click="fetchDeliveries(deliveryPagination.page - 1)">{{ t('telegramBot.instances.previous') }}</Button>
              <span class="text-sm text-muted-foreground">{{ t('telegramBot.broadcasts.pageSummary', { page: deliveryPagination.page, total: Math.max(1, deliveryPagination.total_page) }) }}</span>
              <Button variant="outline" size="sm" :disabled="deliveriesLoading || retrying !== null || deliveryPagination.page >= deliveryPagination.total_page" @click="fetchDeliveries(deliveryPagination.page + 1)">{{ t('telegramBot.instances.next') }}</Button>
            </div>
          </template>
        </CardContent>
      </Card>

      <!-- Message Content -->
      <Card>
        <CardHeader>
          <CardTitle>{{ t('telegramBot.broadcasts.detailMessageContent') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <div class="rounded-md border bg-muted/50 p-4">
            <div class="prose prose-sm max-w-none dark:prose-invert break-words whitespace-pre-wrap" v-html="messageHtml" />
          </div>
        </CardContent>
      </Card>

      <!-- Attachment -->
      <Card v-if="broadcast.attachment_url">
        <CardHeader>
          <CardTitle>{{ t('telegramBot.broadcasts.detailAttachment') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <div class="flex items-center gap-3">
            <Paperclip class="h-4 w-4 text-muted-foreground" />
            <a :href="broadcast.attachment_url" target="_blank" rel="noopener noreferrer" class="text-sm text-primary underline underline-offset-4 hover:text-primary/80">
              {{ broadcast.attachment_name || broadcast.attachment_url }}
            </a>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
