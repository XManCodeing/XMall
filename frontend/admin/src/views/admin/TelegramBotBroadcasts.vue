<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminTelegramBroadcast } from '@/api/types'
import { useAdminAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDate } from '@/utils/format'
import { notifyErrorOnce } from '@/utils/notify'
import { Eye, Loader2, Send } from 'lucide-vue-next'

const { t, te } = useI18n()
const auth = useAdminAuthStore()
const canList = computed(() => auth.hasPermission('GET:/admin/telegram-bot/broadcasts'))
const canCreate = computed(() => auth.hasPermission('POST:/admin/telegram-bot/broadcasts'))
const loading = ref(false)
const refreshing = ref(false)
const error = ref('')
const items = ref<AdminTelegramBroadcast[]>([])
const pagination = ref({ page: 1, page_size: 20, total: 0, total_page: 1 })
let requestID = 0
let pollTimer: ReturnType<typeof setInterval> | undefined

const hasActiveJobs = computed(() =>
  items.value.some(item => item.status === 'pending' || item.status === 'running'),
)

const fetchBroadcasts = async (page = pagination.value.page, options: { silent?: boolean } = {}) => {
  if (!canList.value) return
  const silent = options.silent === true && items.value.length > 0
  if (!silent && loading.value) return
  if (silent && refreshing.value) return
  const request = ++requestID
  if (silent) refreshing.value = true
  else {
    loading.value = true
    error.value = ''
  }
  try {
    const res = await adminAPI.getTelegramBroadcasts({ page, page_size: pagination.value.page_size })
    if (request !== requestID) return
    if (!Array.isArray(res.data?.data)) throw new Error(t('common.api.responseMissing'))
    items.value = res.data.data
    pagination.value = res.data?.pagination || pagination.value
    if (!silent) error.value = ''
  } catch (cause) {
    if (request !== requestID) return
    notifyErrorOnce(cause, t('telegramBot.instances.failed'))
    if (!silent) {
      items.value = []
      error.value = t('telegramBot.instances.failed')
    }
  } finally {
    if (request === requestID) {
      loading.value = false
      refreshing.value = false
    }
  }
}

const formatStatus = (value: string) => {
  const keys: Record<string, string> = { pending: 'statusPending', running: 'statusRunning', completed: 'statusCompleted', failed: 'statusFailed' }
  const key = keys[value]
  if (key) return t(`telegramBot.broadcasts.${key}`)
  return te(`telegramBot.instances.states.${value}`) ? t(`telegramBot.instances.states.${value}`) : value
}

const poll = () => {
  if (typeof document !== 'undefined' && document.hidden) return
  if (!hasActiveJobs.value || loading.value || refreshing.value) return
  void fetchBroadcasts(pagination.value.page, { silent: true })
}

/** Resume one visible-page poller without resetting cached pagination. @returns Nothing. */
function startPolling() {
  if (pollTimer !== undefined) return
  poll()
  pollTimer = setInterval(poll, 8000)
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', poll)
}

/** Suspend background work, retaining list state and any request already in flight. @returns Nothing. */
function stopPolling() {
  if (pollTimer !== undefined) clearInterval(pollTimer)
  pollTimer = undefined
  if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', poll)
}

onMounted(() => { void fetchBroadcasts(1); startPolling() })
onActivated(startPolling)
onDeactivated(stopPolling)
onUnmounted(() => { ++requestID; stopPolling() })
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between gap-3">
      <div>
        <h2 class="text-2xl font-bold tracking-tight">{{ t('telegramBot.broadcasts.title') }}</h2>
        <p class="text-muted-foreground">{{ t('telegramBot.broadcasts.subtitle') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <Loader2 v-if="refreshing" class="h-4 w-4 animate-spin text-muted-foreground" />
        <Button variant="outline" :disabled="loading || refreshing || !canList" @click="fetchBroadcasts(1)">
          {{ t('admin.common.search') }}
        </Button>
        <Button v-if="canCreate" as-child>
          <RouterLink to="/telegram-bot/broadcasts/create">
            <Send class="mr-2 h-4 w-4" />
            {{ t('telegramBot.broadcasts.create') }}
          </RouterLink>
        </Button>
      </div>
    </div>
    <p v-if="!canList" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.forbidden') }}</p>
    <div v-if="canList" class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table>
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-6 py-3">ID</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.instances.bot') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.broadcasts.tableTitle') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.broadcasts.tableStatus') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.broadcasts.tableRecipientCount') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.broadcasts.tableSuccessCount') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.broadcasts.tableFailedCount') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.instances.states.unknown') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('telegramBot.broadcasts.tableCreatedAt') }}</TableHead>
            <TableHead class="px-6 py-3 text-right">{{ t('telegramBot.broadcasts.tableActions') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-if="loading && !items.length">
            <TableCell :colspan="10" class="px-6 py-8 text-center">
              <Loader2 class="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
            </TableCell>
          </TableRow>
          <TableRow v-else-if="!items.length && !error">
            <TableCell :colspan="10" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.broadcasts.empty') }}</TableCell>
          </TableRow>
          <TableRow v-for="item in items" :key="item.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4">{{ item.id }}</TableCell>
            <TableCell class="px-6 py-4">{{ item.bot_id || '-' }}</TableCell>
            <TableCell class="max-w-64 break-words px-6 py-4">
              <p class="font-medium">{{ item.title }}</p>
              <p v-if="item.last_error" class="text-xs text-destructive">{{ item.last_error }}</p>
            </TableCell>
            <TableCell class="px-6 py-4">{{ formatStatus(item.status) }}</TableCell>
            <TableCell class="px-6 py-4">{{ item.recipient_count }}</TableCell>
            <TableCell class="px-6 py-4">{{ item.success_count }}</TableCell>
            <TableCell class="px-6 py-4">{{ item.failed_count }}</TableCell>
            <TableCell class="px-6 py-4">{{ item.unknown_count ?? 0 }}</TableCell>
            <TableCell class="px-6 py-4">{{ formatDate(item.created_at) || '-' }}</TableCell>
            <TableCell class="px-6 py-4 text-right">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <Button
                  v-if="auth.hasPermission(`GET:/admin/telegram-bot/broadcasts/${item.id}`)"
                  variant="outline"
                  size="sm"
                  as-child
                >
                  <RouterLink :to="`/telegram-bot/broadcasts/${item.id}`">
                    <Eye class="mr-2 h-4 w-4" />
                    {{ t('telegramBot.broadcasts.detail') }}
                  </RouterLink>
                </Button>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>
    <div v-if="canList" class="flex items-center justify-end gap-3">
      <Button variant="outline" size="sm" :disabled="loading || refreshing || pagination.page <= 1" @click="fetchBroadcasts(pagination.page - 1, { silent: items.length > 0 })">
        {{ t('telegramBot.broadcasts.prevPage') }}
      </Button>
      <span class="text-sm text-muted-foreground">
        {{ t('telegramBot.broadcasts.pageSummary', { page: pagination.page, total: Math.max(1, pagination.total_page) }) }}
      </span>
      <Button variant="outline" size="sm" :disabled="loading || refreshing || pagination.page >= pagination.total_page" @click="fetchBroadcasts(pagination.page + 1, { silent: items.length > 0 })">
        {{ t('telegramBot.broadcasts.nextPage') }}
      </Button>
    </div>
  </div>
</template>
