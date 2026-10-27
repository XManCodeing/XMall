<script setup lang="ts">
import ClassicPersonalCenter from '@/views/PersonalCenter.vue'
import type { PersonalSection } from '@/composables/usePersonalCenter'

withDefaults(defineProps<{ section?: PersonalSection }>(), { section: 'overview' })
</script>

<template>
  <div class="orbit-page orbit-page--personal-center orbit-classic-adapter" :data-orbit-section="section">
    <ClassicPersonalCenter :section="section" />
  </div>
</template>
