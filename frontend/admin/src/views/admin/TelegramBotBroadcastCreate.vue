<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { adminAPI } from '@/api/admin'
import type { AdminTelegramBroadcastUser } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { notifyError, notifyErrorOnce, notifySuccess } from '@/utils/notify'
import { formatDate, toRFC3339 } from '@/utils/format'
import { Loader2, Plus, Search, Send, Trash2, Users, ImageIcon } from 'lucide-vue-next'
import MediaPicker from '@/components/admin/MediaPicker.vue'
import { useAdminAuthStore } from '@/stores/auth'
import { useTelegramBotInstances } from '@/composables/useTelegramBotInstances'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import FileInput from '@/components/FileInput.vue'

const { t } = useI18n()
const router = useRouter()
const auth = useAdminAuthStore()
const canCreate = computed(() => auth.hasPermission('POST:/admin/telegram-bot/broadcasts'))
const canListUsers = computed(() => auth.hasPermission('GET:/admin/telegram-bot/users'))
const { instances, refresh, loading: loadingBots, refreshError: botError, canList } = useTelegramBotInstances()
const botID = ref('')
const availableBots = computed(() => instances.value.filter(item => item.mode === 'internal' && item.enabled && item.token_set))
let userRequest = 0

const submitting = ref(false)
const uploading = ref(false)
const loadingUsers = ref(false)

type BroadcastButton = { text: string; url: string }

const form = reactive({
  title: '',
  recipient_type: 'all',
  attachment_url: '',
  attachment_name: '',
  message_html: '',
  buttons: [] as BroadcastButton[],
})

const addButton = () => {
  if (form.buttons.length >= 5) return
  form.buttons.push({ text: '', url: '' })
}

const removeButton = (index: number) => {
  form.buttons.splice(index, 1)
}

const normalizedButtons = computed(() =>
  form.buttons
    .map(item => ({ text: item.text.trim(), url: item.url.trim() }))
    .filter(item => item.text || item.url),
)

