<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { membershipOrderDuration, membershipOrderSnapshot } from '@/utils/membershipOrder'

/** 接收订单项的购买快照，不读取当前会员权益或交付状态。 */
const props = defineProps<{ snapshot?: unknown }>()
const { t } = useI18n()
const purchase = computed(() => membershipOrderSnapshot(props.snapshot))
</script>

<template>
  <div v-if="purchase" class="mt-2 space-y-1 break-words text-xs text-muted-foreground" data-membership-order>
    <div class="font-medium text-foreground">{{ t('membershipOrder.title') }}</div>
    <div class="break-all">{{ t('membershipOrder.beneficiary') }}：{{ purchase.beneficiary_telegram_id }}</div>
    <div>{{ t('membershipOrder.duration') }}：{{ membershipOrderDuration(purchase, t) }}</div>
    <div>{{ t('membershipOrder.snapshotHint') }}</div>
  </div>
</template>
