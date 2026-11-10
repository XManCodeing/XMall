<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminAffiliateWithdraw } from '@/api/types'
import {
  AFFILIATE_WITHDRAW_STATUS_PAID,
  AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW,
  AFFILIATE_WITHDRAW_STATUS_REJECTED,
} from '@/constants/affiliate'
import IdCell from '@/components/IdCell.vue'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import TableSkeleton from '@/components/TableSkeleton.vue'
import ListPagination from '@/components/ListPagination.vue'
import { useListRefresh, type ListFetchOptions } from '@/composables/useListRefresh'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { confirmAction } from '@/utils/confirm'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
import { formatDate } from '@/utils/format'
import { adminUrl } from '@/utils/adminBase'

const { t } = useI18n()
const loading = ref(true)
const { refreshing, refreshList } = useListRefresh()
const operating = ref(false)
const rows = ref<AdminAffiliateWithdraw[]>([])
const pagination = ref({
  page: 1,
  page_size: 20,
  total: 0,
  total_page: 1,
})

const filters = reactive({
  keyword: '',
  affiliateProfileId: '',
  status: '__all__',
})

const normalizeFilterValue = (value: string) => (value === '__all__' ? '' : value)
const userDetailLink = (userId: number) => adminUrl(`/users/${userId}`)
const resolveAffiliateUserID = (item: AdminAffiliateWithdraw) => Number(item?.affiliate_profile?.user_id || item?.affiliate_profile?.user?.id || 0)

const fetchRows = async (page = 1, options: ListFetchOptions = {}) => {
  if (!options.preserveRows) loading.value = true
  try {
    const response = await adminAPI.getAffiliateWithdraws({
      page,
      page_size: pagination.value.page_size,
      keyword: filters.keyword || undefined,
      affiliate_profile_id: filters.affiliateProfileId || undefined,
      status: normalizeFilterValue(filters.status) || undefined,
    })
    rows.value = response.data.data || []
    pagination.value = response.data.pagination || pagination.value
  } catch {
    if (!options.preserveRows) rows.value = []
  } finally {
    if (!options.preserveRows) loading.value = false
  }
}

/** 按当前筛选查询第一页；加载期间忽略重复操作。@returns 查询完成。 */
const handleSearch = async (): Promise<void> => {
  if (loading.value || refreshing.value) return
  pagination.value.page = 1
  await fetchRows(1)
}
/** 清空筛选条件，保留分页尺寸。@returns 查询完成。 */
const resetFilters = async (): Promise<void> => {
  if (loading.value || refreshing.value) return
  Object.assign(filters, { keyword: '', affiliateProfileId: '', status: '__all__' })
  await handleSearch()
}

const refreshCurrentPage = () => {
  refreshList(() => fetchRows(pagination.value.page, { preserveRows: true }))
}

const changePage = (page: number) => {
  if (page < 1 || page > pagination.value.total_page) return
  fetchRows(page)
}

const pageSizeOptions = [10, 20, 50, 100]

const changePageSize = (size: number) => {
  if (size === pagination.value.page_size) return
  pagination.value.page_size = size
  fetchRows(1)
}

const statusLabel = (status?: string) => {
  if (status === AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW) return t('admin.affiliatesWithdraws.status.pendingReview')
  if (status === AFFILIATE_WITHDRAW_STATUS_REJECTED) return t('admin.affiliatesWithdraws.status.rejected')
  if (status === AFFILIATE_WITHDRAW_STATUS_PAID) return t('admin.affiliatesWithdraws.status.paid')
  return status || '-'
}

const statusClass = (status?: string) => {
  if (status === AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW) return 'border-amber-200 bg-amber-50 text-amber-700'
  if (status === AFFILIATE_WITHDRAW_STATUS_REJECTED) return 'border-zinc-200 bg-zinc-50 text-zinc-700'
  if (status === AFFILIATE_WITHDRAW_STATUS_PAID) return 'border-emerald-200 bg-emerald-50 text-emerald-700'
  return 'border-border bg-muted/30 text-muted-foreground'
}

const rejectTarget = ref<AdminAffiliateWithdraw | null>(null)
const rejectReason = ref('')
/** 打开页面内驳回表单，不发送请求。@param row 待审核提现 @returns void */
const rejectWithdraw = (row: AdminAffiliateWithdraw) => {
  if (operating.value) return
  rejectReason.value = ''
  rejectTarget.value = row
}

