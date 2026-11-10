<script setup lang="ts">
import { computed, onScopeDispose, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminCategory, LocalizedText } from '@/api/types'
import { useAdminAuthStore } from '@/stores/auth'
import { useFormValidation, rules } from '@/composables/useFormValidation'
import { buildAdminCategoryPath, createAdminCategoryMap, flattenAdminCategories } from '@/utils/category'
import { getLocalizedText } from '@/utils/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import MediaPicker from '@/components/admin/MediaPicker.vue'

// Shared category editor extracted from Categories.vue; both product forms use the same hierarchy and payload.
const props = defineProps<{ open: boolean; category?: AdminCategory | null }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [category: AdminCategory] }>()
const auth = useAdminAuthStore()
const { t } = useI18n()
const categories = ref<AdminCategory[]>([]), currentLang = ref('zh-CN')
const loading = ref(false), submitting = ref(false), error = ref('')
const form = reactive({ id: 0, parent_id: 0, name: {} as LocalizedText, slug: '', icon: '', sort_order: 0 })
const { errors, validate, clearErrors } = useFormValidation({ slug: [rules.required('This field is required')], name: [rules.required('This field is required')] })
const canRead = computed(() => auth.hasPermission('GET:/admin/categories'))
const canWrite = computed(() => auth.hasPermission(props.category ? `PUT:/admin/categories/${props.category.id}` : 'POST:/admin/categories'))
const languages = computed(() => ['zh-CN', 'zh-TW', 'en-US'].map((code, index) => ({ code, name: t(`admin.common.lang.${['zhCN', 'zhTW', 'enUS'][index]}`) })))
const categoryMap = computed(() => createAdminCategoryMap(categories.value))
const canChooseParent = computed(() => !props.category || form.parent_id > 0 || !categories.value.some(item => item.parent_id === form.id))
const parentCategoryOptions = computed(() => canChooseParent.value ? flattenAdminCategories(categories.value).filter(item => item.depth === 0 && item.category.id !== form.id).map(item => item.category) : [])
let generation = 0
/** Resolve a category label using the existing hierarchy. @param category Category row. @returns Localized path. */
function categoryPath(category: AdminCategory) { return buildAdminCategoryPath(category, categoryMap.value, item => getLocalizedText(item.name)) }
/** Read parent choices without modifying either product or category draft. @returns Completion promise. */
async function load() {
  const request = ++generation
  if (!props.open || !canRead.value) return
  loading.value = true; error.value = ''
  try {
    const response = await adminAPI.getCategories()
    if (request === generation) categories.value = response.data.data || []
  } catch { if (request === generation) error.value = t('common.api.requestFailed') }
  finally { if (request === generation) loading.value = false }
}
/** Close only this nested editor; invalidate all pending work. @returns Nothing. */
function close() { generation++; submitting.value = false; loading.value = false; emit('update:open', false) }
/** Save the existing category payload once; errors retain every draft field. @returns Completion promise. */
async function submit() {
  if (submitting.value || loading.value || error.value || !props.open || !canRead.value || !canWrite.value) return
  if (!validate({ slug: form.slug, name: form.name['zh-CN'] })) return
  if (form.parent_id && !parentCategoryOptions.value.some(item => item.id === form.parent_id)) return
  const request = generation
  submitting.value = true
  try {
    const payload = { ...form, name: { ...form.name } }
    const response = props.category ? await adminAPI.updateCategory(form.id, payload) : await adminAPI.createCategory(payload)
    if (request !== generation || !props.open || !canRead.value || !canWrite.value) return
    emit('saved', response.data.data)
    close()
  } catch { if (request === generation) error.value = t('common.api.requestFailed') }
  finally { if (request === generation) submitting.value = false }
}
watch(() => props.open, value => {
  generation++; loading.value = false; submitting.value = false
  if (!value) return
  const category = props.category
  Object.assign(form, { id: category?.id || 0, parent_id: category?.parent_id || 0, name: { 'zh-CN': '', 'zh-TW': '', 'en-US': '', ...category?.name }, slug: category?.slug || '', icon: category?.icon || '', sort_order: category?.sort_order || 0 })
  currentLang.value = 'zh-CN'; categories.value = []; clearErrors(); void load()
}, { immediate: true, flush: 'sync' })
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => { close(); categories.value = []; form.name = {}; form.slug = ''; form.icon = '' }, { flush: 'sync' })
onScopeDispose(() => { generation++ })
</script>

