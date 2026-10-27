<script setup lang="ts">
import ClassicLegal from '@/views/Legal.vue'

defineProps<{ type: 'terms' | 'privacy' }>()
</script>

<template>
  <div class="orbit-page orbit-page--legal orbit-classic-adapter orbit-editorial">
    <ClassicLegal :type="type" />
  </div>
</template>