/** 提交当前提现的驳回原因；失败保留表单以便重试。@returns 提交完成通知 */
const submitRejection = async () => {
  const row = rejectTarget.value
  if (!row || operating.value || row.status !== AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW) return
  operating.value = true
  try {
    await adminAPI.rejectAffiliateWithdraw(row.id, { reason: rejectReason.value.trim() || undefined })
    rejectTarget.value = null
    notifySuccess(t('admin.affiliatesWithdraws.actions.rejectSuccess'))
    await fetchRows(pagination.value.page)
  } catch (err: any) {
    notifyErrorOnce(err, err?.message || t('admin.affiliatesWithdraws.actions.rejectFailed'))
  } finally {
    operating.value = false
  }
}

const payWithdraw = async (row: AdminAffiliateWithdraw) => {
  const confirmed = await confirmAction({ description: t('admin.affiliatesWithdraws.actions.payConfirm', { id: row.id }) })
  if (!confirmed) return
  operating.value = true
  try {
    await adminAPI.payAffiliateWithdraw(row.id)
    notifySuccess(t('admin.affiliatesWithdraws.actions.paySuccess'))
    await fetchRows(pagination.value.page)
  } catch (err: any) {
    notifyErrorOnce(err, err?.message || t('admin.affiliatesWithdraws.actions.payFailed'))
  } finally {
    operating.value = false
  }
}

onMounted(() => {
  fetchRows()
})
</script>

