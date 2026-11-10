<script setup lang="ts">
import { computed, ref, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminCategory } from '@/api/types'
import IdCell from '@/components/IdCell.vue'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import TableSkeleton from '@/components/TableSkeleton.vue'
import CategoryEditModal from './components/CategoryEditModal.vue'
import { getLocalizedText } from '@/utils/format'
import { getImageUrl } from '@/utils/image'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
import { confirmAction } from '@/utils/confirm'
import { flattenAdminCategories } from '@/utils/category'

const { t } = useI18n()
const loading = ref(false), showModal = ref(false)
const editingCategory = ref<AdminCategory | null>(null)
const categories = ref<AdminCategory[]>([])
const route = useRoute()
const categoryHierarchyItems = computed(() => flattenAdminCategories(categories.value))
/** Get the existing localized hierarchy badge. @param category Category row. @returns Level label. */
const getCategoryLevelText = (category: AdminCategory) => t(category.parent_id > 0 ? 'admin.categories.level.child' : 'admin.categories.level.root')
/** Refresh management rows after editing. @returns Completion promise. */
const fetchCategories = async () => {
  loading.value = true
  try { const response = await adminAPI.getCategories(); categories.value = response.data.data || [] }
  catch (error) { notifyErrorOnce(error, t('common.api.requestFailed')) }
  finally { loading.value = false }
}
/** Open shared editor in create mode. @returns Nothing. */
const openCreateModal = () => { editingCategory.value = null; showModal.value = true }
/** Open shared editor in edit mode. @param category Existing category. @returns Nothing. */
const openEditModal = (category: AdminCategory) => { editingCategory.value = category; showModal.value = true }
const toggleActive = async (category: AdminCategory) => {
  const newStatus = !category.is_active
  try {
    category.is_active = newStatus
    await adminAPI.patchCategoryActive(category.id, newStatus)
    notifySuccess(newStatus ? t('admin.categories.status.activatedTip') : t('admin.categories.status.deactivatedTip'))
  } catch (err: any) {
    category.is_active = !newStatus
    notifyErrorOnce(err, t('admin.categories.errors.toggleFailed', { message: err?.message || '' }))
  }
}
const handleDelete = async (category: AdminCategory) => {
  const confirmed = await confirmAction({ description: t('admin.categories.confirmDelete', { name: getLocalizedText(category.name) }), confirmText: t('admin.common.delete'), variant: 'destructive' })
  if (!confirmed) return
  try { await adminAPI.deleteCategory(category.id); void fetchCategories() }
  catch (err: any) { notifyErrorOnce(err, t('admin.categories.errors.deleteFailed', { message: err?.message || '' })) }
}
const openEditById = async (rawId: unknown) => {
  const id = Number(rawId)
  if (!Number.isFinite(id) || id <= 0) return
  if (!categories.value.length) await fetchCategories()
  const target = categories.value.find(item => item.id === id)
  if (target) openEditModal(target)
}
onMounted(() => { void fetchCategories(); if (route.query.category_id) void openEditById(route.query.category_id) })
watch(() => route.query.category_id, value => { if (value) void openEditById(value) })
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <h1 class="text-2xl font-semibold">{{ t('admin.categories.title') }}</h1>
      <Button class="w-full sm:w-auto" @click="openCreateModal">{{ t('admin.categories.create') }}</Button>
    </div>
    <div class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table class="min-w-[760px]">
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-6 py-3">{{ t('admin.categories.table.id') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('admin.categories.table.icon') }}</TableHead>
            <TableHead class="px-6 py-3 min-w-[260px]">{{ t('admin.categories.table.name') }}</TableHead>
            <TableHead class="px-6 py-3 min-w-[220px]">{{ t('admin.categories.table.slug') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('admin.categories.table.sort') }}</TableHead>
            <TableHead class="px-6 py-3">{{ t('admin.categories.table.status') }}</TableHead>
            <TableHead class="px-6 py-3 min-w-[140px] text-right">{{ t('admin.categories.table.action') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-if="loading"><TableCell :colspan="7" class="p-0"><TableSkeleton :columns="7" :rows="5" /></TableCell></TableRow>
          <TableRow v-else-if="categories.length === 0"><TableCell colspan="7" class="px-6 py-8 text-center text-muted-foreground">{{ t('admin.categories.empty') }}</TableCell></TableRow>
          <TableRow v-for="item in categoryHierarchyItems" :key="item.category.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4"><IdCell :value="item.category.id" /></TableCell>
            <TableCell class="px-6 py-4"><img v-if="item.category.icon" :src="getImageUrl(item.category.icon)" class="h-8 w-8 shrink-0 rounded object-cover" :alt="getLocalizedText(item.category.name)" /><span v-else class="text-xs text-muted-foreground">-</span></TableCell>
            <TableCell class="min-w-[260px] px-6 py-4">
              <div class="space-y-1" :class="item.depth > 0 ? 'pl-6' : ''"><div class="flex items-start gap-2"><span class="inline-flex shrink-0 whitespace-nowrap rounded-full border px-2 py-0.5 text-[11px]" :class="item.depth > 0 ? 'border-sky-200 bg-sky-50 text-sky-700' : 'border-border bg-muted/30 text-muted-foreground'">{{ getCategoryLevelText(item.category) }}</span><span class="min-w-0 break-words font-medium text-foreground">{{ getLocalizedText(item.category.name) }}</span></div><div v-if="item.parent" class="text-xs text-muted-foreground break-words">{{ t('admin.categories.table.parentPrefix', { name: getLocalizedText(item.parent.name) }) }}</div></div>
            </TableCell>
            <TableCell class="min-w-[220px] px-6 py-4 font-mono text-muted-foreground break-all">{{ item.category.slug }}</TableCell>
            <TableCell class="px-6 py-4 font-mono text-muted-foreground">{{ item.category.sort_order }}</TableCell>
            <TableCell class="px-6 py-4"><span class="inline-flex shrink-0 whitespace-nowrap cursor-pointer rounded-full border px-2.5 py-1 text-xs transition-colors" :class="item.category.is_active ? 'text-emerald-700 border-emerald-200 bg-emerald-50 hover:bg-emerald-100' : 'text-muted-foreground border-border bg-muted/30 hover:bg-muted/50'" :title="item.category.is_active ? t('admin.categories.status.clickToDeactivate') : t('admin.categories.status.clickToActivate')" @click="toggleActive(item.category)">{{ t(item.category.is_active ? 'admin.categories.status.active' : 'admin.categories.status.inactive') }}</span></TableCell>
            <TableCell class="min-w-[140px] px-6 py-4 text-right"><div class="flex items-center justify-end gap-2"><Button size="sm" variant="outline" @click="openEditModal(item.category)">{{ t('admin.categories.actions.edit') }}</Button><Button size="sm" variant="destructive" @click="handleDelete(item.category)">{{ t('admin.categories.actions.delete') }}</Button></div></TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>
    <CategoryEditModal v-model:open="showModal" :category="editingCategory" @saved="fetchCategories" />
  </div>
</template>
