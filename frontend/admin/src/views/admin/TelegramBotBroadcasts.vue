<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminTelegramBroadcast } from '@/api/types'
import { useAdminAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDate } from '@/utils/format'
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
    items.value = res.data?.data || []
    pagination.value = res.data?.pagination || pagination.value
    if (!silent) error.value = ''
  } catch {
    if (request !== requestID) return
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

onMounted(() => {
  void fetchBroadcasts(1)
  pollTimer = setInterval(poll, 8000)
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', poll)
})

onUnmounted(() => {
  ++requestID
  if (pollTimer !== undefined) clearInterval(pollTimer)
  if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', poll)
})
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
        <Button variant="outline" :disabled="loading || refreshing || !canList" @click="fetchBroadcasts(pagination.page, { silent: items.length > 0 })">
          {{ t('telegramBot.instances.reload') }}
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
    <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <Card v-if="canList">
      <CardContent class="p-4">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>ID</TableHead>
              <TableHead>{{ t('telegramBot.instances.bot') }}</TableHead>
              <TableHead>{{ t('telegramBot.broadcasts.tableTitle') }}</TableHead>
              <TableHead>{{ t('telegramBot.broadcasts.tableStatus') }}</TableHead>
              <TableHead>{{ t('telegramBot.broadcasts.tableRecipientCount') }}</TableHead>
              <TableHead>{{ t('telegramBot.broadcasts.tableSuccessCount') }}</TableHead>
              <TableHead>{{ t('telegramBot.broadcasts.tableFailedCount') }}</TableHead>
              <TableHead>{{ t('telegramBot.instances.states.unknown') }}</TableHead>
              <TableHead>{{ t('telegramBot.broadcasts.tableCreatedAt') }}</TableHead>
              <TableHead class="text-right">{{ t('telegramBot.broadcasts.tableActions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-if="loading && !items.length">
              <TableCell :colspan="10" class="py-10 text-center">
                <Loader2 class="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
              </TableCell>
            </TableRow>
            <TableRow v-else-if="!items.length">
              <TableCell :colspan="10" class="py-10 text-center text-muted-foreground">{{ error || t('telegramBot.broadcasts.empty') }}</TableCell>
            </TableRow>
            <TableRow v-for="item in items" :key="item.id">
              <TableCell>{{ item.id }}</TableCell>
              <TableCell>{{ item.bot_id || '-' }}</TableCell>
              <TableCell class="max-w-64 break-words">
                <p class="font-medium">{{ item.title }}</p>
                <p v-if="item.last_error" class="text-xs text-destructive">{{ item.last_error }}</p>
              </TableCell>
              <TableCell>{{ formatStatus(item.status) }}</TableCell>
              <TableCell>{{ item.recipient_count }}</TableCell>
              <TableCell>{{ item.success_count }}</TableCell>
              <TableCell>{{ item.failed_count }}</TableCell>
              <TableCell>{{ item.unknown_count ?? 0 }}</TableCell>
              <TableCell>{{ formatDate(item.created_at) || '-' }}</TableCell>
              <TableCell class="text-right">
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
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </CardContent>
    </Card>
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
