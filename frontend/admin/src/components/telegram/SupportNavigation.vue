<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyErrorOnce } from '@/utils/notify'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

/** 复用客服两页的导航；active 指定当前子页，不参与数据查询或任务处理。 */
defineProps<{ active: 'sessions' | 'tasks' }>()
const { t } = useI18n()
const router = useRouter()
const auth = useAdminAuthStore()
const tabs = computed(() => [
  { value: 'sessions', label: 'sessionsTab', permission: 'sessions' },
  { value: 'tasks', label: 'tasksTab', permission: 'downstreams' },
].filter(tab => auth.hasPermission(`GET:/admin/telegram-bot/support/${tab.permission}`)))
/** 按现有权限切换客服子页。value 为标签值，返回导航完成，不触发业务写入。 */
async function navigate(value: string | number) {
  if (!tabs.value.some(tab => tab.value === value)) return
  try { await router.push(`/telegram-bot/support/${value}`) }
  catch (cause) { notifyErrorOnce(cause, t('telegramSupport.failed')) }
}
</script>

<template>
  <Tabs :model-value="active" @update:model-value="navigate">
    <div class="membership-subtabs max-w-full overflow-x-auto">
    <TabsList :aria-label="t('telegramSupport.sessionsTitle')">
      <TabsTrigger v-for="tab in tabs" :key="tab.value" :value="tab.value" class="min-w-[160px] px-6 text-muted-foreground hover:text-foreground">
        {{ t(`telegramSupport.${tab.label}`) }}
      </TabsTrigger>
    </TabsList>
    </div>
  </Tabs>
</template>

<style scoped src="./membership-subtabs.css"></style>
