<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { adminAPI } from '@/api/admin'
import type { AdminCategory } from '@/api/types'
import { buildAdminCategoryPath, createAdminCategoryMap, createAdminCategoryChildCountMap, flattenAdminCategories, isAdminProductCategorySelectable } from '@/utils/category'
import { getLocalizedText } from '@/utils/format'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import CategoryEditModal from './CategoryEditModal.vue'

const props = defineProps<{ modelValue: number | null; categories: AdminCategory[]; active: boolean; initialCategoryID?: number | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: number]; refreshed: [categories: AdminCategory[]] }>()
const auth = useAdminAuthStore(), { t } = useI18n()
const rows = ref<AdminCategory[]>([]), creating = ref(false), loading = ref(false), error = ref('')
const pendingID = ref<number | null>(null)
let generation = 0
const canRead = computed(() => auth.hasPermission('GET:/admin/categories'))
const canCreate = computed(() => canRead.value && auth.hasPermission('POST:/admin/categories'))
const categoryMap = computed(() => createAdminCategoryMap(rows.value))
const childCounts = computed(() => createAdminCategoryChildCountMap(rows.value))
const options = computed(() => flattenAdminCategories(rows.value))
/** Refresh just the classification list and select a newly created leaf, never reload a product. @returns Completion promise. */
async function refresh() {
  if (!props.active || !canRead.value) return
  const request = ++generation
  loading.value = true; error.value = ''
  try {
    const response = await adminAPI.getCategories()
    if (request !== generation || !props.active || !canRead.value) return
    rows.value = response.data.data || []
    emit('refreshed', rows.value)
    const created = rows.value.find(item => item.id === pendingID.value)
    if (created && isAdminProductCategorySelectable(created, childCounts.value)) {
      emit('update:modelValue', created.id); pendingID.value = null
    } else if (pendingID.value) error.value = t('productCategory.unavailable')
  } catch { if (request === generation) error.value = t('productCategory.refreshFailed') }
  finally { if (request === generation) loading.value = false }
}
/** Queue a successfully persisted ID for validated selection. @param category API result. @returns Completion promise. */
async function created(category: AdminCategory) { pendingID.value = category.id; await refresh() }
/** Keep previously selected legacy parents selectable, matching the existing website editor. @param category Row. @returns Selectability. */
function selectable(category: AdminCategory) { return isAdminProductCategorySelectable(category, childCounts.value) || category.id === props.initialCategoryID }
/** Invalidate responses on close or identity change. @returns Nothing. */
function invalidate() { generation++; creating.value = false; loading.value = false; error.value = ''; pendingID.value = null }
watch(() => props.categories, value => { rows.value = value }, { immediate: true })
watch(() => props.active, value => { invalidate(); if (value) rows.value = props.categories }, { flush: 'sync' })
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => { invalidate(); rows.value = [] }, { flush: 'sync' })
onScopeDispose(invalidate)
</script>

<template>
  <div class="space-y-1.5">
    <div class="flex items-center justify-between gap-2"><label class="text-xs font-medium text-muted-foreground">{{ t('admin.products.form.category') }}</label><Button v-if="canCreate" type="button" variant="link" class="h-auto p-0 text-xs" :disabled="loading || creating || pendingID !== null" @click="creating = true">{{ t('admin.categories.create') }}</Button></div>
    <Select :model-value="String(modelValue || '__none__')" :disabled="!canRead || loading" @update:model-value="value => emit('update:modelValue', value === '__none__' ? 0 : Number(value))"><SelectTrigger class="h-9 w-full"><SelectValue :placeholder="t('admin.products.form.categoryPlaceholder')" /></SelectTrigger><SelectContent><SelectItem value="__none__">{{ t('admin.products.form.categoryPlaceholder') }}</SelectItem><SelectItem v-for="item in options" :key="item.category.id" :value="String(item.category.id)" :disabled="!selectable(item.category)" :class="item.depth > 0 ? 'pl-6' : ''">{{ buildAdminCategoryPath(item.category, categoryMap, category => getLocalizedText(category.name)) }}</SelectItem></SelectContent></Select>
    <p v-if="!canRead" class="text-xs text-muted-foreground">{{ t('telegramMembership.categoryPermission') }}</p>
    <p v-if="loading" role="status" class="text-xs text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <p v-if="error" role="alert" class="text-xs text-destructive">{{ error }} <Button type="button" variant="link" class="h-auto p-0 text-xs" @click="refresh">{{ t('telegramBot.instances.reload') }}</Button></p>
    <CategoryEditModal :open="creating && active" @update:open="value => creating = value" @saved="created" />
  </div>
</template>
