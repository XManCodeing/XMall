<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const props = defineProps<{
  page: number
  totalPage?: number
  total?: number
  /** Cursor mode has no authoritative total or arbitrary page jump. */
  mode?: 'page' | 'cursor'
  hasNext?: boolean
  hasPrevious?: boolean
  loading?: boolean
  disabled?: boolean
  alwaysShow?: boolean
  pageSize?: number
  pageSizeOptions?: number[]
}>()

const emit = defineEmits<{
  changePage: [page: number]
  changePageSize: [pageSize: number]
}>()

const { t } = useI18n()
const jumpPage = ref('')
const normalizedPageSizeOptions = computed(() => {
  const source = Array.isArray(props.pageSizeOptions) ? props.pageSizeOptions : []
  return source
    .map((item) => Number(item))
    .filter((item, index, array) => Number.isFinite(item) && item > 0 && array.indexOf(item) === index)
    .sort((a, b) => a - b)
})
const pageSizeValue = computed(() => {
  const raw = Number(props.pageSize || 0)
  if (!Number.isFinite(raw) || raw <= 0) return ''
  return String(Math.floor(raw))
})
const cursorMode = computed(() => props.mode === 'cursor')
const blocked = computed(() => props.loading || props.disabled)
const lastPage = computed(() => Math.max(1, props.totalPage || 1))
const previousAllowed = computed(() => !blocked.value && (cursorMode.value ? (props.hasPrevious ?? props.page > 1) : props.page > 1))
const nextAllowed = computed(() => !blocked.value && (cursorMode.value ? props.hasNext === true : props.page < lastPage.value))
const shouldShow = computed(() => props.alwaysShow || cursorMode.value || ((props.total || 0) > 0 && (lastPage.value > 1 || normalizedPageSizeOptions.value.length > 0)))

/** Emit only permitted navigation. @param target Requested page. @returns Nothing. */
function changePage(target: number) {
  if (blocked.value || !Number.isSafeInteger(target) || target < 1 || target === props.page) return
  if (cursorMode.value) {
    if (target === props.page - 1 && previousAllowed.value || target === props.page + 1 && nextAllowed.value) emit('changePage', target)
  } else if (target <= lastPage.value) emit('changePage', target)
}
/** Clamp a numeric jump to known pages. @returns Nothing. */
const jumpToPage = () => {
  if (!jumpPage.value || cursorMode.value || blocked.value) return
  const raw = Number(jumpPage.value)
  if (!Number.isFinite(raw)) return
  changePage(Math.min(Math.max(Math.floor(raw), 1), lastPage.value))
}

/** Validate page size selection. @param value Selected size. @returns Nothing. */
const changePageSize = (value: unknown) => {
  const parsed = Number(value)
  if (blocked.value || !Number.isSafeInteger(parsed) || !normalizedPageSizeOptions.value.includes(parsed)) return
  emit('changePageSize', parsed)
}
</script>

<template>
  <div
    v-if="shouldShow"
    class="flex flex-wrap items-center justify-between gap-3 border-t border-border px-6 py-4"
  >
    <div class="flex items-center gap-3">
      <span class="text-xs text-muted-foreground">
        {{ cursorMode ? t('telegramRecovery.page', { page }) : t('admin.common.pageInfo', { total: total ?? 0, page, totalPage: lastPage }) }}
      </span>
      <slot name="actions" />
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <div v-if="normalizedPageSizeOptions.length > 0" class="flex items-center gap-2">
        <span class="text-xs text-muted-foreground">{{ t('admin.common.pageSize') }}</span>
        <Select :disabled="blocked" :model-value="pageSizeValue" @update:modelValue="changePageSize">
          <SelectTrigger class="h-8 w-[92px] text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="size in normalizedPageSizeOptions" :key="size" :value="String(size)">
              {{ size }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-if="!cursorMode" class="flex items-center gap-2">
        <Input
          :disabled="blocked || lastPage <= 1"
          v-model="jumpPage"
          type="number"
          min="1"
          :max="totalPage"
          class="h-8 w-20"
          :placeholder="t('admin.common.jumpPlaceholder')"
        />
        <Button variant="outline" size="sm" class="h-8" :disabled="blocked || lastPage <= 1" @click="jumpToPage">
          {{ t('admin.common.jumpTo') }}
        </Button>
      </div>
      <div class="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          class="h-8"
          :disabled="!previousAllowed"
          @click="changePage(page - 1)"
        >
          {{ t('admin.common.prevPage') }}
        </Button>
        <Button
          variant="outline"
          size="sm"
          class="h-8"
          :disabled="!nextAllowed"
          @click="changePage(page + 1)"
        >
          {{ t('admin.common.nextPage') }}
        </Button>
      </div>
    </div>
  </div>
</template>