<template>
  <div class="space-y-3">
    <Dialog :open="!!rejectTarget" @update:open="value => { if (!value && !operating) rejectTarget = null }">
      <DialogContent @escape-key-down="event => { if (operating) event.preventDefault() }">
        <DialogHeader>
          <DialogTitle>{{ t('admin.common.confirm') }}</DialogTitle>
          <DialogDescription>{{ t('admin.affiliatesWithdraws.actions.rejectConfirm', { id: rejectTarget?.id }) }}</DialogDescription>
        </DialogHeader>
        <form class="space-y-4" @submit.prevent="submitRejection">
          <Label for="affiliate-reject-reason">{{ t('admin.affiliatesWithdraws.actions.rejectReasonPrompt') }}</Label>
          <Input id="affiliate-reject-reason" v-model="rejectReason" :disabled="operating" />
          <DialogFooter>
            <Button type="button" variant="outline" :disabled="operating" @click="rejectTarget = null">{{ t('admin.common.cancel') }}</Button>
            <Button type="submit" variant="destructive" :disabled="operating">{{ t('admin.common.confirm') }}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  <div class="space-y-6">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <h1 class="text-2xl font-semibold">{{ t('admin.affiliatesWithdraws.title') }}</h1>
    </div>

    <div class="rounded-xl border border-border bg-card p-4 shadow-sm">
      <div class="flex flex-wrap items-center gap-3">
        <div class="w-full md:w-56">
          <Input v-model="filters.keyword" :placeholder="t('admin.affiliatesWithdraws.filters.keyword')" @keydown.enter="handleSearch" />
        </div>
        <div class="w-full md:w-44">
          <Input v-model="filters.affiliateProfileId" :placeholder="t('admin.affiliatesWithdraws.filters.profileId')" @keydown.enter="handleSearch" />
        </div>
        <div class="w-full md:w-48">
          <Select v-model="filters.status">
            <SelectTrigger class="h-9 w-full">
              <SelectValue :placeholder="t('admin.affiliatesWithdraws.filters.statusAll')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all__">{{ t('admin.affiliatesWithdraws.filters.statusAll') }}</SelectItem>
              <SelectItem :value="AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW">{{ t('admin.affiliatesWithdraws.status.pendingReview') }}</SelectItem>
              <SelectItem :value="AFFILIATE_WITHDRAW_STATUS_REJECTED">{{ t('admin.affiliatesWithdraws.status.rejected') }}</SelectItem>
              <SelectItem :value="AFFILIATE_WITHDRAW_STATUS_PAID">{{ t('admin.affiliatesWithdraws.status.paid') }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="flex-1"></div>
        <Button size="sm" variant="outline" :disabled="loading || refreshing" @click="handleSearch">{{ t('admin.common.search') }}</Button>
        <Button size="sm" variant="outline" :disabled="loading || refreshing" @click="resetFilters">{{ t('admin.common.reset') }}</Button>
        <Button size="sm" variant="outline" :disabled="loading || refreshing" @click="refreshCurrentPage">{{ t('admin.common.refresh') }}</Button>
      </div>
    </div>

    <div class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table class="min-w-[980px]">
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-6 py-3">{{ t('admin.affiliatesWithdraws.table.id') }}</TableHead>
            <TableHead class="min-w-[100px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.user') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('admin.affiliatesWithdraws.table.amount') }}</TableHead>
            <TableHead class="min-w-[100px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.channel') }}</TableHead>
            <TableHead class="min-w-[160px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.account') }}</TableHead>
            <TableHead class="min-w-[90px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.status') }}</TableHead>
            <TableHead class="min-w-[160px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.rejectReason') }}</TableHead>
            <TableHead class="min-w-[100px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.processedBy') }}</TableHead>
            <TableHead class="min-w-[100px] px-6 py-3">{{ t('admin.affiliatesWithdraws.table.createdAt') }}</TableHead>
            <TableHead class="min-w-[100px] px-6 py-3 text-right">{{ t('admin.affiliatesWithdraws.table.action') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-if="loading">
            <TableCell :colspan="10" class="p-0">
              <TableSkeleton :columns="10" :rows="5" />
            </TableCell>
          </TableRow>
          <TableRow v-else-if="rows.length === 0">
            <TableCell colspan="10" class="px-6 py-8 text-center text-muted-foreground">{{ t('admin.affiliatesWithdraws.empty') }}</TableCell>
          </TableRow>
          <TableRow v-for="item in rows" :key="item.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4">
              <IdCell :value="item.id" />
            </TableCell>
            <TableCell class="min-w-[100px] px-6 py-4 text-xs text-muted-foreground">
              <div class="text-foreground">
                <span class="break-words">{{ item?.affiliate_profile?.user?.display_name || '-' }}</span>
              </div>
              <div v-if="item?.affiliate_profile?.user?.email" class="mt-0.5 break-all">{{ item.affiliate_profile.user.email }}</div>
              <div class="mt-0.5 break-all">
                <a
                  v-if="resolveAffiliateUserID(item) > 0"
                  :href="userDetailLink(resolveAffiliateUserID(item))"
                  target="_blank"
                  rel="noopener"
                  class="font-mono text-primary underline-offset-4 hover:underline"
                >
                  #{{ resolveAffiliateUserID(item) }}
                </a>
                <span v-else class="font-mono text-foreground">-</span>
                <span class="ml-1 font-mono text-muted-foreground">/ {{ item?.affiliate_profile?.code || '-' }}</span>
              </div>
            </TableCell>
            <TableCell class="px-6 py-4 font-mono text-xs text-foreground">{{ item.amount || '0.00' }}</TableCell>
            <TableCell class="min-w-[100px] px-6 py-4 text-xs text-foreground break-words">{{ item.channel || '-' }}</TableCell>
            <TableCell class="min-w-[160px] px-6 py-4 text-xs text-muted-foreground break-all">{{ item.account || '-' }}</TableCell>
            <TableCell class="min-w-[90px] px-6 py-4 text-xs">
              <span class="inline-flex shrink-0 whitespace-nowrap rounded-full border px-2.5 py-1 text-xs" :class="statusClass(item.status)">
                {{ statusLabel(item.status) }}
              </span>
            </TableCell>
            <TableCell class="min-w-[160px] px-6 py-4 text-xs text-muted-foreground break-words">{{ item.reject_reason || '-' }}</TableCell>
            <TableCell class="min-w-[100px] px-6 py-4 text-xs text-muted-foreground">
              <div class="break-words">{{ (typeof item?.processor === 'object' ? item?.processor?.username : item?.processor) || '-' }}</div>
              <div class="mt-0.5">{{ formatDate(item.processed_at) || '-' }}</div>
            </TableCell>
            <TableCell class="min-w-[100px] px-6 py-4 text-xs text-muted-foreground">{{ formatDate(item.created_at) }}</TableCell>
            <TableCell class="min-w-[100px] px-6 py-4 text-right">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  :disabled="operating || item.status !== AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW"
                  @click="rejectWithdraw(item)"
                >
                  {{ t('admin.affiliatesWithdraws.actions.reject') }}
                </Button>
                <Button
                  size="sm"
                  :disabled="operating || item.status !== AFFILIATE_WITHDRAW_STATUS_PENDING_REVIEW"
                  @click="payWithdraw(item)"
                >
                  <span :title="t('admin.affiliatesWithdraws.actions.pay')" :aria-label="t('admin.affiliatesWithdraws.actions.pay')">{{ t('tableAction.paid') }}</span>
                </Button>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>

      <ListPagination
        :page="pagination.page"
        :total-page="pagination.total_page"
        :total="pagination.total"
        :page-size="pagination.page_size"
        :page-size-options="pageSizeOptions"
        @change-page="changePage"
        @change-page-size="changePageSize"
      />
    </div>
  </div>
  </div>
</template>