<template>
  <Dialog scope="page" :open="open" @update:open="value => { if (!value) close() }">
    <DialogScrollContent class="w-[calc(100vw-1rem)] max-w-lg p-4 sm:p-6" @interact-outside="event => event.preventDefault()">
      <DialogHeader><DialogTitle>{{ t(category ? 'admin.categories.modal.editTitle' : 'admin.categories.modal.createTitle') }}</DialogTitle></DialogHeader>
      <DialogDescription class="sr-only">{{ t('admin.categories.form.parentTip') }}</DialogDescription>
      <form class="space-y-6" data-category-form @submit.stop.prevent="submit">
        <p v-if="loading" role="status">{{ t('telegramBot.instances.loading') }}</p>
        <p v-if="!canRead || !canWrite" role="alert" class="text-sm text-muted-foreground">{{ t('telegramMembership.categoryPermission') }}</p>
        <div v-if="error" role="alert" class="text-sm text-destructive">{{ error }} <Button type="button" variant="link" @click="load">{{ t('telegramBot.instances.reload') }}</Button></div>
        <fieldset :disabled="loading || submitting || !canRead || !canWrite" class="space-y-6">
          <div class="border-b border-border flex gap-4"><button v-for="lang in languages" :key="lang.code" type="button" class="border-b-2 px-3 py-2 text-sm font-medium" :class="currentLang === lang.code ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground'" @click="currentLang = lang.code">{{ lang.name }}</button></div>
          <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.categories.form.name', { lang: languages.find(item => item.code === currentLang)?.name }) }}</label><Input v-model="form.name[currentLang]" :placeholder="t('admin.categories.form.namePlaceholder')" /><p v-if="errors.name" class="text-xs text-destructive mt-1">{{ errors.name }}</p></div>
          <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.categories.form.slug') }}</label><Input v-model="form.slug" :placeholder="t('admin.categories.form.slugPlaceholder')" /><p v-if="errors.slug" class="text-xs text-destructive mt-1">{{ errors.slug }}</p></div>
          <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.categories.form.parent') }}</label>
            <Select :model-value="String(form.parent_id || 0)" @update:model-value="value => form.parent_id = Number(value || 0)"><SelectTrigger class="h-9 w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="0">{{ t('admin.categories.form.parentRoot') }}</SelectItem><SelectItem v-for="parent in parentCategoryOptions" :key="parent.id" :value="String(parent.id)">{{ categoryPath(parent) }}</SelectItem></SelectContent></Select>
            <p class="text-xs text-muted-foreground mt-1">{{ t(canChooseParent ? 'admin.categories.form.parentTip' : 'admin.categories.form.parentLockedTip') }}</p>
          </div>
          <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.categories.form.icon') }}</label><MediaPicker v-model="form.icon" scene="category" /></div>
          <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.categories.form.sortOrder') }}</label><Input v-model.number="form.sort_order" type="number" placeholder="0" /><p class="text-xs text-muted-foreground mt-1">{{ t('admin.categories.form.sortTip') }}</p></div>
        </fieldset>
        <div class="flex justify-end gap-3 border-t border-border pt-6"><Button type="button" variant="outline" @click="close">{{ t('admin.common.cancel') }}</Button><Button type="submit" :disabled="loading || submitting || !!error || !canRead || !canWrite">{{ t(category ? 'admin.categories.actions.saveChanges' : 'admin.categories.actions.createNow') }}</Button></div>
      </form>
    </DialogScrollContent>
  </Dialog>
</template>
