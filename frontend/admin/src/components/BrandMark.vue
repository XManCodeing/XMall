<script setup lang="ts">
import { adminUrl } from '@/utils/adminBase'
</script>

<template>
  <span class="inline-flex shrink-0" aria-hidden="true">
    <img :src="adminUrl('/xmall.svg')" alt="" class="h-full w-full object-contain dark:hidden" />
    <img :src="adminUrl('/xmall-dark.svg')" alt="" class="hidden h-full w-full object-contain dark:block" />
  </span>
</template>
