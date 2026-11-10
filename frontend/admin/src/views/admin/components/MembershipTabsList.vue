<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { MembershipAttentionState } from '@/composables/useMembershipAttention'

const props = defineProps<{
  tabs: readonly string[]
  label: string
  states: Partial<Record<string, MembershipAttentionState>>
}>()
const { t } = useI18n()

/** 获取真实异常角标，查询失败或未知数量不得展示为零。
 * @param tab 标签标识。@returns 角标文案及可访问说明，无异常时返回空。
 */
function badge(tab: string) {
  const state = props.states[tab]
  if (!state) return undefined
  if (state.error) return { text: '—', label: t('telegramMembership.tabs.unavailable'), failed: false }
  if (state.loading || state.count === null) return { text: t('telegramMembership.tabs.checking'), label: t('telegramMembership.tabs.checking'), failed: false }
  return state.count > 0 ? { text: String(state.count), label: t('telegramMembership.tabs.attention', { count: state.count }), failed: true } : undefined
}
</script>

<template>
  <div class="max-w-full overflow-x-auto py-1">
    <TabsList class="h-12 w-full min-w-[680px] max-w-[880px] flex-nowrap justify-start gap-1 rounded-md border border-border/60 bg-muted/70 p-1 shadow-inner" :aria-label="label">
      <TabsTrigger v-for="tab in tabs" :key="tab" :value="tab" class="h-full min-w-[160px] flex-1 gap-2 rounded-md px-6 py-2 text-muted-foreground transition-all duration-200 motion-reduce:transition-none hover:text-foreground data-[state=active]:border-border/50 data-[state=active]:bg-background data-[state=active]:text-foreground data-[state=active]:shadow-md dark:data-[state=active]:border-border dark:data-[state=active]:bg-secondary">
        {{ t(`telegramMembership.tabs.${tab}`) }}
        <span v-if="badge(tab)" class="rounded-full border px-1.5 py-0.5 text-xs" :class="badge(tab)?.failed ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-border text-muted-foreground'" :aria-label="badge(tab)?.label" :title="badge(tab)?.label">{{ badge(tab)?.text }}</span>
      </TabsTrigger>
    </TabsList>
  </div>
</template>