const buttonsInvalid = computed(() =>
  normalizedButtons.value.some(item => !item.text || !/^https?:\/\//i.test(item.url)),
)

const canSubmit = computed(() =>
  canCreate.value
  && !submitting.value
  && !uploading.value
  && availableBots.value.some(item => item.id === Number(botID.value))
  && !(form.recipient_type === 'specific' && !canListUsers.value)
  && !buttonsInvalid.value,
)

const submitBlockedReason = computed(() => {
  if (!canCreate.value || !canList.value) return t('telegramBot.instances.forbidden')
  if (!availableBots.value.length) return t('telegramBot.instances.empty')
  if (!botID.value) return t('telegramBot.broadcasts.botRequired')
  if (form.recipient_type === 'specific' && !canListUsers.value) return t('telegramBot.instances.forbidden')
  if (buttonsInvalid.value) return t('telegramBot.broadcasts.buttonsInvalid')
  return ''
})

const filters = reactive({
  keyword: '',
  display_name: '',
  telegram_username: '',
  telegram_user_id: '',
  created_from: '',
  created_to: '',
})

const selectedUserIds = ref<number[]>([])
const users = ref<AdminTelegramBroadcastUser[]>([])
const pagination = ref({
  page: 1,
  page_size: 10,
  total: 0,
  total_page: 1,
})

const allPageSelected = computed(() => users.value.length > 0 && users.value.every((item) => selectedUserIds.value.includes(item.user_id)))
const selectedCount = computed(() => selectedUserIds.value.length)
const fetchUsers = async (page = 1) => {
  if (form.recipient_type !== 'specific' || !botID.value || !canListUsers.value) return
  const request = ++userRequest
  loadingUsers.value = true
  try {
    const res = await adminAPI.getTelegramBroadcastUsers({
      bot_id: Number(botID.value),
      page,
      page_size: pagination.value.page_size,
      keyword: filters.keyword || undefined,
      display_name: filters.display_name || undefined,
      telegram_username: filters.telegram_username || undefined,
      telegram_user_id: filters.telegram_user_id || undefined,
      created_from: toRFC3339(filters.created_from),
      created_to: toRFC3339(filters.created_to),
    })
    if (request !== userRequest) return
    users.value = res.data?.data || []
    pagination.value = res.data?.pagination || pagination.value
  } catch (error) {
    if (request !== userRequest) return
    users.value = []
    notifyErrorOnce(error, t('telegramBot.broadcasts.userLoadFailed'))
  } finally {
    if (request === userRequest) loadingUsers.value = false
  }
}

const resetFilters = () => {
  filters.keyword = ''
  filters.display_name = ''
  filters.telegram_username = ''
  filters.telegram_user_id = ''
  filters.created_from = ''
  filters.created_to = ''
  fetchUsers(1)
}

const toggleSelectAll = () => {
  if (allPageSelected.value) {
    const currentIds = new Set(users.value.map((item) => item.user_id))
    selectedUserIds.value = selectedUserIds.value.filter((id) => !currentIds.has(id))
    return
  }
  const next = new Set(selectedUserIds.value)
  users.value.forEach((item) => next.add(item.user_id))
  selectedUserIds.value = Array.from(next)
}

const toggleUser = (userID: number, v: boolean | 'indeterminate') => {
  const next = new Set(selectedUserIds.value)
  if (v === true) {
    next.add(userID)
  } else {
    next.delete(userID)
  }
  selectedUserIds.value = Array.from(next)
}

const mediaPickerRef = ref<InstanceType<typeof MediaPicker> | null>(null)
const mediaPickerValue = ref('')

const isBroadcastImage = (name: string) => /\.(png|jpe?g|webp)$/i.test(name)

const handleMediaSelected = (value: string | string[]) => {
  const path = Array.isArray(value) ? value[0] : value
  if (path) {
    const filename = path.split('/').pop() || ''
    if (!isBroadcastImage(filename)) {
      notifyError(t('telegramBot.broadcasts.attachmentImageOnly'))
      mediaPickerValue.value = ''
      return
    }
    form.attachment_url = path
    form.attachment_name = filename
  }
  mediaPickerValue.value = ''
}

const clearAttachment = () => {
  form.attachment_url = ''
  form.attachment_name = ''
}

const handleAttachmentChange = async (files: FileList | null) => {
  const file = files?.[0]
  if (!file) return
  if (!isBroadcastImage(file.name) && !String(file.type || '').startsWith('image/')) {
    notifyError(t('telegramBot.broadcasts.attachmentImageOnly'))
    return
  }

  uploading.value = true
  try {
    const formData = new FormData()
    formData.append('file', file)
    const res = await adminAPI.upload(formData, 'telegram')
    const data = (res.data?.data || {}) as Record<string, unknown>
    const name = String(data.filename || file.name || '')
    if (!isBroadcastImage(name)) {
      notifyError(t('telegramBot.broadcasts.attachmentImageOnly'))
      return
    }
    form.attachment_url = String(data.url || '')
    form.attachment_name = name
    notifySuccess(t('telegramBot.broadcasts.uploadSuccess'))
  } catch (error) {
    notifyErrorOnce(error, t('telegramBot.broadcasts.uploadFailed'))
  } finally {
    uploading.value = false
  }
}

const handleSubmit = async () => {
  if (!canSubmit.value) return
  if (!form.title.trim() || !form.message_html.trim()) {
    notifyError(t('telegramBot.broadcasts.formInvalid'))
    return
  }
  if (form.recipient_type === 'specific' && selectedUserIds.value.length === 0) {
    notifyError(t('telegramBot.broadcasts.usersRequired'))
    return
  }

  submitting.value = true
  try {
    await adminAPI.createTelegramBroadcast({
      bot_id: Number(botID.value),
      title: form.title.trim(),
      recipient_type: form.recipient_type,
      user_ids: form.recipient_type === 'specific' ? selectedUserIds.value : [],
      filters: {},
      attachment_url: form.attachment_url || undefined,
      attachment_name: form.attachment_name || undefined,
      message_html: form.message_html.trim(),
      buttons: normalizedButtons.value.length
        ? normalizedButtons.value.map(item => [item])
        : undefined,
    })
    notifySuccess(t('telegramBot.broadcasts.createSuccess'))
    router.push('/telegram-bot/broadcasts')
  } catch (error) {
    notifyErrorOnce(error, t('telegramBot.broadcasts.createFailed'))
  } finally {
    submitting.value = false
  }
}

watch(
  () => form.recipient_type,
  (value) => {
    if (value === 'specific' && users.value.length === 0) {
      fetchUsers(1)
    }
  },
)

watch(botID, () => {
  ++userRequest
  users.value = []
  selectedUserIds.value = []
  loadingUsers.value = false
  pagination.value = { page: 1, page_size: 10, total: 0, total_page: 1 }
  fetchUsers(1)
})

watch(availableBots, (bots) => {
  if (botID.value) return
  if (bots.length === 1) botID.value = String(bots[0]!.id)
}, { immediate: true })

onMounted(() => {
  refresh()
})
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h2 class="text-2xl font-bold tracking-tight">{{ t('telegramBot.broadcasts.createTitle') }}</h2>
        <p class="text-muted-foreground">{{ t('telegramBot.broadcasts.createSubtitle') }}</p>
      </div>
      <Button variant="outline" class="w-full sm:w-auto" as-child>
        <RouterLink to="/telegram-bot/broadcasts">
          {{ t('telegramBot.broadcasts.backToList') }}
        </RouterLink>
      </Button>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('telegramBot.broadcasts.basicInfo') }}</CardTitle>
        <CardDescription>{{ t('telegramBot.broadcasts.basicInfoDesc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <p v-if="!canCreate || !canList" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.forbidden') }}</p>
        <p v-if="botError" role="alert" class="text-sm text-destructive">{{ botError }}</p>
        <div class="space-y-2">
          <Label>{{ t('telegramBot.instances.bot') }}</Label>
          <Select v-model="botID" :disabled="loadingBots || submitting || !canList">
            <SelectTrigger><SelectValue :placeholder="t('telegramBot.instances.bot')" /></SelectTrigger>
            <SelectContent><SelectItem v-for="item in availableBots" :key="item.id" :value="String(item.id)">{{ item.username ? `@${item.username}` : item.telegram_id }}</SelectItem></SelectContent>
          </Select>
          <p v-if="!loadingBots && !availableBots.length" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.empty') }}</p>
          <Button v-if="canList" variant="link" size="sm" :disabled="loadingBots || submitting" @click="refresh">{{ t('telegramBot.instances.reload') }}</Button>
        </div>
        <div class="space-y-2">
          <Label>{{ t('telegramBot.broadcasts.fieldTitle') }}</Label>
          <Input v-model="form.title" :placeholder="t('telegramBot.broadcasts.fieldTitlePlaceholder')" />
        </div>

        <div class="space-y-2">
          <Label>{{ t('telegramBot.broadcasts.fieldRecipientType') }}</Label>
          <div class="grid gap-3 md:grid-cols-2">
            <button
              type="button"
              class="rounded-lg border p-4 text-left transition-colors"
              :class="form.recipient_type === 'all' ? 'border-primary bg-primary/5' : 'border-border'"
              @click="form.recipient_type = 'all'"
            >
              <div class="font-medium">{{ t('telegramBot.broadcasts.recipientTypeAll') }}</div>
              <div class="text-sm text-muted-foreground">{{ t('telegramBot.broadcasts.recipientTypeAllDesc') }}</div>
            </button>
            <button
              type="button"
              class="rounded-lg border p-4 text-left transition-colors"
              :class="form.recipient_type === 'specific' ? 'border-primary bg-primary/5' : 'border-border'"
              @click="form.recipient_type = 'specific'"
            >
              <div class="font-medium">{{ t('telegramBot.broadcasts.recipientTypeSpecific') }}</div>
              <div class="text-sm text-muted-foreground">{{ t('telegramBot.broadcasts.recipientTypeSpecificDesc') }}</div>
            </button>
          </div>
        </div>

        <div class="space-y-2">
          <Label>{{ t('telegramBot.broadcasts.fieldAttachment') }}</Label>
          <div class="rounded-lg border border-dashed p-4">
            <div v-if="form.attachment_url" class="flex flex-wrap items-center justify-between gap-3">
              <div class="space-y-1">
                <div class="font-medium">{{ form.attachment_name || t('telegramBot.broadcasts.attachmentUploaded') }}</div>
                <div class="break-all text-xs text-muted-foreground">{{ form.attachment_url }}</div>
              </div>
              <Button variant="outline" size="sm" @click="clearAttachment">
                <Trash2 class="h-4 w-4 mr-2" />
                {{ t('telegramBot.broadcasts.removeAttachment') }}
              </Button>
            </div>
            <div v-else class="flex flex-wrap items-center gap-3">
              <FileInput
                :button-text="uploading ? t('admin.common.loading') : t('telegramBot.broadcasts.uploadAttachment')"
                :disabled="uploading"
                @change="handleAttachmentChange"
              />
              <Button variant="outline" type="button" @click="mediaPickerRef?.openPicker()">
                <ImageIcon class="h-4 w-4 mr-2" />
                {{ t('admin.mediaPicker.selectFromLibrary') }}
              </Button>
              <MediaPicker ref="mediaPickerRef" :model-value="mediaPickerValue" scene="telegram" dialog-only @update:model-value="handleMediaSelected" />
              <span class="text-sm text-muted-foreground">{{ t('telegramBot.broadcasts.attachmentHint') }}</span>
            </div>
          </div>
        </div>

        <div class="space-y-2">
          <Label>{{ t('telegramBot.broadcasts.fieldMessageHtml') }}</Label>
          <Textarea v-model="form.message_html" :placeholder="t('telegramBot.broadcasts.fieldMessageHtmlPlaceholder')" rows="8" />
          <p class="text-xs text-muted-foreground">{{ t('telegramBot.broadcasts.htmlHint') }}</p>
        </div>

        <div class="space-y-3">
          <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <Label>{{ t('telegramBot.broadcasts.fieldButtons') }}</Label>
              <p class="text-xs text-muted-foreground">{{ t('telegramBot.broadcasts.buttonsHint') }}</p>
            </div>
            <Button type="button" size="sm" variant="outline" class="w-full sm:w-auto" :disabled="form.buttons.length >= 5" @click="addButton">
              <Plus class="mr-1 h-4 w-4" />
              {{ t('telegramBot.broadcasts.addButton') }}
            </Button>
          </div>
          <div v-if="form.buttons.length === 0" class="rounded-lg border border-dashed border-border p-4 text-sm text-muted-foreground">
            {{ t('telegramBot.broadcasts.buttonsEmpty') }}
          </div>
          <div v-for="(button, index) in form.buttons" :key="index" class="grid gap-2 rounded-lg border border-border p-3 md:grid-cols-[1fr_1.4fr_auto]">
            <Input v-model="button.text" :placeholder="t('telegramBot.broadcasts.buttonTextPlaceholder')" />
            <Input v-model="button.url" :placeholder="t('telegramBot.broadcasts.buttonUrlPlaceholder')" />
            <Button type="button" variant="ghost" size="sm" @click="removeButton(index)">
              <Trash2 class="h-4 w-4 text-destructive" />
            </Button>
          </div>
          <p v-if="buttonsInvalid" role="alert" class="text-xs text-destructive">{{ t('telegramBot.broadcasts.buttonsInvalid') }}</p>
        </div>
      </CardContent>
    </Card>

    <Card v-if="form.recipient_type === 'specific'">
      <CardHeader>
        <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>{{ t('telegramBot.broadcasts.userSelectorTitle') }}</CardTitle>
            <CardDescription>{{ t('telegramBot.broadcasts.userSelectorDesc') }}</CardDescription>
          </div>
          <div class="flex w-fit items-center gap-2 rounded-full border px-3 py-1 text-sm">
            <Users class="h-4 w-4" />
            {{ t('telegramBot.broadcasts.selectedCount', { count: selectedCount }) }}
          </div>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          <Input v-model="filters.keyword" :placeholder="t('telegramBot.broadcasts.filterKeyword')" />
          <Input v-model="filters.display_name" :placeholder="t('telegramBot.broadcasts.filterDisplayName')" />
          <Input v-model="filters.telegram_username" :placeholder="t('telegramBot.broadcasts.filterTelegramUsername')" />
          <Input v-model="filters.telegram_user_id" :placeholder="t('telegramBot.broadcasts.filterTelegramUserId')" />
          <Input v-model="filters.created_from" type="datetime-local" />
          <Input v-model="filters.created_to" type="datetime-local" />
        </div>

        <div class="flex flex-wrap gap-2">
          <Button variant="outline" :disabled="loadingUsers" @click="fetchUsers(1)">
            <Search class="h-4 w-4 mr-2" />
            {{ t('telegramBot.broadcasts.searchUsers') }}
          </Button>
          <Button variant="outline" :disabled="loadingUsers" @click="resetFilters">
            {{ t('telegramBot.broadcasts.resetFilters') }}
          </Button>
        </div>

        <div class="rounded-lg border overflow-x-auto">
          <Table class="min-w-[920px]">
            <TableHeader>
              <TableRow>
                <TableHead class="w-12">
                  <Checkbox :model-value="allPageSelected" @update:model-value="toggleSelectAll" />
                </TableHead>
                <TableHead class="min-w-[180px]">{{ t('telegramBot.broadcasts.userDisplayName') }}</TableHead>
                <TableHead class="min-w-[180px]">{{ t('telegramBot.broadcasts.userTelegramUsername') }}</TableHead>
                <TableHead class="min-w-[180px]">{{ t('telegramBot.broadcasts.userTelegramId') }}</TableHead>
                <TableHead class="min-w-[220px]">{{ t('telegramBot.broadcasts.userEmail') }}</TableHead>
                <TableHead class="min-w-[180px]">{{ t('telegramBot.broadcasts.userBoundAt') }}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-if="loadingUsers">
                <TableCell :colspan="6" class="py-8 text-center text-muted-foreground">
                  <Loader2 class="mx-auto h-5 w-5 animate-spin" />
                </TableCell>
              </TableRow>
              <TableRow v-else-if="users.length === 0">
                <TableCell :colspan="6" class="py-8 text-center text-muted-foreground">
                  {{ t('telegramBot.broadcasts.userEmpty') }}
                </TableCell>
              </TableRow>
              <TableRow v-for="item in users" :key="item.user_id">
                <TableCell>
                  <Checkbox
                    :model-value="selectedUserIds.includes(item.user_id)"
                    @update:model-value="(v) => toggleUser(item.user_id, v)"
                  />
                </TableCell>
                <TableCell class="min-w-[180px]">
                  <div class="break-words">{{ item.display_name || '-' }}</div>
                </TableCell>
                <TableCell class="min-w-[180px]">
                  <div class="break-all">{{ item.telegram_username ? `@${item.telegram_username}` : '-' }}</div>
                </TableCell>
                <TableCell class="min-w-[180px] font-mono text-xs">
                  <div class="break-all">{{ item.telegram_user_id }}</div>
                </TableCell>
                <TableCell class="min-w-[220px]">
                  <div class="break-all">{{ item.user_email || '-' }}</div>
                </TableCell>
                <TableCell class="min-w-[180px]">{{ formatDate(item.bound_at) || '-' }}</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>

        <div class="flex flex-wrap items-center justify-end gap-2">
          <Button variant="outline" size="sm" :disabled="pagination.page <= 1" @click="fetchUsers(pagination.page - 1)">
            {{ t('telegramBot.broadcasts.prevPage') }}
          </Button>
          <span class="text-sm text-muted-foreground">
            {{ t('telegramBot.broadcasts.pageSummary', { page: pagination.page, total: pagination.total_page }) }}
          </span>
          <Button variant="outline" size="sm" :disabled="pagination.page >= pagination.total_page" @click="fetchUsers(pagination.page + 1)">
            {{ t('telegramBot.broadcasts.nextPage') }}
          </Button>
        </div>
      </CardContent>
    </Card>

    <div class="flex flex-col items-stretch gap-2 sm:items-end">
      <p v-if="submitBlockedReason" class="text-sm text-muted-foreground">{{ submitBlockedReason }}</p>
      <Button class="w-full sm:w-auto" :disabled="!canSubmit" @click="handleSubmit">
        <Send class="h-4 w-4 mr-2" />
        {{ submitting ? t('admin.common.loading') : t('telegramBot.broadcasts.submit') }}
      </Button>
    </div>
  </div>
</template>
