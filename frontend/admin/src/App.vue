<script setup lang="ts">
import { watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterView } from 'vue-router'
import NoticeHost from '@/components/NoticeHost.vue'
const { t } = useI18n()
watchEffect(() => {
  document.title = t('admin.brand')
})
</script>

<template>
  <RouterView />
  <NoticeHost />
</template>
