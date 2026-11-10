<template>
  <form
    class="rounded-xl border bg-card p-4 shadow-sm"
    @submit.prevent="search"
  >
    <fieldset :disabled="loading" class="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <slot name="fields" />
    </fieldset>
    <div class="mt-3 flex flex-wrap items-center justify-end gap-2">
      <Button type="button" variant="outline" size="sm" :disabled="loading" @click="reset">
        <RotateCcw class="h-4 w-4" />
        {{ t('resellerConsole.common.reset') }}
      </Button>
      <Button type="submit" size="sm" :disabled="loading">
        <Search class="h-4 w-4" />
        {{ t('orders.filters.search') }}
      </Button>
    </div>
  </form>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RotateCcw, Search } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'

const props = withDefaults(defineProps<{ loading?: boolean }>(), { loading: false })
const emit = defineEmits<{ (e: 'search'): void; (e: 'reset'): void }>()

/** 提交当前查询；加载时忽略 Enter 和重复提交。@returns 无返回值。 */
function search(): void {
  if (!props.loading) emit('search')
}

/** 请求恢复默认查询条件；加载时保持表单不变。@returns 无返回值。 */
function reset(): void {
  if (!props.loading) emit('reset')
}

const { t } = useI18n()
</script>
