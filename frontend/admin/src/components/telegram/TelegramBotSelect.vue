<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { useTelegramBotOptions } from '@/composables/useTelegramBotOptions'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button } from '@/components/ui/button'

const props = withDefaults(defineProps<{ modelValue: string; required?: boolean; disabled?: boolean; id?: string }>(), {
  required: false, disabled: false,
})
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const auth = useAdminAuthStore()
const canBots = computed(() => auth.hasPermission('GET:/admin/telegram-bot/instances'))
const { bots, botsLoading, botsError, loadBots, clearBots } = useTelegramBotOptions(canBots, () => t('telegramBot.instances.failed'))
const unavailable = computed(() => !!props.modelValue && !bots.value.some(bot => bot.id === props.modelValue))
const placeholder = computed(() => t(props.required ? 'telegramSupportSettings.selectBot' : 'telegramBot.groups.all'))

/** 只接受当前授权选项；“全部”映射回既有空字符串筛选。
 * @param value 下拉选择值。@returns 无。
 */
function select(value: unknown) {
  if (props.disabled || botsLoading.value || !canBots.value || botsError.value) return
  if (value === 'all' && !props.required) emit('update:modelValue', '')
  else if (typeof value === 'string' && bots.value.some(bot => bot.id === value)) emit('update:modelValue', value)
}
watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions), canBots.value], () => {
  clearBots()
  emit('update:modelValue', '')
  void loadBots()
}, { flush: 'sync' })
onMounted(() => { void loadBots() })
onBeforeUnmount(clearBots)
</script>

<template>
  <div class="space-y-1">
    <Select :required="required" :model-value="modelValue || (required ? '' : 'all')" :disabled="disabled || botsLoading || !canBots || !!botsError || (required && !bots.length)" @update:model-value="select">
      <SelectTrigger :id="id" class="w-full" :aria-label="t('telegramBot.groups.instance')" :aria-required="required">
        <SelectValue :placeholder="placeholder" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem v-if="!required" value="all">{{ t('telegramBot.groups.all') }}</SelectItem>
        <SelectItem v-if="unavailable" :value="modelValue" disabled>{{ modelValue }} · {{ t('membershipRecovery.values.unavailable') }}</SelectItem>
        <SelectItem v-for="bot in bots" :key="bot.id" :value="bot.id">{{ bot.username ? `@${bot.username.replace(/^@/, '')}` : t('telegramBot.instances.bot') }} · {{ bot.id }}</SelectItem>
      </SelectContent>
    </Select>
    <p v-if="!canBots" class="text-xs text-muted-foreground">{{ t('telegramBot.groups.instancePermission') }}</p>
    <p v-else-if="botsLoading" role="status" class="text-xs text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <div v-else-if="botsError" role="alert" class="flex items-center gap-2 text-xs text-destructive">
      <span>{{ botsError }}</span><Button variant="link" size="sm" :disabled="disabled" @click="loadBots">{{ t('telegramBot.instances.reload') }}</Button>
    </div>
    <p v-else-if="!bots.length" class="text-xs text-muted-foreground">{{ t('telegramBot.instances.empty') }}</p>
  </div>
</template>
