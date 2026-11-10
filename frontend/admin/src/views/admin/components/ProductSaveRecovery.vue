<script setup lang="ts">
import { Button } from '@/components/ui/button'
import type { ProductSaveRecovery } from '@/composables/useProductSaveRecovery'
defineProps<{ recovery: ProductSaveRecovery }>()
</script>
<template>
  <section v-if="recovery.unknown.value" class="space-y-2 rounded-lg border border-border bg-muted/20 p-3" aria-live="polite">
    <p role="alert" class="text-sm text-destructive">{{ recovery.messages.value.unknown }}</p>
    <p class="text-xs text-muted-foreground">{{ recovery.messages.value.memory }}</p>
    <Button type="button" variant="outline" size="sm" :disabled="recovery.checking.value || !recovery.canCheck.value" @click="recovery.check()">{{ recovery.checking.value ? recovery.messages.value.checking : recovery.messages.value.check }}</Button>
    <p v-if="!recovery.canCheck.value" class="text-sm text-muted-foreground">{{ recovery.messages.value.permission }}</p>
    <p v-if="recovery.notice.value" class="text-sm">{{ recovery.notice.value }}</p>
    <div v-if="recovery.candidate.value" class="flex items-center gap-3 text-sm">
      <span>ID: {{ recovery.candidate.value.id }} · {{ recovery.candidate.value.slug }}</span>
      <Button type="button" variant="outline" size="sm" @click="recovery.openCandidate()">{{ recovery.messages.value.open }}</Button>
    </div>
  </section>
</template>
